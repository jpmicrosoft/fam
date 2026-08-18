package hostedautopilot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeCommandRunner struct {
	commands []Command
	run      func(Command) (CommandExecution, error)
}

func (f *fakeCommandRunner) Run(_ context.Context, command Command) (CommandExecution, error) {
	command.Args = append([]string(nil), command.Args...)
	command.Environment = copyEnvironment(command.Environment)
	f.commands = append(f.commands, command)
	if f.run == nil {
		return CommandExecution{}, nil
	}
	return f.run(command)
}

func allToolsLookPath(name string) (string, error) {
	return "resolved-" + name, nil
}

func validPreflight() PreflightOptions {
	return PreflightOptions{
		Cloud:               AzureCloud,
		AcceptPreview:       true,
		ApproveSampleCommit: ReviewedSampleCommit,
		Region:              "caller-approved-region",
		AllowedRegions:      []string{"caller-approved-region"},
		LookPath:            allToolsLookPath,
	}
}

func createSamplePath(t *testing.T, isolatedDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(isolatedDir, filepath.FromSlash(SamplePath)), 0o755); err != nil {
		t.Fatalf("create sample path: %v", err)
	}
}

func TestPreflightRejectsWrongApprovedCommit(t *testing.T) {
	options := validPreflight()
	options.ApproveSampleCommit = strings.Repeat("0", 40)

	_, err := Preflight(options)

	if !errors.Is(err, ErrCommitNotApproved) {
		t.Fatalf("expected ErrCommitNotApproved, got %v", err)
	}
}

func TestPreflightRejectsMissingTool(t *testing.T) {
	options := validPreflight()
	options.LookPath = func(name string) (string, error) {
		if name == "docker" {
			return "", errors.New("not found")
		}
		return "resolved-" + name, nil
	}

	_, err := Preflight(options)

	if !errors.Is(err, ErrMissingExecutable) || !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected missing docker error, got %v", err)
	}
}

func TestPreflightRejectsUnsupportedCloudBeforeToolLookup(t *testing.T) {
	options := validPreflight()
	options.Cloud = "UnsupportedCloud"
	options.LookPath = func(string) (string, error) {
		t.Fatal("LookPath must not be called for an unsupported cloud")
		return "", nil
	}

	_, err := Preflight(options)

	if !errors.Is(err, ErrUnsupportedCloud) {
		t.Fatalf("expected ErrUnsupportedCloud, got %v", err)
	}
}

func TestRunRejectsWrongCheckedOutCommit(t *testing.T) {
	isolatedDir := t.TempDir()
	runner := &fakeCommandRunner{
		run: func(command Command) (CommandExecution, error) {
			if reflect.DeepEqual(command.Args, []string{"rev-parse", "HEAD"}) {
				return CommandExecution{Stdout: strings.Repeat("1", 40)}, nil
			}
			return CommandExecution{}, nil
		},
	}

	result, err := Run(context.Background(), RunRequest{
		Preflight:   validPreflight(),
		IsolatedDir: isolatedDir,
		Runner:      runner,
	})

	if !errors.Is(err, ErrCheckedOutCommit) {
		t.Fatalf("expected ErrCheckedOutCommit, got %v", err)
	}
	if len(result.Commands) != 5 {
		t.Fatalf("expected five git commands, got %d", len(result.Commands))
	}
	for _, command := range runner.commands {
		if command.Executable == "resolved-azd" {
			t.Fatal("azd must not run before commit verification succeeds")
		}
	}
}

func TestRunRejectsMissingSamplePath(t *testing.T) {
	isolatedDir := t.TempDir()
	runner := &fakeCommandRunner{
		run: func(command Command) (CommandExecution, error) {
			if reflect.DeepEqual(command.Args, []string{"rev-parse", "HEAD"}) {
				return CommandExecution{Stdout: ReviewedSampleCommit + "\n"}, nil
			}
			return CommandExecution{}, nil
		},
	}

	result, err := Run(context.Background(), RunRequest{
		Preflight:   validPreflight(),
		IsolatedDir: isolatedDir,
		Runner:      runner,
	})

	if !errors.Is(err, ErrSamplePathMissing) {
		t.Fatalf("expected ErrSamplePathMissing, got %v", err)
	}
	if len(result.Commands) != 5 {
		t.Fatalf("expected five git commands, got %d", len(result.Commands))
	}
}

