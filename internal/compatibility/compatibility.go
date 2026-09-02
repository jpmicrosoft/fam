// Package compatibility evaluates the documented Foundry tool support matrix.
package compatibility

import "strings"

const (
	SourceURL        = "https://learn.microsoft.com/azure/foundry/agents/concepts/limits-quotas-regions#tool-support-by-region-and-model"
	MemorySourceURL  = "https://learn.microsoft.com/azure/foundry/agents/concepts/what-is-memory#region-availability"
	SourceUpdated    = "2026-08-04"
	MemorySourceDate = "2026-08-10"
)

const (
	StatusSupported   = "supported"
	StatusUnsupported = "unsupported"
	StatusUnknown     = "unknown"
)

type Result struct {
	Tool         string `json:"tool" yaml:"tool"`
	MatrixTool   string `json:"matrixTool,omitempty" yaml:"matrixTool,omitempty"`
	RegionStatus string `json:"regionStatus" yaml:"regionStatus"`
	ModelStatus  string `json:"modelStatus" yaml:"modelStatus"`
}

var matrixToolByManifestType = map[string]string{
	"a2a":                          "a2a",
	"a2a_preview":                  "a2a",
	"azure_ai_search":              "azure_ai_search",
	"azure_function":               "azure_function",
	"bing_custom_search":           "bing_custom_search",
	"bing_custom_search_preview":   "bing_custom_search",
	"bing_grounding":               "bing_grounding",
	"browser_automation_preview":   "browser_automation",
	"code_interpreter":             "code_interpreter",
	"computer_use_preview":         "computer_use",
	"custom_code_interpreter":      "mcp",
	"fabric_iq_preview":            "fabric",
	"file_search":                  "file_search",
	"function":                     "function",
	"image_generation":             "image_generation",
	"mcp":                          "mcp",
	"memory_search_preview":        "memory",
	"openapi":                      "openapi",
	"sharepoint_grounding_preview": "sharepoint",
	"toolbox":                      "mcp",
	"web_search":                   "web_search",
	"work_iq_preview":              "work_iq",
}

var documentedRegions = stringSet(
	"australiaeast",
	"brazilsouth",
	"canadaeast",
	"eastus",
	"eastus2",
	"francecentral",
	"germanywestcentral",
	"italynorth",
	"japaneast",
	"koreacentral",
	"northcentralus",
	"norwayeast",
	"polandcentral",
	"southafricanorth",
	"southcentralus",
	"southeastasia",
	"southindia",
	"spaincentral",
	"swedencentral",
	"switzerlandnorth",
	"uaenorth",
	"uksouth",
	"westus",
	"westus3",
)

var regionUnsupported = map[string]map[string]struct{}{
	"computer_use": stringSetExcept(documentedRegions, "eastus2", "southindia", "swedencentral"),
	"file_search": stringSet(
		"brazilsouth",
		"italynorth",
	),
	"function": stringSet(
		"brazilsouth",
		"northcentralus",
		"southcentralus",
		"westus",
	),
}

var memorySupportedRegions = stringSet(
	"australiaeast",
	"brazilsouth",
	"canadaeast",
	"eastus2",
	"francecentral",
	"italynorth",
	"japaneast",
	"koreacentral",
	"northcentralus",
	"norwayeast",
	"southafricanorth",
	"southindia",
	"swedencentral",
	"switzerlandnorth",
	"uaenorth",
	"uksouth",
	"westus",
	"westus2",
	"westus3",
)

var memoryKnownRegions = stringSetUnion(documentedRegions, "westus2")

