package httpx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

type sequenceClient struct {
	responses []*http.Response
	errors    []error
	requests  []*http.Request
}

func (c *sequenceClient) Do(req *http.Request) (*http.Response, error) {
	c.requests = append(c.requests, req)
	index := len(c.requests) - 1
	var resp *http.Response
	var err error
	if index < len(c.responses) {
		resp = c.responses[index]
	}
	if index < len(c.errors) {
		err = c.errors[index]
	}
	return resp, err
}

func testResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}
}

func TestRetryClientRetriesSafeMethods(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{
		testResponse(http.StatusTooManyRequests),
		testResponse(http.StatusOK),
	}}
	sleeps := 0
	client := NewRetryClient(base, Options{
		Retries: 2,
		Sleep: func(context.Context, time.Duration) error {
			sleeps++
			return nil
		},
	})
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || len(base.requests) != 2 || sleeps != 1 {
		t.Fatalf("unexpected retry outcome: status=%d requests=%d sleeps=%d", resp.StatusCode, len(base.requests), sleeps)
	}
	if base.requests[0].Header.Get("x-ms-client-request-id") == "" {
		t.Fatal("request ID was not added")
	}
}

func TestRetryTraceContainsOnlySafeRequestMetadata(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{testResponse(http.StatusOK)}}
	var events []TraceEvent
	client := NewRetryClient(base, Options{
		Trace: func(event TraceEvent) {
			events = append(events, event)
		},
	})
	req, _ := http.NewRequest(
		http.MethodGet,
		"https://example.test/api/projects/project?api-key=secret",
		nil,
	)
	if _, err := client.Do(req); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one trace event, got %#v", events)
	}
	event := events[0]
	if event.Method != http.MethodGet ||
		event.Host != "example.test" ||
		event.Path != "/api/projects/project" ||
		event.StatusCode != http.StatusOK ||
		event.Attempt != 1 ||
		event.MaxAttempts != 1 {
		t.Fatalf("unexpected safe trace metadata: %#v", event)
	}
}

func TestRetryClientDoesNotRepeatPost(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{testResponse(http.StatusServiceUnavailable)}}
	client := NewRetryClient(base, Options{Retries: 3})
	req, _ := http.NewRequest(http.MethodPost, "https://example.test", strings.NewReader("{}"))
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable || len(base.requests) != 1 {
		t.Fatalf("POST should not be retried: %#v", base.requests)
	}
}

func TestRetryClientHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := &sequenceClient{
		responses: []*http.Response{nil},
		errors:    []error{errors.New("temporary")},
	}
	client := NewRetryClient(base, Options{
		Retries: 1,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test", nil)
	if _, err := client.Do(req); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

func TestResponseErrorIncludesDiagnostics(t *testing.T) {
	resp := testResponse(http.StatusForbidden)
	resp.Header.Set("x-ms-request-id", "request-1")
	err := ResponseError("Foundry", "get agent", resp, []byte(`{"error":"denied"}`))
	if err == nil || !strings.Contains(err.Error(), "request-1") {
		t.Fatalf("missing diagnostics: %v", err)
	}
}

func TestForbiddenResponseIncludesActionAndScopeRemediation(t *testing.T) {
	action := "Microsoft.CognitiveServices/accounts/deployments/write"
	scope := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct"
	body := []byte(`{
		"error": {
			"code": "AuthorizationFailed",
			"message": "The client does not have authorization to perform action '` +
		action + `' over scope '` + scope + `'."
		}
	}`)
	err := ResponseError(
		"ARM",
		"model deployment creation",
		testResponse(http.StatusForbidden),
		body,
	)
	if !errs.IsKind(err, "authorization") || errs.ExitCode(err) != 5 {
		t.Fatalf("403 was not distinguished from authentication: %v", err)
	}
	steps := errs.Remediation(err)
	if len(steps) != 3 ||
		!strings.Contains(steps[0], action) ||
		!strings.Contains(steps[0], scope) {
		t.Fatalf("403 remediation omitted Azure action or scope: %#v", steps)
	}
}

func TestForbiddenResponseUsesStructuredActionAndSanitizesControls(t *testing.T) {
	err := ResponseError(
		"ARM",
		"resource update",
		testResponse(http.StatusForbidden),
		[]byte(`{
			"error": {
				"action": "Microsoft.Resources/resources/write\u001b",
				"scope": "/subscriptions/sub/resourceGroups/rg",
				"message": "denied"
			}
		}`),
	)
	steps := errs.Remediation(err)
	if len(steps) == 0 ||
		strings.Contains(strings.Join(steps, "\n"), "\x1b") ||
		!strings.Contains(steps[0], "Microsoft.Resources/resources/write") {
		t.Fatalf("structured authorization details were not sanitized: %#v", steps)
	}
}
