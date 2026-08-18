package receipt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultPathSanitizesTheAgentName proves the receipt file name cannot be
// steered out of the receipts directory by a manifest-supplied agent name, even
// though the schema also constrains that field.
func TestDefaultPathSanitizesTheAgentName(t *testing.T) {
	manifest := filepath.Join("root", "agent.yaml")
	receiptsDir := filepath.Join("root", ".foundry-agent-manager", "receipts")
	stamp := time.Date(2026, 8, 3, 12, 30, 45, 123456789, time.UTC)

	hostileNames := []string{
		`../../evil`,
		`..\..\evil`,
		`/etc/passwd`,
		`C:\Windows\win.ini`,
		`agent name with spaces`,
		"agent\nname",
		`agent:stream`,
		`agent*?"<>|`,
		"",
	}
	for _, name := range hostileNames {
		t.Run(name, func(t *testing.T) {
			path := DefaultPath(manifest, name, stamp)
			if filepath.Dir(path) != receiptsDir {
				t.Fatalf("agent name %q escaped the receipts directory: %s", name, path)
			}
			base := filepath.Base(path)
			for _, forbidden := range []string{"/", `\`, "..", ":", "*", "?", "\"", "<", ">", "|", " ", "\n"} {
				if strings.Contains(strings.TrimSuffix(base, ".json"), forbidden) {
					t.Fatalf("file name %q still contains %q", base, forbidden)
				}
			}
			if !strings.HasSuffix(base, ".json") {
				t.Fatalf("unexpected receipt file name: %s", base)
			}
		})
	}
}

func TestDefaultPathIsSortableAndCollisionResistant(t *testing.T) {
	manifest := filepath.Join("root", "agent.yaml")
	earlier := DefaultPath(manifest, "agent", time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC))
	later := DefaultPath(manifest, "agent", time.Date(2026, 8, 3, 12, 0, 0, 1, time.UTC))
	if earlier >= later {
		t.Fatalf("receipt names must sort chronologically: %s !< %s", earlier, later)
	}
	// The stamp is always rendered in UTC regardless of the caller's zone.
	zone := time.FixedZone("UTC+9", 9*3600)
	local := DefaultPath(manifest, "agent", time.Date(2026, 8, 3, 21, 0, 0, 0, zone))
	if filepath.Base(local) != filepath.Base(earlier) {
		t.Fatalf("receipt names must be UTC based: %s vs %s", filepath.Base(local), filepath.Base(earlier))
	}
}

func TestSaveIsAtomicAndReplacesAnExistingReceipt(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "receipt.json")
	store := New(path, "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	if err := store.AddStep("preflight", "succeeded", "first"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddStep("agent-version", "succeeded", "second"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var loaded Receipt
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("receipt is not valid JSON: %v", err)
	}
	if len(loaded.Steps) != 2 || loaded.SchemaVersion != SchemaVersion {
		t.Fatalf("unexpected receipt: %#v", loaded)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatal("the receipt must end with a newline")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".receipt-") {
			t.Fatalf("a temporary receipt was left behind: %s", entry.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("unexpected receipt directory contents: %#v", entries)
	}
}

func TestSaveIsANoOpWithoutAPath(t *testing.T) {
	store := New("", "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	if err := store.AddStep("preflight", "succeeded", "ready"); err != nil {
		t.Fatalf("a pathless store must not fail: %v", err)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatalf("a pathless store must not fail: %v", err)
	}
	if len(store.Receipt.Steps) != 1 || store.Receipt.Status != "succeeded" {
		t.Fatalf("in-memory state must still be updated: %#v", store.Receipt)
	}
}

func TestShortValuesAreNeverRegisteredAsSecrets(t *testing.T) {
	store := New("", "AzureCloud", "agent.yaml", "manifest", "desired", "agent")
	store.RegisterSecret("")
	store.RegisterSecret("ab")
	if got := store.Redact("target https://contoso.azure-api.net/ab"); !strings.Contains(got, "/ab") {
		t.Fatalf("short values must not mangle diagnostics: %q", got)
	}
	store.RegisterSecret("longer-secret")
	store.RegisterSecret("longer-secret")
	if len(store.secrets) != 1 {
		t.Fatalf("duplicate registrations must be collapsed: %#v", store.secrets)
	}
	if got := store.Redact("key=longer-secret"); strings.Contains(got, "longer-secret") {
		t.Fatalf("a registered secret must be redacted: %q", got)
	}
}

func TestReceiptIDsAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		id := New("", "AzureCloud", "agent.yaml", "manifest", "desired", "agent").Receipt.ID
		if id == "" {
			t.Fatal("a receipt must always carry an id")
		}
		if seen[id] {
			t.Fatalf("duplicate receipt id %q", id)
		}
		seen[id] = true
	}
}
