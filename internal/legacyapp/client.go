package legacyapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"foundry-agent-manager/internal/arm"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/redact"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	defaultPollInterval     = 2 * time.Second
	defaultMaxPollAttempts  = 150
	defaultMaxResponseBytes = int64(1024 * 1024)
)

// Client operates only on the explicit legacy application ARM resources.
type Client struct {
	options          Options
	httpClient       HTTPClient
	pollInterval     time.Duration
	maxPollAttempts  int
	maxResponseBytes int64
	sleep            func(context.Context, time.Duration) error
}

// DeleteResult records which legacy resources were removed.
type DeleteResult struct {
	DeletedDeployment  bool
	DeletedApplication bool
}

// NewClient validates all ARM routing and resource identifiers before authentication.
func NewClient(options Options) (*Client, error) {
	validated, err := validateOptions(options)
	if err != nil {
		return nil, err
	}
	if validated.HTTPClient == nil {
		validated.HTTPClient = httpx.NewRetryClient(
			newDefaultHTTPClient(),
			httpx.Options{Retries: 2},
		)
	} else {
		validated.HTTPClient = hardenHTTPClient(validated.HTTPClient)
	}
	if validated.PollInterval <= 0 {
		validated.PollInterval = defaultPollInterval
	}
	if validated.MaxPollAttempts <= 0 {
		validated.MaxPollAttempts = defaultMaxPollAttempts
	}
	return &Client{
		options:          validated,
		httpClient:       validated.HTTPClient,
		pollInterval:     validated.PollInterval,
		maxPollAttempts:  validated.MaxPollAttempts,
		maxResponseBytes: defaultMaxResponseBytes,
		sleep:            sleepContext,
	}, nil
}

// GetApplication returns the selected legacy application state.
func (c *Client) GetApplication(ctx context.Context) (ApplicationState, error) {
	requestURL, err := c.applicationURL()
	if err != nil {
		return ApplicationState{}, err
	}
	var payload struct {
		ID         string                `json:"id"`
		Name       string                `json:"name"`
		ETag       string                `json:"etag"`
		Properties ApplicationProperties `json:"properties"`
	}
	found, err := c.get(ctx, requestURL, "legacy application inspection", &payload)
	if err != nil {
		return ApplicationState{}, err
	}
	if !found {
		return ApplicationState{Exists: false, Name: c.options.ApplicationName}, nil
	}
	if err := validateReturnedApplicationID(payload.ID, c.options); err != nil {
		return ApplicationState{}, err
	}
	if payload.Name != "" && !strings.EqualFold(payload.Name, c.options.ApplicationName) {
		return ApplicationState{}, errs.Conflict(
			"Azure returned legacy application name %q instead of %q",
			payload.Name,
			c.options.ApplicationName,
		)
	}
	return ApplicationState{
		Exists:     true,
		ID:         payload.ID,
		Name:       defaultString(payload.Name, c.options.ApplicationName),
		ETag:       payload.ETag,
		Properties: payload.Properties,
	}, nil
}

