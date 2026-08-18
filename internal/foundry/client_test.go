package foundry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

// mockCred implements azcore.TokenCredential for testing.
type mockCred struct {
	scopes [][]string
}

type failingCred struct{}

func (failingCred) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{}, errors.New("credential unavailable")
}

func (m *mockCred) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	m.scopes = append(m.scopes, append([]string(nil), options.Scopes...))
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

// mockHTTP records requests and returns canned responses.
type mockHTTP struct {
	requests  []*http.Request
	responses []*http.Response
	index     int
}

type failingHTTP struct{}

func (failingHTTP) Do(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection reset")
}

func (m *mockHTTP) Do(req *http.Request) (*http.Response, error) {
	m.requests = append(m.requests, req)
	if m.index < len(m.responses) {
		resp := m.responses[m.index]
		m.index++
		if resp.Request == nil {
			resp.Request = req
		}
		return resp, nil
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Request:    req,
	}, nil
}

func jsonResp(status int, body interface{}) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(data))),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestUpsert_SendsCorrectRequest(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			jsonResp(200, map[string]interface{}{
				"id": "agent-123", "name": "test-agent", "version": 1,
			}),
		},
	}

	credential := &mockCred{}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", credential, mock, false)
	result, err := client.Upsert("test-agent", "gpt-4o", "be nice", "desc", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Name != "test-agent" {
		t.Errorf("expected name test-agent, got %s", result.Name)
	}
	if result.ID != "agent-123" {
		t.Errorf("expected id agent-123, got %s", result.ID)
	}

	// Verify request
	req := mock.requests[0]
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if !strings.Contains(req.URL.Path, "/agents/test-agent/versions") {
		t.Errorf("unexpected path: %s", req.URL.Path)
	}
	if !strings.Contains(req.URL.RawQuery, "api-version=v1") {
		t.Errorf("missing api-version: %s", req.URL.RawQuery)
	}
	if req.Header.Get("Authorization") != "Bearer test-token" {
		t.Errorf("unexpected auth header: %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("Foundry-Features") != "" {
		t.Error("preview header should not be set without allowPreview")
	}
	if len(credential.scopes) != 1 || len(credential.scopes[0]) != 1 || credential.scopes[0][0] != scope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}

	// Verify body
	body, _ := io.ReadAll(req.Body)
	var bodyMap map[string]interface{}
	json.Unmarshal(body, &bodyMap)
	def := bodyMap["definition"].(map[string]interface{})
	if def["kind"] != "prompt" {
		t.Errorf("expected kind prompt, got %v", def["kind"])
	}
	if def["model"] != "gpt-4o" {
		t.Errorf("expected model gpt-4o, got %v", def["model"])
	}
}

func TestUpsertDefinitionAndInvocationPreserveStructuredInputs(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{"id": "agent-1", "name": "agent", "version": 1}),
		jsonResp(200, map[string]interface{}{"id": "response-1", "output_text": "ready"}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	definition := map[string]interface{}{
		"kind":         "prompt",
		"model":        "model",
		"instructions": "help",
		"structured_inputs": map[string]interface{}{
			"storeIds": map[string]interface{}{"required": true},
		},
	}
	if _, err := client.UpsertDefinitionContext(context.Background(), "agent", "", definition); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InvokePromptVersionWithOptionsContext(
		context.Background(),
		"agent",
		"1",
		"ready?",
		InvocationOptions{
			StructuredInputs: map[string]interface{}{"storeIds": []interface{}{"vs-1"}},
			MemoryUserID:     "user-123",
		},
	); err != nil {
		t.Fatal(err)
	}
	if got := mock.requests[1].Header.Get("x-memory-user-id"); got != "user-123" {
		t.Fatalf("unexpected memory user id header %q", got)
	}
	for index, field := range []string{"definition", "structured_inputs"} {
		data, err := io.ReadAll(mock.requests[index].Body)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]interface{}
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body[field]; !exists {
			t.Fatalf("request %d omitted %s: %#v", index, field, body)
		}
	}

}

