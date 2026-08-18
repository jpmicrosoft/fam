package foundry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

const skillsPreviewHeader = "Skills=V1Preview"

type SkillDetails struct {
	ID             string `json:"id,omitempty" yaml:"id,omitempty"`
	Name           string `json:"name" yaml:"name"`
	Description    string `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedAt      int64  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	DefaultVersion string `json:"default_version,omitempty" yaml:"defaultVersion,omitempty"`
	LatestVersion  string `json:"latest_version,omitempty" yaml:"latestVersion,omitempty"`
}

type SkillVersion struct {
	ID          string `json:"id,omitempty" yaml:"id,omitempty"`
	SkillID     string `json:"skill_id,omitempty" yaml:"skillId,omitempty"`
	Name        string `json:"name,omitempty" yaml:"name,omitempty"`
	Version     string `json:"version" yaml:"version"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	CreatedAt   int64  `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
}

type SkillInlineContent struct {
	Description   string            `json:"description"`
	Instructions  string            `json:"instructions"`
	License       string            `json:"license,omitempty"`
	Compatibility string            `json:"compatibility,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	AllowedTools  []string          `json:"allowed_tools,omitempty"`
}

type SkillFile struct {
	Name string
	Data []byte
}

func skillPath(name string) string {
	return "/skills/" + url.PathEscape(name)
}

func skillRequestOptions() requestOptions {
	return requestOptions{foundryFeatures: skillsPreviewHeader}
}

func (c *Client) GetSkillContext(ctx context.Context, name string) (*SkillDetails, error) {
	var result SkillDetails
	found, err := c.skillJSON(ctx, http.MethodGet, skillPath(name), nil, "get skill", &result)
	if err != nil || !found {
		return nil, err
	}
	return &result, nil
}

func (c *Client) ListSkillsContext(ctx context.Context) ([]SkillDetails, error) {
	var result []SkillDetails
	path := "/skills?limit=100"
	for {
		var page struct {
			Data    []SkillDetails `json:"data"`
			HasMore bool           `json:"has_more"`
			LastID  string         `json:"last_id"`
		}
		found, err := c.skillJSON(ctx, http.MethodGet, path, nil, "list skills", &page)
		if err != nil {
			return nil, err
		}
		if !found {
			return result, nil
		}
		result = append(result, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return result, nil
		}
		path = "/skills?limit=100&after=" + url.QueryEscape(page.LastID)
	}
}

func (c *Client) CreateSkillInlineContext(
	ctx context.Context,
	name string,
	content SkillInlineContent,
	setDefault bool,
) (*SkillVersion, error) {
	body := map[string]interface{}{"inline_content": content, "default": setDefault}
	var result SkillVersion
	_, err := c.skillMutationJSON(
		ctx,
		http.MethodPost,
		skillPath(name)+"/versions",
		body,
		"create skill version",
		&result,
	)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) CreateSkillFromFilesContext(
	ctx context.Context,
	name string,
	files []SkillFile,
	setDefault bool,
) (*SkillVersion, error) {
	if len(files) == 0 {
		return nil, errs.Config("skill upload requires at least one file")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("default", fmt.Sprintf("%t", setDefault)); err != nil {
		return nil, errs.FoundryWrap(err, "failed to encode skill default field")
	}
	for _, file := range files {
		if file.Name == "" {
			return nil, errs.Config("skill upload file name must not be empty")
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
			"name": "files", "filename": file.Name,
		}))
		header.Set("Content-Type", "application/octet-stream")
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to encode skill file %q", file.Name)
		}
		if _, err := part.Write(file.Data); err != nil {
			return nil, errs.FoundryWrap(err, "failed to encode skill file %q", file.Name)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, errs.FoundryWrap(err, "failed to finish skill upload")
	}
	options := skillRequestOptions()
	options.contentType = writer.FormDataContentType()
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		skillPath(name)+"/versions",
		rawRequestBody{reader: bytes.NewReader(body.Bytes()), contentLength: int64(body.Len())},
		options,
	)
	if err != nil {
		return nil, errs.AmbiguousMutation(errs.FoundryWrap(err, "failed to upload skill %q", name))
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(errs.FoundryWrap(readErr, "failed to read skill upload response"))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", "upload skill", resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result SkillVersion
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(errs.FoundryWrap(err, "failed to parse skill upload response"))
	}
	return &result, nil
}

