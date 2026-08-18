// Package tools translates declarative manifest tool entries into Foundry wire format.
package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"

	"gopkg.in/yaml.v3"
)

// SupportedToolTypes enumerates all direct prompt-agent tool types the manager understands.
var SupportedToolTypes = []string{
	"a2a_preview",
	"azure_ai_search",
	"azure_function",
	"bing_custom_search_preview",
	"bing_grounding",
	"browser_automation_preview",
	"code_interpreter",
	"computer_use_preview",
	"custom_code_interpreter",
	"fabric_iq_preview",
	"file_search",
	"function",
	"image_generation",
	"memory_search_preview",
	"mcp",
	"openapi",
	"sharepoint_grounding_preview",
	"toolbox",
	"web_search",
	"work_iq_preview",
}

func buildMemorySearch(tool map[string]interface{}) (interface{}, error) {
	storeName := getStr(tool, "memory_store_name")
	if storeName == "" {
		return nil, fmt.Errorf("missing required field \"memory_store_name\"")
	}
	scope := getStr(tool, "scope")
	if scope == "" {
		return nil, fmt.Errorf("missing required field \"scope\"")
	}
	result := map[string]interface{}{
		"type":              "memory_search_preview",
		"memory_store_name": storeName,
		"scope":             scope,
	}
	if raw, exists := tool["update_delay"]; exists {
		delay, ok := intValue(raw)
		if !ok || delay < 0 {
			return nil, fmt.Errorf("field \"update_delay\" must be a non-negative integer")
		}
		result["update_delay"] = delay
	}
	if options := getMap(tool, "search_options"); options != nil {
		built := map[string]interface{}{}
		if raw, exists := options["max_memories"]; exists {
			count, ok := intValue(raw)
			if !ok || count <= 0 {
				return nil, fmt.Errorf("field \"search_options.max_memories\" must be a positive integer")
			}
			built["max_memories"] = count
		}
		result["search_options"] = built
	}
	return result, nil
}

func intValue(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		converted := int(typed)
		return converted, float64(converted) == typed
	default:
		return 0, false
	}
}

// buildCustomCodeInterpreter attaches an Azure Container Apps Dynamic
// Sessions-backed interpreter through its documented MCP endpoint.
func buildCustomCodeInterpreter(tool map[string]interface{}) (interface{}, error) {
	mcp := make(map[string]interface{}, len(tool))
	for key, value := range tool {
		mcp[key] = value
	}
	mcp["type"] = "mcp"
	return buildMCP(mcp)
}

var previewToolTypes = map[string]struct{}{
	"a2a_preview":                  {},
	"bing_custom_search_preview":   {},
	"browser_automation_preview":   {},
	"computer_use_preview":         {},
	"fabric_iq_preview":            {},
	"image_generation":             {},
	"memory_search_preview":        {},
	"sharepoint_grounding_preview": {},
	"work_iq_preview":              {},
}

// SupportedMCPApprovalModes lists the scalar human-approval modes accepted by
// the Foundry Agent Service MCP tool contract. Per-tool filter objects are also
// supported by mcpApprovalMode.
var SupportedMCPApprovalModes = []string{"always", "never"}

// DefaultMCPApprovalMode is the fail-closed default when a manifest omits require_approval.
const DefaultMCPApprovalMode = "always"

const managedVectorStoreKey = "_foundry_agent_manager_vector_store"

var mcpHeaderNamePattern = regexp.MustCompile(`^[!#$%&'*+\-.^_` + "`" + `|~0-9A-Za-z]+$`)

var blockedMCPHeaderNames = map[string]struct{}{
	"authorization":             {},
	"cookie":                    {},
	"proxy-authorization":       {},
	"set-cookie":                {},
	"x-api-key":                 {},
	"api-key":                   {},
	"ocp-apim-subscription-key": {},
	"subscription-key":          {},
	"x-functions-key":           {},
}

