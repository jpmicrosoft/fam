package update

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

func githubClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:                  http.ProxyFromEnvironment,
			DialContext:            (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    15 * time.Second,
			ResponseHeaderTimeout:  30 * time.Second,
			MaxResponseHeaderBytes: 64 << 10,
			IdleConnTimeout:        30 * time.Second,
			MaxIdleConns:           4,
			DisableCompression:     true,
		},
	}
}

func allowedURL(destination *url.URL, initial bool) bool {
	if destination.Scheme != "https" || destination.User != nil ||
		destination.Opaque != "" || destination.Fragment != "" ||
		(destination.Port() != "" && destination.Port() != "443") {
		return false
	}
	host := destination.Hostname()
	if destination.Host != host && destination.Host != host+":443" {
		return false
	}
	if initial {
		return host == "api.github.com"
	}
	// The releases asset API returns this GitHub-owned object storage CDN.
	// Do not widen this to githubusercontent.com suffixes or arbitrary S3.
	return host == "release-assets.githubusercontent.com"
}

func (u *Updater) get(ctx context.Context, endpoint string, metadata bool, limit int64) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, u.options.Timeout)
		data, retry, delay, err := u.getAttempt(attemptCtx, endpoint, metadata, limit)
		if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			retry = true
		}
		cancel()
		if err == nil || !retry || attempt >= u.options.Retries {
			return data, err
		}
		if delay == 0 {
			delay = u.options.RetryDelay
			for i := 0; i < attempt && delay < time.Minute; i++ {
				delay *= 2
			}
		}
		if delay > time.Minute {
			delay = time.Minute
		}
		if err := wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (u *Updater) getAttempt(ctx context.Context, endpoint string, metadata bool, limit int64) ([]byte, bool, time.Duration, error) {
	destination, err := url.Parse(endpoint)
	if err != nil || !allowedURL(destination, true) || destination.RawQuery != "" {
		return nil, false, 0, errs.Security("update request destination is not the fixed GitHub API")
	}
	for hop := 0; ; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, false, 0, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
		if err != nil {
			return nil, false, 0, errs.Security("cannot construct a safe GitHub update request")
		}
		request.Header.Set("User-Agent", "fam-self-update")
		request.Header.Set("Accept", "application/octet-stream")
		if metadata {
			request.Header.Set("Accept", "application/vnd.github+json")
		}
		if hop == 0 {
			request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
			if u.options.Token != "" {
				request.Header.Set("Authorization", "Bearer "+u.options.Token)
			}
		}
		// http.Client parses Location before CheckRedirect. Use its dedicated
		// transport directly so malformed redirects also reach our validator,
		// without client-managed cookies, referers, or response-body draining.
		response, err := u.client.Transport.RoundTrip(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, 0, ctx.Err()
			}
			var certificateError *tls.CertificateVerificationError
			if errors.As(err, &certificateError) {
				return nil, false, 0, errs.Security("GitHub TLS certificate verification failed")
			}
			// Transport errors can contain both signed URLs and attacker text.
			// Do not wrap url.Error (or its cause) into operator-visible errors.
			return nil, true, 0, githubError(0)
		}
		if response.StatusCode >= 300 && response.StatusCode <= 399 {
			location := response.Header.Get("Location")
			closeErr := response.Body.Close()
			if metadata || hop >= 3 {
				return nil, false, 0, errs.Security("GitHub update redirect is not permitted")
			}
			next, parseErr := url.Parse(location)
			if parseErr != nil || location == "" {
				return nil, false, 0, errs.Security("GitHub asset redirect is malformed")
			}
			next = destination.ResolveReference(next)
			if !allowedURL(next, false) {
				return nil, false, 0, errs.Security("GitHub asset redirect destination is not permitted")
			}
			if closeErr != nil {
				return nil, true, 0, githubError(0)
			}
			destination = next
			continue
		}
		if response.StatusCode != http.StatusOK {
			status := response.StatusCode
			delay := retryAfter(response.Header.Get("Retry-After"))
			// Close rather than draining arbitrary error or rate-limit bodies.
			_ = response.Body.Close()
			return nil, httpx.IsTransientStatus(status), delay, githubError(status)
		}
		data, readErr := boundedRead(ctx, response.Body, limit)
		closeErr := response.Body.Close()
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, false, 0, ctx.Err()
			}
			if errs.IsKind(readErr, "security") {
				return nil, false, 0, readErr
			}
			return nil, true, 0, githubError(0)
		}
		if closeErr != nil {
			return nil, true, 0, githubError(0)
		}
		return data, false, 0, nil
	}
}

func githubError(status int) error {
	steps := []string{
		"Check access to the jpmicrosoft/fam GitHub releases and retry.",
		"For a private release, provide a GitHub token with repository contents read access via FAM_INSTALL_TOKEN, GITHUB_TOKEN, or GH_TOKEN.",
	}
	if status == 0 {
		return errs.WithNextSteps(errs.Transient("GitHub update request failed"), steps...)
	}
	if status == 401 || status == 403 {
		return errs.WithNextSteps(errs.Authorization("GitHub update request was denied (HTTP %d)", status), steps...)
	}
	if status == 404 {
		return errs.WithNextSteps(errs.NotFound("GitHub release or asset was not found (HTTP 404)"), steps...)
	}
	return errs.WithNextSteps(errs.Transient("GitHub update request failed (HTTP %d)", status), steps...)
}

func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds >= 60 {
			return time.Minute
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay > time.Minute {
			return time.Minute
		}
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func wait(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.ReadAt(p, offset)
}

func boundedRead(ctx context.Context, reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx, reader}, limit+1))
	if int64(len(data)) > limit {
		return nil, errs.Security("update data exceeds its size limit")
	}
	return data, err
}

func archiveError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return errs.Security("release archive is malformed, truncated, or exceeds its expansion limits")
}
