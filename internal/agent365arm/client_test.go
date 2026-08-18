package agent365arm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
	testResourceGroup = "foundry-rg"
	testAccount       = "foundry-account"
)

type testCredential struct {
	mu     sync.Mutex
	scopes [][]string
	err    error
}

func (c *testCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{
		Token:     "test-token",
		ExpiresOn: time.Now().Add(time.Hour),
	}, nil
}

type recordedRequest struct {
	Method        string
	URL           string
	Authorization string
	Accept        string
	ContentType   string
	IfMatch       string
	Body          []byte
}

type scriptedRoundTripper struct {
	t         *testing.T
	mu        sync.Mutex
	responses []*http.Response
	errors    []error
	requests  []recordedRequest
}

func (s *scriptedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
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
		Accept:        req.Header.Get("Accept"),
		ContentType:   req.Header.Get("Content-Type"),
		IfMatch:       req.Header.Get("If-Match"),
		Body:          body,
	})
	if len(s.errors) > 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		return nil, err
	}
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

func testOptions(
	t *testing.T,
	responses ...*http.Response,
) (Options, *testCredential, *scriptedRoundTripper) {
	t.Helper()
	credential := &testCredential{}
	transport := &scriptedRoundTripper{t: t, responses: responses}
	return Options{
		SubscriptionID: testSubscription,
		ResourceGroup:  testResourceGroup,
		AccountName:    testAccount,
		ARMEndpoint:    AzureCloudARMEndpoint,
		ARMScope:       AzureCloudARMScope,
		Credential:     credential,
		HTTPClient:     &http.Client{Transport: transport},
	}, credential, transport
}

func accountID(resourceGroup, account string) string {
	return "/subscriptions/" + testSubscription +
		"/resourceGroups/" + resourceGroup +
		"/providers/Microsoft.CognitiveServices/accounts/" + account
}

func accountPayload(logging any, status any) map[string]any {
	properties := map[string]any{}
	if logging != nil {
		properties["a365LoggingEnabled"] = logging
	}
	if status != nil {
		properties["a365Status"] = status
	}
	return map[string]any{
		"id":         accountID(testResourceGroup, testAccount),
		"name":       testAccount,
		"location":   "eastus",
		"etag":       `W/"body-etag"`,
		"properties": properties,
	}
}

func jsonResponse(status int, body any) *http.Response {
	data, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(string(data))),
	}
}

func textResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGetStatusReturnsLoggingPresenceAndSystemStatus(t *testing.T) {
	response := jsonResponse(
		http.StatusOK,
		accountPayload(true, string(A365StatusEnabled)),
	)
	response.Header.Set("x-ms-request-id", "request-123")
	response.Header.Set("x-ms-client-request-id", "client-123")
	options, credential, transport := testOptions(t, response)

	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ID != accountID(testResourceGroup, testAccount) ||
		status.Name != testAccount ||
		status.Location != "eastus" {
		t.Fatalf("unexpected resource identity: %#v", status)
	}
	if !status.A365LoggingEnabledPresent ||
		!status.A365LoggingEnabled ||
		status.A365Status != A365StatusEnabled ||
		!status.CollectionActive() {
		t.Fatalf("unexpected Agent 365 status: %#v", status)
	}
	if status.ETag != `W/"body-etag"` ||
		status.Response.RequestID != "request-123" ||
		status.Response.ClientRequestID != "client-123" {
		t.Fatalf("unexpected response metadata: %#v", status)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("expected one GET, got %#v", transport.requests)
	}
	request := transport.requests[0]
	if request.Method != http.MethodGet ||
		request.Authorization != "Bearer test-token" ||
		request.Accept != "application/json" ||
		request.ContentType != "" {
		t.Fatalf("unexpected GET request: %#v", request)
	}
	if len(credential.scopes) != 1 ||
		len(credential.scopes[0]) != 1 ||
		credential.scopes[0][0] != AzureCloudARMScope {
		t.Fatalf("unexpected scopes: %#v", credential.scopes)
	}
}