// DescribeTools returns a human-readable, offline description of each tool entry.
func DescribeTools(tools []map[string]interface{}) ([]string, error) {
	var described []string
	for i, tool := range tools {
		toolType, _ := tool["type"].(string)
		switch toolType {
		case "code_interpreter":
			described = append(described, "code_interpreter")
		case "file_search":
			ids := getStrSlice(tool, "vector_store_ids")
			if name := getStr(tool, "vector_store"); name != "" {
				described = append(
					described,
					fmt.Sprintf("file_search(vector_store=%q, managed=true)", name),
				)
			} else {
				described = append(described, fmt.Sprintf("file_search(vector_store_ids=[%s])", joinStr(ids)))
			}
		case "openapi":
			auth := openapiAuthType(tool)
			described = append(described, fmt.Sprintf("openapi(name=%q, auth=%s)", getStr(tool, "name"), auth))
		case "mcp":
			approval, err := describeMCPApproval(tool)
			if err != nil {
				return nil, fmt.Errorf("tool %d (mcp): %w", i+1, err)
			}
			described = append(described, fmt.Sprintf("mcp(server_label=%q, require_approval=%q)",
				getStr(tool, "server_label"), approval))
		case "memory_search_preview":
			described = append(described, fmt.Sprintf(
				"memory_search_preview(memory_store_name=%q, scope=%q)",
				getStr(tool, "memory_store_name"),
				getStr(tool, "scope"),
			))
		case "azure_function":
			fn := getMap(tool, "function")
			described = append(described, fmt.Sprintf("azure_function(function=%q)", getStr(fn, "name")))
		case "bing_grounding", "bing_custom_search_preview":
			described = append(described, fmt.Sprintf(
				"%s(search_configurations=%d)",
				toolType,
				len(getMapSlice(tool, "search_configurations")),
			))
		case "toolbox":
			version := getStr(tool, "version")
			if version == "" {
				version = "default"
			}
			approval, err := describeMCPApproval(tool)
			if err != nil {
				return nil, fmt.Errorf("tool %d (toolbox): %w", i+1, err)
			}
			described = append(described, fmt.Sprintf(
				"toolbox(name=%q, version=%q, require_approval=%q)",
				getStr(tool, "name"),
				version,
				approval,
			))
		case "web_search":
			described = append(described, "web_search")
		case "azure_ai_search":
			described = append(described, fmt.Sprintf(
				"azure_ai_search(indexes=%d)",
				len(getMapSlice(tool, "indexes")),
			))
		case "a2a_preview":
			sendCredentials, _ := tool["send_credentials_for_agent_card"].(bool)
			described = append(described, fmt.Sprintf(
				"a2a_preview(project_connection_id=%q, agent_card_path=%q, send_credentials_for_agent_card=%t)",
				getStr(tool, "project_connection_id"),
				getStr(tool, "agent_card_path"),
				sendCredentials,
			))
		case "browser_automation_preview", "fabric_iq_preview", "work_iq_preview":
			described = append(described, fmt.Sprintf(
				"%s(project_connection_id=%q)",
				toolType,
				getStr(tool, "project_connection_id"),
			))
		case "sharepoint_grounding_preview":
			described = append(described, fmt.Sprintf(
				"sharepoint_grounding_preview(project_connections=%d)",
				len(getStrSlice(tool, "project_connection_ids")),
			))
		case "computer_use_preview":
			described = append(described, fmt.Sprintf(
				"computer_use_preview(environment=%q, display=%dx%d)",
				getStr(tool, "environment"),
				getInt(tool, "display_width"),
				getInt(tool, "display_height"),
			))
		case "custom_code_interpreter":
			approval, err := describeMCPApproval(tool)
			if err != nil {
				return nil, fmt.Errorf("tool %d (custom_code_interpreter): %w", i+1, err)
			}
			described = append(described, fmt.Sprintf(
				"custom_code_interpreter(server_label=%q, require_approval=%q)",
				getStr(tool, "server_label"),
				approval,
			))
		case "image_generation":
			described = append(described, fmt.Sprintf(
				"image_generation(quality=%q, size=%q)",
				getStr(tool, "quality"),
				getStr(tool, "size"),
			))
		case "function":
			fn := getMap(tool, "function")
			described = append(described, fmt.Sprintf(
				"function(name=%q, execution=caller)",
				getStr(fn, "name"),
			))
		default:
			return nil, errs.ToolBuild("tool[%d]: unsupported tool type %q", i, toolType)
		}
	}
	return described, nil
}

func describeMCPApproval(tool map[string]interface{}) (string, error) {
	approval, err := mcpApprovalMode(tool)
	if err != nil {
		return "", err
	}
	if mode, ok := approval.(string); ok {
		return mode, nil
	}
	policy := approval.(map[string]interface{})
	parts := make([]string, 0, len(policy))
	for _, mode := range []string{"always", "never"} {
		raw, found := policy[mode]
		if !found {
			continue
		}
		filter := raw.(map[string]interface{})
		selections := make([]string, 0, 2)
		if names, ok := stringList(filter["tool_names"]); ok {
			selections = append(selections, "tool_names=["+strings.Join(names, ", ")+"]")
		}
		if readOnly, found := filter["read_only"].(bool); found {
			selections = append(selections, fmt.Sprintf("read_only=%t", readOnly))
		}
		parts = append(parts, mode+"("+strings.Join(selections, ", ")+")")
	}
	return strings.Join(parts, "; "), nil
}

// BuildTools converts manifest tool entries into Foundry wire-format objects.
func BuildTools(tools []map[string]interface{}, baseDir string) ([]interface{}, error) {
	return BuildToolsForProject(tools, baseDir, "")
}

