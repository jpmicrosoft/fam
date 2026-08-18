package hosted

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func writeWorkspace(t *testing.T, azureYAML string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(azureYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func validCodeWorkspace(t *testing.T) string {
	t.Helper()
	return writeWorkspace(t, `name: hosted-project
services:
  project:
    host: azure.ai.project
    endpoint: https://account.services.ai.azure.com/api/projects/project
  agent:
    host: azure.ai.agent
    kind: hosted
    name: hosted-agent
    project: src/agent
    uses: [project]
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
      dependencyResolution: remote_build
    protocols:
      - protocol: responses
        version: 2.0.0
    env:
      MODEL_NAME: ${MODEL_NAME}
    container:
      resources:
        cpu: "1"
        memory: 2Gi
`, map[string]string{"src/agent/main.py": "print('ready')\n"})
}

func TestLoadWorkspaceCodeMode(t *testing.T) {
	root := validCodeWorkspace(t)
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.ServiceName != "agent" ||
		workspace.Selected.AgentName != "hosted-agent" ||
		workspace.Selected.Mode != DeploymentModeCode {
		t.Fatalf("unexpected service: %#v", workspace.Selected)
	}
	if !workspace.ExistingProject || workspace.Selected.ProjectService != "project" {
		t.Fatalf("project resolution failed: %#v", workspace)
	}
	if workspace.Hash == "" || len(workspace.ContractWarnings) != 0 {
		t.Fatalf("expected deterministic hash without contract warnings: %#v", workspace)
	}
}

func TestLoadWorkspaceParsesRAIPolicy(t *testing.T) {
	root := validCodeWorkspace(t)
	path := filepath.Join(root, AzureYAMLFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("    protocols:\n"), []byte(
		"    policies:\n      - type: rai_policy\n        raiPolicyName: ${RAI_POLICY_ID}\n    protocols:\n",
	), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.RAIPolicy == nil ||
		workspace.Selected.RAIPolicy.PolicyID != "${RAI_POLICY_ID}" ||
		!workspace.Selected.RAIPolicy.UnresolvedReference {
		t.Fatalf("unexpected RAI policy: %#v", workspace.Selected.RAIPolicy)
	}
}

func TestLoadWorkspaceRejectsInvalidRAIPolicy(t *testing.T) {
	root := validCodeWorkspace(t)
	path := filepath.Join(root, AzureYAMLFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("    protocols:\n"), []byte(
		"    policies:\n      - type: rai_policy\n        raiPolicyName: Microsoft.DefaultV2\n    protocols:\n",
	), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil ||
		!strings.Contains(err.Error(), "RAI policy resource ID must start with /") {
		t.Fatalf("invalid RAI policy was not rejected: %v", err)
	}
}

func TestLoadWorkspacePreservesHostedMetadata(t *testing.T) {
	root := validCodeWorkspace(t)
	path := filepath.Join(root, AzureYAMLFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("    protocols:\n"), []byte(
		"    metadata:\n      owner: platform\n      authors: [Ada, Grace]\n    protocols:\n",
	), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	authors, ok := workspace.Selected.Metadata["authors"].([]string)
	if workspace.Selected.Metadata["owner"] != "platform" ||
		!ok ||
		len(authors) != 2 ||
		authors[0] != "Ada" {
		t.Fatalf("unexpected Hosted metadata: %#v", workspace.Selected.Metadata)
	}
}

func TestLoadWorkspaceRejectsArrayEntryPointUnsupportedByPinnedExtension(t *testing.T) {
	root := validCodeWorkspace(t)
	path := filepath.Join(root, AzureYAMLFile)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte("entryPoint: main.py"), []byte("entryPoint: [python, main.py]"), 1)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected array entryPoint rejection, got %v", err)
	}
}

func TestLoadWorkspaceRejectsUndefinedServiceDependency(t *testing.T) {
	root := validCodeWorkspace(t)
	data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(string(data), "uses: [project]", "uses: [missing-project]", 1)
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected undefined service dependency rejection, got %v", err)
	}
}

func TestLoadWorkspaceAcceptsScalarEntryPoint(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: dotnet_10
      entryPoint: Agent.dll
      dependencyResolution: bundled
`, map[string]string{"src/agent/Agent.dll": "binary"})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := workspace.Selected.Code.EntryPoint; len(got) != 1 || got[0] != "Agent.dll" {
		t.Fatalf("unexpected entry point: %#v", got)
	}
	if got := workspace.Selected.Protocols; len(got) != 1 ||
		got[0].Name != "invocations" || got[0].Version != "2.0.0" {
		t.Fatalf("unexpected pinned default protocol: %#v", got)
	}
}

func TestLoadWorkspaceRequiresServiceSelection(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  first:
    host: azure.ai.agent
    kind: hosted
    project: src/first
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
  second:
    host: azure.ai.agent
    kind: hosted
    project: src/second
    codeConfiguration:
      runtime: python_3_14
      entryPoint: main.py
`, map[string]string{
		"src/first/main.py":  "print('first')",
		"src/second/main.py": "print('second')",
	})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected explicit service selection, got %v", err)
	}
	workspace, err := LoadWorkspace(root, "second")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.ServiceName != "second" {
		t.Fatalf("selected wrong service: %#v", workspace.Selected)
	}
}

