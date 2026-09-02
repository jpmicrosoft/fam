package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

const validEndpoint = "https://acct.services.ai.azure.com"
const validResourceID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/projects/proj"

var raiID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/raiPolicies/Microsoft.DefaultV2"

func validDoc() map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":         "demo-agent",
			"instructions": "be clear",
			"model":        "chat-001",
		},
		"project": map[string]interface{}{
			"resource_id": validResourceID,
		},
		"tools": []interface{}{
			map[string]interface{}{"type": "code_interpreter"},
		},
	}
}

func TestEndpointDerivedFromResourceID(t *testing.T) {
	cfg, err := ResolveConfig(validDoc())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := validEndpoint + "/api/projects/proj"
	if cfg.Project.Endpoint != expected {
		t.Errorf("unexpected endpoint: %s (want %s)", cfg.Project.Endpoint, expected)
	}
}

func TestAgentMetadataPassthrough(t *testing.T) {
	doc := validDoc()
	doc["agent"].(map[string]interface{})["metadata"] = map[string]interface{}{
		"owner":       "platform",
		"environment": "production",
	}
	if err := ValidateManifest(doc); err != nil {
		t.Fatal(err)
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Metadata["owner"] != "platform" ||
		cfg.Agent.Metadata["environment"] != "production" ||
		!cfg.Agent.MetadataConfigured {
		t.Fatalf("unexpected metadata: %#v", cfg.Agent.Metadata)
	}
}

func TestAgentMetadataRejectsNonStringValue(t *testing.T) {
	doc := validDoc()
	doc["agent"].(map[string]interface{})["metadata"] = map[string]interface{}{
		"costCenter": 42,
	}
	if err := ValidateManifest(doc); err == nil {
		t.Fatal("expected non-string agent metadata to fail schema validation")
	}
}

func TestModelDeploymentConfigurationDefaultsToAgentModel(t *testing.T) {
	doc := validDoc()
	doc["model_deployment"] = map[string]interface{}{
		"model_name":    "gpt-5-mini",
		"model_version": "2025-08-07",
		"model_format":  "OpenAI",
		"sku_name":      "GlobalStandard",
		"capacity":      10,
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ModelDeployment.Configured ||
		cfg.ModelDeployment.DeploymentName != "chat-001" ||
		cfg.ModelDeployment.ModelName != "gpt-5-mini" ||
		cfg.ModelDeployment.Capacity != 10 {
		t.Fatalf("unexpected model deployment configuration: %#v", cfg.ModelDeployment)
	}
}

func TestModelDeploymentConfigurationRequiresCompleteDesiredState(t *testing.T) {
	doc := validDoc()
	doc["model_deployment"] = map[string]interface{}{
		"model_name": "gpt-5-mini",
		"capacity":   10,
	}
	_, err := ResolveConfig(doc)
	if err == nil || !errs.IsKind(err, "config") ||
		!strings.Contains(err.Error(), "model_deployment.model_version") {
		t.Fatalf("expected incomplete model deployment configuration to fail, got %v", err)
	}
}

func TestResourceIDDerivesFully(t *testing.T) {
	cfg, err := ResolveConfig(validDoc())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Project.SubscriptionID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("subscription = %q", cfg.Project.SubscriptionID)
	}
	if cfg.Project.ResourceGroup != "rg" {
		t.Errorf("rg = %q", cfg.Project.ResourceGroup)
	}
	if cfg.Project.AccountName != "acct" {
		t.Errorf("account = %q", cfg.Project.AccountName)
	}
	if cfg.Project.Name != "proj" {
		t.Errorf("project = %q", cfg.Project.Name)
	}
	if cfg.Project.AccountEndpoint != validEndpoint {
		t.Errorf("account endpoint = %q", cfg.Project.AccountEndpoint)
	}
}

func TestInvalidResourceIDRejected(t *testing.T) {
	doc := validDoc()
	doc["project"].(map[string]interface{})["resource_id"] = "/subscriptions/bad/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/p"
	_, err := ResolveConfig(doc)
	if err == nil || !strings.Contains(err.Error(), "not a valid UUID") {
		t.Fatalf("expected UUID error, got %v", err)
	}
}

func TestEndpointConfigurationPreservesSafeProtocolsAndAuthorization(t *testing.T) {
	doc := validDoc()
	doc["endpoint"] = map[string]interface{}{
		"protocols": []interface{}{"activity", "invocations"},
		"authorization_schemes": []interface{}{
			map[string]interface{}{"type": "BotServiceRbac"},
		},
		"agent_card": map[string]interface{}{
			"version":     "1.0.0",
			"description": "Support agent",
			"skills": []interface{}{
				map[string]interface{}{
					"id":          "support",
					"name":        "Support",
					"description": "Handles support requests",
					"tags":        []interface{}{"support"},
					"examples":    []interface{}{"Help with an incident"},
				},
			},
		},
	}

	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Endpoint.Configured {
		t.Fatal("expected endpoint configuration to be enabled")
	}
	if got := strings.Join(cfg.Endpoint.Protocols, ","); got != "responses,activity,invocations" {
		t.Fatalf("safe responses protocol was not preserved: %s", got)
	}
	if got := strings.Join(cfg.Endpoint.AuthorizationSchemes, ","); got != "Entra,BotServiceRbac" {
		t.Fatalf("safe Entra authorization was not preserved: %s", got)
	}
	if len(cfg.Endpoint.AgentCard.Skills) != 1 ||
		cfg.Endpoint.AgentCard.Skills[0].ID != "support" {
		t.Fatalf("unexpected agent card: %#v", cfg.Endpoint.AgentCard)
	}
}

func TestMissingEndpointLazyRaisesOnlyWhenRequired(t *testing.T) {
	doc := validDoc()
	doc["project"] = map[string]interface{}{"name": "proj"}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Project.Endpoint != "" {
		t.Errorf("expected empty endpoint, got %s", cfg.Project.Endpoint)
	}
	_, err = cfg.RequireProjectEndpoint()
	if err == nil || !errs.IsKind(err, "config") {
		t.Error("expected config error when requiring endpoint")
	}
}

func TestRAIPolicyIDPassthrough(t *testing.T) {
	doc := validDoc()
	doc["agent"].(map[string]interface{})["rai_policy_id"] = raiID
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.RAIPolicyID != raiID {
		t.Errorf("unexpected rai_policy_id: %s", cfg.Agent.RAIPolicyID)
	}
}

func TestAbsentRAIPolicyDefaultsToNone(t *testing.T) {
	cfg, err := ResolveConfig(validDoc())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.RAIPolicyID != "" {
		t.Errorf("expected empty rai_policy_id, got %s", cfg.Agent.RAIPolicyID)
	}
}

func TestApimBlockEnablesAndPinsTarget(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"target":   "https://gw.azure-api.net",
		"api_path": "/foundry",
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Apim.Enabled {
		t.Error("expected apim enabled")
	}
	if cfg.Apim.Target != "https://gw.azure-api.net" {
		t.Errorf("unexpected target: %s", cfg.Apim.Target)
	}
}

func TestApimCoordinatesFromManifest(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"gateway_url": "https://gw.azure-api.net",
		"api_path":    "agents/chat",
		"api_name":    "agents",
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Apim.ResolvedTarget() != "https://gw.azure-api.net/agents/chat" {
		t.Errorf("unexpected resolved target: %s", cfg.Apim.ResolvedTarget())
	}
	if cfg.Apim.APIName != "agents" {
		t.Errorf("unexpected api_name: %s", cfg.Apim.APIName)
	}
}

func TestNoApimBlockStaysDisabled(t *testing.T) {
	cfg, err := ResolveConfig(validDoc())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Apim.Enabled {
		t.Error("expected apim disabled")
	}
}

func TestResourceIDWrongProviderRejected(t *testing.T) {
	doc := validDoc()
	doc["project"].(map[string]interface{})["resource_id"] = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.Storage/accounts/acct/projects/p"
	_, err := ResolveConfig(doc)
	if err == nil || !strings.Contains(err.Error(), "Microsoft.CognitiveServices") {
		t.Errorf("expected provider error, got %v", err)
	}
}

func TestSSRFApimTargetRejected(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"target": "https://evil.example.com",
	}
	_, err := ResolveConfig(doc)
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for SSRF APIM target")
	}
}

