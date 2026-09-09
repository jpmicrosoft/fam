package qa

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type weeklyReviewStep struct {
	Name            string            `yaml:"name"`
	ID              string            `yaml:"id"`
	Uses            string            `yaml:"uses"`
	Run             string            `yaml:"run"`
	If              string            `yaml:"if"`
	Shell           string            `yaml:"shell"`
	Env             map[string]string `yaml:"env"`
	With            map[string]any    `yaml:"with"`
	TimeoutMinutes  int               `yaml:"timeout-minutes"`
	ContinueOnError bool              `yaml:"continue-on-error"`
}

type weeklyReviewDocument struct {
	Permissions map[string]string `yaml:"permissions"`
	Engine      struct {
		ID         string            `yaml:"id"`
		CopilotSDK bool              `yaml:"copilot-sdk"`
		Env        map[string]string `yaml:"env"`
		Harness    struct {
			MaxRetries *int `yaml:"max-retries"`
		} `yaml:"harness"`
	} `yaml:"engine"`
	MaxToolDenials int `yaml:"max-tool-denials"`
	MaxAICredits   int `yaml:"max-ai-credits"`
	Network        struct {
		Allowed []string `yaml:"allowed"`
	} `yaml:"network"`
	Tools struct {
		Bash []string `yaml:"bash"`
	} `yaml:"tools"`
	SafeOutputs map[string]any     `yaml:"safe-outputs"`
	Steps       []weeklyReviewStep `yaml:"steps"`
	Jobs        map[string]struct {
		If          string             `yaml:"if"`
		Permissions map[string]string  `yaml:"permissions"`
		Steps       []weeklyReviewStep `yaml:"steps"`
	} `yaml:"jobs"`
}

func weeklyReviewDocuments(t *testing.T) (weeklyReviewDocument, weeklyReviewDocument) {
	t.Helper()
	source := repositoryFile(t, ".github", "workflows", "weekly-foundry-capability-review.md")
	source = strings.ReplaceAll(source, "\r\n", "\n")
	frontmatter, _, ok := strings.Cut(source, "\n---\n")
	if !strings.HasPrefix(source, "---\n") || !ok {
		t.Fatal("weekly review has no YAML frontmatter")
	}
	var authoring, compiled weeklyReviewDocument
	if err := yaml.Unmarshal([]byte(frontmatter), &authoring); err != nil {
		t.Fatalf("parse weekly review frontmatter: %v", err)
	}
	lock := repositoryFile(t, ".github", "workflows", "weekly-foundry-capability-review.lock.yml")
	if err := yaml.Unmarshal([]byte(lock), &compiled); err != nil {
		t.Fatalf("parse compiled weekly review: %v", err)
	}
	return authoring, compiled
}

func weeklyReviewStepIndex(t *testing.T, steps []weeklyReviewStep, nameOrID string) int {
	t.Helper()
	for index, step := range steps {
		if step.Name == nameOrID || step.ID == nameOrID {
			return index
		}
	}
	t.Fatalf("weekly review step %q not found", nameOrID)
	return -1
}

