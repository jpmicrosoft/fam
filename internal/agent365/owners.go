package agent365

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

// DirectoryObject is a minimal directory object returned by owners/sponsors.
type DirectoryObject struct {
	ODataType   string `json:"@odata.type,omitempty" yaml:"odataType,omitempty"`
	ID          string `json:"id" yaml:"id"`
	DisplayName string `json:"displayName,omitempty" yaml:"displayName,omitempty"`
}

func validateDirectoryObject(obj DirectoryObject) error {
	if !guidPattern.MatchString(strings.TrimSpace(obj.ID)) {
		return fmt.Errorf("directory object ID is not a GUID")
	}
	return nil
}

// ListBlueprintOwners returns the owners of a blueprint application.
func (c *Client) ListBlueprintOwners(
	ctx context.Context,
	objectID string,
) ([]DirectoryObject, error) {
	result, _, err := c.ListBlueprintOwnersPaginated(
		ctx,
		objectID,
		PaginationOptions{},
	)
	return result, err
}

// ListBlueprintOwnersPaginated returns blueprint owners with bounded continuation support.
func (c *Client) ListBlueprintOwnersPaginated(
	ctx context.Context,
	objectID string,
	opts PaginationOptions,
) ([]DirectoryObject, bool, error) {
	objectID, err := ValidateGUID(objectID, "blueprint object ID")
	if err != nil {
		return nil, false, err
	}
	query := url.Values{}
	query.Set("$select", "id,displayName")
	path := fmt.Sprintf(
		"/v1.0/applications/%s/microsoft.graph.agentIdentityBlueprint/owners?%s",
		url.PathEscape(objectID),
		query.Encode(),
	)
	return c.listDirectoryObjects(
		ctx,
		path,
		"list blueprint owners",
		"AgentIdentityBlueprint.Read.All",
		opts,
	)
}

// ListBlueprintSponsors returns the sponsors of a blueprint application.
func (c *Client) ListBlueprintSponsors(
	ctx context.Context,
	objectID string,
) ([]DirectoryObject, error) {
	result, _, err := c.ListBlueprintSponsorsPaginated(
		ctx,
		objectID,
		PaginationOptions{},
	)
	return result, err
}

// ListBlueprintSponsorsPaginated returns blueprint sponsors with bounded continuation support.
func (c *Client) ListBlueprintSponsorsPaginated(
	ctx context.Context,
	objectID string,
	opts PaginationOptions,
) ([]DirectoryObject, bool, error) {
	objectID, err := ValidateGUID(objectID, "blueprint object ID")
	if err != nil {
		return nil, false, err
	}
	query := url.Values{}
	query.Set("$select", "id,displayName")
	path := fmt.Sprintf(
		"/v1.0/applications/%s/microsoft.graph.agentIdentityBlueprint/sponsors?%s",
		url.PathEscape(objectID),
		query.Encode(),
	)
	return c.listDirectoryObjects(
		ctx,
		path,
		"list blueprint sponsors",
		"Application.Read.All",
		opts,
	)
}

func (c *Client) listDirectoryObjects(
	ctx context.Context,
	path string,
	operation string,
	requiredPermission string,
	opts PaginationOptions,
) ([]DirectoryObject, bool, error) {
	raw, truncated, err := c.paginatedGetJSON(ctx, path, operation, opts)
	if err != nil {
		if errs.KindOf(err) == "authorization" {
			return nil, false, errs.WithNextSteps(
				err,
				fmt.Sprintf("Grant and consent the Microsoft Graph %s permission to the calling identity.", requiredPermission),
				"Verify --tenant-id selects the correct tenant.",
			)
		}
		return nil, false, err
	}
	objects := make([]DirectoryObject, 0, len(raw))
	for _, r := range raw {
		var obj DirectoryObject
		if err := json.Unmarshal(r, &obj); err != nil {
			return nil, false, errs.FoundryWrap(err, "invalid directory object JSON in %s", operation)
		}
		if err := validateDirectoryObject(obj); err != nil {
			return nil, false, errs.FoundryWrap(err, "Microsoft Graph returned invalid directory object in %s", operation)
		}
		objects = append(objects, obj)
	}
	return objects, truncated, nil
}
