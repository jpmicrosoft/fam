package agent365

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// ResolvedPermission is a friendly representation of one permission.
type ResolvedPermission struct {
	ResourceAppID         string `json:"resourceAppId" yaml:"resourceAppId"`
	ResourceDisplayName   string `json:"resourceDisplayName" yaml:"resourceDisplayName"`
	PermissionID          string `json:"permissionId" yaml:"permissionId"`
	PermissionValue       string `json:"permissionValue" yaml:"permissionValue"`
	PermissionDisplayName string `json:"permissionDisplayName,omitempty" yaml:"permissionDisplayName,omitempty"`
	PermissionType        string `json:"permissionType" yaml:"permissionType"` // Scope or Role
}

// resourceServicePrincipal is the minimal SP shape for permission resolution.
type resourceServicePrincipal struct {
	ID                     string       `json:"id"`
	DisplayName            string       `json:"displayName"`
	AppID                  string       `json:"appId"`
	OAuth2PermissionScopes []oauthScope `json:"oauth2PermissionScopes"`
	AppRoles               []appRole    `json:"appRoles"`
}

type oauthScope struct {
	ID                      string `json:"id"`
	Value                   string `json:"value"`
	AdminConsentDisplayName string `json:"adminConsentDisplayName"`
}

type appRole struct {
	ID          string `json:"id"`
	Value       string `json:"value"`
	DisplayName string `json:"displayName"`
}

// ResolvePermissions maps a blueprint's requiredResourceAccess entries to
// friendly display names by looking up the resource service principal.
// This is an explicit method that requires Application.Read.All; permission
// errors are surfaced clearly and never silently degraded.
func (c *Client) ResolvePermissions(
	ctx context.Context,
	blueprint Blueprint,
) ([]ResolvedPermission, error) {
	var resolved []ResolvedPermission

	for _, resource := range blueprint.RequiredResourceAccess {
		if !guidPattern.MatchString(strings.TrimSpace(resource.ResourceAppID)) {
			return nil, errs.Config("invalid resource application ID %q", resource.ResourceAppID)
		}

		// Look up the resource service principal by appId.
		query := url.Values{}
		query.Set("$filter", fmt.Sprintf("appId eq '%s'", resource.ResourceAppID))
		query.Set("$select", "id,displayName,appId,oauth2PermissionScopes,appRoles")
		query.Set("$top", "2")

		var page struct {
			Value []resourceServicePrincipal `json:"value"`
		}
		if err := c.getJSON(
			ctx,
			"/v1.0/servicePrincipals?"+query.Encode(),
			"resolve resource service principal",
			&page,
		); err != nil {
			if errs.KindOf(err) == "authorization" {
				return nil, errs.WithNextSteps(
					err,
					"Grant and consent the Microsoft Graph Application.Read.All permission to the calling identity.",
					"Verify --tenant-id selects the correct tenant.",
				)
			}
			return nil, err
		}
		if len(page.Value) == 0 {
			return nil, errs.NotFound(
				"resource service principal with appId %q was not found",
				resource.ResourceAppID,
			)
		}

		sp := page.Value[0]
		scopeMap := make(map[string]oauthScope)
		for _, s := range sp.OAuth2PermissionScopes {
			scopeMap[strings.ToLower(s.ID)] = s
		}
		roleMap := make(map[string]appRole)
		for _, r := range sp.AppRoles {
			roleMap[strings.ToLower(r.ID)] = r
		}

		for _, access := range resource.ResourceAccess {
			rp := ResolvedPermission{
				ResourceAppID:       resource.ResourceAppID,
				ResourceDisplayName: sp.DisplayName,
				PermissionID:        access.ID,
				PermissionType:      access.Type,
			}
			key := strings.ToLower(access.ID)
			if access.Type == "Scope" {
				if s, ok := scopeMap[key]; ok {
					rp.PermissionValue = s.Value
					rp.PermissionDisplayName = s.AdminConsentDisplayName
				}
			} else if access.Type == "Role" {
				if r, ok := roleMap[key]; ok {
					rp.PermissionValue = r.Value
					rp.PermissionDisplayName = r.DisplayName
				}
			}
			resolved = append(resolved, rp)
		}
	}

	return resolved, nil
}
