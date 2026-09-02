// Package config loads, validates, and resolves Foundry Agent Manager manifests.
package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/netcheck"
	manifestschema "foundry-agent-manager/schema"
)

const (
	DefaultProjectAPIVersion    = "2025-06-01"
	DefaultConnectionAPIVersion = "2025-04-01-preview"
)

var (
	Version     = "0.16.2"
	BuildCommit = ""
	BuildDate   = ""
)

// AgentSpec holds the resolved Foundry agent definition.
type AgentSpec struct {
	Name               string
	Instructions       string
	Model              string
	Description        string
	RAIPolicyID        string
	StructuredInputs   map[string]interface{}
	Metadata           map[string]string
	MetadataConfigured bool
}

// AgentCardSkillSpec describes one consumer-facing capability advertised by
// the stable agent endpoint.
type AgentCardSkillSpec struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
}

// AgentCardSpec describes the optional card exposed to consumers and A2A
// discovery.
type AgentCardSpec struct {
	Version     string
	Description string
	Skills      []AgentCardSkillSpec
}

// EndpointSpec contains desired stable-endpoint properties. Version routing is
// intentionally excluded: deploy stages versions and promote/rollback own the
// selector.
type EndpointSpec struct {
	Configured           bool
	Protocols            []string
	AuthorizationSchemes []string
	AgentCard            AgentCardSpec
}

// ProjectSpec holds the resolved Foundry project coordinates.
type ProjectSpec struct {
	ResourceID      string
	Name            string
	Endpoint        string
	AccountEndpoint string
	AccountName     string
	ResourceGroup   string
	SubscriptionID  string
	Location        string
	DisplayName     string
	Description     string
	APIVersion      string
	ARMEndpoint     string
	ARMScope        string
	AllowedRegions  []string
}

// ModelDeploymentSpec describes an optional account-level model deployment.
type ModelDeploymentSpec struct {
	Configured              bool
	DeploymentName          string
	ModelName               string
	ModelVersion            string
	ModelFormat             string
	SKUName                 string
	Capacity                int
	RAIPolicyName           string
	VersionUpgradeOption    string
	SpilloverDeploymentName string
}

// ApimSpec holds the optional APIM connection configuration.
type ApimSpec struct {
	Enabled              bool
	ConnectionName       string
	Target               string
	GatewayURL           string
	APIPath              string
	APIName              string
	Auth                 string
	Audience             string
	DeploymentInPath     bool
	InferenceAPIVersion  string
	IsSharedToAll        bool
	Models               []string
	ConnectionAPIVersion string
	AllowedSuffixes      []string
	BlockedAudienceHosts []string
}

// ResolvedTarget returns the full APIM target URL.
func (a *ApimSpec) ResolvedTarget() string {
	if a.Target != "" {
		return strings.TrimRight(a.Target, "/")
	}
	if a.GatewayURL != "" && a.APIPath != "" {
		return strings.TrimRight(a.GatewayURL, "/") + "/" + strings.Trim(a.APIPath, "/")
	}
	return ""
}

// ValidateResolvedTarget validates the effective target URL, not only the
// individual manifest fields it was assembled from.
func (a *ApimSpec) ValidateResolvedTarget() (string, error) {
	target := a.ResolvedTarget()
	if target == "" {
		return "", errs.Config("apim.target is unresolved: set apim.target, or apim.gateway_url + apim.api_path.")
	}
	if len(a.AllowedSuffixes) > 0 {
		return netcheck.ValidateAPIMTargetForSuffixes(target, "apim.target", a.AllowedSuffixes)
	}
	return netcheck.ValidateAPIMTarget(target, "apim.target")
}

// ARMAuthType maps the manifest auth value to the connection ARM authType.
func (a *ApimSpec) ARMAuthType() string {
	if a.Auth == "api_key" {
		return "ApiKey"
	}
	return "ProjectManagedIdentity"
}

