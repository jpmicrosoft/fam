package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

var toolboxNamePattern = regexp.MustCompile(
	`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`,
)

// SupportedToolboxToolTypes enumerates tools that can be placed in a managed
// Foundry Toolbox version.
var SupportedToolboxToolTypes = []string{
	"a2a_preview",
	"azure_ai_search",
	"bing_custom_search",
	"browser_automation_preview",
	"code_interpreter",
	"fabric_iq_preview",
	"file_search",
	"mcp",
	"openapi",
	"reminder_preview",
	"toolbox_search",
	"web_search",
	"work_iq_preview",
}

var previewToolboxToolTypes = map[string]struct{}{
	"a2a_preview":                {},
	"browser_automation_preview": {},
	"fabric_iq_preview":          {},
	"reminder_preview":           {},
	"work_iq_preview":            {},
}

// ToolboxSkill identifies an existing same-project skill.
type ToolboxSkill struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ToolboxDefinition is one immutable Toolbox version request derived from a
// manifest. Tools and skills contain no credentials; project connections own
// downstream authentication.
type ToolboxDefinition struct {
	Name                string
	Description         string
	RAIPolicyID         string
	Tools               []interface{}
	Skills              []ToolboxSkill
	RequiresPreview     bool
	PreviewCapabilities []string
	PreviewFeatures     []string
}

// Payload returns the Foundry REST request body for creating a version.
func (d ToolboxDefinition) Payload() map[string]interface{} {
	payload := map[string]interface{}{
		"description": d.Description,
		"tools":       d.Tools,
	}
	if len(d.Skills) > 0 {
		payload["skills"] = d.Skills
	}
	if d.RAIPolicyID != "" {
		payload["policies"] = map[string]interface{}{
			"rai_config": map[string]interface{}{
				"rai_policy_name": d.RAIPolicyID,
			},
		}
	}
	return payload
}

// PreviewHeader returns the opt-in header required by preview-only Toolbox
// subcontracts. The Toolbox REST create operation itself uses api-version=v1.
func (d ToolboxDefinition) PreviewHeader() string {
	features := append([]string(nil), d.PreviewFeatures...)
	sort.Strings(features)
	return strings.Join(features, ",")
}

// DescribeToolbox returns a concise offline description of the Toolbox.
func DescribeToolbox(definition ToolboxDefinition) string {
	return fmt.Sprintf(
		"toolbox(name=%q, tools=%d, skills=%d, preview=%t)",
		definition.Name,
		len(definition.Tools),
		len(definition.Skills),
		definition.RequiresPreview,
	)
}

// BuildToolboxes validates and converts top-level Toolbox definitions.
func BuildToolboxes(
	raw []map[string]interface{},
	baseDir string,
) ([]ToolboxDefinition, error) {
	definitions := make([]ToolboxDefinition, 0, len(raw))
	seenNames := make(map[string]struct{}, len(raw))
	for i, document := range raw {
		definition, err := buildToolbox(document, baseDir)
		if err != nil {
			if errs.IsKind(err, "security") {
				return nil, errs.SecurityWrap(err, "toolboxes[%d]", i)
			}
			return nil, errs.ToolBuild("toolboxes[%d]: %v", i, err)
		}
		normalized := strings.ToLower(definition.Name)
		if _, exists := seenNames[normalized]; exists {
			return nil, errs.ToolBuild(
				"toolboxes[%d]: duplicate toolbox name %q",
				i,
				definition.Name,
			)
		}
		seenNames[normalized] = struct{}{}
		definitions = append(definitions, definition)
	}
	return definitions, nil
}

func buildToolbox(
	document map[string]interface{},
	baseDir string,
) (ToolboxDefinition, error) {
	name := getStr(document, "name")
	if !toolboxNamePattern.MatchString(name) {
		return ToolboxDefinition{}, fmt.Errorf(
			"name %q must be 1-64 alphanumeric, dot, underscore, or hyphen characters and start and end with an alphanumeric character",
			name,
		)
	}
	rawTools := getMapSlice(document, "tools")
	rawSkills := getMapSlice(document, "skills")
	if len(rawTools) == 0 && len(rawSkills) == 0 {
		return ToolboxDefinition{}, fmt.Errorf(
			"at least one tool or skill reference is required",
		)
	}

	definition := ToolboxDefinition{
		Name:        name,
		Description: getStr(document, "description"),
		RAIPolicyID: getStr(document, "rai_policy_id"),
	}
	unnamedType := ""
	for i, tool := range rawTools {
		built, preview, err := buildToolboxTool(tool, baseDir)
		if err != nil {
			if errs.IsKind(err, "security") {
				return ToolboxDefinition{}, errs.SecurityWrap(
					err,
					"tools[%d] (%s)",
					i,
					getStr(tool, "type"),
				)
			}
			return ToolboxDefinition{}, fmt.Errorf(
				"tools[%d] (%s): %w",
				i,
				getStr(tool, "type"),
				err,
			)
		}
		toolType := getStr(tool, "type")
		if getStr(tool, "name") == "" && unnamedToolboxType(toolType) {
			if unnamedType != "" {
				return ToolboxDefinition{}, fmt.Errorf(
					"tools[%d]: multiple tools without identifiers are not allowed (%s and %s); set a unique name on all but one",
					i,
					unnamedType,
					toolType,
				)
			}
			unnamedType = toolType
		}
		if preview {
			definition.RequiresPreview = true
			definition.PreviewCapabilities = append(
				definition.PreviewCapabilities,
				toolType,
			)
		}
		definition.Tools = append(definition.Tools, built)
	}

	for i, skill := range rawSkills {
		name := getStr(skill, "name")
		if name == "" {
			return ToolboxDefinition{}, fmt.Errorf(
				"skills[%d].name is required",
				i,
			)
		}
		definition.Skills = append(definition.Skills, ToolboxSkill{
			Type:    "skill_reference",
			Name:    name,
			Version: getStr(skill, "version"),
		})
		definition.RequiresPreview = true
		definition.PreviewCapabilities = append(
			definition.PreviewCapabilities,
			"skill_reference",
		)
	}
	if len(definition.Skills) > 0 {
		definition.PreviewFeatures = append(
			definition.PreviewFeatures,
			"Skills=V1Preview",
		)
	}
	return definition, nil
}

