package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/hosted"
	"foundry-agent-manager/internal/hostedskills"
)

func TestHostedSkillLocalAttachmentLifecycle(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	directory := filepath.Join(root, "skills", "greeting")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: greeting\ndescription: A greeting for tests\n---\nReturn the greeting marker.\n")
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	application := filepath.Join(root, "src", "agent", "main.py")
	before, err := os.ReadFile(application)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"attach", "--path", "skills/greeting"},
		{"list"},
		{"attach", "--skill", "published-policy", "--version", "2"},
		{"remove", "--skill", "published-policy"},
		{"remove", "--path", "skills/greeting"},
	} {
		command := append([]string{"hosted", "skill", args[0], "--workspace", root}, args[1:]...)
		result := runCLI(t, "", command...)
		if result.code != 0 {
			t.Fatalf("%v: %s", command, result.stderr)
		}
	}
	cfg, err := hostedskills.Load(root, "agent")
	if err != nil || cfg == nil || len(cfg.Skills) != 0 || cfg.Language != "python" {
		t.Fatalf("explicit detachment: %#v %v", cfg, err)
	}
	after, err := os.ReadFile(application)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("local configuration editing changed application: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, "SKILL.md")); err != nil {
		t.Fatalf("detach removed user input: %v", err)
	}
}

func TestHostedSkillRejectsUnpinnedOrAmbiguousSources(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	for _, args := range [][]string{
		{"--skill", "greeting"},
		{"--skill", "greeting", "--version", "1", "--path", "skills/greeting"},
		{"--skill", "greeting", "--version", "1", "--mode", "mcp"},
	} {
		command := append([]string{"hosted", "skill", "attach", "--workspace", root}, args...)
		result := runCLI(t, "", command...)
		if result.code == 0 {
			t.Fatalf("accepted invalid source %v", command)
		}
		if _, err := os.Stat(filepath.Join(root, "fam.skills.yaml")); !os.IsNotExist(err) {
			t.Fatalf("failed edit wrote declaration: %v", err)
		}
	}
}

func TestHostedSkillEditingPreservesOtherServicesAndComments(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	path := filepath.Join(root, "fam.skills.yaml")
	initial := "# keep me\napiVersion: foundry-agent-manager/skills/v1\nservices:\n  other:\n    mode: bundle\n    language: dotnet\n    skills: []\n"
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"hosted", "skill", "attach", "--workspace", root, "--skill", "greeting", "--version", "1"}
	first := runCLI(t, "", args...)
	if first.code != 0 {
		t.Fatal(first.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(content, []byte("# keep me")) {
		t.Fatalf("comment not preserved: %v %s", err, content)
	}
	other, err := hostedskills.Load(root, "other")
	if err != nil || other == nil || other.Language != "dotnet" {
		t.Fatalf("other service changed: %#v %v", other, err)
	}
	second := runCLI(t, "", args...)
	if second.code != 0 || !strings.Contains(second.stdout, "changed=false") {
		t.Fatalf("non-idempotent edit: %#v", second)
	}
	current, _ := os.ReadFile(path)
	if !bytes.Equal(content, current) {
		t.Fatal("idempotent edit rewrote configuration")
	}
}

func TestHostedSkillSyncLocalBundleIsOfflineAndDetectsStaleness(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	directory := filepath.Join(root, "skills", "greeting")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("---\nname: greeting\ndescription: A selected greeting\n---\nReturn the selected marker.\n")
	input := filepath.Join(directory, "SKILL.md")
	if err := os.WriteFile(input, content, 0o600); err != nil {
		t.Fatal(err)
	}
	result := runCLI(t, "", "hosted", "skill", "attach", "--workspace", root, "--path", "skills/greeting")
	if result.code != 0 {
		t.Fatal(result.stderr)
	}
	result = runCLI(t, "", "hosted", "skill", "sync", "--workspace", root, "--output", "json")
	if result.code != 0 || !strings.Contains(result.stdout, `"runtimeVerified": false`) {
		t.Fatalf("offline sync failed or claimed runtime verification: %#v", result)
	}
	source := filepath.Join(root, "src", "agent")
	for _, name := range []string{
		filepath.Join("fam_skills", "manifest.json"),
		filepath.Join("fam_skills", "fam_skills_runtime.py"),
		filepath.Join("fam_skills", "greeting", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("missing deployable file %s: %v", name, err)
		}
	}
	cfg, err := hostedskills.Load(root, "agent")
	if err != nil {
		t.Fatal(err)
	}
	options := hostedskills.ValidateOptions{Root: root, SourceDirectory: source, Service: "agent", Config: cfg}
	if _, err := hostedskills.ValidateArtifact(options); err != nil {
		t.Fatal(err)
	}
	workspace, err := hosted.LoadWorkspace(root, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateHostedSkillArtifacts(workspace, "https://account.services.ai.azure.com/api/projects/project"); err != nil {
		t.Fatalf("online endpoint resolution invalidated an offline local bundle: %v", err)
	}
	if err := os.WriteFile(input, append(content, []byte("A changed marker.\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := hostedskills.ValidateArtifact(options); err == nil {
		t.Fatal("changed local content retained artifact readiness")
	}
}

func TestHostedSkillExplicitBundleModeClearsMCPToolbox(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	args := []string{"hosted", "skill", "attach", "--workspace", root, "--skill", "greeting", "--version", "1"}
	result := runCLI(t, "", append(append([]string{}, args...),
		"--mode", "mcp", "--toolbox", "shared-skills", "--toolbox-version", "2")...)
	if result.code != 0 {
		t.Fatal(result.stderr)
	}
	result = runCLI(t, "", append(args, "--mode", "bundle")...)
	if result.code != 0 {
		t.Fatal(result.stderr)
	}
	cfg, err := hostedskills.Load(root, "agent")
	if err != nil || cfg == nil || cfg.Mode != "bundle" || cfg.Toolbox != nil || len(cfg.Skills) != 1 {
		t.Fatalf("mode transition lost content or retained MCP configuration: %#v %v", cfg, err)
	}
}