func TestUpsertDefinitionSendsMetadataAtVersionLevel(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{"id": "agent-1", "name": "agent", "version": 1}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	_, err := client.UpsertDefinitionContext(
		context.Background(),
		"agent",
		"description",
		map[string]interface{}{
			"kind":         "prompt",
			"model":        "model",
			"instructions": "help",
		},
		map[string]string{"owner": "platform"},
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(mock.requests[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	metadata, ok := body["metadata"].(map[string]interface{})
	if !ok || metadata["owner"] != "platform" {
		t.Fatalf("metadata was not sent at the agent-version level: %#v", body)
	}
	if definition := body["definition"].(map[string]interface{}); definition["metadata"] != nil {
		t.Fatalf("metadata must not be nested in the agent definition: %#v", definition)
	}
}

func TestPromptInvocationContinuesMCPApprovalRequests(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"id": "response-1",
			"output": []interface{}{map[string]interface{}{
				"type": "mcp_approval_request", "id": "approval-1",
				"server_label": "github", "name": "create_issue",
				"arguments": `{"title":"Test"}`,
			}},
		}),
		jsonResp(200, map[string]interface{}{"id": "response-2", "output_text": "created"}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	first, err := client.InvokePromptVersionWithOptionsContext(
		context.Background(), "agent", "2", "create an issue", InvocationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.ApprovalRequests) != 1 ||
		first.ApprovalRequests[0].ToolName != "create_issue" {
		t.Fatalf("unexpected approval request: %#v", first)
	}
	final, err := client.ContinuePromptVersionWithApprovalsContext(
		context.Background(),
		"agent",
		"2",
		first.ID,
		[]MCPApprovalDecision{{
			ApprovalRequestID: "approval-1",
			ServerLabel:       "github",
			ToolName:          "create_issue",
			Approve:           true,
		}},
		InvocationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if final.OutputText != "created" {
		t.Fatalf("unexpected final response: %#v", final)
	}
	data, err := io.ReadAll(mock.requests[1].Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != "response-1" {
		t.Fatalf("continuation omitted previous response: %#v", body)
	}
	input := body["input"].([]interface{})
	decision := input[0].(map[string]interface{})
	if decision["type"] != "mcp_approval_response" ||
		decision["approval_request_id"] != "approval-1" ||
		decision["approve"] != true {
		t.Fatalf("unexpected approval response: %#v", decision)
	}
}

func TestRequestOptionsOverrideAPIVersion(t *testing.T) {
	mock := &mockHTTP{}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	resp, err := client.doWithOptions(
		context.Background(),
		http.MethodGet,
		"/memory_stores",
		nil,
		requestOptions{apiVersion: "2025-11-15-preview"},
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := mock.requests[0].URL.Query().Get("api-version"); got != "2025-11-15-preview" {
		t.Fatalf("unexpected api-version %q", got)
	}
}

func TestClientWithOptionsRequiresExplicitScope(t *testing.T) {
	credential := &mockCred{}
	client := NewClientWithOptions(
		"https://acct.services.ai.azure.com/api/projects/p",
		credential,
		&mockHTTP{},
		ClientOptions{},
	)
	_, err := client.GetAgentContext(context.Background(), "agent")
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected missing scope config error, got %v", err)
	}
	if len(credential.scopes) != 0 {
		t.Fatalf("credential must not be called without an explicit scope: %#v", credential.scopes)
	}
}

func TestUpsertMarksTransportFailureAsAmbiguous(t *testing.T) {
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		failingHTTP{},
		false,
	)
	_, err := client.Upsert("agent", "model", "instructions", "", nil, "")
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected ambiguous mutation error, got %v", err)
	}
}

func TestUpsertMarksTransientResponseAsAmbiguous(t *testing.T) {
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		&mockHTTP{responses: []*http.Response{
			jsonResp(http.StatusServiceUnavailable, map[string]interface{}{"error": "busy"}),
		}},
		false,
	)
	_, err := client.Upsert("agent", "model", "instructions", "", nil, "")
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected ambiguous transient mutation error, got %v", err)
	}
}

