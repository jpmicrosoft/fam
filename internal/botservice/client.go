package botservice

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

	"foundry-agent-manager/internal/arm"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	defaultRequestTimeout = 120 * time.Second
	maxResponseBytes      = 4 << 20
	providerNamespace     = "Microsoft.BotService"
	teamsChannelName      = "MsTeamsChannel"
)

// Client is an AzureCloud-only Microsoft.BotService ARM client.
type Client struct {
	options ARMOptions
	http    HTTPClient
}

// NewClient validates all routing before retaining credentials or sending requests.
func NewClient(options ARMOptions) (*Client, error) {
	if err := validateARMOptions(options); err != nil {
		return nil, err
	}
	client := options.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	} else if standard, ok := client.(*http.Client); ok {
		// Clone caller-owned clients instead of mutating shared state, then pin
		// bearer-bearing requests to their original ARM destination.
		cloned := *standard
		cloned.CheckRedirect = refuseRedirect
		client = &cloned
	}
	return &Client{options: options, http: client}, nil
}

// EnsureBot calls EnsureBotContext with a background context.
func EnsureBot(options ARMOptions, spec BotSpec) (EnsureResult, error) {
	return EnsureBotContext(context.Background(), options, spec)
}

// EnsureBotContext creates a client and ensures a bot resource.
func EnsureBotContext(ctx context.Context, options ARMOptions, spec BotSpec) (EnsureResult, error) {
	client, err := NewClient(options)
	if err != nil {
		return EnsureResult{}, err
	}
	return client.EnsureBotContext(ctx, spec)
}

// EnsureTeamsChannel calls EnsureTeamsChannelContext with a background context.
func EnsureTeamsChannel(options ARMOptions, botName string) (EnsureResult, error) {
	return EnsureTeamsChannelContext(context.Background(), options, botName)
}

// EnsureTeamsChannelContext creates a client and ensures MsTeamsChannel.
func EnsureTeamsChannelContext(ctx context.Context, options ARMOptions, botName string) (EnsureResult, error) {
	client, err := NewClient(options)
	if err != nil {
		return EnsureResult{}, err
	}
	return client.EnsureTeamsChannelContext(ctx, botName)
}

// CheckProviderRegistrationContext validates that Microsoft.BotService is registered.
func CheckProviderRegistrationContext(ctx context.Context, options ARMOptions) error {
	client, err := NewClient(options)
	if err != nil {
		return err
	}
	token, err := client.token(ctx)
	if err != nil {
		return err
	}
	return client.checkProviderRegistration(ctx, token)
}

// EnsureBotContext performs GET-before-PUT reconciliation and verifies any mutation.
func (c *Client) EnsureBotContext(ctx context.Context, spec BotSpec) (EnsureResult, error) {
	spec, err := validateBotSpec(spec)
	if err != nil {
		return EnsureResult{}, err
	}
	token, err := c.token(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	if err := c.checkProviderRegistration(ctx, token); err != nil {
		return EnsureResult{}, err
	}

	resourceURL, err := c.botURL(spec.Name)
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to build bot service ARM URL")
	}
	current, exists, err := c.getBot(ctx, resourceURL, token)
	if err != nil {
		return EnsureResult{}, err
	}
	desired := desiredBotState(c.options, spec)
	if exists {
		if botStateMatches(current, desired) {
			return EnsureResult{Status: StatusUnchanged, ResourceID: current.ID}, nil
		}
		if conflict := immutableBotDrift(current, desired); conflict != "" && !spec.AllowUpdate {
			return EnsureResult{}, errs.Conflict(
				"bot %q already exists with different %s; set AllowUpdate only after confirming the app identity, tenant, and endpoint change",
				spec.Name,
				conflict,
			)
		}
	}

	if err := c.put(ctx, resourceURL, token, botWriteBody(desired), "bot service upsert"); err != nil {
		return EnsureResult{}, err
	}
	verified, found, err := c.getBot(ctx, resourceURL, token)
	if err != nil {
		return EnsureResult{}, errs.AmbiguousMutation(err)
	}
	if !found {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.Conflict("bot %q was not found during post-PUT verification", spec.Name),
		)
	}
	if !botStateMatches(verified, desired) {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.Conflict("bot %q did not match the requested state after PUT", spec.Name),
		)
	}
	status := StatusCreated
	if exists {
		status = StatusUpdated
	}
	return EnsureResult{Status: status, ResourceID: verified.ID}, nil
}