var modelSupport = map[string]map[string]struct{}{
	"gpt-4.1": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-4.1-mini": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-4.1-nano": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-4o": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-4o-mini": support(
		"a2a", "bing_custom_search", "bing_grounding", "browser_automation",
		"code_interpreter", "fabric", "file_search", "function", "mcp",
		"openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "image_generation", "mcp", "openapi", "sharepoint",
		"web_search", "work_iq",
	),
	"gpt-5-mini":  support("code_interpreter", "file_search", "mcp", "web_search", "work_iq"),
	"gpt-5-codex": support("code_interpreter", "file_search", "mcp", "work_iq"),
	"gpt-5-chat":  support("code_interpreter", "file_search", "work_iq"),
	"gpt-5-pro":   support("code_interpreter", "file_search"),
	"gpt-5.1": support(
		"azure_ai_search", "azure_function", "bing_grounding", "code_interpreter",
		"fabric", "file_search", "function", "mcp", "openapi", "sharepoint",
		"web_search", "work_iq",
	),
	"gpt-5.2": support(
		"azure_ai_search", "azure_function", "bing_grounding", "code_interpreter",
		"fabric", "file_search", "function", "mcp", "openapi", "sharepoint",
		"web_search", "work_iq",
	),
	"gpt-5.2-chat": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"function", "mcp", "openapi", "web_search", "work_iq",
	),
	"gpt-5.3-chat": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.3-codex": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.4": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.4-mini": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.4-nano": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.4-pro": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-5.5": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-chat-latest": support(
		"a2a", "azure_ai_search", "bing_custom_search", "bing_grounding",
		"browser_automation", "code_interpreter", "fabric", "file_search",
		"mcp", "openapi", "sharepoint", "web_search", "work_iq",
	),
	"gpt-oss-120b": support("code_interpreter", "file_search", "function", "mcp", "work_iq"),
	"model-router": support(
		"bing_custom_search", "bing_grounding", "browser_automation",
		"code_interpreter", "fabric", "file_search", "function", "mcp",
		"openapi", "sharepoint", "work_iq",
	),
	"o1": support(
		"azure_ai_search", "bing_custom_search", "browser_automation",
		"code_interpreter", "file_search", "function", "mcp", "sharepoint",
		"web_search", "work_iq",
	),
	"o3": support(
		"a2a", "azure_ai_search", "bing_custom_search", "browser_automation",
		"code_interpreter", "fabric", "file_search", "function", "mcp",
		"openapi", "web_search", "work_iq",
	),
	"o3-mini": support(
		"a2a", "bing_custom_search", "bing_grounding", "browser_automation",
		"code_interpreter", "fabric", "file_search", "work_iq",
	),
	"o4-mini": support(
		"a2a", "bing_custom_search", "bing_grounding", "browser_automation",
		"code_interpreter", "fabric", "file_search", "function", "mcp",
		"sharepoint", "web_search", "work_iq",
	),
	"computer-use-preview": support("computer_use"),
}

func Check(model, region, toolType string) Result {
	matrixTool := matrixToolByManifestType[toolType]
	result := Result{
		Tool:         toolType,
		MatrixTool:   matrixTool,
		RegionStatus: StatusUnknown,
		ModelStatus:  StatusUnknown,
	}
	if matrixTool == "" {
		return result
	}
	normalizedRegion := normalize(region)
	if matrixTool == "memory" {
		if _, known := memoryKnownRegions[normalizedRegion]; known {
			result.RegionStatus = StatusUnsupported
			if _, supported := memorySupportedRegions[normalizedRegion]; supported {
				result.RegionStatus = StatusSupported
			}
		}
		return result
	}
	if _, documented := documentedRegions[normalizedRegion]; documented {
		result.RegionStatus = StatusSupported
		if unsupported := regionUnsupported[matrixTool]; unsupported != nil {
			if _, exists := unsupported[normalizedRegion]; exists {
				result.RegionStatus = StatusUnsupported
			}
		}
		if matrixTool == "azure_function" || matrixTool == "work_iq" {
			result.RegionStatus = StatusUnknown
		}
	}
	if supported, documented := modelSupport[strings.ToLower(strings.TrimSpace(model))]; documented {
		result.ModelStatus = StatusUnsupported
		if _, exists := supported[matrixTool]; exists {
			result.ModelStatus = StatusSupported
		}
	}
	return result
}

func support(values ...string) map[string]struct{} {
	return stringSet(values...)
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func stringSetExcept(all map[string]struct{}, allowed ...string) map[string]struct{} {
	allowedSet := stringSet(allowed...)
	result := make(map[string]struct{}, len(all))
	for value := range all {
		if _, exists := allowedSet[value]; !exists {
			result[value] = struct{}{}
		}
	}
	return result
}

func stringSetUnion(all map[string]struct{}, values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(all)+len(values))
	for value := range all {
		result[value] = struct{}{}
	}
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalize(value string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(
		strings.ToLower(strings.TrimSpace(value)),
	)
}
