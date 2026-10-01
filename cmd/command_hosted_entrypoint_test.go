package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"foundry-agent-manager/internal/hosted"

	"gopkg.in/yaml.v3"
)

func TestHostedDraftEntryPointPayload(t *testing.T) {
	tests := []struct {
		name, runtime, declaration, dependency string
		files                                  map[string]string
		want                                   []string
	}{
		{"legacy python", "python_3_13", "main.py", "remote_build", map[string]string{"main.py": "pass\n"}, []string{"python", "main.py"}},
		{"legacy array", "python_3_14", "[main.py]", "bundled", map[string]string{"main.py": "pass\n"}, []string{"python", "main.py"}},
		{"explicit command", "python_3_13", `[python, -u, main.py, --port, "8080"]`, "bundled", map[string]string{"main.py": "pass\n"}, []string{"python", "-u", "main.py", "--port", "8080"}},
		{"dotnet bundled", "dotnet_10", "Agent.dll", "bundled", map[string]string{"Agent.dll": "published"}, []string{"dotnet", "Agent.dll"}},
		{"dotnet remote build", "dotnet_10", "[dotnet, Agent.dll]", "remote_build", map[string]string{"Agent.csproj": "<Project />"}, []string{"dotnet", "Agent.dll"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "src"), 0o700); err != nil {
				t.Fatal(err)
			}
			for name, content := range test.files {
				if err := os.WriteFile(filepath.Join(root, "src", name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			declaration := fmt.Sprintf(`name: launch-test
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src
    codeConfiguration:
      runtime: %s
      entryPoint: %s
      dependencyResolution: %s
`, test.runtime, test.declaration, test.dependency)
			if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(declaration), 0o600); err != nil {
				t.Fatal(err)
			}
			workspace, err := hosted.LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			assertHostedDraftEntryPoint(t, workspace, test.want)
		})
	}
}

func TestHostedScaffoldDraftEntryPointPayload(t *testing.T) {
	t.Chdir(t.TempDir())
	result, err := hosted.Scaffold(hosted.ScaffoldOptions{
		Destination: "workspace", AgentName: "agent", Protocol: "responses", NoGuardrail: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := hosted.LoadWorkspace(result.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	assertHostedDraftEntryPoint(t, workspace, []string{"python", "main.py"})
}

func assertHostedDraftEntryPoint(t *testing.T, workspace hosted.Workspace, want []string) {
	t.Helper()
	archive, err := hosted.BuildCodeArchive(workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Remove()
	definition := hostedDraftDefinition(workspace, nil, "")
	encoded, err := json.Marshal(map[string]any{"draft": true, "definition": definition})
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Definition struct {
			Code struct {
				Runtime              string   `json:"runtime"`
				EntryPoint           []string `json:"entry_point"`
				DependencyResolution string   `json:"dependency_resolution"`
			} `json:"code_configuration"`
		} `json:"definition"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(wire.Definition.Code.EntryPoint, want) ||
		wire.Definition.Code.Runtime != workspace.Selected.Code.Runtime ||
		wire.Definition.Code.DependencyResolution != workspace.Selected.Code.DependencyResolution {
		t.Fatalf("incorrect REST draft code configuration: %s", encoded)
	}
	code, ok := definition["code_configuration"].(map[string]any)
	if !ok {
		t.Fatal("draft code_configuration missing")
	}
	args, ok := code["entry_point"].([]string)
	if !ok {
		t.Fatal("draft entry_point is not an argument array")
	}
	args[0] = "changed"
	if !slices.Equal(workspace.Selected.Code.EntryPoint, want) {
		t.Fatal("draft payload aliases the validated workspace command")
	}
}

func TestHostedDeployEntryPointMaterializesAndRestores(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed=%t", fail), func(t *testing.T) {
			root := writeHostedLifecycleWorkspace(t, true)
			original, err := os.ReadFile(filepath.Join(root, "azure.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			fake := &hostedCommandFakeRunner{failDeploy: fail, advance: !fail}
			observed := false
			oldLookPath, oldRunner := hostedLookPathFn, newHostedRunnerFn
			hostedLookPathFn = func(string) (string, error) { return "azd", nil }
			newHostedRunnerFn = func() hosted.Runner {
				return hostedRunnerFunc(func(command hosted.Command) (hosted.Execution, error) {
					if command.Phase == "deploy" || command.Phase == "doctor" {
						observed = true
						data, err := os.ReadFile(filepath.Join(root, "azure.yaml"))
						if err != nil {
							t.Fatal(err)
						}
						var document struct {
							Services map[string]struct {
								Code struct {
									EntryPoint string `yaml:"entryPoint"`
								} `yaml:"codeConfiguration"`
							} `yaml:"services"`
						}
						if err := yaml.Unmarshal(data, &document); err != nil {
							t.Fatalf("azd received a non-scalar entry point: %v", err)
						}
						if document.Services["agent"].Code.EntryPoint != "main.py" {
							t.Fatalf("azd did not receive the filename-only contract: %s", data)
						}
					}
					return fake.Run(context.Background(), command)
				})
			}
			t.Cleanup(func() {
				hostedLookPathFn, newHostedRunnerFn = oldLookPath, oldRunner
			})
			run := runCLI(t, "", "hosted-deploy", "--workspace", root, "--environment", "prod",
				"--accept-preview", "--no-guardrail", "--receipt", filepath.Join(t.TempDir(), "receipt.json"), "--output", "json")
			if !observed || (run.code != 0) != fail {
				t.Fatalf("unexpected deploy outcome: observed=%t result=%#v", observed, run)
			}
			restored, err := os.ReadFile(filepath.Join(root, "azure.yaml"))
			if err != nil || !bytes.Equal(original, restored) {
				t.Fatalf("deploy did not restore original scalar declaration: %v", err)
			}
		})
	}
}
