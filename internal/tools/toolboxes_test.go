package tools

import (
	"reflect"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestBuildToolboxesProducesImmutablePayload(t *testing.T) {
	raw := []map[string]interface{}{{
		"name":          "operations",
		"description":   "Operational tools.",
		"rai_policy_id": "rai-policy",
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search", "name": "web"},
			map[string]interface{}{"type": "toolbox_search"},
			map[string]interface{}{"type": "reminder_preview"},
			map[string]interface{}{
				"type":         "mcp",
				"name":         "docs",
				"server_label": "docs",
				"server_url":   "https://mcp.example.test/tools",
			},
			map[string]interface{}{
				"type": "openapi",
				"name": "orders",
				"spec": map[string]interface{}{
					"openapi": "3.0.0",
					"servers": []interface{}{
						map[string]interface{}{"url": "https://api.example.test"},
					},
					"paths": map[string]interface{}{},
				},
				"auth": map[string]interface{}{
					"type":     "managed_identity",
					"audience": "api://orders",
				},
			},
		},
		"skills": []interface{}{
			map[string]interface{}{"name": "triage", "version": "3"},
		},
	}}
	definitions, err := BuildToolboxes(raw, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 1 {
		t.Fatalf("unexpected definitions: %#v", definitions)
	}
	definition := definitions[0]
	if !definition.RequiresPreview ||
		definition.PreviewHeader() != "Skills=V1Preview" ||
		!reflect.DeepEqual(
			definition.PreviewCapabilities,
			[]string{"reminder_preview", "skill_reference"},
		) {
		t.Fatalf("preview metadata mismatch: %#v", definition)
	}
	payload := definition.Payload()
	if payload["description"] != "Operational tools." {
		t.Fatalf("description missing from payload: %#v", payload)
	}
	policies := payload["policies"].(map[string]interface{})
	rai := policies["rai_config"].(map[string]interface{})
	if rai["rai_policy_name"] != "rai-policy" {
		t.Fatalf("RAI policy missing: %#v", payload)
	}
	skills := payload["skills"].([]ToolboxSkill)
	if len(skills) != 1 || skills[0].Type != "skill_reference" ||
		skills[0].Name != "triage" || skills[0].Version != "3" {
		t.Fatalf("skill reference mismatch: %#v", skills)
	}
	openAPI := definition.Tools[4].(map[string]interface{})
	openAPIDefinition := openAPI["openapi"].(map[string]interface{})
	if openAPI["type"] != "openapi" ||
		openAPIDefinition["name"] != "orders" ||
		openAPIDefinition["auth"] == nil ||
		openAPIDefinition["spec"] == nil {
		t.Fatalf("Toolbox OpenAPI payload mismatch: %#v", openAPI)
	}
}

func TestToolboxDestinationsIncludesExternalTools(t *testing.T) {
	definitions, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "operations",
		"description": "Operational tools.",
		"tools": []interface{}{
			map[string]interface{}{
				"type":         "mcp",
				"server_label": "docs",
				"server_url":   "https://mcp.example.test/tools",
			},
			map[string]interface{}{
				"type":                  "a2a_preview",
				"project_connection_id": "a2a-connection",
				"base_url":              "https://a2a.example.test",
				"agent_card_path":       "https://cards.example.test/agent-card.json",
			},
			map[string]interface{}{
				"type":                  "work_iq_preview",
				"project_connection_id": "work-connection",
				"base_url":              "https://work.example.test",
			},
			map[string]interface{}{
				"type": "openapi",
				"name": "orders",
				"spec": map[string]interface{}{
					"openapi": "3.0.0",
					"servers": []interface{}{
						map[string]interface{}{"url": "https://api.example.test"},
					},
					"paths": map[string]interface{}{},
				},
			},
		},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destinations, err := ToolboxDestinations(definitions)
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 5 ||
		destinations[0].URL != "https://mcp.example.test/tools" ||
		destinations[1].URL != "https://a2a.example.test" ||
		destinations[2].URL != "https://cards.example.test/agent-card.json" ||
		destinations[3].URL != "https://work.example.test" ||
		destinations[4].URL != "https://api.example.test" {
		t.Fatalf("unexpected destinations: %#v", destinations)
	}
}

func TestBuildToolboxA2AForwardsAgentCardConfiguration(t *testing.T) {
	definitions, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "delegation",
		"description": "Delegate to a protected remote agent.",
		"tools": []interface{}{map[string]interface{}{
			"type":                            "a2a_preview",
			"project_connection_id":           "a2a-connection",
			"base_url":                        "https://a2a.example.test",
			"agent_card_path":                 "/private/agent-card.json",
			"send_credentials_for_agent_card": true,
			"require_approval":                "always",
			"tool_configs":                    map[string]interface{}{"mode": "strict"},
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, definitions[0].Tools, []interface{}{map[string]interface{}{
		"type":                            "a2a_preview",
		"project_connection_id":           "a2a-connection",
		"base_url":                        "https://a2a.example.test",
		"agent_card_path":                 "/private/agent-card.json",
		"send_credentials_for_agent_card": true,
		"require_approval":                "always",
		"tool_configs":                    map[string]interface{}{"mode": "strict"},
	}})
}

func TestBuildToolboxBingCustomSearchProducesDocumentedWireFormat(t *testing.T) {
	definitions, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "research",
		"description": "Search curated domains.",
		"tools": []interface{}{map[string]interface{}{
			"type": "bing_custom_search",
			"name": "docs-search",
			"custom_search_configuration": map[string]interface{}{
				"project_connection_id": "bing-custom-connection",
				"instance_name":         "docs",
			},
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, definitions[0].Tools, []interface{}{map[string]interface{}{
		"type": "web_search",
		"name": "docs-search",
		"custom_search_configuration": map[string]interface{}{
			"project_connection_id": "bing-custom-connection",
			"instance_name":         "docs",
		},
	}})
	if definitions[0].RequiresPreview ||
		len(definitions[0].PreviewCapabilities) != 0 {
		t.Fatalf("did not expect preview metadata: %#v", definitions[0])
	}
}

func TestBuildToolboxesRejectsDuplicateNamesAndUnnamedTypes(t *testing.T) {
	_, err := BuildToolboxes([]map[string]interface{}{
		{
			"name":        "operations",
			"description": "One.",
			"skills":      []interface{}{map[string]interface{}{"name": "one"}},
		},
		{
			"name":        "OPERATIONS",
			"description": "Two.",
			"skills":      []interface{}{map[string]interface{}{"name": "two"}},
		},
	}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "duplicate toolbox name") {
		t.Fatalf("expected duplicate name rejection, got %v", err)
	}

	_, err = BuildToolboxes([]map[string]interface{}{{
		"name":        "research",
		"description": "Search.",
		"tools": []interface{}{map[string]interface{}{
			"type": "bing_custom_search",
			"name": "docs-search",
		}},
	}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "custom_search_configuration") {
		t.Fatalf("expected missing Bing configuration rejection, got %v", err)
	}

	_, err = BuildToolboxes([]map[string]interface{}{{
		"name":        "operations",
		"description": "Tools.",
		"tools": []interface{}{
			map[string]interface{}{"type": "web_search"},
			map[string]interface{}{"type": "code_interpreter"},
		},
	}}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "multiple tools without identifiers") {
		t.Fatalf("expected unnamed tool rejection, got %v", err)
	}
}

func TestBuildToolboxesPreservesSpecContainmentSecurity(t *testing.T) {
	_, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "operations",
		"description": "Tools.",
		"tools": []interface{}{
			map[string]interface{}{
				"type":      "openapi",
				"name":      "orders",
				"spec_file": "../outside.yaml",
			},
		},
	}}, t.TempDir())
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security-kind containment failure, got %v", err)
	}
}

