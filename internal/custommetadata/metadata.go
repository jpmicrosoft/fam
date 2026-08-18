// Package custommetadata validates operator-defined metadata shared by agent
// versions, deployment receipts, and Log Analytics records.
package custommetadata

import (
	"fmt"
	"strings"
	"unicode/utf8"

	errs "foundry-agent-manager/internal/errors"
)

const (
	MaxEntries     = 16
	MaxKeyLength   = 64
	MaxValueLength = 512
)

// ParseAssignments parses repeatable key=value command-line values. Later
// assignments replace earlier values for the same key.
func ParseAssignments(assignments []string) (map[string]string, error) {
	result := make(map[string]string, len(assignments))
	for _, assignment := range assignments {
		key, value, found := strings.Cut(assignment, "=")
		if !found {
			return nil, errs.Config(
				"metadata %q must use key=value syntax",
				assignment,
			)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errs.Config("metadata keys must not be empty")
		}
		result[key] = value
	}
	if err := Validate(result); err != nil {
		return nil, err
	}
	return result, nil
}

// FromMap converts a decoded manifest object to string metadata.
func FromMap(values map[string]interface{}) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, errs.Config("agent.metadata.%s must be a string", key)
		}
		result[key] = text
	}
	if err := Validate(result); err != nil {
		return nil, err
	}
	return result, nil
}

// Merge returns a defensive copy with later maps overriding earlier maps.
func Merge(values ...map[string]string) map[string]string {
	var result map[string]string
	for _, value := range values {
		if len(value) == 0 {
			continue
		}
		if result == nil {
			result = make(map[string]string)
		}
		for key, text := range value {
			result[key] = text
		}
	}
	return result
}

// Clone returns a defensive copy.
func Clone(value map[string]string) map[string]string {
	return Merge(value)
}

// InterfaceMap converts metadata to a manifest-compatible object.
func InterfaceMap(value map[string]string) map[string]interface{} {
	if len(value) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(value))
	for key, text := range value {
		result[key] = text
	}
	return result
}

// HostedMap validates and clones metadata from an azure.ai.agent service.
// Hosted Agents allow string values and the documented authors string list.
func HostedMap(value map[string]interface{}) (map[string]interface{}, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if len(value) > MaxEntries {
		return nil, errs.Config(
			"agent metadata must contain at most %d entries; received %d",
			MaxEntries,
			len(value),
		)
	}
	result := make(map[string]interface{}, len(value))
	for key, raw := range value {
		if err := validateKey(key); err != nil {
			return nil, err
		}
		switch typed := raw.(type) {
		case string:
			if err := validateValue(key, typed); err != nil {
				return nil, err
			}
			result[key] = typed
		case []interface{}:
			if key != "authors" {
				return nil, errs.Config("agent metadata %q must be a string", key)
			}
			authors := make([]string, len(typed))
			for i, author := range typed {
				text, ok := author.(string)
				if !ok {
					return nil, errs.Config("agent metadata authors[%d] must be a string", i)
				}
				if !utf8.ValidString(text) {
					return nil, errs.Config("agent metadata authors[%d] must contain valid UTF-8", i)
				}
				authors[i] = text
			}
			result[key] = authors
		case []string:
			authors := append([]string(nil), typed...)
			if key != "authors" {
				return nil, errs.Config("agent metadata %q must be a string", key)
			}
			for i, author := range authors {
				if !utf8.ValidString(author) {
					return nil, errs.Config("agent metadata authors[%d] must contain valid UTF-8", i)
				}
			}
			result[key] = authors
		default:
			return nil, errs.Config("agent metadata %q must be a string", key)
		}
	}
	return result, nil
}

// MergeHosted overlays string custom metadata on a Hosted metadata object.
func MergeHosted(base map[string]interface{}, overrides map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(base)+len(overrides))
	for key, value := range base {
		switch typed := value.(type) {
		case []string:
			result[key] = append([]string(nil), typed...)
		case []interface{}:
			result[key] = append([]interface{}(nil), typed...)
		default:
			result[key] = value
		}
	}
	for key, value := range overrides {
		result[key] = value
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// Validate enforces the Microsoft Foundry agent-version metadata contract.
func Validate(value map[string]string) error {
	if len(value) > MaxEntries {
		return errs.Config(
			"agent metadata must contain at most %d entries; received %d",
			MaxEntries,
			len(value),
		)
	}
	for key, text := range value {
		if err := validateKey(key); err != nil {
			return err
		}
		if err := validateValue(key, text); err != nil {
			return err
		}
	}
	return nil
}

func validateKey(key string) error {
	if !utf8.ValidString(key) {
		return errs.Config("agent metadata key %q must contain valid UTF-8", key)
	}
	keyLength := utf8.RuneCountInString(key)
	if keyLength == 0 || keyLength > MaxKeyLength {
		return errs.Config(
			"agent metadata key %q must contain 1-%d characters",
			key,
			MaxKeyLength,
		)
	}
	return nil
}

func validateValue(key, value string) error {
	if !utf8.ValidString(value) {
		return errs.Config("agent metadata %q must contain valid UTF-8", key)
	}
	if valueLength := utf8.RuneCountInString(value); valueLength > MaxValueLength {
		return errs.Config(
			"agent metadata value for %q must contain at most %d characters; received %d",
			key,
			MaxValueLength,
			valueLength,
		)
	}
	return nil
}

// Summary returns a safe count-only description for diagnostic output.
func Summary(value map[string]string) string {
	return fmt.Sprintf("%d metadata field(s)", len(value))
}
