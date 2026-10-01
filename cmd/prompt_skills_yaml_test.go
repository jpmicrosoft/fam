package main

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"foundry-agent-manager/internal/config"
)

func TestPromptSkillLocalRejectsUnsafeYAMLWithoutWriting(t *testing.T) {
	for _, test := range []struct{ name, manifest string }{
		{"inherited-skills", nativePromptManifest("  <<: {skills: [{name: greeting, version: \"1\"}]}\n")},
		{"merge-sequence", nativePromptManifest("  <<: [{skills: [{name: greeting, version: \"1\"}]}]\n")},
		{"merge-with-explicit-skills", nativePromptManifest("  <<: {description: inherited}\n" + nativePromptSkillDeclaration)},
		{"inherited-agent", "apiVersion: foundry-agent-manager/v1\n<<:\n  agent:\n    name: base-agent\n    model: base-model\n    instructions: base instructions\n    skills: [{name: greeting, version: \"1\"}]\n"},
		{"aliased-agent", "apiVersion: foundry-agent-manager/v1\n<<: {agent: &agent {name: base-agent, model: base-model, instructions: base instructions}}\nagent: *agent\n"},
		{"anchored-agent", strings.Replace(nativePromptManifest(nativePromptSkillDeclaration), "agent:\n", "agent: &agent\n", 1)},
		{"anchored-root", "&manifest\n" + nativePromptManifest(nativePromptSkillDeclaration)},
		{"anchored-agent-key", strings.Replace(baseManifest, "agent:\n", "&key agent:\n", 1)},
		{"anchored-skills-key", nativePromptManifest("  &key skills: []\n")},
		{"anchored-sequence", nativePromptManifest("  skills: &skills [{name: greeting, version: \"1\"}]\n")},
		{"anchored-entry", nativePromptManifest("  skills:\n    - &skill {name: greeting, version: \"1\"}\n")},
		{"merged-entry", nativePromptManifest("  skills:\n    - <<: {name: greeting, version: \"1\"}\n")},
		{"aliased-name", nativePromptManifest("  description: &skill greeting\n  skills:\n    - name: *skill\n      version: \"1\"\n")},
		{"aliased-version", nativePromptManifest("  description: &pin \"1\"\n  skills:\n    - name: greeting\n      version: *pin\n")},
		{"anchor-referenced-outside-skills", nativePromptManifest("  skills:\n    - name: greeting\n      version: &pin \"1\"\n  description: *pin\n")},
		{"anchor-referenced-outside-agent", strings.Replace(
			nativePromptManifest("  skills:\n    - name: greeting\n      version: &pin \"1\"\n"),
			"project:\n", "project:\n  description: *pin\n", 1)},
	} {
		for _, operation := range []string{"list", "attach", "remove"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				original := "# preserve this file exactly\n" + test.manifest
				path := writeManifest(t, original)
				// These are valid semantic manifests; the local editor must not
				// mistake inherited declarations for unmanaged/direct entries.
				document, err := config.LoadManifest(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := config.ValidateManifest(document); err != nil {
					t.Fatal(err)
				}
				args := []string{"prompt", "skill", operation, "-f", path}
				if operation != "list" {
					args = append(args, "--skill", "greeting")
				}
				if operation == "attach" {
					args = append(args, "--version", "2")
				}
				run := runCLI(t, "", args...)
				if run.code == 0 || !strings.Contains(run.stderr, "expand") ||
					!strings.Contains(run.stderr, "agent.skills") {
					t.Fatalf("unsafe YAML did not receive actionable rejection: %#v", run)
				}
				current, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(current, []byte(original)) {
					t.Fatalf("rejected %s changed the file:\n%s", operation, current)
				}
			})
		}
	}
}

func TestPromptSkillLocalPreservesUnrelatedYAMLStructures(t *testing.T) {
	content := strings.Replace(baseManifest, "  instructions: base instructions\n",
		"  instructions: &instructions base instructions # keep instructions\n"+
			"  description: *instructions\n"+
			"  metadata:\n    <<: {owner: platform}\n    team: tools # keep metadata\n", 1)
	path := writeManifest(t, content)
	for _, args := range [][]string{
		{"list"},
		{"attach", "--skill", "greeting", "--version", "1"},
		{"list"},
		{"remove", "--skill", "greeting"},
	} {
		run := runCLI(t, "", append([]string{"prompt", "skill"}, append(args, "-f", path)...)...)
		if run.code != 0 {
			t.Fatal(run.stderr)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"&instructions", "*instructions", "# keep instructions", "# keep metadata", "owner: platform", "code_interpreter"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("unrelated YAML structure %q was lost:\n%s", want, data)
		}
	}
	document, err := config.LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateManifest(document); err != nil {
		t.Fatal(err)
	}
	agent := document["agent"].(map[string]interface{})
	if agent["description"] != "base instructions" || agent["metadata"].(map[string]interface{})["owner"] != "platform" {
		t.Fatalf("unrelated YAML semantics changed: %#v", agent)
	}
}
