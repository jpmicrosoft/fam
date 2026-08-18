package agent365arm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"foundry-agent-manager/internal/arm"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/redact"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	defaultRequestTimeout = 120 * time.Second
	maxRequestBytes       = 4 << 10
	maxResponseBytes      = 1 << 20
)

// Client manages the Agent 365 properties of one Foundry account.
type Client struct {
	options     Options
	httpClient  HTTPClient
	resourceURL string
}

// NewClient validates ARM routing and resource identifiers before authentication.
func NewClient(options Options) (*Client, error) {
	validated, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	resourceURL, err := arm.ResourceURLForEndpoint(
		validated.ARMEndpoint,
		validated.APIVersion,
		"subscriptions", validated.SubscriptionID,
		"resourceGroups", validated.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", validated.AccountName,
	)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to build Agent 365 account ARM URL")
	}
	parsed, err := url.Parse(resourceURL)
	if err != nil {
		return nil, errs.SecurityWrap(err, "failed to validate Agent 365 account ARM URL")
	}
	if err := validateARMRequestURL(parsed); err != nil {
		return nil, err
	}

	httpClient := validated.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout:       defaultRequestTimeout,
			CheckRedirect: refuseRedirect,
		}
	} else if standard, ok := httpClient.(*http.Client); ok {
		cloned := *standard
		if cloned.Timeout <= 0 {
			cloned.Timeout = defaultRequestTimeout
		}
		cloned.CheckRedirect = refuseRedirect
		httpClient = &cloned
	}

	return &Client{
		options:     validated,
		httpClient:  httpClient,
		resourceURL: resourceURL,
	}, nil
}

// GetStatus returns the selected account's Agent 365 logging and system status.
func (c *Client) GetStatus(ctx context.Context) (AccountStatus, error) {
	token, err := c.token(ctx)
	if err != nil {
		return AccountStatus{}, err
	}
	return c.getStatus(ctx, token)
}

// Status is a concise alias for GetStatus.
func (c *Client) Status(ctx context.Context) (AccountStatus, error) {
	return c.GetStatus(ctx)
}

// Plan compares the explicit logging flag with the requested state. The
// read-only A365Status does not determine whether a PATCH is required.
func (c *Client) Plan(ctx context.Context, enabled bool) (Plan, error) {
	current, err := c.GetStatus(ctx)
	if err != nil {
		return Plan{}, err
	}
	action := PlanNoChange
	changeRequired := !current.LoggingMatches(enabled)
	if changeRequired {
		if enabled {
			action = PlanEnable
		} else {
			action = PlanDisable
		}
	}
	return Plan{
		Current:          current,
		RequestedEnabled: enabled,
		ChangeRequired:   changeRequired,
		Action:           action,
	}, nil
}

// Enable sets a365LoggingEnabled to true and verifies the resulting flag.
func (c *Client) Enable(ctx context.Context, ifMatch string) (MutationResult, error) {
	return c.SetLogging(ctx, true, ifMatch)
}

// Disable sets a365LoggingEnabled to false and verifies the resulting flag.
func (c *Client) Disable(ctx context.Context, ifMatch string) (MutationResult, error) {
	return c.SetLogging(ctx, false, ifMatch)
}

// Set is a concise alias for SetLogging.
func (c *Client) Set(
	ctx context.Context,
	enabled bool,
	ifMatch string,
) (MutationResult, error) {
	return c.SetLogging(ctx, enabled, ifMatch)
}