// BuildToolsForProject converts manifest tool entries into Foundry wire-format
// objects and can derive same-project Toolbox endpoints.
func BuildToolsForProject(
	tools []map[string]interface{},
	baseDir string,
	projectEndpoint string,
) ([]interface{}, error) {
	var built []interface{}
	for i, tool := range tools {
		toolType, _ := tool["type"].(string)
		var result interface{}
		var err error
		switch toolType {
		case "code_interpreter":
			result = buildCodeInterpreter(tool)
		case "file_search":
			ids := getStrSlice(tool, "vector_store_ids")
			managedName := getStr(tool, "vector_store")
			if len(ids) == 0 && managedName == "" {
				return nil, errs.ToolBuild(
					"tool[%d] (file_search): provide \"vector_store_ids\" or \"vector_store\"",
					i,
				)
			}
			if len(ids) > 0 && managedName != "" {
				return nil, errs.ToolBuild(
					"tool[%d] (file_search): \"vector_store_ids\" and \"vector_store\" are mutually exclusive",
					i,
				)
			}
			if managedName != "" {
				result = map[string]interface{}{
					"type":                "file_search",
					managedVectorStoreKey: managedName,
				}
			} else {
				result = map[string]interface{}{
					"type":             "file_search",
					"vector_store_ids": ids,
				}
			}
		case "openapi":
			result, err = buildOpenAPI(tool, baseDir)
		case "mcp":
			result, err = buildMCP(tool)
		case "azure_function":
			result, err = buildAzureFunction(tool)
		case "bing_grounding":
			result, err = buildBingGrounding(tool, false)
		case "bing_custom_search_preview":
			result, err = buildBingGrounding(tool, true)
		case "toolbox":
			result, err = buildToolboxAttachment(tool, projectEndpoint)
		case "web_search":
			result = buildWebSearch(tool)
		case "azure_ai_search":
			result, err = buildAzureAISearch(tool)
		case "a2a_preview":
			result, err = buildA2A(tool)
		case "browser_automation_preview":
			result, err = buildBrowserAutomation(tool)
		case "fabric_iq_preview":
			result, err = buildFabricIQ(tool)
		case "work_iq_preview":
			result, err = buildConnectionTool(toolType, tool)
		case "sharepoint_grounding_preview":
			result, err = buildSharePoint(tool)
		case "computer_use_preview":
			result, err = buildComputerUse(tool)
		case "custom_code_interpreter":
			result, err = buildCustomCodeInterpreter(tool)
		case "memory_search_preview":
			result, err = buildMemorySearch(tool)
		case "image_generation":
			result = buildImageGeneration(tool)
		case "function":
			result, err = buildFunction(tool)
		default:
			return nil, errs.ToolBuild("tool[%d]: unsupported tool type %q", i, toolType)
		}
		if err != nil {
			// A containment failure (traversal, symlink or junction escape) must
			// keep its security kind so the process exits with the security exit
			// code instead of the generic tool-build code.
			if errs.IsKind(err, "security") {
				return nil, errs.SecurityWrap(err, "tool[%d] (%s)", i, toolType)
			}
			return nil, errs.ToolBuild("tool[%d] (%s): %v", i, toolType, err)
		}
		built = append(built, result)
	}
	return built, nil
}

