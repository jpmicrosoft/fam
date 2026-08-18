package legacyapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testSubscription = "11111111-1111-1111-1111-111111111111"
	testAgentID      = "22222222-2222-2222-2222-222222222222"
)

func TestPinnedLegacyApplicationAPIVersion(t *testing.T) {
	if APIVersion != "2026-05-15-preview" {
		t.Fatalf("unexpected legacy Agent Application API version %q", APIVersion)
	}
}

type recordingCredential struct {
	token       string
	err         error
	returnEmpty bool
	scopes      [][]string
}

func (c *recordingCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	if c.returnEmpty {
		return azcore.AccessToken{ExpiresOn: time.Now().Add(time.Hour)}, nil
	}
	token := c.token
	if token == "" {
		token = "arm-token"
	}
	return azcore.AccessToken{Token: token, ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type HTTPClientFunc func(*http.Request) (*http.Response, error)

func (f HTTPClientFunc) Do(req *http.Request) (*http.Response, error) {
	return f(req)
}

type recordedRequest struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

type recordingHTTPClient struct {
	requests  []recordedRequest
	responses []*http.Response
	errors    []error
}

func (c *recordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	c.requests = append(c.requests, recordedRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Header: req.Header.Clone(),
		Body:   body,
	})
	index := len(c.requests) - 1
	if index < len(c.errors) && c.errors[index] != nil {
		return nil, c.errors[index]
	}
	if index < len(c.responses) {
		return c.responses[index], nil
	}
	return response(http.StatusOK, `{}`, nil), nil
}

func response(status int, body string, headers map[string]string) *http.Response {
	header := make(http.Header)
	for key, value := range headers {
		header.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func testOptions(credential *recordingCredential, httpClient HTTPClient) Options {
	return Options{
		SubscriptionID:  testSubscription,
		ResourceGroup:   "resource-group",
		AccountName:     "account1",
		ProjectName:     "project1",
		ApplicationName: "application1",
		DeploymentName:  "deployment1",
		ARMEndpoint:     "https://management.azure.com",
		ARMScope:        "https://management.azure.com/.default",
		Credential:      credential,
		HTTPClient:      httpClient,
		PollInterval:    time.Nanosecond,
		MaxPollAttempts: 5,
	}
}

func newTestClient(t *testing.T, responses ...*http.Response) (*Client, *recordingCredential, *recordingHTTPClient) {
	t.Helper()
	credential := &recordingCredential{}
	httpClient := &recordingHTTPClient{responses: responses}
	client, err := NewClient(testOptions(credential, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	client.sleep = func(context.Context, time.Duration) error { return nil }
	return client, credential, httpClient
}

func applicationPayload(
	metadata ApplicationMetadata,
	routing *TrafficRoutingPolicy,
	agents ...AgentReference,
) string {
	payload := map[string]any{
		"id":   expectedResourceID(testOptions(&recordingCredential{}, nil), false),
		"name": "application1",
		"etag": `"app-etag"`,
		"properties": map[string]any{
			"displayName": metadata.DisplayName,
			"description": metadata.Description,
			"tags":        metadata.Tags,
		},
	}
	if routing != nil {
		payload["properties"].(map[string]any)["trafficRoutingPolicy"] = routing
	}
	if len(agents) > 0 {
		payload["properties"].(map[string]any)["agents"] = agents
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func deploymentPayload(spec ManagedDeploymentSpec, deploymentID string) string {
	properties := desiredDeploymentProperties(spec)
	properties.DeploymentID = deploymentID
	properties.State = "Running"
	data, _ := json.Marshal(map[string]any{
		"id":         expectedResourceID(testOptions(&recordingCredential{}, nil), true),
		"name":       "deployment1",
		"etag":       `"deployment-etag"`,
		"properties": properties,
	})
	return string(data)
}

func decodeBody(t *testing.T, request recordedRequest) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(request.Body, &body); err != nil {
		t.Fatalf("invalid request JSON: %v\n%s", err, request.Body)
	}
	return body
}

func TestEnsureApplicationCreateExactARMContract(t *testing.T) {
	metadata := ApplicationMetadata{
		DisplayName: "Legacy application",
		Description: "Compatibility application",
		Tags:        map[string]string{"environment": "test"},
	}
	agent := AgentReference{AgentID: testAgentID, AgentName: "agent1"}
	client, credential, httpClient := newTestClient(
		t,
		response(http.StatusNotFound, `{"error":"missing"}`, nil),
		response(http.StatusCreated, `{}`, nil),
		response(
			http.StatusOK,
			applicationPayload(
				ApplicationMetadata{DisplayName: metadata.DisplayName},
				nil,
				AgentReference{
					AgentID:   "azureml://subscriptions/sub/resourceGroups/rg/workspaces/account@project@AML/applications/application1/agents/agent1",
					AgentName: "agent1",
				},
			),
			nil,
		),
	)

	result, err := client.EnsureApplication(context.Background(), metadata, agent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeCreated || !result.State.Exists {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(httpClient.requests) != 3 {
		t.Fatalf("expected GET, PUT, GET; got %d requests", len(httpClient.requests))
	}
	put := httpClient.requests[1]
	expectedPath := "/subscriptions/" + testSubscription +
		"/resourceGroups/resource-group/providers/Microsoft.CognitiveServices/accounts/account1" +
		"/projects/project1/applications/application1"
	parsed, _ := url.Parse(put.URL)
	if put.Method != http.MethodPut || parsed.EscapedPath() != expectedPath {
		t.Fatalf("unexpected application request: %s %s", put.Method, put.URL)
	}
	if parsed.Query().Get("api-version") != APIVersion {
		t.Fatalf("unexpected api-version: %s", parsed.RawQuery)
	}
	if put.Header.Get("Authorization") != "Bearer arm-token" ||
		put.Header.Get("Content-Type") != "application/json" ||
		put.Header.Get("If-None-Match") != "*" {
		t.Fatalf("unexpected headers: %#v", put.Header)
	}
	properties := decodeBody(t, put)["properties"].(map[string]any)
	if properties["displayName"] != metadata.DisplayName ||
		properties["description"] != metadata.Description ||
		properties["tags"].(map[string]any)["environment"] != "test" {
		t.Fatalf("unexpected application body: %#v", properties)
	}
	agents := properties["agents"].([]any)
	if len(agents) != 1 ||
		agents[0].(map[string]any)["agentId"] != testAgentID ||
		agents[0].(map[string]any)["agentName"] != "agent1" {
		t.Fatalf("unexpected application agents: %#v", agents)
	}
	for _, scopes := range credential.scopes {
		if len(scopes) != 1 || scopes[0] != "https://management.azure.com/.default" {
			t.Fatalf("unexpected ARM scopes: %#v", credential.scopes)
		}
	}
}

func TestEnsureApplicationNoOp(t *testing.T) {
	metadata := ApplicationMetadata{DisplayName: "application1"}
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusOK, applicationPayload(metadata, nil), nil),
	)
	result, err := client.EnsureApplication(context.Background(), ApplicationMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeNone || len(httpClient.requests) != 1 {
		t.Fatalf("expected no-op from one GET: result=%#v requests=%d", result, len(httpClient.requests))
	}
}

func TestEnsureApplicationUpdatePreservesRouting(t *testing.T) {
	oldMetadata := ApplicationMetadata{DisplayName: "Old"}
	newMetadata := ApplicationMetadata{DisplayName: "New", Description: "updated"}
	routing := &TrafficRoutingPolicy{
		Protocol: RoutingProtocolFixed,
		Rules: []TrafficRoutingRule{{
			DeploymentID:      "server-deployment-id",
			TrafficPercentage: 100,
		}},
	}
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusOK, applicationPayload(oldMetadata, routing), nil),
		response(http.StatusOK, `{}`, nil),
		response(http.StatusOK, applicationPayload(newMetadata, routing), nil),
	)
	result, err := client.EnsureApplication(context.Background(), newMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeUpdated {
		t.Fatalf("unexpected result: %#v", result)
	}
	put := httpClient.requests[1]
	if put.Header.Get("If-Match") != `"app-etag"` {
		t.Fatalf("missing update precondition: %#v", put.Header)
	}
	properties := decodeBody(t, put)["properties"].(map[string]any)
	policy := properties["trafficRoutingPolicy"].(map[string]any)
	if policy["protocol"] != RoutingProtocolFixed {
		t.Fatalf("routing was not preserved: %#v", properties)
	}
}

func TestEnsureManagedDeploymentCreateExactBody(t *testing.T) {
	spec := ManagedDeploymentSpec{
		AgentID:      testAgentID,
		AgentName:    "agent1",
		AgentVersion: "7",
		DisplayName:  "Managed deployment",
		Description:  "Responses endpoint",
		Tags:         map[string]string{"owner": "legacy"},
	}
	normalizedSpec := spec
	normalizedSpec.Description = ""
	normalizedSpec.Tags = nil
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusNotFound, `{}`, nil),
		response(http.StatusCreated, `{}`, nil),
		response(http.StatusOK, deploymentPayload(normalizedSpec, "deployment-id-1"), nil),
	)
	result, err := client.EnsureManagedDeployment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeCreated || result.State.Properties.DeploymentID != "deployment-id-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
	put := httpClient.requests[1]
	parsed, _ := url.Parse(put.URL)
	expectedSuffix := "/applications/application1/agentDeployments/deployment1"
	if put.Method != http.MethodPut || !strings.HasSuffix(parsed.EscapedPath(), expectedSuffix) ||
		parsed.Query().Get("api-version") != APIVersion {
		t.Fatalf("unexpected deployment request: %s %s", put.Method, put.URL)
	}
	properties := decodeBody(t, put)["properties"].(map[string]any)
	if properties["deploymentType"] != DeploymentTypeManaged {
		t.Fatalf("unexpected deployment type: %#v", properties)
	}
	agents := properties["agents"].([]any)
	agent := agents[0].(map[string]any)
	if agent["agentId"] != testAgentID || agent["agentName"] != "agent1" || agent["agentVersion"] != "7" {
		t.Fatalf("unexpected agent reference: %#v", agent)
	}
	protocol := properties["protocols"].([]any)[0].(map[string]any)
	if protocol["protocol"] != ProtocolResponses || protocol["version"] != ProtocolVersionV1 {
		t.Fatalf("unexpected protocol: %#v", protocol)
	}
}

func TestEnsureManagedDeploymentNoOpAndUpdate(t *testing.T) {
	spec := ManagedDeploymentSpec{
		AgentID:      testAgentID,
		AgentName:    "agent1",
		AgentVersion: "7",
		DisplayName:  "deployment1",
	}
	t.Run("no-op", func(t *testing.T) {
		client, _, httpClient := newTestClient(
			t,
			response(http.StatusOK, deploymentPayload(spec, "deployment-id-1"), nil),
		)
		result, err := client.EnsureManagedDeployment(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if result.Change != ChangeNone || len(httpClient.requests) != 1 {
			t.Fatalf("unexpected no-op result: %#v requests=%d", result, len(httpClient.requests))
		}
	})
	t.Run("update", func(t *testing.T) {
		oldSpec := spec
		oldSpec.AgentVersion = "6"
		client, _, httpClient := newTestClient(
			t,
			response(http.StatusOK, deploymentPayload(oldSpec, "deployment-id-1"), nil),
			response(http.StatusOK, `{}`, nil),
			response(http.StatusOK, deploymentPayload(spec, "deployment-id-1"), nil),
		)
		result, err := client.EnsureManagedDeployment(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		if result.Change != ChangeUpdated || httpClient.requests[1].Header.Get("If-Match") != `"deployment-etag"` {
			t.Fatalf("unexpected update result: %#v headers=%#v", result, httpClient.requests[1].Header)
		}
	})
}

func TestEnsureApplicationPollsAzureAsyncOperation(t *testing.T) {
	metadata := ApplicationMetadata{DisplayName: "application1"}
	asyncURL := "https://management.azure.com/subscriptions/" + testSubscription +
		"/providers/Microsoft.CognitiveServices/locations/eastus/operations/operation-1?api-version=" + APIVersion
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusNotFound, `{}`, nil),
		response(http.StatusAccepted, `{}`, map[string]string{
			"Azure-AsyncOperation": asyncURL,
			"Retry-After":          "0",
		}),
		response(http.StatusOK, `{"status":"Updating"}`, map[string]string{"Retry-After": "0"}),
		response(http.StatusOK, `{"status":"Succeeded"}`, nil),
		response(http.StatusOK, applicationPayload(metadata, nil), nil),
	)
	result, err := client.EnsureApplication(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeUpdated {
		t.Fatalf("HTTP 202 must not claim creation ownership: %#v", result)
	}
	if len(httpClient.requests) != 5 ||
		httpClient.requests[2].URL != asyncURL ||
		httpClient.requests[3].URL != asyncURL {
		t.Fatalf("unexpected LRO requests: %#v", httpClient.requests)
	}
}

func TestRouteAllTrafficUsesDeploymentIDAndIsIdempotent(t *testing.T) {
	spec := ManagedDeploymentSpec{
		AgentID:      testAgentID,
		AgentName:    "agent1",
		AgentVersion: "1",
		DisplayName:  "deployment1",
	}
	metadata := ApplicationMetadata{DisplayName: "application1"}
	agent := AgentReference{AgentID: testAgentID, AgentName: "agent1"}
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusOK, deploymentPayload(spec, "deployment-id-42"), nil),
		response(http.StatusOK, applicationPayload(metadata, nil, agent), nil),
		response(http.StatusOK, `{}`, nil),
		response(http.StatusOK, applicationPayload(metadata, &TrafficRoutingPolicy{
			Protocol: RoutingProtocolFixed,
			Rules: []TrafficRoutingRule{{
				RuleID:            "deployment1",
				DeploymentID:      "deployment-id-42",
				TrafficPercentage: 100,
			}},
		}, agent), nil),
	)
	result, err := client.RouteAllTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeUpdated || result.DeploymentID != "deployment-id-42" {
		t.Fatalf("unexpected routing result: %#v", result)
	}
	properties := decodeBody(t, httpClient.requests[2])["properties"].(map[string]any)
	rule := properties["trafficRoutingPolicy"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if rule["deploymentId"] != "deployment-id-42" || rule["trafficPercentage"] != float64(100) {
		t.Fatalf("unexpected routing rule: %#v", rule)
	}
	agents := properties["agents"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["agentName"] != "agent1" {
		t.Fatalf("application agents were not preserved: %#v", properties)
	}

	routed := &TrafficRoutingPolicy{
		Protocol: RoutingProtocolFixed,
		Rules: []TrafficRoutingRule{{
			DeploymentID:      "deployment-id-42",
			TrafficPercentage: 100,
		}},
	}
	client, _, httpClient = newTestClient(
		t,
		response(http.StatusOK, deploymentPayload(spec, "deployment-id-42"), nil),
		response(http.StatusOK, applicationPayload(metadata, routed), nil),
	)
	result, err = client.RouteAllTraffic(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Change != ChangeNone || len(httpClient.requests) != 2 {
		t.Fatalf("expected routing no-op: %#v requests=%d", result, len(httpClient.requests))
	}
}

func TestDeleteIsIdempotentAndSupportsLocationLRO(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		client, _, httpClient := newTestClient(
			t,
			response(http.StatusNotFound, `{"error":"missing"}`, nil),
		)
		deleted, err := client.DeleteDeployment(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if deleted || httpClient.requests[0].Method != http.MethodDelete {
			t.Fatalf("unexpected delete result: deleted=%v request=%#v", deleted, httpClient.requests)
		}
	})
	t.Run("location LRO", func(t *testing.T) {
		location := "https://management.azure.com/subscriptions/" + testSubscription +
			"/resourceGroups/resource-group/providers/Microsoft.CognitiveServices/operations/delete-1"
		client, _, httpClient := newTestClient(
			t,
			response(http.StatusAccepted, `{}`, map[string]string{"Location": location, "Retry-After": "0"}),
			response(http.StatusOK, `{}`, nil),
		)
		deleted, err := client.DeleteApplication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !deleted || len(httpClient.requests) != 2 || httpClient.requests[1].URL != location {
			t.Fatalf("unexpected LRO delete: deleted=%v requests=%#v", deleted, httpClient.requests)
		}
	})
}

func TestStatusUsesOnlyLegacyARMResources(t *testing.T) {
	metadata := ApplicationMetadata{DisplayName: "application1"}
	spec := ManagedDeploymentSpec{
		AgentID:      testAgentID,
		AgentName:    "agent1",
		AgentVersion: "1",
		DisplayName:  "deployment1",
	}
	client, _, httpClient := newTestClient(
		t,
		response(http.StatusOK, applicationPayload(metadata, nil), nil),
		response(http.StatusOK, deploymentPayload(spec, "deployment-id-1"), nil),
	)
	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Application.Exists || !status.Deployment.Exists || len(httpClient.requests) != 2 {
		t.Fatalf("unexpected status: %#v requests=%#v", status, httpClient.requests)
	}
	for _, request := range httpClient.requests {
		if request.Method != http.MethodGet ||
			request.Header.Get("Authorization") != "Bearer arm-token" ||
			!strings.Contains(request.URL, "/Microsoft.CognitiveServices/") {
			t.Fatalf("status used an unexpected API: %#v", request)
		}
	}
}

func TestValidationRejectsMalformedIdentifiersAndRoutingBeforeAuth(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"subscription", func(o *Options) { o.SubscriptionID = "not-a-uuid" }},
		{"resource group", func(o *Options) { o.ResourceGroup = "bad/group" }},
		{"account", func(o *Options) { o.AccountName = "Uppercase" }},
		{"project", func(o *Options) { o.ProjectName = "bad/project" }},
		{"application", func(o *Options) { o.ApplicationName = "bad\nname" }},
		{"deployment", func(o *Options) { o.DeploymentName = "" }},
		{"endpoint", func(o *Options) { o.ARMEndpoint = "https://example.test" }},
		{"scope mismatch", func(o *Options) {
			o.ARMScope = "https://example.test/.default"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			credential := &recordingCredential{}
			options := testOptions(credential, &recordingHTTPClient{})
			tt.mutate(&options)
			_, err := NewClient(options)
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected config error, got %v", err)
			}
			if len(credential.scopes) != 0 {
				t.Fatalf("authentication ran during validation: %#v", credential.scopes)
			}
		})
	}
}

func TestManagedDeploymentValidationRunsBeforeHTTP(t *testing.T) {
	client, credential, httpClient := newTestClient(t)
	tests := []ManagedDeploymentSpec{
		{AgentID: "bad\nid", AgentName: "agent1", AgentVersion: "1"},
		{AgentID: testAgentID, AgentName: "bad/name", AgentVersion: "1"},
		{AgentID: testAgentID, AgentName: "agent1", AgentVersion: "bad/version"},
	}
	for _, spec := range tests {
		if _, err := client.EnsureManagedDeployment(context.Background(), spec); err == nil ||
			!errs.IsKind(err, "config") {
			t.Fatalf("expected config error for %#v, got %v", spec, err)
		}
	}
	if len(httpClient.requests) != 0 || len(credential.scopes) != 0 {
		t.Fatalf("validation performed I/O: requests=%d scopes=%#v", len(httpClient.requests), credential.scopes)
	}
}

func TestAuthenticationFailureStopsBeforeHTTP(t *testing.T) {
	credential := &recordingCredential{err: errors.New("credential unavailable")}
	httpClient := &recordingHTTPClient{}
	client, err := NewClient(testOptions(credential, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetApplication(context.Background())
	if err == nil || !errs.IsKind(err, "auth") {
		t.Fatalf("expected auth error, got %v", err)
	}
	if len(httpClient.requests) != 0 {
		t.Fatalf("HTTP ran after auth failure: %#v", httpClient.requests)
	}
}

func TestEmptyARMTokenStopsBeforeHTTP(t *testing.T) {
	credential := &recordingCredential{returnEmpty: true}
	httpClient := &recordingHTTPClient{}
	client, err := NewClient(testOptions(credential, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetApplication(context.Background())
	if err == nil || !errs.IsKind(err, "auth") || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("expected empty-token auth error, got %v", err)
	}
	if len(httpClient.requests) != 0 {
		t.Fatalf("HTTP ran with an empty token: %#v", httpClient.requests)
	}
}

func TestDefaultHTTPClientRefusesRedirects(t *testing.T) {
	redirectURL, _ := url.Parse("https://management.azure.com/redirected")
	err := newDefaultHTTPClient().CheckRedirect(&http.Request{URL: redirectURL}, nil)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected redirect security error, got %v", err)
	}
}

func TestCustomStandardHTTPClientIsHardenedWithoutMutation(t *testing.T) {
	credential := &recordingCredential{}
	original := &http.Client{Timeout: 15 * time.Second}
	options := testOptions(credential, original)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	hardened, ok := client.httpClient.(*http.Client)
	if !ok {
		t.Fatalf("expected hardened *http.Client, got %T", client.httpClient)
	}
	if hardened == original || hardened.Timeout != original.Timeout {
		t.Fatalf("custom HTTP client was not safely cloned: original=%p hardened=%p", original, hardened)
	}
	if original.CheckRedirect != nil {
		t.Fatal("the caller's HTTP client was mutated")
	}
	redirectURL := mustURL(t, "https://management.azure.com/redirected")
	if err := hardened.CheckRedirect(&http.Request{URL: redirectURL}, nil); err == nil ||
		!errs.IsKind(err, "security") {
		t.Fatalf("custom HTTP client did not refuse redirects: %v", err)
	}
}

func TestARMRequestOriginIsValidatedBeforeAuthentication(t *testing.T) {
	credential := &recordingCredential{}
	httpClient := &recordingHTTPClient{}
	client, err := NewClient(testOptions(credential, httpClient))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.do(
		context.Background(),
		http.MethodGet,
		"https://example.test/subscriptions/"+testSubscription,
		nil,
		nil,
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected request-origin security error, got %v", err)
	}
	if len(credential.scopes) != 0 || len(httpClient.requests) != 0 {
		t.Fatalf(
			"untrusted request destination reached auth or HTTP: scopes=%#v requests=%#v",
			credential.scopes,
			httpClient.requests,
		)
	}
}

func TestCustomHTTPClientCannotChangeARMDestination(t *testing.T) {
	t.Run("mutated request", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := HTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			req.URL, _ = url.Parse("https://example.test/redirected")
			return response(http.StatusOK, `{}`, nil), nil
		})
		client, err := NewClient(testOptions(credential, httpClient))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected mutated-request security error, got %v", err)
		}
	})

	t.Run("response request", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := HTTPClientFunc(func(_ *http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, `{}`, nil)
			resp.Request = &http.Request{
				URL: mustURL(t, "https://example.test/redirected"),
			}
			return resp, nil
		})
		client, err := NewClient(testOptions(credential, httpClient))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected response-destination security error, got %v", err)
		}
	})

	t.Run("redirect response", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := HTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			resp := response(http.StatusTemporaryRedirect, `{}`, map[string]string{
				"Location": "https://example.test/redirected",
			})
			resp.Request = req
			return resp, nil
		})
		client, err := NewClient(testOptions(credential, httpClient))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected redirect-response security error, got %v", err)
		}
	})

	t.Run("wrapped redirect error", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := HTTPClientFunc(func(req *http.Request) (*http.Response, error) {
			return nil, &url.Error{
				Op:  req.Method,
				URL: req.URL.String(),
				Err: errs.Security("ARM redirect was refused"),
			}
		})
		client, err := NewClient(testOptions(credential, httpClient))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "security") || errs.IsKind(err, "transient") {
			t.Fatalf("expected preserved redirect security error, got %v", err)
		}
	})
}

