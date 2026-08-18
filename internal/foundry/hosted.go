package foundry

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
)

const (
	MaxHostedInvocationBytes  = int64(8 << 20)
	MaxHostedSessionFileBytes = int64(50 << 20)
	maxHostedLogLineBytes     = 256 << 10
)

type HostedSession struct {
	AgentSessionID string      `json:"agent_session_id,omitempty" yaml:"agentSessionId,omitempty"`
	SessionID      string      `json:"session_id,omitempty" yaml:"sessionId,omitempty"`
	Status         string      `json:"status,omitempty" yaml:"status,omitempty"`
	AgentVersion   string      `json:"agent_version,omitempty" yaml:"agentVersion,omitempty"`
	Version        string      `json:"version,omitempty" yaml:"version,omitempty"`
	CreatedAt      interface{} `json:"created_at,omitempty" yaml:"createdAt,omitempty"`
	UpdatedAt      interface{} `json:"updated_at,omitempty" yaml:"updatedAt,omitempty"`
	ExpiresAt      interface{} `json:"expires_at,omitempty" yaml:"expiresAt,omitempty"`
}

func (s HostedSession) ID() string {
	if s.AgentSessionID != "" {
		return s.AgentSessionID
	}
	return s.SessionID
}

type HostedSessionFile struct {
	Name         string      `json:"name,omitempty" yaml:"name,omitempty"`
	Path         string      `json:"path,omitempty" yaml:"path,omitempty"`
	Size         int64       `json:"size,omitempty" yaml:"size,omitempty"`
	IsDirectory  bool        `json:"is_directory,omitempty" yaml:"isDirectory,omitempty"`
	LastModified interface{} `json:"last_modified,omitempty" yaml:"lastModified,omitempty"`
}

type HostedInvocationResult struct {
	StatusCode        int                   `json:"statusCode" yaml:"statusCode"`
	ContentType       string                `json:"contentType,omitempty" yaml:"contentType,omitempty"`
	SessionID         string                `json:"sessionId,omitempty" yaml:"sessionId,omitempty"`
	Region            string                `json:"region,omitempty" yaml:"region,omitempty"`
	ResponseID        string                `json:"responseId,omitempty" yaml:"responseId,omitempty"`
	Body              interface{}           `json:"body,omitempty" yaml:"body,omitempty"`
	ApprovalRequests  []MCPApprovalRequest  `json:"approvalRequests,omitempty" yaml:"approvalRequests,omitempty"`
	ApprovalDecisions []MCPApprovalDecision `json:"approvalDecisions,omitempty" yaml:"approvalDecisions,omitempty"`
	ApprovalRounds    int                   `json:"approvalRounds,omitempty" yaml:"approvalRounds,omitempty"`
}

type HostedLogEvent struct {
	Event     string `json:"event" yaml:"event"`
	Timestamp string `json:"timestamp,omitempty" yaml:"timestamp,omitempty"`
	Stream    string `json:"stream,omitempty" yaml:"stream,omitempty"`
	Message   string `json:"message,omitempty" yaml:"message,omitempty"`
	Data      string `json:"data,omitempty" yaml:"data,omitempty"`
}

type HostedLogStream struct {
	Events    []HostedLogEvent `json:"events" yaml:"events"`
	Lines     int              `json:"lines" yaml:"lines"`
	Bytes     int64            `json:"bytes" yaml:"bytes"`
	Truncated bool             `json:"truncated" yaml:"truncated"`
	TimedOut  bool             `json:"timedOut" yaml:"timedOut"`
}

