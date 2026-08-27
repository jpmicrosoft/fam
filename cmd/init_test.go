package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCommandHelpSucceeds(t *testing.T) {
	run := runCLI(t, "", "init", "--help")
	if run.code != 0 {
		t.Fatalf("init --help exited %d: %s", run.code, run.stderr)
	}
	if !strings.Contains(run.stdout, "Usage:") || !strings.Contains(run.stdout, "fam init") {
		t.Fatalf("init --help did not print usage: %q", run.stdout)
	}
}

func TestInitWritesAManifestThatPassesValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", path)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("init did not create %s: %v", path, err)
	}
	validate := runCLI(t, "", "validate", "-f", path)
	if validate.code != 0 {
		t.Fatalf("generated manifest failed validate (%d): %s", validate.code, validate.stderr)
	}
}

func TestInitAppliesSeedFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "",
		"init", "-f", path,
		"--name", "support-agent",
		"--model", "gpt-4o",
		"--description", "Handles support tickets.",
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/contoso/projects/support-project",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	for _, want := range []string{`"agent": "support-agent"`} {
		if !strings.Contains(run.stdout, want) {
			t.Fatalf("init JSON output missing %q: %s", want, run.stdout)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"name: support-agent",
		"model: gpt-4o",
		"description: Handles support tickets.",
		"resource_id:",
		"support-project",
	} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("generated manifest missing %q:\n%s", want, content)
		}
	}
}

func TestInitWritesOptionalRAIPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	const projectID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/contoso/projects/support-project"
	const policyID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/contoso/raiPolicies/custom-policy"
	run := runCLI(t, "",
		"init", "-f", path,
		"--project-resource-id", projectID,
		"--guardrail-policy-id", policyID,
	)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "rai_policy_id: "+policyID) {
		t.Fatalf("generated manifest omitted RAI policy:\n%s", content)
	}
}

func TestInitOmitsRAIPolicyByDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", path)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "rai_policy_id:") {
		t.Fatalf("default Prompt manifest should inherit the model policy:\n%s", content)
	}
}

func TestInitRejectsCrossAccountRAIPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "",
		"init", "-f", path,
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/contoso/projects/support-project",
		"--guardrail-policy-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/other/raiPolicies/custom-policy",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "same Foundry account") {
		t.Fatalf("cross-account RAI policy was not rejected: code=%d stderr=%s", run.code, run.stderr)
	}
}

func TestInitRefusesToOverwriteWithoutForce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if run := runCLI(t, "", "init", "-f", path); run.code != 0 {
		t.Fatalf("first init exited %d: %s", run.code, run.stderr)
	}
	run := runCLI(t, "", "init", "-f", path)
	if run.code != 3 {
		t.Fatalf("re-running init without --force should exit 3 (config), got %d: %s", run.code, run.stderr)
	}
	run = runCLI(t, "", "init", "-f", path, "--force", "--name", "overwritten")
	if run.code != 0 {
		t.Fatalf("init --force exited %d: %s", run.code, run.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "name: overwritten") {
		t.Fatalf("--force did not overwrite the manifest:\n%s", content)
	}
}

func TestInitRejectsADirectoryPath(t *testing.T) {
	dir := t.TempDir()
	run := runCLI(t, "", "init", "-f", dir)
	if run.code != 3 {
		t.Fatalf("init against a directory should exit 3 (config), got %d: %s", run.code, run.stderr)
	}
}

func TestInitCreatesMissingParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "agent.yaml")
	run := runCLI(t, "", "init", "-f", path)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("init did not create nested manifest: %v", err)
	}
}

func TestInitRejectsAzureGovernment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", path, "--cloud", "AzureUSGovernment")
	if run.code != 3 {
		t.Fatalf("expected Azure Government to fail with exit 3, got %d: %s", run.code, run.stderr)
	}
	if !strings.Contains(run.stderr, "dedicated Azure Government subscription") {
		t.Fatalf("unexpected Azure Government rejection: %s", run.stderr)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("init must not write a Government manifest, stat error=%v", err)
	}
}

func TestInitNoToolsOmitsToolsSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", path, "--no-tools")
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "tools:") {
		t.Fatalf("--no-tools must omit the tools section:\n%s", content)
	}
	if run := runCLI(t, "", "validate", "-f", path); run.code != 0 {
		t.Fatalf("a tool-less manifest must still validate (%d): %s", run.code, run.stderr)
	}
}

func TestInitInstructionsFileIsEmbeddedLiterallyAndValidates(t *testing.T) {
	instructionsPath := filepath.Join(t.TempDir(), "instructions.md")
	instructions := "Line one.\nLine two: with a colon.\n# not a yaml comment\n\nBlank line above."
	if err := os.WriteFile(instructionsPath, []byte(instructions), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", manifestPath, "--instructions-file", instructionsPath)
	if run.code != 0 {
		t.Fatalf("init exited %d: %s", run.code, run.stderr)
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(instructions, "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(string(content), line) {
			t.Fatalf("generated manifest is missing instructions line %q:\n%s", line, content)
		}
	}
	if run := runCLI(t, "", "validate", "-f", manifestPath); run.code != 0 {
		t.Fatalf("manifest with embedded instructions failed validate (%d): %s", run.code, run.stderr)
	}
}

func TestInitRejectsAnEmptyInstructionsFile(t *testing.T) {
	instructionsPath := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(instructionsPath, []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(t, "", "init", "-f", manifestPath, "--instructions-file", instructionsPath)
	if run.code != 3 {
		t.Fatalf("an empty --instructions-file should exit 3 (config), got %d: %s", run.code, run.stderr)
	}
	if _, err := os.Stat(manifestPath); err == nil {
		t.Fatal("init must not leave a manifest behind when seeding fails")
	}
}

func TestInitRequiresTheManifestFlag(t *testing.T) {
	run := runCLI(t, "", "init")
	if run.code == 0 {
		t.Fatal("init without --manifest should fail")
	}
}
