package connection

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

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func apimSpec(auth string) *config.ApimSpec {
	return &config.ApimSpec{
		Enabled:              true,
		Target:               "https://gateway.azure-api.net/agents/chat",
		Auth:                 auth,
		Audience:             "https://cognitiveservices.azure.com",
		DeploymentInPath:     true,
		InferenceAPIVersion:  "2025-01-01-preview",
		IsSharedToAll:        false,
		ConnectionAPIVersion: "2025-04-01-preview",
	}
}

func projectSpec() *config.ProjectSpec {
	return &config.ProjectSpec{
		Name:           "project",
		AccountName:    "account",
		ResourceGroup:  "rg with space",
		SubscriptionID: "subscription",
		ARMEndpoint:    arm.Endpoint,
		ARMScope:       arm.Scope,
	}
}

func TestGetAPIMConnectionRejectsMissingARMRoutingBeforeAuthentication(t *testing.T) {
	project := projectSpec()
	project.ARMScope = ""
	credential := &recordingCredential{}
	_, err := GetAPIMConnectionContext(
		context.Background(),
		apimSpec("managed_identity"),
		project,
		"connection",
		credential,
		&recordingHTTPClient{},
	)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected unresolved ARM routing error, got %v", err)
	}
	if len(credential.scopes) != 0 {
		t.Fatalf("authentication should not run with unresolved ARM routing: %#v", credential.scopes)
	}
}

