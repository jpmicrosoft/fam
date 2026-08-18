package foundry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

// Toolbox is the logical Toolbox resource and its promoted default version.
type Toolbox struct {
	ID             string `json:"id,omitempty" yaml:"id,omitempty"`
	Name           string `json:"name" yaml:"name"`
	DefaultVersion string `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
}

// UnmarshalJSON accepts string, numeric, or expanded default_version shapes.
func (t *Toolbox) UnmarshalJSON(data []byte) error {
	type alias Toolbox
	var payload struct {
		alias
		DefaultVersion json.RawMessage `json:"default_version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	*t = Toolbox(payload.alias)
	version, err := parseToolboxVersionReference(payload.DefaultVersion)
	if err != nil {
		return err
	}
	t.DefaultVersion = version
	return nil
}

// ToolboxVersion is one immutable Toolbox version.
type ToolboxVersion struct {
	ID          string                 `json:"id,omitempty" yaml:"id,omitempty"`
	Name        string                 `json:"name,omitempty" yaml:"name,omitempty"`
	Version     string                 `json:"version" yaml:"version"`
	Description string                 `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedAt   int64                  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	Tools       []interface{}          `json:"tools,omitempty" yaml:"tools,omitempty"`
	Skills      []interface{}          `json:"skills,omitempty" yaml:"skills,omitempty"`
	Policies    map[string]interface{} `json:"policies,omitempty" yaml:"policies,omitempty"`
}

// UnmarshalJSON accepts the service's string or numeric version representation.
func (v *ToolboxVersion) UnmarshalJSON(data []byte) error {
	type alias ToolboxVersion
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
	*v = ToolboxVersion(payload.alias)
	v.Version = version
	return nil
}

func parseToolboxVersionReference(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if raw[0] != '{' {
		return parseVersion(raw)
	}
	var expanded struct {
		Version json.RawMessage `json:"version"`
	}
	if err := json.Unmarshal(raw, &expanded); err != nil {
		return "", err
	}
	return parseVersion(expanded.Version)
}

func toolboxPath(name string) string {
	return "/toolboxes/" + url.PathEscape(name)
}

// GetToolboxContext returns nil when the Toolbox does not exist.
func (c *Client) GetToolboxContext(ctx context.Context, name string) (*Toolbox, error) {
	resp, err := c.do(ctx, http.MethodGet, toolboxPath(name), nil)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get Toolbox %q", name)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read Toolbox %q response", name)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("Foundry", fmt.Sprintf("get Toolbox %q", name), resp, data)
	}
	var result Toolbox
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse Toolbox %q response", name)
	}
	if result.Name == "" {
		result.Name = name
	}
	return &result, nil
}

// GetToolboxVersionContext returns nil when the version does not exist.
func (c *Client) GetToolboxVersionContext(
	ctx context.Context,
	name string,
	version string,
) (*ToolboxVersion, error) {
	path := toolboxPath(name) + "/versions/" + url.PathEscape(version)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get Toolbox %q version %s", name, version)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read Toolbox %q version %s response", name, version)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get Toolbox %q version %s", name, version),
			resp,
			data,
		)
	}
	var result ToolboxVersion
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse Toolbox %q version %s response", name, version)
	}
	if result.Name == "" {
		result.Name = name
	}
	return &result, nil
}

// ListToolboxVersionsContext returns every immutable version.
func (c *Client) ListToolboxVersionsContext(
	ctx context.Context,
	name string,
) ([]ToolboxVersion, error) {
	var versions []ToolboxVersion
	path := toolboxPath(name) + "/versions"
	for {
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to list Toolbox %q versions", name)
		}
		data, readErr := readBody(resp)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read Toolbox %q versions response", name)
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError(
				"Foundry",
				fmt.Sprintf("list Toolbox %q versions", name),
				resp,
				data,
			)
		}
		var page struct {
			Data    []ToolboxVersion `json:"data"`
			HasMore bool             `json:"has_more"`
			LastID  string           `json:"last_id"`
		}
		if err := json.Unmarshal(data, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse Toolbox %q versions response", name)
		}
		versions = append(versions, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return versions, nil
		}
		path = toolboxPath(name) + "/versions?after=" + url.QueryEscape(page.LastID)
	}
}

// CreateToolboxVersionContext creates a new immutable Toolbox version.
func (c *Client) CreateToolboxVersionContext(
	ctx context.Context,
	name string,
	payload map[string]interface{},
	previewFeatures string,
) (*ToolboxVersion, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		toolboxPath(name)+"/versions",
		payload,
		requestOptions{foundryFeatures: previewFeatures},
	)
	if err != nil {
		wrapped := errs.FoundryWrap(err, "failed to create Toolbox %q version", name)
		if errs.IsAuthenticationOrAuthorization(err) {
			return nil, wrapped
		}
		return nil, errs.AmbiguousMutation(wrapped)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read create response for Toolbox %q", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("create Toolbox %q version", name),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result ToolboxVersion
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse create response for Toolbox %q", name),
		)
	}
	if result.Name == "" {
		result.Name = name
	}
	return &result, nil
}

// PromoteToolboxVersionContext changes the logical Toolbox default version.
func (c *Client) PromoteToolboxVersionContext(
	ctx context.Context,
	name string,
	version string,
) error {
	resp, err := c.do(
		ctx,
		http.MethodPatch,
		toolboxPath(name),
		map[string]interface{}{"default_version": version},
	)
	if err != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to promote Toolbox %q version %s", name, version),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read promote response for Toolbox %q", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("promote Toolbox %q version %s", name, version),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return errs.AmbiguousMutation(responseErr)
		}
		return responseErr
	}
	return nil
}

// DeleteToolboxVersionContext deletes one immutable version idempotently.
func (c *Client) DeleteToolboxVersionContext(
	ctx context.Context,
	name string,
	version string,
) (bool, error) {
	path := toolboxPath(name) + "/versions/" + url.PathEscape(version)
	resp, err := c.do(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to delete Toolbox %q version %s", name, version),
		)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read delete response for Toolbox %q version %s", name, version),
		)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	responseErr := httpx.ResponseError(
		"Foundry",
		fmt.Sprintf("delete Toolbox %q version %s", name, version),
		resp,
		data,
	)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return false, errs.AmbiguousMutation(responseErr)
	}
	return false, responseErr
}