// EnsureApplication creates or updates writable application metadata and,
// when supplied, the application's represented agents.
func (c *Client) EnsureApplication(
	ctx context.Context,
	metadata ApplicationMetadata,
	agents ...AgentReference,
) (ApplicationResult, error) {
	metadata, err := normalizeApplicationMetadata(metadata, c.options.ApplicationName)
	if err != nil {
		return ApplicationResult{}, err
	}
	for i, agent := range agents {
		if strings.TrimSpace(agent.AgentID) == "" || strings.TrimSpace(agent.AgentName) == "" {
			return ApplicationResult{}, errs.Config(
				"legacy application agent %d requires agentId and agentName",
				i,
			)
		}
	}
	current, err := c.GetApplication(ctx)
	if err != nil {
		return ApplicationResult{}, err
	}
	desiredAgents := current.Properties.Agents
	if len(agents) > 0 {
		desiredAgents = append([]AgentReference(nil), agents...)
	}
	if current.Exists &&
		applicationMetadataConverged(current.Properties.ApplicationMetadata, metadata) &&
		agentReferencesEquivalent(current.Properties.Agents, desiredAgents) {
		return ApplicationResult{Change: ChangeNone, State: current}, nil
	}

	properties := ApplicationProperties{
		ApplicationMetadata: metadata,
		Agents:              desiredAgents,
	}
	if current.Exists {
		properties.TrafficRoutingPolicy = current.Properties.TrafficRoutingPolicy
	}
	headers := conditionalHeaders(current.Exists, current.ETag)
	requestURL, err := c.applicationURL()
	if err != nil {
		return ApplicationResult{}, err
	}
	status, err := c.put(ctx, requestURL, "legacy application upsert", map[string]any{
		"properties": properties,
	}, headers)
	if err != nil {
		return ApplicationResult{}, err
	}
	state, err := c.GetApplication(ctx)
	if err != nil {
		return ApplicationResult{}, errs.AmbiguousMutation(err)
	}
	if !state.Exists {
		return ApplicationResult{}, errs.AmbiguousMutation(
			errs.Foundry("legacy application was not found after a successful upsert"),
		)
	}
	if !applicationMetadataConverged(state.Properties.ApplicationMetadata, metadata) ||
		!agentReferencesEquivalent(state.Properties.Agents, desiredAgents) {
		return ApplicationResult{}, errs.AmbiguousMutation(
			errs.Conflict("legacy application state did not converge after a successful upsert"),
		)
	}
	change := ChangeUpdated
	if !current.Exists && status == http.StatusCreated {
		change = ChangeCreated
	}
	return ApplicationResult{Change: change, State: state}, nil
}

// GetDeployment returns the selected child agentDeployment state.
func (c *Client) GetDeployment(ctx context.Context) (DeploymentState, error) {
	requestURL, err := c.deploymentURL()
	if err != nil {
		return DeploymentState{}, err
	}
	var payload struct {
		ID         string               `json:"id"`
		Name       string               `json:"name"`
		ETag       string               `json:"etag"`
		Properties DeploymentProperties `json:"properties"`
	}
	found, err := c.get(ctx, requestURL, "legacy agent deployment inspection", &payload)
	if err != nil {
		return DeploymentState{}, err
	}
	if !found {
		return DeploymentState{Exists: false, Name: c.options.DeploymentName}, nil
	}
	if err := validateReturnedDeploymentID(payload.ID, c.options); err != nil {
		return DeploymentState{}, err
	}
	if payload.Name != "" && !strings.EqualFold(payload.Name, c.options.DeploymentName) {
		return DeploymentState{}, errs.Conflict(
			"Azure returned legacy agent deployment name %q instead of %q",
			payload.Name,
			c.options.DeploymentName,
		)
	}
	return DeploymentState{
		Exists:     true,
		ID:         payload.ID,
		Name:       defaultString(payload.Name, c.options.DeploymentName),
		ETag:       payload.ETag,
		Properties: payload.Properties,
	}, nil
}

// EnsureManagedDeployment reconciles a Managed deployment exposing Responses v1.
func (c *Client) EnsureManagedDeployment(ctx context.Context, spec ManagedDeploymentSpec) (DeploymentResult, error) {
	spec, err := validateManagedDeployment(spec, c.options.DeploymentName)
	if err != nil {
		return DeploymentResult{}, err
	}
	current, err := c.GetDeployment(ctx)
	if err != nil {
		return DeploymentResult{}, err
	}
	desired := desiredDeploymentProperties(spec)
	if current.Exists && deploymentPropertiesConverged(current.Properties, desired) {
		return DeploymentResult{Change: ChangeNone, State: current}, nil
	}
	requestURL, err := c.deploymentURL()
	if err != nil {
		return DeploymentResult{}, err
	}
	status, err := c.put(ctx, requestURL, "legacy agent deployment upsert", map[string]any{
		"properties": desired,
	}, conditionalHeaders(current.Exists, current.ETag))
	if err != nil {
		return DeploymentResult{}, err
	}
	state, err := c.GetDeployment(ctx)
	if err != nil {
		return DeploymentResult{}, errs.AmbiguousMutation(err)
	}
	if !state.Exists {
		return DeploymentResult{}, errs.AmbiguousMutation(
			errs.Foundry("legacy agent deployment was not found after a successful upsert"),
		)
	}
	if !deploymentPropertiesConverged(state.Properties, desired) {
		return DeploymentResult{}, errs.AmbiguousMutation(
			errs.Conflict("legacy agent deployment did not converge after a successful upsert"),
		)
	}
	change := ChangeUpdated
	if !current.Exists && status == http.StatusCreated {
		change = ChangeCreated
	}
	return DeploymentResult{Change: change, State: state}, nil
}

