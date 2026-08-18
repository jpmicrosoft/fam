package foundry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

func TestGetAgentParsesModernEndpointAndPreservesUnknownFields(t *testing.T) {
	response := `{
		"object":"agent",
		"id":"agent-id",
		"name":"agent",
		"state":"enabled",
		"future_agent_field":{"receipt":"keep"},
		"agent_endpoint":{
			"future_endpoint_field":true,
			"version_selector":{
				"selector_revision":"future",
				"version_selection_rules":[{
					"type":"FixedRatio",
					"agent_version":"7",
					"traffic_percentage":100,
					"future_rule_field":"keep"
				}]
			},
			"protocol_configuration":{
				"responses":{},
				"activity":{"enable_m365_public_endpoint":true},
				"invocations":{},
				"a2a":{},
				"mcp":{}
			},
			"authorization_schemes":[{
				"type":"Entra",
				"isolation_key_source":{"kind":"Entra","future_identity_source":1},
				"future_authorization_field":"keep"
			}]
		},
		"instance_identity":{"principal_id":"principal","client_id":"client","status":"active"},
		"blueprint":{"principal_id":"blueprint-principal","client_id":"blueprint-client","status":"disabled"},
		"agent_card":{
			"version":"1.0.0",
			"description":"Useful agent",
			"skills":[{
				"id":"search",
				"name":"Search",
				"description":"Finds things",
				"tags":["search"],
				"examples":["Find this"]
			}]
		},
		"versions":{"latest":{
			"id":"version-id",
			"name":"agent",
			"version":7,
			"definition":{"kind":"prompt"},
			"instance_identity":{"principal_id":"version-principal","client_id":"version-client"}
		}}
	}`
	mock := &mockHTTP{responses: []*http.Response{{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(response)),
	}}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)

	agent, err := client.GetAgent("agent")
	if err != nil {
		t.Fatal(err)
	}
	if !agent.IsModernAgent() || agent.IsLegacyAgent() {
		t.Fatalf("unexpected agent classification: %#v", agent)
	}
	if agent.InstanceIdentity.PrincipalID != "principal" ||
		agent.Blueprint.ClientID != "blueprint-client" ||
		agent.Versions.Latest.InstanceIdentity.PrincipalID != "version-principal" {
		t.Fatalf("identities were not parsed: %#v", agent)
	}
	if !agent.AgentEndpoint.ProtocolConfiguration.Has(ProtocolResponses) ||
		!agent.AgentEndpoint.ProtocolConfiguration.Has(ProtocolActivity) ||
		!agent.AgentEndpoint.ProtocolConfiguration.Has(ProtocolInvocations) ||
		!agent.AgentEndpoint.ProtocolConfiguration.Has(ProtocolA2A) ||
		!agent.AgentEndpoint.ProtocolConfiguration.Has(ProtocolMCP) {
		t.Fatalf("protocol key presence was lost: %#v", agent.AgentEndpoint.ProtocolConfiguration)
	}
	if agent.AgentCard.Version != "1.0.0" ||
		len(agent.AgentCard.Skills) != 1 ||
		agent.AgentCard.Skills[0].ID != "search" {
		t.Fatalf("agent card was not parsed: %#v", agent.AgentCard)
	}
	if got, err := agent.EffectiveActiveVersion(); err != nil || got != "7" {
		t.Fatalf("unexpected active version %q: %v", got, err)
	}
	raw := agent.RawVersionSelector()
	if !strings.Contains(string(raw), `"selector_revision":"future"`) {
		t.Fatalf("raw selector was not retained: %s", raw)
	}

	remarshaled, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"future_agent_field",
		"future_endpoint_field",
		"selector_revision",
		"future_rule_field",
		"future_authorization_field",
		"future_identity_source",
	} {
		if !strings.Contains(string(remarshaled), field) {
			t.Errorf("unknown field %q was not preserved: %s", field, remarshaled)
		}
	}
}

