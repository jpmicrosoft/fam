// Package foundry implements Foundry prompt-agent lifecycle operations via direct HTTP.
package foundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	PublicScope = "https://ai.azure.com/.default"
	apiVersion  = "v1"
	scope       = PublicScope

	previewHeader = "WorkflowAgents=V1Preview,ExternalAgents=V1Preview,DraftAgents=V1Preview,AgentsOptimization=V2Preview"
)

// ClientOptions configures cloud authentication and preview behavior.
type ClientOptions struct {
	Scope        string
	AllowPreview bool
}

// AgentResult is the outcome of an agent version creation.
type AgentResult struct {
	ID      string `json:"id" yaml:"id"`
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
	Status  string `json:"status,omitempty" yaml:"status,omitempty"`
}

// Agent is the remote logical agent and its latest version.
type Agent struct {
	Object             string                   `json:"object,omitempty" yaml:"object,omitempty"`
	ID                 string                   `json:"id" yaml:"id"`
	Name               string                   `json:"name" yaml:"name"`
	State              string                   `json:"state" yaml:"state"`
	AgentEndpoint      *AgentEndpointConfig     `json:"agent_endpoint,omitempty" yaml:"agentEndpoint,omitempty"`
	InstanceIdentity   *AgentIdentity           `json:"instance_identity,omitempty" yaml:"instanceIdentity,omitempty"`
	Blueprint          *AgentIdentity           `json:"blueprint,omitempty" yaml:"blueprint,omitempty"`
	BlueprintReference *AgentBlueprintReference `json:"blueprint_reference,omitempty" yaml:"blueprintReference,omitempty"`
	AgentCard          *AgentCard               `json:"agent_card,omitempty" yaml:"agentCard,omitempty"`
	Versions           struct {
		Latest AgentVersion `json:"latest" yaml:"latest"`
	} `json:"versions" yaml:"versions"`
	AdditionalFields map[string]json.RawMessage `json:"-" yaml:"-"`
}