func (c *Client) InvokeHostedContext(
	ctx context.Context,
	name string,
	protocol string,
	body []byte,
	contentType string,
	sessionID string,
	isolationKey string,
) (*HostedInvocationResult, error) {
	switch protocol {
	case "responses":
		protocol = "openai/responses"
	case "invocations":
	default:
		return nil, errs.Config(
			"Hosted smoke supports responses and invocations; protocol %q requires a different client contract",
			protocol,
		)
	}
	if len(body) == 0 {
		return nil, errs.Config("Hosted invocation body is required")
	}
	if int64(len(body)) > MaxHostedInvocationBytes {
		return nil, errs.Config("Hosted invocation body exceeds the %d byte limit", MaxHostedInvocationBytes)
	}
	if _, _, err := mime.ParseMediaType(contentType); err != nil {
		return nil, errs.Config("Hosted invocation content type %q is invalid: %v", contentType, err)
	}
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return nil, err
	}
	path := agentPath(name) + "/endpoint/protocols/" + protocol
	if protocol == "invocations" && sessionID != "" {
		path += "?agent_session_id=" + url.QueryEscape(sessionID)
	}
	headers := make(http.Header)
	if isolationKey != "" {
		headers.Set("x-ms-user-isolation-key", isolationKey)
	}
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPost,
		path,
		rawRequestBody{reader: strings.NewReader(string(body)), contentLength: int64(len(body))},
		requestOptions{
			contentType:     contentType,
			suppressPreview: true,
			headers:         headers,
		},
	)
	if err != nil {
		wrapped := errs.FoundryWrap(err, "failed to invoke Hosted Agent %q", name)
		if errs.IsAuthenticationOrAuthorization(err) ||
			errs.IsKind(err, "config") ||
			errs.IsKind(err, "security") {
			return nil, wrapped
		}
		return nil, errs.AmbiguousMutation(wrapped)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read Hosted Agent %q invocation response", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", fmt.Sprintf("invoke Hosted Agent %q", name), resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	result := &HostedInvocationResult{
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		SessionID:   resp.Header.Get("x-agent-session-id"),
		Region:      resp.Header.Get("x-ms-region"),
	}
	var parsed interface{}
	if json.Valid(data) {
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, errs.FoundryWrap(err, "failed to parse Hosted Agent %q invocation response", name)
		}
		result.Body = parsed
		if object, ok := parsed.(map[string]interface{}); ok {
			if value, ok := object["id"].(string); ok {
				result.ResponseID = value
			}
			approvals, approvalErr := findMCPApprovalRequests(object["output"])
			if approvalErr != nil {
				return nil, errs.FoundryWrap(
					approvalErr,
					"failed to parse Hosted Agent %q MCP approval requests",
					name,
				)
			}
			result.ApprovalRequests = approvals
			if result.SessionID == "" {
				if value, ok := object["agent_session_id"].(string); ok {
					result.SessionID = value
				} else if value, ok := object["session_id"].(string); ok {
					result.SessionID = value
				}
			}
		}
	} else if len(data) > 0 {
		result.Body = string(data)
	}
	return result, nil
}

func (c *Client) ContinueHostedApprovalsContext(
	ctx context.Context,
	name string,
	previousResponseID string,
	decisions []MCPApprovalDecision,
	sessionID string,
	isolationKey string,
) (*HostedInvocationResult, error) {
	body, err := approvalContinuationBody(previousResponseID, decisions)
	if err != nil {
		return nil, err
	}
	if sessionID != "" {
		body["agent_session_id"] = sessionID
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Config("failed to encode Hosted MCP approval continuation: %v", err)
	}
	return c.InvokeHostedContext(
		ctx,
		name,
		"responses",
		encoded,
		"application/json",
		"",
		isolationKey,
	)
}

func (c *Client) CreateHostedSessionContext(
	ctx context.Context,
	name string,
	version string,
	isolationKey string,
) (*HostedSession, error) {
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return nil, err
	}
	body := map[string]interface{}{}
	if version != "" {
		body["version_indicator"] = map[string]interface{}{
			"type":          "version_ref",
			"agent_version": version,
		}
	}
	resp, err := c.hostedSessionRequest(ctx, http.MethodPost, name, "", "", body, isolationKey)
	if err != nil {
		return nil, hostedMutationError(err, "create session for Hosted Agent %q", name)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read Hosted Agent %q session creation response", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", fmt.Sprintf("create session for Hosted Agent %q", name), resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var session HostedSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse Hosted Agent %q session creation response", name),
		)
	}
	if session.ID() == "" {
		return nil, errs.AmbiguousMutation(
			errs.Foundry("Hosted Agent %q session creation response contained no session id", name),
		)
	}
	return &session, nil
}

