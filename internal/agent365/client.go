// Package agent365 provides read-only access to Microsoft Entra Agent ID
// blueprints through documented Microsoft Graph v1.0 APIs.
package agent365

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	Endpoint             = "https://graph.microsoft.com"
	Scope                = "https://graph.microsoft.com/.default"
	maxResponseBodyBytes = 2 << 20
	maxListResults       = 100
)

var guidPattern = regexp.MustCompile(
	`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`,
)

// HTTPClient is the transport dependency used by Client.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Blueprint is the non-secret Agent ID blueprint metadata used by the CLI.
// Credential properties are intentionally excluded from both the Graph query
// and this model.
type Blueprint struct {
	ObjectID                  string                   `json:"objectId" yaml:"objectId"`
	AppID                     string                   `json:"appId" yaml:"appId"`
	DisplayName               string                   `json:"displayName" yaml:"displayName"`
	Description               string                   `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedDateTime           string                   `json:"createdDateTime,omitempty" yaml:"createdDateTime,omitempty"`
	DisabledByMicrosoftStatus string                   `json:"disabledByMicrosoftStatus,omitempty" yaml:"disabledByMicrosoftStatus,omitempty"`
	ManagerApplications       []string                 `json:"managerApplications" yaml:"managerApplications"`
	PublisherDomain           string                   `json:"publisherDomain,omitempty" yaml:"publisherDomain,omitempty"`
	SignInAudience            string                   `json:"signInAudience,omitempty" yaml:"signInAudience,omitempty"`
	Tags                      []string                 `json:"tags" yaml:"tags"`
	RequiredResourceAccess    []RequiredResourceAccess `json:"requiredResourceAccess" yaml:"requiredResourceAccess"`
}

// UnmarshalJSON maps Graph's generic directory object "id" property to the
// explicit ObjectID field used in CLI output.
func (b *Blueprint) UnmarshalJSON(data []byte) error {
	type blueprintWire Blueprint
	var decoded struct {
		blueprintWire
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*b = Blueprint(decoded.blueprintWire)
	b.ObjectID = decoded.ID
	return nil
}

// RequiredResourceAccess describes one resource API requested by a blueprint.
type RequiredResourceAccess struct {
	ResourceAppID  string           `json:"resourceAppId" yaml:"resourceAppId"`
	ResourceAccess []ResourceAccess `json:"resourceAccess" yaml:"resourceAccess"`
}

// ResourceAccess is one delegated scope or application role requested by a blueprint.
type ResourceAccess struct {
	ID   string `json:"id" yaml:"id"`
	Type string `json:"type" yaml:"type"`
}

// InheritablePermission describes permission scopes that identities created
// from a blueprint may inherit without additional consent.
type InheritablePermission struct {
	ResourceAppID     string            `json:"resourceAppId" yaml:"resourceAppId"`
	InheritableScopes InheritableScopes `json:"inheritableScopes" yaml:"inheritableScopes"`
}

// InheritableScopes preserves all documented inheritance modes.
type InheritableScopes struct {
	ODataType string   `json:"@odata.type,omitempty" yaml:"odataType,omitempty"`
	Kind      string   `json:"kind" yaml:"kind"`
	Scopes    []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// BlueprintList is one bounded Graph page.
type BlueprintList struct {
	Blueprints []Blueprint `json:"blueprints" yaml:"blueprints"`
	Count      int         `json:"count" yaml:"count"`
	Truncated  bool        `json:"truncated" yaml:"truncated"`
}

// BlueprintSelector identifies a blueprint unambiguously.
type BlueprintSelector struct {
	AppID    string
	ObjectID string
}

// Client reads Agent ID blueprint metadata from Microsoft Graph.
type Client struct {
	credential azcore.TokenCredential
	httpClient HTTPClient
}

// NewClient creates a read-only Microsoft Graph client.
func NewClient(credential azcore.TokenCredential, httpClient HTTPClient) (*Client, error) {
	if credential == nil {
		return nil, errs.Auth("Agent 365 Microsoft Graph credential is required")
	}
	if httpClient == nil {
		return nil, errs.Config("Agent 365 Microsoft Graph HTTP client is required")
	}
	return &Client{credential: credential, httpClient: httpClient}, nil
}

// ValidateSelector requires exactly one valid blueprint application or object ID.
func ValidateSelector(selector BlueprintSelector) (BlueprintSelector, error) {
	selector.AppID = strings.TrimSpace(selector.AppID)
	selector.ObjectID = strings.TrimSpace(selector.ObjectID)
	if selector.AppID == "" && selector.ObjectID == "" {
		return BlueprintSelector{}, errs.Config(
			"either --blueprint-id or --blueprint-object-id is required",
		)
	}
	if selector.AppID != "" && selector.ObjectID != "" {
		return BlueprintSelector{}, errs.Config(
			"--blueprint-id and --blueprint-object-id are mutually exclusive",
		)
	}
	if selector.AppID != "" {
		normalized, err := ValidateGUID(selector.AppID, "blueprint application ID")
		if err != nil {
			return BlueprintSelector{}, err
		}
		selector.AppID = normalized
	}
	if selector.ObjectID != "" {
		normalized, err := ValidateGUID(selector.ObjectID, "blueprint object ID")
		if err != nil {
			return BlueprintSelector{}, err
		}
		selector.ObjectID = normalized
	}
	return selector, nil
}

// ValidateGUID validates and normalizes a Microsoft Entra identifier.
func ValidateGUID(value, label string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if !guidPattern.MatchString(value) {
		return "", errs.Config("%s must be a valid GUID", label)
	}
	return value, nil
}

// ListBlueprints returns at most 100 blueprints, matching Graph's documented
// maximum page size.
func (c *Client) ListBlueprints(ctx context.Context, limit int) (BlueprintList, error) {
	return c.ListBlueprintsPaginated(ctx, limit, PaginationOptions{})
}

// ListBlueprintsPaginated returns blueprints with bounded continuation support.
func (c *Client) ListBlueprintsPaginated(
	ctx context.Context,
	pageSize int,
	opts PaginationOptions,
) (BlueprintList, error) {
	if err := ValidateListLimit(pageSize); err != nil {
		return BlueprintList{}, err
	}
	query := url.Values{}
	query.Set("$select", blueprintSelect)
	query.Set("$top", fmt.Sprintf("%d", pageSize))
	raw, truncated, err := c.paginatedGetJSON(
		ctx,
		"/v1.0/applications/microsoft.graph.agentIdentityBlueprint?"+query.Encode(),
		"list Agent ID blueprints",
		opts,
	)
	if err != nil {
		return BlueprintList{}, wrapBlueprintForbidden(err)
	}
	blueprints := make([]Blueprint, 0, len(raw))
	for _, item := range raw {
		var blueprint Blueprint
		if err := json.Unmarshal(item, &blueprint); err != nil {
			return BlueprintList{}, errs.FoundryWrap(
				err,
				"Microsoft Graph returned invalid Agent ID blueprint JSON",
			)
		}
		if err := validateBlueprint(blueprint); err != nil {
			return BlueprintList{}, errs.FoundryWrap(
				err,
				"Microsoft Graph returned invalid Agent ID blueprint metadata",
			)
		}
		blueprints = append(blueprints, blueprint)
	}
	return BlueprintList{
		Blueprints: blueprints,
		Count:      len(blueprints),
		Truncated:  truncated,
	}, nil
}

// ValidateListLimit checks Graph's documented page-size boundary.
func ValidateListLimit(limit int) error {
	if limit < 1 || limit > maxListResults {
		return errs.Config(
			"Agent 365 list limit must be between 1 and %d",
			maxListResults,
		)
	}
	return nil
}

// GetBlueprint resolves a blueprint by its application ID or object ID.
func (c *Client) GetBlueprint(
	ctx context.Context,
	selector BlueprintSelector,
) (*Blueprint, error) {
	selector, err := ValidateSelector(selector)
	if err != nil {
		return nil, err
	}
	if selector.ObjectID != "" {
		query := url.Values{}
		query.Set("$select", blueprintSelect)
		var blueprint Blueprint
		path := fmt.Sprintf(
			"/v1.0/applications/%s/microsoft.graph.agentIdentityBlueprint?%s",
			url.PathEscape(selector.ObjectID),
			query.Encode(),
		)
		if err := c.getJSON(ctx, path, "get Agent ID blueprint", &blueprint); err != nil {
			return nil, wrapBlueprintForbidden(err)
		}
		if err := validateBlueprint(blueprint); err != nil {
			return nil, errs.FoundryWrap(
				err,
				"Microsoft Graph returned invalid Agent ID blueprint metadata",
			)
		}
		return &blueprint, nil
	}

	query := url.Values{}
	query.Set("$filter", fmt.Sprintf("appId eq '%s'", selector.AppID))
	query.Set("$select", blueprintSelect)
	query.Set("$top", "2")
	var page blueprintPage
	if err := c.getJSON(
		ctx,
		"/v1.0/applications/microsoft.graph.agentIdentityBlueprint?"+query.Encode(),
		"find Agent ID blueprint",
		&page,
	); err != nil {
		return nil, wrapBlueprintForbidden(err)
	}
	switch len(page.Value) {
	case 0:
		return nil, errs.NotFound(
			"Agent ID blueprint with application ID %q was not found",
			selector.AppID,
		)
	case 1:
		if err := validateBlueprint(page.Value[0]); err != nil {
			return nil, errs.FoundryWrap(
				err,
				"Microsoft Graph returned invalid Agent ID blueprint metadata",
			)
		}
		return &page.Value[0], nil
	default:
		return nil, errs.Conflict(
			"Microsoft Graph returned multiple Agent ID blueprints for application ID %q",
			selector.AppID,
		)
	}
}

// ListInheritablePermissions gets the documented inheritance configuration for
// one blueprint object ID.
func (c *Client) ListInheritablePermissions(
	ctx context.Context,
	objectID string,
) ([]InheritablePermission, error) {
	objectID, err := ValidateGUID(objectID, "blueprint object ID")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("$select", "resourceAppId,inheritableScopes")
	var page permissionPage
	path := fmt.Sprintf(
		"/v1.0/applications/%s/microsoft.graph.agentIdentityBlueprint/inheritablePermissions?%s",
		url.PathEscape(objectID),
		query.Encode(),
	)
	if err := c.getJSON(
		ctx,
		path,
		"list Agent ID blueprint inheritable permissions",
		&page,
	); err != nil {
		return nil, wrapBlueprintForbidden(err)
	}
	for _, permission := range page.Value {
		if !guidPattern.MatchString(strings.TrimSpace(permission.ResourceAppID)) {
			return nil, errs.Foundry(
				"Microsoft Graph returned an invalid inheritable permission resource application ID",
			)
		}
	}
	return page.Value, nil
}

func (c *Client) getJSON(
	ctx context.Context,
	path string,
	operation string,
	result interface{},
) error {
	requestURL := Endpoint + path
	parsed, err := url.Parse(requestURL)
	if err != nil {
		return errs.SecurityWrap(err, "failed to construct Microsoft Graph request")
	}
	if parsed.Scheme != "https" || parsed.Hostname() != "graph.microsoft.com" ||
		parsed.Port() != "" || parsed.User != nil {
		return errs.Security("refusing untrusted Microsoft Graph endpoint %q", parsed.Redacted())
	}
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{Scope},
	})
	if err != nil {
		return errs.WithNextSteps(
			errs.AuthWrap(err, "failed to acquire Microsoft Graph token for Agent 365"),
			"Sign in to the Microsoft Entra tenant that owns the Agent ID blueprint.",
			"Use --tenant-id when the blueprint is in a different tenant.",
		)
	}
	if strings.TrimSpace(token.Token) == "" {
		return errs.Auth("Microsoft Graph credential returned an empty Agent 365 access token")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return errs.FoundryWrap(err, "failed to create Microsoft Graph request")
	}

	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return errs.FoundryWrap(err, "Microsoft Graph %s request failed", operation)
	}
	if response == nil {
		return errs.Foundry("Microsoft Graph %s returned a nil response", operation)
	}
	body, readErr := readBoundedBody(response.Body, maxResponseBodyBytes)
	if response.Body != nil {
		_ = response.Body.Close()
	}
	if readErr != nil {
		return errs.FoundryWrap(readErr, "failed to read Microsoft Graph %s response", operation)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		base := httpx.ResponseError("Microsoft Graph", operation, response, body)
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return errs.WithNextSteps(
				base,
				"Sign in to the Microsoft Entra tenant that owns the Agent ID blueprint.",
				"Verify --tenant-id and that the selected credential can request Microsoft Graph tokens.",
			)
		case http.StatusForbidden:
			return errs.WithNextSteps(
				base,
				"Grant and consent the least-privileged Microsoft Graph permission required by this Agent 365 operation.",
				"For delegated Agent ID administration, verify the calling user has any required Microsoft Entra role.",
				"Verify --tenant-id selects the tenant that owns the requested Agent ID resource.",
			)
		default:
			return base
		}
	}
	if len(body) == 0 {
		return errs.Foundry("Microsoft Graph %s response omitted its body", operation)
	}
	if err := json.Unmarshal(body, result); err != nil {
		return errs.FoundryWrap(err, "Microsoft Graph %s returned invalid JSON", operation)
	}
	return nil
}

func wrapBlueprintForbidden(err error) error {
	if errs.KindOf(err) == "authorization" {
		return errs.WithNextSteps(
			err,
			"Grant and consent the Microsoft Graph AgentIdentityBlueprint.Read.All permission to the calling identity.",
			"For delegated non-owner access, assign the least-privileged Agent ID Administrator role.",
			"Verify --tenant-id selects the tenant that owns the blueprint.",
		)
	}
	return err
}

func validateBlueprint(blueprint Blueprint) error {
	if !guidPattern.MatchString(strings.TrimSpace(blueprint.ObjectID)) {
		return fmt.Errorf("blueprint object ID is not a GUID")
	}
	if !guidPattern.MatchString(strings.TrimSpace(blueprint.AppID)) {
		return fmt.Errorf("blueprint application ID is not a GUID")
	}
	if strings.TrimSpace(blueprint.DisplayName) == "" {
		return fmt.Errorf("blueprint display name is empty")
	}
	for _, manager := range blueprint.ManagerApplications {
		if !guidPattern.MatchString(strings.TrimSpace(manager)) {
			return fmt.Errorf("blueprint manager application ID is not a GUID")
		}
	}
	for _, resource := range blueprint.RequiredResourceAccess {
		if !guidPattern.MatchString(strings.TrimSpace(resource.ResourceAppID)) {
			return fmt.Errorf("required resource application ID is not a GUID")
		}
		for _, access := range resource.ResourceAccess {
			if !guidPattern.MatchString(strings.TrimSpace(access.ID)) {
				return fmt.Errorf("required resource access ID is not a GUID")
			}
			if access.Type != "Scope" && access.Type != "Role" {
				return fmt.Errorf(
					"required resource access type %q is not Scope or Role",
					access.Type,
				)
			}
		}
	}
	return nil
}

func readBoundedBody(body io.Reader, limit int64) ([]byte, error) {
	if body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeded %d bytes", limit)
	}
	return data, nil
}

const blueprintSelect = "id,appId,displayName,description,createdDateTime," +
	"disabledByMicrosoftStatus,managerApplications,publisherDomain," +
	"signInAudience,tags,requiredResourceAccess"

type blueprintPage struct {
	Value    []Blueprint `json:"value"`
	NextLink string      `json:"@odata.nextLink"`
}

type permissionPage struct {
	Value []InheritablePermission `json:"value"`
}
