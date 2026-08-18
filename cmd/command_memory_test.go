package main

import (
	"testing"

	"foundry-agent-manager/internal/foundry"
)

func TestMemoryDefinitionsEqualNormalizesServiceDefaultTTL(t *testing.T) {
	zero := int64(0)
	desired := foundry.MemoryStoreDefinition{
		Kind:           "default",
		ChatModel:      "chat",
		EmbeddingModel: "embedding",
		Options: &foundry.MemoryStoreDefaultOptions{
			UserProfileEnabled: true,
		},
	}
	current := desired
	current.Options = &foundry.MemoryStoreDefaultOptions{
		UserProfileEnabled: true,
		DefaultTTLSeconds:  &zero,
	}
	if !memoryDefinitionsEqual(current, desired) {
		t.Fatal("service default_ttl_seconds=0 must equal an omitted desired TTL")
	}

	nonzero := int64(60)
	current.Options.DefaultTTLSeconds = &nonzero
	if memoryDefinitionsEqual(current, desired) {
		t.Fatal("a nonzero service TTL must remain an immutable-definition difference")
	}
}

func TestMemoryMetadataEqualTreatsNilAndEmptyAsEqual(t *testing.T) {
	if !memoryMetadataEqual(nil, map[string]string{}) {
		t.Fatal("nil and empty metadata must be equivalent")
	}
	if memoryMetadataEqual(map[string]string{"owner": "qa"}, nil) {
		t.Fatal("nonempty metadata must not equal missing metadata")
	}
}
