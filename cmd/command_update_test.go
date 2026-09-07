package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/update"

	"gopkg.in/yaml.v3"
)

type fakeSelfUpdater struct {
	plan        *update.Plan
	checkErr    error
	applyErr    error
	checkCalls  int
	applyCalls  int
	applyResult *update.Result
}

func (f *fakeSelfUpdater) Check(context.Context) (*update.Plan, error) {
	f.checkCalls++
	return f.plan, f.checkErr
}

func (f *fakeSelfUpdater) Apply(_ context.Context, plan *update.Plan) (update.Result, error) {
	f.applyCalls++
	result := plan.Result
	if f.applyResult != nil {
		return *f.applyResult, f.applyErr
	}
	if f.applyErr != nil {
		return result, f.applyErr
	}
	result.Status = "updated"
	result.Changed = true
	return result, f.applyErr
}

func stubSelfUpdater(t *testing.T) (*fakeSelfUpdater, *update.Options) {
	t.Helper()
	previous := newSelfUpdaterFn
	t.Cleanup(func() { newSelfUpdaterFn = previous })
	fake := &fakeSelfUpdater{plan: &update.Plan{Result: update.Result{
		Status:          "available",
		CurrentVersion:  config.Version,
		TargetVersion:   "99.0.0",
		Executable:      `C:\tools\fam.exe`,
		Asset:           "fam_99.0.0_windows_arm64.zip",
		UpdateAvailable: true,
	}}}
	var options update.Options
	newSelfUpdaterFn = func(value update.Options) (selfUpdater, error) {
		options = value
		return fake, nil
	}
	return fake, &options
}

func TestUpdateCheckAndConfirmation(t *testing.T) {
	for _, test := range []struct {
		name  string
		args  []string
		input string
		apply int
		code  int
	}{
		{"check", []string{"--check"}, "", 0, 0},
		{"check with yes", []string{"--check", "--yes"}, "", 0, 0},
		{"qualification pins consumed check", []string{"--check=true", "--tenant-id", "--check", "--yes"}, "", 0, 0},
		{"EOF declines", nil, "", 0, 3},
		{"no declines", nil, "no\n", 0, 3},
		{"confirmed", nil, "yes\n", 1, 0},
		{"yes", []string{"--yes"}, "", 1, 0},
		{"explicit check false", []string{"--check=false", "--yes"}, "", 1, 0},
		{"JSON requires yes", []string{"--output", "json"}, "yes\n", 0, 3},
		{"YAML requires yes", []string{"--output", "yaml"}, "yes\n", 0, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake, _ := stubSelfUpdater(t)
			run := runCLI(t, test.input, append([]string{"update"}, test.args...)...)
			if run.code != test.code || fake.checkCalls != 1 || fake.applyCalls != test.apply {
				t.Fatalf("code=%d check=%d apply=%d stdout=%q stderr=%q", run.code, fake.checkCalls, fake.applyCalls, run.stdout, run.stderr)
			}
			if test.name == "confirmed" && (!strings.Contains(run.stderr, fake.plan.Result.Executable) ||
				!strings.Contains(run.stderr, fake.plan.Result.TargetVersion)) {
				t.Fatalf("confirmation omitted destination/version: %s", run.stderr)
			}
		})
	}
}

func TestUpdateOutputAndNoOp(t *testing.T) {
	for _, format := range []string{"text", "json", "yaml"} {
		for _, status := range []string{"available", "up-to-date", "newer-installed"} {
			t.Run(format+"/"+status, func(t *testing.T) {
				fake, _ := stubSelfUpdater(t)
				fake.plan.Result.Status = status
				fake.plan.Result.UpdateAvailable = status == "available"
				args := []string{"update", "--output", format}
				if status == "available" {
					args = append(args, "--check")
				}
				run := runCLI(t, "", args...)
				if run.code != 0 || fake.applyCalls != 0 || run.stderr != "" {
					t.Fatalf("unexpected result: %#v; apply=%d", run, fake.applyCalls)
				}
				var result update.Result
				switch format {
				case "json":
					if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
						t.Fatal(err)
					}
				case "yaml":
					if err := yaml.Unmarshal([]byte(run.stdout), &result); err != nil {
						t.Fatal(err)
					}
				default:
					if !strings.Contains(run.stdout, fake.plan.Result.CurrentVersion) {
						t.Fatalf("missing current version: %s", run.stdout)
					}
					return
				}
				if result.Status != status || result.Changed || result.TargetVersion != "99.0.0" {
					t.Fatalf("unexpected structured result: %#v", result)
				}
			})
		}
	}
}

