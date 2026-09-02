package hosted

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCodeArchiveIsDeterministicAndHonorsAgentIgnore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "agent")
	if err := os.MkdirAll(filepath.Join(source, "__pycache__"), 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"main.py":                      "print('ready')\n",
		"keep.txt":                     "keep\n",
		"ignored.txt":                  "ignore\n",
		".env":                         "SECRET=value\n",
		"__pycache__/main.cpython.pyc": "compiled",
		".agentignore":                 "ignored.txt\n",
	}
	for name, content := range files {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(`name: hosted-project
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
    protocols:
      - protocol: responses
        version: 1.0.0
`), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(root, "agent")
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildCodeArchive(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Remove()
	second, err := BuildCodeArchive(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Remove()
	if first.SHA256 != second.SHA256 {
		t.Fatalf("deterministic archives differ: %s != %s", first.SHA256, second.SHA256)
	}
	reader, err := zip.OpenReader(first.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	found := make(map[string]bool)
	for _, file := range reader.File {
		found[file.Name] = true
	}
	for _, included := range []string{"main.py", "keep.txt"} {
		if !found[included] {
			t.Fatalf("archive omitted %s: %#v", included, found)
		}
	}
	for _, excluded := range []string{"ignored.txt", ".env", ".agentignore", "__pycache__/main.cpython.pyc"} {
		if found[excluded] {
			t.Fatalf("archive included excluded file %s", excluded)
		}
	}
}

func TestBuildCodeArchiveRejectsUnsupportedAgentIgnoreNegation(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".agentignore"), []byte("!main.py\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workspace := Workspace{
		Selected: Service{
			Mode:            DeploymentModeCode,
			SourceDirectory: source,
		},
	}
	if _, err := BuildCodeArchive(workspace); err == nil {
		t.Fatal("expected unsupported .agentignore negation to fail")
	}
}

func TestDeploymentSnapshotHonorsAgentIgnore(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "agent")
	if err := os.MkdirAll(filepath.Join(source, ".agent_configs"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"main.py":                      "print('ready')\n",
		".agentignore":                 "eval.yaml\n.agent_configs/\n",
		"eval.yaml":                    "name: generated\n",
		".agent_configs/baseline.yaml": "version: 1\n",
	} {
		path := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := Workspace{
		Name: "hosted-project",
		Hash: "workspace-hash",
		Selected: Service{
			ServiceName:     "agent",
			AgentName:       "hosted-agent",
			Mode:            DeploymentModeCode,
			SourceDirectory: source,
		},
	}
	before, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.FileCount != 1 {
		t.Fatalf("snapshot included ignored deployment files: %#v", before)
	}
	if err := os.WriteFile(
		filepath.Join(source, "eval.yaml"),
		[]byte("name: regenerated\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	after, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("ignored evaluation output changed deployment snapshot: before=%#v after=%#v", before, after)
	}
}

func TestScaffoldCreatesValidatedWorkspace(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	result, err := Scaffold(ScaffoldOptions{
		Destination: "agent-workspace",
		AgentName:   "support-agent",
		Protocol:    "responses",
		Metadata:    map[string]string{"owner": "platform"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(result.Root, "support-agent")
	if err != nil {
		t.Fatalf("generated workspace is invalid: %v", err)
	}
	if protocols := workspace.Selected.Protocols; len(protocols) != 1 ||
		protocols[0].Version != DefaultProtocolVer {
		t.Fatalf("generated workspace uses incompatible protocols: %#v", protocols)
	}
	if workspace.Selected.Metadata["owner"] != "platform" {
		t.Fatalf("generated workspace omitted metadata: %#v", workspace.Selected.Metadata)
	}
	for _, relative := range result.Files {
		if _, err := os.Stat(filepath.Join(result.Root, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("missing generated file %s: %v", relative, err)
		}
	}
}

func TestScaffoldWiresHostedBingGrounding(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	result, err := Scaffold(ScaffoldOptions{
		Destination:             "bing-agent-workspace",
		AgentName:               "bing-agent",
		Protocol:                "responses",
		BingGroundingConnection: "bing-search",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(result.Root, "bing-agent")
	if err != nil {
		t.Fatalf("generated workspace is invalid: %v", err)
	}
	bing := workspace.Selected.BingGrounding
	if bing == nil ||
		bing.ConnectionName != "${BING_GROUNDING_CONNECTION_NAME}" ||
		!bing.UnresolvedReference {
		t.Fatalf("unexpected generated Bing Grounding runtime: %#v", bing)
	}
	source := filepath.Join(result.Root, "src", "bing-agent")
	checks := map[string][]string{
		filepath.Join(source, "main.py"): {
			"from azure.ai.projects import AIProjectClient",
			"project.connections.get(",
			"FoundryChatClient.get_bing_grounding_tool(",
			"connection_id=connection.id",
		},
		filepath.Join(source, "requirements.txt"): {
			"agent-framework-foundry==1.10.4\n",
			"aiohttp\n",
			"azure-ai-projects\n",
		},
		filepath.Join(source, ".env.example"): {
			"BING_GROUNDING_CONNECTION_NAME=bing-search",
		},
		filepath.Join(result.Root, "azure.yaml"): {
			"BING_GROUNDING_CONNECTION_NAME: ${BING_GROUNDING_CONNECTION_NAME}",
		},
	}
	for path, expected := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range expected {
			if !strings.Contains(string(data), text) {
				t.Fatalf("%s does not contain %q:\n%s", path, text, data)
			}
		}
	}
}

func TestScaffoldWiresHostedBingCustomSearchAndToolbox(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	result, err := Scaffold(ScaffoldOptions{
		Destination:                "tool-agent-workspace",
		AgentName:                  "tool-agent",
		Protocol:                   "responses",
		BingCustomSearchConnection: "bing-custom",
		BingCustomSearchInstance:   "contoso-search",
		ToolboxName:                "operations",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(result.Root, "tool-agent")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.Toolbox == nil ||
		workspace.Selected.Toolbox.ApprovalMode != "${TOOLBOX_APPROVAL_MODE}" ||
		workspace.Selected.BingCustomSearch == nil ||
		workspace.Selected.BingCustomSearch.ConnectionName != "${BING_CUSTOM_SEARCH_CONNECTION_NAME}" ||
		workspace.Selected.BingCustomSearch.InstanceName != "${BING_CUSTOM_SEARCH_INSTANCE_NAME}" {
		t.Fatalf("generated runtime declarations are incomplete: %#v", workspace.Selected)
	}
	source := filepath.Join(result.Root, "src", "tool-agent")
	checks := map[string][]string{
		filepath.Join(source, "main.py"): {
			"FoundryToolbox",
			`toolbox.approval_mode = toolbox_approval_mode`,
			"FoundryChatClient.get_bing_custom_search_tool(",
			`instance_name=os.environ["BING_CUSTOM_SEARCH_INSTANCE_NAME"]`,
		},
		filepath.Join(source, ".env.example"): {
			"BING_CUSTOM_SEARCH_CONNECTION_NAME=bing-custom",
			"BING_CUSTOM_SEARCH_INSTANCE_NAME=contoso-search",
			"TOOLBOX_NAME=operations",
			"TOOLBOX_APPROVAL_MODE=always_require",
		},
		filepath.Join(result.Root, "azure.yaml"): {
			"TOOLBOX_NAME: ${TOOLBOX_NAME}",
			"TOOLBOX_APPROVAL_MODE: ${TOOLBOX_APPROVAL_MODE}",
			"BING_CUSTOM_SEARCH_CONNECTION_NAME: ${BING_CUSTOM_SEARCH_CONNECTION_NAME}",
		},
	}
	for path, expected := range checks {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range expected {
			if !strings.Contains(string(data), text) {
				t.Fatalf("%s does not contain %q:\n%s", path, text, data)
			}
		}
	}
}

func TestScaffoldRejectsToolboxWithoutResponsesProtocol(t *testing.T) {
	_, err := Scaffold(ScaffoldOptions{
		Destination: "unused",
		AgentName:   "tool-agent",
		Protocol:    "invocations",
		ToolboxName: "operations",
	})
	if err == nil {
		t.Fatal("expected Toolbox scaffold to require the responses protocol")
	}
}
