package agentdiff

import (
	"testing"

	"foundry-agent-manager/internal/foundry"
)

func remoteAgent() *foundry.Agent {
	agent := &foundry.Agent{Name: "sample", State: "enabled"}
	agent.Versions.Latest = foundry.AgentVersion{
		Version:     "3",
		Description: "sample",
		Definition: map[string]interface{}{
			"kind":         "prompt",
			"model":        "model-a",
			"instructions": "be helpful",
			"temperature":  1.0,
		},
	}
	return agent
}

func TestCompareIgnoresUnmanagedDefaults(t *testing.T) {
	result, err := Compare(remoteAgent(), Desired{
		Description:  "sample",
		Model:        "model-a",
		Instructions: "be helpful",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("unmanaged defaults should not create drift: %#v", result.Differences)
	}
	if result.CurrentHash != result.DesiredHash {
		t.Fatalf("equal definitions should have equal hashes: %#v", result)
	}
}

func TestCompareReportsManagedChanges(t *testing.T) {
	result, err := Compare(remoteAgent(), Desired{
		Description:  "sample",
		Model:        "model-b",
		Instructions: "be helpful",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Differences) != 1 || result.Differences[0].Path != "$.definition.model" {
		t.Fatalf("unexpected differences: %#v", result.Differences)
	}
}

func TestCompareMissingAgent(t *testing.T) {
	result, err := Compare(nil, Desired{Model: "model", Instructions: "help"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.AgentExists || len(result.Differences) != 1 {
		t.Fatalf("unexpected missing-agent result: %#v", result)
	}
}

func TestCompareIncludesStructuredInputs(t *testing.T) {
	agent := remoteAgent()
	agent.Versions.Latest.Definition["structured_inputs"] = map[string]interface{}{
		"storeIds": map[string]interface{}{
			"required": true,
			"schema":   map[string]interface{}{"type": "array"},
		},
	}
	result, err := Compare(agent, Desired{
		Description:  "sample",
		Model:        "model-a",
		Instructions: "be helpful",
		StructuredInputs: map[string]interface{}{
			"storeIds": map[string]interface{}{
				"required": true,
				"schema":   map[string]interface{}{"type": "array"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("matching structured inputs should not create drift: %#v", result.Differences)
	}
}

func TestCompareIncludesAgentMetadata(t *testing.T) {
	agent := remoteAgent()
	agent.Versions.Latest.Metadata = map[string]interface{}{"owner": "platform"}
	result, err := Compare(agent, Desired{
		Description:    "sample",
		Model:          "model-a",
		Instructions:   "be helpful",
		Metadata:       map[string]string{"owner": "operations"},
		ManageMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed ||
		len(result.Differences) != 1 ||
		result.Differences[0].Path != "$.metadata.owner" {
		t.Fatalf("unexpected metadata differences: %#v", result.Differences)
	}
}

func TestCompareIgnoresUnmanagedRemoteMetadata(t *testing.T) {
	agent := remoteAgent()
	agent.Versions.Latest.Metadata = map[string]interface{}{"owner": "platform"}
	result, err := Compare(agent, Desired{
		Description:  "sample",
		Model:        "model-a",
		Instructions: "be helpful",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("undeclared metadata must remain unmanaged: %#v", result.Differences)
	}
}

func TestCompareCanClearManagedMetadata(t *testing.T) {
	agent := remoteAgent()
	agent.Versions.Latest.Metadata = map[string]interface{}{"owner": "platform"}
	result, err := Compare(agent, Desired{
		Description:    "sample",
		Model:          "model-a",
		Instructions:   "be helpful",
		Metadata:       map[string]string{},
		ManageMetadata: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || len(result.Differences) != 1 {
		t.Fatalf("explicit empty metadata must clear remote values: %#v", result.Differences)
	}
}

func TestCollectDifferencesCoversOverlappingAndDisjointMapKeys(t *testing.T) {
	current := map[string]interface{}{
		"currentOnly": "old",
		"shared": map[string]interface{}{
			"changed": "before",
			"same":    "value",
		},
	}
	desired := map[string]interface{}{
		"desiredOnly": "new",
		"shared": map[string]interface{}{
			"changed": "after",
			"same":    "value",
		},
	}

	var differences []Difference
	collectDifferences("$", current, desired, &differences)

	wantPaths := []string{"$.currentOnly", "$.desiredOnly", "$.shared.changed"}
	if len(differences) != len(wantPaths) {
		t.Fatalf("unexpected differences: %#v", differences)
	}
	for index, wantPath := range wantPaths {
		if differences[index].Path != wantPath {
			t.Fatalf(
				"difference %d path = %q, want %q: %#v",
				index,
				differences[index].Path,
				wantPath,
				differences,
			)
		}
	}
}