// SetLogging sends one non-retried PATCH containing only
// properties.a365LoggingEnabled, then verifies the explicit flag with GET.
func (c *Client) SetLogging(
	ctx context.Context,
	enabled bool,
	ifMatch string,
) (MutationResult, error) {
	result := MutationResult{
		RequestedEnabled: enabled,
		Outcome:          MutationNotStarted,
	}
	validatedIfMatch, err := validateIfMatch(ifMatch)
	if err != nil {
		return result, err
	}
	result.IfMatch = validatedIfMatch

	token, err := c.token(ctx)
	if err != nil {
		return result, err
	}
	body, err := json.Marshal(struct {
		Properties struct {
			A365LoggingEnabled bool `json:"a365LoggingEnabled"`
		} `json:"properties"`
	}{
		Properties: struct {
			A365LoggingEnabled bool `json:"a365LoggingEnabled"`
		}{A365LoggingEnabled: enabled},
	})
	if err != nil {
		return result, errs.FoundryWrap(err, "failed to encode Agent 365 logging PATCH")
	}
	if len(body) > maxRequestBytes {
		return result, errs.Foundry(
			"Agent 365 logging PATCH exceeded %d bytes",
			maxRequestBytes,
		)
	}

	headers := map[string]string{}
	if validatedIfMatch != "" {
		headers["If-Match"] = validatedIfMatch
	}
	resp, _, requestErr := c.do(
		ctx,
		http.MethodPatch,
		token,
		body,
		headers,
	)
	result.Patch = responseMetadata(resp)
	if requestErr != nil {
		if errs.IsKind(requestErr, "security") ||
			errs.IsAuthenticationOrAuthorization(requestErr) {
			result.Outcome = MutationRejected
			return result, requestErr
		}
		result.Outcome = MutationAmbiguous
		return result, errs.AmbiguousMutation(
			classifyTransport("Agent 365 logging PATCH", requestErr, token),
		)
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseErr := responseError("Agent 365 logging PATCH", resp)
		if httpx.IsTransientStatus(resp.StatusCode) {
			result.Outcome = MutationAmbiguous
			return result, errs.AmbiguousMutation(responseErr)
		}
		result.Outcome = MutationRejected
		return result, responseErr
	}

	verified, verifyErr := c.getStatus(ctx, token)
	if verifyErr != nil {
		result.Outcome = MutationAmbiguous
		return result, errs.AmbiguousMutation(
			errs.FoundryWrap(
				verifyErr,
				"Agent 365 logging PATCH was accepted but verification failed",
			),
		)
	}
	result.Verified = &verified
	if !verified.LoggingMatches(enabled) {
		result.Outcome = MutationVerificationFailed
		return result, errs.AmbiguousMutation(
			errs.Conflict(
				"Agent 365 logging PATCH requested a365LoggingEnabled=%t, but verification returned present=%t value=%t; a365Status=%q is read-only licensing and consent state",
				enabled,
				verified.A365LoggingEnabledPresent,
				verified.A365LoggingEnabled,
				verified.A365Status,
			),
		)
	}
	result.Outcome = MutationVerified
	return result, nil
}

func (c *Client) token(ctx context.Context) (string, error) {
	token, err := c.options.Credential.GetToken(
		ctx,
		policy.TokenRequestOptions{Scopes: []string{c.options.ARMScope}},
	)
	if err != nil {
		return "", errs.WithNextSteps(
			errs.AuthWrap(err, "failed to get Agent 365 AzureCloud ARM token"),
			authRemediation()...,
		)
	}
	if strings.TrimSpace(token.Token) == "" {
		return "", errs.WithNextSteps(
			errs.Auth("Agent 365 ARM credential returned an empty token"),
			authRemediation()...,
		)
	}
	return token.Token, nil
}

func (c *Client) getStatus(ctx context.Context, token string) (AccountStatus, error) {
	resp, body, err := c.do(ctx, http.MethodGet, token, nil, nil)
	if err != nil {
		if errs.IsKind(err, "security") {
			return AccountStatus{}, err
		}
		return AccountStatus{}, classifyTransport("Agent 365 account GET", err, token)
	}
	if resp.StatusCode != http.StatusOK {
		return AccountStatus{}, responseError("Agent 365 account GET", resp)
	}
	return parseAccountStatus(body, resp, c.options)
}

