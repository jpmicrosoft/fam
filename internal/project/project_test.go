package project

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/arm"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type recordingCredential struct {
	scopes [][]string
}

func (c *recordingCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	return azcore.AccessToken{Token: "arm-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type recordingHTTPClient struct {
	requests  []*http.Request
	responses []*http.Response
}

func (c *recordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	if len(c.responses) == 0 {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func response(status int, body interface{}) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(data))),
		Header:     make(http.Header),
	}
}

func baseProject() *config.ProjectSpec {
	return &config.ProjectSpec{
		Name:           "proj",
		AccountName:    "acct",
		ResourceGroup:  "rg with space",
		SubscriptionID: "sub",
		APIVersion:     "2025-06-01",
		ARMEndpoint:    arm.Endpoint,
		ARMScope:       arm.Scope,
	}
}

func TestInspectProjectRejectsMissingARMRoutingBeforeAuthentication(t *testing.T) {
	project := baseProject()
	project.ARMEndpoint = ""
	credential := &recordingCredential{}
	_, err := InspectProjectContext(context.Background(), project, credential, &recordingHTTPClient{})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected unresolved ARM routing error, got %v", err)
	}
	if len(credential.scopes) != 0 {
		t.Fatalf("authentication should not run with unresolved ARM routing: %#v", credential.scopes)
	}
}

func TestEnsureProjectReturnsExistingEndpoint(t *testing.T) {
	project := baseProject()
	credential := &recordingCredential{}
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(200, map[string]interface{}{
			"location": "East US",
			"properties": map[string]interface{}{
				"endpoints": map[string]interface{}{
					"AI Foundry API": "https://acct.services.ai.azure.com/api/projects/proj",
				},
			},
		}),
	}}

	endpoint, created, err := EnsureProject(project, credential, httpClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if created {
		t.Fatal("existing project should not be reported as created")
	}
	if endpoint != "https://acct.services.ai.azure.com/api/projects/proj" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if len(httpClient.requests) != 1 || httpClient.requests[0].Method != http.MethodGet {
		t.Fatalf("unexpected requests: %#v", httpClient.requests)
	}
	request := httpClient.requests[0]
	if !strings.Contains(request.URL.String(), "rg%20with%20space") {
		t.Fatalf("resource group was not escaped: %s", request.URL)
	}
	if request.URL.Query().Get("api-version") != project.APIVersion {
		t.Fatalf("unexpected api-version: %s", request.URL.RawQuery)
	}
	if request.Header.Get("Authorization") != "Bearer arm-token" {
		t.Fatalf("unexpected authorization header: %q", request.Header.Get("Authorization"))
	}
	if len(credential.scopes) != 1 || len(credential.scopes[0]) != 1 || credential.scopes[0][0] != arm.Scope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}
}

func TestEnsureExistingProjectRequiresARMLocation(t *testing.T) {
	project := baseProject()
	project.AllowedRegions = []string{"eastus", "westus"}
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(200, map[string]interface{}{
			"properties": map[string]interface{}{
				"endpoints": map[string]interface{}{
					"AI Foundry API": "https://acct.services.ai.azure.com/api/projects/proj",
				},
			},
		}),
	}}
	_, _, err := EnsureProject(project, &recordingCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected missing-location config error, got %v", err)
	}
}

func TestEnsureProjectCreatesInAccountRegion(t *testing.T) {
	project := baseProject()
	project.AccountEndpoint = "https://acct.services.ai.azure.com"
	credential := &recordingCredential{}
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(404, map[string]interface{}{"error": "not found"}),
		response(200, map[string]interface{}{"location": "East US"}),
		response(201, map[string]interface{}{}),
	}}

	endpoint, created, err := EnsureProject(project, credential, httpClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !created {
		t.Fatal("missing project should be created")
	}
	if endpoint != "https://acct.services.ai.azure.com/api/projects/proj" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if len(httpClient.requests) != 3 {
		t.Fatalf("expected 3 ARM requests, got %d", len(httpClient.requests))
	}
	if httpClient.requests[0].Method != http.MethodGet ||
		httpClient.requests[1].Method != http.MethodGet ||
		httpClient.requests[2].Method != http.MethodPut {
		t.Fatalf("unexpected request sequence: %s, %s, %s",
			httpClient.requests[0].Method,
			httpClient.requests[1].Method,
			httpClient.requests[2].Method,
		)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(httpClient.requests[2].Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode project request: %v", err)
	}
	if body["location"] != "East US" {
		t.Fatalf("unexpected project location: %#v", body["location"])
	}
	identity := body["identity"].(map[string]interface{})
	if identity["type"] != "SystemAssigned" {
		t.Fatalf("unexpected identity: %#v", identity)
	}
	properties := body["properties"].(map[string]interface{})
	if properties["displayName"] != "proj" {
		t.Fatalf("unexpected display name: %#v", properties)
	}
}

func TestEnsureProjectDoesNotClaimOwnershipFromHTTP200(t *testing.T) {
	project := baseProject()
	project.AccountEndpoint = "https://acct.services.ai.azure.com"
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(404, map[string]interface{}{"error": "not found"}),
		response(200, map[string]interface{}{"location": "East US"}),
		response(200, map[string]interface{}{}),
	}}
	_, created, err := EnsureProject(project, &recordingCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("HTTP 200 must not be treated as confirmed creation ownership")
	}
}

func TestEnsureProjectRejectsRegionMismatch(t *testing.T) {
	project := baseProject()
	project.AccountEndpoint = "https://acct.services.ai.azure.com"
	project.Location = "West US"
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(404, map[string]interface{}{}),
		response(200, map[string]interface{}{"location": "East US"}),
	}}

	_, _, err := EnsureProject(project, &recordingCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected config error, got %v", err)
	}
	if len(httpClient.requests) != 2 {
		t.Fatalf("project creation should not be attempted, got %d requests", len(httpClient.requests))
	}
}

func TestEnsureProjectErrorsWhenEndpointCannotBeDerived(t *testing.T) {
	project := baseProject()
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(200, map[string]interface{}{"properties": map[string]interface{}{}}),
	}}

	_, _, err := EnsureProject(project, &recordingCredential{}, httpClient)
	if err == nil || !errs.IsKind(err, "foundry") {
		t.Fatalf("expected foundry error, got %v", err)
	}
}

func TestInspectProjectReportsMissing(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(404, map[string]interface{}{"error": "not found"}),
	}}
	state, err := InspectProjectContext(context.Background(), baseProject(), &recordingCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatalf("missing project reported as existing: %#v", state)
	}
}

func TestDeleteProjectIsIdempotent(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(404, map[string]interface{}{"error": "not found"}),
	}}
	removed, err := DeleteProjectContext(context.Background(), baseProject(), &recordingCredential{}, httpClient)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("404 should report no project removed")
	}
	if httpClient.requests[0].Method != http.MethodDelete {
		t.Fatalf("unexpected method: %s", httpClient.requests[0].Method)
	}
}