func buildToolboxTool(
	tool map[string]interface{},
	baseDir string,
) (interface{}, bool, error) {
	toolType := getStr(tool, "type")
	_, preview := previewToolboxToolTypes[toolType]
	var (
		built interface{}
		err   error
	)
	switch toolType {
	case "web_search":
		built = buildWebSearch(tool)
	case "azure_ai_search":
		built, err = buildAzureAISearch(tool)
	case "bing_custom_search":
		configuration := getMap(tool, "custom_search_configuration")
		if getStr(configuration, "project_connection_id") == "" {
			return nil, preview, fmt.Errorf(
				"custom_search_configuration.project_connection_id is required",
			)
		}
		if getStr(configuration, "instance_name") == "" {
			return nil, preview, fmt.Errorf(
				"custom_search_configuration.instance_name is required",
			)
		}
		result := map[string]interface{}{
			"type":                        "web_search",
			"custom_search_configuration": configuration,
		}
		copyOptional(result, tool, "name", "description", "tool_configs")
		built = result
	case "code_interpreter":
		built = buildCodeInterpreter(tool)
	case "file_search":
		result := map[string]interface{}{"type": "file_search"}
		if name := getStr(tool, "vector_store"); name != "" {
			result[managedVectorStoreKey] = name
		} else {
			copyOptional(result, tool, "vector_store_ids")
		}
		built = result
	case "openapi":
		built, err = buildOpenAPI(tool, baseDir)
	case "mcp":
		built, err = buildMCP(tool)
	case "a2a_preview":
		built, err = buildA2A(tool)
	case "work_iq_preview":
		built, err = buildConnectionTool(toolType, tool)
	case "browser_automation_preview":
		built, err = buildBrowserAutomation(tool)
	case "fabric_iq_preview":
		built, err = buildFabricIQ(tool)
	case "toolbox_search":
		built = map[string]interface{}{"type": "toolbox_search"}
	case "reminder_preview":
		built = map[string]interface{}{"type": "reminder_preview"}
	default:
		return nil, false, fmt.Errorf(
			"unsupported Toolbox tool type %q",
			toolType,
		)
	}
	if err != nil {
		return nil, preview, err
	}
	result, ok := built.(map[string]interface{})
	if !ok {
		return nil, preview, fmt.Errorf("built payload is not an object")
	}
	copyOptional(
		result,
		tool,
		"name",
		"description",
		"tool_configs",
	)
	return result, preview, nil
}

// ResolveToolboxManagedVectorStores replaces logical grounding references in a
// Toolbox definition with concrete vector-store IDs.
func ResolveToolboxManagedVectorStores(
	definition ToolboxDefinition,
	vectorStoreIDs map[string]string,
) (ToolboxDefinition, error) {
	resolved, err := ResolveManagedVectorStores(definition.Tools, vectorStoreIDs)
	if err != nil {
		return ToolboxDefinition{}, err
	}
	definition.Tools = resolved
	return definition, nil
}

func unnamedToolboxType(toolType string) bool {
	switch toolType {
	case "web_search", "azure_ai_search", "code_interpreter", "file_search":
		return true
	default:
		return false
	}
}

// ToolboxPayloadEqual compares the managed fields of a remote Toolbox version
// with one desired immutable definition.
func ToolboxPayloadEqual(
	remote map[string]interface{},
	desired ToolboxDefinition,
) (bool, error) {
	remoteManaged := map[string]interface{}{
		"description": getStr(remote, "description"),
		"tools":       remote["tools"],
	}
	if skills, found := remote["skills"]; found {
		remoteManaged["skills"] = skills
	}
	if policies, found := remote["policies"]; found {
		remoteManaged["policies"] = policies
	}
	remoteJSON, err := canonicalJSON(remoteManaged)
	if err != nil {
		return false, err
	}
	desiredJSON, err := canonicalJSON(desired.Payload())
	if err != nil {
		return false, err
	}
	return string(remoteJSON) == string(desiredJSON), nil
}

func canonicalJSON(value interface{}) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var normalized interface{}
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	return json.Marshal(normalized)
}
