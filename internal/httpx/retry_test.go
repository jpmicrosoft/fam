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

func TestTransientStatusSetMatchesTheRetryContract(t *testing.T) {
	transient := []int{
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}
	for _, status := range transient {
		if !IsTransientStatus(status) {
			t.Errorf("status %d must be transient", status)
		}
	}
	for _, status := range []int{
		http.StatusOK,
		http.StatusCreated,
		http.StatusNoContent,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusNotImplemented,
	} {
		if IsTransientStatus(status) {
			t.Errorf("status %d must not be transient", status)
		}
	}
}

func TestOnlySafeAndIdempotentMethodsAreRetried(t *testing.T) {
	retryable := map[string]bool{
		http.MethodGet:     true,
		http.MethodHead:    true,
		http.MethodOptions: true,
		http.MethodPut:     true,
		http.MethodDelete:  true,
		http.MethodPost:    false,
		http.MethodPatch:   false,
	}
	for method, wantRetry := range retryable {
		t.Run(method, func(t *testing.T) {
			base := &sequenceClient{responses: []*http.Response{
				testResponse(http.StatusServiceUnavailable),
				testResponse(http.StatusOK),
			}}
			client := NewRetryClient(base, Options{
				Retries: 1,
				Sleep:   func(context.Context, time.Duration) error { return nil },
			})
			req, _ := http.NewRequest(method, "https://example.test", nil)
			if _, err := client.Do(req); err != nil {
				t.Fatal(err)
			}
			attempts := len(base.requests)
			if wantRetry && attempts != 2 {
				t.Fatalf("%s must be retried, got %d attempt(s)", method, attempts)
			}
			if !wantRetry && attempts != 1 {
				t.Fatalf("%s must not be repeated, got %d attempt(s)", method, attempts)
			}
		})
	}
}

func TestRetryAfterHeaderIsHonoredAndCapped(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "seconds", header: "2", want: 2 * time.Second},
		{name: "zero seconds", header: "0", want: 0},
		{name: "capped seconds", header: "600", want: 5 * time.Second},
		{name: "past http date", header: time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat), want: 0},
		{name: "unparsable falls back to backoff", header: "soon", want: time.Second},
		{name: "negative falls back to backoff", header: "-5", want: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			throttled := testResponse(http.StatusTooManyRequests)
			throttled.Header.Set("Retry-After", tt.header)
			base := &sequenceClient{responses: []*http.Response{throttled, testResponse(http.StatusOK)}}
			var slept time.Duration
			client := NewRetryClient(base, Options{
				Retries:   1,
				BaseDelay: time.Second,
				MaxDelay:  5 * time.Second,
				Sleep: func(_ context.Context, delay time.Duration) error {
					slept = delay
					return nil
				},
			})
			req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
			if _, err := client.Do(req); err != nil {
				t.Fatal(err)
			}
			if slept != tt.want {
				t.Fatalf("Retry-After %q produced %v, want %v", tt.header, slept, tt.want)
			}
		})
	}
}

func TestBackoffGrowsExponentiallyAndStopsAtMaxDelay(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{
		testResponse(http.StatusBadGateway),
		testResponse(http.StatusBadGateway),
		testResponse(http.StatusBadGateway),
		testResponse(http.StatusBadGateway),
		testResponse(http.StatusOK),
	}}
	var delays []time.Duration
	client := NewRetryClient(base, Options{
		Retries:   4,
		BaseDelay: time.Second,
		MaxDelay:  4 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
	})
	req, _ := http.NewRequest(http.MethodDelete, "https://example.test", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected final status %d", resp.StatusCode)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("unexpected delay sequence: %v", delays)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delay %d was %v, want %v (sequence %v)", i, delays[i], want[i], delays)
		}
	}
}