// ManagedVectorStoreNames returns the logical grounding stores required by a
// built tool list.
func ManagedVectorStoreNames(built []interface{}) []string {
	seen := map[string]struct{}{}
	for _, raw := range built {
		tool, _ := raw.(map[string]interface{})
		name := getStr(tool, managedVectorStoreKey)
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// ResolveManagedVectorStores returns wire-safe tools with logical managed
// grounding names replaced by concrete vector-store IDs.
func ResolveManagedVectorStores(
	built []interface{},
	vectorStoreIDs map[string]string,
) ([]interface{}, error) {
	data, err := json.Marshal(built)
	if err != nil {
		return nil, errs.ToolBuild("failed to copy tool payloads: %v", err)
	}
	var resolved []interface{}
	if err := json.Unmarshal(data, &resolved); err != nil {
		return nil, errs.ToolBuild("failed to copy tool payloads: %v", err)
	}
	for index, raw := range resolved {
		tool, ok := raw.(map[string]interface{})
		if !ok {
			return nil, errs.ToolBuild("tool[%d]: built payload is not an object", index)
		}
		name := getStr(tool, managedVectorStoreKey)
		if name == "" {
			continue
		}
		id := strings.TrimSpace(vectorStoreIDs[name])
		if id == "" {
			return nil, errs.NotFound(
				"managed vector store %q has no resolved Foundry ID",
				name,
			)
		}
		delete(tool, managedVectorStoreKey)
		tool["vector_store_ids"] = []interface{}{id}
	}
	return resolved, nil
}

// PreviewToolTypes returns sorted preview tool types used by a prompt agent.
func PreviewToolTypes(tools []map[string]interface{}) []string {
	seen := make(map[string]struct{})
	for _, tool := range tools {
		toolType := getStr(tool, "type")
		if _, preview := previewToolTypes[toolType]; preview {
			seen[toolType] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for toolType := range seen {
		result = append(result, toolType)
	}
	sort.Strings(result)
	return result
}

func buildCodeInterpreter(tool map[string]interface{}) interface{} {
	result := map[string]interface{}{"type": "code_interpreter"}
	copyOptional(result, tool, "container")
	return result
}

// buildMCP validates the MCP tool's structural contract before it is deployed.
// Destination-host approval is enforced separately at preflight/deploy time.
func buildMCP(tool map[string]interface{}) (interface{}, error) {
	label := getStr(tool, "server_label")
	if label == "" {
		return nil, fmt.Errorf("missing required field \"server_label\"")
	}
	serverURL, err := requireAbsoluteHTTPSURL(getStr(tool, "server_url"), "server_url")
	if err != nil {
		return nil, err
	}
	approval, err := mcpApprovalMode(tool)
	if err != nil {
		return nil, err
	}
	result := map[string]interface{}{
		"type":             "mcp",
		"server_label":     label,
		"server_url":       serverURL,
		"require_approval": approval,
	}
	headers, err := mcpHeaders(tool)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		result["headers"] = headers
	}
	allowedTools, present, err := mcpAllowedTools(tool)
	if err != nil {
		return nil, err
	}
	if present {
		result["allowed_tools"] = allowedTools
	}
	copyOptional(
		result,
		tool,
		"project_connection_id",
		"tool_configs",
	)
	return result, nil
}

// mcpApprovalMode fails closed on unsupported scalar values or ambiguous
// per-tool filters.
func mcpApprovalMode(tool map[string]interface{}) (interface{}, error) {
	value, present := tool["require_approval"]
	if !present || value == nil {
		return DefaultMCPApprovalMode, nil
	}
	if mode, ok := value.(string); ok {
		if mode == "" {
			return DefaultMCPApprovalMode, nil
		}
		for _, supported := range SupportedMCPApprovalModes {
			if mode == supported {
				return mode, nil
			}
		}
		return nil, fmt.Errorf(
			"require_approval %q is not supported; use one of %s",
			mode,
			strings.Join(SupportedMCPApprovalModes, ", "),
		)
	}
	policy, ok := value.(map[string]interface{})
	if !ok || len(policy) == 0 {
		return nil, fmt.Errorf(
			"require_approval must be one of %s or a non-empty always/never filter object",
			strings.Join(SupportedMCPApprovalModes, ", "),
		)
	}
	result := make(map[string]interface{}, len(policy))
	filters := make(map[string]map[string]interface{}, len(policy))
	for key, raw := range policy {
		if key != "always" && key != "never" {
			return nil, fmt.Errorf(
				"require_approval contains unsupported key %q; use always or never",
				key,
			)
		}
		filter, err := normalizeMCPToolFilter(raw, "require_approval."+key)
		if err != nil {
			return nil, err
		}
		result[key] = filter
		filters[key] = filter
	}
	if always, found := filters["always"]; found {
		if never, found := filters["never"]; found && mcpFiltersOverlap(always, never) {
			return nil, fmt.Errorf(
				"require_approval always and never filters overlap; split them into non-overlapping policies",
			)
		}
	}
	return result, nil
}

func mcpAllowedTools(tool map[string]interface{}) (interface{}, bool, error) {
	raw, present := tool["allowed_tools"]
	if !present || raw == nil {
		return nil, false, nil
	}
	if names, ok := stringList(raw); ok {
		normalized, err := normalizeMCPToolNames(names, "allowed_tools")
		return normalized, true, err
	}
	filter, err := normalizeMCPToolFilter(raw, "allowed_tools")
	if err != nil {
		return nil, false, err
	}
	return filter, true, nil
}

func normalizeMCPToolFilter(raw interface{}, field string) (map[string]interface{}, error) {
	if names, ok := stringList(raw); ok {
		normalized, err := normalizeMCPToolNames(names, field)
		if err != nil {
			return nil, err
		}
		return map[string]interface{}{"tool_names": normalized}, nil
	}
	source, ok := raw.(map[string]interface{})
	if !ok || len(source) == 0 {
		return nil, fmt.Errorf(
			"%s must be a non-empty tool-name list or filter object",
			field,
		)
	}
	result := make(map[string]interface{}, 2)
	for key := range source {
		if key != "tool_names" && key != "read_only" {
			return nil, fmt.Errorf("%s contains unsupported key %q", field, key)
		}
	}
	if rawNames, found := source["tool_names"]; found {
		names, ok := stringList(rawNames)
		if !ok {
			return nil, fmt.Errorf("%s.tool_names must be an array of strings", field)
		}
		normalized, err := normalizeMCPToolNames(names, field+".tool_names")
		if err != nil {
			return nil, err
		}
		result["tool_names"] = normalized
	}
	if rawReadOnly, found := source["read_only"]; found {
		readOnly, ok := rawReadOnly.(bool)
		if !ok {
			return nil, fmt.Errorf("%s.read_only must be a boolean", field)
		}
		result["read_only"] = readOnly
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s must select tool_names, read_only, or both", field)
	}
	return result, nil
}

func normalizeMCPToolNames(names []string, field string) ([]string, error) {
	seen := make(map[string]struct{}, len(names))
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("%s contains an empty or invalid tool name", field)
		}
		if _, found := seen[name]; found {
			return nil, fmt.Errorf("%s contains duplicate tool name %q", field, name)
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%s must contain at least one tool name", field)
	}
	return result, nil
}

func mcpFiltersOverlap(left, right map[string]interface{}) bool {
	leftReadOnly, leftHasReadOnly := left["read_only"].(bool)
	rightReadOnly, rightHasReadOnly := right["read_only"].(bool)
	if leftHasReadOnly && rightHasReadOnly && leftReadOnly == rightReadOnly {
		return true
	}
	leftNames, _ := stringList(left["tool_names"])
	rightNames, _ := stringList(right["tool_names"])
	rightSet := make(map[string]struct{}, len(rightNames))
	for _, name := range rightNames {
		rightSet[name] = struct{}{}
	}
	for _, name := range leftNames {
		if _, found := rightSet[name]; found {
			return true
		}
	}
	return false
}

func mcpHeaders(tool map[string]interface{}) (map[string]string, error) {
	raw, present := tool["headers"]
	if !present || raw == nil {
		return nil, nil
	}
	source, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("headers must be an object with string values")
	}
	result := make(map[string]string, len(source))
	seen := make(map[string]struct{}, len(source))
	for name, rawValue := range source {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if !mcpHeaderNamePattern.MatchString(name) || normalized == "" {
			return nil, fmt.Errorf("headers contains invalid HTTP header name %q", name)
		}
		if _, blocked := blockedMCPHeaderNames[normalized]; blocked {
			return nil, errs.Security(
				"MCP header %q can carry credentials; use project_connection_id instead of storing it in the agent definition",
				name,
			)
		}
		if _, found := seen[normalized]; found {
			return nil, fmt.Errorf("headers contains duplicate case-insensitive name %q", name)
		}
		value, ok := rawValue.(string)
		if !ok {
			return nil, fmt.Errorf("headers.%s must be a string", name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("headers.%s must not contain line breaks or NUL bytes", name)
		}
		if len(value) > 8192 {
			return nil, fmt.Errorf("headers.%s exceeds the 8192-byte safety limit", name)
		}
		seen[normalized] = struct{}{}
		result[name] = value
	}
	return result, nil
}

func stringList(value interface{}) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result = append(result, text)
		}
		return result, true
	default:
		return nil, false
	}
}

