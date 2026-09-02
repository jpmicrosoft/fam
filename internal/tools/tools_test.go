package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
)

func assertJSONEqual(t *testing.T, got, want interface{}) {
	t.Helper()
	normalize := func(value interface{}) interface{} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("failed to marshal test value: %v", err)
		}
		var normalized interface{}
		if err := json.Unmarshal(data, &normalized); err != nil {
			t.Fatalf("failed to normalize test value: %v", err)
		}
		return normalized
	}
	if got, want := normalize(got), normalize(want); !reflect.DeepEqual(got, want) {
		t.Fatalf("wire payload mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestBuildToolsProducesFoundryWireFormats(t *testing.T) {
	manifestTools := []map[string]interface{}{
		{"type": "code_interpreter"},
		{"type": "file_search", "vector_store_ids": []interface{}{"vs-1", "vs-2"}},
		{
			"type":         "mcp",
			"server_label": "docs",
			"server_url":   "https://example.test/mcp",
		},
		{
			"type":         "custom_code_interpreter",
			"server_label": "interpreter",
			"server_url":   "https://interpreter.example.test/mcp",
		},
		{
			"type":              "memory_search_preview",
			"memory_store_name": "assistant-memory",
			"scope":             "{{$userId}}",
			"update_delay":      30,
			"search_options": map[string]interface{}{
				"max_memories": 5,
			},
		},
		{
			"type": "azure_function",
			"function": map[string]interface{}{
				"name":        "process",
				"description": "Process an item.",
			},
			"input_queue": map[string]interface{}{
				"name":             "requests",
				"service_endpoint": "https://storage.queue.core.windows.net",
			},
			"output_queue": map[string]interface{}{
				"name":             "responses",
				"service_endpoint": "https://storage.queue.core.windows.net",
			},
		},
	}

	got, err := BuildTools(manifestTools, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []interface{}{
		map[string]interface{}{"type": "code_interpreter"},
		map[string]interface{}{
			"type":             "file_search",
			"vector_store_ids": []string{"vs-1", "vs-2"},
		},
		map[string]interface{}{
			"type":             "mcp",
			"server_label":     "docs",
			"server_url":       "https://example.test/mcp",
			"require_approval": "always",
		},
		map[string]interface{}{
			"type":             "mcp",
			"server_label":     "interpreter",
			"server_url":       "https://interpreter.example.test/mcp",
			"require_approval": "always",
		},
		map[string]interface{}{
			"type":              "memory_search_preview",
			"memory_store_name": "assistant-memory",
			"scope":             "{{$userId}}",
			"update_delay":      30,
			"search_options": map[string]interface{}{
				"max_memories": 5,
			},
		},
		map[string]interface{}{
			"type": "azure_function",
			"azure_function": map[string]interface{}{
				"input_binding": map[string]interface{}{
					"type": "storage_queue",
					"storage_queue": map[string]interface{}{
						"queue_name":             "requests",
						"queue_service_endpoint": "https://storage.queue.core.windows.net",
					},
				},
				"output_binding": map[string]interface{}{
					"type": "storage_queue",
					"storage_queue": map[string]interface{}{
						"queue_name":             "responses",
						"queue_service_endpoint": "https://storage.queue.core.windows.net",
					},
				},
				"function": map[string]interface{}{
					"name":        "process",
					"description": "Process an item.",
					"parameters": map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
			},
		},
	}
	assertJSONEqual(t, got, want)
}

func TestBuildWebSearchDefaultsApproximateLocationType(t *testing.T) {
	got, err := BuildTools([]map[string]interface{}{{
		"type": "web_search",
		"user_location": map[string]interface{}{
			"country": "US",
		},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, got, []interface{}{map[string]interface{}{
		"type": "web_search",
		"user_location": map[string]interface{}{
			"type":    "approximate",
			"country": "US",
		},
	}})
}

func TestBuildMCPSupportsHeadersFiltersAndPerToolApproval(t *testing.T) {
	built, err := BuildTools([]map[string]interface{}{{
		"type":         "mcp",
		"server_label": "operations",
		"server_url":   "https://operations.example.test/mcp",
		"headers": map[string]interface{}{
			"X-Tenant": "contoso",
		},
		"allowed_tools": map[string]interface{}{
			"tool_names": []interface{}{"read_status", "delete_item"},
		},
		"require_approval": map[string]interface{}{
			"always": map[string]interface{}{
				"tool_names": []interface{}{"delete_item"},
			},
			"never": map[string]interface{}{
				"tool_names": []interface{}{"read_status"},
			},
		},
	}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, built[0], map[string]interface{}{
		"type":         "mcp",
		"server_label": "operations",
		"server_url":   "https://operations.example.test/mcp",
		"headers": map[string]string{
			"X-Tenant": "contoso",
		},
		"allowed_tools": map[string]interface{}{
			"tool_names": []string{"read_status", "delete_item"},
		},
		"require_approval": map[string]interface{}{
			"always": map[string]interface{}{
				"tool_names": []string{"delete_item"},
			},
			"never": map[string]interface{}{
				"tool_names": []string{"read_status"},
			},
		},
	})
}

func TestBuildMCPRejectsCredentialHeadersAndOverlappingApproval(t *testing.T) {
	for _, tool := range []map[string]interface{}{
		{
			"type": "mcp", "server_label": "operations",
			"server_url": "https://operations.example.test/mcp",
			"headers":    map[string]interface{}{"Authorization": "not-a-real-secret"},
		},
		{
			"type": "mcp", "server_label": "operations",
			"server_url": "https://operations.example.test/mcp",
			"headers":    map[string]interface{}{"Ocp-Apim-Subscription-Key": "not-a-real-secret"},
		},
		{
			"type": "mcp", "server_label": "operations",
			"server_url": "https://operations.example.test/mcp",
			"require_approval": map[string]interface{}{
				"always": []interface{}{"delete_item"},
				"never":  []interface{}{"delete_item"},
			},
		},
	} {
		if _, err := BuildTools([]map[string]interface{}{tool}, t.TempDir()); err == nil {
			t.Fatalf("expected invalid MCP policy to fail: %#v", tool)
		}
	}
}

func TestBuildOpenAPIAuthWireFormats(t *testing.T) {
	tests := []struct {
		name string
		auth map[string]interface{}
		want map[string]interface{}
	}{
		{
			name: "anonymous",
			want: map[string]interface{}{"type": "anonymous"},
		},
		{
			name: "managed identity",
			auth: map[string]interface{}{"type": "managed_identity", "audience": "api://orders"},
			want: map[string]interface{}{
				"type":            "managed_identity",
				"security_scheme": map[string]interface{}{"audience": "api://orders"},
			},
		},
		{
			name: "project connection",
			auth: map[string]interface{}{"type": "connection", "connection_id": "conn-123"},
			want: map[string]interface{}{
				"type":            "project_connection",
				"security_scheme": map[string]interface{}{"project_connection_id": "conn-123"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := map[string]interface{}{
				"type": "openapi",
				"name": "orders",
				"spec": map[string]interface{}{"openapi": "3.0.0"},
			}
			if tt.auth != nil {
				tool["auth"] = tt.auth
			}
			built, err := BuildTools([]map[string]interface{}{tool}, t.TempDir())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			openAPI := built[0].(map[string]interface{})["openapi"].(map[string]interface{})
			assertJSONEqual(t, openAPI["auth"], tt.want)
		})
	}
}

func TestBuildOpenAPILoadsContainedYAMLSpec(t *testing.T) {
	base := t.TempDir()
	specDir := filepath.Join(base, "specs")
	if err := os.Mkdir(specDir, 0o700); err != nil {
		t.Fatal(err)
	}
	specPath := filepath.Join(specDir, "orders.yaml")
	spec := "openapi: 3.0.0\ninfo:\n  title: Orders\n  version: 1.0.0\npaths: {}\n"
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}

	built, err := BuildTools([]map[string]interface{}{{
		"type":      "openapi",
		"name":      "orders",
		"spec_file": "specs/orders.yaml",
	}}, base)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	openAPI := built[0].(map[string]interface{})["openapi"].(map[string]interface{})
	loaded := openAPI["spec"].(map[string]interface{})
	if loaded["openapi"] != "3.0.0" {
		t.Fatalf("unexpected loaded spec: %#v", loaded)
	}
}

func TestBuildOpenAPINormalizesNumericYAMLKeys(t *testing.T) {
	base := t.TempDir()
	specPath := filepath.Join(base, "openapi.yaml")
	spec := `openapi: 3.0.0
info:
  title: Orders
  version: 1.0.0
paths:
  /orders:
    get:
      responses:
        200:
          description: OK
`
	if err := os.WriteFile(specPath, []byte(spec), 0o600); err != nil {
		t.Fatal(err)
	}
	built, err := BuildTools([]map[string]interface{}{{
		"type":      "openapi",
		"name":      "orders",
		"spec_file": "openapi.yaml",
	}}, base)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(built)
	if err != nil {
		t.Fatalf("normalized OpenAPI payload is not JSON-compatible: %v", err)
	}
	if !strings.Contains(string(data), `"200"`) {
		t.Fatalf("numeric response key was not normalized: %s", data)
	}
}

func TestAllShippedExamplesBuildTools(t *testing.T) {
	examplesDir := filepath.Join("..", "..", "examples")
	entries, err := os.ReadDir(examplesDir)
	if err != nil {
		t.Fatalf("failed to read examples: %v", err)
	}
	found := false
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}
		// Only agent-manifest examples use this package's schema.
		// examples/publication.example.yaml uses the separate
		// foundry-agent-manager/publication/v1 schema and is intentionally excluded.
		if !strings.HasPrefix(entry.Name(), "agent") {
			continue
		}
		found = true
		t.Run(entry.Name(), func(t *testing.T) {
			path := filepath.Join(examplesDir, entry.Name())
			doc, err := config.LoadManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := config.ValidateManifest(doc); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.ResolveConfig(doc)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := BuildToolsForProject(
				cfg.Tools,
				filepath.Dir(path),
				cfg.Project.Endpoint,
			); err != nil {
				t.Fatal(err)
			}
			if _, err := BuildToolboxes(cfg.Toolboxes, filepath.Dir(path)); err != nil {
				t.Fatal(err)
			}
		})
	}
	if !found {
		t.Fatal("no example manifests found")
	}
}

func TestBuildToolsPreservesContainmentSecurityKind(t *testing.T) {
	tests := map[string]string{
		"parent traversal": "../outside.json",
		"absolute path":    `C:\outside.json`,
		"unc path":         `\\server\share\outside.json`,
	}
	for name, specFile := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := BuildTools([]map[string]interface{}{{
				"type":      "openapi",
				"name":      "orders",
				"spec_file": specFile,
			}}, t.TempDir())
			if err == nil || !errs.IsKind(err, "security") {
				t.Fatalf("a containment failure must keep the security kind, got %v", err)
			}
			if errs.ExitCode(err) != 4 {
				t.Fatalf("expected the security exit code 4, got %d", errs.ExitCode(err))
			}
			if !strings.Contains(err.Error(), "tool[0] (openapi)") {
				t.Fatalf("the error must still locate the tool: %v", err)
			}
		})
	}
}

func TestBuildToolsKeepsToolKindForOrdinaryFailures(t *testing.T) {
	_, err := BuildTools([]map[string]interface{}{{
		"type":      "openapi",
		"name":      "orders",
		"spec_file": "specs/does-not-exist.json",
	}}, t.TempDir())
	if err == nil || !errs.IsKind(err, "tool") {
		t.Fatalf("a missing spec file stays a tool-build failure, got %v", err)
	}
	if errs.ExitCode(err) != 9 {
		t.Fatalf("expected the tool exit code 9, got %d", errs.ExitCode(err))
	}
}

func TestBuildBingToolsProducesDocumentedWireFormats(t *testing.T) {
	tools, err := BuildTools([]map[string]interface{}{
		{
			"type": "bing_grounding",
			"search_configurations": []interface{}{map[string]interface{}{
				"project_connection_id": "bing-connection",
				"count":                 7,
				"market":                "en-US",
				"set_lang":              "en",
				"freshness":             "7d",
			}},
		},
		{
			"type": "bing_custom_search_preview",
			"search_configurations": []interface{}{map[string]interface{}{
				"project_connection_id": "bing-custom-connection",
				"instance_name":         "docs",
			}},
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, tools, []interface{}{
		map[string]interface{}{
			"type": "bing_grounding",
			"bing_grounding": map[string]interface{}{
				"search_configurations": []interface{}{map[string]interface{}{
					"project_connection_id": "bing-connection",
					"count":                 7,
					"market":                "en-US",
					"set_lang":              "en",
					"freshness":             "7d",
				}},
			},
		},
		map[string]interface{}{
			"type": "bing_custom_search_preview",
			"bing_custom_search_preview": map[string]interface{}{
				"search_configurations": []interface{}{map[string]interface{}{
					"project_connection_id": "bing-custom-connection",
					"instance_name":         "docs",
				}},
			},
		},
	})
}

func TestBuildToolsRejectsUnsupportedAndMalformedEntries(t *testing.T) {
	tests := map[string][]map[string]interface{}{
		"unknown type":            {{"type": "shell"}},
		"missing type":            {{"name": "orders"}},
		"file_search without ids": {{"type": "file_search"}},
		"mcp without label":       {{"type": "mcp", "server_url": "https://mcp.contoso.com"}},
		"openapi without name":    {{"type": "openapi", "spec": map[string]interface{}{}}},
		"openapi without spec":    {{"type": "openapi", "name": "orders"}},
		"azure_function bare":     {{"type": "azure_function"}},
		"bing grounding bare":     {{"type": "bing_grounding"}},
		"bing grounding no conn":  {{"type": "bing_grounding", "search_configurations": []interface{}{map[string]interface{}{}}}},
		"bing custom no instance": {{"type": "bing_custom_search_preview", "search_configurations": []interface{}{map[string]interface{}{"project_connection_id": "conn"}}}},
		"memory bare":             {{"type": "memory_search_preview"}},
		"openapi bad auth":        {{"type": "openapi", "name": "o", "spec": map[string]interface{}{}, "auth": map[string]interface{}{"type": "basic"}}},
		"openapi mi no audience":  {{"type": "openapi", "name": "o", "spec": map[string]interface{}{}, "auth": map[string]interface{}{"type": "managed_identity"}}},
		"openapi conn no id":      {{"type": "openapi", "name": "o", "spec": map[string]interface{}{}, "auth": map[string]interface{}{"type": "connection"}}},
	}
	for name, manifestTools := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := BuildTools(manifestTools, t.TempDir()); err == nil {
				t.Fatal("expected the malformed tool entry to be rejected")
			}
		})
	}
}

