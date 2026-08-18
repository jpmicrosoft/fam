package foundry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type capturedRequestHTTP struct {
	request *http.Request
	body    []byte
}

func (c *capturedRequestHTTP) Do(request *http.Request) (*http.Response, error) {
	c.request = request
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		c.body = body
	}
	response := jsonResp(http.StatusCreated, map[string]interface{}{
		"name":    "agent",
		"version": "draft-1",
		"draft":   true,
		"status":  "active",
	})
	response.Request = request
	return response, nil
}

func TestHostedInvocationRoutes(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"id":               "resp-1",
			"agent_session_id": "session-1",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{"ok": true}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	if _, err := client.InvokeHostedContext(
		context.Background(),
		"agent",
		"responses",
		[]byte(`{"input":"hello","stream":false}`),
		"application/json",
		"",
		"",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InvokeHostedContext(
		context.Background(),
		"agent",
		"invocations",
		[]byte(`{"input":"hello"}`),
		"application/json",
		"session-1",
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	if got := mock.requests[0].URL.Path; got != "/api/projects/project/agents/agent/endpoint/protocols/openai/responses" {
		t.Fatalf("unexpected responses path: %s", got)
	}
	if got := mock.requests[0].URL.Query().Get("api-version"); got != "v1" {
		t.Fatalf("unexpected responses api version: %s", got)
	}
	if got := mock.requests[1].URL.Path; got != "/api/projects/project/agents/agent/endpoint/protocols/invocations" {
		t.Fatalf("unexpected invocations path: %s", got)
	}
	if got := mock.requests[1].URL.Query().Get("agent_session_id"); got != "session-1" {
		t.Fatalf("unexpected invocation session: %s", got)
	}
	if got := mock.requests[1].Header.Get("x-ms-user-isolation-key"); got != "user-1" {
		t.Fatalf("unexpected isolation key: %s", got)
	}
	for _, request := range mock.requests {
		if request.Header.Get("Foundry-Features") != "" {
			t.Fatal("stable Hosted invocation must not send preview feature headers")
		}
	}
}

func TestHostedMCPApprovalContinuation(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"id": "resp-1",
			"output": []map[string]interface{}{{
				"type":         "mcp_approval_request",
				"id":           "approval-1",
				"server_label": "operations",
				"name":         "delete_item",
				"arguments":    map[string]interface{}{"id": "42"},
			}},
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"id": "resp-2",
			"output": []map[string]interface{}{{
				"type": "message",
				"content": []map[string]interface{}{{
					"type": "output_text",
					"text": "deleted",
				}},
			}},
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	initial, err := client.InvokeHostedContext(
		context.Background(),
		"agent",
		"responses",
		[]byte(`{"input":"delete item 42","stream":false}`),
		"application/json",
		"",
		"user-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.ApprovalRequests) != 1 ||
		initial.ApprovalRequests[0].ToolName != "delete_item" {
		t.Fatalf("unexpected approval request: %#v", initial)
	}
	if _, err := client.ContinueHostedApprovalsContext(
		context.Background(),
		"agent",
		initial.ResponseID,
		[]MCPApprovalDecision{{
			ApprovalRequestID: "approval-1",
			Approve:           true,
		}},
		"session-1",
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	requestBody, err := io.ReadAll(mock.requests[1].Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(requestBody, &body); err != nil {
		t.Fatal(err)
	}
	if body["previous_response_id"] != "resp-1" {
		t.Fatalf("unexpected continuation body: %#v", body)
	}
	if body["agent_session_id"] != "session-1" {
		t.Fatalf("continuation lost Hosted session: %#v", body)
	}
	input := body["input"].([]interface{})
	decision := input[0].(map[string]interface{})
	if decision["type"] != "mcp_approval_response" ||
		decision["approval_request_id"] != "approval-1" ||
		decision["approve"] != true {
		t.Fatalf("unexpected approval decision: %#v", decision)
	}
	if got := mock.requests[1].Header.Get("x-ms-user-isolation-key"); got != "user-1" {
		t.Fatalf("continuation lost isolation key: %q", got)
	}
}

func TestHostedSessionAndLogRoutes(t *testing.T) {
	logBody := "event: log\n" +
		`data: {"timestamp":"2026-01-01T00:00:00Z","stream":"stdout","message":"ready"}` +
		"\n\n"
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusCreated, map[string]interface{}{
			"agent_session_id": "session-1",
			"status":           "active",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []map[string]interface{}{{"agent_session_id": "session-1", "status": "active"}},
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"agent_session_id": "session-1",
			"status":           "active",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{}),
		jsonResp(http.StatusOK, map[string]interface{}{}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"entries": []map[string]interface{}{{"name": "data.csv", "size": 3}},
		}),
		{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("abc")),
			Header:     http.Header{"Content-Type": []string{"application/octet-stream"}},
		},
		jsonResp(http.StatusNoContent, map[string]interface{}{}),
		{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(logBody)),
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		},
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	ctx := context.Background()
	if _, err := client.CreateHostedSessionContext(ctx, "agent", "2", "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListHostedSessionsContext(ctx, "agent", "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetHostedSessionContext(ctx, "agent", "session-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.StopHostedSessionContext(ctx, "agent", "session-1", "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.UploadHostedSessionFileContext(
		ctx,
		"agent",
		"session-1",
		"data.csv",
		strings.NewReader("abc"),
		3,
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	if mock.requests[4].GetBody == nil {
		t.Fatal("PUT upload body must be replayable for safe transient retries")
	}
	if _, err := client.ListHostedSessionFilesContext(
		ctx,
		"agent",
		"session-1",
		".",
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	var downloaded bytes.Buffer
	if _, err := client.DownloadHostedSessionFileContext(
		ctx,
		"agent",
		"session-1",
		"data.csv",
		&downloaded,
		10,
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteHostedSessionFileContext(
		ctx,
		"agent",
		"session-1",
		"data.csv",
		"user-1",
	); err != nil {
		t.Fatal(err)
	}
	logs, err := client.StreamHostedLogsContext(
		ctx,
		"agent",
		"2",
		"session-1",
		10,
		1024,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs.Events) != 1 || logs.Events[0].Message != "ready" {
		t.Fatalf("unexpected logs: %#v", logs)
	}

	expectedPaths := []string{
		"/api/projects/project/agents/agent/endpoint/sessions",
		"/api/projects/project/agents/agent/endpoint/sessions",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1:stop",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1/files/content",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1/files",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1/files/content",
		"/api/projects/project/agents/agent/endpoint/sessions/session-1/files",
		"/api/projects/project/agents/agent/versions/2/sessions/session-1:logstream",
	}
	for i, expected := range expectedPaths {
		if got := mock.requests[i].URL.Path; got != expected {
			t.Fatalf("request %d path = %s, want %s", i, got, expected)
		}
		if got := mock.requests[i].URL.Query().Get("api-version"); got != "v1" {
			t.Fatalf("request %d api-version = %s", i, got)
		}
	}
	createBody, err := io.ReadAll(mock.requests[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(createBody), `"agent_version":"2"`) {
		t.Fatalf("session create body did not pin version: %s", createBody)
	}
}

func TestHostedVersionListingIncludesDraftsAndPreservesCursor(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"data":     []map[string]interface{}{{"version": "draft-1", "draft": true}},
			"has_more": true,
			"last_id":  "cursor-1",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []map[string]interface{}{{"version": "2"}},
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	versions, err := client.ListVersionDetailsWithDraftsContext(context.Background(), "agent", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("unexpected versions: %#v", versions)
	}
	for i, request := range mock.requests {
		query := request.URL.Query()
		if query.Get("include_drafts") != "true" {
			t.Fatalf("request %d dropped include_drafts: %s", i, request.URL.RawQuery)
		}
	}
	if got := mock.requests[1].URL.Query().Get("after"); got != "cursor-1" {
		t.Fatalf("unexpected cursor: %s", got)
	}
}

func TestHostedVersionPaginationRejectsRepeatedCursor(t *testing.T) {
	page := map[string]interface{}{
		"data":     []map[string]interface{}{},
		"has_more": true,
		"last_id":  "same",
	}
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, page),
		jsonResp(http.StatusOK, page),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	_, err := client.ListVersionDetailsWithDraftsContext(context.Background(), "agent", false)
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
}

func TestHostedSessionPaginationRejectsRepeatedCursor(t *testing.T) {
	page := map[string]interface{}{
		"data":     []map[string]interface{}{},
		"has_more": true,
		"last_id":  "same",
	}
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, page),
		jsonResp(http.StatusOK, page),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	if _, err := client.ListHostedSessionsContext(context.Background(), "agent", ""); err == nil {
		t.Fatal("expected repeated session cursor to fail")
	}
}

func TestCreateHostedCodeVersionUsesVerifiedMultipartContract(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "agent.zip")
	if err := os.WriteFile(archivePath, []byte("zip-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	mock := &capturedRequestHTTP{}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	result, err := client.CreateHostedCodeVersionContext(
		context.Background(),
		"agent",
		map[string]interface{}{
			"draft":    true,
			"metadata": map[string]string{"owner": "platform"},
			"definition": map[string]interface{}{
				"kind": "hosted",
			},
		},
		archivePath,
		"abc123",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Draft || result.Version != "draft-1" {
		t.Fatalf("unexpected version: %#v", result)
	}
	request := mock.request
	if request.URL.Path != "/api/projects/project/agents/agent/versions" {
		t.Fatalf("unexpected multipart path: %s", request.URL.Path)
	}
	if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
		t.Fatalf("unexpected content type: %s", request.Header.Get("Content-Type"))
	}
	if request.Header.Get("x-ms-code-zip-sha256") != "abc123" {
		t.Fatalf("missing code hash header: %#v", request.Header)
	}
	body := mock.body
	if !bytes.Contains(body, []byte(`"draft":true`)) ||
		!bytes.Contains(body, []byte(`"owner":"platform"`)) ||
		!bytes.Contains(body, []byte("zip-bytes")) {
		t.Fatalf("multipart body omitted metadata or code: %q", body)
	}
}

func TestCreateHostedImageVersionSendsMetadata(t *testing.T) {
	mock := &capturedRequestHTTP{}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	_, err := client.CreateHostedVersionContext(
		context.Background(),
		"agent",
		"description",
		map[string]interface{}{"kind": "hosted"},
		true,
		map[string]interface{}{"owner": "platform"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(mock.body, &body); err != nil {
		t.Fatal(err)
	}
	metadata, ok := body["metadata"].(map[string]interface{})
	if !ok || metadata["owner"] != "platform" {
		t.Fatalf("Hosted metadata was not sent: %#v", body)
	}
}