// AgentVersion is the service representation of one immutable version.
type AgentVersion struct {
	Object           string                 `json:"object,omitempty" yaml:"object,omitempty"`
	ID               string                 `json:"id" yaml:"id"`
	Name             string                 `json:"name" yaml:"name"`
	Version          string                 `json:"version" yaml:"version"`
	Description      string                 `json:"description,omitempty" yaml:"description,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt        int64                  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	Draft            bool                   `json:"draft,omitempty" yaml:"draft,omitempty"`
	Status           string                 `json:"status,omitempty" yaml:"status,omitempty"`
	Definition       map[string]interface{} `json:"definition" yaml:"definition"`
	Error            interface{}            `json:"error,omitempty" yaml:"error,omitempty"`
	InstanceIdentity *AgentIdentity         `json:"instance_identity,omitempty" yaml:"instanceIdentity,omitempty"`
}

// UnmarshalJSON accepts the service's string or numeric version representation.
func (v *AgentVersion) UnmarshalJSON(data []byte) error {
	type alias AgentVersion
	var payload struct {
		alias
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	version, err := parseVersion(payload.Version)
	if err != nil {
		return err
	}
	*v = AgentVersion(payload.alias)
	v.Version = version
	return nil
}

// InvocationResult contains the useful fields from a Responses API smoke test.
type InvocationResult struct {
	ID                string                `json:"id" yaml:"id"`
	OutputText        string                `json:"outputText,omitempty" yaml:"outputText,omitempty"`
	ApprovalRequests  []MCPApprovalRequest  `json:"approvalRequests,omitempty" yaml:"approvalRequests,omitempty"`
	ApprovalDecisions []MCPApprovalDecision `json:"approvalDecisions,omitempty" yaml:"approvalDecisions,omitempty"`
	ApprovalRounds    int                   `json:"approvalRounds,omitempty" yaml:"approvalRounds,omitempty"`
}

type InvocationOptions struct {
	StructuredInputs map[string]interface{}
	MemoryUserID     string
}

type MCPApprovalRequest struct {
	ID          string      `json:"id" yaml:"id"`
	ServerLabel string      `json:"serverLabel" yaml:"serverLabel"`
	ToolName    string      `json:"toolName" yaml:"toolName"`
	Arguments   interface{} `json:"arguments,omitempty" yaml:"arguments,omitempty"`
}

type MCPApprovalDecision struct {
	ApprovalRequestID string `json:"approvalRequestId" yaml:"approvalRequestId"`
	ServerLabel       string `json:"serverLabel" yaml:"serverLabel"`
	ToolName          string `json:"toolName" yaml:"toolName"`
	Approve           bool   `json:"approve" yaml:"approve"`
}

// HTTPClient abstracts net/http for testing.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client manages the Foundry prompt-agent lifecycle.
type Client struct {
	endpoint     string
	scope        string
	cred         azcore.TokenCredential
	httpClient   HTTPClient
	allowPreview bool
}

// NewClient creates a public-cloud client and preserves the original constructor.
func NewClient(endpoint string, cred azcore.TokenCredential, httpClient HTTPClient, allowPreview bool) *Client {
	return NewClientWithOptions(endpoint, cred, httpClient, ClientOptions{
		Scope:        PublicScope,
		AllowPreview: allowPreview,
	})
}

// NewClientWithOptions creates a client for a selected Azure cloud.
func NewClientWithOptions(endpoint string, cred azcore.TokenCredential, httpClient HTTPClient, options ClientOptions) *Client {
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 120 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Client{
		endpoint:     strings.TrimRight(endpoint, "/"),
		scope:        options.Scope,
		cred:         cred,
		httpClient:   httpClient,
		allowPreview: options.AllowPreview,
	}
}

func (c *Client) token(ctx context.Context) (string, error) {
	if strings.TrimSpace(c.scope) == "" {
		return "", errs.Config("Foundry token scope is required")
	}
	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.scope}})
	if err != nil {
		return "", errs.AuthWrap(err, "failed to get Foundry token")
	}
	if strings.TrimSpace(token.Token) == "" {
		return "", errs.Auth("Foundry credential returned an empty token")
	}
	return token.Token, nil
}

func (c *Client) do(ctx context.Context, method, path string, body interface{}) (*http.Response, error) {
	return c.doWithOptions(ctx, method, path, body, requestOptions{})
}

type requestOptions struct {
	contentType     string
	accept          string
	apiVersion      string
	omitAPIVersion  bool
	suppressPreview bool
	foundryFeatures string
	headers         http.Header
}

type rawRequestBody struct {
	reader        io.Reader
	contentLength int64
}

func (c *Client) doWithOptions(
	ctx context.Context,
	method string,
	path string,
	body interface{},
	options requestOptions,
) (*http.Response, error) {
	requestURL, err := url.Parse(c.endpoint + path)
	if err != nil {
		return nil, fmt.Errorf("failed to build request URL: %w", err)
	}
	if !options.omitAPIVersion {
		query := requestURL.Query()
		requestAPIVersion := options.apiVersion
		if requestAPIVersion == "" {
			requestAPIVersion = apiVersion
		}
		query.Set("api-version", requestAPIVersion)
		requestURL.RawQuery = query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		if raw, ok := body.(rawRequestBody); ok {
			bodyReader = raw.reader
		} else {
			data, err := json.Marshal(body)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal request body: %w", err)
			}
			bodyReader = bytes.NewReader(data)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	token, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		contentType := options.contentType
		if contentType == "" {
			contentType = "application/json"
		}
		req.Header.Set("Content-Type", contentType)
	}
	if options.accept != "" {
		req.Header.Set("Accept", options.accept)
	}
	for name, values := range options.headers {
		if strings.EqualFold(name, "Authorization") {
			return nil, errs.Security("Foundry request options must not override Authorization")
		}
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}
	if raw, ok := body.(rawRequestBody); ok {
		req.ContentLength = raw.contentLength
	}
	if options.foundryFeatures != "" {
		req.Header.Set("Foundry-Features", options.foundryFeatures)
	} else if c.allowPreview && !options.suppressPreview {
		req.Header.Set("Foundry-Features", previewHeader)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return resp, err
	}
	if resp == nil {
		return nil, fmt.Errorf("Foundry HTTP client returned a nil response")
	}
	if err := validateResponseDestination(req, resp); err != nil {
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		return nil, err
	}
	return resp, nil
}

func validateResponseDestination(request *http.Request, response *http.Response) error {
	if response.Request == nil || response.Request.URL == nil {
		return errs.Security("Foundry HTTP client response omitted its request destination")
	}
	if request.URL.String() != response.Request.URL.String() {
		return errs.Security(
			"Foundry HTTP client changed request destination from %q to %q",
			request.URL.String(),
			response.Request.URL.String(),
		)
	}
	return nil
}

func readBody(resp *http.Response) ([]byte, error) {
	if resp.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, nil
}

// ProbeContext verifies authentication and project data-plane access.
func (c *Client) ProbeContext(ctx context.Context) error {
	resp, err := c.do(ctx, http.MethodGet, "/agents?limit=1", nil)
	if err != nil {
		return errs.FoundryWrap(err, "Foundry data-plane probe failed")
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return errs.FoundryWrap(err, "failed to read Foundry data-plane probe response")
	}
	if resp.StatusCode != http.StatusOK {
		return httpx.ResponseError("Foundry", "data-plane probe", resp, data)
	}
	return nil
}

// WaitUntilReady blocks until a newly created project is visible on the data plane.
func (c *Client) WaitUntilReady(timeout, interval time.Duration) error {
	return c.WaitUntilReadyContext(context.Background(), timeout, interval)
}

func (c *Client) WaitUntilReadyContext(ctx context.Context, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := c.do(ctx, http.MethodGet, "/agents?limit=1", nil)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errs.IsAuthenticationOrAuthorization(err) {
				return err
			}
			if time.Now().After(deadline) {
				return errs.Transient("project not visible on the data plane after %ds: %v", int(timeout.Seconds()), err)
			}
			if err := sleepContext(ctx, interval); err != nil {
				return err
			}
			continue
		}
		data, readErr := readBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return errs.FoundryWrap(readErr, "failed to read project readiness response")
		}
		if resp.StatusCode == http.StatusOK {
			return nil
		}
		if resp.StatusCode == http.StatusNotFound || isProjectMissingText(string(data)) {
			if time.Now().After(deadline) {
				return errs.Transient(
					"project not visible on the data plane after %ds; status %d: %s%s",
					int(timeout.Seconds()),
					resp.StatusCode,
					string(data),
					httpx.Diagnostics(resp),
				)
			}
			if err := sleepContext(ctx, interval); err != nil {
				return err
			}
			continue
		}
		return httpx.ResponseError("Foundry", "project readiness check", resp, data)
	}
}

// Upsert creates a new immutable agent version.
func (c *Client) Upsert(name, model, instructions string, description string, tools []interface{}, raiPolicyID string) (*AgentResult, error) {
	return c.UpsertContext(context.Background(), name, model, instructions, description, tools, raiPolicyID)
}

func (c *Client) UpsertContext(ctx context.Context, name, model, instructions, description string, tools []interface{}, raiPolicyID string) (*AgentResult, error) {
	definition := map[string]interface{}{
		"kind":         "prompt",
		"model":        model,
		"instructions": instructions,
	}
	if len(tools) > 0 {
		definition["tools"] = tools
	}
	if raiPolicyID != "" {
		definition["rai_config"] = map[string]interface{}{"rai_policy_name": raiPolicyID}
	}
	return c.UpsertDefinitionContext(ctx, name, description, definition)
}

// UpsertDefinitionContext creates an immutable agent version from a complete
// prompt-agent definition.
func (c *Client) UpsertDefinitionContext(
	ctx context.Context,
	name string,
	description string,
	definition map[string]interface{},
	metadata ...map[string]string,
) (*AgentResult, error) {
	if definition == nil {
		return nil, errs.Config("agent definition is required")
	}
	body := map[string]interface{}{"definition": definition}
	if description != "" {
		body["description"] = description
	}
	if len(metadata) > 0 && len(metadata[0]) > 0 {
		body["metadata"] = metadata[0]
	}

	resp, err := c.do(ctx, http.MethodPost, agentPath(name)+"/versions", body)
	if err != nil {
		wrapped := errs.FoundryWrap(err, "failed to create agent %q version", name)
		if errs.IsAuthenticationOrAuthorization(err) {
			return nil, wrapped
		}
		return nil, errs.AmbiguousMutation(wrapped)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read create response for agent %q", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", fmt.Sprintf("create agent %q version", name), resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}

	var result struct {
		ID      string          `json:"id"`
		Name    string          `json:"name"`
		Version json.RawMessage `json:"version"`
		Status  string          `json:"status"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse create response for agent %q", name),
		)
	}
	version, err := parseVersion(result.Version)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "create response for agent %q contained an invalid version", name),
		)
	}
	return &AgentResult{ID: result.ID, Name: result.Name, Version: version, Status: result.Status}, nil
}