// requireAbsoluteHTTPSURL rejects relative, non-https, and credential-bearing URLs.
func requireAbsoluteHTTPSURL(rawURL, field string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("missing required field %q", field)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%s %q is not a valid URL: %v", field, rawURL, err)
	}
	if !parsed.IsAbs() || !strings.EqualFold(parsed.Scheme, "https") {
		return "", fmt.Errorf("%s %q must be an absolute https:// URL", field, rawURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("%s %q must not embed credentials", field, rawURL)
	}
	if parsed.Hostname() == "" {
		return "", fmt.Errorf("%s %q has no host", field, rawURL)
	}
	return trimmed, nil
}

func buildOpenAPI(tool map[string]interface{}, baseDir string) (interface{}, error) {
	name := getStr(tool, "name")
	if name == "" {
		return nil, fmt.Errorf("missing required field \"name\"")
	}
	spec, err := loadSpec(tool, baseDir)
	if err != nil {
		return nil, err
	}
	auth, err := buildOpenAPIAuth(tool)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"type": "openapi",
		"openapi": map[string]interface{}{
			"name":        name,
			"spec":        spec,
			"description": getStr(tool, "description"),
			"auth":        auth,
		},
	}, nil
}

func buildToolboxAttachment(
	tool map[string]interface{},
	projectEndpoint string,
) (interface{}, error) {
	name := getStr(tool, "name")
	if name == "" {
		return nil, fmt.Errorf("missing required field \"name\"")
	}
	connectionID := getStr(tool, "project_connection_id")
	if connectionID == "" {
		return nil, fmt.Errorf("missing required field \"project_connection_id\"")
	}
	if strings.TrimSpace(projectEndpoint) == "" {
		return nil, fmt.Errorf(
			"project endpoint is required to derive the Toolbox MCP endpoint",
		)
	}
	serverURL, err := ToolboxEndpoint(projectEndpoint, name, getStr(tool, "version"))
	if err != nil {
		return nil, err
	}
	approval, err := mcpApprovalMode(tool)
	if err != nil {
		return nil, err
	}
	label := getStr(tool, "server_label")
	if label == "" {
		label = name
	}
	result := map[string]interface{}{
		"type":                  "mcp",
		"server_label":          label,
		"server_url":            serverURL,
		"require_approval":      approval,
		"project_connection_id": connectionID,
	}
	headers, err := mcpHeaders(tool)
	if err != nil {
		return nil, err
	}
	if len(headers) > 0 {
		result["headers"] = headers
	}
	allowedTools, present, err := mcpAllowedTools(tool)
	if err != nil {
		return nil, err
	}
	if present {
		result["allowed_tools"] = allowedTools
	}
	copyOptional(result, tool, "tool_configs")
	return result, nil
}

