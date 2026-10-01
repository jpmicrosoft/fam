package agentdiff

import (
	"reflect"
	"testing"

	"foundry-agent-manager/internal/skills"
)

func TestPromptSkillsPreserveExistingHarnessWithoutSelectingOne(t *testing.T) {
	for _, configured := range []bool{false, true} {
		remote := remoteAgent()
		remote.Versions.Latest.Definition["skills"] = []skills.Reference{{Name: "greeting", Version: "1"}}
		harness := map[string]interface{}{"type": "github_copilot_preview"}
		remote.Versions.Latest.Definition["harness"] = harness
		desired := Desired{
			Description: "sample", Model: "model-a", Instructions: "be helpful",
			ManageSkills: configured, Skills: []skills.Reference{{Name: "greeting", Version: "1"}},
		}
		effective, err := PreserveSkills(remote, desired)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(effective.Harness, harness) || desired.Harness != nil {
			t.Fatalf("existing harness not preserved independently: %#v", effective)
		}
		comparison, err := Compare(remote, desired)
		if err != nil || comparison.Changed {
			t.Fatalf("preserved harness introduced drift: %#v, %v", comparison, err)
		}
		delete(remote.Versions.Latest.Definition, "harness")
		effective, err = PreserveSkills(remote, desired)
		if err != nil || effective.Harness != nil {
			t.Fatalf("a harness must never be selected automatically: %#v, %v", effective, err)
		}
	}
}

func TestPromptSkillsRejectUnpreservableHarness(t *testing.T) {
	for _, harness := range []interface{}{nil, "github_copilot_preview", map[string]interface{}{}, map[string]interface{}{"type": 1}} {
		remote := remoteAgent()
		remote.Versions.Latest.Definition["harness"] = harness
		if _, err := PreserveSkills(remote, Desired{ManageSkills: true}); err == nil {
			t.Fatalf("invalid harness must not be silently dropped: %#v", harness)
		}
	}
}