func TestVersionSelectorClassification(t *testing.T) {
	pinned := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("4", 100),
	}}
	latest := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule(LatestAgentVersion, 100),
	}}
	split := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("3", 40),
		NewFixedRatioVersionSelectionRule("4", 60),
	}}
	malformed := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("4", 90),
	}}

	tests := []struct {
		name     string
		selector *VersionSelector
		mode     SelectorMode
		active   []string
	}{
		{name: "default latest", mode: SelectorDefaultLatest, active: []string{"4"}},
		{name: "explicit latest", selector: latest, mode: SelectorLatest, active: []string{"4"}},
		{name: "pinned", selector: pinned, mode: SelectorPinned, active: []string{"4"}},
		{name: "split", selector: split, mode: SelectorSplit, active: []string{"3", "4"}},
		{name: "malformed", selector: malformed, mode: SelectorMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ResolveVersionSelector(test.selector, "4")
			if got.Mode != test.mode {
				t.Fatalf("expected %q, got %#v", test.mode, got)
			}
			if strings.Join(got.ActiveVersions, ",") != strings.Join(test.active, ",") {
				t.Fatalf("unexpected active versions: %v", got.ActiveVersions)
			}
		})
	}
	if !pinned.IsPinned() || pinned.IsLatest() || pinned.IsMalformed() {
		t.Fatal("pinned selector helpers disagreed")
	}
	if !latest.IsLatest() || latest.IsPinned() || latest.IsMalformed() {
		t.Fatal("latest selector helpers disagreed")
	}
	if !malformed.IsMalformed() {
		t.Fatal("malformed selector was not detected")
	}
}

func TestMalformedSelectorMissingRequiredTrafficPercentage(t *testing.T) {
	var selector VersionSelector
	if err := json.Unmarshal([]byte(`{
		"version_selection_rules":[{"type":"FixedRatio","agent_version":"4"}]
	}`), &selector); err != nil {
		t.Fatal(err)
	}
	resolution := ResolveVersionSelector(&selector, "4")
	if !resolution.IsMalformed() || !strings.Contains(resolution.Problem, "traffic_percentage") {
		t.Fatalf("expected missing percentage to be malformed: %#v", resolution)
	}
}

func TestPatchRejectsMalformedSelectorBeforeSending(t *testing.T) {
	mock := &mockHTTP{}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	selector := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("4", 90),
	}}
	err := client.PatchVersionSelector("agent", selector)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a configuration error, got %v", err)
	}
	if len(mock.requests) != 0 {
		t.Fatalf("malformed selector was sent to Foundry: %#v", mock.requests)
	}
}

func TestPatchVersionSelectorUsesMergePatchAndPreservesSiblings(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(http.StatusNoContent, nil)}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, true)
	selector := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("9", 100),
	}}

	if err := client.PatchVersionSelectorContext(context.Background(), "agent/name", selector); err != nil {
		t.Fatal(err)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPatch ||
		request.URL.EscapedPath() != "/api/projects/p/agents/agent%2Fname" ||
		request.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("unexpected patch request: %s %s", request.Method, request.URL)
	}
	if got := request.Header.Get("Content-Type"); got != mergePatchContentType {
		t.Fatalf("unexpected content type: %q", got)
	}
	if got := request.Header.Get("Foundry-Features"); got != "" {
		t.Fatalf("endpoint patch must not send preview features: %q", got)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 1 {
		t.Fatalf("selector patch included unrelated top-level fields: %s", body["agent_endpoint"])
	}
	var endpoint map[string]json.RawMessage
	if err := json.Unmarshal(body["agent_endpoint"], &endpoint); err != nil {
		t.Fatal(err)
	}
	if len(endpoint) != 1 || endpoint["version_selector"] == nil {
		t.Fatalf("selector patch would replace endpoint siblings: %s", body["agent_endpoint"])
	}
}

func TestPatchAgentEndpointConfiguration(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(http.StatusOK, map[string]any{})}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	patch := AgentEndpointConfig{
		ProtocolConfiguration: NewProtocolConfiguration(ProtocolResponses, ProtocolMCP),
		AuthorizationSchemes:  []AuthorizationScheme{{Type: AuthorizationSchemeEntra}},
	}
	if err := client.PatchAgentEndpointContext(context.Background(), "agent", patch); err != nil {
		t.Fatal(err)
	}
	var body struct {
		AgentEndpoint AgentEndpointConfig `json:"agent_endpoint"`
	}
	if err := json.NewDecoder(mock.requests[0].Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.AgentEndpoint.ProtocolConfiguration.Has(ProtocolResponses) ||
		!body.AgentEndpoint.ProtocolConfiguration.Has(ProtocolMCP) ||
		len(body.AgentEndpoint.AuthorizationSchemes) != 1 {
		t.Fatalf("unexpected endpoint patch: %#v", body.AgentEndpoint)
	}
}