// ToolboxEndpoint returns the documented same-project Toolbox MCP endpoint.
// An empty version follows the Toolbox default_version; a version pins the
// immutable developer endpoint.
func ToolboxEndpoint(projectEndpoint, name, version string) (string, error) {
	projectEndpoint, err := requireAbsoluteHTTPSURL(projectEndpoint, "project endpoint")
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("toolbox name is required")
	}
	path := strings.TrimRight(projectEndpoint, "/") +
		"/toolboxes/" + url.PathEscape(name)
	if strings.TrimSpace(version) != "" {
		path += "/versions/" + url.PathEscape(strings.TrimSpace(version))
	}
	return path + "/mcp?api-version=v1", nil
}

// IsProjectToolboxEndpoint reports whether rawURL is an MCP endpoint under the
// configured Foundry project. Only this narrowly derived endpoint shape is
// treated as an internal destination.
func IsProjectToolboxEndpoint(rawURL, projectEndpoint string) bool {
	project, err := url.Parse(strings.TrimSpace(projectEndpoint))
	if err != nil || project.Scheme != "https" || project.Host == "" {
		return false
	}
	target, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || target.Scheme != project.Scheme || target.Host != project.Host {
		return false
	}
	if target.RawQuery != "api-version=v1" || target.Fragment != "" {
		return false
	}
	basePath := strings.TrimSuffix(project.EscapedPath(), "/")
	relative := strings.TrimPrefix(target.EscapedPath(), basePath)
	if relative == target.EscapedPath() || !strings.HasPrefix(relative, "/toolboxes/") {
		return false
	}
	parts := strings.Split(strings.Trim(relative, "/"), "/")
	if len(parts) == 3 {
		return parts[0] == "toolboxes" && parts[1] != "" && parts[2] == "mcp"
	}
	return len(parts) == 5 &&
		parts[0] == "toolboxes" &&
		parts[1] != "" &&
		parts[2] == "versions" &&
		parts[3] != "" &&
		parts[4] == "mcp"
}

func buildWebSearch(tool map[string]interface{}) interface{} {
	result := map[string]interface{}{"type": "web_search"}
	if source, ok := tool["user_location"].(map[string]interface{}); ok {
		location := make(map[string]interface{}, len(source)+1)
		for key, value := range source {
			location[key] = value
		}
		if getStr(location, "type") == "" {
			location["type"] = "approximate"
		}
		result["user_location"] = location
	}
	copyOptional(result, tool, "custom_search_configuration")
	return result
}

func buildBingGrounding(
	tool map[string]interface{},
	custom bool,
) (interface{}, error) {
	configurations := getMapSlice(tool, "search_configurations")
	if len(configurations) == 0 {
		return nil, fmt.Errorf("missing required field \"search_configurations\"")
	}
	for i, configuration := range configurations {
		if getStr(configuration, "project_connection_id") == "" {
			return nil, fmt.Errorf(
				"search_configurations[%d] is missing required field \"project_connection_id\"",
				i,
			)
		}
		if custom && getStr(configuration, "instance_name") == "" {
			return nil, fmt.Errorf(
				"search_configurations[%d] is missing required field \"instance_name\"",
				i,
			)
		}
	}
	toolType := "bing_grounding"
	if custom {
		toolType = "bing_custom_search_preview"
	}
	return map[string]interface{}{
		"type": toolType,
		toolType: map[string]interface{}{
			"search_configurations": configurations,
		},
	}, nil
}

func buildAzureAISearch(tool map[string]interface{}) (interface{}, error) {
	indexes := getMapSlice(tool, "indexes")
	if len(indexes) == 0 {
		return nil, fmt.Errorf("missing required field \"indexes\"")
	}
	for i, index := range indexes {
		if getStr(index, "project_connection_id") == "" {
			return nil, fmt.Errorf(
				"indexes[%d] is missing required field \"project_connection_id\"",
				i,
			)
		}
		if getStr(index, "index_name") == "" {
			return nil, fmt.Errorf(
				"indexes[%d] is missing required field \"index_name\"",
				i,
			)
		}
	}
	return map[string]interface{}{
		"type":            "azure_ai_search",
		"azure_ai_search": map[string]interface{}{"indexes": indexes},
	}, nil
}

func buildConnectionTool(
	toolType string,
	tool map[string]interface{},
) (interface{}, error) {
	connectionID := getStr(tool, "project_connection_id")
	if connectionID == "" {
		return nil, fmt.Errorf("missing required field \"project_connection_id\"")
	}
	result := map[string]interface{}{
		"type":                  toolType,
		"project_connection_id": connectionID,
	}
	if raw := getStr(tool, "base_url"); raw != "" {
		baseURL, err := requireAbsoluteHTTPSURL(raw, "base_url")
		if err != nil {
			return nil, err
		}
		result["base_url"] = baseURL
	}
	copyOptional(result, tool, "require_approval", "tool_configs")
	return result, nil
}

func buildA2A(tool map[string]interface{}) (interface{}, error) {
	result, err := buildConnectionTool("a2a_preview", tool)
	if err != nil {
		return nil, err
	}
	built := result.(map[string]interface{})

	if raw, exists := tool["agent_card_path"]; exists {
		path, ok := raw.(string)
		if !ok {
			return nil, fmt.Errorf("agent_card_path must be a string")
		}
		validated, err := validateAgentCardPath(path)
		if err != nil {
			return nil, err
		}
		built["agent_card_path"] = validated
	}
	if raw, exists := tool["send_credentials_for_agent_card"]; exists {
		enabled, ok := raw.(bool)
		if !ok {
			return nil, fmt.Errorf("send_credentials_for_agent_card must be a boolean")
		}
		built["send_credentials_for_agent_card"] = enabled
	}
	return built, nil
}

func validateAgentCardPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("agent_card_path must not be empty")
	}
	if strings.Contains(trimmed, `\`) {
		return "", fmt.Errorf("agent_card_path %q must not contain backslashes", raw)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("agent_card_path %q is not a valid URL reference: %v", raw, err)
	}
	if parsed.Fragment != "" {
		return "", fmt.Errorf("agent_card_path %q must not include a fragment", raw)
	}
	if parsed.IsAbs() {
		return requireAbsoluteHTTPSURL(trimmed, "agent_card_path")
	}
	if parsed.Host != "" || strings.HasPrefix(trimmed, "//") {
		return "", fmt.Errorf(
			"agent_card_path %q must be relative or an absolute https:// URL",
			raw,
		)
	}
	if parsed.Path == "" {
		return "", fmt.Errorf("agent_card_path %q must include a path", raw)
	}
	return trimmed, nil
}

func buildBrowserAutomation(tool map[string]interface{}) (interface{}, error) {
	connectionID := getStr(tool, "project_connection_id")
	if connectionID == "" {
		return nil, fmt.Errorf("missing required field \"project_connection_id\"")
	}
	result := map[string]interface{}{
		"type": "browser_automation_preview",
		"browser_automation_preview": map[string]interface{}{
			"connection": map[string]interface{}{
				"project_connection_id": connectionID,
			},
		},
	}
	copyOptional(result, tool, "name", "description")
	return result, nil
}

func buildFabricIQ(tool map[string]interface{}) (interface{}, error) {
	result, err := buildConnectionTool("fabric_iq_preview", tool)
	if err != nil {
		return nil, err
	}
	built := result.(map[string]interface{})
	if raw := getStr(tool, "server_url"); raw != "" {
		serverURL, err := requireAbsoluteHTTPSURL(raw, "server_url")
		if err != nil {
			return nil, err
		}
		built["server_url"] = serverURL
	}
	copyOptional(built, tool, "server_label")
	return built, nil
}

func buildSharePoint(tool map[string]interface{}) (interface{}, error) {
	connectionIDs := getStrSlice(tool, "project_connection_ids")
	if len(connectionIDs) == 0 {
		return nil, fmt.Errorf(
			"missing required field \"project_connection_ids\"",
		)
	}
	connections := make([]interface{}, 0, len(connectionIDs))
	for _, connectionID := range connectionIDs {
		if strings.TrimSpace(connectionID) == "" {
			return nil, fmt.Errorf(
				"project_connection_ids must contain non-empty values",
			)
		}
		connections = append(connections, map[string]interface{}{
			"project_connection_id": connectionID,
		})
	}
	return map[string]interface{}{
		"type": "sharepoint_grounding_preview",
		"sharepoint_grounding_preview": map[string]interface{}{
			"project_connections": connections,
		},
	}, nil
}

func buildComputerUse(tool map[string]interface{}) (interface{}, error) {
	width := getInt(tool, "display_width")
	height := getInt(tool, "display_height")
	environment := getStr(tool, "environment")
	if width <= 0 || height <= 0 || environment == "" {
		return nil, fmt.Errorf(
			"display_width, display_height, and environment are required",
		)
	}
	return map[string]interface{}{
		"type":           "computer_use_preview",
		"display_width":  width,
		"display_height": height,
		"environment":    environment,
	}, nil
}

func buildImageGeneration(tool map[string]interface{}) interface{} {
	result := map[string]interface{}{"type": "image_generation"}
	copyOptional(result, tool, "model", "quality", "size")
	return result
}

func buildFunction(tool map[string]interface{}) (interface{}, error) {
	function := getMap(tool, "function")
	if function == nil || getStr(function, "name") == "" {
		return nil, fmt.Errorf(
			"function.name is required",
		)
	}
	parameters := getMap(function, "parameters")
	if parameters == nil {
		parameters = map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		}
	}
	result := map[string]interface{}{
		"type":       "function",
		"name":       getStr(function, "name"),
		"parameters": parameters,
	}
	copyOptional(result, function, "description", "strict")
	return result, nil
}

func buildOpenAPIAuth(tool map[string]interface{}) (interface{}, error) {
	authMap := getMap(tool, "auth")
	if authMap == nil {
		return map[string]interface{}{"type": "anonymous"}, nil
	}
	authType := getStrDefault(authMap, "type", "anonymous")
	name := getStr(tool, "name")
	switch authType {
	case "anonymous":
		return map[string]interface{}{"type": "anonymous"}, nil
	case "managed_identity":
		audience := getStr(authMap, "audience")
		if audience == "" {
			return nil, fmt.Errorf("openapi tool %q: auth.type 'managed_identity' requires 'audience'", name)
		}
		return map[string]interface{}{
			"type":            "managed_identity",
			"security_scheme": map[string]interface{}{"audience": audience},
		}, nil
	case "connection":
		connID := getStr(authMap, "connection_id")
		if connID == "" {
			return nil, fmt.Errorf("openapi tool %q: auth.type 'connection' requires 'connection_id'", name)
		}
		return map[string]interface{}{
			"type":            "project_connection",
			"security_scheme": map[string]interface{}{"project_connection_id": connID},
		}, nil
	default:
		return nil, fmt.Errorf("openapi tool %q: unsupported auth type %q", name, authType)
	}
}

func loadSpec(tool map[string]interface{}, baseDir string) (interface{}, error) {
	if spec := getMap(tool, "spec"); spec != nil {
		return spec, nil
	}
	specFile := getStr(tool, "spec_file")
	if specFile == "" {
		return nil, fmt.Errorf("openapi tool %q: provide either 'spec' or 'spec_file'", getStr(tool, "name"))
	}
	field := fmt.Sprintf("openapi tool %q spec_file", getStr(tool, "name"))
	data, err := netcheck.ReadContainedFile(baseDir, specFile, field)
	if err != nil {
		return nil, err
	}
	// Try JSON first, then YAML
	var result interface{}
	if err := json.Unmarshal(data, &result); err == nil {
		return result, nil
	}
	if err := yaml.Unmarshal(data, &result); err == nil {
		normalized, err := normalizeYAMLValue(result)
		if err != nil {
			return nil, fmt.Errorf("openapi tool %q: invalid YAML spec: %v", getStr(tool, "name"), err)
		}
		return normalized, nil
	}
	return nil, fmt.Errorf("openapi tool %q: spec_file is neither valid JSON nor YAML", getStr(tool, "name"))
}

func normalizeYAMLValue(value interface{}) (interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	case map[interface{}]interface{}:
		result := make(map[string]interface{}, len(typed))
		for key, item := range typed {
			keyString, err := yamlKeyString(key)
			if err != nil {
				return nil, err
			}
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			result[keyString] = normalized
		}
		return result, nil
	case []interface{}:
		result := make([]interface{}, len(typed))
		for index, item := range typed {
			normalized, err := normalizeYAMLValue(item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

func yamlKeyString(value interface{}) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64, bool:
		return fmt.Sprint(typed), nil
	default:
		return "", fmt.Errorf("mapping key %v has unsupported type %T", value, value)
	}
}

func buildAzureFunction(tool map[string]interface{}) (interface{}, error) {
	fn := getMap(tool, "function")
	if fn == nil {
		return nil, fmt.Errorf("missing required field \"function\"")
	}
	inQueue := getMap(tool, "input_queue")
	if inQueue == nil {
		return nil, fmt.Errorf("missing required field \"input_queue\"")
	}
	outQueue := getMap(tool, "output_queue")
	if outQueue == nil {
		return nil, fmt.Errorf("missing required field \"output_queue\"")
	}

	params := getMap(fn, "parameters")
	if params == nil {
		params = map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
	}

	return map[string]interface{}{
		"type": "azure_function",
		"azure_function": map[string]interface{}{
			"input_binding": map[string]interface{}{
				"type": "storage_queue",
				"storage_queue": map[string]interface{}{
					"queue_name":             getStr(inQueue, "name"),
					"queue_service_endpoint": getStr(inQueue, "service_endpoint"),
				},
			},
			"output_binding": map[string]interface{}{
				"type": "storage_queue",
				"storage_queue": map[string]interface{}{
					"queue_name":             getStr(outQueue, "name"),
					"queue_service_endpoint": getStr(outQueue, "service_endpoint"),
				},
			},
			"function": map[string]interface{}{
				"name":        getStr(fn, "name"),
				"description": getStr(fn, "description"),
				"parameters":  params,
			},
		},
	}, nil
}

func openapiAuthType(tool map[string]interface{}) string {
	auth := getMap(tool, "auth")
	if auth == nil {
		return "anonymous"
	}
	return getStrDefault(auth, "type", "anonymous")
}

// Helpers
func getStr(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
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

func getStrDefault(m map[string]interface{}, key, def string) string {
	s := getStr(m, key)
	if s == "" {
		return def
	}
	return s
}

func getMap(m map[string]interface{}, key string) map[string]interface{} {
	if m == nil {
		return nil
	}
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

func getMapSlice(m map[string]interface{}, key string) []map[string]interface{} {
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
		if object, ok := item.(map[string]interface{}); ok {
			result = append(result, object)
		}
	}
	return result
}

func getInt(m map[string]interface{}, key string) int {
	if m == nil {
		return 0
	}
	switch value := m[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func copyOptional(
	destination map[string]interface{},
	source map[string]interface{},
	keys ...string,
) {
	for _, key := range keys {
		if value, found := source[key]; found && value != nil {
			destination[key] = value
		}
	}
}

func joinStr(s []string) string {
	result := ""
	for i, v := range s {
		if i > 0 {
			result += ", "
		}
		result += v
	}
	return result
}
