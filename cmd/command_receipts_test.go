package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/azcloud"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

const (
	commandReceiptEndpoint = "https://receipts.eastus-1.ingest.monitor.azure.com"
	commandReceiptDCR      = "dcr-0123456789abcdef0123456789abcdef"
	commandReceiptStream   = "Custom-FoundryAgentReceipts"
)

func TestReceiptUploadCommandPublishesPreservedReceipt(t *testing.T) {
	path := completedOperationReceiptFile(t)
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/dataCollectionRules/" + commandReceiptDCR + "/streams/" + commandReceiptStream: route(
			http.StatusNoContent,
			"",
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(
		t,
		"",
		"receipt",
		"upload",
		"--file",
		path,
		"--receipt-log-endpoint",
		commandReceiptEndpoint,
		"--receipt-log-dcr-id",
		commandReceiptDCR,
		"--receipt-log-stream",
		commandReceiptStream,
		"--output",
		"json",
	)
	if run.code != 0 {
		t.Fatalf("receipt upload failed: %s", run.stderr)
	}
	var result struct {
		File           string `json:"file"`
		ReceiptID      string `json:"receiptId"`
		Operation      string `json:"operation"`
		DCRImmutableID string `json:"dcrImmutableId"`
		StreamName     string `json:"streamName"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatalf("invalid structured output: %v / %s", err, run.stdout)
	}
	if result.File != path ||
		result.ReceiptID != "command-receipt" ||
		result.Operation != "model-deployment-create" ||
		result.DCRImmutableID != commandReceiptDCR ||
		result.StreamName != commandReceiptStream {
		t.Fatalf("unexpected receipt upload result: %#v", result)
	}
	if len(httpClient.requests) != 1 ||
		httpClient.requests[0].Header.Get("Authorization") != "Bearer token" {
		t.Fatalf("unexpected Logs ingestion request: %#v", httpClient.requests)
	}
}

func TestAutomaticReceiptPublishingPreservesLocalFileAndRetryGuidance(t *testing.T) {
	action := "Microsoft.Insights/Telemetry/Write"
	scope := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Insights/dataCollectionRules/dcr"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/dataCollectionRules/" + commandReceiptDCR + "/streams/" + commandReceiptStream: route(
			http.StatusForbidden,
			`{"error":{"code":"AuthorizationFailed","message":"The client does not have authorization to perform action '`+
				action+`' over scope '`+scope+`'."}}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	command, _, err := rootCmd().Find([]string{"receipt", "upload"})
	if err != nil {
		t.Fatal(err)
	}
	setReceiptLogFlags(t, command)
	path := filepath.Join(t.TempDir(), "automatic-receipt.json")
	store, err := newManagedOperationStore(
		command,
		path,
		"model-deployment-create",
		"AzureCloud",
		receipt.ManifestReference{Path: "agent.yaml"},
		receipt.ResourceReference{Name: "project"},
		"agent",
	)
	if err != nil {
		t.Fatal(err)
	}
	store.Receipt.ID = "automatic-receipt"
	err = store.Complete("succeeded", nil)
	if err == nil || !errs.IsKind(err, "authorization") {
		t.Fatalf("expected authorization failure, got %v", err)
	}
	steps := strings.Join(errs.Remediation(err), "\n")
	for _, expected := range []string{action, scope, "Retry command:", path} {
		if !strings.Contains(steps, expected) {
			t.Fatalf("receipt remediation omitted %q: %s", expected, steps)
		}
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("local receipt was not preserved: %v", readErr)
	}
	var saved receipt.ReceiptV2
	if err := json.Unmarshal(data, &saved); err != nil || saved.Status != "succeeded" {
		t.Fatalf("unexpected preserved receipt: %#v / %v", saved, err)
	}
}

func TestReceiptPublishingRejectsPartialConfigurationBeforeCredentialUse(t *testing.T) {
	command, _, err := rootCmd().Find([]string{"receipt", "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Root().PersistentFlags().Set("receipt-log-endpoint", commandReceiptEndpoint); err != nil {
		t.Fatal(err)
	}
	_, err = newManagedOperationStore(
		command,
		filepath.Join(t.TempDir(), "receipt.json"),
		"test",
		"AzureCloud",
		receipt.ManifestReference{},
		receipt.ResourceReference{},
		"agent",
	)
	if err == nil || !errs.IsKind(err, "config") ||
		!strings.Contains(err.Error(), "--receipt-log-dcr-id") {
		t.Fatalf("expected local partial-config rejection, got %v", err)
	}
}

func TestAutomaticReceiptPublishingPreservesLocalFileWhenCredentialFails(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn = originalCredential
		newHTTPClientFn = originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		return nil, errs.Auth("credential unavailable")
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("HTTP client must not be created after credential failure")
		return nil
	}

	command, _, err := rootCmd().Find([]string{"receipt", "upload"})
	if err != nil {
		t.Fatal(err)
	}
	setReceiptLogFlags(t, command)
	path := filepath.Join(t.TempDir(), "credential-failure.json")
	store, err := newManagedOperationStore(
		command,
		path,
		"test",
		"AzureCloud",
		receipt.ManifestReference{},
		receipt.ResourceReference{},
		"agent",
	)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Complete("succeeded", nil)
	if err == nil || !errs.IsKind(err, "auth") {
		t.Fatalf("expected authentication failure, got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("local receipt was not preserved before authentication: %v", err)
	}
}

func TestProductionReceiptStoresUseManagedConstructors(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") ||
			name == "command_receipts.go" {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "receipt.New(") ||
			strings.Contains(string(data), "receipt.NewOperation(") {
			t.Errorf("%s bypasses automatic receipt publishing constructors", name)
		}
	}
}

func completedOperationReceiptFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := receipt.NewOperation(
		path,
		"model-deployment-create",
		"AzureCloud",
		receipt.ManifestReference{Path: "agent.yaml"},
		receipt.ResourceReference{Name: "project"},
		"agent",
	)
	store.Receipt.ID = "command-receipt"
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(absolute)
}

func setReceiptLogFlags(t *testing.T, command *cobra.Command) {
	t.Helper()
	flags := command.Root().PersistentFlags()
	for name, value := range map[string]string{
		"receipt-log-endpoint": commandReceiptEndpoint,
		"receipt-log-dcr-id":   commandReceiptDCR,
		"receipt-log-stream":   commandReceiptStream,
	} {
		if err := flags.Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
}