func TestTransientAndAmbiguousErrorsAreDistinguishedAndRedacted(t *testing.T) {
	t.Run("GET transient", func(t *testing.T) {
		client, _, _ := newTestClient(
			t,
			response(http.StatusServiceUnavailable, `{"error":"retry"}`, nil),
		)
		_, err := client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "transient") || errs.IsAmbiguousMutation(err) {
			t.Fatalf("expected non-ambiguous transient GET error, got %v", err)
		}

	})
	t.Run("PUT transient is ambiguous and redacted", func(t *testing.T) {
		metadata := ApplicationMetadata{DisplayName: "application1"}
		client, _, _ := newTestClient(
			t,
			response(http.StatusNotFound, `{}`, nil),
			response(http.StatusServiceUnavailable, `{"error":"arm-token leaked"}`, nil),
		)
		_, err := client.EnsureApplication(context.Background(), metadata)
		if err == nil || !errs.IsKind(err, "transient") || !errs.IsAmbiguousMutation(err) {
			t.Fatalf("expected ambiguous transient PUT error, got %v", err)
		}
		if strings.Contains(err.Error(), "arm-token") || !strings.Contains(err.Error(), "<redacted>") {
			t.Fatalf("credential was not redacted: %v", err)
		}
	})
	t.Run("transport mutation is ambiguous", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := &recordingHTTPClient{
			responses: []*http.Response{response(http.StatusNotFound, `{}`, nil)},
			errors:    []error{nil, errors.New("connection reset")},
		}
		client, err := NewClient(testOptions(credential, httpClient))
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.EnsureApplication(
			context.Background(),
			ApplicationMetadata{DisplayName: "application1"},
		)
		if err == nil || !errs.IsKind(err, "transient") || !errs.IsAmbiguousMutation(err) {
			t.Fatalf("expected ambiguous transport error, got %v", err)
		}
	})
}

