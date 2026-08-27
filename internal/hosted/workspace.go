// Package hosted validates and orchestrates Microsoft Foundry Hosted Agent
// deployments through the Azure Developer CLI.
package hosted

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/netcheck"
	agenttools "foundry-agent-manager/internal/tools"

	"gopkg.in/yaml.v3"
)

const (
	AzureYAMLFile        = "azure.yaml"
	MinimumAZDVersion    = "1.27.1"
	RequiredExtension    = "azure.ai.agents"
	RequiredExtensionVer = "1.0.0-beta.8"
	DefaultProtocol      = "invocations"
	DefaultProtocolVer   = "2.0.0"
	DefaultCPU           = "1"
	DefaultMemory        = "2Gi"
	DefaultDependency    = "remote_build"
	ReservedProjectEnv   = "FOUNDRY_PROJECT_ENDPOINT"
	LegacyProjectEnv     = "AZURE_AI_PROJECT_ENDPOINT"
	ToolboxNameEnv       = "TOOLBOX_NAME"
	ToolboxEndpointEnv   = "TOOLBOX_ENDPOINT"
	ToolboxApprovalEnv   = "TOOLBOX_APPROVAL_MODE"
	BingGroundingConnEnv = "BING_GROUNDING_CONNECTION_NAME"
	BingCustomConnEnv    = "BING_CUSTOM_SEARCH_CONNECTION_NAME"
	BingCustomInstEnv    = "BING_CUSTOM_SEARCH_INSTANCE_NAME"
	RAIPolicyEnv         = "RAI_POLICY_ID"
	DefaultRAIPolicyName = "Microsoft.DefaultV2"
	maxReferenceDepth    = 8
	maxReferencedFiles   = 64
	maxReferencedBytes   = 32 << 20
	DeploymentModeCode   = DeploymentMode("code")
	DeploymentModeImage  = DeploymentMode("image")
	DeploymentModeDocker = DeploymentMode("container")
)

var (
	serviceNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	agentNamePattern    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	environmentPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	envNamePattern      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	envReferencePattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
	toolboxNamePattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)
	protocolVerPattern  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	cpuPattern          = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?$`)
	memoryPattern       = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+)?Gi$`)
	supportedRuntimes   = map[string]struct{}{"python_3_13": {}, "python_3_14": {}, "dotnet_10": {}}
	supportedProtocols  = map[string]struct{}{"responses": {}, "invocations": {}, "invocations_ws": {}, "a2a": {}}
	supportedDependency = map[string]struct{}{"remote_build": {}, "bundled": {}}
)

type DeploymentMode string

type Protocol struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

type CodeConfiguration struct {
	Runtime              string   `json:"runtime" yaml:"runtime"`
	EntryPoint           []string `json:"entryPoint" yaml:"entryPoint"`
	DependencyResolution string   `json:"dependencyResolution" yaml:"dependencyResolution"`
}

type Resources struct {
	CPU    string `json:"cpu" yaml:"cpu"`
	Memory string `json:"memory" yaml:"memory"`
}

// RAIPolicy describes the deployment-time content-safety policy for a Hosted Agent.
type RAIPolicy struct {
	PolicyID            string `json:"policyId" yaml:"policyId"`
	UnresolvedReference bool   `json:"unresolvedReference" yaml:"unresolvedReference"`
}

// ToolboxRuntime describes an explicit Hosted runtime Toolbox declaration.
type ToolboxRuntime struct {
	Name                    string `json:"name,omitempty" yaml:"name,omitempty"`
	Endpoint                string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	ApprovalMode            string `json:"approvalMode" yaml:"approvalMode"`
	UnresolvedReference     bool   `json:"unresolvedReference" yaml:"unresolvedReference"`
	RuntimeApprovalRequired bool   `json:"runtimeApprovalRequired" yaml:"runtimeApprovalRequired"`
}

// BingGroundingRuntime describes a Hosted application-level Bing tool declaration.
// Loading and invoking the tool remains application-code responsibility.
type BingGroundingRuntime struct {
	ConnectionName      string `json:"connectionName" yaml:"connectionName"`
	UnresolvedReference bool   `json:"unresolvedReference" yaml:"unresolvedReference"`
}

type BingCustomSearchRuntime struct {
	ConnectionName      string `json:"connectionName" yaml:"connectionName"`
	InstanceName        string `json:"instanceName" yaml:"instanceName"`
	UnresolvedReference bool   `json:"unresolvedReference" yaml:"unresolvedReference"`
}

