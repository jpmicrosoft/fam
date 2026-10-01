package hosted

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

func TestProjectionChecksExpectedLengthBound(t *testing.T) {
	for _, test := range []struct {
		name, current, expected string
		wantError               bool
	}{
		{"empty", "", "", false},
		{"exact", "source", "source", false},
		{"short", "sourc", "source", true},
		{"extra byte", "source!", "source", true},
		{"empty grows", "!", "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), AzureYAMLFile)
			if err := os.WriteFile(path, []byte(test.current), 0o600); err != nil {
				t.Fatal(err)
			}
			err := checkProjectionBytes(path, []byte(test.expected))
			if (err != nil) != test.wantError {
				t.Fatalf("unexpected comparison result: %v", err)
			}
		})
	}
}

func TestProjectionRejectsFileGrownBeyondExpectedLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), AzureYAMLFile)
	const original = "small validated source"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	// Grow the file without allocating its contents in the test or the reader.
	if err := os.Truncate(path, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := checkProjectionBytes(path, []byte(original)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected a bounded-read rejection, got %v", err)
	}
}

func TestProjectionAllowsExpectedExpandedDocument(t *testing.T) {
	// Resolved YAML may legitimately exceed the per-reference read limit.
	expected := bytes.Repeat([]byte("x"), netcheck.MaxContainedFileBytes+1)
	path := filepath.Join(t.TempDir(), AzureYAMLFile)
	if err := os.WriteFile(path, expected, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checkProjectionBytes(path, expected); err != nil {
		t.Fatalf("projection introduced a new fixed document cap: %v", err)
	}
}

func TestProjectionRejectsReplacedLink(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, AzureYAMLFile)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	for _, file := range []string{path, outside} {
		if err := os.WriteFile(file, []byte("same bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := checkProjectionBytes(path, []byte("same bytes")); !errs.IsKind(err, "security") {
		t.Fatalf("replacement link must not be read as the validated source: %v", err)
	}
}

func TestProjectionRecoveryIgnoreCreatedAndVerified(t *testing.T) {
	workspace := projectionWorkspace(t)
	for attempt := 0; attempt < 2; attempt++ {
		restore, err := MaterializeEntryPoint(workspace)
		if err != nil {
			t.Fatal(err)
		}
		ignore := filepath.Join(projectionRecoveryPath(workspace), ".gitignore")
		data, err := os.ReadFile(ignore)
		if err != nil || string(data) != projectionRecoveryIgnore {
			t.Fatalf("missing exact FAM-owned recovery ignore: %v", err)
		}
		if err := restore(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectionRecoveryIgnoreRejectsConflictingContent(t *testing.T) {
	for _, content := range []string{
		"",
		"*\n", // Safe-looking but not a FAM ownership marker.
		"# operator-owned\n*.tmp\n",
		projectionRecoveryIgnore + "!original.yaml\n",
		strings.Repeat("x", 4096),
	} {
		t.Run(content[:min(len(content), 24)], func(t *testing.T) {
			workspace := projectionWorkspace(t)
			base := projectionRecoveryPath(workspace)
			if err := os.Mkdir(base, 0o700); err != nil {
				t.Fatal(err)
			}
			ignore := filepath.Join(base, ".gitignore")
			if err := os.WriteFile(ignore, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := MaterializeEntryPoint(workspace); err == nil || !strings.Contains(err.Error(), "conflicting or unowned") {
				t.Fatalf("expected recovery-ignore conflict, got %v", err)
			}
			data, err := os.ReadFile(ignore)
			if err != nil || string(data) != content {
				t.Fatalf("conflicting ignore was changed: %v", err)
			}
			data, err = os.ReadFile(workspace.AzureYAML)
			if err != nil || !bytes.Equal(data, beforeProjectionSource(workspace)) {
				t.Fatalf("ignore conflict changed azure.yaml: %v", err)
			}
			entries, err := os.ReadDir(base)
			if err != nil || len(entries) != 1 || entries[0].Name() != ".gitignore" {
				t.Fatalf("ignore conflict retained a lease or copied configuration: %v", err)
			}
		})
	}
}

func TestProjectionRecoveryIgnoreRejectsLinksAndDirectories(t *testing.T) {
	for _, kind := range []string{"link", "directory"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			ignore := filepath.Join(base, ".gitignore")
			if kind == "directory" {
				if err := os.Mkdir(ignore, 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				outside := filepath.Join(t.TempDir(), ".gitignore")
				if err := os.WriteFile(outside, []byte(projectionRecoveryIgnore), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, ignore); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := ensureProjectionRecoveryIgnore(base); err == nil {
				t.Fatal("non-regular recovery ignore was accepted")
			}
		})
	}
}

func TestProjectionRecoveryExcludedFromOrdinaryGitAdd(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	workspace := projectionWorkspace(t)
	// Both the workspace and its recovery sibling are inside this temporary repo.
	repository := filepath.Dir(workspace.Root)
	var environment []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			environment = append(environment, entry)
		}
	}
	emptyConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(emptyConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+emptyConfig)
	runGit := func(args ...string) string {
		t.Helper()
		command := exec.Command(git, args...)
		command.Dir, command.Env = repository, environment
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated git %v failed: %v\n%s", args, err, output)
		}
		return string(output)
	}
	runGit("init", "--quiet", ".")
	restore, err := MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	runGit("-c", "core.autocrlf=false", "-c", "core.safecrlf=false", "add", "--all")
	runGit("ls-files", "--error-unmatch", "--", filepath.Join(filepath.Base(workspace.Root), AzureYAMLFile))
	if files := runGit("ls-files", "--", filepath.Base(projectionRecoveryPath(workspace))); files != "" {
		t.Fatalf("ordinary git add included retained configuration: %s", files)
	}
}

func TestProjectionHardLinkProbeFailureLeavesSourceUntouched(t *testing.T) {
	workspace := projectionWorkspace(t)
	injected := errors.New("hard links unsupported")
	ops := defaultProjectionIO()
	ops.checkHardLinks = func(directory string) error {
		if filepath.Dir(directory) != projectionRecoveryPath(workspace) {
			t.Fatalf("probe was not in the owned recovery directory: %s", directory)
		}
		return injected
	}
	ops.move = func(string, string) error {
		t.Fatal("source displaced after a failed hard-link probe")
		return nil
	}
	_, err := projectConfiguration(workspace, []byte("# projection\n"), ops)
	if !errors.Is(err, injected) || !strings.Contains(err.Error(), "azure.yaml was not changed") {
		t.Fatalf("expected pre-displacement capability failure, got %v", err)
	}
	data, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || !bytes.Equal(data, beforeProjectionSource(workspace)) {
		t.Fatalf("probe failure changed the live source: %v", err)
	}
	// Preparation failure releases the lease, so a supported retry can proceed.
	restore, err := MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionHardLinkProbeCleansUp(t *testing.T) {
	directory := t.TempDir()
	if err := checkProjectionHardLinks(directory); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("capability probe left files behind: %v", err)
	}
}
