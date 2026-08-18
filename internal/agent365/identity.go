package agent365

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// AgentIdentity is the non-secret Agent ID identity (service principal) metadata.
type AgentIdentity struct {
	ID                        string   `json:"id" yaml:"id"`
	DisplayName               string   `json:"displayName" yaml:"displayName"`
	AppID                     string   `json:"appId,omitempty" yaml:"appId,omitempty"`
	CreatedDateTime           string   `json:"createdDateTime,omitempty" yaml:"createdDateTime,omitempty"`
	CreatedByAppID            string   `json:"createdByAppId,omitempty" yaml:"createdByAppId,omitempty"`
	AgentIdentityBlueprintID  string   `json:"agentIdentityBlueprintId,omitempty" yaml:"agentIdentityBlueprintId,omitempty"`
	AccountEnabled            *bool    `json:"accountEnabled,omitempty" yaml:"accountEnabled,omitempty"`
	DisabledByMicrosoftStatus string   `json:"disabledByMicrosoftStatus,omitempty" yaml:"disabledByMicrosoftStatus,omitempty"`
	ServicePrincipalType      string   `json:"servicePrincipalType,omitempty" yaml:"servicePrincipalType,omitempty"`
	Tags                      []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

const identitySelect = "id,displayName,appId,createdDateTime,createdByAppId," +
	"agentIdentityBlueprintId,accountEnabled,disabledByMicrosoftStatus," +
	"servicePrincipalType,tags"

// AgentIdentityList is a bounded list result.
type AgentIdentityList struct {
	Identities []AgentIdentity `json:"identities" yaml:"identities"`
	Count      int             `json:"count" yaml:"count"`
	Truncated  bool            `json:"truncated" yaml:"truncated"`
}

func validateAgentIdentity(ai AgentIdentity) error {
	if !guidPattern.MatchString(strings.TrimSpace(ai.ID)) {
		return fmt.Errorf("agent identity object ID is not a GUID")
	}
	if strings.TrimSpace(ai.DisplayName) == "" {
		return fmt.Errorf("agent identity display name is empty")
	}
	for _, candidate := range []struct {
		label string
		value string
	}{
		{label: "application ID", value: ai.AppID},
		{label: "creator application ID", value: ai.CreatedByAppID},
		{label: "identity blueprint ID", value: ai.AgentIdentityBlueprintID},
	} {
		if strings.TrimSpace(candidate.value) != "" &&
			!guidPattern.MatchString(strings.TrimSpace(candidate.value)) {
			return fmt.Errorf("agent identity %s is not a GUID", candidate.label)
		}
	}
	return nil
}

// ListAgentIdentities returns agent identities with optional pagination.
func (c *Client) ListAgentIdentities(
	ctx context.Context,
	limit int,
	opts PaginationOptions,
) (AgentIdentityList, error) {
	if err := ValidateListLimit(limit); err != nil {
		return AgentIdentityList{}, err
	}
	query := url.Values{}
	query.Set("$select", identitySelect)
	query.Set("$top", fmt.Sprintf("%d", limit))
	path := "/v1.0/servicePrincipals/microsoft.graph.agentIdentity?" + query.Encode()

	raw, truncated, err := c.paginatedGetJSON(ctx, path, "list agent identities", opts)
	if err != nil {
		return AgentIdentityList{}, wrapIdentityForbidden(err)
	}
	identities := make([]AgentIdentity, 0, len(raw))
	for _, r := range raw {
		var ai AgentIdentity
		if err := json.Unmarshal(r, &ai); err != nil {
			return AgentIdentityList{}, errs.FoundryWrap(err, "invalid agent identity JSON")
		}
		if err := validateAgentIdentity(ai); err != nil {
			return AgentIdentityList{}, errs.FoundryWrap(err, "Microsoft Graph returned invalid agent identity")
		}
		identities = append(identities, ai)
	}
	return AgentIdentityList{
		Identities: identities,
		Count:      len(identities),
		Truncated:  truncated,
	}, nil
}

// GetAgentIdentity retrieves a single agent identity by object ID.
func (c *Client) GetAgentIdentity(
	ctx context.Context,
	objectID string,
) (*AgentIdentity, error) {
	objectID, err := ValidateGUID(objectID, "agent identity object ID")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("$select", identitySelect)
	path := fmt.Sprintf(
		"/v1.0/servicePrincipals/%s/microsoft.graph.agentIdentity?%s",
		url.PathEscape(objectID),
		query.Encode(),
	)
	var ai AgentIdentity
	if err := c.getJSON(ctx, path, "get agent identity", &ai); err != nil {
		return nil, wrapIdentityForbidden(err)
	}
	if err := validateAgentIdentity(ai); err != nil {
		return nil, errs.FoundryWrap(err, "Microsoft Graph returned invalid agent identity")
	}
	return &ai, nil
}

// ListAgentIdentitiesByBlueprint filters identities by agentIdentityBlueprintId.
func (c *Client) ListAgentIdentitiesByBlueprint(
	ctx context.Context,
	blueprintID string,
	limit int,
	opts PaginationOptions,
) (AgentIdentityList, error) {
	blueprintID, err := ValidateGUID(blueprintID, "blueprint ID")
	if err != nil {
		return AgentIdentityList{}, err
	}
	if err := ValidateListLimit(limit); err != nil {
		return AgentIdentityList{}, err
	}
	query := url.Values{}
	query.Set("$select", identitySelect)
	query.Set("$top", fmt.Sprintf("%d", limit))
	query.Set("$filter", fmt.Sprintf("agentIdentityBlueprintId eq '%s'", blueprintID))
	path := "/v1.0/servicePrincipals/microsoft.graph.agentIdentity?" + query.Encode()

	raw, truncated, err := c.paginatedGetJSON(ctx, path, "list agent identities by blueprint", opts)
	if err != nil {
		return AgentIdentityList{}, wrapIdentityForbidden(err)
	}
	identities := make([]AgentIdentity, 0, len(raw))
	for _, r := range raw {
		var ai AgentIdentity
		if err := json.Unmarshal(r, &ai); err != nil {
			return AgentIdentityList{}, errs.FoundryWrap(err, "invalid agent identity JSON")
		}
		if err := validateAgentIdentity(ai); err != nil {
			return AgentIdentityList{}, errs.FoundryWrap(err, "Microsoft Graph returned invalid agent identity")
		}
		identities = append(identities, ai)
	}
	return AgentIdentityList{
		Identities: identities,
		Count:      len(identities),
		Truncated:  truncated,
	}, nil
}

func wrapIdentityForbidden(err error) error {
	if errs.KindOf(err) == "authorization" {
		return errs.WithNextSteps(
			err,
			"Grant and consent the Microsoft Graph AgentIdentity.Read.All permission to the calling identity.",
			"For delegated non-owner access, assign the Agent ID Administrator role.",
			"Verify --tenant-id selects the correct tenant.",
		)
	}
	return err
}
