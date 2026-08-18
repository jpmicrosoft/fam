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

type recordingPublisher struct {
	payloads [][]byte
	err      error
}

func (p *recordingPublisher) Publish(_ context.Context, payload []byte) error {
	p.payloads = append(p.payloads, append([]byte(nil), payload...))
	return p.err
}

func TestStorePersistsTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest-hash", "desired-hash", "agent")
	store.Receipt.Metadata = map[string]interface{}{"owner": "platform"}
	if err := store.AddStep("preflight", "succeeded", "ready"); err != nil {
		t.Fatal(err)
	}
	store.Receipt.Agent.Version = "4"
	store.Receipt.Agent.Changed = true
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Receipt
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Status != "succeeded" ||
		loaded.Agent.Version != "4" ||
		loaded.Metadata["owner"] != "platform" ||
		len(loaded.Steps) != 1 {
		t.Fatalf("unexpected receipt: %#v", loaded)
	}
}

func TestDefaultPathIsManifestRelative(t *testing.T) {
	path := DefaultPath(filepath.Join("root", "agent.yaml"), "sample/agent", time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	wantDir := filepath.Join("root", ".foundry-agent-manager", "receipts")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("unexpected receipt directory: %s", path)
	}
}

func TestRegisteredSecretsAreRedactedFromStepsErrorsAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest-hash", "desired-hash", "agent")
	secret := "apim-subscription-key-value"
	store.RegisterSecret(secret)

	if err := store.AddStep("apim-connection", "failed", "ARM rejected key "+secret); err != nil {
		t.Fatal(err)
	}
	// Direct field mutation must be covered by the final sweep as well.
	store.Receipt.APIM.Action = "updated with " + secret
	if err := store.Complete("failed", errors.New("upsert failed: "+secret)); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(store.Receipt.Steps[0].Details, secret) {
		t.Fatalf("step details retained the secret: %#v", store.Receipt.Steps[0])
	}
	if strings.Contains(store.Receipt.Error, secret) {
		t.Fatalf("receipt error retained the secret: %s", store.Receipt.Error)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("receipt file retained the secret: %s", data)
	}
	if !strings.Contains(string(data), "<redacted>") {
		t.Fatalf("receipt file is missing the redaction placeholder: %s", data)
	}
	var loaded Receipt
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("redaction produced invalid JSON: %v", err)
	}
}

func TestReceiptNeverRecordsOperatorTrustConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest-hash", "desired-hash", "agent")
	if err := store.AddStep("preflight", "succeeded", "all checks passed; 2 approved destination(s)"); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"trusted-apim-host", "trusted-tool-host", "FOUNDRY_AGENT_MANAGER_TRUSTED"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("receipt recorded operator trust configuration %q: %s", forbidden, data)
		}
	}
}

func TestStorePublishesOnlyTheTerminalRedactedReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	secret := "receipt-publisher-secret"
	store.RegisterSecret(secret)
	publisher := &recordingPublisher{}
	store.SetPublisher(context.Background(), publisher)
	if err := store.AddStep("deploy", "started", "using "+secret); err != nil {
		t.Fatal(err)
	}
	if len(publisher.payloads) != 0 {
		t.Fatal("intermediate receipt transitions must remain local")
	}
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	if len(publisher.payloads) != 1 ||
		strings.Contains(string(publisher.payloads[0]), secret) {
		t.Fatalf("unexpected published receipt: %q", publisher.payloads)
	}
	var published Receipt
	if err := json.Unmarshal(publisher.payloads[0], &published); err != nil ||
		len(published.Steps) != 1 ||
		published.Steps[0].Details != "using <redacted>" {
		t.Fatalf("published receipt was not safely redacted: %#v / %v", published, err)
	}
}

func TestStorePreservesLocalReceiptWhenPublisherFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	store.SetPublisher(context.Background(), &recordingPublisher{err: errors.New("ingestion failed")})
	if err := store.Complete("succeeded", nil); err == nil ||
		!strings.Contains(err.Error(), "ingestion failed") {
		t.Fatalf("expected publisher error, got %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("local receipt was not preserved: %v", err)
	}
	var loaded Receipt
	if err := json.Unmarshal(data, &loaded); err != nil || loaded.Status != "succeeded" {
		t.Fatalf("unexpected preserved receipt: %#v / %v", loaded, err)
	}
}
