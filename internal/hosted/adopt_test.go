package hosted

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdoptPythonSourceCopiesExistingAgent(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sourceRelative := ".adopt-source-" + filepath.Base(t.TempDir())
	destinationRelative := ".adopt-workspace-" + filepath.Base(t.TempDir())
	source := filepath.Join(cwd, sourceRelative)
	destination := filepath.Join(cwd, destinationRelative)
	t.Cleanup(func() {
		_ = os.RemoveAll(source)
		_ = os.RemoveAll(destination)
	})
	writeAdoptionSource(t, source, map[string]string{
		"main.py":          "from agent_framework_foundry_hosting import ResponsesHostServer\nResponsesHostServer(agent).run()\n",
		"requirements.txt": "agent-framework-foundry-hosting\n",
		"package/tool.py":  "VALUE = 1\n",
		".env":             "SECRET=do-not-copy\n",
		".venv/secret.txt": "do-not-copy\n",
	})

	result, err := AdoptPythonSource(AdoptOptions{
		Source:               source,
		Destination:          destinationRelative,
		AgentName:            "existing-agent",
		Protocol:             "responses",
		Runtime:              "python_3_13",
		DependencyResolution: "remote_build",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Root != destination || result.InPlace || !result.HostingDetected {
		t.Fatalf("unexpected adoption result: %#v", result)
	}
	if result.EntryPoint != "main.py" ||
		len(result.DependencyFiles) != 1 ||
		result.DependencyFiles[0] != "requirements.txt" {
		t.Fatalf("unexpected source detection: %#v", result)
	}
	adoptedSource := filepath.Join(destination, "src", "existing-agent")
	for _, relative := range []string{"main.py", "requirements.txt", "package/tool.py", ".agentignore", ".env.example"} {
		if _, err := os.Stat(filepath.Join(adoptedSource, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("adopted source omitted %s: %v", relative, err)
		}
	}
	for _, relative := range []string{".env", ".venv/secret.txt"} {
		if _, err := os.Stat(filepath.Join(adoptedSource, filepath.FromSlash(relative))); !os.IsNotExist(err) {
			t.Fatalf("adoption copied excluded path %s: %v", relative, err)
		}
	}
	workspace, err := LoadWorkspace(destination, "existing-agent")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.Source != "src/existing-agent" ||
		workspace.Selected.Code == nil ||
		workspace.Selected.Code.EntryPoint[0] != "main.py" {
		t.Fatalf("unexpected adopted workspace: %#v", workspace.Selected)
	}
	if _, err := os.Stat(filepath.Join(source, ".agentignore")); !os.IsNotExist(err) {
		t.Fatalf("copy adoption modified the source: %v", err)
	}
}

func TestAdoptPythonSourceSupportsInPlace(t *testing.T) {
	source := filepath.Join(t.TempDir(), "existing-agent")
	writeAdoptionSource(t, source, map[string]string{
		"app.py":             "from agent_framework_foundry_hosting import InvocationsHostServer\nInvocationsHostServer(agent).run()\n",
		"pyproject.toml":     "[project]\nname = \"existing-agent\"\n",
		".agentignore":       "custom-cache/\n",
		"custom-cache/a.txt": "ignored\n",
	})

	result, err := AdoptPythonSource(AdoptOptions{
		Source:               source,
		InPlace:              true,
		AgentName:            "existing-agent",
		Protocol:             "invocations",
		Runtime:              "python_3_14",
		DependencyResolution: "bundled",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.InPlace || result.CopiedFiles != 0 || result.EntryPoint != "app.py" {
		t.Fatalf("unexpected in-place result: %#v", result)
	}
	workspace, err := LoadWorkspace(source, "existing-agent")
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Selected.Source != "." ||
		workspace.Selected.Code == nil ||
		workspace.Selected.Code.Runtime != "python_3_14" ||
		workspace.Selected.Code.DependencyResolution != "bundled" {
		t.Fatalf("unexpected in-place workspace: %#v", workspace.Selected)
	}
	ignore, err := os.ReadFile(filepath.Join(source, ".agentignore"))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"custom-cache/", ".env", "azure.yaml"} {
		if !strings.Contains(string(ignore), expected) {
			t.Fatalf("in-place .agentignore omitted %q:\n%s", expected, ignore)
		}
	}
}

func TestAdoptPythonSourceRollsBackInvalidInPlaceIgnore(t *testing.T) {
	source := filepath.Join(t.TempDir(), "existing-agent")
	originalIgnore := "!main.py\n"
	writeAdoptionSource(t, source, map[string]string{
		"main.py":          "print('ready')\n",
		"requirements.txt": "example\n",
		".agentignore":     originalIgnore,
	})

	_, err := AdoptPythonSource(AdoptOptions{
		Source:               source,
		InPlace:              true,
		AgentName:            "existing-agent",
		Protocol:             "responses",
		Runtime:              "python_3_13",
		DependencyResolution: "remote_build",
	})
	if err == nil || !strings.Contains(err.Error(), "rolled back") && !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("expected incompatible .agentignore rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(source, "azure.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("failed in-place adoption retained azure.yaml: %v", statErr)
	}
	restored, readErr := os.ReadFile(filepath.Join(source, ".agentignore"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(restored) != originalIgnore {
		t.Fatalf("in-place rollback changed .agentignore: %q", restored)
	}
	if _, statErr := os.Stat(filepath.Join(source, ".env.example")); !os.IsNotExist(statErr) {
		t.Fatalf("failed in-place adoption retained .env.example: %v", statErr)
	}
}

func TestAdoptPythonSourceRequiresUnambiguousEntrypointAndDependencies(t *testing.T) {
	source := filepath.Join(t.TempDir(), "existing-agent")
	writeAdoptionSource(t, source, map[string]string{
		"one.py": "print('one')\n",
		"two.py": "print('two')\n",
	})
	options := AdoptOptions{
		Source:               source,
		Destination:          "unused",
		AgentName:            "existing-agent",
		Protocol:             "responses",
		Runtime:              "python_3_13",
		DependencyResolution: "remote_build",
	}
	if _, err := AdoptPythonSource(options); err == nil || !strings.Contains(err.Error(), "--entry-point") {
		t.Fatalf("ambiguous entrypoint was not rejected: %v", err)
	}
	options.EntryPoint = "one.py"
	if _, err := AdoptPythonSource(options); err == nil || !strings.Contains(err.Error(), "requirements.txt") {
		t.Fatalf("missing dependency metadata was not rejected: %v", err)
	}
}

func TestAdoptPythonSourceRejectsNonRegularEnvExample(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "existing-agent")
	writeAdoptionSource(t, source, map[string]string{
		"main.py":          "print('ready')\n",
		"requirements.txt": "example\n",
	})
	if err := os.Mkdir(filepath.Join(source, ".env.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	destination := ".adopt-env-example-" + filepath.Base(t.TempDir())
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(cwd, destination)) })

	_, err = AdoptPythonSource(AdoptOptions{
		Source:               source,
		Destination:          destination,
		AgentName:            "existing-agent",
		Protocol:             "responses",
		Runtime:              "python_3_13",
		DependencyResolution: "remote_build",
	})
	if err == nil || !strings.Contains(err.Error(), ".env.example must be a regular file") {
		t.Fatalf("non-regular .env.example was not rejected: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cwd, destination)); !os.IsNotExist(statErr) {
		t.Fatalf("failed adoption retained destination: %v", statErr)
	}
}

func TestResolveAdoptionDestinationRejectsLinkedParentOutsideCurrentDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	linkName := ".adopt-linked-parent-" + filepath.Base(t.TempDir())
	linkPath := filepath.Join(cwd, linkName)
	if err := os.Symlink(external, linkPath); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(linkPath) })

	_, err = resolveAdoptionDestination(filepath.Join(linkName, "workspace"))
	if err == nil || !strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("linked destination parent was not rejected: %v", err)
	}
}

func TestRootIdentityDetectsDirectoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows prevents renaming a directory while the rooted handle is open")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	moved := filepath.Join(parent, "moved")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Rename(source, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := ensureRootMatchesPath(root, source, "--source"); err == nil ||
		!strings.Contains(err.Error(), "changed while adoption was running") {
		t.Fatalf("source directory replacement was not detected: %v", err)
	}
}

func TestReplaceRootFileDoesNotFollowReplacementSymlink(t *testing.T) {
	rootPath := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("external\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(rootPath, ".agentignore")
	if err := os.Symlink(external, target); err != nil {
		t.Skipf("file symlinks are unavailable: %v", err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := replaceRootFile(root, ".agentignore", []byte("managed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalData, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(externalData) != "external\n" {
		t.Fatalf("rooted replacement modified the symlink target: %q", externalData)
	}
	targetInfo, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !targetInfo.Mode().IsRegular() {
		t.Fatalf("replacement did not produce a regular file: %v", targetInfo.Mode())
	}
}

func writeAdoptionSource(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for relative, contents := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
