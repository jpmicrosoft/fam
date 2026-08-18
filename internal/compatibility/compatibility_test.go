package compatibility

import "testing"

func TestCheckUsesDocumentedModelAndRegionMatrix(t *testing.T) {
	supported := Check("gpt-4.1", "East US", "mcp")
	if supported.ModelStatus != StatusSupported || supported.RegionStatus != StatusSupported {
		t.Fatalf("expected supported combination: %#v", supported)
	}
	unsupportedModel := Check("gpt-5-pro", "East US", "mcp")
	if unsupportedModel.ModelStatus != StatusUnsupported {
		t.Fatalf("expected unsupported model combination: %#v", unsupportedModel)
	}
	unsupportedRegion := Check("gpt-4.1", "West US", "function")
	if unsupportedRegion.RegionStatus != StatusUnsupported {
		t.Fatalf("expected unsupported region combination: %#v", unsupportedRegion)
	}
}

func TestCheckReturnsUnknownForUnpublishedCombinations(t *testing.T) {
	result := Check("deployment-alias", "unsupported-region", "memory_search_preview")
	if result.ModelStatus != StatusUnknown || result.RegionStatus != StatusUnknown {
		t.Fatalf("unpublished combinations must remain unknown: %#v", result)
	}
}

func TestCheckUsesDocumentedMemoryRegionAvailability(t *testing.T) {
	if result := Check("gpt-5-mini", "East US", "memory_search_preview"); result.RegionStatus != StatusUnsupported || result.ModelStatus != StatusUnknown {
		t.Fatalf("East US must be rejected for Memory: %#v", result)
	}
	if result := Check("gpt-5-mini", "East US 2", "memory_search_preview"); result.RegionStatus != StatusSupported {
		t.Fatalf("East US 2 must be accepted for Memory: %#v", result)
	}
}