type Service struct {
	ServiceName      string                   `json:"serviceName" yaml:"serviceName"`
	AgentName        string                   `json:"agentName" yaml:"agentName"`
	Source           string                   `json:"source" yaml:"source"`
	SourceDirectory  string                   `json:"sourceDirectory" yaml:"sourceDirectory"`
	Mode             DeploymentMode           `json:"mode" yaml:"mode"`
	Code             *CodeConfiguration       `json:"code,omitempty" yaml:"code,omitempty"`
	Image            string                   `json:"image,omitempty" yaml:"image,omitempty"`
	Protocols        []Protocol               `json:"protocols" yaml:"protocols"`
	Resources        Resources                `json:"resources" yaml:"resources"`
	RAIPolicy        *RAIPolicy               `json:"raiPolicy,omitempty" yaml:"raiPolicy,omitempty"`
	EnvironmentNames []string                 `json:"environmentNames,omitempty" yaml:"environmentNames,omitempty"`
	Metadata         map[string]interface{}   `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Toolbox          *ToolboxRuntime          `json:"toolbox,omitempty" yaml:"toolbox,omitempty"`
	BingGrounding    *BingGroundingRuntime    `json:"bingGrounding,omitempty" yaml:"bingGrounding,omitempty"`
	BingCustomSearch *BingCustomSearchRuntime `json:"bingCustomSearch,omitempty" yaml:"bingCustomSearch,omitempty"`
	ProjectService   string                   `json:"projectService,omitempty" yaml:"projectService,omitempty"`
	ProjectEndpoint  string                   `json:"projectEndpoint,omitempty" yaml:"projectEndpoint,omitempty"`
	Uses             []string                 `json:"uses,omitempty" yaml:"uses,omitempty"`
	ReferencedFiles  []string                 `json:"referencedFiles,omitempty" yaml:"referencedFiles,omitempty"`
	Environment      map[string]string        `json:"-" yaml:"-"`
}

type Workspace struct {
	Root             string   `json:"root" yaml:"root"`
	AzureYAML        string   `json:"azureYaml" yaml:"azureYaml"`
	Name             string   `json:"name" yaml:"name"`
	Hash             string   `json:"hash" yaml:"hash"`
	HostedServices   []string `json:"hostedServices" yaml:"hostedServices"`
	Selected         Service  `json:"selected" yaml:"selected"`
	ReferencedFiles  []string `json:"referencedFiles,omitempty" yaml:"referencedFiles,omitempty"`
	ExistingProject  bool     `json:"existingProject" yaml:"existingProject"`
	ProvisioningHint string   `json:"provisioningHint" yaml:"provisioningHint"`
	ContractWarnings []string `json:"contractWarnings,omitempty" yaml:"contractWarnings,omitempty"`
}

type documentResolver struct {
	root       string
	files      map[string][]byte
	stack      map[string]bool
	totalBytes int
}

// ValidateEnvironmentName rejects values that could be parsed as another azd
// option when appended as an argument.
func ValidateEnvironmentName(name string) error {
	if name == "" {
		return nil
	}
	if !environmentPattern.MatchString(name) {
		return errs.Config(
			"--environment %q is invalid; use 1-128 letters, digits, dots, underscores, or hyphens and start with a letter or digit",
			name,
		)
	}
	return nil
}

// LoadWorkspace reads and validates one Hosted Agent service from azure.yaml.
// Local $ref files are allowed only when they remain inside the workspace.
func LoadWorkspace(path, selectedService string) (Workspace, error) {
	root, err := resolveWorkspaceRoot(path)
	if err != nil {
		return Workspace{}, err
	}
	if selectedService != "" && !serviceNamePattern.MatchString(selectedService) {
		return Workspace{}, errs.Config("--service %q is not a safe azure.yaml service name", selectedService)
	}

	resolver := &documentResolver{
		root:  root,
		files: make(map[string][]byte),
		stack: make(map[string]bool),
	}
	document, err := resolver.readMapping(AzureYAMLFile, "azure.yaml")
	if err != nil {
		return Workspace{}, err
	}
	services, ok := asMap(document["services"])
	if !ok || len(services) == 0 {
		return Workspace{}, errs.Manifest("azure.yaml must define a non-empty services mapping")
	}

	serviceNames := sortedKeys(services)
	resolvedServices := make(map[string]map[string]any, len(services))
	legacyConfigs := make(map[string]bool, len(services))
	hostedNames := make([]string, 0)
	for _, name := range serviceNames {
		if !serviceNamePattern.MatchString(name) {
			return Workspace{}, errs.Manifest("azure.yaml service name %q is unsafe or invalid", name)
		}
		service, resolveErr := resolver.resolveService(services[name], AzureYAMLFile, 0)
		if resolveErr != nil {
			if errs.IsKind(resolveErr, "security") {
				return Workspace{}, errs.SecurityWrap(resolveErr, "services.%s", name)
			}
			return Workspace{}, errs.ManifestWrap(resolveErr, "services.%s", name)
		}
		service, legacyConfig := effectiveAgentService(service)
		if legacyConfig {
			legacyConfigs[name] = true
		}
		resolvedServices[name] = service
		if getString(service, "host") == "azure.ai.agent" &&
			strings.EqualFold(getString(service, "kind"), "hosted") {
			hostedNames = append(hostedNames, name)
		}
	}
	document["services"] = resolvedServices
	if err := rejectExecutableHooks(document, resolvedServices); err != nil {
		return Workspace{}, err
	}

	if len(hostedNames) == 0 {
		return Workspace{}, errs.Manifest("azure.yaml defines no hosted azure.ai.agent service")
	}
	if selectedService == "" {
		if len(hostedNames) != 1 {
			return Workspace{}, errs.Config(
				"azure.yaml defines %d Hosted Agent services (%s); select one with --service",
				len(hostedNames),
				strings.Join(hostedNames, ", "),
			)
		}
		selectedService = hostedNames[0]
	}
	selectedMap, found := resolvedServices[selectedService]
	if !found || getString(selectedMap, "host") != "azure.ai.agent" ||
		!strings.EqualFold(getString(selectedMap, "kind"), "hosted") {
		return Workspace{}, errs.Config("--service %q is not a hosted azure.ai.agent service", selectedService)
	}

	selected, err := buildService(root, selectedService, selectedMap, resolvedServices)
	if err != nil {
		return Workspace{}, err
	}
	referenced := sortedFileNames(resolver.files)
	selected.ReferencedFiles = append([]string(nil), referenced...)
	existingProject := strings.TrimSpace(selected.ProjectEndpoint) != ""
	hint := "azd deploy will use the selected environment's existing provisioned resources"
	if existingProject {
		hint = "azure.yaml declares an existing Foundry project endpoint; provisioning is not required for agent deployment"
	}

	warnings := contractWarnings(selectedMap, resolvedServices, legacyConfigs[selectedService])

	return Workspace{
		Root:             root,
		AzureYAML:        filepath.Join(root, AzureYAMLFile),
		Name:             getString(document, "name"),
		Hash:             hashFiles(resolver.files),
		HostedServices:   hostedNames,
		Selected:         selected,
		ReferencedFiles:  referenced,
		ExistingProject:  existingProject,
		ProvisioningHint: hint,
		ContractWarnings: warnings,
	}, nil
}

func resolveWorkspaceRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errs.Config("--workspace is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errs.Config("failed to resolve --workspace: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errs.Config("failed to resolve --workspace %q: %v", path, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", errs.Config("failed to inspect --workspace %q: %v", path, err)
	}
	if !info.IsDir() {
		return "", errs.Config("--workspace %q is not a directory", path)
	}
	return filepath.Clean(resolved), nil
}

func (r *documentResolver) readMapping(relative, field string) (map[string]any, error) {
	if remoteReference(relative) {
		return nil, errs.Security("%s: remote references are not allowed: %q", field, relative)
	}
	if err := validateRelativePath(relative, field); err != nil {
		return nil, err
	}
	relative = filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	data, found := r.files[relative]
	if !found {
		if len(r.files) >= maxReferencedFiles {
			return nil, errs.Security(
				"%s: Hosted Agent workspace references more than %d files",
				field,
				maxReferencedFiles,
			)
		}
		var err error
		data, err = netcheck.ReadContainedFile(r.root, filepath.FromSlash(relative), field)
		if err != nil {
			return nil, err
		}
		if r.totalBytes+len(data) > maxReferencedBytes {
			return nil, errs.Security(
				"%s: Hosted Agent workspace YAML references exceed the %d MiB aggregate limit",
				field,
				maxReferencedBytes>>20,
			)
		}
		r.totalBytes += len(data)
		r.files[relative] = append([]byte(nil), data...)
	}

	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, errs.Manifest("%s is not valid YAML or JSON: %v", relative, err)
	}
	normalized, err := normalizeYAML(raw)
	if err != nil {
		return nil, errs.Manifest("%s contains unsupported YAML values: %v", relative, err)
	}
	mapping, ok := normalized.(map[string]any)
	if !ok || mapping == nil {
		return nil, errs.Manifest("%s must contain a mapping at the top level", relative)
	}
	return mapping, nil
}

func (r *documentResolver) resolveService(value any, currentFile string, depth int) (map[string]any, error) {
	service, ok := asMap(value)
	if !ok {
		return nil, errs.Manifest("service definition must be a mapping")
	}
	ref := getString(service, "$ref")
	if ref == "" {
		resolved, err := r.resolveMap(service, currentFile, depth)
		if err != nil {
			return nil, err
		}
		return resolved, nil
	}
	if getString(service, "host") == "" {
		return nil, errs.Manifest("a service using $ref must declare host in azure.yaml")
	}
	referenced, err := r.resolveReference(ref, currentFile, depth)
	if err != nil {
		return nil, err
	}
	for _, field := range []string{"project", "language", "image", "docker"} {
		if _, found := referenced[field]; found {
			return nil, errs.Manifest(
				"a service $ref must not provide core field %q; declare it beside $ref in azure.yaml",
				field,
			)
		}
	}
	resolved := cloneMap(referenced)
	for _, key := range sortedKeys(service) {
		if key == "$ref" {
			continue
		}
		value, resolveErr := r.resolveValue(service[key], currentFile, depth)
		if resolveErr != nil {
			return nil, resolveErr
		}
		resolved[key] = value
	}
	return resolved, nil
}

func (r *documentResolver) resolveValue(value any, currentFile string, depth int) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		return r.resolveMap(typed, currentFile, depth)
	case []any:
		resolved := make([]any, len(typed))
		for i, item := range typed {
			value, err := r.resolveValue(item, currentFile, depth)
			if err != nil {
				return nil, err
			}
			resolved[i] = value
		}
		return resolved, nil
	default:
		return value, nil
	}
}

func (r *documentResolver) resolveMap(document map[string]any, currentFile string, depth int) (map[string]any, error) {
	resolved := make(map[string]any, len(document))
	if rawRef, found := document["$ref"]; found {
		ref, ok := rawRef.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, errs.Manifest("$ref must be a non-empty string")
		}
		referenced, err := r.resolveReference(ref, currentFile, depth)
		if err != nil {
			return nil, err
		}
		resolved = cloneMap(referenced)
	}
	for _, key := range sortedKeys(document) {
		if key == "$ref" {
			continue
		}
		value, err := r.resolveValue(document[key], currentFile, depth)
		if err != nil {
			return nil, err
		}
		resolved[key] = value
	}
	return resolved, nil
}

func (r *documentResolver) resolveReference(ref, currentFile string, depth int) (map[string]any, error) {
	if depth >= maxReferenceDepth {
		return nil, errs.Manifest("local $ref nesting exceeds %d levels", maxReferenceDepth)
	}
	if remoteReference(ref) {
		return nil, errs.Security("remote $ref values are not allowed: %q", ref)
	}
	if err := validateNonAbsolutePath(ref, "$ref"); err != nil {
		return nil, err
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(currentFile), filepath.FromSlash(ref))))
	if err := validateRelativePath(joined, "$ref"); err != nil {
		return nil, err
	}
	if r.stack[joined] {
		return nil, errs.Manifest("cyclic local $ref detected at %q", joined)
	}
	r.stack[joined] = true
	defer delete(r.stack, joined)

	referenced, err := r.readMapping(joined, "$ref")
	if err != nil {
		return nil, err
	}
	return r.resolveMap(referenced, joined, depth+1)
}

func buildService(
	root string,
	serviceName string,
	document map[string]any,
	allServices map[string]map[string]any,
) (Service, error) {
	agentName := getString(document, "name")
	if agentName == "" {
		agentName = serviceName
	}
	if !agentNamePattern.MatchString(agentName) {
		return Service{}, errs.Manifest(
			"services.%s.name %q must be 1-63 alphanumeric or hyphen characters and start and end with an alphanumeric character",
			serviceName,
			agentName,
		)
	}
	if _, found := document["environmentVariables"]; found {
		if _, found := document["env"]; found {
			return Service{}, errs.Manifest(
				"services.%s must not define both env and environmentVariables",
				serviceName,
			)
		}
	}
	if _, found := document["remoteBuild"]; found {
		return Service{}, errs.Manifest(
			"services.%s.remoteBuild is not a Hosted Agent setting; use codeConfiguration.dependencyResolution",
			serviceName,
		)
	}

	source := getString(document, "project")
	if source == "" {
		return Service{}, errs.Manifest("services.%s.project is required", serviceName)
	}
	sourceDirectory, sourceRoot, err := openContainedDirectory(root, source, "services."+serviceName+".project")
	if err != nil {
		return Service{}, err
	}
	defer sourceRoot.Close()

	codeDocument, hasCode := asMap(document["codeConfiguration"])
	image := getString(document, "image")
	if hasCode && image != "" {
		return Service{}, errs.Manifest(
			"services.%s cannot define both codeConfiguration and image",
			serviceName,
		)
	}

	mode := DeploymentModeDocker
	var code *CodeConfiguration
	switch {
	case hasCode:
		mode = DeploymentModeCode
		parsed, parseErr := parseCodeConfiguration(serviceName, codeDocument)
		if parseErr != nil {
			return Service{}, parseErr
		}
		code = &parsed
	case image != "":
		mode = DeploymentModeImage
		if err := validateImage(image, serviceName); err != nil {
			return Service{}, err
		}
	default:
		dockerfile, contextDirectory, dockerErr := dockerPaths(serviceName, document["docker"])
		if dockerErr != nil {
			return Service{}, dockerErr
		}
		if err := requireRegularFile(sourceRoot, dockerfile, "services."+serviceName+".project"); err != nil {
			return Service{}, err
		}
		contextRoot, err := sourceRoot.OpenRoot(contextDirectory)
		if err != nil {
			return Service{}, errs.Security(
				"services.%s.docker.context %q is not an accessible directory contained by the agent source: %v",
				serviceName,
				contextDirectory,
				err,
			)
		}
		_ = contextRoot.Close()
	}

	protocols, err := parseProtocols(serviceName, document["protocols"])
	if err != nil {
		return Service{}, err
	}
	resources, err := parseResources(serviceName, document)
	if err != nil {
		return Service{}, err
	}
	raiPolicy, err := parseRAIPolicy(serviceName, document["policies"])
	if err != nil {
		return Service{}, err
	}
	envNames, err := validateEnvironment(serviceName, document["env"])
	if err != nil {
		return Service{}, err
	}
	environment := environmentValues(document["env"])
	if len(envNames) == 0 {
		envNames, err = validateEnvironmentVariables(serviceName, document["environmentVariables"])
		if err != nil {
			return Service{}, err
		}
		environment = environmentVariableValues(document["environmentVariables"])
	}
	var metadata map[string]interface{}
	if rawMetadata, present := document["metadata"]; present && rawMetadata != nil {
		metadataObject, ok := rawMetadata.(map[string]interface{})
		if !ok {
			return Service{}, errs.Manifest("services.%s.metadata must be an object", serviceName)
		}
		metadata, err = custommetadata.HostedMap(metadataObject)
		if err != nil {
			return Service{}, errs.Manifest("services.%s.metadata is invalid: %v", serviceName, err)
		}
	}
	uses, err := stringSlice(document["uses"], "services."+serviceName+".uses")
	if err != nil {
		return Service{}, err
	}
	projectService, projectEndpoint, err := selectProjectService(serviceName, uses, allServices)
	if err != nil {
		return Service{}, err
	}
	if projectEndpoint != "" {
		if err := validateProjectEndpoint(projectEndpoint, "services."+projectService+".endpoint"); err != nil {
			return Service{}, err
		}
	}
	toolbox, err := parseToolboxRuntime(serviceName, document, projectEndpoint)
	if err != nil {
		return Service{}, err
	}
	bingGrounding, err := parseBingGroundingRuntime(serviceName, document)
	if err != nil {
		return Service{}, err
	}
	bingCustomSearch, err := parseBingCustomSearchRuntime(serviceName, document)
	if err != nil {
		return Service{}, err
	}

	return Service{
		ServiceName:      serviceName,
		AgentName:        agentName,
		Source:           filepath.ToSlash(filepath.Clean(filepath.FromSlash(source))),
		SourceDirectory:  sourceDirectory,
		Mode:             mode,
		Code:             code,
		Image:            image,
		Protocols:        protocols,
		Resources:        resources,
		RAIPolicy:        raiPolicy,
		EnvironmentNames: envNames,
		Metadata:         metadata,
		Toolbox:          toolbox,
		BingGrounding:    bingGrounding,
		BingCustomSearch: bingCustomSearch,
		ProjectService:   projectService,
		ProjectEndpoint:  projectEndpoint,
		Uses:             uses,
		Environment:      environment,
	}, nil
}

func parseRAIPolicy(serviceName string, value any) (*RAIPolicy, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) != 1 {
		return nil, errs.Manifest(
			"services.%s.policies must contain exactly one rai_policy entry",
			serviceName,
		)
	}
	policy, ok := asMap(items[0])
	if !ok {
		return nil, errs.Manifest("services.%s.policies[0] must be an object", serviceName)
	}
	if !strings.EqualFold(getString(policy, "type"), "rai_policy") {
		return nil, errs.Manifest(
			"services.%s.policies[0].type must be rai_policy",
			serviceName,
		)
	}
	policyID := strings.TrimSpace(getString(policy, "raiPolicyName"))
	if policyID == "" {
		return nil, errs.Manifest(
			"services.%s.policies[0].raiPolicyName is required",
			serviceName,
		)
	}
	if policyID == "${RAI_POLICY_ID}" {
		return &RAIPolicy{PolicyID: policyID, UnresolvedReference: true}, nil
	}
	parsed, err := foundryid.ParseRAIPolicyID(policyID)
	if err != nil {
		return nil, errs.Manifest(
			"services.%s.policies[0].raiPolicyName is invalid: %v",
			serviceName,
			err,
		)
	}
	return &RAIPolicy{PolicyID: parsed.String()}, nil
}

func parseToolboxRuntime(
	serviceName string,
	document map[string]any,
	projectEndpoint string,
) (*ToolboxRuntime, error) {
	name, hasName := environmentSetting(document, ToolboxNameEnv)
	endpoint, hasEndpoint := environmentSetting(document, ToolboxEndpointEnv)
	_, hasApprovalMode := environmentSetting(document, ToolboxApprovalEnv)
	if !hasName && !hasEndpoint {
		if hasApprovalMode {
			return nil, errs.Manifest(
				"services.%s.env.%s requires %s or %s",
				serviceName,
				ToolboxApprovalEnv,
				ToolboxNameEnv,
				ToolboxEndpointEnv,
			)
		}
		return nil, nil
	}
	if hasName && hasEndpoint {
		return nil, errs.Manifest(
			"services.%s.env must declare only one of %s or %s",
			serviceName,
			ToolboxNameEnv,
			ToolboxEndpointEnv,
		)
	}
	approvalMode := "always_require"
	if configured, found := environmentSetting(document, ToolboxApprovalEnv); found {
		if envReferencePattern.MatchString(configured) {
			approvalMode = configured
		} else if configured != "always_require" && configured != "never_require" {
			return nil, errs.Manifest(
				"services.%s.env.%s must be always_require, never_require, or an azd variable reference",
				serviceName,
				ToolboxApprovalEnv,
			)
		} else {
			approvalMode = configured
		}
	}
	result := &ToolboxRuntime{
		ApprovalMode:            approvalMode,
		RuntimeApprovalRequired: approvalMode != "never_require",
	}
	if hasName {
		if envReferencePattern.MatchString(name) {
			result.Name = name
			result.UnresolvedReference = true
			return result, nil
		}
		if !toolboxNamePattern.MatchString(name) {
			return nil, errs.Manifest(
				"services.%s.env.%s %q is not a valid Toolbox name or azd variable reference",
				serviceName,
				ToolboxNameEnv,
				name,
			)
		}
		result.Name = name
		return result, nil
	}
	if envReferencePattern.MatchString(endpoint) {
		result.Endpoint = endpoint
		result.UnresolvedReference = true
		return result, nil
	}
	if strings.TrimSpace(projectEndpoint) == "" {
		return nil, errs.Manifest(
			"services.%s.env.%s must use an azd variable reference when the workspace does not declare the Foundry project endpoint",
			serviceName,
			ToolboxEndpointEnv,
		)
	}
	if !agenttools.IsProjectToolboxEndpoint(endpoint, projectEndpoint) {
		return nil, errs.Security(
			"services.%s.env.%s must be a same-project Toolbox MCP endpoint ending in ?api-version=v1",
			serviceName,
			ToolboxEndpointEnv,
		)
	}
	result.Endpoint = endpoint
	return result, nil
}

func parseBingGroundingRuntime(
	serviceName string,
	document map[string]any,
) (*BingGroundingRuntime, error) {
	connectionName, found := environmentSetting(document, BingGroundingConnEnv)
	if !found {
		return nil, nil
	}
	if envReferencePattern.MatchString(connectionName) {
		return &BingGroundingRuntime{
			ConnectionName:      connectionName,
			UnresolvedReference: true,
		}, nil
	}
	if !validBingGroundingConnectionName(connectionName) {
		return nil, errs.Manifest(
			"services.%s.env.%s must be a non-empty project connection name or azd variable reference",
			serviceName,
			BingGroundingConnEnv,
		)
	}
	return &BingGroundingRuntime{ConnectionName: connectionName}, nil
}

func validBingGroundingConnectionName(value string) bool {
	return validBingConnectionValue(value)
}

func parseBingCustomSearchRuntime(
	serviceName string,
	document map[string]any,
) (*BingCustomSearchRuntime, error) {
	connectionName, hasConnection := environmentSetting(document, BingCustomConnEnv)
	instanceName, hasInstance := environmentSetting(document, BingCustomInstEnv)
	if !hasConnection && !hasInstance {
		return nil, nil
	}
	if !hasConnection || !hasInstance {
		return nil, errs.Manifest(
			"services.%s.env must declare both %s and %s",
			serviceName,
			BingCustomConnEnv,
			BingCustomInstEnv,
		)
	}
	connectionReference := envReferencePattern.MatchString(connectionName)
	instanceReference := envReferencePattern.MatchString(instanceName)
	if !connectionReference && !validBingConnectionValue(connectionName) {
		return nil, errs.Manifest(
			"services.%s.env.%s must be a non-empty project connection name or azd variable reference",
			serviceName,
			BingCustomConnEnv,
		)
	}
	if !instanceReference && !validBingConnectionValue(instanceName) {
		return nil, errs.Manifest(
			"services.%s.env.%s must be a non-empty instance name or azd variable reference",
			serviceName,
			BingCustomInstEnv,
		)
	}
	return &BingCustomSearchRuntime{
		ConnectionName:      connectionName,
		InstanceName:        instanceName,
		UnresolvedReference: connectionReference || instanceReference,
	}, nil
}

func validBingConnectionValue(value string) bool {
	return value != "" &&
		value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\r\n")
}

func environmentSetting(document map[string]any, name string) (string, bool) {
	if env, ok := asMap(document["env"]); ok {
		value, found := env[name]
		if !found {
			return "", false
		}
		text, _ := value.(string)
		return text, true
	}
	items, _ := document["environmentVariables"].([]any)
	for _, item := range items {
		entry, ok := asMap(item)
		if ok && getString(entry, "name") == name {
			return getString(entry, "value"), true
		}
	}
	return "", false
}

func parseCodeConfiguration(serviceName string, document map[string]any) (CodeConfiguration, error) {
	runtime := getString(document, "runtime")
	if _, ok := supportedRuntimes[runtime]; !ok {
		return CodeConfiguration{}, errs.Manifest(
			"services.%s.codeConfiguration.runtime %q is unsupported; use python_3_13, python_3_14, or dotnet_10",
			serviceName,
			runtime,
		)
	}
	entryPoint, err := entryPointArgs(document["entryPoint"])
	if err != nil {
		return CodeConfiguration{}, errs.ManifestWrap(
			err,
			"services.%s.codeConfiguration.entryPoint",
			serviceName,
		)
	}
	dependency := getString(document, "dependencyResolution")
	if dependency == "" {
		dependency = DefaultDependency
	}
	if _, ok := supportedDependency[dependency]; !ok {
		return CodeConfiguration{}, errs.Manifest(
			"services.%s.codeConfiguration.dependencyResolution %q is unsupported; use remote_build or bundled",
			serviceName,
			dependency,
		)
	}
	return CodeConfiguration{
		Runtime:              runtime,
		EntryPoint:           entryPoint,
		DependencyResolution: dependency,
	}, nil
}

func entryPointArgs(value any) ([]string, error) {
	entryPoint, ok := value.(string)
	if !ok || strings.TrimSpace(entryPoint) == "" || strings.ContainsRune(entryPoint, '\x00') {
		return nil, errs.Manifest(
			"must be a non-empty string for the pinned azure.ai.agents extension",
		)
	}
	return []string{entryPoint}, nil
}

func parseProtocols(serviceName string, value any) ([]Protocol, error) {
	if value == nil {
		return []Protocol{{Name: DefaultProtocol, Version: DefaultProtocolVer}}, nil
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil, errs.Manifest("services.%s.protocols must be a non-empty array", serviceName)
	}
	result := make([]Protocol, 0, len(items))
	seen := make(map[string]bool)
	for i, item := range items {
		document, ok := asMap(item)
		if !ok {
			return nil, errs.Manifest("services.%s.protocols[%d] must be a mapping", serviceName, i)
		}
		name := strings.ToLower(getString(document, "protocol"))
		if _, ok := supportedProtocols[name]; !ok {
			return nil, errs.Manifest(
				"services.%s.protocols[%d].protocol %q is unsupported",
				serviceName,
				i,
				name,
			)
		}
		if seen[name] {
			return nil, errs.Manifest("services.%s.protocols contains duplicate protocol %q", serviceName, name)
		}
		version := getString(document, "version")
		if !protocolVerPattern.MatchString(version) {
			return nil, errs.Manifest(
				"services.%s.protocols[%d].version %q must be a semantic version",
				serviceName,
				i,
				version,
			)
		}
		seen[name] = true
		result = append(result, Protocol{Name: name, Version: version})
	}
	return result, nil
}

func parseResources(serviceName string, document map[string]any) (Resources, error) {
	resources := Resources{CPU: DefaultCPU, Memory: DefaultMemory}
	container, ok := asMap(document["container"])
	if !ok {
		if direct, directOK := asMap(document["resources"]); directOK {
			container = map[string]any{"resources": direct}
			ok = true
		}
	}
	if !ok {
		return resources, nil
	}
	resourceDocument, ok := asMap(container["resources"])
	if !ok {
		return resources, nil
	}
	if cpu := scalarString(resourceDocument["cpu"]); cpu != "" {
		if !cpuPattern.MatchString(cpu) {
			return Resources{}, errs.Manifest(
				"services.%s.container.resources.cpu %q must be a decimal CPU value",
				serviceName,
				cpu,
			)
		}
		value, err := strconv.ParseFloat(cpu, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.25 || value > 4 {
			return Resources{}, errs.Manifest(
				"services.%s.container.resources.cpu %q must be between 0.25 and 4",
				serviceName,
				cpu,
			)
		}
		resources.CPU = cpu
	}
	if memory := scalarString(resourceDocument["memory"]); memory != "" {
		if !memoryPattern.MatchString(memory) {
			return Resources{}, errs.Manifest(
				"services.%s.container.resources.memory %q must be a decimal value using Gi units",
				serviceName,
				memory,
			)
		}
		value, err := strconv.ParseFloat(strings.TrimSuffix(memory, "Gi"), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0.5 || value > 8 {
			return Resources{}, errs.Manifest(
				"services.%s.container.resources.memory %q must be between 0.5Gi and 8Gi",
				serviceName,
				memory,
			)
		}
		resources.Memory = memory
	}
	return resources, nil
}

func contractWarnings(
	selected map[string]any,
	services map[string]map[string]any,
	legacyConfig bool,
) []string {
	warnings := make([]string, 0)
	if _, found := selected["environmentVariables"]; found {
		warnings = append(
			warnings,
			"environmentVariables is a deprecated agent-definition shape; migrate the selected service to the azure.yaml env mapping",
		)
	}
	for _, protocol := range protocolNames(selected["protocols"]) {
		if protocol == "invocations_ws" {
			warnings = append(
				warnings,
				"the pinned azure.ai.agents extension supports invocations_ws, but the current azure.yaml Learn reference omits it from the protocol table",
			)
			break
		}
	}
	if legacyConfig {
		warnings = append(
			warnings,
			"the selected agent uses the deprecated config-nested azure.ai.agent shape; re-run azd ai agent init to migrate it to inline azure.yaml fields",
		)
	}
	for _, service := range services {
		if getString(service, "host") == "azure.ai.project" {
			if _, found := service["network"]; found {
				warnings = append(
					warnings,
					"first-party private-networking schemas currently disagree; Foundry Agent Manager passes the project network block to the pinned azd extension without interpreting it",
				)
				break
			}
		}
	}
	return warnings
}

func validateEnvironment(serviceName string, value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	document, ok := asMap(value)
	if !ok {
		return nil, errs.Manifest("services.%s.env must be a mapping", serviceName)
	}
	names := sortedKeys(document)
	for _, name := range names {
		if !envNamePattern.MatchString(name) {
			return nil, errs.Manifest("services.%s.env contains invalid variable name %q", serviceName, name)
		}
		if strings.EqualFold(name, ReservedProjectEnv) {
			return nil, errs.Manifest(
				"services.%s.env must not declare %s because Foundry injects it",
				serviceName,
				ReservedProjectEnv,
			)
		}
		value, ok := document[name].(string)
		if !ok {
			return nil, errs.Manifest("services.%s.env.%s must be a string", serviceName, name)
		}
		if strings.ContainsRune(value, '\x00') {
			return nil, errs.Manifest("services.%s.env.%s contains a NUL byte", serviceName, name)
		}
	}
	return names, nil
}

func validateEnvironmentVariables(serviceName string, value any) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errs.Manifest(
			"services.%s.environmentVariables must be an array of name/value mappings",
			serviceName,
		)
	}
	names := make([]string, 0, len(items))
	seen := make(map[string]bool, len(items))
	for i, item := range items {
		document, ok := asMap(item)
		if !ok {
			return nil, errs.Manifest(
				"services.%s.environmentVariables[%d] must be a mapping",
				serviceName,
				i,
			)
		}
		name := getString(document, "name")
		if !envNamePattern.MatchString(name) {
			return nil, errs.Manifest(
				"services.%s.environmentVariables[%d].name %q is invalid",
				serviceName,
				i,
				name,
			)
		}
		if strings.EqualFold(name, ReservedProjectEnv) {
			return nil, errs.Manifest(
				"services.%s.environmentVariables must not declare %s because Foundry injects it",
				serviceName,
				ReservedProjectEnv,
			)
		}
		if seen[name] {
			return nil, errs.Manifest(
				"services.%s.environmentVariables contains duplicate variable %q",
				serviceName,
				name,
			)
		}
		value, ok := document["value"].(string)
		if !ok || strings.ContainsRune(value, '\x00') {
			return nil, errs.Manifest(
				"services.%s.environmentVariables[%d].value must be a string without NUL bytes",
				serviceName,
				i,
			)
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func environmentValues(value any) map[string]string {
	document, ok := asMap(value)
	if !ok || len(document) == 0 {
		return nil
	}
	values := make(map[string]string, len(document))
	for name, value := range document {
		text, _ := value.(string)
		values[name] = text
	}
	return values
}

func environmentVariableValues(value any) map[string]string {
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	values := make(map[string]string, len(items))
	for _, item := range items {
		document, ok := asMap(item)
		if !ok {
			continue
		}
		values[getString(document, "name")] = getString(document, "value")
	}
	return values
}

func selectProjectService(
	serviceName string,
	uses []string,
	services map[string]map[string]any,
) (string, string, error) {
	candidates := make([]string, 0)
	for _, name := range uses {
		service, ok := services[name]
		if !ok {
			return "", "", errs.Manifest(
				"services.%s.uses references undefined service %q",
				serviceName,
				name,
			)
		}
		if getString(service, "host") == "azure.ai.project" {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		for name, service := range services {
			if getString(service, "host") == "azure.ai.project" {
				candidates = append(candidates, name)
			}
		}
		sort.Strings(candidates)
	}
	if len(candidates) > 1 {
		return "", "", errs.Manifest(
			"services.%s references multiple azure.ai.project services (%s); use exactly one project dependency",
			serviceName,
			strings.Join(candidates, ", "),
		)
	}
	if len(candidates) == 0 {
		return "", "", nil
	}
	name := candidates[0]
	return name, getString(services[name], "endpoint"), nil
}

func openContainedDirectory(root, relative, field string) (string, *os.Root, error) {
	if err := validateRelativePath(relative, field); err != nil {
		return "", nil, err
	}
	base, err := os.OpenRoot(root)
	if err != nil {
		return "", nil, errs.Security("%s: failed to open workspace safely: %v", field, err)
	}
	defer base.Close()
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	sourceRoot, err := base.OpenRoot(clean)
	if err != nil {
		return "", nil, errs.Security(
			"%s: %q is not an accessible directory contained by the workspace: %v",
			field,
			relative,
			err,
		)
	}
	resolved := filepath.Join(root, filepath.FromSlash(clean))
	if real, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = real
	}
	return filepath.Clean(resolved), sourceRoot, nil
}

func requireRegularFile(root *os.Root, relative, field string) error {
	info, err := root.Stat(relative)
	if err != nil {
		return errs.Manifest("%s must contain %s for container deployment", field, relative)
	}
	if !info.Mode().IsRegular() {
		return errs.Manifest("%s/%s must be a regular file", field, relative)
	}
	return nil
}

func dockerPaths(serviceName string, value any) (string, string, error) {
	dockerfile := "Dockerfile"
	contextDirectory := "."
	if value == nil {
		return dockerfile, contextDirectory, nil
	}
	document, ok := asMap(value)
	if !ok {
		return "", "", errs.Manifest("services.%s.docker must be a mapping", serviceName)
	}
	if configured := getString(document, "path"); configured != "" {
		dockerfile = configured
	}
	if configured := getString(document, "context"); configured != "" {
		contextDirectory = configured
	}
	if err := validateRelativePath(dockerfile, "services."+serviceName+".docker.path"); err != nil {
		return "", "", err
	}
	if err := validateRelativePath(contextDirectory, "services."+serviceName+".docker.context"); err != nil {
		return "", "", err
	}
	return filepath.FromSlash(dockerfile), filepath.FromSlash(contextDirectory), nil
}

func validateImage(image, serviceName string) error {
	if strings.TrimSpace(image) == "" || strings.ContainsRune(image, '\x00') {
		return errs.Manifest("services.%s.image must be a non-empty container image reference", serviceName)
	}
	if strings.ContainsAny(image, "\r\n\t ") {
		return errs.Manifest("services.%s.image must not contain whitespace", serviceName)
	}
	return nil
}

func validateRelativePath(relative, field string) error {
	if strings.TrimSpace(relative) == "" || strings.ContainsRune(relative, '\x00') {
		return errs.Security("%s must be a non-empty relative path", field)
	}
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	isDrivePath := len(cleaned) >= 2 && cleaned[1] == ':'
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, `\`) || strings.HasPrefix(cleaned, "/") || isDrivePath {
		return errs.Security("%s: %q must be relative to the Hosted Agent workspace", field, relative)
	}
	for _, part := range strings.FieldsFunc(filepath.ToSlash(cleaned), func(r rune) bool { return r == '/' }) {
		if part == ".." {
			return errs.Security("%s: %q must not traverse outside the Hosted Agent workspace", field, relative)
		}
	}
	return nil
}

func validateNonAbsolutePath(relative, field string) error {
	if strings.TrimSpace(relative) == "" || strings.ContainsRune(relative, '\x00') {
		return errs.Security("%s must be a non-empty relative path", field)
	}
	cleaned := filepath.Clean(filepath.FromSlash(relative))
	isDrivePath := len(cleaned) >= 2 && cleaned[1] == ':'
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, `\`) || strings.HasPrefix(cleaned, "/") || isDrivePath {
		return errs.Security("%s: %q must be relative to the Hosted Agent workspace", field, relative)
	}
	return nil
}

func remoteReference(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.Contains(lower, "://") || strings.HasPrefix(lower, "//")
}

func stringSlice(value any, field string) ([]string, error) {
	if value == nil {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errs.Manifest("%s must be an array of strings", field)
	}
	result := make([]string, len(items))
	for i, item := range items {
		text, ok := item.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return nil, errs.Manifest("%s[%d] must be a non-empty string", field, i)
		}
		result[i] = text
	}
	return result, nil
}

func normalizeYAML(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			text, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("mapping key %v is not a string", key)
			}
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[text] = normalized
		}
		return result, nil
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeYAML(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func hashFiles(files map[string][]byte) string {
	hash := sha256.New()
	for _, name := range sortedFileNames(files) {
		_, _ = hash.Write([]byte(name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(files[name])
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func sortedFileNames(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedKeys(document map[string]any) []string {
	keys := make([]string, 0, len(document))
	for key := range document {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func asMap(value any) (map[string]any, bool) {
	document, ok := value.(map[string]any)
	return document, ok
}

func getString(document map[string]any, key string) string {
	value, _ := document[key].(string)
	return strings.TrimSpace(value)
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func effectiveAgentService(service map[string]any) (map[string]any, bool) {
	if getString(service, "host") != "azure.ai.agent" || getString(service, "kind") != "" {
		return service, false
	}
	config, ok := asMap(service["config"])
	if !ok || getString(config, "kind") == "" {
		return service, false
	}
	effective := cloneMap(config)
	for key, value := range service {
		if key != "config" {
			effective[key] = value
		}
	}
	return effective, true
}

func rejectExecutableHooks(document map[string]any, services map[string]map[string]any) error {
	if _, found := document["hooks"]; found {
		return errs.Security(
			"azure.yaml hooks are not supported because azd would execute workspace-controlled commands on the local machine",
		)
	}
	for name, service := range services {
		if _, found := service["hooks"]; found {
			return errs.Security(
				"services.%s.hooks is not supported because azd would execute workspace-controlled commands on the local machine",
				name,
			)
		}
	}
	return nil
}

func validateProjectEndpoint(endpoint, field string) error {
	validated, err := netcheck.ValidateFoundryEndpointForSuffixes(
		endpoint,
		field,
		[]string{"services.ai.azure.com"},
	)
	if err != nil {
		return err
	}
	parsed, err := url.Parse(validated)
	if err != nil {
		return errs.Security("%s: invalid Foundry project endpoint: %v", field, err)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errs.Security("%s: Foundry project endpoint must not contain a query or fragment", field)
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return errs.Security("%s: Foundry project endpoint must use the default HTTPS port", field)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "services.ai.azure.com" {
		return errs.Security("%s: Foundry project endpoint must include an account subdomain", field)
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 3 || segments[0] != "api" || segments[1] != "projects" || segments[2] == "" {
		return errs.Security(
			"%s: expected a Foundry project endpoint path in the form /api/projects/<project>",
			field,
		)
	}
	projectName, err := url.PathUnescape(segments[2])
	if err != nil || projectName == "." || projectName == ".." ||
		strings.ContainsAny(projectName, `/\`) ||
		strings.IndexFunc(projectName, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errs.Security("%s: Foundry project endpoint contains an invalid project name", field)
	}
	return nil
}

func protocolNames(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if document, ok := asMap(item); ok {
			result = append(result, strings.ToLower(getString(document, "protocol")))
		}
	}
	return result
}

func cloneMap(document map[string]any) map[string]any {
	clone := make(map[string]any, len(document))
	for key, value := range document {
		clone[key] = value
	}
	return clone
}
