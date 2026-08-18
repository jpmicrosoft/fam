package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"foundry-agent-manager/internal/agentdiff"
	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/grounding"
	"foundry-agent-manager/internal/memory"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type resolvedManifest struct {
	Config       *config.ResolvedConfig
	Document     map[string]interface{}
	ManifestPath string
	BaseDir      string
	ManifestHash string
}

type preparedAgent struct {
	Resolved       *resolvedManifest
	WireTools      []interface{}
	Toolboxes      []tools.ToolboxDefinition
	Grounding      []grounding.VectorStore
	Desired        agentdiff.Desired
	APIMEnabled    bool
	ConnectionName string
	APIMModels     []string
}

func manifestDirectory(manifestPath string) (string, error) {
	absolute, err := filepath.Abs(manifestPath)
	if err != nil {
		return "", errs.Manifest("failed to resolve manifest path: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", errs.Manifest("failed to resolve manifest path %s: %v", manifestPath, err)
	}
	return filepath.Dir(resolved), nil
}

// manifestSection returns a top-level mapping for override application. A
// present-but-non-mapping section is rejected instead of being silently
// replaced, so manifest content is never discarded before schema validation.
func manifestSection(doc map[string]interface{}, key string) (map[string]interface{}, error) {
	value, present := doc[key]
	if !present || value == nil {
		return map[string]interface{}{}, nil
	}
	section, ok := value.(map[string]interface{})
	if !ok {
		return nil, errs.Manifest("manifest failed schema validation: %s: must be an object", key)
	}
	return section, nil
}

func applyOverrides(doc map[string]interface{}, cmd *cobra.Command) error {
	manifestPath := getFlag(cmd, "manifest")
	agent, err := manifestSection(doc, "agent")
	if err != nil {
		return err
	}
	if value := getFlag(cmd, "name"); value != "" {
		agent["name"] = value
	}
	if value := getFlag(cmd, "model"); value != "" {
		agent["model"] = value
	}
	if value := getFlag(cmd, "description"); value != "" {
		agent["description"] = value
	}
	if value := getFlag(cmd, "instructions-file"); value != "" {
		base, err := manifestDirectory(manifestPath)
		if err != nil {
			return err
		}
		data, err := netcheck.ReadContainedFile(base, value, "--instructions-file")
		if err != nil {
			if errs.IsKind(err, "security") {
				return err
			}
			return errs.Manifest("%v", err)
		}
		agent["instructions"] = string(data)
	}
	metadataOverrides, err := metadataFromFlags(cmd)
	if err != nil {
		return err
	}
	var manifestMetadata map[string]string
	if rawMetadata, present := agent["metadata"]; present && rawMetadata != nil {
		metadataObject, ok := rawMetadata.(map[string]interface{})
		if !ok {
			return errs.Manifest(
				"manifest failed schema validation: agent/metadata: must be an object",
			)
		}
		manifestMetadata, err = custommetadata.FromMap(metadataObject)
		if err != nil {
			return err
		}
	}
	metadata := custommetadata.Merge(manifestMetadata, metadataOverrides)
	if len(metadata) > 0 {
		agent["metadata"] = custommetadata.InterfaceMap(metadata)
	}
	doc["agent"] = agent

	project, err := manifestSection(doc, "project")
	if err != nil {
		return err
	}
	if value := getFlag(cmd, "project-resource-id"); value != "" {
		project["resource_id"] = value
	}
	if len(project) > 0 {
		doc["project"] = project
	}
	return nil
}

func resolveManifest(cmd *cobra.Command) (*resolvedManifest, error) {
	manifestPath := getFlag(cmd, "manifest")
	doc, err := config.LoadManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	if err := applyOverrides(doc, cmd); err != nil {
		return nil, err
	}

	manifestCloud, _ := doc["cloud"].(string)
	profile, err := azcloud.Resolve(selectedCloudName(cmd, manifestCloud))
	if err != nil {
		return nil, err
	}
	if getFlag(cmd, "cloud") != "" || os.Getenv("FOUNDRY_AGENT_MANAGER_CLOUD") != "" {
		doc["cloud"] = profile.Name
	}
	if err := config.ValidateManifest(doc); err != nil {
		return nil, err
	}
	cfg, err := config.ResolveConfigWithCloud(doc, profile)
	if err != nil {
		return nil, err
	}
	setCommandMetadata(cmd, cfg.Agent.Metadata)
	if value := getFlag(cmd, "location"); value != "" {
		cfg.Project.Location = value
	}
	if err := config.ValidateResolvedConfig(cfg); err != nil {
		return nil, err
	}
	baseDir, err := manifestDirectory(manifestPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, errs.Manifest("failed to hash manifest %s: %v", manifestPath, err)
	}
	hash := sha256.Sum256(data)
	return &resolvedManifest{
		Config:       cfg,
		Document:     doc,
		ManifestPath: manifestPath,
		BaseDir:      baseDir,
		ManifestHash: hex.EncodeToString(hash[:]),
	}, nil
}

func resolve(cmd *cobra.Command) (*config.ResolvedConfig, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	return resolved.Config, nil
}

func prepareAgent(cmd *cobra.Command) (*preparedAgent, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	if cfg.Agent.Model == "" {
		return nil, errs.Config("agent.model is unresolved; set agent.model or pass --model")
	}
	wireTools, err := tools.BuildToolsForProject(
		cfg.Tools,
		resolved.BaseDir,
		cfg.Project.Endpoint,
	)
	if err != nil {
		return nil, err
	}
	toolboxDefinitions, err := tools.BuildToolboxes(
		cfg.Toolboxes,
		resolved.BaseDir,
	)
	if err != nil {
		return nil, err
	}
	groundingDefinitions, err := grounding.Build(
		cfg.Grounding,
		resolved.BaseDir,
		true,
	)
	if err != nil {
		return nil, err
	}
	memoryDefinitions, err := memory.Build(cfg.MemoryStores)
	if err != nil {
		return nil, err
	}
	if err := validateManagedGroundingReferences(
		wireTools,
		toolboxDefinitions,
		groundingDefinitions,
	); err != nil {
		return nil, err
	}
	if err := validateManagedMemoryReferences(cfg.Tools, memoryDefinitions); err != nil {
		return nil, err
	}
	apimEnabled := cfg.Apim.Enabled && !getBoolFlag(cmd, "no-apim")
	effectiveModel := cfg.Agent.Model
	connectionName := ""
	var apimModels []string
	if apimEnabled {
		connectionName = connection.DefaultConnectionName(&cfg.Apim, cfg.Agent.Name)
		apimModels = append([]string(nil), cfg.Apim.Models...)
		if len(apimModels) == 0 {
			apimModels = []string{cfg.Agent.Model}
		}
		found := false
		for _, model := range apimModels {
			if model == cfg.Agent.Model {
				found = true
				break
			}
		}
		if !found {
			apimModels = append(apimModels, cfg.Agent.Model)
		}
		effectiveModel = connectionName + "/" + cfg.Agent.Model
	}
	return &preparedAgent{
		Resolved:  resolved,
		WireTools: wireTools,
		Toolboxes: toolboxDefinitions,
		Grounding: groundingDefinitions,
		Desired: agentdiff.Desired{
			Description:      cfg.Agent.Description,
			Model:            effectiveModel,
			Instructions:     cfg.Agent.Instructions,
			Tools:            wireTools,
			RAIPolicyID:      cfg.Agent.RAIPolicyID,
			StructuredInputs: cfg.Agent.StructuredInputs,
			Metadata:         cfg.Agent.Metadata,
			ManageMetadata:   cfg.Agent.MetadataConfigured,
		},
		APIMEnabled:    apimEnabled,
		ConnectionName: connectionName,
		APIMModels:     apimModels,
	}, nil
}

func validateManagedGroundingReferences(
	wireTools []interface{},
	toolboxes []tools.ToolboxDefinition,
	definitions []grounding.VectorStore,
) error {
	defined := make(map[string]string, len(definitions))
	for _, definition := range definitions {
		defined[strings.ToLower(definition.Name)] = definition.Name
	}
	var names []string
	names = append(names, tools.ManagedVectorStoreNames(wireTools)...)
	for _, toolbox := range toolboxes {
		names = append(names, tools.ManagedVectorStoreNames(toolbox.Tools)...)
	}
	for _, name := range names {
		if _, exists := defined[strings.ToLower(name)]; !exists {
			return errs.Config(
				"file_search references managed vector store %q, but grounding.vector_stores does not define it",
				name,
			)
		}
	}
	return nil
}

func validateManagedMemoryReferences(
	rawTools []map[string]interface{},
	definitions []memory.Definition,
) error {
	defined := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		defined[strings.ToLower(definition.Name)] = struct{}{}
	}
	for _, tool := range rawTools {
		if fmt.Sprint(tool["type"]) != "memory_search_preview" {
			continue
		}
		name := strings.TrimSpace(fmt.Sprint(tool["memory_store_name"]))
		if _, exists := defined[strings.ToLower(name)]; !exists {
			return errs.Config(
				"memory_search_preview references memory store %q, but memory_stores does not define it",
				name,
			)
		}
	}
	return nil
}

func hasProjectCoordinates(project config.ProjectSpec) bool {
	return project.SubscriptionID != "" &&
		project.ResourceGroup != "" &&
		project.AccountName != "" &&
		project.Name != ""
}
