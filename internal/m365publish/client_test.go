package m365publish

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
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testProjectEndpoint = "https://account.services.ai.azure.com/api/projects/project"
	testBotARMID        = "/subscriptions/11111111-2222-3333-4444-555555555555" +
		"/resourceGroups/agents-rg/providers/Microsoft.BotService/botServices/agent-bot"
)

type testCredential struct {
	scopes [][]string
}

func (c *testCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type recordedClient struct {
	requests []*http.Request
	bodies   [][]byte
	do       func(*http.Request) (*http.Response, error)
}

func (c *recordedClient) Do(request *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, request)
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		c.bodies = append(c.bodies, body)
	}
	return c.do(request)
}

func validRequest() Request {
	return Request{
		AgentName:           "support-agent",
		AgentDisplayName:    "Support Agent",
		BotServiceARMID:     testBotARMID,
		PublishScope:        "Shared",
		AppVersion:          "1.0.0",
		ShortDescription:    "Handles support requests.",
		FullDescription:     "Handles support requests for the organization.",
		DeveloperName:       "Contoso",
		DeveloperWebsiteURL: "https://example.com",
		PrivacyURL:          "https://example.com/privacy",
		TermsOfUseURL:       "https://example.com/terms",
	}
}

func newTestClient(t *testing.T, httpClient HTTPClient) (*Client, *testCredential) {
	t.Helper()
	credential := &testCredential{}
	client, err := NewClient(Options{
		ProjectEndpoint: testProjectEndpoint,
		Scope:           AzureCloudScope,
		Credential:      credential,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, credential
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestPublishUsesDocumentedContractAndNeverAutopilot(t *testing.T) {
	httpClient := &recordedClient{
		do: func(request *http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, `{"titleId":"title-123"}`)
			resp.Request = request
			return resp, nil
		},
	}
	client, credential := newTestClient(t, httpClient)
	result, err := client.PublishContext(context.Background(), validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if result.TitleID != "title-123" || result.AdminApprovalRequired {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(httpClient.requests) != 1 {
		t.Fatalf("expected one POST, got %d", len(httpClient.requests))
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodPost ||
		request.URL.Path != "/api/projects/project/agents/support-agent/microsoft365/publish" ||
		request.URL.Query().Get("api-version") != APIVersion {
		t.Fatalf("unexpected publish request: %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Authorization") == "" {
		t.Fatal("publish request omitted authorization")
	}
	request.Header.Set("Authorization", "******")
	if request.Header.Get("Authorization") != "******" ||
		request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected headers: %#v", request.Header)
	}
	if len(credential.scopes) != 1 || credential.scopes[0][0] != AzureCloudScope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}
	var body map[string]any
	if err := json.Unmarshal(httpClient.bodies[0], &body); err != nil {
		t.Fatal(err)
	}
	if value, present := body["publishAsAutopilot"]; !present || value != false {
		t.Fatalf("publishAsAutopilot must be explicitly false: %#v", body)
	}
	if body["botServiceArmId"] != testBotARMID || body["publishScope"] != "Shared" {
		t.Fatalf("unexpected publish body: %#v", body)
	}
}

func TestTenantPublishReportsExternalAdminApproval(t *testing.T) {
	httpClient := &recordedClient{
		do: func(request *http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, `{"titleId":"tenant-title"}`)
			resp.Request = request
			return resp, nil
		},
	}
	client, _ := newTestClient(t, httpClient)
	request := validRequest()
	request.PublishScope = "Tenant"
	result, err := client.PublishContext(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !result.AdminApprovalRequired {
		t.Fatalf("tenant publication must require external approval: %#v", result)
	}
}

func TestPublishIsNeverRetriedAfterAmbiguousFailure(t *testing.T) {
	attempts := 0
	base := &recordedClient{
		do: func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("connection reset")
		},
	}
	retrying := httpx.NewRetryClient(base, httpx.Options{
		Retries:   5,
		BaseDelay: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error {
			return nil
		},
	})
	client, _ := newTestClient(t, retrying)
	_, err := client.PublishContext(context.Background(), validRequest())
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected ambiguous mutation, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("publish POST was retried %d times", attempts)
	}
}

func TestTransientResponseIsAmbiguousButConflictIsDeterministic(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		ambiguous bool
	}{
		{"service unavailable", http.StatusServiceUnavailable, true},
		{"version conflict", http.StatusConflict, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &recordedClient{
				do: func(request *http.Request) (*http.Response, error) {
					resp := response(test.status, `{"error":"failed"}`)
					resp.Request = request
					return resp, nil
				},
			}
			client, _ := newTestClient(t, httpClient)
			_, err := client.PublishContext(context.Background(), validRequest())
			if err == nil {
				t.Fatal("expected error")
			}
			if errs.IsAmbiguousMutation(err) != test.ambiguous {
				t.Fatalf("unexpected ambiguity for %d: %v", test.status, err)
			}
		})
	}
}