// GetAgent returns nil when the agent does not exist.
func (c *Client) GetAgent(name string) (*Agent, error) {
	return c.GetAgentContext(context.Background(), name)
}

func (c *Client) GetAgentContext(ctx context.Context, name string) (*Agent, error) {
	resp, err := c.do(ctx, http.MethodGet, agentPath(name), nil)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get agent %q", name)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read agent %q response", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("Foundry", fmt.Sprintf("get agent %q", name), resp, data)
	}
	var agent Agent
	if err := json.Unmarshal(data, &agent); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse agent %q response", name)
	}
	return &agent, nil
}

// GetAgentVersion returns nil when the requested version does not exist.
func (c *Client) GetAgentVersionContext(ctx context.Context, name, version string) (*AgentVersion, error) {
	resp, err := c.do(ctx, http.MethodGet, agentPath(name)+"/versions/"+url.PathEscape(version), nil)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get agent %q version %s", name, version)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read agent %q version %s response", name, version)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("Foundry", fmt.Sprintf("get agent %q version %s", name, version), resp, data)
	}
	var result AgentVersion
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse agent %q version %s response", name, version)
	}
	return &result, nil
}

// GetLatestVersion returns the latest version string for an agent, or "" if absent.
func (c *Client) GetLatestVersion(name string) (string, error) {
	agent, err := c.GetAgent(name)
	if err != nil || agent == nil {
		return "", err
	}
	if agent.Versions.Latest.Version == "" {
		return "", errs.Foundry("agent %q response contained no latest version", name)
	}
	return agent.Versions.Latest.Version, nil
}