func TestLoadWorkspaceRejectsInvalidImplicitAgentName(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent_service:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`, map[string]string{"src/agent/main.py": "print('ready')"})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected implicit agent name rejection, got %v", err)
	}
}

func TestLoadWorkspaceRequiresOneProjectDependencyWhenMultipleExist(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  first-project:
    host: azure.ai.project
  second-project:
    host: azure.ai.project
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`, map[string]string{"src/agent/main.py": "print('ready')"})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected project ambiguity rejection, got %v", err)
	}
}

func TestLoadWorkspaceResolvesContainedServiceRef(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    project: src/agent
    $ref: agents/agent.yaml
    name: overlay-agent
`, map[string]string{
		"agents/agent.yaml": `kind: hosted
name: ref-agent
codeConfiguration:
  runtime: python_3_13
  entryPoint: main.py
`,
		"src/agent/main.py": "print('ready')",
	})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.AgentName != "overlay-agent" ||
		!containsString(workspace.ReferencedFiles, "agents/agent.yaml") {
		t.Fatalf("local reference was not resolved: %#v", workspace)
	}
}

func TestLoadWorkspaceResolvesNestedListRefs(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  project:
    host: azure.ai.project
    endpoint: https://account.services.ai.azure.com/api/projects/project
    deployments:
      - $ref: definitions/deployment.yaml
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    uses: [project]
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    protocols:
      - $ref: definitions/protocol.yaml
`, map[string]string{
		"definitions/deployment.yaml": `$ref: deployment-base.yaml
name: chat
`,
		"definitions/deployment-base.yaml": `model:
  format: OpenAI
  name: model
  version: "1"
sku:
  name: Standard
  capacity: 1
`,
		"definitions/protocol.yaml": `protocol: invocations_ws
version: 1.0.0
`,
		"src/agent/main.py": "print('ready')",
	})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"definitions/deployment.yaml",
		"definitions/deployment-base.yaml",
		"definitions/protocol.yaml",
	} {
		if !containsString(workspace.ReferencedFiles, expected) {
			t.Fatalf("missing referenced file %q in %#v", expected, workspace.ReferencedFiles)
		}
	}
	if got := workspace.Selected.Protocols[0].Name; got != "invocations_ws" {
		t.Fatalf("nested protocol ref was not resolved: %q", got)
	}
}

func TestLoadWorkspaceRejectsServiceRefCoreFields(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    $ref: agents/agent.yaml
`, map[string]string{
		"agents/agent.yaml": `kind: hosted
project: src/agent
codeConfiguration:
  runtime: python_3_13
  entryPoint: main.py
`,
		"src/agent/main.py": "print('ready')",
	})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected core field rejection, got %v", err)
	}
}

func TestLoadWorkspaceRejectsCyclicRefs(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    protocols:
      - $ref: definitions/first.yaml
`, map[string]string{
		"definitions/first.yaml":  "$ref: second.yaml\n",
		"definitions/second.yaml": "$ref: first.yaml\n",
		"src/agent/main.py":       "print('ready')",
	})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected cyclic reference rejection, got %v", err)
	}
}