// EnsureTeamsChannelContext performs GET-before-PUT reconciliation for MsTeamsChannel.
func (c *Client) EnsureTeamsChannelContext(ctx context.Context, botName string) (EnsureResult, error) {
	if err := ValidateBotName(botName); err != nil {
		return EnsureResult{}, err
	}
	token, err := c.token(ctx)
	if err != nil {
		return EnsureResult{}, err
	}
	if err := c.checkProviderRegistration(ctx, token); err != nil {
		return EnsureResult{}, err
	}
	channelURL, err := c.channelURL(botName)
	if err != nil {
		return EnsureResult{}, errs.FoundryWrap(err, "failed to build Teams channel ARM URL")
	}
	current, exists, err := c.getTeamsChannel(ctx, channelURL, token, botName)
	if err != nil {
		return EnsureResult{}, err
	}
	desired := desiredTeamsChannel(c.options, botName)
	if exists && teamsChannelMatches(current, desired) {
		return EnsureResult{Status: StatusUnchanged, ResourceID: current.ID}, nil
	}
	if err := c.put(ctx, channelURL, token, channelWriteBody(desired), "Teams channel upsert"); err != nil {
		return EnsureResult{}, err
	}
	verified, found, err := c.getTeamsChannel(ctx, channelURL, token, botName)
	if err != nil {
		return EnsureResult{}, errs.AmbiguousMutation(err)
	}
	if !found {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.Conflict("Teams channel for bot %q was not found during post-PUT verification", botName),
		)
	}
	if !teamsChannelMatches(verified, desired) {
		return EnsureResult{}, errs.AmbiguousMutation(
			errs.Conflict("Teams channel for bot %q did not match the requested state after PUT", botName),
		)
	}
	status := StatusCreated
	if exists {
		status = StatusUpdated
	}
	return EnsureResult{Status: status, ResourceID: verified.ID}, nil
}

func (c *Client) token(ctx context.Context) (string, error) {
	token, err := c.options.Credential.GetToken(
		ctx,
		policy.TokenRequestOptions{Scopes: []string{c.options.ARMScope}},
	)
	if err != nil {
		return "", errs.AuthWrap(err, "failed to get AzureCloud ARM token for Bot Service")
	}
	if token.Token == "" {
		return "", errs.Auth("AzureCloud ARM credential returned an empty token")
	}
	return token.Token, nil
}