func (c *Client) do(
	ctx context.Context,
	method string,
	token string,
	body []byte,
	headers map[string]string,
) (*http.Response, []byte, error) {
	if len(body) > maxRequestBytes {
		return nil, nil, fmt.Errorf("request exceeded %d bytes", maxRequestBytes)
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.resourceURL, reader)
	if err != nil {
		return nil, nil, err
	}
	if err := validateARMRequestURL(req.URL); err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		closeResponse(resp)
		return resp, nil, err
	}
	if resp == nil {
		return nil, nil, fmt.Errorf("ARM HTTP client returned no response")
	}
	if err := validateResponseDestination(req.URL, resp); err != nil {
		closeResponse(resp)
		return resp, nil, err
	}
	if isRedirectStatus(resp.StatusCode) {
		location := safeHeader(resp.Header.Get("Location"), 2048)
		closeResponse(resp)
		return resp, nil, errs.Security(
			"Agent 365 ARM redirect response (%d) was refused (Location=%q)",
			resp.StatusCode,
			location,
		)
	}
	if resp.Body == nil {
		return resp, nil, fmt.Errorf("ARM HTTP client returned a response with a nil body")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp, nil, err
	}
	if len(data) > maxResponseBytes {
		return resp, nil, fmt.Errorf("ARM response exceeded %d bytes", maxResponseBytes)
	}
	return resp, data, nil
}