// ListVersionDetails returns every non-draft version for an agent.
func (c *Client) ListVersionDetailsContext(ctx context.Context, name string) ([]AgentVersion, error) {
	return c.ListVersionDetailsWithDraftsContext(ctx, name, false)
}

// ListVersionDetailsWithDraftsContext returns every version, optionally
// including preview draft versions.
func (c *Client) ListVersionDetailsWithDraftsContext(
	ctx context.Context,
	name string,
	includeDrafts bool,
) ([]AgentVersion, error) {
	var versions []AgentVersion
	values := url.Values{}
	if includeDrafts {
		values.Set("include_drafts", "true")
	}
	seen := make(map[string]struct{})
	for {
		path := agentPath(name) + "/versions"
		if encoded := values.Encode(); encoded != "" {
			path += "?" + encoded
		}
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to list versions for agent %q", name)
		}
		data, readErr := readBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read versions response for agent %q", name)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("Foundry", fmt.Sprintf("list versions for agent %q", name), resp, data)
		}
		var page struct {
			Data    []AgentVersion `json:"data"`
			HasMore bool           `json:"has_more"`
			LastID  string         `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse versions response for agent %q", name)
		}
		versions = append(versions, page.Data...)
		if !page.HasMore || page.LastID == "" {
			break
		}
		if _, duplicate := seen[page.LastID]; duplicate {
			return nil, errs.Foundry(
				"agent %q version pagination repeated cursor %q",
				name,
				page.LastID,
			)
		}
		seen[page.LastID] = struct{}{}
		values.Set("after", page.LastID)
	}
	return versions, nil
}

// ListVersions preserves the original version-string API.
func (c *Client) ListVersions(name string) ([]string, error) {
	details, err := c.ListVersionDetailsContext(context.Background(), name)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(details))
	for _, detail := range details {
		versions = append(versions, detail.Version)
	}
	return versions, nil
}

// DeleteAgent deletes the whole agent. Idempotent.
func (c *Client) DeleteAgent(name string, force bool) (bool, error) {
	return c.DeleteAgentContext(context.Background(), name, force)
}

func (c *Client) DeleteAgentContext(ctx context.Context, name string, force bool) (bool, error) {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s?force=%t", agentPath(name), force), nil)
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to delete agent %q", name)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.FoundryWrap(readErr, "failed to read delete response for agent %q", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	return false, httpx.ResponseError("Foundry", fmt.Sprintf("delete agent %q", name), resp, data)
}

// DeleteVersion deletes a single agent version. Idempotent.
func (c *Client) DeleteVersion(name, version string, force bool) error {
	return c.DeleteVersionContext(context.Background(), name, version, force)
}

func (c *Client) DeleteVersionContext(ctx context.Context, name, version string, force bool) error {
	resp, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("%s/versions/%s?force=%t", agentPath(name), url.PathEscape(version), force),
		nil,
	)
	if err != nil {
		return errs.FoundryWrap(err, "failed to delete agent %q version %s", name, version)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return errs.FoundryWrap(readErr, "failed to read delete response for agent %q version %s", name, version)
	}
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return httpx.ResponseError("Foundry", fmt.Sprintf("delete agent %q version %s", name, version), resp, data)
}

// PruneVersions deletes every version except the latest.
func (c *Client) PruneVersions(name string, force bool) ([]string, error) {
	return c.PruneVersionsKeepContext(context.Background(), name, 1, force)
}

// PlanPruneContext returns the latest version and versions that exceed
// retention, while protecting every version currently receiving traffic.
func (c *Client) PlanPruneContext(ctx context.Context, name string, keep int) (string, []string, error) {
	return c.PlanPrunePreservingActiveContext(ctx, name, keep)
}

// PlanPrunePreservingActiveContext retains the newest requested versions plus
// any older version that currently receives endpoint traffic.
func (c *Client) PlanPrunePreservingActiveContext(
	ctx context.Context,
	name string,
	keep int,
) (string, []string, error) {
	if keep < 1 {
		return "", nil, errs.Config("--keep must be at least 1")
	}
	agent, err := c.GetAgentContext(ctx, name)
	if err != nil || agent == nil {
		return "", nil, err
	}
	versions, err := c.ListVersionDetailsContext(ctx, name)
	if err != nil {
		return "", nil, err
	}
	latest := agent.Versions.Latest.Version
	if latest == "" {
		latest = newestAgentVersion(versions)
		if latest == "" {
			return "", nil, errs.Foundry(
				"cannot safely plan pruning for agent %q: no latest or listed version was found",
				name,
			)
		}
	}
	var selector *VersionSelector
	if agent.AgentEndpoint != nil {
		selector = agent.AgentEndpoint.VersionSelector
	}
	resolution := ResolveVersionSelector(selector, latest)
	if resolution.IsMalformed() {
		return "", nil, errs.Foundry(
			"cannot safely plan pruning for agent %q: malformed agent version selector: %s",
			name,
			resolution.Problem,
		)
	}
	protected := append([]string{latest}, resolution.ActiveVersions...)
	removed, err := PlanVersionRetention(versions, keep, protected...)
	if err != nil {
		return "", nil, err
	}
	return latest, removed, nil
}

func newestAgentVersion(versions []AgentVersion) string {
	if len(versions) == 0 {
		return ""
	}
	newest := versions[0]
	for _, version := range versions[1:] {
		if version.CreatedAt > newest.CreatedAt {
			newest = version
		}
	}
	return newest.Version
}

// PlanVersionRetention returns versions safe to remove. It keeps the newest
// requested count and all explicitly protected versions without mutating input.
func PlanVersionRetention(
	versions []AgentVersion,
	keep int,
	protectedVersions ...string,
) ([]string, error) {
	if keep < 1 {
		return nil, errs.Config("--keep must be at least 1")
	}
	ordered := append([]AgentVersion(nil), versions...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].CreatedAt > ordered[j].CreatedAt
	})
	protected := make(map[string]struct{}, len(protectedVersions)+keep)
	for _, version := range protectedVersions {
		if version != "" {
			protected[version] = struct{}{}
		}
	}
	for i := 0; i < keep && i < len(ordered); i++ {
		protected[ordered[i].Version] = struct{}{}
	}
	removed := make([]string, 0, len(ordered))
	for _, version := range ordered {
		if _, retain := protected[version.Version]; !retain {
			removed = append(removed, version.Version)
		}
	}
	return removed, nil
}

// PruneVersionsKeepContext retains the newest requested number of versions.
func (c *Client) PruneVersionsKeepContext(ctx context.Context, name string, keep int, force bool) ([]string, error) {
	_, planned, err := c.PlanPruneContext(ctx, name, keep)
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, version := range planned {
		if err := c.DeleteVersionContext(ctx, name, version, force); err != nil {
			return removed, err
		}
		removed = append(removed, version)
	}
	return removed, nil
}

// Disable suspends an agent.
func (c *Client) Disable(name string) error {
	return c.DisableContext(context.Background(), name)
}

func (c *Client) DisableContext(ctx context.Context, name string) error {
	return c.lifecycleAction(ctx, name, "disable")
}

// Enable resumes a suspended agent.
func (c *Client) Enable(name string) error {
	return c.EnableContext(context.Background(), name)
}

func (c *Client) EnableContext(ctx context.Context, name string) error {
	return c.lifecycleAction(ctx, name, "enable")
}

func (c *Client) lifecycleAction(ctx context.Context, name, action string) error {
	resp, err := c.do(ctx, http.MethodPost, agentPath(name)+":"+action, nil)
	if err != nil {
		wrapped := errs.FoundryWrap(err, "failed to %s agent %q", action, name)
		if errs.IsAuthenticationOrAuthorization(err) ||
			errs.IsKind(err, "config") ||
			errs.IsKind(err, "security") {
			return wrapped
		}
		return errs.AmbiguousMutation(wrapped)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read %s response for agent %q", action, name),
		)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	responseErr := httpx.ResponseError(
		"Foundry",
		fmt.Sprintf("%s agent %q", action, name),
		resp,
		data,
	)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return errs.AmbiguousMutation(responseErr)
	}
	return responseErr
}

// InvokePromptContext sends one Responses API message to a prompt agent.
func (c *Client) InvokePromptContext(ctx context.Context, name, prompt string) (*InvocationResult, error) {
	return c.InvokePromptVersionContext(ctx, name, "", prompt)
}

// InvokePromptVersionContext sends one Responses API message to a specific prompt-agent version.
func (c *Client) InvokePromptVersionContext(ctx context.Context, name, version, prompt string) (*InvocationResult, error) {
	return c.InvokePromptVersionWithInputsContext(ctx, name, version, prompt, nil)
}

// InvokePromptVersionWithInputsContext sends one Responses API message with
// optional runtime structured inputs.
func (c *Client) InvokePromptVersionWithInputsContext(
	ctx context.Context,
	name string,
	version string,
	prompt string,
	structuredInputs map[string]interface{},
) (*InvocationResult, error) {
	return c.InvokePromptVersionWithOptionsContext(ctx, name, version, prompt, InvocationOptions{
		StructuredInputs: structuredInputs,
	})
}

func (c *Client) InvokePromptVersionWithOptionsContext(
	ctx context.Context,
	name string,
	version string,
	prompt string,
	options InvocationOptions,
) (*InvocationResult, error) {
	reference := map[string]interface{}{
		"type": "agent_reference",
		"name": name,
	}
	if version != "" {
		reference["version"] = version
	}
	body := map[string]interface{}{
		"agent_reference": reference,
		"input": []interface{}{
			map[string]interface{}{"role": "user", "content": prompt},
		},
	}
	if len(options.StructuredInputs) > 0 {
		body["structured_inputs"] = options.StructuredInputs
	}
	return c.invokeResponsesPathWithOptions(
		ctx, name, "/openai/v1/responses", body, false, true, options.MemoryUserID,
	)
}

func (c *Client) ContinuePromptVersionWithApprovalsContext(
	ctx context.Context,
	name string,
	version string,
	previousResponseID string,
	decisions []MCPApprovalDecision,
	options InvocationOptions,
) (*InvocationResult, error) {
	reference := map[string]interface{}{
		"type": "agent_reference",
		"name": name,
	}
	if version != "" {
		reference["version"] = version
	}
	body, err := approvalContinuationBody(previousResponseID, decisions)
	if err != nil {
		return nil, err
	}
	body["agent_reference"] = reference
	return c.invokeResponsesPathWithOptions(
		ctx, name, "/openai/v1/responses", body, false, true, options.MemoryUserID,
	)
}

func (c *Client) invokeResponsesPath(
	ctx context.Context,
	name string,
	path string,
	body any,
	suppressPreview bool,
) (*InvocationResult, error) {
	return c.invokeResponsesPathWithMemoryUser(ctx, name, path, body, suppressPreview, "")
}

func (c *Client) invokeResponsesPathWithMemoryUser(
	ctx context.Context,
	name string,
	path string,
	body any,
	suppressPreview bool,
	memoryUserID string,
) (*InvocationResult, error) {
	return c.invokeResponsesPathWithOptions(
		ctx,
		name,
		path,
		body,
		suppressPreview,
		false,
		memoryUserID,
	)
}

func (c *Client) invokeResponsesPathWithOptions(
	ctx context.Context,
	name string,
	path string,
	body any,
	suppressPreview bool,
	omitAPIVersion bool,
	memoryUserID string,
) (*InvocationResult, error) {
	if strings.ContainsAny(memoryUserID, "\r\n") {
		return nil, errs.Config("memory user id must not contain line breaks")
	}
	headers := make(http.Header)
	if memoryUserID != "" {
		headers.Set("x-memory-user-id", memoryUserID)
	}
	resp, err := c.doWithOptions(ctx, http.MethodPost, path, body, requestOptions{
		suppressPreview: suppressPreview,
		omitAPIVersion:  omitAPIVersion,
		headers:         headers,
	})
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to invoke agent %q", name)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read invocation response for agent %q", name)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpx.ResponseError("Foundry", fmt.Sprintf("invoke agent %q", name), resp, data)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse invocation response for agent %q", name)
	}
	id, _ := payload["id"].(string)
	output, _ := payload["output_text"].(string)
	if output == "" {
		output = findOutputText(payload["output"])
	}
	approvals, err := findMCPApprovalRequests(payload["output"])
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse MCP approval requests for agent %q", name)
	}
	if output == "" && len(approvals) == 0 {
		return nil, errs.Foundry("invocation response for agent %q contained no text output", name)
	}
	return &InvocationResult{
		ID: id, OutputText: output, ApprovalRequests: approvals,
	}, nil
}

func approvalContinuationBody(
	previousResponseID string,
	decisions []MCPApprovalDecision,
) (map[string]interface{}, error) {
	previousResponseID = strings.TrimSpace(previousResponseID)
	if previousResponseID == "" {
		return nil, errs.Config("MCP approval continuation requires previous_response_id")
	}
	if len(decisions) == 0 {
		return nil, errs.Config("MCP approval continuation requires at least one decision")
	}
	input := make([]interface{}, 0, len(decisions))
	seen := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		id := strings.TrimSpace(decision.ApprovalRequestID)
		if id == "" || strings.ContainsAny(id, "\r\n\x00") {
			return nil, errs.Config("MCP approval decision has an invalid approval request id")
		}
		if _, found := seen[id]; found {
			return nil, errs.Config("MCP approval request %q was decided more than once", id)
		}
		seen[id] = struct{}{}
		input = append(input, map[string]interface{}{
			"type":                "mcp_approval_response",
			"approval_request_id": id,
			"approve":             decision.Approve,
		})
	}
	return map[string]interface{}{
		"previous_response_id": previousResponseID,
		"input":                input,
	}, nil
}

func findMCPApprovalRequests(value interface{}) ([]MCPApprovalRequest, error) {
	items, _ := value.([]interface{})
	result := make([]MCPApprovalRequest, 0)
	for _, raw := range items {
		item, _ := raw.(map[string]interface{})
		if item["type"] != "mcp_approval_request" {
			continue
		}
		request := MCPApprovalRequest{
			ID:          strings.TrimSpace(stringValue(item, "id")),
			ServerLabel: strings.TrimSpace(stringValue(item, "server_label")),
			ToolName:    strings.TrimSpace(stringValue(item, "name")),
			Arguments:   item["arguments"],
		}
		if request.ID == "" || request.ServerLabel == "" || request.ToolName == "" {
			return nil, fmt.Errorf("approval item is missing id, server_label, or name")
		}
		result = append(result, request)
	}
	return result, nil
}

func stringValue(source map[string]interface{}, key string) string {
	value, _ := source[key].(string)
	return value
}

func findOutputText(value interface{}) string {
	switch typed := value.(type) {
	case []interface{}:
		for index := len(typed) - 1; index >= 0; index-- {
			item, ok := typed[index].(map[string]interface{})
			if !ok ||
				!strings.EqualFold(stringValue(item, "type"), "message") ||
				!strings.EqualFold(stringValue(item, "role"), "assistant") {
				continue
			}
			if found := findOutputText(item["content"]); found != "" {
				return found
			}
		}
		for _, item := range typed {
			if found := findOutputText(item); found != "" {
				return found
			}
		}
	case map[string]interface{}:
		if text, ok := typed["text"].(string); ok && text != "" {
			return text
		}
		for _, key := range []string{"content", "output", "message"} {
			if found := findOutputText(typed[key]); found != "" {
				return found
			}
		}
	}
	return ""
}

func agentPath(name string) string {
	return "/agents/" + url.PathEscape(name)
}

func parseVersion(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("version is missing")
	}
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	switch version := value.(type) {
	case string:
		if version == "" {
			return "", fmt.Errorf("version is empty")
		}
		return version, nil
	case json.Number:
		return version.String(), nil
	default:
		return "", fmt.Errorf("version has unsupported type %T", value)
	}
}

func isProjectMissingText(body string) bool {
	return strings.Contains(strings.ToLower(body), "project does not exist")
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