// RouteAllTraffic updates the application to route 100 percent to the deployment.
func (c *Client) RouteAllTraffic(ctx context.Context) (RoutingResult, error) {
	deployment, err := c.GetDeployment(ctx)
	if err != nil {
		return RoutingResult{}, err
	}
	if !deployment.Exists {
		return RoutingResult{}, errs.NotFound(
			"legacy agent deployment %q does not exist",
			c.options.DeploymentName,
		)
	}
	deploymentID := deployment.Properties.DeploymentID
	if deploymentID == "" {
		return RoutingResult{}, errs.Foundry(
			"legacy agent deployment %q did not report properties.deploymentId",
			c.options.DeploymentName,
		)
	}
	application, err := c.GetApplication(ctx)
	if err != nil {
		return RoutingResult{}, err
	}
	if !application.Exists {
		return RoutingResult{}, errs.NotFound(
			"legacy application %q does not exist",
			c.options.ApplicationName,
		)
	}
	desired := TrafficRoutingPolicy{
		Protocol: RoutingProtocolFixed,
		Rules: []TrafficRoutingRule{{
			RuleID:            c.options.DeploymentName,
			DeploymentID:      deploymentID,
			TrafficPercentage: 100,
		}},
	}
	if routingEqual(application.Properties.TrafficRoutingPolicy, desired) {
		return RoutingResult{
			Change:       ChangeNone,
			DeploymentID: deploymentID,
			State:        application,
		}, nil
	}
	properties := ApplicationProperties{
		ApplicationMetadata:  application.Properties.ApplicationMetadata,
		Agents:               application.Properties.Agents,
		TrafficRoutingPolicy: &desired,
	}
	requestURL, err := c.applicationURL()
	if err != nil {
		return RoutingResult{}, err
	}
	_, err = c.put(ctx, requestURL, "legacy application traffic routing update", map[string]any{
		"properties": properties,
	}, conditionalHeaders(true, application.ETag))
	if err != nil {
		return RoutingResult{}, err
	}
	state, err := c.GetApplication(ctx)
	if err != nil {
		return RoutingResult{}, errs.AmbiguousMutation(err)
	}
	if !state.Exists || !routingEqual(state.Properties.TrafficRoutingPolicy, desired) {
		return RoutingResult{}, errs.AmbiguousMutation(
			errs.Conflict("legacy application traffic routing did not converge after a successful update"),
		)
	}
	return RoutingResult{
		Change:       ChangeUpdated,
		DeploymentID: deploymentID,
		State:        state,
	}, nil
}

