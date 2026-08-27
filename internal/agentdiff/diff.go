// Package agentdiff compares the manifest-managed portion of a Foundry agent.
package agentdiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"foundry-agent-manager/internal/foundry"
)

// Desired is the exact agent definition managed by Foundry Agent Manager.
type Desired struct {
	Description      string
	Model            string
	Instructions     string
	Tools            []interface{}
	RAIPolicyID      string
	StructuredInputs map[string]interface{}
	Metadata         map[string]string
	ManageMetadata   bool
}

// Difference describes one changed managed field.
type Difference struct {
	Path    string      `json:"path" yaml:"path"`
	Current interface{} `json:"current,omitempty" yaml:"current,omitempty"`
	Desired interface{} `json:"desired,omitempty" yaml:"desired,omitempty"`
}

// Result is a stable remote comparison contract.
type Result struct {
	Changed        bool         `json:"changed" yaml:"changed"`
	AgentExists    bool         `json:"agentExists" yaml:"agentExists"`
	CurrentVersion string       `json:"currentVersion,omitempty" yaml:"currentVersion,omitempty"`
	CurrentHash    string       `json:"currentHash,omitempty" yaml:"currentHash,omitempty"`
	DesiredHash    string       `json:"desiredHash" yaml:"desiredHash"`
	Differences    []Difference `json:"differences" yaml:"differences"`
}

// Compare compares a desired definition with the latest remote agent version.
func Compare(agent *foundry.Agent, desired Desired) (Result, error) {
	desiredValue := desiredManagedValue(desired)
	desiredHash, err := hashValue(desiredValue)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		AgentExists: agent != nil,
		DesiredHash: desiredHash,
	}
	if agent == nil {
		result.Changed = true
		result.Differences = []Difference{{Path: "$", Current: nil, Desired: desiredValue}}
		return result, nil
	}

	result.CurrentVersion = agent.Versions.Latest.Version
	currentValue := remoteManagedValue(agent.Versions.Latest, desired.ManageMetadata)
	result.CurrentHash, err = hashValue(currentValue)
	if err != nil {
		return Result{}, err
	}
	collectDifferences("$", currentValue, desiredValue, &result.Differences)
	result.Changed = len(result.Differences) > 0
	return result, nil
}

// DesiredValue returns the canonical manifest-managed payload used by deploy.
func DesiredValue(desired Desired) map[string]interface{} {
	return desiredManagedValue(desired)
}

func desiredManagedValue(desired Desired) map[string]interface{} {
	definition := map[string]interface{}{
		"kind":         "prompt",
		"model":        desired.Model,
		"instructions": desired.Instructions,
	}
	if len(desired.Tools) > 0 {
		definition["tools"] = normalize(desired.Tools)
	}
	if desired.RAIPolicyID != "" {
		definition["rai_config"] = map[string]interface{}{"rai_policy_name": desired.RAIPolicyID}
	}
	if len(desired.StructuredInputs) > 0 {
		definition["structured_inputs"] = normalize(desired.StructuredInputs)
	}
	result := map[string]interface{}{
		"description": desired.Description,
		"definition":  definition,
	}
	if desired.ManageMetadata {
		metadata := desired.Metadata
		if metadata == nil {
			metadata = map[string]string{}
		}
		result["metadata"] = normalize(metadata)
	}
	return result
}

func remoteManagedValue(version foundry.AgentVersion, manageMetadata bool) map[string]interface{} {
	definition := map[string]interface{}{}
	for _, key := range []string{"kind", "model", "instructions", "tools", "rai_config", "structured_inputs"} {
		if value, ok := version.Definition[key]; ok {
			if key == "tools" {
				if tools, ok := value.([]interface{}); ok && len(tools) == 0 {
					continue
				}
			}
			definition[key] = normalize(value)
		}
	}
	result := map[string]interface{}{
		"description": version.Description,
		"definition":  definition,
	}
	if manageMetadata {
		metadata := version.Metadata
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		result["metadata"] = normalize(metadata)
	}
	return result
}

func hashValue(value interface{}) (string, error) {
	data, err := json.Marshal(normalize(value))
	if err != nil {
		return "", fmt.Errorf("failed to canonicalize agent definition: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func normalize(value interface{}) interface{} {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var normalized interface{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return value
	}
	return normalized
}

func collectDifferences(path string, current, desired interface{}, differences *[]Difference) {
	currentMap, currentIsMap := current.(map[string]interface{})
	desiredMap, desiredIsMap := desired.(map[string]interface{})
	if currentIsMap && desiredIsMap {
		keys := make(map[string]struct{})
		for key := range currentMap {
			keys[key] = struct{}{}
		}
		for key := range desiredMap {
			keys[key] = struct{}{}
		}
		ordered := make([]string, 0, len(keys))
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		for _, key := range ordered {
			collectDifferences(path+"."+key, currentMap[key], desiredMap[key], differences)
		}
		return
	}
	if !reflect.DeepEqual(current, desired) {
		*differences = append(*differences, Difference{Path: path, Current: current, Desired: desired})
	}
}