func TestManagedVectorStoreResolutionForAgentTools(t *testing.T) {
	built, err := BuildToolsForProject([]map[string]interface{}{{
		"type":         "file_search",
		"vector_store": "product-docs",
	}}, t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ManagedVectorStoreNames(built); !reflect.DeepEqual(got, []string{"product-docs"}) {
		t.Fatalf("unexpected logical names: %#v", got)
	}
	resolved, err := ResolveManagedVectorStores(
		built,
		map[string]string{"product-docs": "vs-123"},
	)
	if err != nil {
		t.Fatal(err)
	}
	payload := resolved[0].(map[string]interface{})
	if got := getStrSlice(payload, "vector_store_ids"); !reflect.DeepEqual(
		got,
		[]string{"vs-123"},
	) {
		t.Fatalf("logical vector store was not resolved: %#v", payload)
	}
	if _, exists := payload[managedVectorStoreKey]; exists {
		t.Fatalf("logical placeholder leaked into the wire payload: %#v", payload)
	}
	if _, err := ResolveManagedVectorStores(built, map[string]string{}); err == nil {
		t.Fatal("missing logical vector-store resolution must fail")
	}
}

func TestDescribeToolsMatchesTheSupportedCatalog(t *testing.T) {
	described, err := DescribeTools([]map[string]interface{}{
		{"type": "a2a", "a2a_version": "1.0", "project_connection_id": "stable-a2a-connection"},
		{"type": "a2a_preview", "project_connection_id": "a2a-connection"},
		{"type": "azure_ai_search", "indexes": []interface{}{map[string]interface{}{"index_name": "docs"}}},
		{"type": "bing_custom_search_preview", "search_configurations": []interface{}{map[string]interface{}{"project_connection_id": "bing-custom", "instance_name": "docs"}}},
		{"type": "bing_grounding", "search_configurations": []interface{}{map[string]interface{}{"project_connection_id": "bing"}}},
		{"type": "browser_automation_preview", "project_connection_id": "browser-connection"},
		{"type": "code_interpreter"},
		{"type": "computer_use_preview", "environment": "browser", "display_width": 1280, "display_height": 720},
		{"type": "custom_code_interpreter", "server_label": "interpreter", "server_url": "https://interpreter.example.test/mcp"},
		{"type": "fabric_iq_preview", "project_connection_id": "fabric-connection"},
		{"type": "file_search", "vector_store_ids": []interface{}{"vs-1", "vs-2"}},
		{"type": "function", "function": map[string]interface{}{"name": "lookup"}},
		{"type": "image_generation", "quality": "high", "size": "1024x1024"},
		{"type": "memory_search_preview", "memory_store_name": "assistant-memory", "scope": "user"},
		{"type": "openapi", "name": "orders", "auth": map[string]interface{}{"type": "managed_identity"}},
		{
			"type":         "mcp",
			"server_label": "docs",
			"require_approval": map[string]interface{}{
				"always": map[string]interface{}{"tool_names": []interface{}{"update"}},
				"never":  map[string]interface{}{"read_only": true},
			},
		},
		{"type": "sharepoint_grounding_preview", "project_connection_ids": []interface{}{"sharepoint-connection"}},
		{
			"type": "toolbox",
			"name": "operations",
			"require_approval": map[string]interface{}{
				"always": map[string]interface{}{"tool_names": []interface{}{"delete"}},
			},
		},
		{"type": "web_search"},
		{"type": "work_iq_preview", "project_connection_id": "work-connection"},
		{"type": "azure_function", "function": map[string]interface{}{"name": "processItem"}},
	})
	if err != nil {
		t.Fatalf("unexpected describe error: %v", err)
	}
	want := []string{
		`a2a(a2a_version="1.0", project_connection_id="stable-a2a-connection", agent_card_path="", send_credentials_for_agent_card=false)`,
		`a2a_preview(project_connection_id="a2a-connection", agent_card_path="", send_credentials_for_agent_card=false)`,
		"azure_ai_search(indexes=1)",
		"bing_custom_search_preview(search_configurations=1)",
		"bing_grounding(search_configurations=1)",
		`browser_automation_preview(project_connection_id="browser-connection")`,
		"code_interpreter",
		`computer_use_preview(environment="browser", display=1280x720)`,
		`custom_code_interpreter(server_label="interpreter", require_approval="always")`,
		`fabric_iq_preview(project_connection_id="fabric-connection")`,
		"file_search(vector_store_ids=[vs-1, vs-2])",
		`function(name="lookup", execution=caller)`,
		`image_generation(quality="high", size="1024x1024")`,
		`memory_search_preview(memory_store_name="assistant-memory", scope="user")`,
		`openapi(name="orders", auth=managed_identity)`,
		`mcp(server_label="docs", require_approval="always(tool_names=[update]); never(read_only=true)")`,
		"sharepoint_grounding_preview(project_connections=1)",
		`toolbox(name="operations", version="default", require_approval="always(tool_names=[delete])")`,
		"web_search",
		`work_iq_preview(project_connection_id="work-connection")`,
		`azure_function(function="processItem")`,
	}
	if len(described) != len(want) {
		t.Fatalf("unexpected description count: %#v", described)
	}
	for i := range want {
		if described[i] != want[i] {
			t.Fatalf("description %d: got %q want %q", i, described[i], want[i])
		}
	}
	if _, err := DescribeTools([]map[string]interface{}{{"type": "shell"}}); err == nil {
		t.Fatal("an unsupported tool type must not be describable")
	}
	for _, supported := range SupportedToolTypes {
		if !strings.Contains(strings.Join(want, " "), supported) {
			t.Fatalf("supported tool type %q is not covered by this test", supported)
		}
	}
}

func TestBuildExpandedDirectToolsAndPreviewCatalog(t *testing.T) {
	projectEndpoint := "https://account.services.ai.azure.com/api/projects/project"
	manifestTools := []map[string]interface{}{
		{"type": "toolbox", "name": "operations", "project_connection_id": "toolbox-connection", "version": "2"},
		{"type": "web_search"},
		{"type": "azure_ai_search", "indexes": []interface{}{map[string]interface{}{
			"project_connection_id": "search-connection",
			"index_name":            "docs",
		}}},
		{"type": "a2a", "a2a_version": "1.0", "project_connection_id": "stable-a2a-connection"},
		{"type": "a2a_preview", "project_connection_id": "a2a-connection"},
		{"type": "bing_custom_search_preview", "search_configurations": []interface{}{map[string]interface{}{
			"project_connection_id": "bing-connection",
			"instance_name":         "docs",
		}}},
		{"type": "browser_automation_preview", "project_connection_id": "browser-connection"},
		{"type": "fabric_iq_preview", "project_connection_id": "fabric-connection", "server_url": "https://fabric.example.test/mcp"},
		{"type": "work_iq_preview", "project_connection_id": "work-connection"},
		{"type": "sharepoint_grounding_preview", "project_connection_ids": []interface{}{"sharepoint-connection"}},
		{"type": "computer_use_preview", "display_width": 1280, "display_height": 720, "environment": "browser"},
		{"type": "image_generation", "model": "image-model", "quality": "high", "size": "1024x1024"},
		{"type": "function", "function": map[string]interface{}{
			"name":        "lookup",
			"description": "Look up a record.",
			"parameters": map[string]interface{}{
				"type": "object",
			},
		}},
	}
	built, err := BuildToolsForProject(manifestTools, t.TempDir(), projectEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	if len(built) != len(manifestTools) {
		t.Fatalf("built %d tools, want %d", len(built), len(manifestTools))
	}
	toolbox := built[0].(map[string]interface{})
	if toolbox["type"] != "mcp" ||
		toolbox["server_url"] != projectEndpoint+"/toolboxes/operations/versions/2/mcp?api-version=v1" ||
		toolbox["project_connection_id"] != "toolbox-connection" {
		t.Fatalf("unexpected Toolbox attachment: %#v", toolbox)
	}
	function := built[len(built)-1].(map[string]interface{})
	if function["type"] != "function" ||
		function["name"] != "lookup" ||
		function["description"] != "Look up a record." {
		t.Fatalf("unexpected function payload: %#v", function)
	}
	wantPreview := []string{
		"a2a_preview",
		"bing_custom_search_preview",
		"browser_automation_preview",
		"computer_use_preview",
		"fabric_iq_preview",
		"image_generation",
		"sharepoint_grounding_preview",
		"work_iq_preview",
	}
	if got := PreviewToolTypes(manifestTools); !reflect.DeepEqual(got, wantPreview) {
		t.Fatalf("preview catalog mismatch: got %v want %v", got, wantPreview)
	}
}

func TestBuildA2ASupportsStableAndPreviewAuthenticatedAgentCards(t *testing.T) {
	built, err := BuildTools([]map[string]interface{}{
		{
			"type":                            "a2a",
			"a2a_version":                     "1.0",
			"project_connection_id":           "a2a-connection",
			"base_url":                        "https://a2a.example.test",
			"agent_card_path":                 "/private/agent-card.json",
			"send_credentials_for_agent_card": true,
		},
		{
			"type":                            "a2a_preview",
			"project_connection_id":           "public-a2a-connection",
			"agent_card_path":                 "https://cards.example.test/.well-known/agent-card.json",
			"send_credentials_for_agent_card": false,
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, built, []interface{}{
		map[string]interface{}{
			"type":                            "a2a",
			"a2a_version":                     "1.0",
			"project_connection_id":           "a2a-connection",
			"base_url":                        "https://a2a.example.test",
			"agent_card_path":                 "/private/agent-card.json",
			"send_credentials_for_agent_card": true,
		},
		map[string]interface{}{
			"type":                            "a2a_preview",
			"project_connection_id":           "public-a2a-connection",
			"agent_card_path":                 "https://cards.example.test/.well-known/agent-card.json",
			"send_credentials_for_agent_card": false,
		},
	})
}

func TestBuildStableA2ARequiresVersionOne(t *testing.T) {
	for _, tool := range []map[string]interface{}{
		{
			"type":                  "a2a",
			"project_connection_id": "a2a-connection",
		},
		{
			"type":                  "a2a",
			"a2a_version":           "0.3",
			"project_connection_id": "a2a-connection",
		},
	} {
		if _, err := BuildTools([]map[string]interface{}{tool}, t.TempDir()); err == nil ||
			!strings.Contains(err.Error(), "a2a_version") {
			t.Fatalf("invalid stable A2A version was accepted: tool=%#v err=%v", tool, err)
		}
	}
}

func TestBuildA2ARejectsInvalidAgentCardConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		field string
		value interface{}
	}{
		{name: "path type", field: "agent_card_path", value: true},
		{name: "credential type", field: "send_credentials_for_agent_card", value: "true"},
		{name: "empty path", field: "agent_card_path", value: "  "},
		{name: "http URL", field: "agent_card_path", value: "http://cards.example.test/agent-card.json"},
		{name: "scheme-relative URL", field: "agent_card_path", value: "//cards.example.test/agent-card.json"},
		{name: "embedded credentials", field: "agent_card_path", value: "https://user:password@cards.example.test/agent-card.json"},
		{name: "backslash path", field: "agent_card_path", value: `\private\agent-card.json`},
		{name: "fragment", field: "agent_card_path", value: "/agent-card.json#current"},
		{name: "absolute fragment", field: "agent_card_path", value: "https://cards.example.test/agent-card.json#current"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tool := map[string]interface{}{
				"type":                  "a2a_preview",
				"project_connection_id": "a2a-connection",
				test.field:              test.value,
			}
			if _, err := BuildTools([]map[string]interface{}{tool}, t.TempDir()); err == nil {
				t.Fatalf("expected %s to be rejected", test.field)
			}
		})
	}
}

func TestToolboxEndpointClassificationIsNarrow(t *testing.T) {
	project := "https://account.services.ai.azure.com/api/projects/project"
	valid := []string{
		project + "/toolboxes/operations/mcp?api-version=v1",
		project + "/toolboxes/operations/versions/2/mcp?api-version=v1",
	}
	for _, endpoint := range valid {
		if !IsProjectToolboxEndpoint(endpoint, project) {
			t.Fatalf("valid same-project Toolbox endpoint rejected: %s", endpoint)
		}
	}
	invalid := []string{
		"https://attacker.example/toolboxes/operations/mcp?api-version=v1",
		project + "/toolboxes/operations/mcp?api-version=v2",
		project + "/toolboxes/operations/versions/2/mcp?api-version=v1#fragment",
		project + "/agents/operations/mcp?api-version=v1",
	}
	for _, endpoint := range invalid {
		if IsProjectToolboxEndpoint(endpoint, project) {
			t.Fatalf("unsafe Toolbox endpoint accepted: %s", endpoint)
		}
	}
}
