package tools

import (
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func buildForTest(t *testing.T, tool map[string]interface{}) []interface{} {
	t.Helper()
	built, err := BuildTools([]map[string]interface{}{tool}, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	return built
}

func TestDestinationsCollectsRootPathAndOperationServers(t *testing.T) {
	built := buildForTest(t, map[string]interface{}{
		"type": "openapi",
		"name": "orders",
		"spec": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{"url": "https://root.contoso.com"}},
			"paths": map[string]interface{}{
				"/items": map[string]interface{}{
					"servers": []interface{}{map[string]interface{}{"url": "https://path.contoso.com"}},
					"get": map[string]interface{}{
						"servers": []interface{}{map[string]interface{}{"url": "https://operation.contoso.com"}},
					},
				},
			},
			"webhooks": map[string]interface{}{
				"onEvent": map[string]interface{}{
					"servers": []interface{}{map[string]interface{}{"url": "https://webhook.contoso.com"}},
				},
			},
			"components": map[string]interface{}{
				"pathItems": map[string]interface{}{
					"shared": map[string]interface{}{
						"servers": []interface{}{map[string]interface{}{"url": "https://component.contoso.com"}},
					},
				},
			},
		},
		"auth": map[string]interface{}{"type": "managed_identity", "audience": "api://orders"},
	})
	destinations, err := Destinations(built)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	found := map[string]bool{}
	for _, destination := range destinations {
		found[destination.URL] = true
		if destination.Audience != "api://orders" || destination.AuthType != "managed_identity" {
			t.Fatalf("destination lost its auth context: %#v", destination)
		}
	}
	for _, want := range []string{
		"https://root.contoso.com",
		"https://path.contoso.com",
		"https://operation.contoso.com",
		"https://webhook.contoso.com",
		"https://component.contoso.com",
	} {
		if !found[want] {
			t.Fatalf("destination %q was not inspected: %#v", want, destinations)
		}
	}
}

func TestDestinationsFailClosedOnAmbiguousServers(t *testing.T) {
	tests := map[string]interface{}{
		"missing servers": map[string]interface{}{"openapi": "3.0.0", "paths": map[string]interface{}{}},
		"templated url": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{"url": "https://{tenant}.contoso.com"}},
		},
		"server variables": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{
				"url":       "https://api.contoso.com",
				"variables": map[string]interface{}{"tenant": map[string]interface{}{"default": "a"}},
			}},
		},
		"servers not an array": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": "https://api.contoso.com",
		},
		"server without url": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{"description": "no url"}},
		},
	}
	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			built := buildForTest(t, map[string]interface{}{
				"type": "openapi",
				"name": "orders",
				"spec": spec,
			})
			if _, err := Destinations(built); err == nil || !errs.IsKind(err, "security") {
				t.Fatalf("expected a security error, got %v", err)
			}
		})
	}
}

func TestDestinationsIncludesMCPServer(t *testing.T) {
	built := buildForTest(t, map[string]interface{}{
		"type":         "mcp",
		"server_label": "docs",
		"server_url":   "https://mcp.contoso.com/sse",
	})
	destinations, err := Destinations(built)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(destinations) != 1 || destinations[0].URL != "https://mcp.contoso.com/sse" {
		t.Fatalf("unexpected MCP destinations: %#v", destinations)
	}
}