func (c *Client) ListHostedSessionsContext(
	ctx context.Context,
	name string,
	isolationKey string,
) ([]HostedSession, error) {
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return nil, err
	}
	var sessions []HostedSession
	after := ""
	seen := make(map[string]struct{})
	for {
		query := ""
		if after != "" {
			query = "after=" + url.QueryEscape(after)
		}
		resp, err := c.hostedSessionRequest(ctx, http.MethodGet, name, "", query, nil, isolationKey)
		if err != nil {
			return nil, errs.FoundryWrap(err, "failed to list sessions for Hosted Agent %q", name)
		}
		data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
		resp.Body.Close()
		if readErr != nil {
			return nil, errs.FoundryWrap(readErr, "failed to read sessions for Hosted Agent %q", name)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, httpx.ResponseError("Foundry", fmt.Sprintf("list sessions for Hosted Agent %q", name), resp, data)
		}
		page, hasMore, lastID, parseErr := parseHostedSessionPage(data)
		if parseErr != nil {
			return nil, errs.FoundryWrap(parseErr, "failed to parse sessions for Hosted Agent %q", name)
		}
		sessions = append(sessions, page...)
		if !hasMore || lastID == "" {
			return sessions, nil
		}
		if _, duplicate := seen[lastID]; duplicate {
			return nil, errs.Foundry(
				"Hosted Agent %q session pagination repeated cursor %q",
				name,
				lastID,
			)
		}
		seen[lastID] = struct{}{}
		after = lastID
	}
}

func (c *Client) GetHostedSessionContext(
	ctx context.Context,
	name string,
	sessionID string,
	isolationKey string,
) (*HostedSession, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errs.Config("Hosted Agent session id is required")
	}
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return nil, err
	}
	resp, err := c.hostedSessionRequest(ctx, http.MethodGet, name, sessionID, "", nil, isolationKey)
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to get Hosted Agent %q session %q", name, sessionID)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return nil, errs.FoundryWrap(readErr, "failed to read Hosted Agent %q session %q", name, sessionID)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError(
			"Foundry",
			fmt.Sprintf("get Hosted Agent %q session %q", name, sessionID),
			resp,
			data,
		)
	}
	var session HostedSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse Hosted Agent %q session %q", name, sessionID)
	}
	return &session, nil
}

func (c *Client) StopHostedSessionContext(
	ctx context.Context,
	name string,
	sessionID string,
	isolationKey string,
) error {
	resp, err := c.hostedSessionRequest(ctx, http.MethodPost, name, sessionID+":stop", "", nil, isolationKey)
	if err != nil {
		return hostedMutationError(err, "stop Hosted Agent %q session %q", name, sessionID)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read stop response for Hosted Agent %q session %q", name, sessionID),
		)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	responseErr := httpx.ResponseError(
		"Foundry",
		fmt.Sprintf("stop Hosted Agent %q session %q", name, sessionID),
		resp,
		data,
	)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return errs.AmbiguousMutation(responseErr)
	}
	return responseErr
}

func (c *Client) DeleteHostedSessionContext(
	ctx context.Context,
	name string,
	sessionID string,
	isolationKey string,
) (bool, error) {
	resp, err := c.hostedSessionRequest(ctx, http.MethodDelete, name, sessionID, "", nil, isolationKey)
	if err != nil {
		return false, hostedMutationError(err, "delete Hosted Agent %q session %q", name, sessionID)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return false, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read delete response for Hosted Agent %q session %q", name, sessionID),
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
		fmt.Sprintf("delete Hosted Agent %q session %q", name, sessionID),
		resp,
		data,
	)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return false, errs.AmbiguousMutation(responseErr)
	}
	return false, responseErr
}

