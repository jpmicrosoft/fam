package hosted

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaterializeRAIPolicyRendersSelectedServiceAndRestoresAzureYAML(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	original := []byte(`name: hosted-project
services:
  project:
    host: azure.ai.project
  other:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    policies:
      - type: rai_policy
        raiPolicyName: /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/other
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    policies:
      - type: rai_policy
        raiPolicyName: ${RAI_POLICY_ID}
`)
	azureYAML := filepath.Join(root, "azure.yaml")
	if err := os.WriteFile(azureYAML, original, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "agent")
	if err != nil {
		t.Fatal(err)
	}
	const policyID = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/Microsoft.DefaultV2"
	restore, err := MaterializeRAIPolicy(workspace, policyID)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := os.ReadFile(azureYAML)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), "raiPolicyName: "+policyID) ||
		!strings.Contains(string(rendered), "raiPolicyName: /subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/other") {
		t.Fatalf("unexpected rendered azure.yaml:\n%s", rendered)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(azureYAML)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Fatalf("azure.yaml was not restored exactly:\n%s", restored)
	}
	if err := restore(); err != nil {
		t.Fatalf("restore must be idempotent: %v", err)
	}
}