func TestDestinationsIncludesConnectionToolBaseURLs(t *testing.T) {
	built, err := BuildTools([]map[string]interface{}{
		{
			"type":                            "a2a",
			"a2a_version":                     "1.0",
			"project_connection_id":           "a2a-connection",
			"base_url":                        "https://a2a.contoso.com",
			"agent_card_path":                 "https://cards.contoso.com/.well-known/agent-card.json",
			"send_credentials_for_agent_card": true,
		},
		{
			"type":                  "work_iq_preview",
			"project_connection_id": "work-connection",
			"base_url":              "https://work.contoso.com",
		},
		{
			"type":                  "fabric_iq_preview",
			"project_connection_id": "fabric-connection",
			"base_url":              "https://fabric-base.contoso.com",
			"server_url":            "https://fabric-mcp.contoso.com/sse",
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destinations, err := Destinations(built)
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 5 {
		t.Fatalf("unexpected connection destinations: %#v", destinations)
	}
	for index, want := range []string{
		"https://a2a.contoso.com",
		"https://cards.contoso.com/.well-known/agent-card.json",
		"https://work.contoso.com",
		"https://fabric-base.contoso.com",
		"https://fabric-mcp.contoso.com/sse",
	} {
		if destinations[index].URL != want {
			t.Fatalf("destination[%d]=%q, want %q", index, destinations[index].URL, want)
		}
	}
	if destinations[1].AuthType != "anonymous" {
		t.Fatalf("cross-host agent-card fetch must be anonymous: %#v", destinations[1])
	}
	if destinations[0].Type != "a2a" || destinations[1].Type != "a2a" {
		t.Fatalf("stable A2A destination type was not preserved: %#v", destinations[:2])
	}
}

func TestA2AAgentCardDestinationUsesCredentialsOnlyForSameHost(t *testing.T) {
	built, err := BuildTools([]map[string]interface{}{
		{
			"type":                            "a2a",
			"a2a_version":                     "1.0",
			"project_connection_id":           "a2a-connection",
			"base_url":                        "https://a2a.contoso.com",
			"agent_card_path":                 "https://a2a.contoso.com/private/agent-card.json",
			"send_credentials_for_agent_card": true,
		},
		{
			"type":                            "a2a_preview",
			"project_connection_id":           "public-connection",
			"base_url":                        "https://public.contoso.com",
			"agent_card_path":                 "/.well-known/agent-card.json",
			"send_credentials_for_agent_card": false,
		},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destinations, err := Destinations(built)
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 3 {
		t.Fatalf("relative agent-card paths must not add a host: %#v", destinations)
	}
	if destinations[1].AuthType != "project_connection_same_host_only" {
		t.Fatalf("same-host authenticated card fetch not identified: %#v", destinations[1])
	}
}

func TestConnectionToolBaseURLMustBeAbsoluteHTTPSWithoutCredentials(t *testing.T) {
	for _, baseURL := range []string{
		"http://a2a.contoso.com",
		"/a2a",
		"******a2a.contoso.com",
	} {
		_, err := BuildTools([]map[string]interface{}{{
			"type":                  "a2a_preview",
			"project_connection_id": "a2a-connection",
			"base_url":              baseURL,
		}}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Fatalf("expected base_url %q to be rejected, got %v", baseURL, err)
		}
	}
}

func TestDestinationsIgnoresLocalTools(t *testing.T) {
	built, err := BuildTools([]map[string]interface{}{
		{"type": "code_interpreter"},
		{"type": "file_search", "vector_store_ids": []interface{}{"vs-1"}},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destinations, err := Destinations(built)
	if err != nil {
		t.Fatal(err)
	}
	if len(destinations) != 0 {
		t.Fatalf("local tools have no external destination: %#v", destinations)
	}
}

func TestMCPRequireApprovalIsFailClosed(t *testing.T) {
	base := map[string]interface{}{
		"type":         "mcp",
		"server_label": "docs",
		"server_url":   "https://mcp.contoso.com/sse",
	}
	for _, mode := range SupportedMCPApprovalModes {
		tool := map[string]interface{}{}
		for key, value := range base {
			tool[key] = value
		}
		tool["require_approval"] = mode
		built := buildForTest(t, tool)
		if got := built[0].(map[string]interface{})["require_approval"]; got != mode {
			t.Fatalf("unexpected approval mode: %#v", got)
		}
	}
	for _, mode := range []interface{}{
		"sometimes",
		"Never",
		1,
		map[string]interface{}{"sometimes": []string{"a"}},
		map[string]interface{}{"always": []string{"a"}, "never": []string{"a"}},
	} {
		tool := map[string]interface{}{}
		for key, value := range base {
			tool[key] = value
		}
		tool["require_approval"] = mode
		if _, err := BuildTools([]map[string]interface{}{tool}, t.TempDir()); err == nil {
			t.Fatalf("expected require_approval %v to be rejected", mode)
		}
	}
	built := buildForTest(t, base)
	if got := built[0].(map[string]interface{})["require_approval"]; got != DefaultMCPApprovalMode {
		t.Fatalf("expected the fail-closed default, got %#v", got)
	}
}

func TestMCPServerURLMustBeAbsoluteHTTPSWithoutCredentials(t *testing.T) {
	for _, serverURL := range []string{
		"http://mcp.contoso.com/sse",
		"/sse",
		"https://user:pass@mcp.contoso.com/sse",
		"",
	} {
		_, err := BuildTools([]map[string]interface{}{{
			"type":         "mcp",
			"server_label": "docs",
			"server_url":   serverURL,
		}}, t.TempDir())
		if err == nil {
			t.Fatalf("expected server_url %q to be rejected", serverURL)
		}
		if !strings.Contains(err.Error(), "server_url") {
			t.Fatalf("unexpected error for %q: %v", serverURL, err)
		}
	}
}

func TestDestinationsRejectRemoteReferences(t *testing.T) {
	built := buildForTest(t, map[string]interface{}{
		"type": "openapi",
		"name": "orders",
		"spec": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{"url": "https://api.contoso.com"}},
			"paths": map[string]interface{}{
				"/items": map[string]interface{}{
					"$ref": "https://attacker.example/paths.json#/items",
				},
			},
		},
	})
	if _, err := Destinations(built); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security error for a remote $ref, got %v", err)
	}
}

func TestDestinationsAllowLocalReferences(t *testing.T) {
	built := buildForTest(t, map[string]interface{}{
		"type": "openapi",
		"name": "orders",
		"spec": map[string]interface{}{
			"openapi": "3.0.0",
			"servers": []interface{}{map[string]interface{}{"url": "https://api.contoso.com"}},
			"paths": map[string]interface{}{
				"/items": map[string]interface{}{
					"get": map[string]interface{}{
						"responses": map[string]interface{}{
							"200": map[string]interface{}{"$ref": "#/components/responses/Ok"},
						},
					},
				},
			},
		},
	})
	if _, err := Destinations(built); err != nil {
		t.Fatalf("local references must be allowed: %v", err)
	}
}