func (c *Client) UploadHostedSessionFileContext(
	ctx context.Context,
	name string,
	sessionID string,
	remotePath string,
	reader io.Reader,
	size int64,
	isolationKey string,
) error {
	if size < 0 || size > MaxHostedSessionFileBytes {
		return errs.Config("Hosted session file size must be between 0 and %d bytes", MaxHostedSessionFileBytes)
	}
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return err
	}
	payload, err := readBoundedBody(reader, MaxHostedSessionFileBytes)
	if err != nil {
		return errs.Config("failed to read Hosted session upload: %v", err)
	}
	if int64(len(payload)) != size {
		return errs.Config(
			"Hosted session upload changed while it was being read: expected %d bytes, got %d",
			size,
			len(payload),
		)
	}
	query := "path=" + url.QueryEscape(remotePath)
	path := hostedSessionPath(name, sessionID) + "/files/content?" + query
	headers := hostedIsolationHeaders(isolationKey)
	resp, err := c.doWithOptions(
		ctx,
		http.MethodPut,
		path,
		rawRequestBody{reader: bytes.NewReader(payload), contentLength: size},
		requestOptions{
			contentType:     "application/octet-stream",
			suppressPreview: true,
			headers:         headers,
		},
	)
	if err != nil {
		return hostedMutationError(err, "upload %q to Hosted Agent %q session %q", remotePath, name, sessionID)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read Hosted session file upload response"),
		)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	responseErr := httpx.ResponseError("Foundry", "upload Hosted session file", resp, data)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return errs.AmbiguousMutation(responseErr)
	}
	return responseErr
}