func TestRetryReplaysTheRequestBodyAndDrainsAbandonedResponses(t *testing.T) {
	first := &trackingBody{Reader: strings.NewReader(`{"first":true}`)}
	base := &sequenceClient{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: first},
		testResponse(http.StatusOK),
	}}
	client := NewRetryClient(base, Options{
		Retries: 1,
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
	req, err := http.NewRequest(http.MethodPut, "https://example.test", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(req); err != nil {
		t.Fatal(err)
	}
	if len(base.requests) != 2 {
		t.Fatalf("expected a retry, got %d attempt(s)", len(base.requests))
	}
	if !first.closed {
		t.Fatal("the abandoned response body must be drained and closed")
	}
	for attempt, request := range base.requests {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("attempt %d body could not be read: %v", attempt, readErr)
		}
		if string(body) != `{"model":"m"}` {
			t.Fatalf("attempt %d replayed %q", attempt, body)
		}
	}
}

func TestNonReplayableBodyIsNotRetried(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{
		testResponse(http.StatusServiceUnavailable),
		testResponse(http.StatusOK),
	}}
	client := NewRetryClient(base, Options{
		Retries: 1,
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
	req, err := http.NewRequest(http.MethodPut, "https://example.test", io.NopCloser(strings.NewReader("{}")))
	if err != nil {
		t.Fatal(err)
	}
	req.GetBody = nil
	if _, err := client.Do(req); err == nil {
		t.Fatal("a body that cannot be replayed must not be retried silently")
	}
}

func TestExhaustedTransportRetriesReportATransientFailure(t *testing.T) {
	transportErr := errors.New("connection reset by peer")
	base := &sequenceClient{
		responses: []*http.Response{nil, nil},
		errors:    []error{transportErr, transportErr},
	}
	client := NewRetryClient(base, Options{
		Retries: 1,
		Sleep:   func(context.Context, time.Duration) error { return nil },
	})
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	_, err := client.Do(req)
	if !errors.Is(err, transportErr) {
		t.Fatalf("the final transport error must be surfaced, got %v", err)
	}
	if len(base.requests) != 2 {
		t.Fatalf("expected the full retry budget to be used, got %d", len(base.requests))
	}
}

func TestRetryOptionsAreClampedToSafeDefaults(t *testing.T) {
	client := NewRetryClient(nil, Options{Retries: -5, BaseDelay: -time.Second, MaxDelay: -time.Second})
	if client.options.Retries != 0 {
		t.Fatalf("a negative retry count must clamp to zero, got %d", client.options.Retries)
	}
	if client.options.BaseDelay <= 0 || client.options.MaxDelay <= 0 {
		t.Fatalf("delays must stay positive: %#v", client.options)
	}
	if client.base == nil || client.options.Sleep == nil {
		t.Fatal("the client must fall back to usable defaults")
	}
}

func TestResponseErrorMapsStatusesToStableKinds(t *testing.T) {
	tests := []struct {
		status int
		kind   string
	}{
		{http.StatusUnauthorized, "auth"},
		{http.StatusForbidden, "authorization"},
		{http.StatusNotFound, "not_found"},
		{http.StatusConflict, "conflict"},
		{http.StatusRequestTimeout, "transient"},
		{http.StatusTooManyRequests, "transient"},
		{http.StatusInternalServerError, "transient"},
		{http.StatusBadGateway, "transient"},
		{http.StatusServiceUnavailable, "transient"},
		{http.StatusGatewayTimeout, "transient"},
		{http.StatusBadRequest, "foundry"},
		{http.StatusNotImplemented, "foundry"},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			err := ResponseError("ARM", "connection upsert", testResponse(tt.status), []byte(`{"error":"x"}`))
			if !errs.IsKind(err, tt.kind) {
				t.Fatalf("status %d produced %v", tt.status, err)
			}
			if !strings.Contains(err.Error(), "connection upsert") {
				t.Fatalf("the operation must be named: %v", err)
			}
		})
	}
}

func TestDiagnosticsIncludeCorrelationHeadersOnlyWhenPresent(t *testing.T) {
	if Diagnostics(nil) != "" {
		t.Fatal("a nil response has no diagnostics")
	}
	if got := Diagnostics(testResponse(http.StatusOK)); got != "" {
		t.Fatalf("expected no diagnostics, got %q", got)
	}
	resp := testResponse(http.StatusBadGateway)
	resp.Header.Set("x-ms-request-id", "request-1")
	resp.Header.Set("x-ms-client-request-id", "client-1")
	resp.Header.Set("x-ms-error-code", "TooBusy")
	got := Diagnostics(resp)
	for _, want := range []string{"x-ms-request-id=request-1", "x-ms-client-request-id=client-1", "x-ms-error-code=TooBusy"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostics %q is missing %q", got, want)
		}
	}
}

func TestClientRequestIDIsAUniqueUUIDAndIsPreservedWhenSupplied(t *testing.T) {
	base := &sequenceClient{responses: []*http.Response{testResponse(http.StatusOK), testResponse(http.StatusOK)}}
	client := NewRetryClient(base, Options{})
	for i := 0; i < 2; i++ {
		req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
		if _, err := client.Do(req); err != nil {
			t.Fatal(err)
		}
	}
	first := base.requests[0].Header.Get("x-ms-client-request-id")
	second := base.requests[1].Header.Get("x-ms-client-request-id")
	if first == "" || second == "" || first == second {
		t.Fatalf("request IDs must be present and unique: %q / %q", first, second)
	}
	if len(first) != 36 || strings.Count(first, "-") != 4 {
		t.Fatalf("request ID %q is not UUID shaped", first)
	}

	supplied := &sequenceClient{responses: []*http.Response{testResponse(http.StatusOK)}}
	suppliedClient := NewRetryClient(supplied, Options{})
	req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
	req.Header.Set("x-ms-client-request-id", "caller-supplied")
	if _, err := suppliedClient.Do(req); err != nil {
		t.Fatal(err)
	}
	if got := supplied.requests[0].Header.Get("x-ms-client-request-id"); got != "caller-supplied" {
		t.Fatalf("a caller-supplied correlation id must be preserved, got %q", got)
	}
}

// trackingBody records whether an abandoned response body was closed.
type trackingBody struct {
	io.Reader
	closed bool
}

func (b *trackingBody) Close() error {
	b.closed = true
	return nil
}