func TestBuildConnectionBodyAPIKey(t *testing.T) {
	body, err := BuildConnectionBody(apimSpec("api_key"), []string{"chat-model"}, "secret-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	properties := body["properties"].(map[string]interface{})
	if properties["category"] != "ApiManagement" ||
		properties["authType"] != "ApiKey" ||
		properties["target"] != "https://gateway.azure-api.net/agents/chat" {
		t.Fatalf("unexpected properties: %#v", properties)
	}
	credentials := properties["credentials"].(map[string]interface{})
	if credentials["key"] != "secret-key" {
		t.Fatalf("unexpected credentials: %#v", credentials)
	}
	metadata := properties["metadata"].(map[string]string)
	if metadata["deploymentInPath"] != "true" ||
		metadata["inferenceAPIVersion"] != "2025-01-01-preview" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	var models []map[string]interface{}
	if err := json.Unmarshal([]byte(metadata["models"]), &models); err != nil {
		t.Fatalf("invalid models metadata: %v", err)
	}
	if len(models) != 1 || models[0]["name"] != "chat-model" {
		t.Fatalf("unexpected models metadata: %#v", models)
	}
}

func TestBuildConnectionBodyManagedIdentity(t *testing.T) {
	body, err := BuildConnectionBody(apimSpec("managed_identity"), []string{"chat-model"}, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	properties := body["properties"].(map[string]interface{})
	if properties["authType"] != "ProjectManagedIdentity" {
		t.Fatalf("unexpected authType: %#v", properties["authType"])
	}
	if properties["audience"] != "https://cognitiveservices.azure.com" {
		t.Fatalf("unexpected audience: %#v", properties["audience"])
	}
	if credentials := properties["credentials"].(map[string]interface{}); len(credentials) != 0 {
		t.Fatalf("managed identity credentials should be empty: %#v", credentials)
	}
}

func TestEnsureAPIMConnectionSendsARMContract(t *testing.T) {
	credential := &recordingCredential{}
	httpClient := &recordingHTTPClient{responses: []*http.Response{response(201, "{}")}}
	name, err := EnsureAPIMConnection(
		apimSpec("api_key"),
		projectSpec(),
		"connection name",
		[]string{"chat-model"},
		"secret-key",
		credential,
		httpClient,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "connection name" {
		t.Fatalf("unexpected connection name: %s", name)
	}
	if len(httpClient.requests) != 1 {
		t.Fatalf("expected one request, got %d", len(httpClient.requests))
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodPut {
		t.Fatalf("unexpected method: %s", request.Method)
	}
	if !strings.Contains(request.URL.String(), "rg%20with%20space") ||
		!strings.Contains(request.URL.String(), "connection%20name") {
		t.Fatalf("resource segments were not escaped: %s", request.URL)
	}
	if request.URL.Query().Get("api-version") != "2025-04-01-preview" {
		t.Fatalf("unexpected api-version: %s", request.URL.RawQuery)
	}
	if request.Header.Get("Authorization") != "Bearer arm-token" ||
		request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected headers: %#v", request.Header)
	}
	if len(credential.scopes) != 1 || len(credential.scopes[0]) != 1 || credential.scopes[0][0] != arm.Scope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode request body: %v", err)
	}
	properties := body["properties"].(map[string]interface{})
	credentials := properties["credentials"].(map[string]interface{})
	if credentials["key"] != "secret-key" {
		t.Fatalf("unexpected request body: %#v", body)
	}
}

func TestEnsureAPIMConnectionDoesNotClaimOwnershipFromHTTP200(t *testing.T) {
	result, err := EnsureAPIMConnectionContext(
		context.Background(),
		apimSpec("managed_identity"),
		projectSpec(),
		"connection",
		[]string{"chat-model"},
		"",
		&recordingCredential{},
		&recordingHTTPClient{responses: []*http.Response{response(http.StatusOK, "{}")}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created {
		t.Fatal("HTTP 200 must not be treated as confirmed connection creation ownership")
	}
}

func TestDeleteAPIMConnectionIsIdempotent(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{response(404, `{"error":"not found"}`)}}
	removed, err := DeleteAPIMConnection(
		apimSpec("api_key"),
		projectSpec(),
		"connection",
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed {
		t.Fatal("404 should report that nothing was removed")
	}
	if len(httpClient.requests) != 1 || httpClient.requests[0].Method != http.MethodDelete {
		t.Fatalf("unexpected requests: %#v", httpClient.requests)
	}
}

func TestGetAPIMConnectionReturnsSafeState(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{response(200, `{
		"id": "/connections/apim-agent",
		"name": "apim-agent",
		"properties": {
			"target": "https://gateway.azure-api.net/agents/chat",
			"authType": "ProjectManagedIdentity",
			"credentials": {}
		}
	}`)}}
	state, err := GetAPIMConnectionContext(
		context.Background(),
		apimSpec("managed_identity"),
		projectSpec(),
		"apim-agent",
		&recordingCredential{},
		httpClient,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || state.Name != "apim-agent" || !state.Restorable() {
		t.Fatalf("unexpected state: %#v", state)
	}
	if httpClient.requests[0].Method != http.MethodGet {
		t.Fatalf("unexpected request: %s", httpClient.requests[0].Method)
	}
}

func TestAPIKeyConnectionWithoutReturnedSecretIsNotRestorable(t *testing.T) {
	state := State{
		Exists: true,
		Properties: map[string]interface{}{
			"authType": "ApiKey",
		},
	}
	if state.Restorable() {
		t.Fatal("API-key state without credentials must not be restorable")
	}
}

func TestAPIKeyConnectionWithRedactedSecretIsNotRestorable(t *testing.T) {
	state := State{
		Exists: true,
		Properties: map[string]interface{}{
			"authType":    "ApiKey",
			"credentials": map[string]interface{}{"key": "********"},
		},
	}
	if state.Restorable() {
		t.Fatal("API-key state returned by ARM must never be treated as restorable")
	}
}

func TestUnknownAuthenticationTypeIsNotRestorable(t *testing.T) {
	state := State{
		Exists: true,
		Properties: map[string]interface{}{
			"authType":    "CustomKeys",
			"credentials": map[string]interface{}{"keys": map[string]interface{}{"secret": "********"}},
		},
	}
	if state.Restorable() {
		t.Fatal("an unsupported secret-bearing authentication type must not be restorable")
	}
}

func TestEnsureAPIMConnectionRedactsSecretFromErrors(t *testing.T) {
	const secret = `secret-"value"`
	httpClient := &recordingHTTPClient{responses: []*http.Response{
		response(http.StatusBadRequest, `{"error":"invalid key secret-\"value\""}`),
	}}
	_, err := EnsureAPIMConnection(
		apimSpec("api_key"),
		projectSpec(),
		"connection",
		[]string{"chat-model"},
		secret,
		&recordingCredential{},
		httpClient,
	)
	if err == nil {
		t.Fatal("expected an APIM error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), `secret-\"value\"`) {
		t.Fatalf("error exposed the APIM secret: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("error did not contain a redaction marker: %v", err)
	}
}

func TestRestoreManagedIdentityConnection(t *testing.T) {
	httpClient := &recordingHTTPClient{responses: []*http.Response{response(200, `{}`)}}
	state := State{
		Exists: true,
		Name:   "connection",
		Properties: map[string]interface{}{
			"category":          "ApiManagement",
			"target":            "https://gateway.azure-api.net/old",
			"authType":          "ProjectManagedIdentity",
			"isSharedToAll":     false,
			"audience":          "https://cognitiveservices.azure.com",
			"credentials":       map[string]interface{}{},
			"provisioningState": "Succeeded",
		},
	}
	if err := RestoreAPIMConnectionContext(
		context.Background(),
		apimSpec("managed_identity"),
		projectSpec(),
		state,
		&recordingCredential{},
		httpClient,
	); err != nil {
		t.Fatal(err)
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodPut {
		t.Fatalf("unexpected method: %s", request.Method)
	}
	var body map[string]interface{}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	properties := body["properties"].(map[string]interface{})
	if _, exists := properties["provisioningState"]; exists {
		t.Fatalf("read-only property was included: %#v", properties)
	}
	if properties["target"] != "https://gateway.azure-api.net/old" {
		t.Fatalf("unexpected restore body: %#v", properties)
	}
}

func TestRestoreRejectsCrossCloudManagedIdentityState(t *testing.T) {
	apim := apimSpec("managed_identity")
	apim.AllowedSuffixes = []string{"azure-api.net"}
	apim.BlockedAudienceHosts = []string{"azure.us", "usgovcloudapi.net", "microsoft.us"}
	state := State{
		Exists: true,
		Name:   "connection",
		Properties: map[string]interface{}{
			"category":      "ApiManagement",
			"target":        "https://gateway.azure-api.us/old",
			"authType":      "ProjectManagedIdentity",
			"isSharedToAll": false,
			"audience":      "https://cognitiveservices.azure.us",
			"credentials":   map[string]interface{}{},
		},
	}
	err := RestoreAPIMConnectionContext(
		context.Background(),
		apim,
		projectSpec(),
		state,
		&recordingCredential{},
		&recordingHTTPClient{},
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected cross-cloud restore rejection, got %v", err)
	}
}