func (c *Client) ListHostedSessionFilesContext(
	ctx context.Context,
	name string,
	sessionID string,
	remotePath string,
	isolationKey string,
) ([]HostedSessionFile, error) {
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return nil, err
	}
	path := hostedSessionPath(name, sessionID) + "/files?path=" + url.QueryEscape(remotePath)
	resp, err := c.doWithOptions(ctx, http.MethodGet, path, nil, requestOptions{
		suppressPreview: true,
		headers:         hostedIsolationHeaders(isolationKey),
	})
	if err != nil {
		return nil, errs.FoundryWrap(err, "failed to list Hosted session files")
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return nil, errs.FoundryWrap(readErr, "failed to read Hosted session file list")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpx.ResponseError("Foundry", "list Hosted session files", resp, data)
	}
	var payload struct {
		Entries []HostedSessionFile `json:"entries"`
		Data    []HostedSessionFile `json:"data"`
		Value   []HostedSessionFile `json:"value"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, errs.FoundryWrap(err, "failed to parse Hosted session file list")
	}
	switch {
	case payload.Entries != nil:
		return payload.Entries, nil
	case payload.Data != nil:
		return payload.Data, nil
	default:
		return payload.Value, nil
	}
}

func (c *Client) DownloadHostedSessionFileContext(
	ctx context.Context,
	name string,
	sessionID string,
	remotePath string,
	writer io.Writer,
	maxBytes int64,
	isolationKey string,
) (int64, error) {
	if maxBytes <= 0 || maxBytes > MaxHostedSessionFileBytes {
		return 0, errs.Config("Hosted session download limit must be between 1 and %d bytes", MaxHostedSessionFileBytes)
	}
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return 0, err
	}
	path := hostedSessionPath(name, sessionID) + "/files/content?path=" + url.QueryEscape(remotePath)
	resp, err := c.doWithOptions(ctx, http.MethodGet, path, nil, requestOptions{
		accept:          "application/octet-stream",
		suppressPreview: true,
		headers:         hostedIsolationHeaders(isolationKey),
	})
	if err != nil {
		return 0, errs.FoundryWrap(err, "failed to download Hosted session file %q", remotePath)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
		if readErr != nil {
			return 0, errs.FoundryWrap(readErr, "failed to read Hosted session file download error")
		}
		return 0, httpx.ResponseError("Foundry", "download Hosted session file", resp, data)
	}
	if resp.ContentLength > maxBytes {
		return 0, errs.Config(
			"Hosted session file %q is %d bytes and exceeds the %d byte download limit",
			remotePath,
			resp.ContentLength,
			maxBytes,
		)
	}
	written, copyErr := io.Copy(writer, io.LimitReader(resp.Body, maxBytes+1))
	if copyErr != nil {
		return written, errs.FoundryWrap(copyErr, "failed to write Hosted session file %q", remotePath)
	}
	if written > maxBytes {
		return written, errs.Config(
			"Hosted session file %q exceeds the %d byte download limit",
			remotePath,
			maxBytes,
		)
	}
	return written, nil
}

func (c *Client) DeleteHostedSessionFileContext(
	ctx context.Context,
	name string,
	sessionID string,
	remotePath string,
	isolationKey string,
) error {
	if err := validateHostedHeaderValue(isolationKey, "isolation key"); err != nil {
		return err
	}
	path := hostedSessionPath(name, sessionID) + "/files?path=" + url.QueryEscape(remotePath)
	resp, err := c.doWithOptions(ctx, http.MethodDelete, path, nil, requestOptions{
		suppressPreview: true,
		headers:         hostedIsolationHeaders(isolationKey),
	})
	if err != nil {
		return hostedMutationError(err, "delete Hosted session file %q", remotePath)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return errs.AmbiguousMutation(errs.FoundryWrap(readErr, "failed to read Hosted session file delete response"))
	}
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	responseErr := httpx.ResponseError("Foundry", "delete Hosted session file", resp, data)
	if httpx.IsTransientStatus(resp.StatusCode) {
		return errs.AmbiguousMutation(responseErr)
	}
	return responseErr
}

func (c *Client) StreamHostedLogsContext(
	ctx context.Context,
	name string,
	version string,
	sessionID string,
	maxLines int,
	maxBytes int64,
	duration time.Duration,
) (HostedLogStream, error) {
	if maxLines < 1 {
		return HostedLogStream{}, errs.Config("Hosted log line limit must be at least 1")
	}
	if maxBytes < 1 {
		return HostedLogStream{}, errs.Config("Hosted log byte limit must be at least 1")
	}
	if duration <= 0 {
		return HostedLogStream{}, errs.Config("Hosted log duration must be greater than zero")
	}
	streamCtx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	path := fmt.Sprintf(
		"%s/versions/%s/sessions/%s:logstream",
		agentPath(name),
		url.PathEscape(version),
		url.PathEscape(sessionID),
	)
	resp, err := c.doWithOptions(streamCtx, http.MethodGet, path, nil, requestOptions{
		accept:          "text/event-stream",
		suppressPreview: true,
	})
	if err != nil {
		if errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
			return HostedLogStream{TimedOut: true}, nil
		}
		return HostedLogStream{}, errs.FoundryWrap(err, "failed to stream logs for Hosted Agent %q", name)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
		if readErr != nil {
			return HostedLogStream{}, errs.FoundryWrap(readErr, "failed to read Hosted log stream error")
		}
		return HostedLogStream{}, httpx.ResponseError("Foundry", "stream Hosted Agent logs", resp, data)
	}

	result := HostedLogStream{Events: make([]HostedLogEvent, 0)}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 4096), maxHostedLogLineBytes)
	eventName := ""
	var dataLines []string
	flush := func() {
		if eventName == "" && len(dataLines) == 0 {
			return
		}
		data := strings.Join(dataLines, "\n")
		event := HostedLogEvent{Event: eventName, Data: data}
		if event.Event == "" {
			event.Event = "message"
		}
		var payload struct {
			Timestamp string `json:"timestamp"`
			Stream    string `json:"stream"`
			Message   string `json:"message"`
		}
		if json.Unmarshal([]byte(data), &payload) == nil {
			event.Timestamp = payload.Timestamp
			event.Stream = payload.Stream
			event.Message = payload.Message
			event.Data = ""
		}
		result.Events = append(result.Events, event)
		result.Lines++
		eventName = ""
		dataLines = dataLines[:0]
	}
	for scanner.Scan() {
		line := scanner.Text()
		result.Bytes += int64(len(line) + 1)
		if result.Bytes > maxBytes || result.Lines >= maxLines {
			result.Truncated = true
			break
		}
		if line == "" {
			flush()
			continue
		}
		if value, ok := strings.CutPrefix(line, "event:"); ok {
			eventName = strings.TrimSpace(value)
		} else if value, ok := strings.CutPrefix(line, "data:"); ok {
			dataLines = append(dataLines, strings.TrimSpace(value))
		}
	}
	if !result.Truncated {
		flush()
	}
	if scanErr := scanner.Err(); scanErr != nil {
		if errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
			result.TimedOut = true
			return result, nil
		}
		if strings.Contains(strings.ToLower(scanErr.Error()), "token too long") {
			result.Truncated = true
			return result, nil
		}
		return result, errs.FoundryWrap(scanErr, "Hosted Agent log stream failed")
	}
	if errors.Is(streamCtx.Err(), context.DeadlineExceeded) {
		result.TimedOut = true
	}
	return result, nil
}

func (c *Client) CreateHostedVersionContext(
	ctx context.Context,
	name string,
	description string,
	definition map[string]interface{},
	draft bool,
	metadata ...map[string]interface{},
) (*AgentVersion, error) {
	if kind, _ := definition["kind"].(string); !strings.EqualFold(kind, "hosted") {
		return nil, errs.Config("Hosted Agent version definition must have kind hosted")
	}
	body := map[string]interface{}{
		"draft":      draft,
		"definition": definition,
	}
	if description != "" {
		body["description"] = description
	}
	if len(metadata) > 0 && len(metadata[0]) > 0 {
		body["metadata"] = metadata[0]
	}
	return c.createHostedVersionRequest(ctx, name, body, requestOptions{suppressPreview: true})
}

func (c *Client) CreateHostedCodeVersionContext(
	ctx context.Context,
	name string,
	metadata map[string]interface{},
	archivePath string,
	archiveSHA256 string,
) (*AgentVersion, error) {
	archive, err := os.Open(archivePath)
	if err != nil {
		return nil, errs.Config("failed to open Hosted Agent code archive: %v", err)
	}
	defer archive.Close()
	info, err := archive.Stat()
	if err != nil {
		return nil, errs.Config("failed to inspect Hosted Agent code archive: %v", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Config("Hosted Agent code archive must be a regular file")
	}

	bodyFile, err := os.CreateTemp("", "foundry-agent-manager-multipart-*.tmp")
	if err != nil {
		return nil, errs.Config("failed to create Hosted Agent multipart body: %v", err)
	}
	bodyPath := bodyFile.Name()
	defer os.Remove(bodyPath)
	defer bodyFile.Close()
	if err := bodyFile.Chmod(0o600); err != nil {
		return nil, errs.Config("failed to secure Hosted Agent multipart body: %v", err)
	}
	writer := multipart.NewWriter(bodyFile)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	metadataPart, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return nil, errs.Config("failed to create Hosted Agent metadata part: %v", err)
	}
	if err := json.NewEncoder(metadataPart).Encode(metadata); err != nil {
		return nil, errs.Config("failed to encode Hosted Agent metadata: %v", err)
	}
	codeHeader := make(textproto.MIMEHeader)
	codeHeader.Set(
		"Content-Disposition",
		fmt.Sprintf(`form-data; name="code"; filename=%q`, filepath.Base(name)+".zip"),
	)
	codeHeader.Set("Content-Type", "application/zip")
	codePart, err := writer.CreatePart(codeHeader)
	if err != nil {
		return nil, errs.Config("failed to create Hosted Agent code part: %v", err)
	}
	if _, err := io.Copy(codePart, archive); err != nil {
		return nil, errs.Config("failed to build Hosted Agent code part: %v", err)
	}
	if err := writer.Close(); err != nil {
		return nil, errs.Config("failed to finalize Hosted Agent multipart body: %v", err)
	}
	bodyInfo, err := bodyFile.Stat()
	if err != nil {
		return nil, errs.Config("failed to inspect Hosted Agent multipart body: %v", err)
	}
	if _, err := bodyFile.Seek(0, io.SeekStart); err != nil {
		return nil, errs.Config("failed to rewind Hosted Agent multipart body: %v", err)
	}
	headers := make(http.Header)
	headers.Set("x-ms-code-zip-sha256", archiveSHA256)
	return c.createHostedVersionRequest(
		ctx,
		name,
		rawRequestBody{reader: bodyFile, contentLength: bodyInfo.Size()},
		requestOptions{
			contentType:     writer.FormDataContentType(),
			accept:          "application/json",
			suppressPreview: true,
			headers:         headers,
		},
	)
}

func (c *Client) createHostedVersionRequest(
	ctx context.Context,
	name string,
	body interface{},
	options requestOptions,
) (*AgentVersion, error) {
	resp, err := c.doWithOptions(ctx, http.MethodPost, agentPath(name)+"/versions", body, options)
	if err != nil {
		return nil, hostedMutationError(err, "create Hosted Agent %q version", name)
	}
	defer resp.Body.Close()
	data, readErr := readBoundedBody(resp.Body, MaxHostedInvocationBytes)
	if readErr != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(readErr, "failed to read Hosted Agent %q version creation response", name),
		)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseErr := httpx.ResponseError("Foundry", fmt.Sprintf("create Hosted Agent %q version", name), resp, data)
		if httpx.IsTransientStatus(resp.StatusCode) {
			return nil, errs.AmbiguousMutation(responseErr)
		}
		return nil, responseErr
	}
	var version AgentVersion
	if err := json.Unmarshal(data, &version); err != nil {
		return nil, errs.AmbiguousMutation(
			errs.FoundryWrap(err, "failed to parse Hosted Agent %q version creation response", name),
		)
	}
	if version.Version == "" {
		return nil, errs.AmbiguousMutation(
			errs.Foundry("Hosted Agent %q version creation response contained no version", name),
		)
	}
	return &version, nil
}

func (c *Client) hostedSessionRequest(
	ctx context.Context,
	method string,
	name string,
	sessionID string,
	query string,
	body interface{},
	isolationKey string,
) (*http.Response, error) {
	path := agentPath(name) + "/endpoint/sessions"
	if sessionID != "" {
		path += "/" + url.PathEscape(sessionID)
	}
	if query != "" {
		path += "?" + query
	}
	return c.doWithOptions(ctx, method, path, body, requestOptions{
		suppressPreview: true,
		headers:         hostedIsolationHeaders(isolationKey),
	})
}

func hostedSessionPath(name, sessionID string) string {
	return agentPath(name) + "/endpoint/sessions/" + url.PathEscape(sessionID)
}

func hostedIsolationHeaders(isolationKey string) http.Header {
	headers := make(http.Header)
	if isolationKey != "" {
		headers.Set("x-ms-user-isolation-key", isolationKey)
	}
	return headers
}

func validateHostedHeaderValue(value, field string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return errs.Security("Hosted Agent %s contains an unsafe control character", field)
	}
	return nil
}

func hostedMutationError(err error, format string, args ...interface{}) error {
	wrapped := errs.FoundryWrap(err, format, args...)
	if errs.IsAuthenticationOrAuthorization(err) ||
		errs.IsKind(err, "config") ||
		errs.IsKind(err, "security") {
		return wrapped
	}
	return errs.AmbiguousMutation(wrapped)
}

func parseHostedSessionPage(data []byte) ([]HostedSession, bool, string, error) {
	var direct []HostedSession
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &direct); err != nil {
			return nil, false, "", err
		}
		return direct, false, "", nil
	}
	var page struct {
		Data     []HostedSession `json:"data"`
		Value    []HostedSession `json:"value"`
		Sessions []HostedSession `json:"sessions"`
		HasMore  bool            `json:"has_more"`
		LastID   string          `json:"last_id"`
		NextLink string          `json:"next_link"`
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, false, "", err
	}
	sessions := page.Data
	if sessions == nil {
		sessions = page.Value
	}
	if sessions == nil {
		sessions = page.Sessions
	}
	lastID := page.LastID
	if lastID == "" && page.NextLink != "" {
		next, err := url.Parse(page.NextLink)
		if err != nil {
			return nil, false, "", fmt.Errorf("invalid session next_link: %w", err)
		}
		lastID = next.Query().Get("after")
	}
	return sessions, page.HasMore || page.NextLink != "", lastID, nil
}

func readBoundedBody(reader io.Reader, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("response exceeds the %d byte limit", maxBytes)
	}
	return data, nil
}
