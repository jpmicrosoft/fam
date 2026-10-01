package hosted

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func entryPointWorkspace(t *testing.T, runtime, entryPoint, dependency string, files map[string]string) string {
	t.Helper()
	return writeWorkspace(t, fmt.Sprintf(`name: code-entry-point
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src
    codeConfiguration:
      runtime: %s
      entryPoint: %s
      dependencyResolution: %s
`, runtime, entryPoint, dependency), files)
}

func TestCodeEntryPointCommands(t *testing.T) {
	tests := []struct {
		name, runtime, declaration, dependency string
		files                                  map[string]string
		want                                   []string
	}{
		{"legacy python", "python_3_13", "main.py", "remote_build", map[string]string{"src/main.py": "pass\n"}, []string{"python", "main.py"}},
		{"legacy array", "python_3_14", "[main.py]", "bundled", map[string]string{"src/main.py": "pass\n"}, []string{"python", "main.py"}},
		{"explicit python", "python_3_13", "[python, main.py]", "remote_build", map[string]string{"src/main.py": "pass\n"}, []string{"python", "main.py"}},
		{"python arguments", "python_3_13", `[python3, -u, -B, app/main.py, --port, "8080", "two words", ""]`, "bundled", map[string]string{"src/app/main.py": "pass\n"}, []string{"python3", "-u", "-B", "app/main.py", "--port", "8080", "two words", ""}},
		{"python option values", "python_3_14", `[python3.14, -W, error, -X, dev, --, main.py]`, "remote_build", map[string]string{"src/main.py": "pass\n"}, []string{"python3.14", "-W", "error", "-X", "dev", "--", "main.py"}},
		{"legacy dotnet", "dotnet_10", "Agent.dll", "bundled", map[string]string{"src/Agent.dll": "published"}, []string{"dotnet", "Agent.dll"}},
		{"dotnet arguments", "dotnet_10", `[dotnet, Agent.dll, --port, "8080"]`, "bundled", map[string]string{"src/Agent.dll": "published"}, []string{"dotnet", "Agent.dll", "--port", "8080"}},
		{"dotnet exec", "dotnet_10", "[dotnet, exec, Agent.dll]", "bundled", map[string]string{"src/Agent.dll": "published"}, []string{"dotnet", "exec", "Agent.dll"}},
		{"dotnet remote source", "dotnet_10", "[dotnet, Custom.dll]", "remote_build", map[string]string{"src/Agent.csproj": "<Project />", "src/Program.cs": "// source"}, []string{"dotnet", "Custom.dll"}},
		{"legacy dotnet remote source", "dotnet_10", "Agent.dll", "remote_build", map[string]string{"src/Agent.csproj": "<Project />"}, []string{"dotnet", "Agent.dll"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := entryPointWorkspace(t, test.runtime, test.declaration, test.dependency, test.files)
			original, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(workspace.Selected.Code.EntryPoint, test.want) {
				t.Fatalf("command = %#v, want %#v", workspace.Selected.Code.EntryPoint, test.want)
			}
			archive, err := BuildCodeArchive(workspace)
			if err != nil {
				t.Fatal(err)
			}
			defer archive.Remove()
			after, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil || !bytes.Equal(original, after) {
				t.Fatalf("offline validation or packaging modified azure.yaml: %v", err)
			}
		})
	}
}

func TestCodeEntryPointRejectsInvalidCommands(t *testing.T) {
	for _, declaration := range []string{
		`""`, "[]", "null", "123", "{}", "[python]", "[python, null]",
		"[python, 123]", "[python, [main.py]]", `[python, ""]`,
		`["python", "main.py", "bad\u0000arg"]`, `["python", "main.py", "bad\narg"]`,
		"[python, -c, 'print(1)']", "[python, -m, application]",
		"[python, -W]", "[python, -X]", "[python, --unknown, main.py]",
		"[bash, -c, 'python main.py']", "[dotnet, main.py]",
		"python main.py", "[main.py, --port]",
		"[python, ../outside.py]", "[python, app/../../outside.py]",
		"[python, /outside.py]", "[python, 'C:/outside.py']",
		"[python, 'app\\main.py']", "[python, 'https://example.test/main.py']",
	} {
		t.Run(declaration, func(t *testing.T) {
			root := entryPointWorkspace(t, "python_3_13", declaration, "remote_build", map[string]string{"src/main.py": "pass\n"})
			if _, err := LoadWorkspace(root, ""); err == nil {
				t.Fatal("invalid entry point accepted")
			}
		})
	}
}