func TestLoadWorkspaceBoundsReferencedFileCount(t *testing.T) {
	files := map[string]string{"src/agent/main.py": "print('ready')"}
	var deployments strings.Builder
	for i := 0; i < maxReferencedFiles; i++ {
		name := "definitions/deployment-" + strings.Repeat("0", 3-len(strconv.Itoa(i))) + strconv.Itoa(i) + ".yaml"
		deployments.WriteString("      - $ref: " + name + "\n")
		files[name] = `name: deployment
model:
  format: OpenAI
  name: model
  version: "1"
sku:
  name: Standard
  capacity: 1
`
	}
	root := writeWorkspace(t, `name: hosted-project
services:
  project:
    host: azure.ai.project
    deployments:
`+deployments.String()+`  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`, files)
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected reference count rejection, got %v", err)
	}
}

func TestLoadWorkspaceRejectsRemoteRefAndTraversal(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "remote ref",
			yaml: `name: project
services:
  agent:
    host: azure.ai.agent
    $ref: https://example.test/agent.yaml
`,
		},
		{
			name: "nested remote ref",
			yaml: `name: project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    protocols:
      - $ref: https://example.test/protocol.yaml
`,
		},
		{
			name: "source traversal",
			yaml: `name: project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: ../outside
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeWorkspace(t, tt.yaml, nil)
			if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "security") {
				t.Fatalf("expected security error, got %v", err)
			}
		})
	}
}

func TestLoadWorkspaceRejectsDirectoryLinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "main.py"), []byte("print('outside')"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("directory links are unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(`name: project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: linked
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected linked directory escape rejection, got %v", err)
	}
}

func TestLoadWorkspaceRejectsUnverifiedAndReservedConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		with    string
	}{
		{
			name:    "unsupported protocol",
			replace: "protocol: responses",
			with:    "protocol: mcp",
		},
		{
			name:    "reserved environment",
			replace: "MODEL_NAME: ${MODEL_NAME}",
			with:    "FOUNDRY_PROJECT_ENDPOINT: https://override.example",
		},
		{
			name:    "resources above documented range",
			replace: "cpu: \"1\"\n        memory: 2Gi",
			with:    "cpu: \"4.1\"\n        memory: 8Gi",
		},
		{
			name:    "obsolete environment field",
			replace: "env:\n      MODEL_NAME: ${MODEL_NAME}",
			with:    "environmentVariables:\n      MODEL_NAME: value",
		},
		{
			name:    "invalid agent name",
			replace: "name: hosted-agent",
			with:    "name: hosted_agent",
		},
	}
	baseData, err := os.ReadFile(filepath.Join(validCodeWorkspace(t), AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := strings.Replace(string(baseData), tt.replace, tt.with, 1)
			root := writeWorkspace(t, contents, map[string]string{"src/agent/main.py": "print('ready')"})
			if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
				t.Fatalf("expected manifest error, got %v", err)
			}
		})
	}
}

func TestLoadWorkspaceAcceptsDocumentedResourceRangeAndWebSocketProtocol(t *testing.T) {
	root := validCodeWorkspace(t)
	data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(string(data), "protocol: responses", "protocol: invocations_ws", 1)
	contents = strings.Replace(contents, "cpu: \"1\"\n        memory: 2Gi", "cpu: \"4\"\n        memory: 8Gi", 1)
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.Resources.CPU != "4" ||
		workspace.Selected.Resources.Memory != "8Gi" ||
		workspace.Selected.Protocols[0].Name != "invocations_ws" {
		t.Fatalf("documented configuration was not preserved: %#v", workspace.Selected)
	}
}

func TestLoadWorkspaceRecognizesToolboxRuntimeConfiguration(t *testing.T) {
	tests := []struct {
		name         string
		env          string
		wantName     string
		wantEndpoint string
		unresolved   bool
	}{
		{
			name:     "literal name",
			env:      "      TOOLBOX_NAME: operations\n",
			wantName: "operations",
		},
		{
			name:       "azd name reference",
			env:        "      TOOLBOX_NAME: ${TOOLBOX_NAME}\n",
			wantName:   "${TOOLBOX_NAME}",
			unresolved: true,
		},
		{
			name: "literal same-project endpoint",
			env: "      TOOLBOX_ENDPOINT: " +
				"https://account.services.ai.azure.com/api/projects/project/toolboxes/operations/mcp?api-version=v1\n",
			wantEndpoint: "https://account.services.ai.azure.com/api/projects/project/toolboxes/operations/mcp?api-version=v1",
		},
		{
			name:         "azd endpoint reference",
			env:          "      TOOLBOX_ENDPOINT: ${TOOLBOX_ENDPOINT}\n",
			wantEndpoint: "${TOOLBOX_ENDPOINT}",
			unresolved:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := validCodeWorkspace(t)
			data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil {
				t.Fatal(err)
			}
			contents := strings.Replace(
				string(data),
				"      MODEL_NAME: ${MODEL_NAME}\n",
				"      MODEL_NAME: ${MODEL_NAME}\n"+tt.env,
				1,
			)
			if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			toolbox := workspace.Selected.Toolbox
			if toolbox == nil ||
				toolbox.Name != tt.wantName ||
				toolbox.Endpoint != tt.wantEndpoint ||
				toolbox.UnresolvedReference != tt.unresolved ||
				!toolbox.RuntimeApprovalRequired ||
				toolbox.ApprovalMode != "always_require" {
				t.Fatalf("unexpected Toolbox runtime: %#v", toolbox)
			}
		})
	}
}

