package botservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testSubscription  = "11111111-2222-3333-4444-555555555555"
	testAppID         = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testTenantID      = "99999999-8888-7777-6666-555555555555"
	testBotName       = "production-bot"
	testResourceGroup = "bot-rg"
)

type testCredential struct {
	mu     sync.Mutex
	scopes [][]string
}

func (c *testCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type recordedRequest struct {
	Method        string
	URL           string
	Authorization string
	ContentType   string
	Body          []byte
}

type scriptedTransport struct {
	t         *testing.T
	mu        sync.Mutex
	responses []*http.Response
	requests  []recordedRequest
}

func (s *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		if err != nil {
			s.t.Fatalf("read request body: %v", err)
		}
	}
	s.requests = append(s.requests, recordedRequest{
		Method:        req.Method,
		URL:           req.URL.String(),
		Authorization: req.Header.Get("Authorization"),
		ContentType:   req.Header.Get("Content-Type"),
		Body:          body,
	})
	if len(s.responses) == 0 {
		s.t.Fatalf("unexpected request: %s %s", req.Method, req.URL)
	}
	resp := s.responses[0]
	s.responses = s.responses[1:]
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	}
	return resp, nil
}

func jsonResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

func providerResponse(state string) *http.Response {
	return jsonResponse(http.StatusOK, map[string]any{
		"namespace":         providerNamespace,
		"registrationState": state,
	})
}

func testOptions(t *testing.T, responses ...*http.Response) (ARMOptions, *testCredential, *scriptedTransport) {
	t.Helper()
	credential := &testCredential{}
	transport := &scriptedTransport{t: t, responses: responses}
	return ARMOptions{
		ARMEndpoint:    AzureCloudARMEndpoint,
		ARMScope:       AzureCloudARMScope,
		SubscriptionID: testSubscription,
		ResourceGroup:  testResourceGroup,
		Credential:     credential,
		HTTPClient:     &http.Client{Transport: transport},
	}, credential, transport
}

func testSpec() BotSpec {
	return BotSpec{
		Name:           testBotName,
		DisplayName:    "Production Bot",
		Endpoint:       "https://bot.example.com/api/messages",
		MSAAppID:       testAppID,
		MSAAppTenantID: testTenantID,
	}
}

func botResponse(spec BotSpec) BotState {
	options := ARMOptions{SubscriptionID: testSubscription, ResourceGroup: testResourceGroup}
	return desiredBotState(options, spec)
}

func channelResponse() TeamsChannelState {
	options := ARMOptions{SubscriptionID: testSubscription, ResourceGroup: testResourceGroup}
	return desiredTeamsChannel(options, testBotName)
}

