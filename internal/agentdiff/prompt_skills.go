package agentdiff

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/skills"
)

// NativeSkills rejects unknown or unpinned remote references rather than dropping
// fields when a new immutable version must carry them forward.
func NativeSkills(definition map[string]interface{}) ([]skills.Reference, bool, error) {
	value, present := definition["skills"]
	if !present {
		return nil, false, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, true, fmt.Errorf("cannot read native Prompt Skills: %w", err)
	}
	if bytes.Equal(data, []byte("null")) {
		return nil, true, fmt.Errorf("native Prompt Skills must be an array, not null")
	}
	var references []skills.Reference
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&references); err != nil {
		return nil, true, fmt.Errorf("cannot read native Prompt Skills: %w", err)
	}
	seen := make(map[string]bool, len(references))
	for _, reference := range references {
		if err := skills.ValidateReference(reference); err != nil {
			return nil, true, fmt.Errorf("cannot preserve native Prompt Skill: %w", err)
		}
		if seen[reference.Name] {
			return nil, true, fmt.Errorf("duplicate native Prompt Skill %q", reference.Name)
		}
		seen[reference.Name] = true
	}
	return references, true, nil
}

// PreserveSkills resolves omitted Skills to the remote immutable references.
// It does not change the caller's declarative management intent.
func PreserveSkills(agent *foundry.Agent, desired Desired) (Desired, error) {
	if agent == nil {
		return desired, nil
	}
	if !desired.ManageSkills {
		references, present, err := NativeSkills(agent.Versions.Latest.Definition)
		if err != nil {
			return Desired{}, err
		}
		if present {
			desired.Skills = references
			desired.ManageSkills = true
		}
	}
	if raw, present := agent.Versions.Latest.Definition["harness"]; desired.ManageSkills && present {
		harness, ok := raw.(map[string]interface{})
		if !ok {
			return Desired{}, fmt.Errorf("cannot preserve native Prompt Skills harness: expected an object")
		}
		kind, ok := harness["type"].(string)
		if !ok || strings.TrimSpace(kind) == "" {
			return Desired{}, fmt.Errorf("cannot preserve native Prompt Skills harness: missing type")
		}
		// Preserve the existing runtime, never select a harness for the operator.
		desired.Harness = maps.Clone(harness)
	}
	return desired, nil
}