// Status returns application and deployment state without mutating Azure.
func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	application, err := c.GetApplication(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	deployment, err := c.GetDeployment(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	return StatusResult{Application: application, Deployment: deployment}, nil
}

// DeleteDeployment deletes the selected deployment. Missing is a successful no-op.
func (c *Client) DeleteDeployment(ctx context.Context) (bool, error) {
	requestURL, err := c.deploymentURL()
	if err != nil {
		return false, err
	}
	return c.delete(ctx, requestURL, "legacy agent deployment deletion")
}

// DeleteApplication deletes the selected application. Missing is a successful no-op.
func (c *Client) DeleteApplication(ctx context.Context) (bool, error) {
	requestURL, err := c.applicationURL()
	if err != nil {
		return false, err
	}
	return c.delete(ctx, requestURL, "legacy application deletion")
}

// Delete removes either the deployment alone or the parent application and
// its deployment. Deleting the parent directly avoids a service-side failure
// observed when a child deployment references an agent that no longer exists.
func (c *Client) Delete(ctx context.Context, removeApplication bool) (DeleteResult, error) {
	if !removeApplication {
		deleted, err := c.DeleteDeployment(ctx)
		return DeleteResult{DeletedDeployment: deleted}, err
	}
	deployment, err := c.GetDeployment(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	application, err := c.GetApplication(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	if !application.Exists {
		return DeleteResult{}, nil
	}
	if len(application.Properties.Agents) > 0 ||
		routingConfigured(application.Properties.TrafficRoutingPolicy) {
		requestURL, err := c.applicationURL()
		if err != nil {
			return DeleteResult{}, err
		}
		properties := ApplicationProperties{
			ApplicationMetadata: application.Properties.ApplicationMetadata,
			Agents:              application.Properties.Agents,
		}
		if _, err := c.put(
			ctx,
			requestURL,
			"legacy application dependency cleanup",
			map[string]any{"properties": properties},
			conditionalHeaders(true, application.ETag),
		); err != nil {
			return DeleteResult{}, err
		}
		cleaned, err := c.GetApplication(ctx)
		if err != nil {
			return DeleteResult{}, errs.AmbiguousMutation(err)
		}
		if !cleaned.Exists ||
			routingConfigured(cleaned.Properties.TrafficRoutingPolicy) {
			return DeleteResult{}, errs.AmbiguousMutation(
				errs.Conflict("legacy application dependencies did not clear before deletion"),
			)
		}
	}
	deleted, err := c.DeleteApplication(ctx)
	if err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{
		DeletedDeployment:  deployment.Exists && deleted,
		DeletedApplication: deleted,
	}, nil
}

func routingConfigured(policy *TrafficRoutingPolicy) bool {
	return policy != nil && len(policy.Rules) > 0
}

func (c *Client) applicationURL() (string, error) {
	requestURL, err := arm.ResourceURLForEndpoint(
		c.options.ARMEndpoint,
		APIVersion,
		"subscriptions", c.options.SubscriptionID,
		"resourceGroups", c.options.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", c.options.AccountName,
		"projects", c.options.ProjectName,
		"applications", c.options.ApplicationName,
	)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build legacy application ARM URL")
	}
	return requestURL, nil
}

func (c *Client) deploymentURL() (string, error) {
	requestURL, err := arm.ResourceURLForEndpoint(
		c.options.ARMEndpoint,
		APIVersion,
		"subscriptions", c.options.SubscriptionID,
		"resourceGroups", c.options.ResourceGroup,
		"providers", "Microsoft.CognitiveServices",
		"accounts", c.options.AccountName,
		"projects", c.options.ProjectName,
		"applications", c.options.ApplicationName,
		"agentDeployments", c.options.DeploymentName,
	)
	if err != nil {
		return "", errs.FoundryWrap(err, "failed to build legacy agent deployment ARM URL")
	}
	return requestURL, nil
}

func (c *Client) get(ctx context.Context, requestURL, operation string, target any) (bool, error) {
	resp, token, err := c.do(ctx, http.MethodGet, requestURL, nil, nil)
	if err != nil {
		return false, classifyTransport(operation, err, token, false)
	}
	defer resp.Body.Close()
	data, err := c.readBody(resp.Body)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to read %s response", operation)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, httpx.ResponseError("ARM", operation, resp, redact.Bytes(data, token))
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, errs.FoundryWrap(err, "failed to parse %s response", operation)
	}
	return true, nil
}

func (c *Client) put(
	ctx context.Context,
	requestURL string,
	operation string,
	body any,
	headers map[string]string,
) (int, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return 0, errs.FoundryWrap(err, "failed to encode %s request", operation)
	}
	return c.mutate(ctx, http.MethodPut, requestURL, operation, data, headers)
}

func (c *Client) delete(ctx context.Context, requestURL, operation string) (bool, error) {
	status, err := c.mutate(ctx, http.MethodDelete, requestURL, operation, nil, nil)
	if err != nil {
		return false, err
	}
	return status != http.StatusNotFound, nil
}