func TestEnsureBotCreatesWithPinnedARMContractAndVerifies(t *testing.T) {
	spec := testSpec()
	options, credential, transport := testOptions(
		t,
		providerResponse("Registered"),
		jsonResponse(http.StatusNotFound, map[string]any{"error": "missing"}),
		jsonResponse(http.StatusCreated, map[string]any{}),
		jsonResponse(http.StatusOK, botResponse(spec)),
	)

	result, err := EnsureBotContext(context.Background(), options, spec)
	if err != nil {
		t.Fatalf("EnsureBotContext: %v", err)
	}
	if result.Status != StatusCreated || result.ResourceID != botResponse(spec).ID {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(transport.requests) != 4 {
		t.Fatalf("expected provider/GET/PUT/GET, got %#v", transport.requests)
	}
	if transport.requests[0].Method != http.MethodGet ||
		transport.requests[1].Method != http.MethodGet ||
		transport.requests[2].Method != http.MethodPut ||
		transport.requests[3].Method != http.MethodGet {
		t.Fatalf("unexpected request methods: %#v", transport.requests)
	}
	if !strings.Contains(transport.requests[0].URL, "/providers/Microsoft.BotService") ||
		!strings.Contains(transport.requests[0].URL, "api-version="+ProviderAPIVersion) {
		t.Fatalf("unexpected provider URL: %s", transport.requests[0].URL)
	}
	for _, index := range []int{1, 2, 3} {
		request := transport.requests[index]
		if !strings.Contains(request.URL, "/botServices/"+testBotName) ||
			!strings.Contains(request.URL, "api-version="+BotServiceAPIVersion) {
			t.Fatalf("unexpected bot URL: %s", request.URL)
		}
		if request.Authorization != "Bearer test-token" {
			t.Fatalf("request %d has unexpected authorization: %q", index, request.Authorization)
		}
	}
	if transport.requests[2].ContentType != "application/json" {
		t.Fatalf("unexpected PUT content type: %q", transport.requests[2].ContentType)
	}
	var body map[string]any
	if err := json.Unmarshal(transport.requests[2].Body, &body); err != nil {
		t.Fatalf("decode PUT body: %v", err)
	}
	if body["location"] != "global" || body["kind"] != "azurebot" {
		t.Fatalf("unexpected fixed bot state: %#v", body)
	}
	if _, exists := body["id"]; exists {
		t.Fatalf("read-only ID was sent in PUT: %#v", body)
	}
	if body["sku"].(map[string]any)["name"] != "F0" {
		t.Fatalf("unexpected SKU: %#v", body["sku"])
	}
	properties := body["properties"].(map[string]any)
	if properties["msaAppType"] != "SingleTenant" ||
		properties["publicNetworkAccess"] != "Disabled" {
		t.Fatalf("unexpected properties: %#v", properties)
	}
	if len(credential.scopes) != 1 ||
		len(credential.scopes[0]) != 1 ||
		credential.scopes[0][0] != AzureCloudARMScope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}
}

func TestEnsureBotNoOpWhenManagedStateMatches(t *testing.T) {
	spec := testSpec()
	options, _, transport := testOptions(
		t,
		providerResponse("Registered"),
		jsonResponse(http.StatusOK, botResponse(spec)),
	)
	result, err := EnsureBotContext(context.Background(), options, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUnchanged {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("no-op must not PUT: %#v", transport.requests)
	}
}

func TestEnsureBotConflictsOnIdentityTenantOrEndpointDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*BotState)
		field  string
	}{
		{"identity", func(state *BotState) {
			state.Properties.MSAAppID = "00000000-0000-0000-0000-000000000000"
		}, "msaAppId"},
		{"tenant", func(state *BotState) {
			state.Properties.MSAAppTenantID = "00000000-0000-0000-0000-000000000000"
		}, "msaAppTenantId"},
		{"endpoint", func(state *BotState) {
			state.Properties.Endpoint = "https://other.example.com/api/messages"
		}, "endpoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			spec := testSpec()
			current := botResponse(spec)
			test.mutate(&current)
			options, _, transport := testOptions(
				t,
				providerResponse("Registered"),
				jsonResponse(http.StatusOK, current),
			)
			_, err := EnsureBotContext(context.Background(), options, spec)
			if err == nil || !errs.IsKind(err, "conflict") || !strings.Contains(err.Error(), test.field) {
				t.Fatalf("expected actionable %s conflict, got %v", test.field, err)
			}
			if len(transport.requests) != 2 {
				t.Fatalf("conflict must not PUT: %#v", transport.requests)
			}
		})
	}
}

