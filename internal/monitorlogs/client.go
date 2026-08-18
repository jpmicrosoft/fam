// Package monitorlogs publishes completed receipts through the Azure Monitor
// Logs Ingestion API.
package monitorlogs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	apiVersion           = "2023-01-01"
	MaxPayloadBytes      = 1 << 20
	maxResponseBodyBytes = 1 << 20
)

var (
	dcrIDPattern  = regexp.MustCompile(`^dcr-[0-9A-Fa-f]{32}$`)
	streamPattern = regexp.MustCompile(`^Custom-[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

// HTTPClient is the authenticated transport dependency used by Client.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Options identifies the Logs Ingestion API destination.
type Options struct {
	Endpoint        string
	DCRImmutableID  string
	StreamName      string
	Scope           string
	AllowedSuffixes []string
}

// UploadResult describes a receipt accepted by the Logs Ingestion API.
type UploadResult struct {
	ReceiptID      string `json:"receiptId" yaml:"receiptId"`
	SchemaVersion  string `json:"schemaVersion" yaml:"schemaVersion"`
	Operation      string `json:"operation" yaml:"operation"`
	Status         string `json:"status" yaml:"status"`
	Endpoint       string `json:"endpoint" yaml:"endpoint"`
	DCRImmutableID string `json:"dcrImmutableId" yaml:"dcrImmutableId"`
	StreamName     string `json:"streamName" yaml:"streamName"`
	RequestID      string `json:"requestId,omitempty" yaml:"requestId,omitempty"`
}

// Client publishes one completed receipt per Logs Ingestion API request.
type Client struct {
	endpoint       string
	dcrImmutableID string
	streamName     string
	scope          string
	credential     azcore.TokenCredential
	httpClient     HTTPClient
}

type receiptMetadata struct {
	SchemaVersion string                 `json:"schemaVersion"`
	ID            string                 `json:"id"`
	Operation     string                 `json:"operation"`
	Status        string                 `json:"status"`
	StartedAt     time.Time              `json:"startedAt"`
	CompletedAt   *time.Time             `json:"completedAt"`
	Cloud         string                 `json:"cloud"`
	Metadata      map[string]interface{} `json:"metadata"`
	Agent         resourceName           `json:"agent"`
	Project       resourceName           `json:"project"`
	Raw           json.RawMessage        `json:"-"`
}

type resourceName struct {
	Name string `json:"name"`
}

type logRecord struct {
	TimeGenerated time.Time              `json:"TimeGenerated"`
	ReceiptID     string                 `json:"ReceiptId"`
	SchemaVersion string                 `json:"SchemaVersion"`
	Operation     string                 `json:"Operation"`
	Status        string                 `json:"Status"`
	Cloud         string                 `json:"Cloud"`
	AgentName     string                 `json:"AgentName"`
	ProjectName   string                 `json:"ProjectName"`
	Metadata      map[string]interface{} `json:"Metadata,omitempty"`
	Receipt       json.RawMessage        `json:"Receipt"`
}

// NewClient validates the fixed Azure destination before any credential is
// requested or receipt content is sent.
func NewClient(
	options Options,
	credential azcore.TokenCredential,
	httpClient HTTPClient,
) (*Client, error) {
	options, err := ValidateOptions(options)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, errs.Auth("receipt Log Analytics credential is required")
	}
	if httpClient == nil {
		return nil, errs.Config("receipt Log Analytics HTTP client is required")
	}
	return &Client{
		endpoint:       options.Endpoint,
		dcrImmutableID: options.DCRImmutableID,
		streamName:     options.StreamName,
		scope:          options.Scope,
		credential:     credential,
		httpClient:     httpClient,
	}, nil
}

// ValidateOptions normalizes and validates a Logs Ingestion API destination
// without acquiring a credential.
func ValidateOptions(options Options) (Options, error) {
	endpoint, err := netcheck.ValidateMonitorIngestionEndpointForSuffixes(
		options.Endpoint,
		"receipt Log Analytics endpoint",
		options.AllowedSuffixes,
	)
	if err != nil {
		return Options{}, err
	}
	dcrID := strings.TrimSpace(options.DCRImmutableID)
	if !dcrIDPattern.MatchString(dcrID) {
		return Options{}, errs.Config(
			"receipt Log Analytics DCR immutable ID must use dcr- followed by 32 hexadecimal characters",
		)
	}
	stream := strings.TrimSpace(options.StreamName)
	if !streamPattern.MatchString(stream) {
		return Options{}, errs.Config(
			"receipt Log Analytics stream must start with Custom- and contain only letters, numbers, underscores, or hyphens",
		)
	}
	scope := strings.TrimSpace(options.Scope)
	if scope == "" {
		return Options{}, errs.Config("receipt Log Analytics token scope is required")
	}
	options.Endpoint = endpoint
	options.DCRImmutableID = dcrID
	options.StreamName = stream
	options.Scope = scope
	options.AllowedSuffixes = append([]string(nil), options.AllowedSuffixes...)
	return options, nil
}

// Publish uploads a redacted manager-generated v1 or v2 receipt.
func (c *Client) Publish(ctx context.Context, rawReceipt []byte) error {
	_, err := c.Upload(ctx, rawReceipt)
	return err
}

// Upload uploads a redacted manager-generated v1 or v2 receipt and returns
// destination metadata suitable for structured command output.
func (c *Client) Upload(ctx context.Context, rawReceipt []byte) (UploadResult, error) {
	metadata, payload, err := buildPayload(rawReceipt)
	if err != nil {
		return UploadResult{}, err
	}
	token, err := c.credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{c.scope},
	})
	if err != nil {
		return UploadResult{}, errs.AuthWrap(err, "failed to acquire an Azure Monitor Logs ingestion token")
	}
	if strings.TrimSpace(token.Token) == "" {
		return UploadResult{}, errs.Auth("Azure Monitor Logs ingestion returned an empty access token")
	}
	requestURL, err := c.uploadURL()
	if err != nil {
		return UploadResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		return UploadResult{}, errs.Config("failed to create the receipt Logs ingestion request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return UploadResult{}, err
		}
		return UploadResult{}, errs.AmbiguousMutation(errs.Transient(
			"receipt Logs ingestion request failed after it may have reached Azure: %v",
			err,
		))
	}
	if response == nil {
		return UploadResult{}, errs.AmbiguousMutation(errs.Transient(
			"receipt Logs ingestion returned no HTTP response after the request may have reached Azure",
		))
	}
	defer response.Body.Close()
	responseBody, err := readBoundedBody(response.Body, maxResponseBodyBytes)
	if err != nil {
		return UploadResult{}, errs.AmbiguousMutation(errs.Transient(
			"failed to read the receipt Logs ingestion response: %v",
			err,
		))
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		responseErr := httpx.ResponseError(
			"Azure Monitor Logs",
			"receipt ingestion",
			response,
			responseBody,
		)
		if httpx.IsTransientStatus(response.StatusCode) {
			return UploadResult{}, errs.AmbiguousMutation(responseErr)
		}
		return UploadResult{}, responseErr
	}
	return UploadResult{
		ReceiptID:      metadata.ID,
		SchemaVersion:  metadata.SchemaVersion,
		Operation:      metadata.Operation,
		Status:         metadata.Status,
		Endpoint:       c.endpoint,
		DCRImmutableID: c.dcrImmutableID,
		StreamName:     c.streamName,
		RequestID:      request.Header.Get("x-ms-client-request-id"),
	}, nil
}

func (c *Client) uploadURL() (string, error) {
	parsed, err := url.Parse(c.endpoint)
	if err != nil {
		return "", errs.Config("failed to parse the validated receipt Logs ingestion endpoint: %v", err)
	}
	parsed.Path = "/dataCollectionRules/" + url.PathEscape(c.dcrImmutableID) +
		"/streams/" + url.PathEscape(c.streamName)
	query := parsed.Query()
	query.Set("api-version", apiVersion)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func buildPayload(rawReceipt []byte) (receiptMetadata, []byte, error) {
	rawReceipt = bytes.TrimSpace(rawReceipt)
	if len(rawReceipt) == 0 || !json.Valid(rawReceipt) {
		return receiptMetadata{}, nil, errs.Config("receipt file must contain one valid JSON object")
	}
	var metadata receiptMetadata
	if err := json.Unmarshal(rawReceipt, &metadata); err != nil {
		return receiptMetadata{}, nil, errs.Config("failed to parse receipt metadata: %v", err)
	}
	switch metadata.SchemaVersion {
	case receipt.SchemaVersion:
		if strings.TrimSpace(metadata.Operation) == "" {
			metadata.Operation = "prompt-deploy"
		}
	case receipt.SchemaVersionV2:
		if strings.TrimSpace(metadata.Operation) == "" {
			return receiptMetadata{}, nil, errs.Config("v2 receipt operation is required")
		}
	default:
		return receiptMetadata{}, nil, errs.Config(
			"unsupported receipt schemaVersion %q; expected %s or %s",
			metadata.SchemaVersion,
			receipt.SchemaVersion,
			receipt.SchemaVersionV2,
		)
	}
	if strings.TrimSpace(metadata.ID) == "" ||
		strings.TrimSpace(metadata.Status) == "" ||
		metadata.StartedAt.IsZero() {
		return receiptMetadata{}, nil, errs.Config(
			"receipt must include non-empty id, status, and startedAt fields",
		)
	}
	generated := metadata.StartedAt.UTC()
	if metadata.CompletedAt != nil && !metadata.CompletedAt.IsZero() {
		generated = metadata.CompletedAt.UTC()
	}
	metadata.Raw = append(json.RawMessage(nil), rawReceipt...)
	payload, err := json.Marshal([]logRecord{{
		TimeGenerated: generated,
		ReceiptID:     metadata.ID,
		SchemaVersion: metadata.SchemaVersion,
		Operation:     metadata.Operation,
		Status:        metadata.Status,
		Cloud:         metadata.Cloud,
		AgentName:     metadata.Agent.Name,
		ProjectName:   metadata.Project.Name,
		Metadata:      metadata.Metadata,
		Receipt:       metadata.Raw,
	}})
	if err != nil {
		return receiptMetadata{}, nil, errs.Config("failed to encode receipt Logs ingestion payload: %v", err)
	}
	if len(payload) > MaxPayloadBytes {
		return receiptMetadata{}, nil, errs.Config(
			"receipt Logs ingestion payload is %d bytes; Azure Monitor accepts at most %d bytes per request",
			len(payload),
			MaxPayloadBytes,
		)
	}
	return metadata, payload, nil
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