// ResolvedConfig holds all resolved deployment inputs.
type ResolvedConfig struct {
	Agent           AgentSpec
	Endpoint        EndpointSpec
	Project         ProjectSpec
	ModelDeployment ModelDeploymentSpec
	Tools           []map[string]interface{}
	Toolboxes       []map[string]interface{}
	Grounding       []map[string]interface{}
	MemoryStores    []map[string]interface{}
	Apim            ApimSpec
	Cloud           azcloud.Profile
}

// RequireProjectEndpoint returns the project endpoint or a clear error.
func (c *ResolvedConfig) RequireProjectEndpoint() (string, error) {
	if c.Project.Endpoint == "" {
		return "", errs.Config(
			"project endpoint is unknown: set project.resource_id to a valid Foundry project resource ID.",
		)
	}
	return netcheck.ValidateFoundryEndpointForSuffixes(
		c.Project.Endpoint,
		"project.endpoint",
		c.Cloud.FoundryEndpointSuffixes,
	)
}

// LoadSchema returns the embedded manifest JSON Schema.
func LoadSchema() (map[string]interface{}, error) {
	data := manifestschema.Bytes()
	var schema map[string]interface{}
	if err := json.Unmarshal(data, &schema); err != nil {
		return nil, fmt.Errorf("failed to parse schema: %w", err)
	}
	return schema, nil
}

// LoadSchemaBytes returns the raw embedded schema bytes.
func LoadSchemaBytes() ([]byte, error) {
	return manifestschema.Bytes(), nil
}

// LoadManifest reads a YAML or JSON manifest into a map.
func LoadManifest(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Manifest("failed to read manifest %s: %v", path, err)
	}

	// Try JSON first, then YAML
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err == nil {
		if doc == nil {
			return nil, errs.Manifest("manifest %s must be a mapping at the top level", path)
		}
		return doc, nil
	}

	// YAML parsing - will be handled by the yaml package
	doc, yamlErr := parseYAML(data)
	if yamlErr != nil {
		return nil, errs.Manifest("manifest %s is not valid YAML: %v", path, yamlErr)
	}
	if doc == nil {
		return nil, errs.Manifest("manifest %s must be a mapping at the top level", path)
	}
	return doc, nil
}

func stripTrailingSlash(s string) string {
	return strings.TrimRight(s, "/")
}

func getStr(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprintf("%v", v)
	}
	return s
}

func getBool(m map[string]interface{}, key string, defaultVal bool) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	b, ok := v.(bool)
	if ok {
		return b
	}
	return defaultVal
}

func getInt(m map[string]interface{}, key string, defaultVal int) int {
	v, ok := m[key]
	if !ok || v == nil {
		return defaultVal
	}
	switch value := v.(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		parsed, err := value.Int64()
		if err == nil {
			return int(parsed)
		}
	}
	return defaultVal
}

func getStrSlice(m map[string]interface{}, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	result, ok := v.(map[string]interface{})
	if !ok {
		return nil
	}
	return result
}

func getToolsList(m map[string]interface{}, key string) []map[string]interface{} {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(arr))
	for _, item := range arr {
		if tool, ok := item.(map[string]interface{}); ok {
			result = append(result, tool)
		}
	}
	return result
}

