package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPromptSkillLocalLifecyclePreservesOtherContent(t *testing.T) {
	manifest := writeManifest(t, "# keep this comment\n"+baseManifest)
	attach := runCLI(t, "", "prompt", "skill", "attach", "-f", manifest,
		"--skill", "greeting", "--version", "1")
	if attach.code != 0 {
		t.Fatalf("attach: %s", attach.stderr)
	}
	first, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(first, []byte("# keep this comment")) ||
		!bytes.Contains(first, []byte("base instructions")) ||
		!bytes.Contains(first, []byte("code_interpreter")) {
		t.Fatalf("unrelated configuration changed: %s", first)
	}
	again := runCLI(t, "", "prompt", "skill", "attach", "-f", manifest,
		"--skill", "greeting", "--version", "1")
	if again.code != 0 || !strings.Contains(again.stdout, "changed=false") {
		t.Fatalf("repeat attach: %#v", again)
	}
	unchanged, _ := os.ReadFile(manifest)
	if !bytes.Equal(first, unchanged) {
		t.Fatal("an idempotent attachment rewrote the file")
	}
	updated := runCLI(t, "", "prompt", "skill", "attach", "-f", manifest,
		"--skill", "greeting", "--version", "2")
	if updated.code != 0 {
		t.Fatal(updated.stderr)
	}
	listed := runCLI(t, "", "prompt", "skill", "list", "-f", manifest, "--output", "json")
	if listed.code != 0 || !strings.Contains(listed.stdout, `"version": "2"`) {
		t.Fatalf("list: %#v", listed)
	}
	removed := runCLI(t, "", "prompt", "skill", "remove", "-f", manifest, "--skill", "greeting")
	if removed.code != 0 {
		t.Fatal(removed.stderr)
	}
	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatal(err)
	}
	agent := document["agent"].(map[string]interface{})
	values, present := agent["skills"].([]interface{})
	if !present || len(values) != 0 {
		t.Fatalf("remove must explicitly declare an empty managed list: %s", content)
	}
}

func TestPromptSkillLocalEditGuidanceIncludesDualOptIn(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	client := &scriptedHTTP{}
	stubCredentialAndHTTP(t, client)
	for _, args := range [][]string{
		{"attach", "--skill", "greeting", "--version", "1"},
		{"attach", "--skill", "greeting", "--version", "1"},
		{"remove", "--skill", "greeting"},
		{"remove", "--skill", "greeting"},
	} {
		command := append([]string{"prompt", "skill"}, args...)
		run := runCLI(t, "", append(command, "-f", manifest)...)
		if run.code != 0 {
			t.Fatal(run.stderr)
		}
		for _, want := range []string{
			"local configuration only",
			"fam prompt preflight, then fam prompt deploy",
			"both with -f <manifest> --accept-preview --experimental-native-skills",
		} {
			if !strings.Contains(run.stdout, want) {
				t.Fatalf("%v guidance omitted %q: %s", args, want, run.stdout)
			}
		}
	}
	if len(client.requests) != 0 {
		t.Fatal("local edits and their guidance must not make Azure requests")
	}
}

func TestPromptSkillAttachmentPreservesJSON(t *testing.T) {
	var document map[string]interface{}
	if err := yaml.Unmarshal([]byte(baseManifest), &document); err != nil {
		t.Fatal(err)
	}
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, "", "prompt", "skill", "attach", "-f", path,
		"--skill", "greeting", "--version", "1")
	if result.code != 0 {
		t.Fatal(result.stderr)
	}
	content, err = os.ReadFile(path)
	if err != nil || !json.Valid(content) {
		t.Fatalf("JSON configuration became invalid: %v %s", err, content)
	}
	listed := runCLI(t, "", "prompt", "skill", "list", "-f", path, "--output", "json")
	if listed.code != 0 || !strings.Contains(listed.stdout, `"managed": true`) ||
		!strings.Contains(listed.stdout, `"name": "greeting"`) {
		t.Fatalf("JSON list: %#v", listed)
	}
	removed := runCLI(t, "", "prompt", "skill", "remove", "-f", path, "--skill", "greeting")
	if removed.code != 0 {
		t.Fatal(removed.stderr)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("JSON removal produced invalid JSON: %v", err)
	}
	references, ok := document["agent"].(map[string]interface{})["skills"].([]interface{})
	if !ok || len(references) != 0 {
		t.Fatalf("JSON removal must leave an explicit empty list: %s", content)
	}
}

func TestPromptSkillRemoveRejectsUnmanagedConfiguration(t *testing.T) {
	path := writeManifest(t, baseManifest)
	result := runCLI(t, "", "prompt", "skill", "remove", "-f", path, "--skill", "greeting")
	if result.code == 0 || !strings.Contains(result.stderr, "unmanaged") {
		t.Fatalf("unmanaged remote references must not be cleared implicitly: %#v", result)
	}
	content, _ := os.ReadFile(path)
	if string(content) != baseManifest {
		t.Fatal("rejected remove modified configuration")
	}
}

func TestSkillConfigurationRejectsConflictingAndTrailingChanges(t *testing.T) {
	path := writeManifest(t, baseManifest)
	if err := replaceSkillConfiguration(path, []byte("stale"), []byte("replacement")); err == nil {
		t.Fatal("overwrote an externally changed file")
	}
	content, _ := os.ReadFile(path)
	if string(content) != baseManifest {
		t.Fatal("conflict changed the file")
	}
	for _, tail := range []string{"\n---\nagent: {}\n", "\n---\n: [invalid\n"} {
		if err := os.WriteFile(path, []byte(baseManifest+tail), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := readSkillConfiguration(path, false); err == nil {
			t.Fatal("accepted a trailing YAML document")
		}
	}
}
