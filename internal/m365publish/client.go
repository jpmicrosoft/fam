// Package m365publish publishes modern Foundry agents to Microsoft 365 and Teams.
package m365publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"foundry-agent-manager/internal/botservice"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/netcheck"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	APIVersion       = "v1"
	AzureCloudScope  = "https://ai.azure.com/.default"
	maxResponseBytes = 4 << 20

	// Catalog metadata bounds. These are deliberately generous supersets of the
	// Microsoft 365 catalog limits: they exist to fail closed locally, before an
	// irreversible publish request is sent, not to mirror the service exactly.
	maxDisplayNameLength      = 128
	maxShortDescriptionLength = 1000
	maxFullDescriptionLength  = 4000
	maxDeveloperNameLength    = 256
)

var (
	agentNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	appVersionPattern = regexp.MustCompile(
		`^[1-9][0-9]{0,4}\.(?:0|[1-9][0-9]{0,4})\.(?:0|[1-9][0-9]{0,4})$`,
	)
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Options struct {
	ProjectEndpoint string
	Scope           string
	Credential      azcore.TokenCredential
	HTTPClient      HTTPClient
}

type Request struct {
	AgentName                string
	AgentDisplayName         string
	BotServiceARMID          string
	PublishScope             string
	AppVersion               string
	ShortDescription         string
	FullDescription          string
	DeveloperName            string
	DeveloperWebsiteURL      string
	PrivacyURL               string
	TermsOfUseURL            string
	CanRespondWithoutMention bool
	ColorIconBase64          string
	OutlineIconBase64        string
}

type Result struct {
	TitleID               string `json:"titleId"`
	PublishScope          string `json:"publishScope"`
	AdminApprovalRequired bool   `json:"adminApprovalRequired"`
}

type Client struct {
	endpoint   string
	scope      string
	credential azcore.TokenCredential
	http       HTTPClient
}

func NewClient(options Options) (*Client, error) {
	endpoint, err := validateProjectEndpoint(options.ProjectEndpoint)
	if err != nil {
		return nil, err
	}
	if options.Scope != AzureCloudScope {
		return nil, errs.Config(
			"Microsoft 365 publishing supports AzureCloud Foundry scope %q only; got %q",
			AzureCloudScope,
			options.Scope,
		)
	}
	if options.Credential == nil {
		return nil, errs.Config("Microsoft 365 publishing credential must not be nil")
	}
	if options.HTTPClient == nil {
		return nil, errs.Config("Microsoft 365 publishing HTTP client must not be nil")
	}
	httpClient := options.HTTPClient
	if standardClient, ok := httpClient.(*http.Client); ok {
		// Do not mutate a caller-owned client. Publishing carries a bearer token,
		// so redirects must be refused before net/http can forward it.
		cloned := *standardClient
		cloned.CheckRedirect = refuseRedirect
		httpClient = &cloned
	}
	return &Client{
		endpoint:   endpoint,
		scope:      options.Scope,
		credential: options.Credential,
		http:       httpClient,
	}, nil
}

func (c *Client) PublishContext(ctx context.Context, request Request) (Result, error) {
	if err := validateRequest(request); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(publishBody{
		AgentDisplayName:         request.AgentDisplayName,
		BotServiceARMID:          request.BotServiceARMID,
		PublishScope:             request.PublishScope,
		PublishAsAutopilot:       false,
		AppVersion:               request.AppVersion,
		ShortDescription:         request.ShortDescription,
		FullDescription:          request.FullDescription,
		DeveloperName:            request.DeveloperName,
		DeveloperWebsiteURL:      request.DeveloperWebsiteURL,
		PrivacyURL:               request.PrivacyURL,
		TermsOfUseURL:            request.TermsOfUseURL,
		CanRespondWithoutMention: request.CanRespondWithoutMention,
		ColorIconBase64:          request.ColorIconBase64,
		OutlineIconBase64:        request.OutlineIconBase64,
	})
	if err != nil {
		return Result{}, errs.FoundryWrap(err, "failed to encode Microsoft 365 publish request")
	}
	token, err := c.credential.GetToken(
		ctx,
		policy.TokenRequestOptions{Scopes: []string{c.scope}},
	)
	if err != nil {
		return Result{}, errs.AuthWrap(err, "failed to get Foundry token for Microsoft 365 publishing")
	}
	if token.Token == "" {
		return Result{}, errs.Auth("Foundry credential returned an empty token for Microsoft 365 publishing")
	}

	requestURL, err := publishURL(c.endpoint, request.AgentName)
	if err != nil {
		return Result{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return Result{}, errs.FoundryWrap(err, "failed to create Microsoft 365 publish request")
	}
	httpRequest.Header.Set("Authorization", "Bearer "+token.Token)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpRequest)
	if err != nil {
		return Result{}, ambiguous(errs.FoundryWrap(
			err,
			"Microsoft 365 publish request failed; do not retry automatically",
		))
	}
	if resp == nil || resp.Body == nil {
		return Result{}, ambiguous(errs.Foundry(
			"Microsoft 365 publish returned an invalid response; do not retry automatically",
		))
	}
	defer resp.Body.Close()
	if err := validateResponseDestination(httpRequest.URL, resp); err != nil {
		return Result{}, ambiguous(err)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, ambiguous(errs.FoundryWrap(
			err,
			"failed to read Microsoft 365 publish response; do not retry automatically",
		))
	}
	if len(data) > maxResponseBytes {
		return Result{}, ambiguous(errs.Foundry(
			"Microsoft 365 publish response exceeded %d bytes; do not retry automatically",
			maxResponseBytes,
		))
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseErr := httpx.ResponseError("Foundry", "Microsoft 365 publish", resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return Result{}, ambiguous(responseErr)
		}
		return Result{}, responseErr
	}

	var payload struct {
		TitleID string `json:"titleId"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return Result{}, ambiguous(errs.FoundryWrap(
			err,
			"failed to parse successful Microsoft 365 publish response; reconcile before retrying",
		))
	}
	payload.TitleID = strings.TrimSpace(payload.TitleID)
	if payload.TitleID == "" {
		return Result{}, ambiguous(errs.Foundry(
			"successful Microsoft 365 publish response omitted titleId; reconcile before retrying",
		))
	}
	return Result{
		TitleID:               payload.TitleID,
		PublishScope:          request.PublishScope,
		AdminApprovalRequired: request.PublishScope == "Tenant",
	}, nil
}

type publishBody struct {
	AgentDisplayName         string `json:"agentDisplayName,omitempty"`
	BotServiceARMID          string `json:"botServiceArmId"`
	PublishScope             string `json:"publishScope"`
	PublishAsAutopilot       bool   `json:"publishAsAutopilot"`
	AppVersion               string `json:"appVersion"`
	ShortDescription         string `json:"shortDescription"`
	FullDescription          string `json:"fullDescription"`
	DeveloperName            string `json:"developerName"`
	DeveloperWebsiteURL      string `json:"developerWebsiteUrl"`
	PrivacyURL               string `json:"privacyUrl"`
	TermsOfUseURL            string `json:"termsOfUseUrl"`
	CanRespondWithoutMention bool   `json:"canRespondWithoutMention,omitempty"`
	ColorIconBase64          string `json:"colorIconBase64,omitempty"`
	OutlineIconBase64        string `json:"outlineIconBase64,omitempty"`
}

func validateProjectEndpoint(value string) (string, error) {
	validated, err := netcheck.ValidateFoundryEndpointForSuffixes(
		value,
		"project.endpoint",
		[]string{"services.ai.azure.com"},
	)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(validated)
	if err != nil {
		return "", errs.Security("project.endpoint: invalid URL %q: %v", value, err)
	}
	if parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errs.Security(
			"project.endpoint must not contain a port, query, or fragment for Microsoft 365 publishing",
		)
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 3 || segments[0] != "api" || segments[1] != "projects" || segments[2] == "" {
		return "", errs.Config(
			"project.endpoint must have the form https://<account>.services.ai.azure.com/api/projects/<project>",
		)
	}
	project, err := url.PathUnescape(segments[2])
	if err != nil || project == "" || strings.ContainsAny(project, "/\\?#") {
		return "", errs.Config("project.endpoint contains an invalid project path segment")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String(), nil
}

func validateRequest(request Request) error {
	if !agentNamePattern.MatchString(request.AgentName) {
		return errs.Config(
			"agent name %q must be 1-64 ASCII letters, digits, periods, underscores, or hyphens and start with a letter or digit",
			request.AgentName,
		)
	}
	if _, err := botservice.ParseBotServiceARMID(request.BotServiceARMID); err != nil {
		return err
	}
	if request.PublishScope != "Shared" && request.PublishScope != "Tenant" {
		return errs.Config("publish scope must be Shared or Tenant")
	}
	if !appVersionPattern.MatchString(request.AppVersion) {
		return errs.Config("app version %q must be a three-part version such as 1.0.0", request.AppVersion)
	}
	// Catalog metadata is forwarded verbatim to Microsoft 365 by a POST that is
	// never retried, so every string is bounded and screened for control
	// characters here, before the token is requested and the request is sent.
	if err := validateMetadataText(
		"agent display name",
		request.AgentDisplayName,
		maxDisplayNameLength,
		false,
		true,
	); err != nil {
		return err
	}
	for _, field := range []struct {
		label      string
		value      string
		maxLength  int
		multiline  bool
		isOptional bool
	}{
		{"short description", request.ShortDescription, maxShortDescriptionLength, false, false},
		{"full description", request.FullDescription, maxFullDescriptionLength, true, false},
		{"developer name", request.DeveloperName, maxDeveloperNameLength, false, false},
	} {
		if err := validateMetadataText(
			field.label,
			field.value,
			field.maxLength,
			field.multiline,
			field.isOptional,
		); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"developer website URL": request.DeveloperWebsiteURL,
		"privacy URL":           request.PrivacyURL,
		"terms of use URL":      request.TermsOfUseURL,
	} {
		if err := validateMetadataURL(field, value); err != nil {
			return err
		}
	}
	return nil
}

// validateMetadataText bounds one catalog metadata string and rejects control
// characters. Multi-line fields keep newlines and tabs; every other C0/C1
// control character (terminal escapes, NUL) and every explicit bidirectional
// override/isolate is refused, so hostile metadata cannot be published to the
// Microsoft 365 catalog, spoof a publisher name, or be replayed into receipts
// and operator terminals.
func validateMetadataText(field, value string, maxLength int, multiline, optional bool) error {
	if strings.TrimSpace(value) == "" {
		if optional && value == "" {
			return nil
		}
		return errs.Config("%s must not be empty", field)
	}
	if !utf8.ValidString(value) {
		return errs.Config("%s must be valid UTF-8 text", field)
	}
	if count := utf8.RuneCountInString(value); count > maxLength {
		return errs.Config("%s must be at most %d characters, got %d", field, maxLength, count)
	}
	for _, r := range value {
		if isBidiControl(r) {
			return errs.Config("%s must not contain bidirectional override characters", field)
		}
		if !unicode.IsControl(r) {
			continue
		}
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		return errs.Config("%s must not contain control characters", field)
	}
	return nil
}

// isBidiControl reports the explicit Unicode bidirectional embedding, override,
// and isolate controls. They have no legitimate use in catalog metadata and are
// the primary Trojan-Source style display-spoofing vector.
func isBidiControl(r rune) bool {
	switch r {
	case '\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}

func validateMetadataURL(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return errs.Config("%s is invalid: %v", field, err)
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return errs.Config("%s must be an HTTPS URL without embedded credentials", field)
	}
	return nil
}

func publishURL(endpoint, agentName string) (string, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to parse Foundry project endpoint")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") +
		"/agents/" + url.PathEscape(agentName) + "/microsoft365/publish"
	query := parsed.Query()
	query.Set("api-version", APIVersion)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func validateResponseDestination(original *url.URL, response *http.Response) error {
	if response.Request == nil || response.Request.URL == nil {
		return nil
	}
	if original == nil || response.Request.URL.String() != original.String() {
		return errs.Security(
			"Microsoft 365 publish followed an unexpected redirect from %q to %q",
			original,
			response.Request.URL.Redacted(),
		)
	}
	return nil
}

func refuseRedirect(request *http.Request, _ []*http.Request) error {
	return errs.Security(
		"Microsoft 365 publishing redirected an authenticated request to %q; refusing to forward the bearer token",
		request.URL.Redacted(),
	)
}

func ambiguous(err error) error {
	return errs.AmbiguousMutation(fmt.Errorf(
		"%w; inspect the Foundry agent publication state and operation receipt before any retry",
		err,
	))
}