func validateEndpoints(project *ProjectSpec, apim *ApimSpec, profile azcloud.Profile) error {
	if project.Endpoint != "" {
		if _, err := netcheck.ValidateFoundryEndpointForSuffixes(
			project.Endpoint,
			"project.endpoint",
			profile.FoundryEndpointSuffixes,
		); err != nil {
			return err
		}
	}
	if project.AccountEndpoint != "" {
		if _, err := netcheck.ValidateFoundryEndpointForSuffixes(
			project.AccountEndpoint,
			"project.account_endpoint",
			profile.FoundryEndpointSuffixes,
		); err != nil {
			return err
		}
	}
	if err := validateProjectEndpointPaths(project); err != nil {
		return err
	}
	if apim.Enabled {
		if apim.Target != "" {
			if _, err := netcheck.ValidateAPIMTargetForSuffixes(apim.Target, "apim.target", profile.APIMSuffixes); err != nil {
				return err
			}
		}
		if apim.GatewayURL != "" {
			if _, err := netcheck.ValidateAPIMTargetForSuffixes(apim.GatewayURL, "apim.gateway_url", profile.APIMSuffixes); err != nil {
				return err
			}
		}
		if _, err := apim.ValidateResolvedTarget(); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectEndpointPaths(project *ProjectSpec) error {
	if project.AccountEndpoint != "" {
		accountURL, err := url.Parse(project.AccountEndpoint)
		if err != nil {
			return errs.Config("project.account_endpoint is invalid: %v", err)
		}
		hasPath := accountURL.EscapedPath() != "" && accountURL.EscapedPath() != "/"
		if hasPath || accountURL.RawQuery != "" || accountURL.Fragment != "" {
			origin := (&url.URL{Scheme: accountURL.Scheme, Host: accountURL.Host}).String()
			if strings.Contains(strings.ToLower(accountURL.EscapedPath()), "/api/projects/") {
				return errs.Config(
					"project.account_endpoint %q is a project endpoint, not an account origin; "+
						"set project.account_endpoint to %q with project.name %q, "+
						"or move the full URL to project.endpoint",
					project.AccountEndpoint,
					origin,
					project.Name,
				)
			}
			return errs.Config(
				"project.account_endpoint must be the account origin without a path, query, or fragment; use %q",
				origin,
			)
		}
	}

	if project.Endpoint == "" {
		return nil
	}
	projectURL, err := url.Parse(project.Endpoint)
	if err != nil {
		return errs.Config("project.endpoint is invalid: %v", err)
	}
	path := strings.Trim(projectURL.EscapedPath(), "/")
	segments := strings.Split(path, "/")
	validPath := len(segments) == 3 &&
		strings.EqualFold(segments[0], "api") &&
		strings.EqualFold(segments[1], "projects") &&
		segments[2] != ""
	if validPath && projectURL.RawQuery == "" && projectURL.Fragment == "" {
		return nil
	}
	if strings.Count(strings.ToLower("/"+path), "/api/projects/") > 1 {
		return errs.Config(
			"project.endpoint %q contains duplicated /api/projects/<project> paths; "+
				"set it to exactly one Foundry project endpoint",
			project.Endpoint,
		)
	}
	return errs.Config(
		"project.endpoint must identify exactly one Foundry project as "+
			"https://<account>.services.ai.azure.com/api/projects/<project>; got %q",
		project.Endpoint,
	)
}

// ValidateARMRouting ensures control-plane requests cannot silently fall back to another cloud.
func ValidateARMRouting(project *ProjectSpec) error {
	if project == nil {
		return errs.Config("project configuration is required")
	}
	if strings.TrimSpace(project.ARMEndpoint) == "" {
		return errs.Config("project ARM endpoint is unresolved")
	}
	if strings.TrimSpace(project.ARMScope) == "" {
		return errs.Config("project ARM token scope is unresolved")
	}
	return nil
}

// ValidateProjectLocation verifies that a requested project location supports Foundry.
func ValidateProjectLocation(location string, allowed []string) error {
	if location == "" || len(allowed) == 0 {
		return nil
	}
	normalized := normalizeLocation(location)
	for _, candidate := range allowed {
		if normalized == normalizeLocation(candidate) {
			return nil
		}
	}
	return errs.Config(
		"project.location %q is unavailable for Foundry in the selected cloud; supported regions: %s",
		location,
		strings.Join(allowed, ", "),
	)
}

// ValidateReportedProjectLocation requires a cloud-valid location from ARM.
func ValidateReportedProjectLocation(location string, allowed []string) error {
	if len(allowed) > 0 && strings.TrimSpace(location) == "" {
		return errs.Config("ARM project response contained no location for the selected cloud")
	}
	return ValidateProjectLocation(location, allowed)
}

// ValidateResolvedConfig applies cloud-specific checks after all CLI overrides.
func ValidateResolvedConfig(cfg *ResolvedConfig) error {
	if cfg == nil {
		return errs.Config("resolved configuration is nil")
	}
	if err := ValidateARMRouting(&cfg.Project); err != nil {
		return err
	}
	if err := validateEndpoints(&cfg.Project, &cfg.Apim, cfg.Cloud); err != nil {
		return err
	}
	if err := validateAgentRAIPolicy(cfg.Agent.RAIPolicyID, cfg.Project); err != nil {
		return err
	}
	if err := ValidateStructuredInputDefinitions(cfg.Agent.StructuredInputs); err != nil {
		return err
	}
	if err := ValidateProjectLocation(cfg.Project.Location, cfg.Project.AllowedRegions); err != nil {
		return err
	}
	if err := validateAPIMAudience(&cfg.Apim, cfg.Cloud); err != nil {
		return err
	}
	if err := validateEndpointConfig(cfg.Endpoint, cfg.Cloud); err != nil {
		return err
	}
	if err := validateModelDeploymentConfig(cfg.ModelDeployment); err != nil {
		return err
	}
	if err := custommetadata.Validate(cfg.Agent.Metadata); err != nil {
		return err
	}
	for i, tool := range cfg.Tools {
		toolType := getStr(tool, "type")
		if reason := cfg.Cloud.UnsupportedTools[toolType]; reason != "" {
			return errs.Config("tools[%d] type %q is unavailable in %s: %s", i, toolType, cfg.Cloud.Name, reason)
		}
		if toolType != "azure_function" {
			continue
		}
		for _, field := range []string{"input_queue", "output_queue"} {
			queue := getMap(tool, field)
			endpoint := getStr(queue, "service_endpoint")
			if _, err := netcheck.ValidateStorageQueueEndpointForSuffixes(
				endpoint,
				fmt.Sprintf("tools[%d].%s.service_endpoint", i, field),
				cfg.Cloud.StorageQueueSuffixes,
			); err != nil {
				return err
			}
		}
	}
	if len(cfg.Toolboxes) > 0 && !cfg.Cloud.Capabilities.Toolboxes {
		return errs.Config(
			"Foundry Toolboxes are unavailable or unverified in %s",
			cfg.Cloud.Name,
		)
	}
	for toolboxIndex, toolbox := range cfg.Toolboxes {
		for toolIndex, tool := range getToolsList(toolbox, "tools") {
			toolType := getStr(tool, "type")
			if reason := cfg.Cloud.UnsupportedTools[toolType]; reason != "" {
				return errs.Config(
					"toolboxes[%d].tools[%d] type %q is unavailable in %s: %s",
					toolboxIndex,
					toolIndex,
					toolType,
					cfg.Cloud.Name,
					reason,
				)
			}
		}
	}
	return nil
}

func validateAgentRAIPolicy(policyID string, project ProjectSpec) error {
	if strings.TrimSpace(policyID) == "" {
		return nil
	}
	policy, err := foundryid.ParseRAIPolicyID(policyID)
	if err != nil {
		return errs.Config("agent.rai_policy_id is invalid: %v", err)
	}
	if project.SubscriptionID == "" || project.ResourceGroup == "" || project.AccountName == "" {
		return nil
	}
	account, err := foundryid.ParseAccountID(fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s",
		project.SubscriptionID,
		project.ResourceGroup,
		project.AccountName,
	))
	if err != nil {
		return errs.Config("project coordinates are invalid while validating agent.rai_policy_id: %v", err)
	}
	if !policy.SameAccount(account) {
		return errs.Config(
			"agent.rai_policy_id must reference the same Foundry account as the configured project",
		)
	}
	return nil
}

func validateModelDeploymentConfig(spec ModelDeploymentSpec) error {
	if !spec.Configured {
		return nil
	}
	for _, required := range []struct {
		field string
		value string
	}{
		{field: "model_deployment.deployment_name", value: spec.DeploymentName},
		{field: "model_deployment.model_name", value: spec.ModelName},
		{field: "model_deployment.model_version", value: spec.ModelVersion},
		{field: "model_deployment.model_format", value: spec.ModelFormat},
		{field: "model_deployment.sku_name", value: spec.SKUName},
	} {
		if strings.TrimSpace(required.value) == "" {
			return errs.Config("%s must not be empty", required.field)
		}
	}
	if spec.Capacity <= 0 {
		return errs.Config("model_deployment.capacity must be greater than zero")
	}
	switch spec.VersionUpgradeOption {
	case "", "OnceNewDefaultVersionAvailable", "OnceCurrentVersionExpired", "NoAutoUpgrade":
	default:
		return errs.Config(
			"model_deployment.version_upgrade_option %q is invalid",
			spec.VersionUpgradeOption,
		)
	}
	if strings.EqualFold(spec.SpilloverDeploymentName, spec.DeploymentName) {
		return errs.Config(
			"model_deployment.spillover_deployment_name must differ from deployment_name",
		)
	}
	return nil
}

func validateEndpointConfig(endpoint EndpointSpec, profile azcloud.Profile) error {
	if !endpoint.Configured {
		return nil
	}
	if !profile.Capabilities.StableAgentEndpoints {
		return errs.Config("stable agent endpoints are unavailable in %s", profile.Name)
	}
	allowedProtocols := map[string]struct{}{
		"responses":   {},
		"activity":    {},
		"invocations": {},
		"a2a":         {},
		"mcp":         {},
	}
	for _, protocol := range endpoint.Protocols {
		if _, ok := allowedProtocols[protocol]; !ok {
			return errs.Config("endpoint.protocols contains unsupported protocol %q", protocol)
		}
		if protocol == "activity" && !profile.Capabilities.M365Publishing {
			return errs.Config(
				"endpoint protocol %q is unavailable in %s because Bot Service and Microsoft 365 publishing are not supported",
				protocol,
				profile.Name,
			)
		}
	}
	allowedSchemes := map[string]struct{}{
		"Entra":            {},
		"BotServiceRbac":   {},
		"BotServiceTenant": {},
	}
	for _, scheme := range endpoint.AuthorizationSchemes {
		if _, ok := allowedSchemes[scheme]; !ok {
			return errs.Config("endpoint.authorization_schemes contains unsupported type %q", scheme)
		}
		if scheme != "Entra" && !profile.Capabilities.M365Publishing {
			return errs.Config(
				"endpoint authorization scheme %q is unavailable in %s because Bot Service is not supported",
				scheme,
				profile.Name,
			)
		}
	}
	return nil
}

func validateAPIMAudience(apim *ApimSpec, _ azcloud.Profile) error {
	if !apim.Enabled || apim.Auth != "managed_identity" {
		return nil
	}
	return ValidateManagedIdentityAudience(apim.Audience, apim.BlockedAudienceHosts)
}

// ValidateManagedIdentityAudience rejects OAuth scopes and opposite-cloud Microsoft resource hosts.
func ValidateManagedIdentityAudience(audience string, blockedHosts []string) error {
	audience = strings.TrimSpace(audience)
	lower := strings.ToLower(strings.TrimRight(audience, "/"))
	if strings.HasSuffix(lower, "/.default") {
		return errs.Config(
			"managed-identity audience %q is an OAuth scope; use the resource URI without /.default",
			audience,
		)
	}
	parsed, err := url.Parse(audience)
	if err != nil {
		return errs.Config("managed-identity audience %q is invalid: %v", audience, err)
	}
	if parsed.User != nil {
		return errs.Security("managed-identity audience %q must not embed credentials", audience)
	}
	host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
	for _, blocked := range blockedHosts {
		blocked = strings.ToLower(strings.TrimLeft(strings.TrimSpace(blocked), "."))
		if blocked != "" && (host == blocked || strings.HasSuffix(host, "."+blocked)) {
			return errs.Security(
				"managed-identity audience %q belongs to the opposite Azure cloud",
				audience,
			)
		}
	}
	return nil
}

// ResolveConfig builds a resolved configuration from a validated manifest document.
func ResolveConfig(doc map[string]interface{}) (*ResolvedConfig, error) {
	profile, err := azcloud.Resolve(getStr(doc, "cloud"))
	if err != nil {
		return nil, err
	}
	return ResolveConfigWithCloud(doc, profile)
}

// ResolveConfigWithCloud builds a resolved configuration for a selected Azure cloud.
func ResolveConfigWithCloud(doc map[string]interface{}, profile azcloud.Profile) (*ResolvedConfig, error) {
	agentDoc := getMap(doc, "agent")
	if agentDoc == nil {
		agentDoc = map[string]interface{}{}
	}
	projectDoc := getMap(doc, "project")
	if projectDoc == nil {
		projectDoc = map[string]interface{}{}
	}
	endpointDoc := getMap(doc, "endpoint")
	modelDeploymentDoc := getMap(doc, "model_deployment")

	rawMetadata, metadataConfigured := agentDoc["metadata"]
	var metadata map[string]string
	if metadataConfigured {
		metadataObject, ok := rawMetadata.(map[string]interface{})
		if !ok {
			return nil, errs.Config("agent.metadata must be an object")
		}
		parsedMetadata, metadataErr := custommetadata.FromMap(metadataObject)
		if metadataErr != nil {
			return nil, metadataErr
		}
		metadata = parsedMetadata
		if metadata == nil {
			metadata = map[string]string{}
		}
	}
	agent := AgentSpec{
		Name:               getStr(agentDoc, "name"),
		Instructions:       getStr(agentDoc, "instructions"),
		Model:              getStr(agentDoc, "model"),
		Description:        getStr(agentDoc, "description"),
		RAIPolicyID:        getStr(agentDoc, "rai_policy_id"),
		StructuredInputs:   getMap(agentDoc, "structured_inputs"),
		Metadata:           metadata,
		MetadataConfigured: metadataConfigured,
	}

	endpoint := EndpointSpec{}
	if endpointDoc != nil {
		endpoint.Configured = true
		endpoint.Protocols = appendUnique(
			[]string{"responses"},
			getStrSlice(endpointDoc, "protocols")...,
		)
		for _, raw := range getMapSlice(endpointDoc, "authorization_schemes") {
			if value := getStr(raw, "type"); value != "" {
				endpoint.AuthorizationSchemes = append(endpoint.AuthorizationSchemes, value)
			}
		}
		endpoint.AuthorizationSchemes = appendUnique(
			[]string{"Entra"},
			endpoint.AuthorizationSchemes...,
		)
		if cardDoc := getMap(endpointDoc, "agent_card"); cardDoc != nil {
			endpoint.AgentCard.Version = getStr(cardDoc, "version")
			endpoint.AgentCard.Description = getStr(cardDoc, "description")
			for _, skillDoc := range getMapSlice(cardDoc, "skills") {
				endpoint.AgentCard.Skills = append(endpoint.AgentCard.Skills, AgentCardSkillSpec{
					ID:          getStr(skillDoc, "id"),
					Name:        getStr(skillDoc, "name"),
					Description: getStr(skillDoc, "description"),
					Tags:        getStrSlice(skillDoc, "tags"),
					Examples:    getStrSlice(skillDoc, "examples"),
				})
			}
		}
	}

	resourceID := getStr(projectDoc, "resource_id")
	var parsedProject foundryid.ProjectID
	var projectName, accountEndpoint, projectEndpoint string
	if resourceID != "" {
		var parseErr error
		parsedProject, parseErr = foundryid.ParseProjectID(resourceID)
		if parseErr != nil {
			return nil, errs.Config("project.resource_id is invalid: %v", parseErr)
		}
		projectName = parsedProject.ProjectName
		accountEndpoint = parsedProject.AccountEndpoint()
		projectEndpoint = parsedProject.ProjectEndpoint()
	}

	apiVersion := getStr(projectDoc, "api_version")
	if apiVersion == "" {
		apiVersion = DefaultProjectAPIVersion
	}

	project := ProjectSpec{
		ResourceID:      resourceID,
		Name:            projectName,
		Endpoint:        projectEndpoint,
		AccountEndpoint: accountEndpoint,
		AccountName:     parsedProject.AccountName,
		ResourceGroup:   parsedProject.ResourceGroup,
		SubscriptionID:  parsedProject.SubscriptionID,
		Location:        getStr(projectDoc, "location"),
		DisplayName:     getStr(projectDoc, "display_name"),
		Description:     getStr(projectDoc, "description"),
		APIVersion:      apiVersion,
		ARMEndpoint:     profile.ARMEndpoint,
		ARMScope:        profile.ARMScope,
		AllowedRegions:  append([]string(nil), profile.FoundryRegions...),
	}

	modelDeployment := ModelDeploymentSpec{}
	if modelDeploymentDoc != nil {
		modelDeployment = ModelDeploymentSpec{
			Configured:              true,
			DeploymentName:          getStr(modelDeploymentDoc, "deployment_name"),
			ModelName:               getStr(modelDeploymentDoc, "model_name"),
			ModelVersion:            getStr(modelDeploymentDoc, "model_version"),
			ModelFormat:             getStr(modelDeploymentDoc, "model_format"),
			SKUName:                 getStr(modelDeploymentDoc, "sku_name"),
			Capacity:                getInt(modelDeploymentDoc, "capacity", 0),
			RAIPolicyName:           getStr(modelDeploymentDoc, "rai_policy_name"),
			VersionUpgradeOption:    getStr(modelDeploymentDoc, "version_upgrade_option"),
			SpilloverDeploymentName: getStr(modelDeploymentDoc, "spillover_deployment_name"),
		}
		if modelDeployment.DeploymentName == "" {
			modelDeployment.DeploymentName = agent.Model
		}
	}

	defaultAudience := "https://cognitiveservices.azure.com"
	if len(profile.TrustedAudiences) > 0 {
		defaultAudience = profile.TrustedAudiences[0]
	}
	apim := ApimSpec{
		Auth:                 "api_key",
		Audience:             defaultAudience,
		ConnectionAPIVersion: DefaultConnectionAPIVersion,
		AllowedSuffixes:      append([]string(nil), profile.APIMSuffixes...),
		BlockedAudienceHosts: append([]string(nil), profile.OppositeAudienceHosts...),
	}
	apimDoc := getMap(doc, "apim")
	if apimDoc != nil {
		apim.Enabled = true
		apim.ConnectionName = getStr(apimDoc, "connection_name")
		apim.Target = stripTrailingSlash(getStr(apimDoc, "target"))
		apim.GatewayURL = stripTrailingSlash(getStr(apimDoc, "gateway_url"))
		apim.APIPath = getStr(apimDoc, "api_path")
		apim.APIName = getStr(apimDoc, "api_name")
		if a := getStr(apimDoc, "auth"); a != "" {
			apim.Auth = a
		}
		if a := getStr(apimDoc, "audience"); a != "" {
			apim.Audience = a
		}
		apim.DeploymentInPath = getBool(apimDoc, "deployment_in_path", false)
		apim.InferenceAPIVersion = getStr(apimDoc, "inference_api_version")
		apim.IsSharedToAll = getBool(apimDoc, "is_shared_to_all", false)
		apim.Models = getStrSlice(apimDoc, "models")
		if v := getStr(apimDoc, "connection_api_version"); v != "" {
			apim.ConnectionAPIVersion = v
		}
	}

	tools := getToolsList(doc, "tools")
	toolboxes := getToolsList(doc, "toolboxes")
	groundingDoc := getMap(doc, "grounding")
	grounding := getToolsList(groundingDoc, "vector_stores")
	memoryStores := getToolsList(doc, "memory_stores")
	resolved := &ResolvedConfig{
		Agent:           agent,
		Endpoint:        endpoint,
		Project:         project,
		ModelDeployment: modelDeployment,
		Tools:           tools,
		Toolboxes:       toolboxes,
		Grounding:       grounding,
		MemoryStores:    memoryStores,
		Apim:            apim,
		Cloud:           profile,
	}
	if err := ValidateResolvedConfig(resolved); err != nil {
		return nil, err
	}
	return resolved, nil
}

func getMapSlice(m map[string]interface{}, key string) []map[string]interface{} {
	value, ok := m[key]
	if !ok || value == nil {
		return nil
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if object, ok := item.(map[string]interface{}); ok {
			result = append(result, object)
		}
	}
	return result
}

func appendUnique(base []string, values ...string) []string {
	result := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(result)+len(values))
	for _, value := range result {
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func normalizeLocation(value string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
}
