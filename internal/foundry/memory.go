package foundry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

const MemoryAPIVersion = "2025-11-15-preview"

type MemoryStoreDefinition struct {
	Kind           string                     `json:"kind" yaml:"kind"`
	ChatModel      string                     `json:"chat_model" yaml:"chatModel"`
	EmbeddingModel string                     `json:"embedding_model" yaml:"embeddingModel"`
	Options        *MemoryStoreDefaultOptions `json:"options,omitempty" yaml:"options,omitempty"`
}

type MemoryStoreDefaultOptions struct {
	UserProfileEnabled      bool   `json:"user_profile_enabled" yaml:"userProfileEnabled"`
	UserProfileDetails      string `json:"user_profile_details,omitempty" yaml:"userProfileDetails,omitempty"`
	ChatSummaryEnabled      bool   `json:"chat_summary_enabled" yaml:"chatSummaryEnabled"`
	ProceduralMemoryEnabled *bool  `json:"procedural_memory_enabled,omitempty" yaml:"proceduralMemoryEnabled,omitempty"`
	DefaultTTLSeconds       *int64 `json:"default_ttl_seconds,omitempty" yaml:"defaultTtlSeconds,omitempty"`
}

type MemoryStore struct {
	Object      string                `json:"object,omitempty" yaml:"object,omitempty"`
	ID          string                `json:"id" yaml:"id"`
	Name        string                `json:"name" yaml:"name"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Metadata    map[string]string     `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	Definition  MemoryStoreDefinition `json:"definition" yaml:"definition"`
	CreatedAt   int64                 `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	UpdatedAt   int64                 `json:"updated_at,omitempty" yaml:"updatedAt,omitempty"`
}

type MemoryItem struct {
	MemoryID  string `json:"memory_id" yaml:"memoryId"`
	UpdatedAt int64  `json:"updated_at,omitempty" yaml:"updatedAt,omitempty"`
	Scope     string `json:"scope" yaml:"scope"`
	Content   string `json:"content" yaml:"content"`
	Kind      string `json:"kind" yaml:"kind"`
}

type MemoryDeleteResult struct {
	Object  string `json:"object,omitempty" yaml:"object,omitempty"`
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	Name    string `json:"name,omitempty" yaml:"name,omitempty"`
	Deleted bool   `json:"deleted" yaml:"deleted"`
}

type MemoryScopeDeleteResult struct {
	Object  string `json:"object,omitempty" yaml:"object,omitempty"`
	Name    string `json:"name" yaml:"name"`
	Scope   string `json:"scope" yaml:"scope"`
	Deleted bool   `json:"deleted" yaml:"deleted"`
}

type MemoryUpdateResult struct {
	UpdateID     string                 `json:"update_id" yaml:"updateId"`
	Status       string                 `json:"status" yaml:"status"`
	SupersededBy string                 `json:"superseded_by,omitempty" yaml:"supersededBy,omitempty"`
	Result       map[string]interface{} `json:"result,omitempty" yaml:"result,omitempty"`
	Error        interface{}            `json:"error,omitempty" yaml:"error,omitempty"`
}

func (c *Client) CreateMemoryStoreContext(
	ctx context.Context,
	store MemoryStore,
) (*MemoryStore, error) {
	store.Definition.Kind = "default"
	return memoryMutation[MemoryStore](
		c,
		ctx,
		http.MethodPost,
		"/memory_stores",
		store,
		"create memory store "+store.Name,
	)
}

func (c *Client) UpdateMemoryStoreContext(
	ctx context.Context,
	name string,
	description string,
	metadata map[string]string,
) (*MemoryStore, error) {
	body := map[string]interface{}{}
	body["description"] = description
	if metadata != nil {
		body["metadata"] = metadata
	}
	return memoryMutation[MemoryStore](
		c,
		ctx,
		http.MethodPost,
		memoryStorePath(name),
		body,
		"update memory store "+name,
	)
}

func (c *Client) GetMemoryStoreContext(ctx context.Context, name string) (*MemoryStore, error) {
	return memoryRead[MemoryStore](
		c,
		ctx,
		http.MethodGet,
		memoryStorePath(name),
		nil,
		"get memory store "+name,
	)
}

func (c *Client) ListMemoryStoresContext(ctx context.Context) ([]MemoryStore, error) {
	var result []MemoryStore
	after := ""
	for {
		query := url.Values{"limit": []string{"100"}}
		if after != "" {
			query.Set("after", after)
		}
		var page struct {
			Data   []MemoryStore `json:"data"`
			LastID string        `json:"last_id"`
		}
		value, err := memoryRead[json.RawMessage](
			c,
			ctx,
			http.MethodGet,
			"/memory_stores?"+query.Encode(),
			nil,
			"list memory stores",
		)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(*value, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse memory store list")
		}
		result = append(result, page.Data...)
		if page.LastID == "" || page.LastID == after || len(page.Data) == 0 {
			return result, nil
		}
		after = page.LastID
	}
}

func (c *Client) DeleteMemoryStoreContext(
	ctx context.Context,
	name string,
) (*MemoryDeleteResult, error) {
	return memoryMutation[MemoryDeleteResult](
		c,
		ctx,
		http.MethodDelete,
		memoryStorePath(name),
		nil,
		"delete memory store "+name,
	)
}

func (c *Client) SearchMemoriesContext(
	ctx context.Context,
	name string,
	body map[string]interface{},
) (map[string]interface{}, error) {
	result, err := memoryRead[map[string]interface{}](
		c,
		ctx,
		http.MethodPost,
		memoryStorePath(name)+":search_memories",
		body,
		"search memory store "+name,
	)
	if err != nil {
		return nil, err
	}
	return *result, nil
}

func (c *Client) UpdateMemoriesContext(
	ctx context.Context,
	name string,
	body map[string]interface{},
	timeout time.Duration,
	interval time.Duration,
) (*MemoryUpdateResult, error) {
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		memoryStorePath(name)+":update_memories",
		body,
		memoryRequestOptions(),
	)
	if err != nil {
		return nil, memoryMutationFailure(err, "update memories in store %q", name)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read update memories response for store %q", name),
		)
	}
	if resp.StatusCode != http.StatusAccepted {
		responseErr := httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("update memories in store %q", name),
			resp,
			data,
		)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var submitted MemoryUpdateResult
	if len(data) > 0 {
		if err := json.Unmarshal(data, &submitted); err != nil {
			return nil, errs.AmbiguousMutation(
				errs.FoundryWrap(err, "failed to parse update memories response for store %q", name),
			)
		}
	}
	operationPath, err := c.memoryOperationPath(resp.Header.Get("Operation-Location"))
	if err != nil {
		if submitted.UpdateID == "" {
			return nil, errs.AmbiguousMutation(err)
		}
		operationPath = memoryStorePath(name) + "/updates/" + url.PathEscape(submitted.UpdateID)
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		status, err := memoryRead[MemoryUpdateResult](
			c,
			ctx,
			http.MethodGet,
			operationPath,
			nil,
			"get memory update status",
		)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(status.Status) {
		case "completed":
			return status, nil
		case "failed":
			return nil, errs.Foundry("memory update %q failed: %v", status.UpdateID, status.Error)
		case "superseded":
			return nil, errs.Foundry(
				"memory update %q was superseded by %q",
				status.UpdateID,
				status.SupersededBy,
			)
		}
		if time.Now().After(deadline) {
			return nil, errs.Foundry(
				"timed out waiting for memory update %q after %s",
				status.UpdateID,
				timeout,
			)
		}
		if err := sleepContext(ctx, interval); err != nil {
			return nil, err
		}
	}
}

func (c *Client) DeleteMemoryScopeContext(
	ctx context.Context,
	name string,
	scope string,
) (*MemoryScopeDeleteResult, error) {
	return memoryMutation[MemoryScopeDeleteResult](
		c,
		ctx,
		http.MethodPost,
		memoryStorePath(name)+":delete_scope",
		map[string]interface{}{"scope": scope},
		"delete memory scope "+scope,
	)
}

func (c *Client) CreateMemoryItemContext(
	ctx context.Context,
	name string,
	scope string,
	content string,
	kind string,
) (*MemoryItem, error) {
	return memoryMutation[MemoryItem](
		c,
		ctx,
		http.MethodPost,
		memoryStorePath(name)+"/items",
		map[string]interface{}{"scope": scope, "content": content, "kind": kind},
		"create memory item",
	)
}

func (c *Client) UpdateMemoryItemContext(
	ctx context.Context,
	name string,
	memoryID string,
	content string,
) (*MemoryItem, error) {
	return memoryMutation[MemoryItem](
		c,
		ctx,
		http.MethodPost,
		memoryItemPath(name, memoryID),
		map[string]interface{}{"content": content},
		"update memory item "+memoryID,
	)
}

func (c *Client) GetMemoryItemContext(
	ctx context.Context,
	name string,
	memoryID string,
) (*MemoryItem, error) {
	return memoryRead[MemoryItem](
		c,
		ctx,
		http.MethodGet,
		memoryItemPath(name, memoryID),
		nil,
		"get memory item "+memoryID,
	)
}

func (c *Client) ListMemoryItemsContext(
	ctx context.Context,
	name string,
	scope string,
	kind string,
) ([]MemoryItem, error) {
	var result []MemoryItem
	after := ""
	for {
		body := map[string]interface{}{
			"scope": scope,
			"limit": 100,
		}
		if kind != "" {
			body["kind"] = kind
		}
		if after != "" {
			body["after"] = after
		}
		raw, err := memoryRead[json.RawMessage](
			c,
			ctx,
			http.MethodPost,
			memoryStorePath(name)+"/items:list",
			body,
			"list memory items",
		)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data    []MemoryItem `json:"data"`
			LastID  string       `json:"last_id"`
			HasMore *bool        `json:"has_more"`
		}
		if err := json.Unmarshal(*raw, &page); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse memory item list")
		}
		result = append(result, page.Data...)
		if page.HasMore != nil && !*page.HasMore {
			return result, nil
		}
		if page.LastID == "" || page.LastID == after || len(page.Data) == 0 {
			return result, nil
		}
		after = page.LastID
	}
}

func (c *Client) DeleteMemoryItemContext(
	ctx context.Context,
	name string,
	memoryID string,
) (*MemoryDeleteResult, error) {
	return memoryMutation[MemoryDeleteResult](
		c,
		ctx,
		http.MethodDelete,
		memoryItemPath(name, memoryID),
		nil,
		"delete memory item "+memoryID,
	)
}

func memoryRead[T any](
	c *Client,
	ctx context.Context,
	method string,
	path string,
	body interface{},
	action string,
) (*T, error) {
	resp, err := c.doWithOptions(ctx, method, path, body, memoryRequestOptions())
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to %s", action)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to read response for %s", action)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpx.ResponseError("Foundry", action, resp, data)
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse response for %s", action)
	}
	return &result, nil
}

func memoryMutation[T any](
	c *Client,
	ctx context.Context,
	method string,
	path string,
	body interface{},
	action string,
) (*T, error) {
	resp, err := c.doWithOptions(ctx, method, path, body, memoryRequestOptions())
	if err != nil {
		return nil, memoryMutationFailure(err, "failed to %s", action)
	}
	defer resp.Body.Close()
	data, err := readBody(resp)
	if err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to read response for %s", action),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", action, resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var result T
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse response for %s", action),
		)
	}
	return &result, nil
}

func memoryMutationFailure(err error, format string, args ...interface{}) error {
	wrapped := errs.FoundryWrap(err, format, args...)
	if errs.IsAuthenticationOrAuthorization(err) || errs.IsKind(err, "config") ||
		errs.IsKind(err, "security") {
		return wrapped
	}
	return errs.AmbiguousMutation(wrapped)
}

func memoryRequestOptions() requestOptions {
	return requestOptions{
		apiVersion:      MemoryAPIVersion,
		suppressPreview: true,
	}
}

func memoryStorePath(name string) string {
	return "/memory_stores/" + url.PathEscape(name)
}

func memoryItemPath(name, memoryID string) string {
	return memoryStorePath(name) + "/items/" + url.PathEscape(memoryID)
}

func (c *Client) memoryOperationPath(operationLocation string) (string, error) {
	operationLocation = strings.TrimSpace(operationLocation)
	if operationLocation == "" {
		return "", errs.Foundry("memory update response omitted Operation-Location")
	}
	operationURL, err := url.Parse(operationLocation)
	if err != nil {
		return "", errs.Security("memory update returned an invalid Operation-Location: %v", err)
	}
	endpointURL, err := url.Parse(c.endpoint)
	if err != nil {
		return "", errs.Security("configured Foundry endpoint is invalid: %v", err)
	}
	if !strings.EqualFold(operationURL.Scheme, endpointURL.Scheme) ||
		!strings.EqualFold(operationURL.Host, endpointURL.Host) {
		return "", errs.Security("memory update Operation-Location changed the Foundry origin")
	}
	if !strings.HasPrefix(operationURL.EscapedPath(), endpointURL.EscapedPath()+"/") {
		return "", errs.Security("memory update Operation-Location escaped the Foundry project path")
	}
	path := strings.TrimPrefix(operationURL.EscapedPath(), endpointURL.EscapedPath())
	if operationURL.RawQuery != "" {
		path += "?" + operationURL.RawQuery
	}
	return path, nil
}