func (c *Client) mutate(
	ctx context.Context,
	method string,
	requestURL string,
	operation string,
	body []byte,
	headers map[string]string,
) (int, error) {
	resp, token, err := c.do(ctx, method, requestURL, body, headers)
	if err != nil {
		return 0, classifyTransport(operation, err, token, true)
	}
	defer resp.Body.Close()
	data, err := c.readBody(resp.Body)
	if err != nil {
		return 0, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read %s response", operation),
		)
	}

	if method == http.MethodDelete && resp.StatusCode == http.StatusNotFound {
		return resp.StatusCode, nil
	}
	if !successfulMutationStatus(method, resp.StatusCode) {
		responseErr := httpx.ResponseError("ARM", operation, resp, redact.Bytes(data, token))
		if httpx.IsTransientStatus(resp.StatusCode) {
			return 0, errs.AmbiguousMutation(responseErr)
		}
		return 0, responseErr
	}
	if resp.StatusCode == http.StatusAccepted {
		pollURL := resp.Header.Get("Azure-AsyncOperation")
		allowEmptyStatus := false
		if pollURL == "" {
			pollURL = resp.Header.Get("Location")
			allowEmptyStatus = true
		}
		if pollURL == "" {
			return 0, errs.AmbiguousMutation(
				errs.Foundry("%s returned 202 without Azure-AsyncOperation or Location", operation),
			)
		}
		if err := c.poll(ctx, pollURL, operation, token, resp.Header.Get("Retry-After"), allowEmptyStatus); err != nil {
			return 0, err
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) poll(
	ctx context.Context,
	rawURL string,
	operation string,
	initialToken string,
	initialRetryAfter string,
	allowEmptyStatus bool,
) error {
	pollURL, err := c.validatePollURL(rawURL)
	if err != nil {
		return errs.AmbiguousMutation(err)
	}
	delay := c.retryDelay(initialRetryAfter)
	for attempt := 0; attempt < c.maxPollAttempts; attempt++ {
		if err := c.sleep(ctx, delay); err != nil {
			return errs.AmbiguousMutation(err)
		}
		resp, token, err := c.do(ctx, http.MethodGet, pollURL, nil, nil)
		if err != nil {
			if token == "" {
				token = initialToken
			}
			return classifyTransport(operation+" LRO polling", err, token, true)
		}
		data, readErr := c.readBody(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return errs.AmbiguousMutation(
				errs.FoundryWrap(readErr, "failed to read %s LRO response", operation),
			)
		}
		if httpx.IsTransientStatus(resp.StatusCode) {
			return errs.AmbiguousMutation(
				httpx.ResponseError("ARM", operation+" LRO polling", resp, redact.Bytes(data, token)),
			)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return httpx.ResponseError("ARM", operation+" LRO polling", resp, redact.Bytes(data, token))
		}
		status, err := lroStatus(data)
		if err != nil {
			return errs.FoundryWrap(err, "failed to parse %s LRO response", operation)
		}
		switch strings.ToLower(status) {
		case "succeeded", "completed":
			return nil
		case "failed", "canceled", "cancelled":
			return errs.Foundry(
				"%s LRO reported %s: %s",
				operation,
				status,
				strings.TrimSpace(string(redact.Bytes(data, token))),
			)
		case "":
			if resp.StatusCode == http.StatusNoContent ||
				(allowEmptyStatus && resp.StatusCode == http.StatusOK) {
				return nil
			}
			return errs.Foundry("%s LRO response did not contain a status", operation)
		case "accepted", "creating", "deleting", "inprogress", "running", "starting", "stopping", "updating":
			// Continue polling.
		default:
			return errs.Foundry("%s LRO reported unknown status %q", operation, status)
		}
		delay = c.retryDelay(resp.Header.Get("Retry-After"))
	}
	return errs.AmbiguousMutation(
		errs.Transient("%s LRO did not complete after %d polls", operation, c.maxPollAttempts),
	)
}

func (c *Client) do(
	ctx context.Context,
	method string,
	requestURL string,
	body []byte,
	headers map[string]string,
) (*http.Response, string, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return nil, "", errs.FoundryWrap(err, "failed to create ARM request")
	}
	if err := c.validateARMOrigin(req.URL, "ARM request"); err != nil {
		return nil, "", err
	}
	token, err := c.options.Credential.GetToken(
		ctx,
		policy.TokenRequestOptions{Scopes: []string{c.options.ARMScope}},
	)
	if err != nil {
		return nil, "", errs.AuthWrap(err, "failed to get ARM token")
	}
	if strings.TrimSpace(token.Token) == "" {
		return nil, "", errs.Auth("ARM credential returned an empty token")
	}
	req.Header.Set("Authorization", "Bearer "+token.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		closeResponse(resp)
		return resp, token.Token, err
	}
	if resp == nil {
		return nil, token.Token, errs.Transient("ARM HTTP client returned no response")
	}
	if err := c.validateARMOrigin(req.URL, "ARM request after HTTP dispatch"); err != nil {
		closeResponse(resp)
		return nil, token.Token, err
	}
	if resp.Request != nil {
		if err := c.validateARMOrigin(resp.Request.URL, "ARM response request"); err != nil {
			closeResponse(resp)
			return nil, token.Token, err
		}
	}
	if isRedirectStatus(resp.StatusCode) {
		location := resp.Header.Get("Location")
		closeResponse(resp)
		return nil, token.Token, errs.Security(
			"ARM redirect response (%d) was refused (Location=%q)",
			resp.StatusCode,
			location,
		)
	}
	return resp, token.Token, nil
}