func TestPatchAgentDetailsSendsEndpointAndCardAsTopLevelSiblings(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{jsonResp(http.StatusNoContent, nil)}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	endpoint := &AgentEndpointConfig{
		VersionSelector: &VersionSelector{
			VersionSelectionRules: []FixedRatioVersionSelectionRule{
				NewFixedRatioVersionSelectionRule(LatestAgentVersion, 100),
			},
		},
		ProtocolConfiguration: NewProtocolConfiguration(ProtocolResponses, ProtocolA2A),
	}
	card := &AgentCard{
		Version: "1.0.0",
		Skills: []AgentCardSkill{{
			ID:   "search",
			Name: "Search",
		}},
	}
	if err := client.PatchAgentDetailsContext(context.Background(), "agent", AgentDetailsPatch{
		AgentEndpoint: endpoint,
		AgentCard:     card,
	}); err != nil {
		t.Fatal(err)
	}

	var body map[string]json.RawMessage
	if err := json.NewDecoder(mock.requests[0].Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["agent_endpoint"] == nil || body["agent_card"] == nil {
		t.Fatalf("details were not top-level siblings: %s", mustJSON(body))
	}
	var endpointBody map[string]json.RawMessage
	if err := json.Unmarshal(body["agent_endpoint"], &endpointBody); err != nil {
		t.Fatal(err)
	}
	if _, nested := endpointBody["agent_card"]; nested {
		t.Fatalf("agent_card was incorrectly nested under agent_endpoint: %s", body["agent_endpoint"])
	}
}

func TestPatchRejectsDocumentedTrafficSplitting(t *testing.T) {
	mock := &mockHTTP{}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	split := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("3", 40),
		NewFixedRatioVersionSelectionRule("4", 60),
	}}

	err := client.PatchVersionSelector("agent", split)
	if err == nil || !errs.IsKind(err, "config") || !strings.Contains(err.Error(), "traffic splitting") {
		t.Fatalf("expected traffic-splitting write rejection, got %v", err)
	}
	if len(mock.requests) != 0 {
		t.Fatalf("traffic-splitting selector was sent: %#v", mock.requests)
	}
	resolution := ResolveVersionSelector(split, "4")
	if resolution.Mode != SelectorSplit || strings.Join(resolution.ActiveVersions, ",") != "3,4" {
		t.Fatalf("service-returned split was not parsed defensively: %#v", resolution)
	}
}

func TestPinAndRestoreLatestSelectorPatches(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusNoContent, nil),
		jsonResp(http.StatusNoContent, nil),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if err := client.PinAgentVersionContext(context.Background(), "agent", "12"); err != nil {
		t.Fatal(err)
	}
	if err := client.RestoreDefaultVersionSelectorContext(context.Background(), "agent"); err != nil {
		t.Fatal(err)
	}

	var pinBody struct {
		AgentEndpoint struct {
			VersionSelector VersionSelector `json:"version_selector"`
		} `json:"agent_endpoint"`
	}
	if err := json.NewDecoder(mock.requests[0].Body).Decode(&pinBody); err != nil {
		t.Fatal(err)
	}
	rules := pinBody.AgentEndpoint.VersionSelector.VersionSelectionRules
	if len(rules) != 1 ||
		rules[0].AgentVersion != "12" ||
		rules[0].TrafficPercentage != 100 ||
		rules[0].Type != VersionSelectorFixedRatio {
		t.Fatalf("unexpected pin patch: %#v", rules)
	}

	var restoreBody struct {
		AgentEndpoint struct {
			VersionSelector VersionSelector `json:"version_selector"`
		} `json:"agent_endpoint"`
	}
	if err := json.NewDecoder(mock.requests[1].Body).Decode(&restoreBody); err != nil {
		t.Fatal(err)
	}
	rules = restoreBody.AgentEndpoint.VersionSelector.VersionSelectionRules
	if len(rules) != 1 ||
		rules[0].AgentVersion != LatestAgentVersion ||
		rules[0].TrafficPercentage != 100 ||
		rules[0].Type != VersionSelectorFixedRatio {
		t.Fatalf("unexpected latest patch: %#v", rules)
	}
}

func TestPatchTransportAndTransientFailuresAreAmbiguous(t *testing.T) {
	selector := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("1", 100),
	}}
	transportClient := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		failingHTTP{},
		false,
	)
	if err := transportClient.PatchVersionSelector("agent", selector); !errs.IsAmbiguousMutation(err) {
		t.Fatalf("transport failure must be ambiguous: %v", err)
	}

	transientClient := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		&mockHTTP{responses: []*http.Response{
			jsonResp(http.StatusServiceUnavailable, map[string]any{"error": "busy"}),
		}},
		false,
	)
	if err := transientClient.PatchVersionSelector("agent", selector); !errs.IsAmbiguousMutation(err) {
		t.Fatalf("transient response must be ambiguous: %v", err)
	}
}

