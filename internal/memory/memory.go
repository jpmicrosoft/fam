// Package memory parses declarative Foundry memory-store definitions.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
)

type Definition struct {
	Name           string
	Description    string
	ChatModel      string
	EmbeddingModel string
	Metadata       map[string]string
	Options        foundry.MemoryStoreDefaultOptions
	DesiredHash    string
}

func Build(raw []map[string]interface{}) ([]Definition, error) {
	result := make([]Definition, 0, len(raw))
	seen := map[string]struct{}{}
	for index, item := range raw {
		definition, err := buildOne(item)
		if err != nil {
			return nil, errs.Config("memory_stores[%d]: %v", index, err)
		}
		key := strings.ToLower(definition.Name)
		if _, exists := seen[key]; exists {
			return nil, errs.Config("memory_stores[%d]: duplicate memory store name %q", index, definition.Name)
		}
		seen[key] = struct{}{}
		result = append(result, definition)
	}
	return result, nil
}

func (d Definition) Store() foundry.MemoryStore {
	options := d.Options
	return foundry.MemoryStore{
		Name:        d.Name,
		Description: d.Description,
		Metadata:    cloneMetadata(d.Metadata),
		Definition: foundry.MemoryStoreDefinition{
			Kind:           "default",
			ChatModel:      d.ChatModel,
			EmbeddingModel: d.EmbeddingModel,
			Options:        &options,
		},
	}
}

func buildOne(raw map[string]interface{}) (Definition, error) {
	definition := Definition{
		Name:           stringValue(raw["name"]),
		Description:    stringValue(raw["description"]),
		ChatModel:      stringValue(raw["chat_model"]),
		EmbeddingModel: stringValue(raw["embedding_model"]),
		Metadata:       map[string]string{},
		Options: foundry.MemoryStoreDefaultOptions{
			UserProfileEnabled: true,
			ChatSummaryEnabled: true,
		},
	}
	if definition.Name == "" || definition.ChatModel == "" || definition.EmbeddingModel == "" {
		return Definition{}, fmt.Errorf("name, chat_model, and embedding_model are required")
	}
	if metadata, ok := raw["metadata"].(map[string]interface{}); ok {
		for key, value := range metadata {
			text, ok := value.(string)
			if !ok {
				return Definition{}, fmt.Errorf("metadata %q must be a string", key)
			}
			definition.Metadata[key] = text
		}
	}
	if options, ok := raw["options"].(map[string]interface{}); ok {
		if value, exists := options["user_profile_enabled"]; exists {
			definition.Options.UserProfileEnabled, _ = value.(bool)
		}
		if value, exists := options["chat_summary_enabled"]; exists {
			definition.Options.ChatSummaryEnabled, _ = value.(bool)
		}
		definition.Options.UserProfileDetails = stringValue(options["user_profile_details"])
		if value, exists := options["procedural_memory_enabled"]; exists {
			enabled, ok := value.(bool)
			if !ok {
				return Definition{}, fmt.Errorf("procedural_memory_enabled must be a boolean")
			}
			definition.Options.ProceduralMemoryEnabled = &enabled
		}
		if value, exists := options["default_ttl_seconds"]; exists {
			ttl, ok := integerValue(value)
			if !ok || ttl < 0 {
				return Definition{}, fmt.Errorf("default_ttl_seconds must be a non-negative integer")
			}
			definition.Options.DefaultTTLSeconds = &ttl
		}
	}
	data, err := json.Marshal(definition.Store())
	if err != nil {
		return Definition{}, fmt.Errorf("failed to hash memory store: %w", err)
	}
	sum := sha256.Sum256(data)
	definition.DesiredHash = hex.EncodeToString(sum[:])
	return definition, nil
}

func stringValue(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func integerValue(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		if typed != float64(int64(typed)) {
			return 0, false
		}
		return int64(typed), true
	case json.Number:
		integer, err := typed.Int64()
		return integer, err == nil
	default:
		return 0, false
	}
}

func cloneMetadata(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]string, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
