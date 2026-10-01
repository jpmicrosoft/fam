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

// Synthetic fixtures exercise SDK 2.7.1 serialization and the preview header
// required by the live preview_feature_required response; not service captures.
func TestNativePromptSkillsExperimentalWireAndHeaders(t *testing.T) {
	for _, test := range []struct {
		name       string
		preview    bool
		wantHeader string
	}{
		{name: "live-required-skills-header", wantHeader: skillsPreviewHeader},
		{name: "compose-with-existing-rai-preview", preview: true, wantHeader: previewHeader + "," + skillsPreviewHeader},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition := map[string]interface{}{
				"kind": "prompt", "model": "model", "instructions": "unchanged instructions",
				"skills": []interface{}{map[string]interface{}{"name": "greeting", "version": "1"}},
			}
			mock := &mockHTTP{responses: []*http.Response{
				jsonResp(http.StatusOK, map[string]interface{}{"name": "agent", "version": "4"}),
				jsonResp(http.StatusOK, map[string]interface{}{"name": "agent", "version": "4", "definition": definition}),
				jsonResp(http.StatusOK, map[string]interface{}{"id": "response-1", "output_text": "synthetic response"}),
				jsonResp(http.StatusOK, map[string]interface{}{"name": "greeting", "version": "1"}),
			}}
			client := NewClientWithOptions("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, ClientOptions{
				Scope: PublicScope, AllowPreview: test.preview,
				NativePromptSkillsEnabled: true,
			})
			if _, err := client.UpsertDefinitionContext(context.Background(), "agent", "", definition); err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetAgentVersionContext(context.Background(), "agent", "4"); err != nil {
				t.Fatal(err)
			}
			if _, err := client.InvokePromptVersionWithOptionsContext(context.Background(), "agent", "4", "hello", InvocationOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetSkillVersionContext(context.Background(), "greeting", "1"); err != nil {
				t.Fatal(err)
			}
			if len(mock.requests) != 4 {
				t.Fatalf("unexpected request count: %d", len(mock.requests))
			}
			for _, request := range mock.requests[:3] {
				if request.Header.Get("Foundry-Features") != test.wantHeader {
					t.Fatalf("unexpected native header: %#v", request.Header)
				}
			}
			if mock.requests[3].Header.Get("Foundry-Features") != "Skills=V1Preview" {
				t.Fatal("native transport changed the distinct Skills resource API header")
			}
			for _, request := range mock.requests[:2] {
				if request.URL.Query().Get("api-version") != "v1" {
					t.Fatalf("native create/read must retain SDK v1: %s", request.URL)
				}
			}
			data, err := io.ReadAll(mock.requests[0].Body)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Definition map[string]interface{} `json:"definition"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Fatal(err)
			}
			if body.Definition["instructions"] != "unchanged instructions" ||
				body.Definition["harness"] != nil || body.Definition["tools"] != nil {
				t.Fatalf("native attachment must not select a harness or change prompt/tools: %s", data)
			}
			references, ok := body.Definition["skills"].([]interface{})
			if !ok || len(references) != 1 {
				t.Fatalf("missing native inventory: %s", data)
			}
			reference, ok := references[0].(map[string]interface{})
			if !ok || len(reference) != 2 || reference["name"] != "greeting" || reference["version"] != "1" {
				t.Fatalf("native reference must have only name/version: %s", data)
			}
		})
	}
}

func TestNativePromptSkillsHeaderScopeAndComposition(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		enabled    bool
		preview    bool
		options    requestOptions
		wantHeader string
	}{
		{name: "agent-read", path: "/agents/agent", enabled: true, wantHeader: skillsPreviewHeader},
		{name: "exact-version-readback", path: "/agents/agent/versions/4", enabled: true, wantHeader: skillsPreviewHeader},
		{name: "agent-probe", path: "/agents?limit=1", enabled: true, wantHeader: skillsPreviewHeader},
		{name: "version-invoke", path: "/openai/v1/responses", enabled: true, wantHeader: skillsPreviewHeader},
		{name: "stable-invoke-suppresses-other-preview-only", path: "/agents/agent/endpoint/protocols/openai/responses", enabled: true, preview: true, options: requestOptions{suppressPreview: true}, wantHeader: skillsPreviewHeader},
		{name: "explicit-features-preserved", path: "/agents/agent", enabled: true, options: requestOptions{foundryFeatures: "WorkflowAgents=V1Preview"}, wantHeader: "WorkflowAgents=V1Preview," + skillsPreviewHeader},
		{name: "existing-skills-not-duplicated", path: "/agents/agent", enabled: true, options: requestOptions{foundryFeatures: "WorkflowAgents=V1Preview," + skillsPreviewHeader}, wantHeader: "WorkflowAgents=V1Preview," + skillsPreviewHeader},
		{name: "native-disabled", path: "/agents/agent"},
		{name: "rai-does-not-enable-native", path: "/agents/agent", preview: true, wantHeader: previewHeader},
		{name: "unrelated-model-request", path: "/deployments/model", enabled: true},
		{name: "unrelated-openai-request", path: "/openai/v1/models", enabled: true},
		{name: "similar-prefix-is-not-agent", path: "/agents-other", enabled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			mock := &mockHTTP{}
			client := NewClientWithOptions("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, ClientOptions{
				Scope: PublicScope, AllowPreview: test.preview, NativePromptSkillsEnabled: test.enabled,
			})
			response, err := client.doWithOptions(context.Background(), http.MethodGet, test.path, nil, test.options)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if got := mock.requests[0].Header.Get("Foundry-Features"); got != test.wantHeader {
				t.Fatalf("header = %q, want %q", got, test.wantHeader)
			}
		})
	}
}

func TestNativePromptSkillsHeaderPreservesMultipleFeatureValues(t *testing.T) {
	header := make(http.Header)
	header.Add("Foundry-Features", "WorkflowAgents=V1Preview")
	header.Add("Foundry-Features", "DraftAgents=V1Preview")
	addSkillsPreviewHeader(header)
	addSkillsPreviewHeader(header)
	if got := header.Get("Foundry-Features"); got != "WorkflowAgents=V1Preview,DraftAgents=V1Preview,"+skillsPreviewHeader {
		t.Fatalf("composition lost or duplicated features: %q", got)
	}
}

func TestNativePromptSkillsHeaderDoesNotChangeNoSkillsCreate(t *testing.T) {
	for _, preview := range []bool{false, true} {
		mock := &mockHTTP{responses: []*http.Response{
			jsonResp(http.StatusOK, map[string]interface{}{"name": "agent", "version": "4"}),
		}}
		client := NewClientWithOptions("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, ClientOptions{
			Scope: PublicScope, AllowPreview: preview, NativePromptSkillsEnabled: true,
		})
		if _, err := client.UpsertDefinitionContext(context.Background(), "agent", "", map[string]interface{}{
			"kind": "prompt", "model": "model", "instructions": "unchanged",
		}); err != nil {
			t.Fatal(err)
		}
		want := ""
		if preview {
			want = previewHeader
		}
		if got := mock.requests[0].Header.Get("Foundry-Features"); got != want {
			t.Fatalf("no-Skills creation header changed: %q, want %q", got, want)
		}
	}
}

func TestNativePromptSkillsExperimentalRequiresBothOptIns(t *testing.T) {
	for _, accept := range []bool{false, true} {
		for _, experimental := range []bool{false, true} {
			err := RequireNativePromptSkills(accept, experimental)
			if (err == nil) != (accept && experimental) {
				t.Fatalf("accept=%t experimental=%t: %v", accept, experimental, err)
			}
		}
	}
}

func TestNativePromptSkillsExperimentalServiceErrorsPreserveAmbiguity(t *testing.T) {
	for _, test := range []struct {
		status    int
		ambiguous bool
	}{
		{http.StatusBadRequest, false},
		{http.StatusForbidden, false},
		{http.StatusServiceUnavailable, true},
	} {
		mock := &mockHTTP{responses: []*http.Response{
			jsonResp(test.status, map[string]interface{}{"error": "native skills rejected"}),
		}}
		client := NewClientWithOptions("https://acct.services.ai.azure.com/api/projects/p", &mockCred{}, mock, ClientOptions{
			Scope: PublicScope, NativePromptSkillsEnabled: true,
		})
		_, err := client.UpsertDefinitionContext(context.Background(), "agent", "", map[string]interface{}{
			"kind": "prompt", "skills": []interface{}{},
		})
		if err == nil || errs.IsAmbiguousMutation(err) != test.ambiguous || !strings.Contains(err.Error(), "native skills rejected") {
			t.Fatalf("status=%d: unexpected mutation error: %v", test.status, err)
		}
	}
}
