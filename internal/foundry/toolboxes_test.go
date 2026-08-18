package foundry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestCreateToolboxVersionSendsPreviewHeaderAndPayload(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusCreated, map[string]interface{}{
			"id":      "toolbox-version-id",
			"name":    "operations",
			"version": 2,
		}),
	}}
	client := NewClient(
		"https://account.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	payload := map[string]interface{}{
		"description": "Operational tools.",
		"tools":       []interface{}{map[string]interface{}{"type": "toolbox_search"}},
	}
	version, err := client.CreateToolboxVersionContext(
		context.Background(),
		"operations",
		payload,
		"Skills=V1Preview",
	)
	if err != nil {
		t.Fatal(err)
	}
	if version.Version != "2" || version.Name != "operations" {
		t.Fatalf("unexpected created version: %#v", version)
	}
	request := mock.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/project/toolboxes/operations/versions" ||
		request.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("unexpected request: %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Foundry-Features") != "Skills=V1Preview" {
		t.Fatalf("unexpected preview header: %q", request.Header.Get("Foundry-Features"))
	}
	var got map[string]interface{}
	if err := json.NewDecoder(request.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["description"] != "Operational tools." {
		t.Fatalf("payload mismatch: %#v", got)
	}
}

func TestToolboxLifecycleRoutes(t *testing.T) {
	mock := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"id":              "toolbox-id",
			"name":            "operations",
			"default_version": map[string]interface{}{"version": 2},
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"name": "operations", "version": 1, "created_at": 10},
			},
			"has_more": true,
			"last_id":  "one",
		}),
		jsonResp(http.StatusOK, map[string]interface{}{
			"data": []interface{}{
				map[string]interface{}{"name": "operations", "version": "2", "created_at": 20},
			},
			"has_more": false,
		}),
		jsonResp(http.StatusOK, map[string]interface{}{"name": "operations", "version": "2"}),
		jsonResp(http.StatusOK, map[string]interface{}{}),
		{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("not found"))},
	}}
	client := NewClient(
		"https://account.services.ai.azure.com/api/projects/project",
		&mockCred{},
		mock,
		false,
	)
	logical, err := client.GetToolboxContext(context.Background(), "operations")
	if err != nil {
		t.Fatal(err)
	}
	if logical.DefaultVersion != "2" {
		t.Fatalf("expanded default version was not parsed: %#v", logical)
	}
	versions, err := client.ListToolboxVersionsContext(context.Background(), "operations")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 || versions[0].Version != "1" || versions[1].Version != "2" {
		t.Fatalf("unexpected versions: %#v", versions)
	}
	if mock.requests[2].URL.Query().Get("after") != "one" {
		t.Fatalf("pagination cursor missing: %s", mock.requests[2].URL)
	}
	found, err := client.GetToolboxVersionContext(context.Background(), "operations", "2")
	if err != nil || found == nil || found.Version != "2" {
		t.Fatalf("unexpected version lookup: %#v %v", found, err)
	}
	if err := client.PromoteToolboxVersionContext(
		context.Background(),
		"operations",
		"2",
	); err != nil {
		t.Fatal(err)
	}
	promote := mock.requests[4]
	if promote.Method != http.MethodPatch ||
		promote.URL.Path != "/api/projects/project/toolboxes/operations" {
		t.Fatalf("unexpected promote request: %s %s", promote.Method, promote.URL)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(promote.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["default_version"] != "2" {
		t.Fatalf("unexpected promote body: %#v", body)
	}
	removed, err := client.DeleteToolboxVersionContext(
		context.Background(),
		"operations",
		"1",
	)
	if err != nil || removed {
		t.Fatalf("404 deletion must be idempotent: removed=%t err=%v", removed, err)
	}
}

func TestToolboxMutationsClassifyAmbiguousFailures(t *testing.T) {
	client := NewClient(
		"https://account.services.ai.azure.com/api/projects/project",
		&mockCred{},
		failingHTTP{},
		false,
	)
	if _, err := client.CreateToolboxVersionContext(
		context.Background(),
		"operations",
		map[string]interface{}{"description": "Tools.", "tools": []interface{}{}},
		"",
	); err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("create failure must be ambiguous, got %v", err)
	}
	if err := client.PromoteToolboxVersionContext(
		context.Background(),
		"operations",
		"2",
	); err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("promote failure must be ambiguous, got %v", err)
	}
	if _, err := client.DeleteToolboxVersionContext(
		context.Background(),
		"operations",
		"1",
	); err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("delete failure must be ambiguous, got %v", err)
	}
}
