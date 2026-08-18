package monitorlogs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const (
	testEndpoint = "https://receipts.eastus-1.ingest.monitor.azure.com"
	testDCR      = "dcr-0123456789abcdef0123456789abcdef"
	testStream   = "Custom-FoundryAgentReceipts"
	testScope    = "https://monitor.azure.com/.default"
)

type recordingCredential struct {
	scopes [][]string
	err    error
}

func (c *recordingCredential) GetToken(
	_ context.Context,
	options policy.TokenRequestOptions,
) (azcore.AccessToken, error) {
	c.scopes = append(c.scopes, append([]string(nil), options.Scopes...))
	if c.err != nil {
		return azcore.AccessToken{}, c.err
	}
	return azcore.AccessToken{Token: "monitor-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type recordingHTTPClient struct {
	request  *http.Request
	body     []byte
	response *http.Response
	err      error
}

func (c *recordingHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.request = request
	if request.Body != nil {
		c.body, _ = io.ReadAll(request.Body)
	}
	if c.err != nil {
		return nil, c.err
	}
	if c.response == nil {
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	}
	return c.response, nil
}

func TestUploadPublishesReceiptEnvelope(t *testing.T) {
	credential := &recordingCredential{}
	transport := &recordingHTTPClient{}
	client := newTestClient(t, credential, transport)

	raw := completedV2Receipt(t)
	result, err := client.Upload(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReceiptID != "receipt-1" ||
		result.Operation != "model-deployment-create" ||
		result.Status != "succeeded" ||
		result.Endpoint != testEndpoint {
		t.Fatalf("unexpected upload result: %#v", result)
	}
	if len(credential.scopes) != 1 ||
		len(credential.scopes[0]) != 1 ||
		credential.scopes[0][0] != testScope {
		t.Fatalf("unexpected token scopes: %#v", credential.scopes)
	}
	if transport.request.Method != http.MethodPost ||
		transport.request.URL.Path != "/dataCollectionRules/"+testDCR+"/streams/"+testStream ||
		transport.request.URL.Query().Get("api-version") != apiVersion {
		t.Fatalf("unexpected request: %s %s", transport.request.Method, transport.request.URL)
	}
	if transport.request.Header.Get("Authorization") != "Bearer monitor-token" ||
		transport.request.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected request headers: %#v", transport.request.Header)
	}
	var records []struct {
		TimeGenerated time.Time         `json:"TimeGenerated"`
		ReceiptID     string            `json:"ReceiptId"`
		SchemaVersion string            `json:"SchemaVersion"`
		Operation     string            `json:"Operation"`
		Status        string            `json:"Status"`
		Cloud         string            `json:"Cloud"`
		AgentName     string            `json:"AgentName"`
		ProjectName   string            `json:"ProjectName"`
		Metadata      map[string]string `json:"Metadata"`
		Receipt       json.RawMessage   `json:"Receipt"`
	}
	if err := json.Unmarshal(transport.body, &records); err != nil {
		t.Fatalf("invalid request body: %v / %s", err, transport.body)
	}
	if len(records) != 1 ||
		records[0].ReceiptID != "receipt-1" ||
		records[0].SchemaVersion != receipt.SchemaVersionV2 ||
		records[0].AgentName != "agent" ||
		records[0].ProjectName != "project" ||
		records[0].Metadata["owner"] != "platform" ||
		!json.Valid(records[0].Receipt) {
		t.Fatalf("unexpected log record: %#v", records)
	}
}

func TestV1ReceiptUsesPromptDeployOperation(t *testing.T) {
	client := newTestClient(t, &recordingCredential{}, &recordingHTTPClient{})
	store := receipt.New("", "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	store.Receipt.ID = "receipt-v1"
	store.Receipt.Status = "succeeded"
	now := time.Now().UTC()
	store.Receipt.CompletedAt = &now
	raw, err := json.Marshal(store.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Upload(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation != "prompt-deploy" {
		t.Fatalf("unexpected v1 operation: %#v", result)
	}
}

func TestUploadPreservesAuthorizationDetails(t *testing.T) {
	action := "Microsoft.Insights/Telemetry/Write"
	scope := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/dataCollectionRules/dcr"
	client := newTestClient(t, &recordingCredential{}, &recordingHTTPClient{
		response: &http.Response{
			StatusCode: http.StatusForbidden,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":"AuthorizationFailed","message":"The client does not have authorization to perform action '` +
					action + `' over scope '` + scope + `'."}}`,
			)),
		},
	})
	_, err := client.Upload(context.Background(), completedV2Receipt(t))
	if err == nil || !errs.IsKind(err, "authorization") {
		t.Fatalf("expected authorization failure, got %v", err)
	}
	steps := strings.Join(errs.Remediation(err), "\n")
	if !strings.Contains(steps, action) || !strings.Contains(steps, scope) {
		t.Fatalf("authorization remediation lost action or scope: %q", steps)
	}
}

func TestNewClientRejectsUnsafeOrIncompleteDestinations(t *testing.T) {
	tests := []Options{
		{
			Endpoint:        "https://attacker.example",
			DCRImmutableID:  testDCR,
			StreamName:      testStream,
			Scope:           testScope,
			AllowedSuffixes: []string{"ingest.monitor.azure.com"},
		},
		{
			Endpoint:        testEndpoint + "/unexpected",
			DCRImmutableID:  testDCR,
			StreamName:      testStream,
			Scope:           testScope,
			AllowedSuffixes: []string{"ingest.monitor.azure.com"},
		},
		{
			Endpoint:        testEndpoint,
			DCRImmutableID:  "../dcr",
			StreamName:      testStream,
			Scope:           testScope,
			AllowedSuffixes: []string{"ingest.monitor.azure.com"},
		},
		{
			Endpoint:        testEndpoint,
			DCRImmutableID:  testDCR,
			StreamName:      "FoundryAgentReceipts",
			Scope:           testScope,
			AllowedSuffixes: []string{"ingest.monitor.azure.com"},
		},
	}
	for _, options := range tests {
		if _, err := NewClient(options, &recordingCredential{}, &recordingHTTPClient{}); err == nil {
			t.Fatalf("expected destination rejection for %#v", options)
		}
	}
}

func TestUploadRejectsUnknownOrOversizedReceipts(t *testing.T) {
	client := newTestClient(t, &recordingCredential{}, &recordingHTTPClient{})
	if _, err := client.Upload(
		context.Background(),
		[]byte(`{"schemaVersion":"unknown","id":"x","status":"done","startedAt":"2026-01-01T00:00:00Z"}`),
	); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected schema rejection, got %v", err)
	}
	large := completedV2Receipt(t)
	var decoded map[string]any
	if err := json.Unmarshal(large, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded["error"] = strings.Repeat("x", MaxPayloadBytes)
	large, _ = json.Marshal(decoded)
	if _, err := client.Upload(context.Background(), large); err == nil ||
		!strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected payload bound, got %v", err)
	}
}

func newTestClient(
	t *testing.T,
	credential azcore.TokenCredential,
	transport HTTPClient,
) *Client {
	t.Helper()
	client, err := NewClient(Options{
		Endpoint:        testEndpoint,
		DCRImmutableID:  testDCR,
		StreamName:      testStream,
		Scope:           testScope,
		AllowedSuffixes: []string{"ingest.monitor.azure.com"},
	}, credential, transport)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func completedV2Receipt(t *testing.T) []byte {
	t.Helper()
	store := receipt.NewOperation(
		"",
		"model-deployment-create",
		"AzureCloud",
		receipt.ManifestReference{Path: "agent.yaml"},
		receipt.ResourceReference{Name: "project"},
		"agent",
	)
	store.Receipt.ID = "receipt-1"
	store.Receipt.Status = "succeeded"
	store.Receipt.Metadata = map[string]interface{}{"owner": "platform"}
	now := time.Now().UTC()
	store.Receipt.CompletedAt = &now
	raw, err := json.Marshal(store.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