func TestUpdateOptionsAndTokenPrecedence(t *testing.T) {
	fake, options := stubSelfUpdater(t)
	for _, name := range []string{"FAM_INSTALL_TOKEN", "GITHUB_TOKEN", "GH_TOKEN"} {
		t.Setenv(name, "")
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "FAM_INSTALL_TOKEN"} {
		t.Setenv(name, "synthetic-"+name)
		run := runCLI(t, "", "update", "--version", "v99.0.0", "--check",
			"--request-timeout", "45s", "--retry-count", "2", "--retry-delay", "3s")
		if run.code != 0 || options.Token != "synthetic-"+name || options.Version != "v99.0.0" ||
			options.CurrentVersion != config.Version || options.Timeout != 45*time.Second ||
			options.Retries != 2 || options.RetryDelay != 3*time.Second || fake.applyCalls != 0 {
			t.Fatalf("unexpected update options or result: code=%d stderr=%s", run.code, run.stderr)
		}
		if strings.Contains(run.stdout+run.stderr, "synthetic-") {
			t.Fatal("output exposed credential")
		}
	}
}

func TestUpdateHelpAndValidationDoNotInitializeUpdater(t *testing.T) {
	previous := newSelfUpdaterFn
	t.Cleanup(func() { newSelfUpdaterFn = previous })
	newSelfUpdaterFn = func(update.Options) (selfUpdater, error) {
		t.Fatal("help/completion/invalid flags initialized updater")
		return nil, nil
	}
	for _, args := range [][]string{
		{"update", "--help"}, {"help", "update"}, {"__complete", "update", "--version", ""},
		{"--version"}, {"-version"},
	} {
		run := runCLI(t, "", args...)
		if run.code != 0 {
			t.Fatalf("%v failed: %s", args, run.stderr)
		}
	}
	for _, args := range [][]string{
		{"update", "extra"}, {"update", "--version="}, {"update", "--version"},
		{"update", "--request-timeout", "0"}, {"update", "--retry-count", "-1"},
	} {
		if run := runCLI(t, "", args...); run.code != 3 {
			t.Fatalf("%v: expected config error, got %#v", args, run)
		}
	}
}

func TestUpdateErrorsAndQuiet(t *testing.T) {
	fake, _ := stubSelfUpdater(t)
	fake.checkErr = errs.NotFound("release unavailable")
	run := runCLI(t, "", "update", "--yes", "--output", "json")
	if run.code != 6 || fake.applyCalls != 0 || decodeErrorEnvelope(t, run).Kind != "not_found" {
		t.Fatalf("check error not preserved: %#v", run)
	}

	fake.checkErr = nil
	fake.applyErr = errs.Security("checksum mismatch")
	run = runCLI(t, "", "update", "--yes", "--output", "json")
	if run.code == 0 || run.stdout != "" || decodeErrorEnvelope(t, run).Kind != "security" {
		t.Fatalf("apply failure reported success: %#v", run)
	}
	fake.applyErr = nil
	run = runCLI(t, "", "update", "--yes", "--quiet")
	if run.code != 0 || run.stdout != "" {
		t.Fatalf("quiet result: %#v", run)
	}
	run = runCLI(t, "", "update", "--yes", "--quiet", "--output", "json")
	var result update.Result
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if run.code != 0 || !result.Changed || result.Status != "updated" {
		t.Fatalf("missing structured success: %#v", run)
	}
}

func TestUpdatePartialReplacementErrorIncludesRecovery(t *testing.T) {
	fake, _ := stubSelfUpdater(t)
	fake.applyErr = errs.Config("cannot persist replacement")
	result := fake.plan.Result
	result.Status, result.Changed = "updated", true
	result.BackupPath = `C:\tools\.fam-update-fixture\original`
	fake.applyResult = &result
	run := runCLI(t, "", "update", "--yes", "--output", "json")
	var envelope struct {
		Error struct {
			NextSteps []string `json:"nextSteps"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(run.stderr), &envelope); err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(envelope.Error.NextSteps, "\n")
	if run.code == 0 || run.stdout != "" || !strings.Contains(steps, "was installed") ||
		!strings.Contains(steps, result.BackupPath) {
		t.Fatalf("partial replacement lost its recovery outcome: %#v", run)
	}
}