func TestToolboxPayloadEqualIgnoresServiceFields(t *testing.T) {
	definitions, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "operations",
		"description": "Tools.",
		"tools": []interface{}{
			map[string]interface{}{"type": "toolbox_search"},
		},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	remote := definitions[0].Payload()
	remote["id"] = "version-id"
	remote["version"] = "7"
	equal, err := ToolboxPayloadEqual(remote, definitions[0])
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("service-owned fields must not create a managed diff")
	}
	remote["description"] = "Changed."
	equal, err = ToolboxPayloadEqual(remote, definitions[0])
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("managed payload change was not detected")
	}
}

func TestManagedVectorStoreResolutionForToolboxes(t *testing.T) {
	definitions, err := BuildToolboxes([]map[string]interface{}{{
		"name":        "operations",
		"description": "Grounded operations.",
		"tools": []interface{}{map[string]interface{}{
			"type":         "file_search",
			"name":         "documents",
			"vector_store": "product-docs",
		}},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got := ManagedVectorStoreNames(definitions[0].Tools); !reflect.DeepEqual(
		got,
		[]string{"product-docs"},
	) {
		t.Fatalf("unexpected logical names: %#v", got)
	}
	resolved, err := ResolveToolboxManagedVectorStores(
		definitions[0],
		map[string]string{"product-docs": "vs-123"},
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := resolved.Tools[0].(map[string]interface{})
	if got := getStrSlice(payload, "vector_store_ids"); !reflect.DeepEqual(
		got,
		[]string{"vs-123"},
	) {
		t.Fatalf("logical vector store was not resolved: %#v", payload)
	}
	if _, exists := payload[managedVectorStoreKey]; exists {
		t.Fatalf("logical placeholder leaked into the Toolbox payload: %#v", payload)
	}
}