func (c *Client) readBody(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(reader, c.maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > c.maxResponseBytes {
		return nil, fmt.Errorf("response exceeded %d bytes", c.maxResponseBytes)
	}
	return data, nil
}

func (c *Client) validatePollURL(raw string) (string, error) {
	base, _ := url.Parse(c.options.ARMEndpoint)
	reference, err := url.Parse(raw)
	if err != nil {
		return "", errs.Security("invalid ARM LRO URL %q: %v", raw, err)
	}
	resolved := base.ResolveReference(reference)
	if resolved.Scheme != "https" ||
		!strings.EqualFold(resolved.Hostname(), base.Hostname()) ||
		resolved.Port() != "" ||
		resolved.User != nil ||
		resolved.Fragment != "" {
		return "", errs.Security("ARM LRO URL %q is outside the selected ARM endpoint", raw)
	}
	pathPrefix := "/subscriptions/" + strings.ToLower(c.options.SubscriptionID) + "/"
	if !strings.HasPrefix(strings.ToLower(resolved.EscapedPath()), pathPrefix) {
		return "", errs.Security("ARM LRO URL %q is outside the selected subscription", raw)
	}
	return resolved.String(), nil
}

func (c *Client) validateARMOrigin(candidate *url.URL, label string) error {
	if candidate == nil {
		return errs.Security("%s destination is missing", label)
	}
	selected, _ := url.Parse(c.options.ARMEndpoint)
	if candidate.Scheme != "https" ||
		!strings.EqualFold(candidate.Hostname(), selected.Hostname()) ||
		candidate.Port() != "" ||
		candidate.User != nil {
		return errs.Security(
			"%s destination %q is outside the selected ARM origin %q",
			label,
			candidate.String(),
			c.options.ARMEndpoint,
		)
	}
	return nil
}

func (c *Client) retryDelay(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return minDuration(time.Duration(seconds)*time.Second, 30*time.Second)
	}
	if when, err := http.ParseTime(value); err == nil {
		return minDuration(maxDuration(time.Until(when), 0), 30*time.Second)
	}
	return c.pollInterval
}

