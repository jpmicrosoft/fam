package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

func TestAssetRedirectsStripAllCredentialsUsingOfflineServer(t *testing.T) {
	const secret = "synthetic-github-token"
	var hosts []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hosts = append(hosts, r.Host)
		mu.Unlock()
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Error("cookie or referer reached an update request")
		}
		if r.Host == "api.github.com" {
			if r.Header.Get("Authorization") != "Bearer "+secret {
				t.Error("API did not receive the configured token")
			}
			w.Header().Set("Set-Cookie", "sensitive=fixture")
			w.Header().Set("Location", "https://release-assets.githubusercontent.com/first?signature=synthetic")
			w.WriteHeader(http.StatusFound)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("token reached the CDN")
		}
		if r.URL.Path == "/first" {
			w.Header().Set("Location", "/second?signature=synthetic")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		if _, err := w.Write([]byte("offline payload")); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	local, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.options.Token = secret
	localTransport := &http.Transport{}
	defer localTransport.CloseIdleConnections()
	u.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		// Only this injected test transport routes the pinned synthetic hosts to
		// loopback. The production updater has no endpoint override.
		clone := request.Clone(request.Context())
		destination := *request.URL
		destination.Scheme, destination.Host = local.Scheme, local.Host
		clone.URL, clone.Host = &destination, request.URL.Host
		return localTransport.RoundTrip(clone)
	})
	got, err := u.get(context.Background(), assetEndpoint(11), false, 128)
	if err != nil || string(got) != "offline payload" {
		t.Fatalf("redirected request failed: %q, %v", got, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hosts) != 3 || hosts[0] != "api.github.com" ||
		hosts[1] != "release-assets.githubusercontent.com" || hosts[2] != hosts[1] {
		t.Fatalf("unexpected redirect sequence: %v", hosts)
	}
}

func TestRedirectPolicyRejectsMetadataAndUnsafeAssetHosts(t *testing.T) {
	for _, destination := range []string{
		"https://api.github.com/repos/jpmicrosoft/fam/releases/assets/22",
		"http://release-assets.githubusercontent.com/file",
		"https://release-assets.githubusercontent.com.evil.example/file",
		"https://evil.example/file?signature=synthetic-secret",
		"https://user:synthetic-secret@release-assets.githubusercontent.com/file",
		"https://release-assets.githubusercontent.com:444/file",
		"https://release-assets.githubusercontent.com:/file",
		"https://release-assets.githubusercontent.com./file",
		"https://github.com/jpmicrosoft/fam/releases/download/v1.1.0/fam.zip",
		"https://objects.githubusercontent.com/file",
		"https://release-assets.githubusercontent.com/file#fragment",
		"file:///tmp/payload", "//evil.example/file", "://invalid",
	} {
		t.Run(destination, func(t *testing.T) {
			u := testUpdater(t, "1.0.0", "", runtime.GOOS)
			calls := 0
			u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				res := response(http.StatusFound, []byte("synthetic-secret"))
				res.Header.Set("Location", destination)
				return res, nil
			})
			_, err := u.get(context.Background(), assetEndpoint(11), false, 128)
			if !errs.IsKind(err, "security") || calls != 1 || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("unsafe redirect not rejected safely: calls=%d err=%v", calls, err)
			}
		})
	}
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		res := response(302, nil)
		res.Header.Set("Location", "https://release-assets.githubusercontent.com/file")
		return res, nil
	})
	if _, err := u.get(context.Background(), releaseEndpoint+"latest", true, 128); !errs.IsKind(err, "security") {
		t.Fatalf("metadata redirect accepted: %v", err)
	}
}

func TestRedirectHopBudgetAndInitialHostPinning(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	calls := 0
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		res := response(302, nil)
		res.Header.Set("Location", "https://release-assets.githubusercontent.com/loop")
		return res, nil
	})
	if _, err := u.get(context.Background(), assetEndpoint(11), false, 128); !errs.IsKind(err, "security") || calls != 4 {
		t.Fatalf("redirect bound not enforced: calls=%d err=%v", calls, err)
	}
	for _, endpoint := range []string{
		"https://evil.example/file", "https://api.github.com:444/file",
		"https://token@api.github.com/file", releaseEndpoint + "latest?secret=fixture",
	} {
		if _, err := u.get(context.Background(), endpoint, true, 128); !errs.IsKind(err, "security") {
			t.Fatalf("unsafe initial endpoint accepted: %v", err)
		}
	}
	if calls != 4 {
		t.Fatal("unsafe initial endpoint reached the transport")
	}
}

type countingBody struct {
	reads  int
	bytes  int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	b.reads++
	for i := range p {
		p[i] = 'x'
	}
	b.bytes += len(p)
	return len(p), nil
}

func (b *countingBody) Close() error {
	b.closed = true
	return nil
}

func TestRetriesCloseRatherThanDrainUnboundedErrorBodies(t *testing.T) {
	for _, status := range []int{408, 429, 500, 502, 503, 504} {
		u := testUpdater(t, "1.0.0", "", runtime.GOOS)
		u.options.Retries, u.options.RetryDelay = 2, time.Nanosecond
		var bodies []*countingBody
		u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			body := &countingBody{}
			bodies = append(bodies, body)
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: body}, nil
		})
		if _, err := u.get(context.Background(), assetEndpoint(11), false, 8); err == nil {
			t.Fatal("error status treated as success")
		}
		if len(bodies) != 3 {
			t.Fatalf("retry settings ignored: %d", len(bodies))
		}
		for _, body := range bodies {
			if body.reads != 0 || !body.closed {
				t.Fatal("error body was drained or leaked")
			}
		}
	}
}