func TestGetStatusPreservesUnknownStatusAndAbsentFlag(t *testing.T) {
	options, _, _ := testOptions(
		t,
		jsonResponse(http.StatusOK, accountPayload(nil, "FutureConsentState")),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.A365LoggingEnabledPresent ||
		status.A365Status != A365Status("FutureConsentState") ||
		status.A365Status.Known() {
		t.Fatalf("unknown or absent state was not preserved: %#v", status)
	}
}

func TestPatchContainsOnlyLoggingFlagAndVerifies(t *testing.T) {
	patchResponse := textResponse(http.StatusOK, `{}`)
	patchResponse.Header.Set("ETag", `"patch-etag"`)
	patchResponse.Header.Set("x-ms-request-id", "patch-request")
	options, _, transport := testOptions(
		t,
		patchResponse,
		jsonResponse(
			http.StatusOK,
			accountPayload(true, string(A365StatusNotLicensed)),
		),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}

	result, err := client.SetLogging(context.Background(), true, `"current-etag"`)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != MutationVerified ||
		result.Patch.StatusCode != http.StatusOK ||
		result.Patch.ETag != `"patch-etag"` ||
		result.Patch.RequestID != "patch-request" ||
		result.Verified == nil {
		t.Fatalf("unexpected mutation result: %#v", result)
	}
	if result.Verified.A365Status != A365StatusNotLicensed ||
		result.Verified.CollectionActive() {
		t.Fatalf("licensing status was conflated with the logging flag: %#v", result.Verified)
	}
	if len(transport.requests) != 2 ||
		transport.requests[0].Method != http.MethodPatch ||
		transport.requests[1].Method != http.MethodGet {
		t.Fatalf("expected PATCH then GET: %#v", transport.requests)
	}
	patch := transport.requests[0]
	if patch.IfMatch != `"current-etag"` ||
		patch.ContentType != "application/json" {
		t.Fatalf("unexpected PATCH headers: %#v", patch)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(patch.Body, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope) != 1 {
		t.Fatalf("PATCH included broad account fields: %s", patch.Body)
	}
	var properties map[string]json.RawMessage
	if err := json.Unmarshal(envelope["properties"], &properties); err != nil {
		t.Fatal(err)
	}
	if len(properties) != 1 ||
		string(properties["a365LoggingEnabled"]) != "true" {
		t.Fatalf("unexpected PATCH properties: %s", patch.Body)
	}
}

func TestPatchOmitsIfMatchWhenNotProvided(t *testing.T) {
	options, _, transport := testOptions(
		t,
		textResponse(http.StatusNoContent, ""),
		jsonResponse(
			http.StatusOK,
			accountPayload(false, string(A365StatusDisabled)),
		),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Disable(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if transport.requests[0].IfMatch != "" {
		t.Fatalf("unexpected If-Match: %#v", transport.requests[0])
	}
}

func TestVerificationFailsOnlyWhenLoggingFlagDoesNotMatch(t *testing.T) {
	options, _, _ := testOptions(
		t,
		textResponse(http.StatusOK, `{}`),
		jsonResponse(
			http.StatusOK,
			accountPayload(false, string(A365StatusEnabled)),
		),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Enable(context.Background(), "")
	if err == nil ||
		!errs.IsKind(err, "conflict") ||
		!errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected verification conflict, got %#v / %v", result, err)
	}
	if result.Outcome != MutationVerificationFailed ||
		result.Verified == nil ||
		result.Verified.A365Status != A365StatusEnabled {
		t.Fatalf("unexpected verification result: %#v", result)
	}
}

func TestPatchIsNeverRetried(t *testing.T) {
	first := textResponse(http.StatusInternalServerError, `{"error":"retryable"}`)
	first.Header.Set("x-ms-request-id", "failed-patch")
	options, _, transport := testOptions(
		t,
		first,
		textResponse(http.StatusOK, `{}`),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Enable(context.Background(), "")
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected ambiguous PATCH error, got %#v / %v", result, err)
	}
	if result.Outcome != MutationAmbiguous ||
		result.Patch.StatusCode != http.StatusInternalServerError ||
		result.Patch.RequestID != "failed-patch" {
		t.Fatalf("missing reconciliation metadata: %#v", result)
	}
	if len(transport.requests) != 1 ||
		transport.requests[0].Method != http.MethodPatch {
		t.Fatalf("PATCH was retried: %#v", transport.requests)
	}
}

func TestPatchTransportFailureIsAmbiguousAndNotRetried(t *testing.T) {
	options, _, transport := testOptions(t)
	transport.errors = []error{io.ErrUnexpectedEOF}
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Disable(context.Background(), "")
	if err == nil || !errs.IsAmbiguousMutation(err) ||
		result.Outcome != MutationAmbiguous {
		t.Fatalf("expected ambiguous transport result, got %#v / %v", result, err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("PATCH transport error was retried: %#v", transport.requests)
	}
}

func TestMalformedGETResponsesFail(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"invalid JSON", `{`},
		{
			"missing ID",
			`{"name":"foundry-account","location":"eastus","properties":{}}`,
		},
		{
			"logging is not boolean",
			`{"id":"` + accountID(testResourceGroup, testAccount) +
				`","name":"foundry-account","location":"eastus","properties":{"a365LoggingEnabled":"true"}}`,
		},
		{
			"status is not string",
			`{"id":"` + accountID(testResourceGroup, testAccount) +
				`","name":"foundry-account","location":"eastus","properties":{"a365Status":7}}`,
		},
		{
			"properties is not object",
			`{"id":"` + accountID(testResourceGroup, testAccount) +
				`","name":"foundry-account","location":"eastus","properties":[]}`,
		},
		{
			"properties is missing",
			`{"id":"` + accountID(testResourceGroup, testAccount) +
				`","name":"foundry-account","location":"eastus"}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, _, _ := testOptions(
				t,
				textResponse(http.StatusOK, test.body),
			)
			client, err := NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.GetStatus(context.Background()); err == nil {
				t.Fatal("expected malformed response error")
			}
		})
	}
}

func TestResponseBodiesAreBounded(t *testing.T) {
	t.Run("GET", func(t *testing.T) {
		options, _, _ := testOptions(
			t,
			textResponse(http.StatusOK, strings.Repeat("x", maxResponseBytes+1)),
		)
		client, err := NewClient(options)
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.GetStatus(context.Background())
		if err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("expected bounded GET response error, got %v", err)
		}
	})

	t.Run("PATCH", func(t *testing.T) {
		options, _, transport := testOptions(
			t,
			textResponse(http.StatusOK, strings.Repeat("x", maxResponseBytes+1)),
		)
		client, err := NewClient(options)
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.Enable(context.Background(), "")
		if err == nil ||
			!errs.IsAmbiguousMutation(err) ||
			result.Outcome != MutationAmbiguous {
			t.Fatalf("expected ambiguous bounded PATCH error, got %#v / %v", result, err)
		}
		if len(transport.requests) != 1 {
			t.Fatalf("bounded PATCH failure was retried: %#v", transport.requests)
		}
	})
}

func TestResourcePathAndAPIVersionAreConstructedSafely(t *testing.T) {
	resourceGroup := "grüppe_(prod)"
	responseBody := accountPayload(true, string(A365StatusEnabled))
	responseBody["id"] = accountID(resourceGroup, testAccount)
	options, _, transport := testOptions(
		t,
		jsonResponse(http.StatusOK, responseBody),
	)
	options.ResourceGroup = resourceGroup
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	requestURL, err := url.Parse(transport.requests[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	wantSegment := url.PathEscape(resourceGroup)
	if !strings.Contains(
		requestURL.EscapedPath(),
		"/resourceGroups/"+wantSegment+"/providers/",
	) {
		t.Fatalf("resource group was not one escaped segment: %s", requestURL)
	}
	if requestURL.Query().Get("api-version") != DefaultAPIVersion ||
		len(requestURL.Query()) != 1 {
		t.Fatalf("unexpected query: %s", requestURL.RawQuery)
	}
}

func TestPlanUsesLoggingFlagNotLicensingStatus(t *testing.T) {
	options, _, _ := testOptions(
		t,
		jsonResponse(
			http.StatusOK,
			accountPayload(true, string(A365StatusNotLicensed)),
		),
	)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ChangeRequired || plan.Action != PlanNoChange ||
		plan.Current.CollectionActive() {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestConflictAndPreconditionResponsesAreTyped(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusPreconditionFailed} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			response := textResponse(status, `{}`)
			response.Header.Set("ETag", `"current"`)
			options, _, transport := testOptions(t, response)
			client, err := NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Enable(context.Background(), `"old"`)
			if err == nil || !errs.IsKind(err, "conflict") {
				t.Fatalf("expected conflict, got %#v / %v", result, err)
			}
			if result.Outcome != MutationRejected ||
				result.Patch.StatusCode != status ||
				result.Patch.ETag != `"current"` ||
				len(transport.requests) != 1 {
				t.Fatalf("unexpected conflict result: %#v", result)
			}
		})
	}
}

func TestUnauthorizedAndForbiddenIncludeTenantAndRoleGuidance(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			options, _, _ := testOptions(t, textResponse(status, `{}`))
			client, err := NewClient(options)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.GetStatus(context.Background())
			if err == nil {
				t.Fatal("expected authorization error")
			}
			steps := strings.Join(errs.Remediation(err), "\n")
			for _, want := range []string{"Owner or Contributor", "tenant"} {
				if !strings.Contains(strings.ToLower(steps), strings.ToLower(want)) {
					t.Fatalf("missing %q guidance: %q", want, steps)
				}
			}
		})
	}
}

func TestClientRejectsRedirectResponses(t *testing.T) {
	redirect := textResponse(http.StatusTemporaryRedirect, "")
	redirect.Header.Set("Location", "https://evil.example/path")
	options, _, transport := testOptions(t, redirect)
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetStatus(context.Background())
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected redirect security error, got %v", err)
	}
	if len(transport.requests) != 1 {
		t.Fatalf("redirect response was retried: %#v", transport.requests)
	}
}

func TestInvalidRoutingAndIdentifiersFailBeforeAuthentication(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{
			"lookalike endpoint",
			func(options *Options) {
				options.ARMEndpoint = "https://management.azure.com.evil.example"
			},
		},
		{
			"endpoint path",
			func(options *Options) {
				options.ARMEndpoint = "https://management.azure.com/redirect"
			},
		},
		{
			"wrong scope",
			func(options *Options) {
				options.ARMScope = "https://management.azure.com/user_impersonation"
			},
		},
		{
			"api version injection",
			func(options *Options) {
				options.APIVersion = DefaultAPIVersion + "&extra=true"
			},
		},
		{
			"invalid api version date",
			func(options *Options) {
				options.APIVersion = "2026-99-99-preview"
			},
		},
		{
			"invalid account name",
			func(options *Options) {
				options.AccountName = "account/name"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options, credential, transport := testOptions(t)
			test.mutate(&options)
			_, err := NewClient(options)
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected config error, got %v", err)
			}
			if len(credential.scopes) != 0 || len(transport.requests) != 0 {
				t.Fatalf("invalid options reached auth or HTTP")
			}
		})
	}
}

func TestCallerHTTPClientIsClonedAndTimeoutBounded(t *testing.T) {
	original := &http.Client{}
	options, _, _ := testOptions(t)
	options.HTTPClient = original
	if _, err := NewClient(options); err != nil {
		t.Fatal(err)
	}
	if original.CheckRedirect != nil || original.Timeout != 0 {
		t.Fatal("caller-owned HTTP client was mutated")
	}
}

func TestTokenErrorsDoNotReachHTTP(t *testing.T) {
	options, credential, transport := testOptions(t)
	credential.err = errors.New("credential failed")
	client, err := NewClient(options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GetStatus(context.Background())
	if err == nil || !errs.IsKind(err, "auth") {
		t.Fatalf("expected auth error, got %v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatalf("token error reached HTTP: %#v", transport.requests)
	}
}
