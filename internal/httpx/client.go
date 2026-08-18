// Package httpx provides bounded, context-aware HTTP retries and diagnostics.
package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	errs "foundry-agent-manager/internal/errors"
)

// Doer is implemented by http.Client and test clients.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	Retries   int
	BaseDelay time.Duration
	MaxDelay  time.Duration
	Sleep     func(context.Context, time.Duration) error
	Trace     func(TraceEvent)
}

// TraceEvent contains safe request metadata for debug output. It intentionally
// excludes query strings, headers, bodies, response bodies, and credentials.
type TraceEvent struct {
	Method      string
	Host        string
	Path        string
	Attempt     int
	MaxAttempts int
	StatusCode  int
	Duration    time.Duration
	RetryDelay  time.Duration
	Failed      bool
}

// RetryClient adds request IDs and safe bounded retries to another HTTP client.
type RetryClient struct {
	base    Doer
	options Options
}

func NewRetryClient(base Doer, options Options) *RetryClient {
	if base == nil {
		base = http.DefaultClient
	}
	if options.Retries < 0 {
		options.Retries = 0
	}
	if options.BaseDelay <= 0 {
		options.BaseDelay = time.Second
	}
	if options.MaxDelay <= 0 {
		options.MaxDelay = 30 * time.Second
	}
	if options.Sleep == nil {
		options.Sleep = sleepContext
	}
	return &RetryClient{base: base, options: options}
}

func (c *RetryClient) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("x-ms-client-request-id") == "" {
		req.Header.Set("x-ms-client-request-id", requestID())
	}

	attempts := c.options.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		current, err := cloneRequest(req, attempt)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		resp, err := c.base.Do(current)
		duration := time.Since(started)
		retry := shouldRetry(current, resp, err) && attempt < attempts-1
		delay := time.Duration(0)
		if retry {
			delay = retryDelay(resp, c.options.BaseDelay, c.options.MaxDelay, attempt)
		}
		if c.options.Trace != nil {
			statusCode := 0
			if resp != nil {
				statusCode = resp.StatusCode
			}
			c.options.Trace(TraceEvent{
				Method:      current.Method,
				Host:        current.URL.Host,
				Path:        current.URL.EscapedPath(),
				Attempt:     attempt + 1,
				MaxAttempts: attempts,
				StatusCode:  statusCode,
				Duration:    duration,
				RetryDelay:  delay,
				Failed:      err != nil,
			})
		}
		if !retry {
			return resp, err
		}
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		if sleepErr := c.options.Sleep(req.Context(), delay); sleepErr != nil {
			return nil, sleepErr
		}
	}
	return nil, errs.Transient("HTTP request exhausted its retry policy")
}

func cloneRequest(req *http.Request, attempt int) (*http.Request, error) {
	cloned := req.Clone(req.Context())
	if attempt == 0 || req.Body == nil {
		return cloned, nil
	}
	if req.GetBody == nil {
		return nil, fmt.Errorf("request body cannot be replayed")
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, fmt.Errorf("failed to replay request body: %w", err)
	}
	cloned.Body = body
	return cloned, nil
}

func shouldRetry(req *http.Request, resp *http.Response, err error) bool {
	if !methodRetryable(req.Method) {
		return false
	}
	if err != nil {
		return req.Context().Err() == nil
	}
	return IsTransientStatus(resp.StatusCode)
}

// IsTransientStatus reports statuses for which a mutation outcome can be indeterminate.
func IsTransientStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func methodRetryable(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	default:
		// Foundry marks agent POST operations as non-repeatable.
		return false
	}
}

