package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/receipt"
)

func TestMetadataFlagOverridesManifestMetadata(t *testing.T) {
	root := rootCmd()
	command, _, err := root.Find([]string{"prompt", "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("metadata", "owner=operations"); err != nil {
		t.Fatal(err)
	}
	if err := root.PersistentFlags().Set("metadata", "environment=production"); err != nil {
		t.Fatal(err)
	}
	doc := map[string]interface{}{
		"apiVersion": "foundry-agent-manager/v1",
		"agent": map[string]interface{}{
			"name":         "agent",
			"model":        "model",
			"instructions": "help",
			"metadata": map[string]interface{}{
				"owner": "platform",
			},
		},
	}
	if err := applyOverrides(doc, command); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateManifest(doc); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.ResolveConfig(doc)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Metadata["owner"] != "operations" ||
		cfg.Agent.Metadata["environment"] != "production" {
		t.Fatalf("unexpected merged metadata: %#v", cfg.Agent.Metadata)
	}
}

func TestManagedReceiptsCopyCommandMetadata(t *testing.T) {
	command, _, err := rootCmd().Find([]string{"receipt", "upload"})
	if err != nil {
		t.Fatal(err)
	}
	setCommandMetadata(command, map[string]string{"owner": "platform"})
	store, err := newManagedOperationStore(
		command,
		filepath.Join(t.TempDir(), "receipt.json"),
		"test",
		"AzureCloud",
		receipt.ManifestReference{},
		receipt.ResourceReference{},
		"agent",
	)
	if err != nil {
		t.Fatal(err)
	}
	if store.Receipt.Metadata["owner"] != "platform" {
		t.Fatalf("metadata was not copied to the receipt: %#v", store.Receipt.Metadata)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var saved receipt.ReceiptV2
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Metadata["owner"] != "platform" {
		t.Fatalf("persisted receipt omitted metadata: %#v", saved)
	}
}