func TestPatchAndGetPerformsPostPatchVerification(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusNoContent, nil),
		jsonResp(http.StatusOK, map[string]any{
			"name": "agent",
			"agent_endpoint": map[string]any{
				"version_selector": map[string]any{
					"version_selection_rules": []any{
						map[string]any{
							"type":               VersionSelectorFixedRatio,
							"agent_version":      "5",
							"traffic_percentage": 100,
						},
					},
				},
			},
			"versions": map[string]any{
				"latest": map[string]any{
					"name":       "agent",
					"version":    "6",
					"definition": map[string]any{"kind": "prompt"},
				},
			},
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	selector := &VersionSelector{VersionSelectionRules: []FixedRatioVersionSelectionRule{
		NewFixedRatioVersionSelectionRule("5", 100),
	}}
	agent, err := client.PatchVersionSelectorAndGetContext(context.Background(), "agent", selector)
	if err != nil {
		t.Fatal(err)
	}
	if len(mock.requests) != 2 || mock.requests[1].Method != http.MethodGet {
		t.Fatalf("expected PATCH followed by GET, got %#v", mock.requests)
	}
	if active, err := agent.EffectiveActiveVersion(); err != nil || active != "5" {
		t.Fatalf("verification returned unexpected routing: %q, %v", active, err)
	}
}

func TestInvokeEndpointUsesStableResponsesRoute(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]any{
			"id":          "response-id",
			"output_text": "READY",
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, true)
	result, err := client.InvokeEndpointContext(context.Background(), "agent/name", "Ready?")
	if err != nil {
		t.Fatal(err)
	}
	if result.OutputText != "READY" {
		t.Fatalf("unexpected result: %#v", result)
	}
	request := mock.requests[0]
	if request.URL.EscapedPath() != "/api/projects/p/agents/agent%2Fname/endpoint/protocols/openai/responses" ||
		request.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("unexpected stable endpoint route: %s", request.URL)
	}
	if request.Header.Get("Foundry-Features") != "" {
		t.Fatalf("stable endpoint invocation sent preview header: %q", request.Header.Get("Foundry-Features"))
	}
	var body map[string]any
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["input"] != "Ready?" {
		t.Fatalf("unexpected endpoint request body: %#v", body)
	}
}