func retryDelay(resp *http.Response, base, max time.Duration, attempt int) time.Duration {
	if resp != nil {
		if parsed, ok := parseRetryAfter(resp.Header.Get("Retry-After")); ok {
			if parsed > max {
				return max
			}
			return parsed
		}
	}
	delay := base
	for i := 0; i < attempt; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := time.Until(when)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func requestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("foundry-agent-manager-%d", time.Now().UnixNano())
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

// Diagnostics returns correlation fields useful in support requests.
func Diagnostics(resp *http.Response) string {
	if resp == nil {
		return ""
	}
	fields := make([]string, 0, 3)
	for _, header := range []string{"x-ms-request-id", "x-ms-client-request-id", "x-ms-error-code"} {
		if value := resp.Header.Get(header); value != "" {
			fields = append(fields, header+"="+value)
		}
	}
	if len(fields) == 0 {
		return ""
	}
	return " [" + strings.Join(fields, ", ") + "]"
}

// ResponseError creates a stable typed error from an unsuccessful HTTP response.
func ResponseError(service, operation string, resp *http.Response, body []byte) error {
	message := fmt.Sprintf("%s %s failed (%d): %s%s",
		service,
		operation,
		resp.StatusCode,
		strings.TrimSpace(string(body)),
		Diagnostics(resp),
	)
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return errs.Auth("%s", message)
	case http.StatusForbidden:
		action, scope := authorizationDetails(body)
		return errs.AuthorizationDenied(action, scope, "%s", message)
	case http.StatusNotFound:
		return errs.NotFound("%s", message)
	case http.StatusConflict:
		return errs.Conflict("%s", message)
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return errs.Transient("%s", message)
	default:
		return errs.Foundry("%s", message)
	}
}

var (
	authorizationActionPattern = regexp.MustCompile(
		`(?i)\baction\s+['"]([^'"\r\n]+)['"]`,
	)
	authorizationScopePattern = regexp.MustCompile(
		`(?i)\b(?:over|at|on)\s+(?:the\s+)?scope\s+['"]([^'"\r\n]+)['"]`,
	)
	authorizationGenericScopePattern = regexp.MustCompile(
		`(?i)\bscope\s+['"]([^'"\r\n]+)['"]`,
	)
)

func authorizationDetails(body []byte) (string, string) {
	var document any
	if err := json.Unmarshal(body, &document); err != nil {
		return authorizationDetailsFromText(string(body))
	}
	action, scope, messages := authorizationDocumentDetails(document)
	if action != "" && scope != "" {
		return action, scope
	}
	messageAction, messageScope := authorizationDetailsFromText(
		strings.Join(messages, "\n"),
	)
	if action == "" {
		action = messageAction
	}
	if scope == "" {
		scope = messageScope
	}
	return action, scope
}

func authorizationDocumentDetails(value any) (string, string, []string) {
	var action string
	var scope string
	var messages []string
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "action":
					if action == "" {
						if text, ok := child.(string); ok {
							action = sanitizeAuthorizationDetail(text, 512)
						}
					}
				case "scope":
					if scope == "" {
						if text, ok := child.(string); ok {
							scope = sanitizeAuthorizationDetail(text, 2048)
						}
					}
				case "message":
					if text, ok := child.(string); ok {
						messages = append(
							messages,
							sanitizeAuthorizationDetail(text, 4096),
						)
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return action, scope, messages
}

func authorizationDetailsFromText(text string) (string, string) {
	text = sanitizeAuthorizationDetail(text, 8192)
	action := firstAuthorizationMatch(authorizationActionPattern, text, 512)
	scope := firstAuthorizationMatch(authorizationScopePattern, text, 2048)
	if scope == "" {
		scope = firstAuthorizationMatch(
			authorizationGenericScopePattern,
			text,
			2048,
		)
	}
	return action, scope
}

func firstAuthorizationMatch(
	pattern *regexp.Regexp,
	text string,
	maxRunes int,
) string {
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return ""
	}
	return sanitizeAuthorizationDetail(match[1], maxRunes)
}

func sanitizeAuthorizationDetail(value string, maxRunes int) string {
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(strings.TrimSpace(value))
	if maxRunes > 0 && len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