func TestWeeklyFoundryCompilerMatchesRuntime(t *testing.T) {
	workflow := repositoryFile(t, ".github", "workflows", "weekly-foundry-capability-review.lock.yml")
	firstLine, _, _ := strings.Cut(workflow, "\n")
	metadataJSON, ok := strings.CutPrefix(firstLine, "# gh-aw-metadata: ")
	if !ok {
		t.Fatal("compiled workflow is missing its compiler metadata")
	}
	var metadata struct {
		CompilerVersion string `json:"compiler_version"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("parse compiler metadata: %v", err)
	}
	if metadata.CompilerVersion == "" {
		t.Fatal("compiled workflow has no compiler version")
	}

	var lock struct {
		Entries map[string]struct {
			Repo    string `json:"repo"`
			Version string `json:"version"`
			SHA     string `json:"sha"`
		} `json:"entries"`
	}
	lockJSON := repositoryFile(t, ".github", "aw", "actions-lock.json")
	if err := json.Unmarshal([]byte(lockJSON), &lock); err != nil {
		t.Fatalf("parse action lock: %v", err)
	}
	const action = "github/gh-aw-actions/setup"
	entry, ok := lock.Entries[action+"@"+metadata.CompilerVersion]
	if !ok {
		t.Fatalf("action lock has no runtime for compiler %s; regenerate with the matching gh-aw compiler", metadata.CompilerVersion)
	}
	if entry.Repo != action || entry.Version != metadata.CompilerVersion ||
		!regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(entry.SHA) {
		t.Fatalf("invalid locked runtime for compiler %s: %#v", metadata.CompilerVersion, entry)
	}

	usesPattern := regexp.MustCompile(`(?m)^\s*uses:\s*` + regexp.QuoteMeta(action) + `@(\S+)`)
	uses := usesPattern.FindAllStringSubmatch(workflow, -1)
	if len(uses) == 0 {
		t.Fatal("compiled workflow has no setup runtime references")
	}
	for _, use := range uses {
		if use[1] != entry.SHA {
			t.Errorf("setup runtime %s does not match compiler %s pin %s; regenerate the workflow instead of updating only action references",
				use[1], metadata.CompilerVersion, entry.SHA)
		}
	}
}

func TestWeeklyFoundryGoReadinessBeforeInference(t *testing.T) {
	source, compiled := weeklyReviewDocuments(t)
	agentSteps := compiled.Jobs["agent"].Steps
	setupIndex := weeklyReviewStepIndex(t, agentSteps, "Set up Go for the weekly review")
	prepareIndex := weeklyReviewStepIndex(t, agentSteps, "Prepare offline Go validation")
	executeIndex := weeklyReviewStepIndex(t, agentSteps, "agentic_execution")
	if setupIndex >= prepareIndex || prepareIndex >= executeIndex {
		t.Fatal("Go setup and offline preparation must precede inference in the same job")
	}
	if agentSteps[executeIndex].If != "" {
		t.Fatal("inference must retain the default success gate after trusted setup")
	}
	for _, steps := range [][]weeklyReviewStep{source.Steps, agentSteps} {
		setup := steps[weeklyReviewStepIndex(t, steps, "Set up Go for the weekly review")]
		if setup.Uses != "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e" ||
			setup.With["go-version-file"] != "go.mod" || setup.With["cache"] != false {
			t.Fatalf("Go setup must use the CI pin and go.mod without an implicit cache: %#v", setup)
		}
		preparation := steps[weeklyReviewStepIndex(t, steps, "Prepare offline Go validation")]
		if preparation.ContinueOnError || preparation.If != "" ||
			preparation.TimeoutMinutes != 10 || preparation.Shell != "bash" {
			t.Fatalf("offline preparation must be bounded and fail closed: %#v", preparation)
		}
	}
	sourcePreparation := source.Steps[weeklyReviewStepIndex(t, source.Steps, "Prepare offline Go validation")]
	if strings.TrimSpace(sourcePreparation.Run) != strings.TrimSpace(agentSteps[prepareIndex].Run) {
		t.Fatal("compiled Go preparation differs from the authoritative source")
	}
	wantEnv := map[string]string{
		"GOTOOLCHAIN": "local",
		"GOFLAGS":     "-mod=readonly",
		"GOPROXY":     "off",
		"GOSUMDB":     "off",
	}
	for key, want := range wantEnv {
		if source.Engine.Env[key] != want || agentSteps[executeIndex].Env[key] != want {
			t.Errorf("%s must be %q in both source and compiled inference environment", key, want)
		}
	}
	if !strings.Contains(agentSteps[executeIndex].Run, "--env-all") ||
		!strings.Contains(agentSteps[executeIndex].Run, `export PATH="$GOROOT/bin:$PATH"`) {
		t.Fatal("sandbox must inherit prepared caches and prioritize the selected Go toolchain")
	}
}

func TestWeeklyFoundryBoundedInferenceAndPolicy(t *testing.T) {
	source, compiled := weeklyReviewDocuments(t)
	if source.Engine.ID != "copilot" || !source.Engine.CopilotSDK ||
		source.MaxToolDenials != 5 || source.MaxAICredits != 1000 ||
		source.Engine.Harness.MaxRetries == nil || *source.Engine.Harness.MaxRetries != 0 {
		t.Fatal("review must use the Copilot SDK denial limit, no retries, and a 1000-credit cap")
	}
	agent := compiled.Jobs["agent"]
	execution := agent.Steps[weeklyReviewStepIndex(t, agent.Steps, "agentic_execution")]
	for key, want := range map[string]string{
		"GH_AW_COPILOT_SDK_DRIVER":  "1",
		"GH_AW_MAX_TOOL_DENIALS":    "5",
		"GH_AW_HARNESS_MAX_RETRIES": "0",
	} {
		if execution.Env[key] != want {
			t.Errorf("compiled %s = %q, want %q", key, execution.Env[key], want)
		}
	}
	if !strings.Contains(execution.Run, "copilot_sdk_driver.cjs") {
		t.Fatal("denial limit must be wired to the SDK driver, not the CLI-only path")
	}
	configPattern := regexp.MustCompile(`(?m)^\s*printf '%s\\n' '(\{.*\})' > `)
	match := configPattern.FindStringSubmatch(execution.Run)
	if len(match) != 2 {
		t.Fatal("compiled inference must contain a literal firewall budget")
	}
	var firewall struct {
		APIProxy struct {
			MaxAICredits int `json:"maxAiCredits"`
		} `json:"apiProxy"`
	}
	if err := json.Unmarshal([]byte(match[1]), &firewall); err != nil {
		t.Fatalf("parse compiled firewall configuration: %v", err)
	}
	if firewall.APIProxy.MaxAICredits != 1000 {
		t.Fatalf("firewall credit ceiling = %d, want 1000", firewall.APIProxy.MaxAICredits)
	}
	wantPermissions := map[string]string{"contents": "read"}
	if !reflect.DeepEqual(source.Permissions, wantPermissions) ||
		!reflect.DeepEqual(agent.Permissions, wantPermissions) {
		t.Fatal("agent repository permissions must remain read-only")
	}
	if !reflect.DeepEqual(source.Network.Allowed, []string{"defaults", "learn.microsoft.com"}) {
		t.Fatal("review must not broaden the sandbox network allowlist")
	}
	wantBash := []string{"git status", "git diff", "git diff:*", "git grep:*",
		"gofmt:*", "go test:*", "go vet:*", "go build:*"}
	if !reflect.DeepEqual(source.Tools.Bash, wantBash) {
		t.Fatal("review must not broaden the command allowlist")
	}
	pr, ok := source.SafeOutputs["create-pull-request"].(map[string]any)
	if !ok || pr["draft"] != true || pr["max"] != 1 ||
		pr["target-repo"] != "jpmicrosoft/fam" || pr["base-branch"] != "main" {
		t.Fatal("review must retain its single-draft-PR publication boundary")
	}
}

func TestWeeklyFoundryDetectionRequiresOutputs(t *testing.T) {
	source, compiled := weeklyReviewDocuments(t)
	const hasOutputs = "needs.agent.outputs.output_types != '' || needs.agent.outputs.has_patch == 'true'"
	if source.Jobs["detection"].If != hasOutputs {
		t.Fatal("detection must be gated by safe outputs OR a patch, not by agent success")
	}
	want := "(always() && needs.agent.result != 'skipped') && (" + hasOutputs + ")"
	if strings.TrimSpace(compiled.Jobs["detection"].If) != want {
		t.Fatal("compiled detection must preserve the framework guard AND require something to scan")
	}
	detectionSteps := compiled.Jobs["detection"].Steps
	execution := detectionSteps[weeklyReviewStepIndex(t, detectionSteps, "detection_agentic_execution")]
	if !strings.Contains(execution.If, "steps.detection_guard.outputs.run_detection == 'true'") ||
		!strings.Contains(execution.Run, "threat-detect") {
		t.Fatal("real outputs and patches must still run threat detection")
	}
	if compiled.Jobs["safe_outputs"].If != "(!cancelled()) && needs.agent.result != 'skipped' && needs.detection.result == 'success'" {
		t.Fatal("publication must still require successful detection")
	}
	if _, ok := compiled.Jobs["conclusion"]; !ok {
		t.Fatal("framework failure reporting must remain enabled")
	}
	if source.SafeOutputs["report-failed-jobs"] == false {
		t.Fatal("agent failure reporting must not be suppressed")
	}
}

func TestWeeklyFoundryGoPreparationBehavior(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable; preparation behavior is exercised by CI")
	}
	source, _ := weeklyReviewDocuments(t)
	preparation := source.Steps[weeklyReviewStepIndex(t, source.Steps, "Prepare offline Go validation")].Run
	commands := []string{"env GOROOT", "version", "mod download", "mod verify", "test -run ^$ ./..."}
	for failIndex := -1; failIndex < len(commands); failIndex++ {
		name, failAt := "success", ""
		wantCommands := commands
		if failIndex >= 0 {
			name, failAt = commands[failIndex], commands[failIndex]
			wantCommands = commands[:failIndex+1]
		}
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			command := exec.Command(bash, "--noprofile", "--norc", "-s")
			command.Dir = directory
			command.Env = append(os.Environ(), "FAIL_AT="+failAt)
			command.Stdin = strings.NewReader(`
	export HOME="$PWD/home"
	export GITHUB_ENV="$PWD/github-env"
	unset GOPROXY GOSUMDB
	go() {
	  printf '%s\n' "$*" >> go-calls
	  if [[ "$*" == "$FAIL_AT" ]]; then
	    echo "fixture prerequisite failure: $*" >&2
	    return 47
	  fi
	  case "$*" in
	    "env GOROOT") printf '%s\n' "/fixture/go" ;;
	    "test -run ^$ ./...")
	      [[ "$GOTOOLCHAIN" == local && "$GOFLAGS" == -mod=readonly &&
	         "$GOPROXY" == off && "$GOSUMDB" == off &&
	         "$GOROOT" == /fixture/go && "$GOMODCACHE" == "$HOME/go/pkg/mod" &&
	         "$GOCACHE" == "$HOME/.cache/go-build" ]] || return 48 ;;
	  esac
	}
	` + preparation)
			output, runErr := command.CombinedOutput()
			if failAt == "" && runErr != nil {
				t.Fatalf("preparation failed: %v\n%s", runErr, output)
			}
			if failAt != "" {
				exit, ok := runErr.(*exec.ExitError)
				if !ok || exit.ExitCode() != 47 || !strings.Contains(string(output), "fixture prerequisite failure: "+failAt) {
					t.Fatalf("preparation must expose and stop on %q: %v\n%s", failAt, runErr, output)
				}
			}
			data, err := os.ReadFile(filepath.Join(directory, "go-calls"))
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSpace(string(data)), "\n")
			if !reflect.DeepEqual(got, wantCommands) {
				t.Fatalf("Go calls = %q, want %q; preparation continued after a failure", got, wantCommands)
			}
			if failAt == "" {
				data, err = os.ReadFile(filepath.Join(directory, "github-env"))
				if err != nil {
					t.Fatal(err)
				}
				for _, variable := range []string{"GOROOT=/fixture/go\n", "GOTOOLCHAIN=local\n",
					"GOFLAGS=-mod=readonly\n", "GOMODCACHE=", "GOCACHE="} {
					if !strings.Contains(string(data), variable) {
						t.Errorf("prepared environment missing %q: %s", variable, data)
					}
				}
			}
		})
	}
}