func parseAccountStatus(
	data []byte,
	resp *http.Response,
	options Options,
) (AccountStatus, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return AccountStatus{}, errs.Foundry("Agent 365 account GET returned an empty response")
	}
	var payload struct {
		ID         string          `json:"id"`
		Name       string          `json:"name"`
		Location   string          `json:"location"`
		ETag       string          `json:"etag"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return AccountStatus{}, errs.FoundryWrap(
			err,
			"failed to parse Agent 365 account GET response",
		)
	}
	if err := validateReturnedAccountID(payload.ID, options); err != nil {
		return AccountStatus{}, err
	}
	if payload.Name == "" ||
		!strings.EqualFold(payload.Name, options.AccountName) ||
		hasControl(payload.Name) {
		return AccountStatus{}, errs.Conflict(
			"Azure returned Foundry account name %q instead of %q",
			payload.Name,
			options.AccountName,
		)
	}
	if strings.TrimSpace(payload.Location) == "" ||
		len(payload.Location) > 128 ||
		hasControl(payload.Location) {
		return AccountStatus{}, errs.Foundry(
			"Azure returned an invalid Foundry account location",
		)
	}

	var properties map[string]json.RawMessage
	if len(payload.Properties) == 0 ||
		bytes.Equal(bytes.TrimSpace(payload.Properties), []byte("null")) {
		return AccountStatus{}, errs.Foundry(
			"Agent 365 account GET response did not contain a properties object",
		)
	}
	if err := json.Unmarshal(payload.Properties, &properties); err != nil ||
		properties == nil {
		return AccountStatus{}, errs.FoundryWrap(
			err,
			"failed to parse Agent 365 account properties",
		)
	}

	status := AccountStatus{
		ID:       payload.ID,
		Name:     payload.Name,
		Location: payload.Location,
		ETag:     payload.ETag,
		Response: responseMetadata(resp),
	}
	if status.ETag == "" {
		status.ETag = status.Response.ETag
	}
	if raw, exists := properties["a365LoggingEnabled"]; exists {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) ||
			json.Unmarshal(raw, &status.A365LoggingEnabled) != nil {
			return AccountStatus{}, errs.Foundry(
				"Agent 365 account property a365LoggingEnabled was not a boolean",
			)
		}
		status.A365LoggingEnabledPresent = true
	}
	if raw, exists := properties["a365Status"]; exists {
		var value string
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) ||
			json.Unmarshal(raw, &value) != nil ||
			len(value) > 128 ||
			hasControl(value) {
			return AccountStatus{}, errs.Foundry(
				"Agent 365 account property a365Status was not a safe string",
			)
		}
		status.A365Status = A365Status(value)
	}
	return status, nil
}

func responseMetadata(resp *http.Response) ResponseMetadata {
	if resp == nil {
		return ResponseMetadata{}
	}
	return ResponseMetadata{
		StatusCode:      resp.StatusCode,
		RequestID:       safeHeader(resp.Header.Get("x-ms-request-id"), 1024),
		ClientRequestID: safeHeader(resp.Header.Get("x-ms-client-request-id"), 1024),
		ErrorCode:       safeHeader(resp.Header.Get("x-ms-error-code"), 1024),
		ETag:            safeHeader(resp.Header.Get("ETag"), 1024),
		RetryAfter:      safeHeader(resp.Header.Get("Retry-After"), 128),
	}
}

func responseError(operation string, resp *http.Response) error {
	metadata := responseMetadata(resp)
	message := fmt.Sprintf(
		"ARM %s failed (%d)%s",
		operation,
		metadata.StatusCode,
		httpx.Diagnostics(resp),
	)
	switch metadata.StatusCode {
	case http.StatusUnauthorized:
		return errs.WithNextSteps(errs.Auth("%s", message), authRemediation()...)
	case http.StatusForbidden:
		return errs.WithNextSteps(
			errs.Authorization("%s", message),
			authorizationRemediation()...,
		)
	case http.StatusNotFound:
		return errs.NotFound("%s", message)
	case http.StatusConflict, http.StatusPreconditionFailed:
		return errs.Conflict("%s", message)
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return errs.Transient("%s", message)
	default:
		return errs.Foundry("%s", message)
	}
}

func authRemediation() []string {
	return []string{
		"Authenticate in the Microsoft Entra tenant that owns the target Azure subscription, then refresh the ARM credential.",
		"Verify the active principal has Owner or Contributor on the Foundry account or an appropriate parent scope.",
		"Request the AzureCloud ARM bearer scope https://management.azure.com/.default and retry after tenant or RBAC changes propagate.",
	}
}

func authorizationRemediation() []string {
	return []string{
		"Assign the active principal the Owner or Contributor role on the Foundry account or an appropriate parent scope.",
		"Verify the active principal is authenticated in, or has access through, the Microsoft Entra tenant that owns the target Azure subscription.",
		"After RBAC propagation, refresh the AzureCloud ARM credential and retry.",
	}
}

func classifyTransport(operation string, err error, token string) error {
	if err == nil {
		return nil
	}
	if errs.IsKind(err, "security") ||
		errs.IsAuthenticationOrAuthorization(err) {
		return err
	}
	return errs.Transient(
		"%s failed: %s",
		operation,
		redact.Text(err.Error(), token),
	)
}

func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return errs.Security(
		"Agent 365 ARM redirect to %q was refused before forwarding the bearer token",
		req.URL.Redacted(),
	)
}

func validateARMRequestURL(value *url.URL) error {
	if value == nil ||
		value.Scheme != "https" ||
		!strings.EqualFold(value.Hostname(), "management.azure.com") ||
		value.Port() != "" ||
		value.User != nil {
		return errs.Security(
			"refusing to send an Agent 365 ARM bearer token to invalid URL %q",
			value,
		)
	}
	return nil
}

func validateResponseDestination(original *url.URL, resp *http.Response) error {
	if resp.Request == nil || resp.Request.URL == nil {
		return nil
	}
	if err := validateARMRequestURL(resp.Request.URL); err != nil {
		return errs.SecurityWrap(err, "Agent 365 ARM response followed an unsafe redirect")
	}
	if original != nil && resp.Request.URL.String() != original.String() {
		return errs.Security(
			"Agent 365 ARM response followed an unexpected redirect from %q to %q",
			original.Redacted(),
			resp.Request.URL.Redacted(),
		)
	}
	return nil
}

func isRedirectStatus(status int) bool {
	switch status {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func safeHeader(value string, maxRunes int) string {
	value = strings.Map(func(char rune) rune {
		if unicode.IsControl(char) {
			return ' '
		}
		return char
	}, value)
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if maxRunes > 0 && len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

func closeResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}
