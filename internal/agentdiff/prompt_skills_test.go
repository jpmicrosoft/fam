package agentdiff

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"foundry-agent-manager/internal/skills"
)

func TestPromptSkillsDiffAndHash(t *testing.T) {
	references := []skills.Reference{{Name: "greeting", Version: "1"}, {Name: "review", Version: "2"}}
	for _, test := range []struct {
		name    string
		manage  bool
		skills  []skills.Reference
		changed bool
	}{
		{name: "omitted-preserves"},
		{name: "equal", manage: true, skills: references},
		{name: "clear", manage: true, skills: []skills.Reference{}, changed: true},
		{name: "new-version", manage: true, skills: []skills.Reference{{Name: "greeting", Version: "3"}}, changed: true},
		{name: "order", manage: true, skills: []skills.Reference{references[1], references[0]}, changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			remote := remoteAgent()
			remote.Versions.Latest.Definition["skills"] = references
			desired := Desired{
				Description: "sample", Model: "model-a", Instructions: "be helpful",
				Skills: test.skills, ManageSkills: test.manage,
			}
			result, err := Compare(remote, desired)
			if err != nil {
				t.Fatal(err)
			}
			if result.Changed != test.changed || (result.CurrentHash != result.DesiredHash) != test.changed {
				t.Fatalf("unexpected diff/hash: %#v", result)
			}
			if test.changed && (len(result.Differences) != 1 || result.Differences[0].Path != "$.definition.skills") {
				t.Fatalf("unexpected differences: %#v", result.Differences)
			}
		})
	}
}

func TestPromptSkillsOmitAndClearWireShape(t *testing.T) {
	for _, configured := range []bool{false, true} {
		value := DesiredValue(Desired{ManageSkills: configured})
		definition := value["definition"].(map[string]interface{})
		references, present := definition["skills"]
		if present != configured {
			t.Fatalf("present = %t for configured = %t", present, configured)
		}
		if configured && !reflect.DeepEqual(references, []interface{}{}) {
			t.Fatalf("clear must serialize as [], got %#v", references)
		}
	}
	result, err := Compare(remoteAgent(), Desired{
		Description: "sample", Model: "model-a", Instructions: "be helpful", ManageSkills: true,
	})
	if err != nil || result.Changed {
		t.Fatalf("absent remote skills should equal explicit clear: %#v, %v", result, err)
	}
}

func TestPromptSkillsPreservedWhenOtherFieldChanges(t *testing.T) {
	remote := remoteAgent()
	remote.Versions.Latest.Definition["skills"] = []interface{}{
		map[string]interface{}{"name": "greeting", "version": "1"},
	}
	desired := Desired{Description: "sample", Model: "new-model", Instructions: "be helpful"}
	effective, err := PreserveSkills(remote, desired)
	if err != nil {
		t.Fatal(err)
	}
	if desired.ManageSkills || desired.Skills != nil {
		t.Fatal("preservation changed the original declarative intent")
	}
	data, err := json.Marshal(DesiredValue(effective))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"skills":[{"name":"greeting","version":"1"}]`) ||
		strings.Contains(string(data), "skill_reference") {
		t.Fatalf("unexpected native wire shape: %s", data)
	}
	result, err := Compare(remote, desired)
	if err != nil || len(result.Differences) != 1 || result.Differences[0].Path != "$.definition.model" {
		t.Fatalf("preservation created skill drift: %#v, %v", result, err)
	}
}

func TestPromptSkillsCannotSilentlyDropUnknownRemoteReferences(t *testing.T) {
	for _, value := range []interface{}{
		nil,
		"invalid",
		[]interface{}{map[string]interface{}{"name": "greeting"}},
		[]interface{}{map[string]interface{}{"name": "greeting", "version": "1", "type": "skill_reference"}},
		[]skills.Reference{{Name: "greeting", Version: "1"}, {Name: "greeting", Version: "2"}},
	} {
		remote := remoteAgent()
		remote.Versions.Latest.Definition["skills"] = value
		if _, err := Compare(remote, Desired{}); err == nil {
			t.Fatalf("invalid remote native references were silently dropped: %#v", value)
		}
	}
}