func TestUpsert_WithRAIPolicySetsPreviewHeader(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			jsonResp(200, map[string]interface{}{
				"id": "a", "name": "n", "version": 1,
			}),
		},
	}
	raiID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/raiPolicies/p"
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, true)
	_, err := client.Upsert("n", "m", "i", "", nil, raiID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	req := mock.requests[0]
	if req.Header.Get("Foundry-Features") != previewHeader {
		t.Errorf("expected preview header, got %q", req.Header.Get("Foundry-Features"))
	}

	// Verify rai_config in body
	body, _ := io.ReadAll(req.Body)
	var bodyMap map[string]interface{}
	json.Unmarshal(body, &bodyMap)
	def := bodyMap["definition"].(map[string]interface{})
	raiCfg := def["rai_config"].(map[string]interface{})
	if raiCfg["rai_policy_name"] != raiID {
		t.Errorf("unexpected rai_policy_name: %v", raiCfg["rai_policy_name"])
	}
}

func TestDeleteAgent_404IsIdempotent(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))},
		},
	}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	removed, err := client.DeleteAgent("gone", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed {
		t.Error("expected false for 404")
	}
}

func TestDeleteAgent_ForceParam(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))},
		},
	}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	_, _ = client.DeleteAgent("a", false)
	req := mock.requests[0]
	if !strings.Contains(req.URL.RawQuery, "force=false") {
		t.Errorf("expected force=false in query: %s", req.URL.RawQuery)
	}
}

func TestDisable_PostsCorrectPath(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))},
		},
	}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if err := client.Disable("agent1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.requests[0].URL.Path, "/agents/agent1:disable") {
		t.Errorf("unexpected path: %s", mock.requests[0].URL.Path)
	}
}

func TestEnable_PostsCorrectPath(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))},
		},
	}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if err := client.Enable("agent1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(mock.requests[0].URL.Path, "/agents/agent1:enable") {
		t.Errorf("unexpected path: %s", mock.requests[0].URL.Path)
	}
}

func TestLifecycleTransportAndTransientFailuresAreAmbiguous(t *testing.T) {
	transportClient := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		failingHTTP{},
		false,
	)
	if err := transportClient.Disable("agent1"); !errs.IsAmbiguousMutation(err) {
		t.Fatalf("transport failure must be ambiguous: %v", err)
	}

	transientClient := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		&mockCred{},
		&mockHTTP{responses: []*http.Response{
			jsonResp(http.StatusServiceUnavailable, map[string]interface{}{"error": "busy"}),
		}},
		false,
	)
	if err := transientClient.Enable("agent1"); !errs.IsAmbiguousMutation(err) {
		t.Fatalf("transient response must be ambiguous: %v", err)
	}
}

func TestListVersions_Pagination(t *testing.T) {
	mock := &mockHTTP{
		responses: []*http.Response{
			jsonResp(200, map[string]interface{}{
				"data":     []interface{}{map[string]interface{}{"version": 1}},
				"has_more": true,
				"last_id":  "v1",
			}),
			jsonResp(200, map[string]interface{}{
				"data":     []interface{}{map[string]interface{}{"version": 2}},
				"has_more": false,
			}),
		},
	}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	versions, err := client.ListVersions("a")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(versions) != 2 {
		t.Errorf("expected 2 versions, got %d", len(versions))
	}
	// Check pagination query
	req2 := mock.requests[1]
	if !strings.Contains(req2.URL.RawQuery, "after=v1") {
		t.Errorf("expected after=v1, got %s", req2.URL.RawQuery)
	}
}

func TestGetLatestVersion(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"versions": map[string]interface{}{
				"latest": map[string]interface{}{"version": "7"},
			},
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	version, err := client.GetLatestVersion("agent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != "7" {
		t.Fatalf("unexpected version: %s", version)
	}
}

func TestGetLatestVersionRejectsMalformedResponse(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"versions": map[string]interface{}{"latest": map[string]interface{}{}},
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if _, err := client.GetLatestVersion("agent"); err == nil {
		t.Fatal("expected malformed latest-version response to fail")
	}
}

