package hosted

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAZDEntryPointRejectsCommandsItCannotPreserve(t *testing.T) {
	for _, declaration := range []string{
		"[python, -u, main.py]", "[python3, main.py]", "[python, main.py, --port, '8080']",
	} {
		t.Run(declaration, func(t *testing.T) {
			root := entryPointWorkspace(t, "python_3_13", declaration, "remote_build", map[string]string{"src/main.py": "pass\n"})
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(workspace.AzureYAML)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := MaterializeDeployment(workspace, ""); err == nil || !strings.Contains(err.Error(), "preserve explicit command arguments") {
				t.Fatalf("expected explicit compatibility error, got %v", err)
			}
			after, err := os.ReadFile(workspace.AzureYAML)
			if err != nil || string(after) != string(before) {
				t.Fatalf("unsupported command changed the workspace: %v", err)
			}
		})
	}
}

func TestDoctorMaterializesFilenameAndRestores(t *testing.T) {
	for _, fail := range []bool{false, true} {
		root := entryPointWorkspace(t, "python_3_13", "[python, main.py]", "remote_build", map[string]string{"src/main.py": "pass\n"})
		workspace, err := LoadWorkspace(root, "")
		if err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(workspace.AzureYAML)
		if err != nil {
			t.Fatal(err)
		}
		called := false
		runner := &fakeRunner{run: func(command Command) (Execution, error) {
			called = true
			assertAZDEntryPointFile(t, root, "agent", "main.py")
			result := Execution{Stdout: "Developer has required role on Foundry project\n"}
			if fail {
				result.ExitCode = 1
				return result, errors.New("doctor failed")
			}
			return result, nil
		}}
		_, err = RunDoctor(context.Background(), runner, "azd", workspace, "test", nil)
		if !called || (err != nil) != fail {
			t.Fatalf("unexpected doctor result: called=%v err=%v", called, err)
		}
		after, readErr := os.ReadFile(workspace.AzureYAML)
		if readErr != nil || string(after) != string(before) {
			t.Fatalf("doctor did not restore the exact workspace: %v", readErr)
		}
	}
}

func TestMaterializeEntryPointPreservesConcurrentChanges(t *testing.T) {
	root := entryPointWorkspace(t, "python_3_13", "[python, main.py]", "remote_build", map[string]string{"src/main.py": "pass\n"})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	restore, err := MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatal(err)
	}
	const replacement = "# concurrent operator change\n"
	if err := os.WriteFile(workspace.AzureYAML, []byte(replacement), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err == nil || !strings.Contains(err.Error(), "concurrent edits") {
		t.Fatalf("expected restoration conflict, got %v", err)
	}
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != replacement {
		t.Fatalf("concurrent changes were overwritten: %v", err)
	}
	assertRecoveryContains(t, workspace, beforeProjectionSource(workspace))
}

func beforeProjectionSource(workspace Workspace) []byte {
	return workspace.configurationFiles[AzureYAMLFile]
}

func projectionWorkspace(t *testing.T) Workspace {
	t.Helper()
	root := entryPointWorkspace(t, "python_3_13", "[python, main.py]", "remote_build", map[string]string{"src/main.py": "pass\n"})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(projectionRecoveryPath(workspace)); err != nil {
			t.Error(err)
		}
	})
	return workspace
}

func assertRecoveryContains(t *testing.T, workspace Workspace, want []byte) {
	t.Helper()
	found := false
	err := filepath.WalkDir(projectionRecoveryPath(workspace), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) == string(want) {
			found = true
		}
		return nil
	})
	if err != nil || !found {
		t.Fatalf("recovery does not contain exact bytes %q: %v", want, err)
	}
}

