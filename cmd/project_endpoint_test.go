package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanAcceptsResourceID(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "agent.yaml")
	content := `apiVersion: foundry-agent-manager/v1
agent:
  name: support-agent
  model: gpt-5-mini
  instructions: Help the user.
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
tools:
  - type: code_interpreter
`
	if err := os.WriteFile(manifest, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	run := runCLI(t, "", "plan", "-f", manifest)
	if run.code != 0 {
		t.Fatalf("expected success, got %d: %s", run.code, run.stderr)
	}
	if !strings.Contains(run.stdout, "support-agent") {
		t.Fatalf("plan output missing agent name:\n%s", run.stdout)
	}
}