func TestRunUsesSafeArgvAndVerifiesPinnedCommit(t *testing.T) {
	isolatedDir := t.TempDir()
	runner := &fakeCommandRunner{}
	runner.run = func(command Command) (CommandExecution, error) {
		if reflect.DeepEqual(command.Args, []string{"checkout", "--quiet", "--detach", ReviewedSampleCommit}) {
			createSamplePath(t, isolatedDir)
		}
		if reflect.DeepEqual(command.Args, []string{"rev-parse", "HEAD"}) {
			return CommandExecution{Stdout: ReviewedSampleCommit}, nil
		}
		return CommandExecution{}, nil
	}

	result, err := Run(context.Background(), RunRequest{
		Preflight:   validPreflight(),
		IsolatedDir: isolatedDir,
		AllowedEnv:  map[string]string{"AZURE_ENV_NAME": "reviewed-env"},
		Runner:      runner,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	expectedArgs := [][]string{
		{"init", "--quiet"},
		{"remote", "add", "origin", OfficialRepositoryURL},
		{"fetch", "--quiet", "--depth=1", "--no-tags", "origin", ReviewedSampleCommit},
		{"checkout", "--quiet", "--detach", ReviewedSampleCommit},
		{"rev-parse", "HEAD"},
		{"provision"},
		{"env", "get-values"},
	}
	if len(runner.commands) != len(expectedArgs) {
		t.Fatalf("expected %d commands, got %d", len(expectedArgs), len(runner.commands))
	}
	for i, expected := range expectedArgs {
		if !reflect.DeepEqual(runner.commands[i].Args, expected) {
			t.Fatalf("command %d args: expected %#v, got %#v", i, expected, runner.commands[i].Args)
		}
		if runner.commands[i].Executable != "resolved-git" && runner.commands[i].Executable != "resolved-azd" {
			t.Fatalf("command %d used unexpected executable %q", i, runner.commands[i].Executable)
		}
		for _, arg := range runner.commands[i].Args {
			if strings.Contains(arg, "&&") || strings.ContainsAny(arg, "\r\n") {
				t.Fatalf("command %d contains shell-like argument %q", i, arg)
			}
		}
	}
	if runner.commands[4].OutputPolicy != CaptureStdout {
		t.Fatal("git rev-parse must be the only captured command")
	}
	for i, command := range runner.commands {
		if i != 4 && command.OutputPolicy != DiscardOutput {
			t.Fatalf("command %d must discard output", i)
		}
		if i < 5 && len(command.Environment) != 0 {
			t.Fatalf("git command %d unexpectedly received environment values", i)
		}
		if i >= 5 && command.Environment["AZURE_ENV_NAME"] != "reviewed-env" {
			t.Fatalf("azd command %d did not receive the explicit allow-listed environment", i)
		}
	}

	if result.RepositoryURL != OfficialRepositoryURL ||
		result.CommitSHA != ReviewedSampleCommit ||
		result.SamplePath != SamplePath {
		t.Fatalf("unexpected result metadata: %+v", result)
	}
	if !result.AdminApprovalRemainsExternal || result.AdminApproval != AdminApprovalStatement {
		t.Fatalf("admin approval boundary missing from result: %+v", result)
	}
}

func TestRunRedactsEnvironmentValuesFromResultAndErrors(t *testing.T) {
	isolatedDir := t.TempDir()
	const secret = "do-not-leak-this-value"
	runner := &fakeCommandRunner{}
	runner.run = func(command Command) (CommandExecution, error) {
		if reflect.DeepEqual(command.Args, []string{"checkout", "--quiet", "--detach", ReviewedSampleCommit}) {
			createSamplePath(t, isolatedDir)
		}
		if reflect.DeepEqual(command.Args, []string{"rev-parse", "HEAD"}) {
			return CommandExecution{Stdout: ReviewedSampleCommit}, nil
		}
		if reflect.DeepEqual(command.Args, []string{"provision"}) {
			return CommandExecution{ExitCode: 1}, fmt.Errorf("sample failure included %s", secret)
		}
		return CommandExecution{}, nil
	}

	result, err := Run(context.Background(), RunRequest{
		Preflight:   validPreflight(),
		IsolatedDir: isolatedDir,
		AllowedEnv:  map[string]string{"SAMPLE_SECRET": secret},
		Runner:      runner,
	})

	if err == nil {
		t.Fatal("expected command failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked environment value: %v", err)
	}
	resultText := fmt.Sprintf("%+v", result)
	if strings.Contains(resultText, secret) {
		t.Fatalf("result leaked environment value: %s", resultText)
	}
	last := result.Commands[len(result.Commands)-1]
	if last.Environment["SAMPLE_SECRET"] != RedactedValue {
		t.Fatalf("expected redacted environment, got %#v", last.Environment)
	}
}