func TestLoadWorkspaceRecognizesToolboxApprovalAndBingCustomSearch(t *testing.T) {
	root := validCodeWorkspace(t)
	data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(
		string(data),
		"      MODEL_NAME: ${MODEL_NAME}\n",
		"      MODEL_NAME: ${MODEL_NAME}\n"+
			"      TOOLBOX_NAME: operations\n"+
			"      TOOLBOX_APPROVAL_MODE: never_require\n"+
			"      BING_CUSTOM_SEARCH_CONNECTION_NAME: bing-custom\n"+
			"      BING_CUSTOM_SEARCH_INSTANCE_NAME: contoso-search\n",
		1,
	)
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.Toolbox == nil ||
		workspace.Selected.Toolbox.ApprovalMode != "never_require" ||
		workspace.Selected.Toolbox.RuntimeApprovalRequired {
		t.Fatalf("unexpected Toolbox approval mode: %#v", workspace.Selected.Toolbox)
	}
	if workspace.Selected.BingCustomSearch == nil ||
		workspace.Selected.BingCustomSearch.ConnectionName != "bing-custom" ||
		workspace.Selected.BingCustomSearch.InstanceName != "contoso-search" {
		t.Fatalf("unexpected Bing Custom Search runtime: %#v", workspace.Selected.BingCustomSearch)
	}
}

func TestLoadWorkspaceRejectsUnsafeToolboxRuntimeConfiguration(t *testing.T) {
	tests := []struct {
		name string
		env  string
	}{
		{
			name: "both variables",
			env: "      TOOLBOX_NAME: operations\n" +
				"      TOOLBOX_ENDPOINT: ${TOOLBOX_ENDPOINT}\n",
		},
		{
			name: "external endpoint",
			env: "      TOOLBOX_ENDPOINT: " +
				"https://attacker.example/toolboxes/operations/mcp?api-version=v1\n",
		},
		{
			name: "malformed name",
			env:  "      TOOLBOX_NAME: ../operations\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := validCodeWorkspace(t)
			data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil {
				t.Fatal(err)
			}
			contents := strings.Replace(
				string(data),
				"      MODEL_NAME: ${MODEL_NAME}\n",
				"      MODEL_NAME: ${MODEL_NAME}\n"+tt.env,
				1,
			)
			if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWorkspace(root, ""); err == nil {
				t.Fatal("expected unsafe Toolbox runtime configuration to be rejected")
			}
		})
	}
}

func TestLoadWorkspaceRecognizesBingGroundingRuntimeConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		unresolved bool
	}{
		{name: "literal connection", value: "bing-search"},
		{name: "azd connection reference", value: "${BING_GROUNDING_CONNECTION_NAME}", unresolved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := validCodeWorkspace(t)
			data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil {
				t.Fatal(err)
			}
			contents := strings.Replace(
				string(data),
				"      MODEL_NAME: ${MODEL_NAME}\n",
				"      MODEL_NAME: ${MODEL_NAME}\n"+
					"      BING_GROUNDING_CONNECTION_NAME: "+tt.value+"\n",
				1,
			)
			if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			bing := workspace.Selected.BingGrounding
			if bing == nil ||
				bing.ConnectionName != tt.value ||
				bing.UnresolvedReference != tt.unresolved {
				t.Fatalf("unexpected Bing Grounding runtime: %#v", bing)
			}
		})
	}
}

