package project

import (
	"context"
	"net/http"
	"testing"
)

// multiEndpointProject is an ARM project payload that advertises several
// data-plane endpoints, as Foundry accounts do.
func multiEndpointProject() map[string]interface{} {
	return map[string]interface{}{
		"location": "eastus",
		"properties": map[string]interface{}{
			"endpoints": map[string]interface{}{
				"Zulu Foundry API":  "https://zulu.services.ai.azure.com/api/projects/proj",
				"AI Foundry API":    "https://alpha.services.ai.azure.com/api/projects/proj",
				"Model Inference":   "https://alpha.services.ai.azure.com/models",
				"Mike Foundry API":  "https://mike.services.ai.azure.com/api/projects/proj",
				"Azure OpenAI":      "https://alpha.openai.azure.com",
				"Bravo Foundry API": "https://bravo.services.ai.azure.com/api/projects/proj",
			},
		},
	}
}

// TestProjectEndpointSelectionIsDeterministic guards against Go's randomized map
// iteration choosing a different data-plane endpoint on each run, which would
// make receipts, diagnostics, and host validation non-reproducible.
func TestProjectEndpointSelectionIsDeterministic(t *testing.T) {
	var selected string
	for attempt := 0; attempt < 50; attempt++ {
		project := baseProject()
		project.Endpoint = ""
		project.AccountEndpoint = ""
		httpClient := &recordingHTTPClient{responses: []*http.Response{
			response(200, multiEndpointProject()),
		}}
		state, err := InspectProjectContext(context.Background(), project, &recordingCredential{}, httpClient)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Exists {
			t.Fatal("expected the project to exist")
		}
		if attempt == 0 {
			selected = state.Endpoint
			continue
		}
		if state.Endpoint != selected {
			t.Fatalf("endpoint selection is non-deterministic: %q then %q", selected, state.Endpoint)
		}
	}
	// Sorted key order is the documented tie-break.
	if selected != "https://alpha.services.ai.azure.com/api/projects/proj" {
		t.Fatalf("unexpected endpoint selection: %q", selected)
	}
}

// TestEnsureProjectEndpointSelectionIsDeterministic covers the creation path,
// which resolves the endpoint from the ARM creation response.
func TestEnsureProjectEndpointSelectionIsDeterministic(t *testing.T) {
	var selected string
	for attempt := 0; attempt < 25; attempt++ {
		project := baseProject()
		project.Endpoint = ""
		project.AccountEndpoint = ""
		httpClient := &recordingHTTPClient{responses: []*http.Response{
			response(200, multiEndpointProject()),
		}}
		endpoint, created, err := EnsureProjectContext(
			context.Background(), project, &recordingCredential{}, httpClient,
		)
		if err != nil {
			t.Fatal(err)
		}
		if created {
			t.Fatal("an existing project must not be reported as created")
		}
		if attempt == 0 {
			selected = endpoint
			continue
		}
		if endpoint != selected {
			t.Fatalf("endpoint selection is non-deterministic: %q then %q", selected, endpoint)
		}
	}
	if selected != "https://alpha.services.ai.azure.com/api/projects/proj" {
		t.Fatalf("unexpected endpoint selection: %q", selected)
	}
}
