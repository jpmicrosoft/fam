package agent365

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// BlueprintPrincipal is the non-secret blueprint service principal metadata.
type BlueprintPrincipal struct {
	ID                        string   `json:"id" yaml:"id"`
	DisplayName               string   `json:"displayName" yaml:"displayName"`
	AppID                     string   `json:"appId,omitempty" yaml:"appId,omitempty"`
	CreatedDateTime           string   `json:"createdDateTime,omitempty" yaml:"createdDateTime,omitempty"`
	DisabledByMicrosoftStatus string   `json:"disabledByMicrosoftStatus,omitempty" yaml:"disabledByMicrosoftStatus,omitempty"`
	ServicePrincipalType      string   `json:"servicePrincipalType,omitempty" yaml:"servicePrincipalType,omitempty"`
	Tags                      []string `json:"tags,omitempty" yaml:"tags,omitempty"`
}

const blueprintPrincipalSelect = "id,displayName,appId,createdDateTime," +
	"disabledByMicrosoftStatus,servicePrincipalType,tags"

// BlueprintPrincipalList is a bounded list result.
type BlueprintPrincipalList struct {
	Principals []BlueprintPrincipal `json:"principals" yaml:"principals"`
	Count      int                  `json:"count" yaml:"count"`
	Truncated  bool                 `json:"truncated" yaml:"truncated"`
}

func validateBlueprintPrincipal(bp BlueprintPrincipal) error {
	if !guidPattern.MatchString(strings.TrimSpace(bp.ID)) {
		return fmt.Errorf("blueprint principal object ID is not a GUID")
	}
	if strings.TrimSpace(bp.DisplayName) == "" {
		return fmt.Errorf("blueprint principal display name is empty")
	}
	if strings.TrimSpace(bp.AppID) != "" &&
		!guidPattern.MatchString(strings.TrimSpace(bp.AppID)) {
		return fmt.Errorf("blueprint principal application ID is not a GUID")
	}
	return nil
}

// ListBlueprintPrincipals returns blueprint service principals with optional pagination.
func (c *Client) ListBlueprintPrincipals(
	ctx context.Context,
	limit int,
	opts PaginationOptions,
) (BlueprintPrincipalList, error) {
	if err := ValidateListLimit(limit); err != nil {
		return BlueprintPrincipalList{}, err
	}
	query := url.Values{}
	query.Set("$select", blueprintPrincipalSelect)
	query.Set("$top", fmt.Sprintf("%d", limit))
	path := "/v1.0/servicePrincipals/microsoft.graph.agentIdentityBlueprintPrincipal?" + query.Encode()

	raw, truncated, err := c.paginatedGetJSON(ctx, path, "list blueprint principals", opts)
	if err != nil {
		return BlueprintPrincipalList{}, wrapBlueprintPrincipalForbidden(err)
	}
	principals := make([]BlueprintPrincipal, 0, len(raw))
	for _, r := range raw {
		var bp BlueprintPrincipal
		if err := json.Unmarshal(r, &bp); err != nil {
			return BlueprintPrincipalList{}, errs.FoundryWrap(err, "invalid blueprint principal JSON")
		}
		if err := validateBlueprintPrincipal(bp); err != nil {
			return BlueprintPrincipalList{}, errs.FoundryWrap(err, "Microsoft Graph returned invalid blueprint principal")
		}
		principals = append(principals, bp)
	}
	return BlueprintPrincipalList{
		Principals: principals,
		Count:      len(principals),
		Truncated:  truncated,
	}, nil
}

// GetBlueprintPrincipal retrieves a single blueprint principal by object ID.
func (c *Client) GetBlueprintPrincipal(
	ctx context.Context,
	objectID string,
) (*BlueprintPrincipal, error) {
	objectID, err := ValidateGUID(objectID, "blueprint principal object ID")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("$select", blueprintPrincipalSelect)
	path := fmt.Sprintf(
		"/v1.0/servicePrincipals/%s/microsoft.graph.agentIdentityBlueprintPrincipal?%s",
		url.PathEscape(objectID),
		query.Encode(),
	)
	var bp BlueprintPrincipal
	if err := c.getJSON(ctx, path, "get blueprint principal", &bp); err != nil {
		return nil, wrapBlueprintPrincipalForbidden(err)
	}
	if err := validateBlueprintPrincipal(bp); err != nil {
		return nil, errs.FoundryWrap(err, "Microsoft Graph returned invalid blueprint principal")
	}
	return &bp, nil
}

func wrapBlueprintPrincipalForbidden(err error) error {
	if errs.KindOf(err) == "authorization" {
		return errs.WithNextSteps(
			err,
			"Grant and consent the Microsoft Graph AgentIdentityBlueprintPrincipal.Read.All permission to the calling identity.",
			"Verify --tenant-id selects the correct tenant.",
		)
	}
	return err
}