func TestMaterializeRejectsStaleSource(t *testing.T) {
	workspace := projectionWorkspace(t)
	original := beforeProjectionSource(workspace)
	edited := append([]byte("# edited after validation\r\n"), original...)
	if err := os.WriteFile(workspace.AzureYAML, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeEntryPoint(workspace); err == nil || !strings.Contains(err.Error(), "changed since validation") {
		t.Fatalf("expected stale-source rejection, got %v", err)
	}
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != string(edited) {
		t.Fatalf("stale projection overwrote edits: %v", err)
	}
	// Validation failure must release the lease without requiring recovery.
	fresh, err := LoadWorkspace(workspace.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	restore, err := MaterializeEntryPoint(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeRejectsOverlappingProjections(t *testing.T) {
	workspace := projectionWorkspace(t)
	restore, err := MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := MaterializeEntryPoint(workspace)
		result <- err
	}()
	if err := <-result; err == nil || !strings.Contains(err.Error(), "active or interrupted") {
		t.Fatalf("overlapping projection was not excluded: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	restore, err = MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatalf("ordinary restoration did not release lease: %v", err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
}

func TestProjectionRetainsOriginalOnPostMoveSyncFailure(t *testing.T) {
	workspace := projectionWorkspace(t)
	want := beforeProjectionSource(workspace)
	injected := errors.New("injected post-move sync failure")
	ops := defaultProjectionIO()
	moved := false
	ops.move = func(source, destination string) error {
		snapshot := filepath.Join(filepath.Dir(filepath.Dir(destination)), "original.yaml")
		data, err := os.ReadFile(snapshot)
		if err != nil || string(data) != string(want) {
			t.Fatalf("independent exact backup missing before displacement: %v", err)
		}
		if err := os.Rename(source, destination); err != nil {
			return err
		}
		moved = true
		return nil
	}
	ops.syncDirectory = func(path string) error {
		if moved {
			return injected
		}
		return syncAtomicDirectory(path)
	}
	_, err := projectConfiguration(workspace, []byte("# projection\n"), ops)
	if !errors.Is(err, injected) {
		t.Fatalf("expected injected failure, got %v", err)
	}
	assertRecoveryContains(t, workspace, want)
	if _, err := MaterializeEntryPoint(workspace); err == nil || !strings.Contains(err.Error(), "active or interrupted") {
		t.Fatalf("interrupted projection must require explicit recovery: %v", err)
	}
}

func TestProjectionPreservesEditorRacingRestoration(t *testing.T) {
	for _, boundary := range []string{"before-move", "before-publish", "late-open-file"} {
		t.Run(boundary, func(t *testing.T) {
			workspace := projectionWorkspace(t)
			original := beforeProjectionSource(workspace)
			const rendered = "# projection\n"
			const editor = "# editor bytes must survive\n"
			ops := defaultProjectionIO()
			restoring := false
			var displaced string
			ops.move = func(source, destination string) error {
				if restoring && boundary == "before-move" {
					if err := os.WriteFile(source, []byte(editor), 0o600); err != nil {
						return err
					}
				}
				displaced = destination
				return os.Rename(source, destination)
			}
			ops.publish = func(source, destination string) error {
				if restoring && boundary == "before-publish" {
					if err := os.WriteFile(destination, []byte(editor), 0o600); err != nil {
						return err
					}
				}
				if restoring && boundary == "late-open-file" {
					if err := os.WriteFile(displaced, []byte(editor), 0o600); err != nil {
						return err
					}
				}
				return os.Link(source, destination)
			}
			restore, err := projectConfiguration(workspace, []byte(rendered), ops)
			if err != nil {
				t.Fatal(err)
			}
			restoring = true
			if err := restore(); err == nil {
				t.Fatal("expected explicit restoration conflict")
			}
			assertRecoveryContains(t, workspace, original)
			if boundary == "before-publish" {
				current, err := os.ReadFile(workspace.AzureYAML)
				if err != nil || string(current) != editor {
					t.Fatalf("new editor file was overwritten: %v", err)
				}
			} else {
				assertRecoveryContains(t, workspace, []byte(editor))
			}
		})
	}
}

func TestProjectionNeverOverwritesExistingRecovery(t *testing.T) {
	workspace := projectionWorkspace(t)
	recovery := projectionRecoveryPath(workspace)
	active := filepath.Join(recovery, "active")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	const previous = "previous recoverable original"
	if err := os.WriteFile(filepath.Join(active, "original.yaml"), []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeEntryPoint(workspace); err == nil {
		t.Fatal("existing recovery lease was reused")
	}
	assertRecoveryContains(t, workspace, []byte(previous))
}

func TestMaterializeRejectsStaleReferencedConfiguration(t *testing.T) {
	root := writeWorkspace(t, `name: ref-projection
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    project: src
    codeConfiguration:
      $ref: code.yaml
`, map[string]string{
		"src/main.py": "pass\n",
		"code.yaml":   "runtime: python_3_13\nentryPoint: [python, main.py]\n",
	})
	workspace, err := LoadWorkspace(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "code.yaml"), []byte("# changed reference\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeEntryPoint(workspace); err == nil || !strings.Contains(err.Error(), "code.yaml changed since validation") {
		t.Fatalf("stale resolved reference was accepted: %v", err)
	}
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != string(beforeProjectionSource(workspace)) {
		t.Fatalf("stale reference projection changed azure.yaml: %v", err)
	}
}

func TestProjectionPreservesSourceChangedDuringInitialMove(t *testing.T) {
	workspace := projectionWorkspace(t)
	const editor = "# changed after validation but before initial move\n"
	ops := defaultProjectionIO()
	ops.move = func(source, destination string) error {
		if err := os.WriteFile(source, []byte(editor), 0o600); err != nil {
			return err
		}
		return os.Rename(source, destination)
	}
	if _, err := projectConfiguration(workspace, []byte("# projection\n"), ops); err == nil {
		t.Fatal("initial-move conflict was not reported")
	}
	assertRecoveryContains(t, workspace, beforeProjectionSource(workspace))
	assertRecoveryContains(t, workspace, []byte(editor))
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != editor {
		t.Fatalf("editor version was not re-published: %v", err)
	}
}

func TestProjectionRetainsOriginalOnPostPublishSyncFailure(t *testing.T) {
	for _, failRestore := range []bool{false, true} {
		t.Run(fmt.Sprintf("restore=%v", failRestore), func(t *testing.T) {
			workspace := projectionWorkspace(t)
			injected := errors.New("injected post-publish sync failure")
			ops := defaultProjectionIO()
			publications := 0
			ops.publish = func(source, destination string) error {
				if err := os.Link(source, destination); err != nil {
					return err
				}
				publications++
				return nil
			}
			failAt := 1
			if failRestore {
				failAt = 2
			}
			ops.syncDirectory = func(path string) error {
				if publications == failAt {
					return injected
				}
				return syncAtomicDirectory(path)
			}
			restore, err := projectConfiguration(workspace, []byte("# projection\n"), ops)
			if failRestore {
				if err != nil {
					t.Fatal(err)
				}
				err = restore()
			}
			if !errors.Is(err, injected) {
				t.Fatalf("expected post-publication sync failure, got %v", err)
			}
			assertRecoveryContains(t, workspace, beforeProjectionSource(workspace))
			if _, err := os.Stat(filepath.Join(projectionRecoveryPath(workspace), "active")); err != nil {
				t.Fatalf("failed operation did not retain recovery lease: %v", err)
			}
		})
	}
}

func TestProjectionRefusesExistingRecoverySlot(t *testing.T) {
	workspace := projectionWorkspace(t)
	directory := t.TempDir()
	step := filepath.Join(directory, "restore")
	if err := os.Mkdir(step, 0o700); err != nil {
		t.Fatal(err)
	}
	displaced := filepath.Join(step, "displaced.yaml")
	const previous = "# previous conflicting recovery\n"
	if err := os.WriteFile(displaced, []byte(previous), 0o600); err != nil {
		t.Fatal(err)
	}
	original := beforeProjectionSource(workspace)
	err := replaceProjection(workspace.AzureYAML, directory, "restore", original, []byte("# replacement\n"), 0o600, defaultProjectionIO())
	if err == nil {
		t.Fatal("existing recovery slot was reused")
	}
	current, err := os.ReadFile(displaced)
	if err != nil || string(current) != previous {
		t.Fatalf("existing recovery was overwritten: %v", err)
	}
	current, err = os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != string(original) {
		t.Fatalf("azure.yaml changed on recovery conflict: %v", err)
	}
}

func TestProjectionDetectsLateWritesToDisplacedOriginal(t *testing.T) {
	workspace := projectionWorkspace(t)
	const editor = "# editor retained an original handle\n"
	ops := defaultProjectionIO()
	var originalPath string
	ops.move = func(source, destination string) error {
		originalPath = destination
		return os.Rename(source, destination)
	}
	restore, err := projectConfiguration(workspace, []byte("# projection\n"), ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalPath, []byte(editor), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err == nil || !strings.Contains(err.Error(), "concurrent edits") {
		t.Fatalf("late original edit was not reported: %v", err)
	}
	assertRecoveryContains(t, workspace, beforeProjectionSource(workspace))
	assertRecoveryContains(t, workspace, []byte(editor))
}

func TestMaterializeExactRestorationRetainsIndependentBackup(t *testing.T) {
	workspace := projectionWorkspace(t)
	original := []byte("# preserve comments and CRLF\r\n" + strings.ReplaceAll(string(beforeProjectionSource(workspace)), "\n", "\r\n"))
	if err := os.WriteFile(workspace.AzureYAML, original, 0o600); err != nil {
		t.Fatal(err)
	}
	workspace, err := LoadWorkspace(workspace.Root, "")
	if err != nil {
		t.Fatal(err)
	}
	restore, err := MaterializeEntryPoint(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatal(err)
	}
	if err := restore(); err != nil {
		t.Fatalf("restoration must be idempotent: %v", err)
	}
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != string(original) {
		t.Fatalf("restoration changed exact bytes: %v", err)
	}
	if err := os.WriteFile(workspace.AzureYAML, []byte("# later edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertRecoveryContains(t, workspace, original)
}

func TestDoctorRestoresExactSourceOnCancellation(t *testing.T) {
	workspace := projectionWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{run: func(Command) (Execution, error) {
		cancel()
		return Execution{ExitCode: 1, Stdout: undeployedDoctorOutput}, context.Canceled
	}}
	_, err := RunDoctor(ctx, runner, "azd", workspace, "", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	current, err := os.ReadFile(workspace.AzureYAML)
	if err != nil || string(current) != string(beforeProjectionSource(workspace)) {
		t.Fatalf("canceled doctor did not restore exact source: %v", err)
	}
}

func TestDoctorPreservesCancellationAndRestorationConflict(t *testing.T) {
	workspace := projectionWorkspace(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeRunner{run: func(Command) (Execution, error) {
		if err := os.WriteFile(workspace.AzureYAML, []byte("# editor changed during doctor\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cancel()
		return Execution{ExitCode: 1, Stdout: undeployedDoctorOutput}, context.Canceled
	}}
	_, err := RunDoctor(ctx, runner, "azd", workspace, "", nil)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "concurrent edits") {
		t.Fatalf("lost cancellation or restoration conflict: %v", err)
	}
	assertRecoveryContains(t, workspace, beforeProjectionSource(workspace))
}

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
	assertAZDEntryPointFile(t, root, "agent", "main.py")
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

func TestMaterializeDeploymentEntryPointWithoutRAIPolicy(t *testing.T) {
	for _, declaration := range []string{"main.py", "[main.py]", "[python, main.py]"} {
		t.Run(declaration, func(t *testing.T) {
			root := entryPointWorkspace(t, "python_3_13", declaration, "remote_build", map[string]string{"src/main.py": "pass\n"})
			original, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil {
				t.Fatal(err)
			}
			workspace, err := LoadWorkspace(root, "")
			if err != nil {
				t.Fatal(err)
			}
			restore, err := MaterializeDeployment(workspace, "")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := restore(); err != nil {
					t.Error(err)
				}
			}()
			assertAZDEntryPointFile(t, root, "agent", "main.py")
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			restored, err := os.ReadFile(filepath.Join(root, AzureYAMLFile))
			if err != nil || string(restored) != string(original) {
				t.Fatalf("original azure.yaml was not restored: %v", err)
			}
		})
	}
}