func TestContentLengthIsAdvisoryAndReadsAreLimitPlusOne(t *testing.T) {
	for _, declared := range []int64{-1, 0, 1, 1000000} {
		u := testUpdater(t, "1.0.0", "", runtime.GOOS)
		body := &countingBody{}
		u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: 200, Body: body, Header: http.Header{}, ContentLength: declared,
			}, nil
		})
		if _, err := u.get(context.Background(), assetEndpoint(11), false, 16); !errs.IsKind(err, "security") {
			t.Fatalf("body limit ignored: %v", err)
		}
		if body.bytes != 17 || !body.closed {
			t.Fatalf("body read was not limit+1: %d, closed=%v", body.bytes, body.closed)
		}
	}
}

func TestGitHubErrorsNeverLeakURLsBodiesOrAzureRemediation(t *testing.T) {
	for _, status := range []int{401, 403, 404, 500} {
		u := testUpdater(t, "1.0.0", "", runtime.GOOS)
		u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			res := response(status, []byte("arbitrary-synthetic-secret-body"))
			res.Header.Set("Location", "https://evil.example/?token=synthetic")
			return res, nil
		})
		_, err := u.get(context.Background(), releaseEndpoint+"latest", true, 128)
		if err == nil {
			t.Fatal("HTTP error accepted")
		}
		rendered := err.Error() + strings.Join(errs.Remediation(err), " ")
		if strings.Contains(rendered, "synthetic") || strings.Contains(rendered, "arbitrary") ||
			strings.Contains(strings.ToLower(rendered), "azure") {
			t.Fatalf("unsafe error or remediation: %s", rendered)
		}
	}
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &url.Error{
			Op: "Get", URL: "https://release-assets.githubusercontent.com/file?signature=synthetic-secret",
			Err: errors.New("arbitrary-secret-cause"),
		}
	})
	_, err := u.get(context.Background(), assetEndpoint(11), false, 128)
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "?") {
		t.Fatalf("transport error was not redacted: %v", err)
	}
}

func TestTransportRetriesTruncationAndHonorsCancellation(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.options.Retries, u.options.RetryDelay = 1, time.Nanosecond
	calls := 0
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			res := response(200, nil)
			res.Body = io.NopCloser(failedReader{})
			return res, nil
		}
		return response(200, []byte("retried")), nil
	})
	if got, err := u.get(context.Background(), assetEndpoint(11), false, 128); err != nil || string(got) != "retried" || calls != 2 {
		t.Fatalf("retry did not succeed: %q %v calls=%d", got, err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	u.options.RetryDelay = time.Minute
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return response(503, nil), nil
	})
	if _, err := u.get(ctx, assetEndpoint(11), false, 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("retry ignored cancellation: %v", err)
	}
	if got, err := boundedRead(context.Background(), bytes.NewBufferString("small"), 5); err != nil || string(got) != "small" {
		t.Fatalf("exact size boundary failed: %q %v", got, err)
	}
}

type failedReader struct{}

func (failedReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestRequestTimeoutGetsAFreshBudgetOnRetry(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.options.Timeout = 50 * time.Millisecond
	u.options.Retries, u.options.RetryDelay = 1, time.Nanosecond
	calls := 0
	u.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 {
			t.Fatal("retry inherited the expired request deadline")
		}
		return response(200, []byte("retried")), nil
	})
	data, err := u.get(context.Background(), assetEndpoint(11), false, 128)
	if err != nil || string(data) != "retried" || calls != 2 {
		t.Fatalf("request timeout exhausted later attempts: data=%q calls=%d err=%v", data, calls, err)
	}
}

func TestRedirectResponsesAreClosedWithoutDraining(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	body := &countingBody{}
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 302, Body: body,
			Header: http.Header{"Location": {"https://release-assets.githubusercontent.com/file"}},
		}, nil
	})
	if _, err := u.get(context.Background(), releaseEndpoint+"latest", true, 8); !errs.IsKind(err, "security") {
		t.Fatalf("metadata redirect accepted: %v", err)
	}
	if body.reads != 0 || !body.closed {
		t.Fatal("redirect body was drained or leaked")
	}
}

func TestCheckTimeoutBoundsTheEntireGitHubRequest(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	u.options.Timeout = 10 * time.Millisecond
	u.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})
	if _, err := u.Check(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request did not honor updater timeout: %v", err)
	}
}

func TestRetryAfterAndRetryCountHaveHardBounds(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{"0", 0}, {"2", 2 * time.Second}, {"60", time.Minute},
		{"18446744073709551615", time.Minute}, {"invalid", 0}, {"-1", 0},
	} {
		if got := retryAfter(test.value); got != test.want {
			t.Fatalf("Retry-After %q = %v, want %v", test.value, got, test.want)
		}
	}
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	calls := 0
	u.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, io.ErrUnexpectedEOF
	})
	if _, err := u.get(context.Background(), assetEndpoint(11), false, 8); err == nil || calls != 1 {
		t.Fatalf("zero retries did not mean one attempt: calls=%d err=%v", calls, err)
	}
}

func TestMetadataAndDownloadBudgetsAreApplied(t *testing.T) {
	for _, field := range []string{"metadata", "checksum", "archive"} {
		u := testUpdater(t, "1.0.0", "", runtime.GOOS)
		attachFixture(t, u, "1.1.0", []byte("fixture"))
		if field == "metadata" {
			u.limits.metadata = 8
			if _, err := u.Check(context.Background()); !errs.IsKind(err, "security") {
				t.Fatalf("metadata budget ignored: %v", err)
			}
			continue
		}
		plan, err := u.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if field == "checksum" {
			u.limits.checksum = 8
		} else {
			u.limits.archive = 8
		}
		if _, err := u.Apply(context.Background(), plan); !errs.IsKind(err, "security") {
			t.Fatalf("%s budget ignored: %v", field, err)
		}
	}
}
