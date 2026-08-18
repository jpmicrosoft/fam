package foundry

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestGetModelDeploymentContextReturnsExactDeployment(t *testing.T) {
	httpClient := &mockHTTP{responses: []*http.Response{
		jsonResp(http.StatusOK, map[string]interface{}{
			"name":           "chat-prod",
			"type":           "ModelDeployment",
			"modelName":      "gpt-5-mini",
			"modelPublisher": "OpenAI",
			"modelVersion":   "2025-08-07",
		}),
	}}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		httpClient,
		true,
	)

	deployment, err := client.GetModelDeploymentContext(context.Background(), "chat-prod")
	if err != nil {
		t.Fatal(err)
	}
	if deployment == nil ||
		deployment.Name != "chat-prod" ||
		deployment.ModelName != "gpt-5-mini" ||
		deployment.ModelPublisher != "OpenAI" {
		t.Fatalf("unexpected deployment: %#v", deployment)
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodGet ||
		request.URL.Path != "/api/projects/project/deployments/chat-prod" ||
		request.URL.Query().Get("api-version") != "v1" {
		t.Fatalf("unexpected deployment request: %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Foundry-Features") != "" {
		t.Fatal("the stable deployment read must not send preview feature headers")
	}
}

func TestGetModelDeploymentContextReturnsNilForMissingDeployment(t *testing.T) {
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		&mockHTTP{responses: []*http.Response{
			jsonResp(http.StatusNotFound, map[string]interface{}{"error": "missing"}),
		}},
		false,
	)
	deployment, err := client.GetModelDeploymentContext(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if deployment != nil {
		t.Fatalf("missing deployment was reported as present: %#v", deployment)
	}
}

func TestGetModelDeploymentContextRejectsMalformedOrMismatchedResponses(t *testing.T) {
	tests := []struct {
		name string
		body interface{}
		kind string
	}{
		{name: "missing required fields", body: map[string]interface{}{}, kind: "foundry"},
		{
			name: "different deployment",
			body: map[string]interface{}{"name": "other", "type": "ModelDeployment"},
			kind: "conflict",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient(
				"https://acct.services.ai.azure.com/api/projects/project",
				&mockCred{},
				&mockHTTP{responses: []*http.Response{jsonResp(http.StatusOK, test.body)}},
				false,
			)
			_, err := client.GetModelDeploymentContext(context.Background(), "expected")
			if err == nil || !errs.IsKind(err, test.kind) {
				t.Fatalf("expected %s error, got %v", test.kind, err)
			}
		})
	}
}

func TestGetModelDeploymentContextClassifiesServiceFailures(t *testing.T) {
	tests := []struct {
		status int
		kind   string
	}{
		{status: http.StatusUnauthorized, kind: "auth"},
		{status: http.StatusForbidden, kind: "authorization"},
		{status: http.StatusInternalServerError, kind: "transient"},
	}
	for _, test := range tests {
		client := NewClient(
			"https://acct.services.ai.azure.com/api/projects/project",
			&mockCred{},
			&mockHTTP{responses: []*http.Response{
				jsonResp(test.status, map[string]interface{}{"error": "failed"}),
			}},
			false,
		)
		_, err := client.GetModelDeploymentContext(context.Background(), "model")
		if err == nil || !errs.IsKind(err, test.kind) {
			t.Fatalf("status %d: expected %s error, got %v", test.status, test.kind, err)
		}
	}
}

func TestGetModelDeploymentContextBoundsResponseSize(t *testing.T) {
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body: io.NopCloser(strings.NewReader(
			strings.Repeat("x", int(maxModelDeploymentResponseBytes)+1),
		)),
	}
	client := NewClient(
		"https://acct.services.ai.azure.com/api/projects/project",
		&mockCred{},
		&mockHTTP{responses: []*http.Response{response}},
		false,
	)
	_, err := client.GetModelDeploymentContext(context.Background(), "model")
	if err == nil || !errs.IsKind(err, "foundry") ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized response must fail closed, got %v", err)
	}
}