func TestManagedIdentityAudienceRejectsOAuthScope(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"target":   "https://gateway.azure-api.net",
		"auth":     "managed_identity",
		"audience": "https://cognitiveservices.azure.com/.default",
	}
	_, err := ResolveConfig(doc)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected audience scope config error, got %v", err)
	}
}

func TestInlineOpenAPIYAMLNumericKeysAreJSONCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	manifest := `apiVersion: foundry-agent-manager/v1
agent:
  name: demo-agent
  model: chat
  instructions: help
tools:
  - type: openapi
    name: orders
    spec:
      openapi: 3.0.0
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
	if err := os.WriteFile(path, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := ManifestToJSON(doc)
	if err != nil {
		t.Fatalf("inline OpenAPI spec is not JSON-compatible: %v", err)
	}
	if !strings.Contains(string(data), `"200"`) {
		t.Fatalf("numeric response key was not normalized: %s", data)
	}
}

// Schema validation tests

func TestValidMinimalDocPasses(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":         "demo-agent",
			"model":        "chat-001",
			"instructions": "be nice",
		},
	}
	if err := ValidateManifest(doc); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestMissingAgentRejected(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
	}
	err := ValidateManifest(doc)
	if err == nil {
		t.Error("expected error for missing agent")
	}
}

func TestMissingInstructionsRejected(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":  "demo-agent",
			"model": "chat-001",
		},
	}
	err := ValidateManifest(doc)
	if err == nil {
		t.Error("expected error for missing instructions")
	}
}

func TestBadRAIPolicyIDRejected(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":          "demo-agent",
			"model":         "chat-001",
			"instructions":  "be nice",
			"rai_policy_id": "Microsoft.DefaultV2", // bare name, not ARM id
		},
	}
	err := ValidateManifest(doc)
	if err == nil {
		t.Error("expected error for bad rai_policy_id")
	}
}

func TestGoodRAIPolicyIDAccepted(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":          "demo-agent",
			"model":         "chat-001",
			"instructions":  "be nice",
			"rai_policy_id": raiID,
		},
	}
	if err := ValidateManifest(doc); err != nil {
		t.Errorf("expected valid, got: %v", err)
	}
}

func TestRAIPolicyIDMustMatchConfiguredProjectAccount(t *testing.T) {
	doc := validDoc()
	doc["agent"].(map[string]interface{})["rai_policy_id"] =
		"/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/other/raiPolicies/custom"
	_, err := ResolveConfig(doc)
	if err == nil || !strings.Contains(err.Error(), "same Foundry account") {
		t.Fatalf("cross-account RAI policy was not rejected: %v", err)
	}
}

func TestUnknownTopLevelKeyRejected(t *testing.T) {
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":         "demo-agent",
			"model":        "chat-001",
			"instructions": "be nice",
		},
		"bogus": true,
	}
	err := ValidateManifest(doc)
	if err == nil {
		t.Error("expected error for unknown top-level key")
	}
}

func TestLoadManifestRejectsNullTopLevel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "null.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadManifest(path)
	if err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected manifest error, got %v", err)
	}
}

func TestUnsafeSpecFileRejectedDuringValidation(t *testing.T) {
	paths := []string{
		"../outside.json",
		"specs/foo..bar.json",
		"/tmp/spec.json",
		`\server\share\spec.json`,
		`C:\spec.json`,
	}
	for _, specFile := range paths {
		t.Run(specFile, func(t *testing.T) {
			doc := validDoc()
			doc["tools"] = []interface{}{
				map[string]interface{}{
					"type":      "openapi",
					"name":      "unsafe",
					"spec_file": specFile,
				},
			}
			err := ValidateManifest(doc)
			if err == nil || !errs.IsKind(err, "manifest") {
				t.Fatalf("expected manifest error for %q, got %v", specFile, err)
			}
		})
	}
}

func TestRelativeSpecFileAcceptedDuringValidation(t *testing.T) {
	doc := validDoc()
	doc["tools"] = []interface{}{
		map[string]interface{}{
			"type":      "openapi",
			"name":      "safe",
			"spec_file": "specs/sample-openapi.json",
		},
	}
	if err := ValidateManifest(doc); err != nil {
		t.Fatalf("expected relative spec_file to validate, got %v", err)
	}
}

func TestAllShippedExamplesValidate(t *testing.T) {
	// Find examples relative to the repo root
	// The test binary runs from within internal/config, but we need the repo root
	root := filepath.Join("..", "..", "examples")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("cannot read examples directory: %v", err)
	}
	found := false
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".yaml" && filepath.Ext(name) != ".yml" {
			continue
		}
		// Only agent-manifest examples use the foundry-agent-manager/v1 schema this
		// package validates. examples/publication.example.yaml uses the
		// separate foundry-agent-manager/publication/v1 schema (internal/publication)
		// and is intentionally excluded here.
		if !strings.HasPrefix(name, "agent") {
			continue
		}
		found = true
		t.Run(name, func(t *testing.T) {
			doc, err := LoadManifest(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("failed to load: %v", err)
			}
			if err := ValidateManifest(doc); err != nil {
				t.Errorf("validation failed: %v", err)
			}
		})
	}
	if !found {
		t.Error("no example manifests found to validate")
	}
}

func TestSchemaStructure(t *testing.T) {
	schema, err := LoadSchema()
	if err != nil {
		t.Fatalf("failed to load schema: %v", err)
	}

	// Check required fields
	required, ok := schema["required"].([]interface{})
	if !ok {
		t.Fatal("schema missing required array")
	}
	hasAPIVersion := false
	hasAgent := false
	for _, r := range required {
		if r == "apiVersion" {
			hasAPIVersion = true
		}
		if r == "agent" {
			hasAgent = true
		}
	}
	if !hasAPIVersion {
		t.Error("apiVersion not in required")
	}
	if !hasAgent {
		t.Error("agent not in required")
	}

	// Check additionalProperties is false
	if schema["additionalProperties"] != false {
		t.Error("additionalProperties should be false")
	}
}

func TestAzureGovernmentCloudIsRejected(t *testing.T) {
	doc := validDoc()
	doc["cloud"] = "AzureUSGovernment"
	_, err := ResolveConfig(doc)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected Azure Government to be rejected, got %v", err)
	}
	if !strings.Contains(err.Error(), "dedicated Azure Government subscription") {
		t.Fatalf("unexpected Azure Government rejection: %v", err)
	}
}

func TestOpenAPISpecAndSpecFileAreMutuallyExclusive(t *testing.T) {
	doc := validDoc()
	doc["tools"] = []interface{}{
		map[string]interface{}{
			"type":      "openapi",
			"name":      "orders",
			"spec":      map[string]interface{}{"openapi": "3.0.0"},
			"spec_file": "orders.json",
		},
	}
	if err := ValidateManifest(doc); err == nil {
		t.Fatal("expected spec and spec_file together to fail")
	}
}

func TestAPIMTargetFormsAreMutuallyExclusive(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"target":      "https://gateway.azure-api.net/agents",
		"gateway_url": "https://gateway.azure-api.net",
		"api_path":    "agents",
	}
	if err := ValidateManifest(doc); err == nil {
		t.Fatal("expected APIM target forms together to fail")
	}
}

func TestUnknownNestedToolFieldRejected(t *testing.T) {
	doc := validDoc()
	doc["tools"] = []interface{}{
		map[string]interface{}{
			"type":       "code_interpreter",
			"misspelled": true,
		},
	}
	if err := ValidateManifest(doc); err == nil {
		t.Fatal("expected unknown tool field to fail")
	}
}

func TestA2AAgentCardFieldsAreScopedToA2ATools(t *testing.T) {
	validDirect := validDoc()
	validDirect["tools"] = []interface{}{map[string]interface{}{
		"type":                            "a2a",
		"a2a_version":                     "1.0",
		"project_connection_id":           "a2a-connection",
		"agent_card_path":                 "/private/agent-card.json",
		"send_credentials_for_agent_card": true,
	}}
	if err := ValidateManifest(validDirect); err != nil {
		t.Fatalf("valid direct A2A agent-card fields rejected: %v", err)
	}

	validToolbox := validDoc()
	validToolbox["toolboxes"] = []interface{}{map[string]interface{}{
		"name":        "delegation",
		"description": "Delegate to another agent.",
		"tools": []interface{}{map[string]interface{}{
			"type":                            "a2a",
			"a2a_version":                     "1.0",
			"project_connection_id":           "a2a-connection",
			"agent_card_path":                 "https://cards.example.test/agent-card.json",
			"send_credentials_for_agent_card": false,
		}},
	}}
	if err := ValidateManifest(validToolbox); err != nil {
		t.Fatalf("valid Toolbox A2A agent-card fields rejected: %v", err)
	}

	for _, target := range []string{"direct", "toolbox"} {
		t.Run("stable version required "+target, func(t *testing.T) {
			doc := validDoc()
			tool := map[string]interface{}{
				"type":                  "a2a",
				"project_connection_id": "a2a-connection",
			}
			if target == "direct" {
				doc["tools"] = []interface{}{tool}
			} else {
				doc["toolboxes"] = []interface{}{map[string]interface{}{
					"name":  "delegation",
					"tools": []interface{}{tool},
				}}
			}
			if err := ValidateManifest(doc); err == nil {
				t.Fatal("stable A2A without a2a_version must fail")
			}
		})
	}

	previewCompatibility := validDoc()
	previewCompatibility["tools"] = []interface{}{map[string]interface{}{
		"type":                  "a2a_preview",
		"project_connection_id": "a2a-connection",
	}}
	if err := ValidateManifest(previewCompatibility); err != nil {
		t.Fatalf("legacy preview A2A compatibility was lost: %v", err)
	}

	for _, test := range []struct {
		name string
		tool map[string]interface{}
	}{
		{
			name: "wrong path type",
			tool: map[string]interface{}{
				"type":                  "a2a_preview",
				"project_connection_id": "a2a-connection",
				"agent_card_path":       true,
			},
		},
		{
			name: "wrong credential type",
			tool: map[string]interface{}{
				"type":                            "a2a_preview",
				"project_connection_id":           "a2a-connection",
				"send_credentials_for_agent_card": "true",
			},
		},
		{
			name: "A2A path on Work IQ",
			tool: map[string]interface{}{
				"type":                  "work_iq_preview",
				"project_connection_id": "work-connection",
				"agent_card_path":       "/agent-card.json",
			},
		},
		{
			name: "A2A credentials on Work IQ",
			tool: map[string]interface{}{
				"type":                            "work_iq_preview",
				"project_connection_id":           "work-connection",
				"send_credentials_for_agent_card": true,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			doc := validDoc()
			doc["tools"] = []interface{}{test.tool}
			if err := ValidateManifest(doc); err == nil {
				t.Fatal("expected invalid A2A field placement or type to fail")
			}
		})
	}

	invalidToolbox := validDoc()
	invalidToolbox["toolboxes"] = []interface{}{map[string]interface{}{
		"name":        "invalid-delegation",
		"description": "A2A-only fields must not leak to other Toolbox tools.",
		"tools": []interface{}{map[string]interface{}{
			"type":                            "work_iq_preview",
			"project_connection_id":           "work-connection",
			"send_credentials_for_agent_card": true,
		}},
	}}
	if err := ValidateManifest(invalidToolbox); err == nil {
		t.Fatal("expected A2A-only Toolbox fields on Work IQ to fail")
	}
}

func TestValidateResolvedTargetRejectsUnresolvedAndForeignTargets(t *testing.T) {
	tests := []struct {
		name string
		apim ApimSpec
		kind string
	}{
		{
			name: "gateway without api_path",
			apim: ApimSpec{Enabled: true, GatewayURL: "https://gw.azure-api.net", AllowedSuffixes: []string{"azure-api.net"}},
			kind: "config",
		},
		{
			name: "foreign combined target",
			apim: ApimSpec{Enabled: true, GatewayURL: "https://attacker.example", APIPath: "agents/chat", AllowedSuffixes: []string{"azure-api.net"}},
			kind: "security",
		},
		{
			name: "cross-cloud target",
			apim: ApimSpec{Enabled: true, Target: "https://gw.azure-api.us/agents", AllowedSuffixes: []string{"azure-api.net"}},
			kind: "security",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.apim.ValidateResolvedTarget(); err == nil || !errs.IsKind(err, tt.kind) {
				t.Fatalf("expected a %s error, got %v", tt.kind, err)
			}
		})
	}
	valid := ApimSpec{
		Enabled:         true,
		GatewayURL:      "https://gw.azure-api.net",
		APIPath:         "agents/chat",
		AllowedSuffixes: []string{"azure-api.net"},
	}
	target, err := valid.ValidateResolvedTarget()
	if err != nil || target != "https://gw.azure-api.net/agents/chat" {
		t.Fatalf("unexpected validation result: %q %v", target, err)
	}
}

func TestManagedIdentityAudienceDefaultsToTheCloudAllowList(t *testing.T) {
	doc := validDoc()
	doc["apim"] = map[string]interface{}{
		"target": "https://gw.azure-api.net/agents",
		"auth":   "managed_identity",
	}
	cfg, err := ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Cloud.TrustedAudiences) == 0 || cfg.Apim.Audience != cfg.Cloud.TrustedAudiences[0] {
		t.Fatalf("unexpected default audience: %q (allow-list %#v)", cfg.Apim.Audience, cfg.Cloud.TrustedAudiences)
	}
}