func TestDeleteVersion_404IsIdempotent(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		{StatusCode: 404, Body: io.NopCloser(strings.NewReader("not found"))},
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if err := client.DeleteVersion("agent", "1", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWaitUntilReadyRetriesProjectPropagation(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(404, map[string]interface{}{"error": "Project does not exist"}),
		jsonResp(200, map[string]interface{}{"data": []interface{}{}}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if err := client.WaitUntilReady(time.Second, 0); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mock.requests) != 2 {
		t.Fatalf("expected two readiness requests, got %d", len(mock.requests))
	}
}

func TestWaitUntilReadyDoesNotRetryAuthenticationFailure(t *testing.T) {
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/p",
		failingCred{},
		&mockHTTP{},
		false,
	)
	err := client.WaitUntilReadyContext(context.Background(), time.Second, 10*time.Millisecond)
	if err == nil || !errs.IsKind(err, "auth") {
		t.Fatalf("expected an authentication error, got %v", err)
	}
}

func TestGetAgentReturnsLifecycleAndDefinition(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"object": "agent",
			"id":     "agent-id",
			"name":   "agent",
			"state":  "disabled",
			"versions": map[string]interface{}{
				"latest": map[string]interface{}{
					"id":         "version-id",
					"name":       "agent",
					"version":    9,
					"status":     "active",
					"created_at": 1234,
					"metadata":   map[string]interface{}{"owner": "platform"},
					"definition": map[string]interface{}{
						"kind":         "prompt",
						"model":        "chat",
						"instructions": "help",
					},
				},
			},
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	agent, err := client.GetAgent("agent")
	if err != nil {
		t.Fatal(err)
	}
	if agent.State != "disabled" ||
		agent.Versions.Latest.Version != "9" ||
		agent.Versions.Latest.Metadata["owner"] != "platform" ||
		agent.Versions.Latest.Definition["model"] != "chat" {
		t.Fatalf("unexpected agent: %#v", agent)
	}
}

func TestGetAgentVersionUsesVersionRoute(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"id":         "version-id",
			"name":       "agent",
			"version":    "7",
			"definition": map[string]interface{}{"kind": "prompt", "model": "chat"},
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	version, err := client.GetAgentVersionContext(context.Background(), "agent", "7")
	if err != nil {
		t.Fatal(err)
	}
	if version == nil || version.Version != "7" {
		t.Fatalf("unexpected version: %#v", version)
	}
	if !strings.Contains(mock.requests[0].URL.Path, "/agents/agent/versions/7") {
		t.Fatalf("unexpected path: %s", mock.requests[0].URL)
	}
}

func TestInvokePromptUsesResponsesAPI(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"id":          "resp-1",
			"output_text": "READY",
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	result, err := client.InvokePromptContext(context.Background(), "agent", "Are you ready?")
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "resp-1" || result.OutputText != "READY" {
		t.Fatalf("unexpected invocation: %#v", result)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPost || request.URL.Path != "/api/projects/p/openai/v1/responses" {
		t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
	}
	if request.URL.RawQuery != "" {
		t.Fatalf("OpenAI v1 Responses request must not include a query string: %s", request.URL)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	reference := body["agent_reference"].(map[string]interface{})
	if reference["type"] != "agent_reference" || reference["name"] != "agent" {
		t.Fatalf("unexpected agent reference: %#v", reference)
	}
}

func TestInvokePromptCanPinAgentVersion(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"id":          "resp-2",
			"output_text": "READY",
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	if _, err := client.InvokePromptVersionContext(
		context.Background(),
		"agent",
		"7",
		"Are you ready?",
	); err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(mock.requests[0].Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	reference := body["agent_reference"].(map[string]interface{})
	if reference["version"] != "7" {
		t.Fatalf("smoke invocation was not version-pinned: %#v", reference)
	}
}

func TestPlanPruneRetainsNewestVersions(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(200, map[string]interface{}{
			"versions": map[string]interface{}{
				"latest": map[string]interface{}{
					"name":       "agent",
					"version":    "3",
					"created_at": 30,
					"definition": map[string]interface{}{"kind": "prompt"},
				},
			},
		}),
		jsonResp(200, map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"name": "agent", "version": "1", "created_at": 10, "definition": map[string]interface{}{"kind": "prompt"}},
				map[string]interface{}{"name": "agent", "version": "3", "created_at": 30, "definition": map[string]interface{}{"kind": "prompt"}},
				map[string]interface{}{"name": "agent", "version": "2", "created_at": 20, "definition": map[string]interface{}{"kind": "prompt"}},
			},
			"has_more": false,
		}),
	}}
	client := NewClient("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, false)
	latest, removed, err := client.PlanPruneContext(context.Background(), "agent", 2)
	if err != nil {
		t.Fatal(err)
	}
	if latest != "3" || len(removed) != 1 || removed[0] != "1" {
		t.Fatalf("unexpected prune plan: latest=%s removed=%v", latest, removed)
	}
}
