package tools

// CatalogEntry describes one manifest tool contract supported by the manager.
type CatalogEntry struct {
	Type     string `json:"type" yaml:"type"`
	WireType string `json:"wireType" yaml:"wireType"`
	Preview  bool   `json:"preview" yaml:"preview"`
}

// DirectCatalog returns the manager's built-in prompt-agent tool contracts.
func DirectCatalog() []CatalogEntry {
	result := make([]CatalogEntry, 0, len(SupportedToolTypes))
	for _, toolType := range SupportedToolTypes {
		_, preview := previewToolTypes[toolType]
		wireType := toolType
		if toolType == "custom_code_interpreter" {
			wireType = "mcp"
		}
		result = append(result, CatalogEntry{
			Type:     toolType,
			WireType: wireType,
			Preview:  preview,
		})
	}
	return result
}

// ToolboxCatalog returns the manager's built-in Toolbox tool contracts.
func ToolboxCatalog() []CatalogEntry {
	result := make([]CatalogEntry, 0, len(SupportedToolboxToolTypes))
	for _, toolType := range SupportedToolboxToolTypes {
		_, preview := previewToolboxToolTypes[toolType]
		wireType := toolType
		if toolType == "bing_custom_search" {
			wireType = "web_search"
		}
		result = append(result, CatalogEntry{
			Type:     toolType,
			WireType: wireType,
			Preview:  preview,
		})
	}
	return result
}

// HostedRuntimeCatalog returns application-level tool integrations implemented
// by the manager's Hosted Agent scaffold and workspace contract.
func HostedRuntimeCatalog() []CatalogEntry {
	return []CatalogEntry{
		{
			Type:     "bing_grounding",
			WireType: "bing_grounding",
			Preview:  false,
		},
		{
			Type:     "bing_custom_search",
			WireType: "bing_custom_search",
			Preview:  false,
		},
		{
			Type:     "toolbox",
			WireType: "mcp",
			Preview:  false,
		},
	}
}