func (c *Client) SetSkillDefaultContext(
	ctx context.Context,
	name string,
	version string,
) (*SkillDetails, error) {
	var result SkillDetails
	_, err := c.skillMutationJSON(
		ctx,
		http.MethodPost,
		skillPath(name),
		map[string]interface{}{"default_version": version},
		"set skill default version",
		&result,
	)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteSkillContext(ctx context.Context, name string) (bool, error) {
	return c.skillDelete(ctx, skillPath(name), "delete skill")
}

func (c *Client) ListSkillVersionsContext(
	ctx context.Context,
	name string,
) ([]SkillVersion, error) {
	var result []SkillVersion
	base := skillPath(name) + "/versions"
	path := base + "?limit=100"
	for {
		var page struct {
			Data    []SkillVersion `json:"data"`
			HasMore bool           `json:"has_more"`
			LastID  string         `json:"last_id"`
		}
		found, err := c.skillJSON(ctx, http.MethodGet, path, nil, "list skill versions", &page)
		if err != nil {
			return nil, err
		}
		if !found {
			return result, nil
		}
		result = append(result, page.Data...)
		if !page.HasMore || page.LastID == "" {
			return result, nil
		}
		path = base + "?limit=100&after=" + url.QueryEscape(page.LastID)
	}
}

func (c *Client) GetSkillVersionContext(
	ctx context.Context,
	name string,
	version string,
) (*SkillVersion, error) {
	var result SkillVersion
	found, err := c.skillJSON(
		ctx,
		http.MethodGet,
		skillPath(name)+"/versions/"+url.PathEscape(version),
		nil,
		"get skill version",
		&result,
	)
	if err != nil || !found {
		return nil, err
	}
	return &result, nil
}

func (c *Client) DeleteSkillVersionContext(
	ctx context.Context,
	name string,
	version string,
) (bool, error) {
	return c.skillDelete(
		ctx,
		skillPath(name)+"/versions/"+url.PathEscape(version),
		"delete skill version",
	)
}

func (c *Client) DownloadSkillContext(
	ctx context.Context,
	name string,
	version string,
) ([]byte, error) {
	path := skillPath(name)
	if version == "" {
		path += "/content"
	} else {
		path += "/versions/" + url.PathEscape(version) + "/content"
	}
	options := skillRequestOptions()
	options.accept = "application/zip"
	resp, err := c.doWithOptions(ctx, http.MethodGet, path, nil, options)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to download skill %q", name)
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024*1024))
	if readErr != nil {
		return nil, errs.FoundryWrap(readErr, "failed to read skill %q download", name)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("Foundry", "download skill", resp, data)
	}
	return data, nil
}

func (c *Client) skillJSON(
	ctx context.Context,
	method string,
	path string,
	body interface{},
	action string,
	target interface{},
) (bool, error) {
	resp, err := c.doWithOptions(ctx, method, path, body, skillRequestOptions())
	if err != nil {
		return false, errs.FoundryWrap(err, "failed to %s", action)
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.FoundryWrap(readErr, "failed to read %s response", action)
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, httpx.ResponseError("Foundry", action, resp, data)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, errs.FoundryWrap(err, "failed to parse %s response", action)
	}
	return true, nil
}

func (c *Client) skillMutationJSON(
	ctx context.Context,
	method string,
	path string,
	body interface{},
	action string,
	target interface{},
) (bool, error) {
	resp, err := c.doWithOptions(ctx, method, path, body, skillRequestOptions())
	if err != nil {
		return false, errs.AmbiguousMutation(errs.FoundryWrap(err, "failed to %s", action))
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(errs.FoundryWrap(readErr, "failed to read %s response", action))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", action, resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return false, errs.AmbiguousMutation(responseErr)
		}
		return false, responseErr
	}
	if target != nil {
		if err := json.Unmarshal(data, target); err != nil {
			return false, errs.AmbiguousMutation(errs.FoundryWrap(err, "failed to parse %s response", action))
		}
	}
	return true, nil
}

func (c *Client) skillDelete(ctx context.Context, path string, action string) (bool, error) {
	resp, err := c.doWithOptions(ctx, http.MethodDelete, path, nil, skillRequestOptions())
	if err != nil {
		return false, errs.AmbiguousMutation(errs.FoundryWrap(err, "failed to %s", action))
	}
	defer resp.Body.Close()
	data, readErr := readBody(resp)
	if readErr != nil {
		return false, errs.AmbiguousMutation(errs.FoundryWrap(readErr, "failed to read %s response", action))
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return true, nil
	}
	responseErr := httpx.ResponseError("Foundry", action, resp, data)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return false, errs.AmbiguousMutation(responseErr)
	}
	return false, responseErr
}