func TestEnsureBotAllowUpdatePutsAndVerifies(t *testing.T) {
	spec := testSpec()
	spec.AllowUpdate = true
	current := botResponse(spec)
	current.Properties.MSAAppID = "00000000-0000-0000-0000-000000000000"
	options, _, transport := testOptions(
		t,
		providerResponse("Registered"),
		jsonResponse(http.StatusOK, current),
		jsonResponse(http.StatusOK, map[string]any{}),
		jsonResponse(http.StatusOK, botResponse(spec)),
	)
	result, err := EnsureBotContext(context.Background(), options, spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUpdated || len(transport.requests) != 4 {
		t.Fatalf("unexpected update result/requests: %#v %#v", result, transport.requests)
	}
}

func TestEnsureTeamsChannelCreatesThenVerifies(t *testing.T) {
	options, credential, transport := testOptions(
		t,
		providerResponse("Registered"),
		jsonResponse(http.StatusNotFound, map[string]any{}),
		jsonResponse(http.StatusCreated, map[string]any{}),
		jsonResponse(http.StatusOK, channelResponse()),
	)
	result, err := EnsureTeamsChannelContext(context.Background(), options, testBotName)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusCreated || result.ResourceID != channelResponse().ID {
		t.Fatalf("unexpected result: %#v", result)
	}
	for _, index := range []int{1, 2, 3} {
		if !strings.Contains(transport.requests[index].URL, "/channels/MsTeamsChannel") ||
			!strings.Contains(transport.requests[index].URL, "api-version="+ChannelsAPIVersion) {
			t.Fatalf("unexpected channel URL: %s", transport.requests[index].URL)
		}
	}
	var body map[string]any
	if err := json.Unmarshal(transport.requests[2].Body, &body); err != nil {
		t.Fatal(err)
	}
	properties := body["properties"].(map[string]any)
	if body["name"] != teamsChannelName ||
		properties["channelName"] != teamsChannelName ||
		len(properties) != 1 {
		t.Fatalf("unexpected channel envelope: %#v", properties)
	}
	if len(credential.scopes) != 1 || credential.scopes[0][0] != AzureCloudARMScope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}
}

func TestEnsureTeamsChannelNoOp(t *testing.T) {
	current := channelResponse()
	current.Name = testBotName + "/" + teamsChannelName
	options, _, transport := testOptions(
		t,
		providerResponse("Registered"),
		jsonResponse(http.StatusOK, current),
	)
	result, err := EnsureTeamsChannelContext(context.Background(), options, testBotName)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != StatusUnchanged || len(transport.requests) != 2 {
		t.Fatalf("unexpected no-op: %#v %#v", result, transport.requests)
	}
}

func TestProviderNotRegisteredIsActionableAndNeverRegisters(t *testing.T) {
	options, _, transport := testOptions(t, providerResponse("NotRegistered"))
	_, err := EnsureBotContext(context.Background(), options, testSpec())
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected config error, got %v", err)
	}
	if !strings.Contains(err.Error(), "az provider register") ||
		!strings.Contains(err.Error(), providerNamespace) {
		t.Fatalf("provider error is not actionable: %v", err)
	}
	if len(transport.requests) != 1 || transport.requests[0].Method != http.MethodGet {
		t.Fatalf("client attempted provider registration: %#v", transport.requests)
	}
}

func TestAzureCloudOnlyRoutingRejectedBeforeAuthentication(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		scope    string
	}{
		{
			"government",
			"https://management.usgovcloudapi.net",
			"https://management.core.usgovcloudapi.net/.default",
		},
		{
			"untrusted commercial lookalike",
			"https://management.azure.com.evil.example",
			AzureCloudARMScope,
		},
		{
			"commercial endpoint with path",
			"https://management.azure.com/redirect",
			AzureCloudARMScope,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, credential, transport := testOptions(t)
			options.ARMEndpoint = test.endpoint
			options.ARMScope = test.scope
			_, err := EnsureBotContext(context.Background(), options, testSpec())
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected AzureCloud config rejection, got %v", err)
			}
			if len(credential.scopes) != 0 || len(transport.requests) != 0 {
				t.Fatalf("invalid routing reached auth/HTTP: %#v %#v", credential.scopes, transport.requests)
			}
		})
	}
}

func TestDefaultHTTPClientRefusesRedirects(t *testing.T) {
	client := defaultHTTPClient()
	request, err := http.NewRequest(http.MethodGet, "https://evil.example/path", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(request, nil); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected redirect security error, got %v", err)
	}
}