func (c *Client) checkProviderRegistration(ctx context.Context, token string) error {
	providerURL, err := arm.ResourceURLForEndpoint(
		c.options.ARMEndpoint,
		ProviderAPIVersion,
		"subscriptions", c.options.SubscriptionID,
		"providers", providerNamespace,
	)
	if err != nil {
		return errs.FoundryWrap(err, "failed to build Bot Service provider registration URL")
	}
	resp, data, err := c.do(ctx, http.MethodGet, providerURL, token, nil)
	if err != nil {
		return wrapARMReadError(err, "failed to read Microsoft.BotService provider registration")
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.ResponseError("ARM", "Bot Service provider registration check", resp, data)
	}
	var payload struct {
		Namespace         string `json:"namespace"`
		RegistrationState string `json:"registrationState"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return errs.FoundryWrap(err, "failed to parse Microsoft.BotService provider registration")
	}
	if payload.Namespace != "" && !strings.EqualFold(payload.Namespace, providerNamespace) {
		return errs.Conflict(
			"ARM returned provider namespace %q while checking %s",
			payload.Namespace,
			providerNamespace,
		)
	}
	if !strings.EqualFold(payload.RegistrationState, "Registered") {
		state := payload.RegistrationState
		if state == "" {
			state = "unknown"
		}
		return errs.Config(
			"Azure resource provider %s is not registered in subscription %s (state: %s); register it explicitly with `az provider register --namespace %s --subscription %s`, wait for Registered, then retry",
			providerNamespace,
			c.options.SubscriptionID,
			state,
			providerNamespace,
			c.options.SubscriptionID,
		)
	}
	return nil
}

func (c *Client) getBot(ctx context.Context, resourceURL, token string) (BotState, bool, error) {
	resp, data, err := c.do(ctx, http.MethodGet, resourceURL, token, nil)
	if err != nil {
		return BotState{}, false, wrapARMReadError(err, "bot service GET failed")
	}
	if resp.StatusCode == http.StatusNotFound {
		return BotState{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return BotState{}, false, httpx.ResponseError("ARM", "bot service GET", resp, data)
	}
	var state BotState
	if err := json.Unmarshal(data, &state); err != nil {
		return BotState{}, false, errs.FoundryWrap(err, "failed to parse bot service GET response")
	}
	if err := validateReturnedBotID(state.ID, c.options, state.Name); err != nil {
		return BotState{}, false, err
	}
	expectedName := resourceNameFromURL(resourceURL)
	if !strings.EqualFold(state.Name, expectedName) {
		return BotState{}, false, errs.Conflict(
			"ARM returned bot name %q while reading %q",
			state.Name,
			expectedName,
		)
	}
	return state, true, nil
}

func (c *Client) getTeamsChannel(
	ctx context.Context,
	resourceURL string,
	token string,
	botName string,
) (TeamsChannelState, bool, error) {
	resp, data, err := c.do(ctx, http.MethodGet, resourceURL, token, nil)
	if err != nil {
		return TeamsChannelState{}, false, wrapARMReadError(err, "Teams channel GET failed")
	}
	if resp.StatusCode == http.StatusNotFound {
		return TeamsChannelState{}, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return TeamsChannelState{}, false, httpx.ResponseError("ARM", "Teams channel GET", resp, data)
	}
	var state TeamsChannelState
	if err := json.Unmarshal(data, &state); err != nil {
		return TeamsChannelState{}, false, errs.FoundryWrap(err, "failed to parse Teams channel GET response")
	}
	if err := validateReturnedChannelID(state.ID, c.options, botName); err != nil {
		return TeamsChannelState{}, false, err
	}
	if state.Name != "" &&
		!strings.EqualFold(state.Name, teamsChannelName) &&
		!strings.EqualFold(state.Name, botName+"/"+teamsChannelName) {
		return TeamsChannelState{}, false, errs.Conflict(
			"ARM returned channel name %q while reading %s",
			state.Name,
			teamsChannelName,
		)
	}
	return state, true, nil
}

func (c *Client) put(ctx context.Context, resourceURL, token string, body any, operation string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return errs.FoundryWrap(err, "failed to encode %s request", operation)
	}
	resp, responseData, err := c.do(ctx, http.MethodPut, resourceURL, token, data)
	if err != nil {
		return errs.AmbiguousMutation(wrapARMReadError(err, operation+" failed"))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		responseErr := httpx.ResponseError("ARM", operation, resp, responseData)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return errs.AmbiguousMutation(responseErr)
		}
		return responseErr
	}
	return nil
}

func (c *Client) do(
	ctx context.Context,
	method string,
	requestURL string,
	token string,
	body []byte,
) (*http.Response, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
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
	resp, err := c.http.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, nil, err
	}
	if resp == nil {
		return nil, nil, fmt.Errorf("HTTP client returned a nil response")
	}
	if resp.Body == nil {
		return nil, nil, fmt.Errorf("HTTP client returned a response with a nil body")
	}
	defer resp.Body.Close()
	if err := validateResponseDestination(req.URL, resp); err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, nil, fmt.Errorf("ARM response exceeded %d bytes", maxResponseBytes)
	}
	return resp, data, nil
}

func (c *Client) botURL(botName string) (string, error) {
	return arm.ResourceURLForEndpoint(
		c.options.ARMEndpoint,
		BotServiceAPIVersion,
		"subscriptions", c.options.SubscriptionID,
		"resourceGroups", c.options.ResourceGroup,
		"providers", providerNamespace,
		"botServices", botName,
	)
}

func (c *Client) channelURL(botName string) (string, error) {
	return arm.ResourceURLForEndpoint(
		c.options.ARMEndpoint,
		ChannelsAPIVersion,
		"subscriptions", c.options.SubscriptionID,
		"resourceGroups", c.options.ResourceGroup,
		"providers", providerNamespace,
		"botServices", botName,
		"channels", teamsChannelName,
	)
}

func desiredBotState(options ARMOptions, spec BotSpec) BotState {
	return BotState{
		ID:       expectedBotID(options, spec.Name),
		Name:     spec.Name,
		Location: "global",
		Kind:     "azurebot",
		SKU:      SKU{Name: "F0"},
		Properties: BotProperties{
			DisplayName:         spec.DisplayName,
			Endpoint:            spec.Endpoint,
			MSAAppID:            spec.MSAAppID,
			MSAAppTenantID:      spec.MSAAppTenantID,
			MSAAppType:          "SingleTenant",
			PublicNetworkAccess: "Disabled",
		},
	}
}

func desiredTeamsChannel(options ARMOptions, botName string) TeamsChannelState {
	return TeamsChannelState{
		ID:       expectedBotID(options, botName) + "/channels/" + teamsChannelName,
		Name:     teamsChannelName,
		Location: "global",
		Properties: ChannelProperties{
			ChannelName: teamsChannelName,
		},
	}
}

func botWriteBody(desired BotState) any {
	return struct {
		Location   string        `json:"location"`
		Kind       string        `json:"kind"`
		SKU        SKU           `json:"sku"`
		Properties BotProperties `json:"properties"`
	}{
		Location:   desired.Location,
		Kind:       desired.Kind,
		SKU:        desired.SKU,
		Properties: desired.Properties,
	}
}

func channelWriteBody(desired TeamsChannelState) any {
	return struct {
		Location   string            `json:"location"`
		Name       string            `json:"name"`
		Properties ChannelProperties `json:"properties"`
	}{
		Location:   desired.Location,
		Name:       desired.Name,
		Properties: desired.Properties,
	}
}

func botStateMatches(actual, desired BotState) bool {
	actualEndpoint, err := validateMessagingEndpoint(actual.Properties.Endpoint)
	if err != nil {
		return false
	}
	return strings.EqualFold(actual.Location, desired.Location) &&
		strings.EqualFold(actual.Kind, desired.Kind) &&
		strings.EqualFold(actual.SKU.Name, desired.SKU.Name) &&
		actual.Properties.DisplayName == desired.Properties.DisplayName &&
		actualEndpoint == desired.Properties.Endpoint &&
		strings.EqualFold(actual.Properties.MSAAppID, desired.Properties.MSAAppID) &&
		strings.EqualFold(actual.Properties.MSAAppTenantID, desired.Properties.MSAAppTenantID) &&
		strings.EqualFold(actual.Properties.MSAAppType, desired.Properties.MSAAppType) &&
		strings.EqualFold(actual.Properties.PublicNetworkAccess, desired.Properties.PublicNetworkAccess)
}

func immutableBotDrift(actual, desired BotState) string {
	fields := make([]string, 0, 3)
	if !strings.EqualFold(actual.Properties.MSAAppID, desired.Properties.MSAAppID) {
		fields = append(fields, "msaAppId")
	}
	if !strings.EqualFold(actual.Properties.MSAAppTenantID, desired.Properties.MSAAppTenantID) {
		fields = append(fields, "msaAppTenantId")
	}
	actualEndpoint, err := validateMessagingEndpoint(actual.Properties.Endpoint)
	if err != nil || actualEndpoint != desired.Properties.Endpoint {
		fields = append(fields, "endpoint")
	}
	return strings.Join(fields, ", ")
}

func teamsChannelMatches(actual, desired TeamsChannelState) bool {
	return strings.EqualFold(actual.Location, desired.Location) &&
		strings.EqualFold(actual.Properties.ChannelName, desired.Properties.ChannelName)
}

func defaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       defaultRequestTimeout,
		CheckRedirect: refuseRedirect,
	}
}

func refuseRedirect(req *http.Request, _ []*http.Request) error {
	return errs.Security(
		"ARM redirected a Bot Service request to %q; refusing to forward the bearer token",
		req.URL.Redacted(),
	)
}

func validateARMRequestURL(value *url.URL) error {
	if value == nil ||
		value.Scheme != "https" ||
		!strings.EqualFold(value.Hostname(), "management.azure.com") ||
		value.Port() != "" ||
		value.User != nil {
		return errs.Security("refusing to send an ARM bearer token to invalid URL %q", value)
	}
	return nil
}

func validateResponseDestination(original *url.URL, resp *http.Response) error {
	if resp.Request == nil || resp.Request.URL == nil {
		return nil
	}
	if err := validateARMRequestURL(resp.Request.URL); err != nil {
		return errs.SecurityWrap(err, "ARM response followed an unsafe redirect")
	}
	if original != nil && resp.Request.URL.String() != original.String() {
		return errs.Security(
			"ARM response followed an unexpected redirect from %q to %q",
			original.Redacted(),
			resp.Request.URL.Redacted(),
		)
	}
	return nil
}

func resourceNameFromURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	name, _ := url.PathUnescape(parts[len(parts)-1])
	return name
}

func wrapARMReadError(err error, message string) error {
	if errs.IsKind(err, "security") {
		return errs.SecurityWrap(err, "%s", message)
	}
	return errs.FoundryWrap(err, "%s", message)
}
