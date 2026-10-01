package schema

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSkillsSchemaEmbeddingAndDefensiveCopy(t *testing.T) {
	onDisk, err := os.ReadFile("skills.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	embedded := SkillsBytes()
	if !bytes.Equal(onDisk, embedded) {
		t.Fatal("embedded Hosted Skills schema does not match disk")
	}
	var document map[string]any
	if err := json.Unmarshal(embedded, &document); err != nil {
		t.Fatal(err)
	}
	if document["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatal("unexpected Hosted Skills schema dialect")
	}
	embedded[0] = 'X'
	if !bytes.Equal(onDisk, SkillsBytes()) {
		t.Fatal("SkillsBytes must return an independent copy")
	}
}

func TestSkillsSchemaStrictDeliveryModes(t *testing.T) {
	resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(SkillsBytes()))
	if err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("skills.schema.json", resource); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("skills.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		config string
		valid  bool
	}{
		{"local bundle", `{"mode":"bundle","language":"python","skills":[{"path":"skills/greeting"}]}`, true},
		{"remote bundle", `{"mode":"bundle","language":"dotnet","skills":[{"name":"greeting","version":"1"}]}`, true},
		{"pinned mcp", `{"mode":"mcp","language":"python","toolbox":{"name":"operations","version":"2"},"skills":[{"name":"greeting","version":"1"}]}`, true},
		{"detached mcp", `{"mode":"mcp","language":"dotnet","skills":[]}`, true},
		{"missing pin", `{"mode":"bundle","language":"python","skills":[{"name":"greeting"}]}`, false},
		{"moving pin", `{"mode":"bundle","language":"python","skills":[{"name":"greeting","version":"latest"}]}`, false},
		{"moving case pin", `{"mode":"bundle","language":"python","skills":[{"name":"greeting","version":"DEFAULT"}]}`, false},
		{"local mcp", `{"mode":"mcp","language":"python","toolbox":{"name":"operations","version":"2"},"skills":[{"path":"skills/greeting"}]}`, false},
		{"mixed source", `{"mode":"bundle","language":"python","skills":[{"path":"skills/greeting","name":"greeting","version":"1"}]}`, false},
		{"unknown field", `{"mode":"bundle","language":"python","skills":[],"publish":true}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			instance, err := jsonschema.UnmarshalJSON(bytes.NewBufferString(`{"apiVersion":"foundry-agent-manager/skills/v1","services":{"agent":` + test.config + `}}`))
			if err != nil {
				t.Fatal(err)
			}
			err = compiled.Validate(instance)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v: %v", test.valid, err)
			}
		})
	}
}
