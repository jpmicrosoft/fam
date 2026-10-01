package hosted

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/hostedskills"
)

const localSkillSidecar = `apiVersion: foundry-agent-manager/skills/v1
services:
  agent:
    mode: bundle
    language: python
    skills:
      - path: skills/greeting
`

const hostedTestSkill = "---\nname: greeting\ndescription: Test instructions\n---\nUse the greeting instructions.\n"

func writeHostedSkillInputs(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, hostedskills.FileName), []byte(localSkillSidecar), 0o600); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "skills", "greeting")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(hostedTestSkill), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLoadsSkillsAndIncludesDeclarationIdentity(t *testing.T) {
	root := validCodeWorkspace(t)
	before, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if before.Selected.Skills != nil {
		t.Fatal("unconfigured workspace should not manage Skills")
	}
	writeHostedSkillInputs(t, root)
	after, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Selected.Skills == nil || after.Selected.Skills.Mode != hostedskills.ModeBundle ||
		after.Hash == before.Hash || !strings.Contains(strings.Join(after.ReferencedFiles, ","), hostedskills.FileName) {
		t.Fatalf("Hosted Skill declaration omitted from workspace: %#v", after)
	}
	if len(after.ContractWarnings) == 0 {
		t.Fatal("workspace must not imply runtime readiness from a declaration")
	}
}

func TestWorkspaceSkillsLanguageMustMatchCodeRuntime(t *testing.T) {
	root := validCodeWorkspace(t)
	data := strings.Replace(localSkillSidecar, "python", "dotnet", 1)
	if err := os.WriteFile(filepath.Join(root, hostedskills.FileName), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil || !strings.Contains(err.Error(), "language") {
		t.Fatalf("mismatched code language accepted: %v", err)
	}
}

func TestWorkspaceIgnoresAbsentSelectedServiceSkills(t *testing.T) {
	root := validCodeWorkspace(t)
	before, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, hostedskills.FileName), []byte(strings.Replace(localSkillSidecar, "  agent:", "  other:", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	after, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if after.Selected.Skills != nil || after.Hash != before.Hash {
		t.Fatal("another service's declaration changed unmanaged selected service identity")
	}
}
