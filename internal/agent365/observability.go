package agent365

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const (
	// ObservabilityDisplayName is the exact display name of the Agent365Observability SP.
	ObservabilityDisplayName = "Agent365Observability"
	// ObservabilityAppRoleID is the fixed app role ID for observability assignment.
	ObservabilityAppRoleID = "8f71190c-00c8-461d-a63b-f74abde9ba52"
)

// ObservabilityServicePrincipal is a minimal service principal.
type ObservabilityServicePrincipal struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	AppID       string `json:"appId"`
}

// AppRoleAssignment represents one app role assignment.
type AppRoleAssignment struct {
	ID                   string `json:"id" yaml:"id"`
	AppRoleID            string `json:"appRoleId" yaml:"appRoleId"`
	PrincipalID          string `json:"principalId" yaml:"principalId"`
	PrincipalDisplayName string `json:"principalDisplayName,omitempty" yaml:"principalDisplayName,omitempty"`
	ResourceID           string `json:"resourceId" yaml:"resourceId"`
	ResourceDisplayName  string `json:"resourceDisplayName,omitempty" yaml:"resourceDisplayName,omitempty"`
}

// ResolveObservabilityServicePrincipal finds the Agent365Observability SP by
// exact displayName filter.
func (c *Client) ResolveObservabilityServicePrincipal(
	ctx context.Context,
) (*ObservabilityServicePrincipal, error) {
	query := url.Values{}
	query.Set("$filter", fmt.Sprintf("displayName eq '%s'", ObservabilityDisplayName))
	query.Set("$select", "id,displayName,appId")
	query.Set("$top", "2")

	var page struct {
		Value []ObservabilityServicePrincipal `json:"value"`
	}
	if err := c.getJSON(
		ctx,
		"/v1.0/servicePrincipals?"+query.Encode(),
		"resolve observability service principal",
		&page,
	); err != nil {
		return nil, wrapObservabilityForbidden(err)
	}
	if len(page.Value) == 0 {
		return nil, errs.NotFound(
			"Agent365Observability service principal was not found in this tenant",
		)
	}
	if len(page.Value) > 1 {
		return nil, errs.Conflict(
			"Microsoft Graph returned multiple service principals named %q",
			ObservabilityDisplayName,
		)
	}
	sp := page.Value[0]
	if !guidPattern.MatchString(strings.TrimSpace(sp.ID)) {
		return nil, errs.Foundry("Agent365Observability service principal has an invalid ID")
	}
	if !guidPattern.MatchString(strings.TrimSpace(sp.AppID)) {
		return nil, errs.Foundry("Agent365Observability service principal has an invalid application ID")
	}
	if sp.DisplayName != ObservabilityDisplayName {
		return nil, errs.Conflict(
			"Microsoft Graph returned service principal name %q instead of %q",
			sp.DisplayName,
			ObservabilityDisplayName,
		)
	}
	return &sp, nil
}

// ListAppRoleAssignments returns the app role assignments for the given
// service principal (agent identity). This is a read-only GET operation.
func (c *Client) ListAppRoleAssignments(
	ctx context.Context,
	servicePrincipalID string,
) ([]AppRoleAssignment, error) {
	servicePrincipalID, err := ValidateGUID(servicePrincipalID, "service principal ID")
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("$select", "id,appRoleId,principalId,principalDisplayName,resourceId,resourceDisplayName")
	path := fmt.Sprintf(
		"/v1.0/servicePrincipals/%s/appRoleAssignments?%s",
		url.PathEscape(servicePrincipalID),
		query.Encode(),
	)
	var page struct {
		Value []AppRoleAssignment `json:"value"`
	}
	if err := c.getJSON(ctx, path, "list app role assignments", &page); err != nil {
		return nil, wrapObservabilityForbidden(err)
	}
	for _, assignment := range page.Value {
		for _, candidate := range []struct {
			label string
			value string
		}{
			{label: "app role ID", value: assignment.AppRoleID},
			{label: "principal ID", value: assignment.PrincipalID},
			{label: "resource ID", value: assignment.ResourceID},
		} {
			if !guidPattern.MatchString(strings.TrimSpace(candidate.value)) {
				return nil, errs.Foundry(
					"Microsoft Graph returned an invalid observability %s",
					candidate.label,
				)
			}
		}
	}
	return page.Value, nil
}

// HasObservabilityAssignment checks whether the given agent identity SP has
// the fixed observability app role assigned to it from the given resource SP.
func (c *Client) HasObservabilityAssignment(
	ctx context.Context,
	agentIdentityID string,
	observabilityResourceID string,
) (bool, *AppRoleAssignment, error) {
	assignments, err := c.ListAppRoleAssignments(ctx, agentIdentityID)
	if err != nil {
		return false, nil, err
	}
	for i := range assignments {
		a := &assignments[i]
		if strings.EqualFold(a.AppRoleID, ObservabilityAppRoleID) &&
			strings.EqualFold(a.ResourceID, observabilityResourceID) {
			return true, a, nil
		}
	}
	return false, nil, nil
}

func wrapObservabilityForbidden(err error) error {
	if errs.KindOf(err) == "authorization" {
		return errs.WithNextSteps(
			err,
			"Grant and consent the Microsoft Graph Application.Read.All permission to the calling identity.",
			"Verify --tenant-id selects the correct tenant.",
		)
	}
	return err
}