func TestResponseLimitAndReturnedIDValidation(t *testing.T) {
	t.Run("response limit", func(t *testing.T) {
		credential := &recordingCredential{}
		httpClient := &recordingHTTPClient{responses: []*http.Response{
			response(http.StatusOK, strings.Repeat("x", 33), nil),
		}}
		options := testOptions(credential, httpClient)
		client, err := NewClient(options)
		if err != nil {
			t.Fatal(err)
		}
		client.maxResponseBytes = 32
		_, err = client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "foundry") {
			t.Fatalf("expected bounded response error, got %v", err)
		}
	})
	t.Run("cross-resource ID", func(t *testing.T) {
		payload := applicationPayload(ApplicationMetadata{DisplayName: "application1"}, nil)
		payload = strings.Replace(payload, "/applications/application1", "/applications/other", 1)
		client, _, _ := newTestClient(t, response(http.StatusOK, payload, nil))
		_, err := client.GetApplication(context.Background())
		if err == nil || !errs.IsKind(err, "conflict") {
			t.Fatalf("expected returned ID conflict, got %v", err)
		}
	})
}

func TestLRORejectsCrossEndpointAndReportsFailure(t *testing.T) {
	t.Run("cross endpoint", func(t *testing.T) {
		client, _, _ := newTestClient(
			t,
			response(http.StatusAccepted, `{}`, map[string]string{
				"Azure-AsyncOperation": "https://attacker.example.com/subscriptions/" + testSubscription + "/operations/1",
			}),
		)
		_, err := client.DeleteApplication(context.Background())
		if err == nil || !errs.IsKind(err, "security") || !errs.IsAmbiguousMutation(err) {
			t.Fatalf("expected ambiguous security error, got %v", err)
		}
	})
	t.Run("terminal failure", func(t *testing.T) {
		asyncURL := "https://management.azure.com/subscriptions/" + testSubscription + "/operations/failed"
		client, _, _ := newTestClient(
			t,
			response(http.StatusAccepted, `{}`, map[string]string{
				"Azure-AsyncOperation": asyncURL,
				"Retry-After":          "0",
			}),
			response(http.StatusOK, `{"status":"Failed","error":{"code":"DeploymentFailed"}}`, nil),
		)
		_, err := client.DeleteApplication(context.Background())
		if err == nil || !errs.IsKind(err, "foundry") || errs.IsAmbiguousMutation(err) {
			t.Fatalf("expected explicit terminal LRO failure, got %v", err)
		}
	})
}