func TestPlanPrunePreservesPinnedActiveVersion(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]any{
			"name": "agent",
			"agent_endpoint": map[string]any{
				"version_selector": map[string]any{
					"version_selection_rules": []any{
						map[string]any{
							"type":               VersionSelectorFixedRatio,
							"agent_version":      "1",
							"traffic_percentage": 100,
						},
					},
				},
			},
			"versions": map[string]any{
				"latest": versionJSON("agent", "4", 40),
			},
		}),
		jsonResp(http.StatusOK, map[string]any{
			"data": []any{
				versionJSON("agent", "1", 10),
				versionJSON("agent", "4", 40),
				versionJSON("agent", "2", 20),
				versionJSON("agent", "3", 30),
			},
			"has_more": false,
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	latest, removed, err := client.PlanPrunePreservingActiveContext(context.Background(), "agent", 2)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "4" || strings.Join(removed, ",") != "2" {
		t.Fatalf("active pin was not protected: latest=%q removed=%v", latest, removed)
	}
}

func TestPlanPruneProtectsServiceReturnedTrafficSplit(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]any{
			"name": "agent",
			"agent_endpoint": map[string]any{
				"version_selector": map[string]any{
					"version_selection_rules": []any{
						map[string]any{
							"type":               VersionSelectorFixedRatio,
							"agent_version":      "1",
							"traffic_percentage": 50,
						},
						map[string]any{
							"type":               VersionSelectorFixedRatio,
							"agent_version":      "2",
							"traffic_percentage": 50,
						},
					},
				},
			},
			"versions": map[string]any{
				"latest": versionJSON("agent", "4", 40),
			},
		}),
		jsonResp(http.StatusOK, map[string]any{
			"data": []any{
				versionJSON("agent", "1", 10),
				versionJSON("agent", "4", 40),
				versionJSON("agent", "2", 20),
				versionJSON("agent", "3", 30),
			},
			"has_more": false,
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)

	latest, removed, err := client.PlanPrunePreservingActiveContext(
		context.Background(),
		"agent",
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "4" || strings.Join(removed, ",") != "3" {
		t.Fatalf("split traffic versions were not protected: latest=%q removed=%v", latest, removed)
	}
}

func TestPlanPruneInfersMissingLatestBeforeResolvingLatestRouting(t *testing.T) {
	tests := []struct {
		name          string
		agentEndpoint map[string]any
	}{
		{name: "default latest"},
		{
			name: "explicit latest",
			agentEndpoint: map[string]any{
				"version_selector": map[string]any{
					"version_selection_rules": []any{
						map[string]any{
							"type":               VersionSelectorFixedRatio,
							"agent_version":      LatestAgentVersion,
							"traffic_percentage": 100,
						},
					},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agent := map[string]any{
				"name":     "agent",
				"versions": map[string]any{},
			}
			if test.agentEndpoint != nil {
				agent["agent_endpoint"] = test.agentEndpoint
			}
			mock := &mockHTTP{responses: []*http.Response{
				jsonResp(http.StatusOK, agent),
				jsonResp(http.StatusOK, map[string]any{
					"data": []any{
						versionJSON("agent", "1", 10),
						versionJSON("agent", "3", 30),
						versionJSON("agent", "2", 20),
					},
					"has_more": false,
				}),
			}}
			client := NewClient(
				"https://acct.services.ai.azure.com/api/projects/p",
				&mockCred{},
				mock,
				false,
			)

			latest, removed, err := client.PlanPrunePreservingActiveContext(
				context.Background(),
				"agent",
				1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if latest != "3" || strings.Join(removed, ",") != "2,1" {
				t.Fatalf("unexpected inferred plan: latest=%q removed=%v", latest, removed)
			}
		})
	}
}

func TestPlanVersionRetentionDoesNotMutateInput(t *testing.T) {
	versions := []AgentVersion{
		{Version: "1", CreatedAt: 10},
		{Version: "3", CreatedAt: 30},
		{Version: "2", CreatedAt: 20},
	}
	removed, err := PlanVersionRetention(versions, 1, "1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "2" {
		t.Fatalf("unexpected retention plan: %v", removed)
	}
	if versions[0].Version != "1" || versions[1].Version != "3" {
		t.Fatalf("planner mutated caller input: %#v", versions)
	}
}

func TestEmptyTokenFailsBeforeHTTP(t *testing.T) {
	mock := &mockHTTP{}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		emptyTokenCredential{},
		mock,
		false,
	)
	_, err := client.do(context.Background(), http.MethodGet, "/agents", nil)
	if err == nil || !errs.IsKind(err, "auth") {
		t.Fatalf("expected empty token authentication failure, got %v", err)
	}
	if len(mock.requests) != 0 {
		t.Fatalf("request was sent with an empty token: %#v", mock.requests)
	}
}

func TestDefaultHTTPClientRefusesRedirects(t *testing.T) {
	reachedDestination := make(chan struct{}, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reachedDestination <- struct{}{}
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()

	client := NewClient(source.URL, &mockCred{}, nil, false)
	if err := client.ProbeContext(context.Background()); err == nil {
		t.Fatal("expected redirect response to fail the probe")
	}
	select {
	case <-reachedDestination:
		t.Fatal("default Foundry client followed a redirect")
	default:
	}
}

func TestCustomHTTPClientDestinationChangeIsRejected(t *testing.T) {
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		destinationChangingHTTP{},
		false,
	)
	_, err := client.do(context.Background(), http.MethodGet, "/agents", nil)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected destination-change security error, got %v", err)
	}
}

type emptyTokenCredential struct{}

func (emptyTokenCredential) GetToken(
	context.Context,
	policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	return azcore.AccessToken{}, nil
}

type destinationChangingHTTP struct{}

func (destinationChangingHTTP) Do(request *http.Request) (*http.Response, error) {
	changed := request.Clone(request.Context())
	changed.URL, _ = url.Parse("https://different.example/agents?api-version=v1")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Request:    changed,
	}, nil
}

func mustJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func versionJSON(name, version string, createdAt int64) map[string]any {
	return map[string]any{
		"name":       name,
		"version":    version,
		"created_at": createdAt,
		"definition": map[string]any{"kind": "prompt"},
	}
}
