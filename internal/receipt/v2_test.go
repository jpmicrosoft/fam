package receipt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOperationStorePersistsReleaseAndExternalActions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := NewOperation(
		path,
		"m365-publish",
		"AzureCloud",
		ManifestReference{Path: "agent.yaml", Hash: "manifest", DesiredHash: "desired"},
		ResourceReference{Name: "project", Endpoint: "https://example.test"},
		"agent",
	)
	store.Receipt.Metadata = map[string]interface{}{"owner": "platform"}
	store.Receipt.Agent.CreatedVersion = "5"
	store.Receipt.Agent.ActiveVersionBefore = "4"
	store.Receipt.Agent.ActiveVersionAfter = "4"
	store.Receipt.Agent.SelectorBefore = SelectorState{
		Mode:          "pinned",
		ActiveVersion: "4",
		Raw:           json.RawMessage(`{"version_selection_rules":[{"type":"FixedRatio","agent_version":"4","traffic_percentage":100}]}`),
	}
	if err := store.AddResource(ResourceChange{
		Kind:         "bot-service",
		Name:         "support-bot",
		Action:       "created",
		Status:       "succeeded",
		CreatedByRun: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddExternalAction(ExternalAction{
		Kind:           "microsoft365.publish",
		System:         "Microsoft365",
		Status:         "approval-required",
		IdempotencyKey: "1.0.0",
		ResourceID:     "title-id",
		Irreversible:   true,
		Compensation:   "not-supported",
		StartedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("pending-external-action", nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded ReceiptV2
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersionV2 ||
		loaded.Operation != "m365-publish" ||
		loaded.Metadata["owner"] != "platform" ||
		loaded.Agent.CreatedVersion != "5" ||
		len(loaded.Resources) != 1 ||
		len(loaded.ExternalActions) != 1 {
		t.Fatalf("unexpected operation receipt: %#v", loaded)
	}
}

func TestOperationStoreRedactsSecretsEverywhere(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := NewOperation(
		path,
		"promote",
		"AzureCloud",
		ManifestReference{Path: "agent.yaml"},
		ResourceReference{},
		"agent",
	)
	secret := "operation-secret-value"
	store.RegisterSecret(secret)
	if err := store.AddResource(ResourceChange{
		Kind:           "test",
		Reconciliation: "remove " + secret,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddExternalAction(ExternalAction{
		Kind:           "test",
		System:         "external",
		Status:         "unknown",
		Irreversible:   true,
		Reconciliation: "inspect " + secret,
		StartedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	store.Receipt.Agent.ID = secret
	if err := store.Complete("failed", errors.New("failed with "+secret)); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("operation receipt leaked a registered secret: %s", data)
	}
	if !strings.Contains(string(data), "<redacted>") {
		t.Fatalf("operation receipt did not retain a redaction marker: %s", data)
	}
}

func TestOperationPathIsManifestRelativeAndSanitized(t *testing.T) {
	path := OperationPath(
		filepath.Join("root", "agent.yaml"),
		"m365/publish",
		"sample/agent",
		time.Date(2026, 8, 4, 17, 0, 0, 0, time.UTC),
	)
	if filepath.Dir(path) != filepath.Join("root", ".foundry-agent-manager", "receipts") {
		t.Fatalf("unexpected operation receipt directory: %s", path)
	}
	base := filepath.Base(path)
	if strings.ContainsAny(base, `/\`) ||
		!strings.Contains(base, "m365-publish") ||
		!strings.Contains(base, "sample-agent") {
		t.Fatalf("operation receipt path was not sanitized: %s", path)
	}
}

func TestOperationStorePublishesTerminalReceipt(t *testing.T) {
	store := NewOperation(
		filepath.Join(t.TempDir(), "receipt.json"),
		"model-deployment-create",
		"AzureCloud",
		ManifestReference{Path: "agent.yaml"},
		ResourceReference{Name: "project"},
		"agent",
	)
	publisher := &recordingPublisher{}
	store.SetPublisher(context.Background(), publisher)
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	if len(publisher.payloads) != 1 ||
		!strings.Contains(string(publisher.payloads[0]), `"operation": "model-deployment-create"`) {
		t.Fatalf("unexpected published operation receipt: %q", publisher.payloads)
	}
}