func TestSuccessfulMalformedResponseRequiresReconciliation(t *testing.T) {
	httpClient := &recordedClient{
		do: func(request *http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, `{}`)
			resp.Request = request
			return resp, nil
		},
	}
	client, _ := newTestClient(t, httpClient)
	_, err := client.PublishContext(context.Background(), validRequest())
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected ambiguous response error, got %v", err)
	}
}

func TestRejectsGovernmentAndUntrustedEndpointsBeforeAuthentication(t *testing.T) {
	for _, endpoint := range []string{
		"https://account.services.ai.azure.us/api/projects/project",
		"https://account.services.ai.azure.com.evil.example/api/projects/project",
		"https://account.services.ai.azure.com/api/projects/project?redirect=1",
	} {
		credential := &testCredential{}
		_, err := NewClient(Options{
			ProjectEndpoint: endpoint,
			Scope:           AzureCloudScope,
			Credential:      credential,
			HTTPClient:      &recordedClient{},
		})
		if err == nil {
			t.Fatalf("expected endpoint rejection for %q", endpoint)
		}
		if len(credential.scopes) != 0 {
			t.Fatalf("invalid endpoint reached authentication: %#v", credential.scopes)
		}
	}
}

func TestRejectsInvalidRequestBeforeAuthentication(t *testing.T) {
	httpClient := &recordedClient{}
	client, credential := newTestClient(t, httpClient)
	request := validRequest()
	request.PublishScope = "Everyone"
	_, err := client.PublishContext(context.Background(), request)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected config error, got %v", err)
	}
	if len(credential.scopes) != 0 || len(httpClient.requests) != 0 {
		t.Fatal("invalid request reached authentication or HTTP")
	}
}

func TestRejectsHostileCatalogMetadataBeforeAuthentication(t *testing.T) {
	cases := map[string]func(*Request){
		"control characters in the display name": func(r *Request) {
			r.AgentDisplayName = "Support\u001b[31m Agent"
		},
		"newline in the display name": func(r *Request) {
			r.AgentDisplayName = "Support\nAgent"
		},
		"NUL in the short description": func(r *Request) {
			r.ShortDescription = "Handles support\x00requests"
		},
		"terminal escape in the full description": func(r *Request) {
			r.FullDescription = "Handles\u001b]0;pwned\u0007 support requests"
		},
		"control characters in the developer name": func(r *Request) {
			r.DeveloperName = "Contoso\r\n"
		},
		"oversized display name": func(r *Request) {
			r.AgentDisplayName = strings.Repeat("a", maxDisplayNameLength+1)
		},
		"oversized full description": func(r *Request) {
			r.FullDescription = strings.Repeat("a", maxFullDescriptionLength+1)
		},
		"invalid UTF-8 developer name": func(r *Request) {
			r.DeveloperName = string([]byte{0xff, 0xfe})
		},
		"whitespace-only display name": func(r *Request) {
			r.AgentDisplayName = "   "
		},
		"bidirectional override in the display name": func(r *Request) {
			r.AgentDisplayName = "Support \u202etnegA"
		},
		"bidirectional isolate in the developer name": func(r *Request) {
			r.DeveloperName = "Contoso\u2066"
		},
	}
	for name, mutate := range cases {
		httpClient := &recordedClient{}
		client, credential := newTestClient(t, httpClient)
		request := validRequest()
		mutate(&request)
		_, err := client.PublishContext(context.Background(), request)
		if err == nil || !errs.IsKind(err, "config") {
			t.Fatalf("%s: expected config rejection, got %v", name, err)
		}
		if len(credential.scopes) != 0 || len(httpClient.requests) != 0 {
			t.Fatalf("%s: hostile metadata reached authentication or the publish POST", name)
		}
	}
}

func TestAcceptsMultilineFullDescriptionAndOmittedDisplayName(t *testing.T) {
	httpClient := &recordedClient{
		do: func(request *http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, `{"titleId":"title-123"}`)
			resp.Request = request
			return resp, nil
		},
	}
	client, _ := newTestClient(t, httpClient)
	request := validRequest()
	request.AgentDisplayName = ""
	request.FullDescription = "Line one.\n\nLine two.\tIndented."
	if _, err := client.PublishContext(context.Background(), request); err != nil {
		t.Fatalf("legitimate multi-line metadata must publish: %v", err)
	}
}

func TestNewClientHardensStandardHTTPClientWithoutMutatingIt(t *testing.T) {
	original := &http.Client{Timeout: time.Second}
	client, _ := newTestClient(t, original)
	hardened, ok := client.http.(*http.Client)
	if !ok {
		t.Fatalf("expected hardened *http.Client, got %T", client.http)
	}
	if hardened == original || hardened.Timeout != original.Timeout {
		t.Fatalf("standard client was not safely cloned: original=%p hardened=%p", original, hardened)
	}
	if original.CheckRedirect != nil {
		t.Fatal("caller-owned HTTP client was mutated")
	}
	request, err := http.NewRequest(http.MethodGet, "https://redirect.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hardened.CheckRedirect(request, nil); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("hardened client must reject redirects: %v", err)
	}
}