func TestCodeEntryPointRequiresCorrectSourceForDependencyMode(t *testing.T) {
	for _, test := range []struct {
		name, runtime, entryPoint, dependency string
		files                                 map[string]string
	}{
		{"missing python source", "python_3_13", "[python, missing.py]", "remote_build", map[string]string{"src/requirements.txt": ""}},
		{"missing bundled assembly", "dotnet_10", "[dotnet, Agent.dll]", "bundled", map[string]string{"src/Agent.csproj": "<Project />"}},
		{"missing remote project", "dotnet_10", "[dotnet, Agent.dll]", "remote_build", map[string]string{"src/Agent.dll": "published"}},
		{"nested remote project", "dotnet_10", "[dotnet, Agent.dll]", "remote_build", map[string]string{"src/nested/Agent.csproj": "<Project />"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := entryPointWorkspace(t, test.runtime, test.entryPoint, test.dependency, test.files)
			if _, err := LoadWorkspace(root, ""); err == nil {
				t.Fatal("entry point accepted without required source")
			}
		})
	}
}

func TestCodeEntryPointRejectsLinksAndDirectories(t *testing.T) {
	for _, test := range []struct {
		name, runtime, entryPoint, dependency, target string
		directory, link                               bool
	}{
		{"script link", "python_3_13", "[python, main.py]", "remote_build", "main.py", false, true},
		{"script parent link", "python_3_13", "[python, app/main.py]", "bundled", "app", true, true},
		{"script directory", "python_3_13", "[python, main.py]", "remote_build", "main.py", true, false},
		{"assembly link", "dotnet_10", "[dotnet, Agent.dll]", "bundled", "Agent.dll", false, true},
		{"project link", "dotnet_10", "[dotnet, Agent.dll]", "remote_build", "Agent.csproj", false, true},
		{"project directory", "dotnet_10", "[dotnet, Agent.dll]", "remote_build", "Agent.csproj", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := entryPointWorkspace(t, test.runtime, test.entryPoint, test.dependency, map[string]string{"src/keep.txt": "keep"})
			target := filepath.Join(root, "src", test.target)
			if test.link {
				external := t.TempDir()
				if !test.directory {
					external = filepath.Join(external, "target")
				} else if err := os.WriteFile(filepath.Join(external, "main.py"), []byte("pass\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if !test.directory {
					if err := os.WriteFile(external, []byte("external"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(external, target); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadWorkspace(root, ""); err == nil {
				t.Fatal("link or directory accepted as contained code source")
			}
		})
	}
}

func TestCodeEntryPointRequiredFilesCannotBeIgnored(t *testing.T) {
	for _, test := range []struct {
		runtime, entryPoint, dependency, file string
	}{
		{"python_3_13", "[python, main.py]", "remote_build", "main.py"},
		{"dotnet_10", "[dotnet, Agent.dll]", "bundled", "Agent.dll"},
		{"dotnet_10", "[dotnet, Agent.dll]", "remote_build", "Agent.csproj"},
	} {
		t.Run(test.runtime+"/"+test.dependency, func(t *testing.T) {
			root := entryPointWorkspace(t, test.runtime, test.entryPoint, test.dependency, map[string]string{"src/" + test.file: "source"})
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "src", ".agentignore"), []byte(test.file+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil || !strings.Contains(err.Error(), "excluded") {
				t.Fatalf("snapshot accepted excluded entry point source: %v", err)
			}
			if archive, err := BuildCodeArchive(workspace); err == nil {
				archive.Remove()
				t.Fatal("archive accepted excluded entry point source")
			}
		})
	}
}

func TestCodeEntryPointDeletedAfterLoadBlocksPackaging(t *testing.T) {
	root := validCodeWorkspace(t)
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(workspace.Selected.SourceDirectory, "main.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := ComputeDeploymentSnapshot(workspace, "dev"); err == nil {
		t.Fatal("snapshot accepted a missing entry point")
	}
	if archive, err := BuildCodeArchive(workspace); err == nil {
		archive.Remove()
		t.Fatal("archive accepted a missing entry point")
	}
}

func TestCodeEntryPointChangesDeploymentIdentity(t *testing.T) {
	workspace, err := LoadWorkspace(validCodeWorkspace(t), "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	code := workspace.Selected.Code
	workspace.Selected.Code = nil
	legacy, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == legacy.Hash {
		t.Fatal("normalization would reuse a receipt for the old filename-only deployment")
	}
	workspace.Selected.Code = code
	code.EntryPoint = append(code.EntryPoint, "--port", "8080")
	after, err := ComputeDeploymentSnapshot(workspace, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("runtime arguments were omitted from deployment identity")
	}
}

func assertAZDEntryPointFile(t *testing.T, root, service, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	services, ok := asMap(document["services"])
	if !ok {
		t.Fatal("services mapping missing")
	}
	selected, ok := asMap(services[service])
	if !ok {
		t.Fatal("selected service missing")
	}
	code, ok := asMap(selected["codeConfiguration"])
	if !ok {
		t.Fatal("codeConfiguration missing")
	}
	file, ok := code["entryPoint"].(string)
	if !ok || file != want {
		t.Fatalf("azd entryPoint = %#v, want filename %q", code["entryPoint"], want)
	}
}