func TestLoadWorkspaceRejectsEmptyBingGroundingConnection(t *testing.T) {
	root := validCodeWorkspace(t)
	data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	contents := strings.Replace(
		string(data),
		"      MODEL_NAME: ${MODEL_NAME}\n",
		"      MODEL_NAME: ${MODEL_NAME}\n"+
			"      BING_GROUNDING_CONNECTION_NAME: \"\"\n",
		1,
	)
	if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspace(root, ""); err == nil {
		t.Fatal("expected empty Bing Grounding connection to be rejected")
	}
}

func TestLoadWorkspaceRejectsNonFiniteResources(t *testing.T) {
	for _, replacement := range []string{"cpu: .nan", `cpu: "1e0"`} {
		root := validCodeWorkspace(t)
		data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
		if err != nil {
			t.Fatal(err)
		}
		contents := strings.Replace(string(data), `cpu: "1"`, replacement, 1)
		if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
			t.Fatalf("expected resource %q rejection, got %v", replacement, err)
		}
	}
}

func TestLoadWorkspaceAcceptsDeprecatedConfigShapeWithWarning(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    project: src/agent
    config:
      kind: hosted
      name: config-agent
      codeConfiguration:
        runtime: python_3_13
        entryPoint: main.py
      environmentVariables:
        - name: MODEL_NAME
          value: ${MODEL_NAME}
`, map[string]string{"src/agent/main.py": "print('ready')"})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.AgentName != "config-agent" ||
		!containsString(workspace.Selected.EnvironmentNames, "MODEL_NAME") {
		t.Fatalf("deprecated config was not resolved: %#v", workspace.Selected)
	}
	if !containsSubstring(workspace.ContractWarnings, "deprecated config-nested") {
		t.Fatalf("missing migration warning: %#v", workspace.ContractWarnings)
	}
}

func TestLoadWorkspaceRejectsExecutableHooks(t *testing.T) {
	tests := []string{
		`name: hosted-project
hooks:
  predeploy:
    shell: echo unsafe
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`,
		`name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    hooks:
      predeploy:
        shell: echo unsafe
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`,
	}
	for _, contents := range tests {
		root := writeWorkspace(t, contents, map[string]string{"src/agent/main.py": "print('ready')"})
		if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected hook rejection, got %v", err)
		}
	}
}

func TestLoadWorkspaceRejectsUntrustedProjectEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"https://attacker.example/api/projects/project",
		"https://account.services.ai.azure.us/api/projects/project",
		"https://account.services.ai.azure.com:444/api/projects/project",
		"https://account.services.ai.azure.com/api/projects/project/extra",
		"https://account.services.ai.azure.com/api/projects/project%2Fextra",
		"https://account.services.ai.azure.com/api/projects/project%00name",
		"https://account.services.ai.azure.com/api/projects/project?redirect=1",
		"https://account.services.ai.azure.com/api/projects/project#fragment",
		"https://user:password@account.services.ai.azure.com/api/projects/project",
	} {
		root := validCodeWorkspace(t)
		data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
		if err != nil {
			t.Fatal(err)
		}
		contents := strings.Replace(
			string(data),
			"https://account.services.ai.azure.com/api/projects/project",
			endpoint,
			1,
		)
		if err := os.WriteFile(filepath.Join(root, AzureYAMLFile), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected endpoint %q to be rejected, got %v", endpoint, err)
		}
	}
}

func TestContainerModeAcceptsContainedDockerPaths(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
    docker:
      path: docker/Agent.Dockerfile
      context: docker
`, map[string]string{
		"src/agent/docker/Agent.Dockerfile": "FROM scratch\n",
	})
	if _, err := LoadWorkspace(root, ""); err != nil {
		t.Fatal(err)
	}
}

func TestContainerModeRequiresDockerfile(t *testing.T) {
	root := writeWorkspace(t, `name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src/agent
`, map[string]string{"src/agent/main.py": "print('ready')"})
	if _, err := LoadWorkspace(root, ""); err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected Dockerfile requirement, got %v", err)
	}
}

func TestValidateEnvironmentNameRejectsArgumentInjection(t *testing.T) {
	for _, value := range []string{"--all", "-e", "bad value", "../prod"} {
		if err := ValidateEnvironmentName(value); err == nil {
			t.Fatalf("expected %q to be rejected", value)
		}
	}
	if err := ValidateEnvironmentName("prod-west.1"); err != nil {
		t.Fatalf("valid environment rejected: %v", err)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, expected string) bool {
	for _, value := range values {
		if strings.Contains(value, expected) {
			return true
		}
	}
	return false
}