func lroStatus(data []byte) (string, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return "", nil
	}
	var payload struct {
		Status     string `json:"status"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	if payload.Status != "" {
		return payload.Status, nil
	}
	return payload.Properties.ProvisioningState, nil
}

func desiredDeploymentProperties(spec ManagedDeploymentSpec) DeploymentProperties {
	return DeploymentProperties{
		DeploymentType: DeploymentTypeManaged,
		DisplayName:    spec.DisplayName,
		Description:    spec.Description,
		Tags:           cloneTags(spec.Tags),
		Agents: []VersionedAgentReference{{
			AgentID:      spec.AgentID,
			AgentName:    spec.AgentName,
			AgentVersion: spec.AgentVersion,
		}},
		Protocols: []ProtocolVersion{{
			Protocol: ProtocolResponses,
			Version:  ProtocolVersionV1,
		}},
	}
}

func applicationMetadataConverged(current, desired ApplicationMetadata) bool {
	if current.DisplayName != desired.DisplayName {
		return false
	}
	// The preview service currently omits accepted description and tags values
	// from GET responses, so verify them only when the service returns them.
	if current.Description != "" && current.Description != desired.Description {
		return false
	}
	return len(current.Tags) == 0 ||
		reflect.DeepEqual(normalizeTags(current.Tags), normalizeTags(desired.Tags))
}

func agentReferencesEquivalent(current, desired []AgentReference) bool {
	if len(current) != len(desired) {
		return false
	}
	for _, wanted := range desired {
		found := false
		for _, existing := range current {
			if existing.AgentName != wanted.AgentName {
				continue
			}
			if existing.AgentID == wanted.AgentID ||
				strings.HasSuffix(
					strings.TrimRight(existing.AgentID, "/"),
					"/agents/"+wanted.AgentName,
				) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func deploymentPropertiesConverged(current, desired DeploymentProperties) bool {
	if current.DeploymentType != desired.DeploymentType ||
		current.DisplayName != desired.DisplayName ||
		!reflect.DeepEqual(current.Agents, desired.Agents) ||
		!reflect.DeepEqual(current.Protocols, desired.Protocols) {
		return false
	}
	if current.Description != "" && current.Description != desired.Description {
		return false
	}
	return len(current.Tags) == 0 ||
		reflect.DeepEqual(normalizeTags(current.Tags), normalizeTags(desired.Tags))
}

func routingEqual(current *TrafficRoutingPolicy, desired TrafficRoutingPolicy) bool {
	if current == nil || current.Protocol != desired.Protocol || len(current.Rules) != 1 {
		return false
	}
	return current.Rules[0].DeploymentID == desired.Rules[0].DeploymentID &&
		current.Rules[0].TrafficPercentage == 100
}

func conditionalHeaders(exists bool, etag string) map[string]string {
	if !exists {
		return map[string]string{"If-None-Match": "*"}
	}
	if etag != "" {
		return map[string]string{"If-Match": etag}
	}
	return nil
}

func successfulMutationStatus(method string, status int) bool {
	if method == http.MethodDelete {
		return status == http.StatusOK ||
			status == http.StatusAccepted ||
			status == http.StatusNoContent
	}
	return status == http.StatusOK ||
		status == http.StatusCreated ||
		status == http.StatusAccepted
}

func classifyTransport(operation string, err error, token string, mutation bool) error {
	if err == nil {
		return nil
	}
	if errs.IsAuthenticationOrAuthorization(err) || errs.IsKind(err, "security") {
		return err
	}
	message := redact.Text(err.Error(), token)
	classified := errs.Transient("%s failed: %s", operation, message)
	if mutation {
		return errs.AmbiguousMutation(classified)
	}
	return classified
}

func newDefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       120 * time.Second,
		CheckRedirect: refuseARMRedirect,
	}
}

func hardenHTTPClient(client HTTPClient) HTTPClient {
	standard, ok := client.(*http.Client)
	if !ok {
		return client
	}
	cloned := *standard
	cloned.CheckRedirect = refuseARMRedirect
	return &cloned
}

func refuseARMRedirect(req *http.Request, _ []*http.Request) error {
	return errs.Security("ARM redirect to %q was refused", req.URL.String())
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

func closeResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func normalizeTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	return tags
}

func cloneTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	result := make(map[string]string, len(tags))
	for key, value := range tags {
		result[key] = value
	}
	return result
}

func defaultString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
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

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}
