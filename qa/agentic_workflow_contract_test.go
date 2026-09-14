package qa

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type weeklyReviewStep struct {
	Name             string            `yaml:"name"`
	ID               string            `yaml:"id"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	If               string            `yaml:"if"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	With             map[string]any    `yaml:"with"`
	TimeoutMinutes   int               `yaml:"timeout-minutes"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
}

type weeklyReviewDocument struct {
	Permissions map[string]string `yaml:"permissions"`
	Engine      struct {
		ID          string            `yaml:"id"`
		CopilotSDK  bool              `yaml:"copilot-sdk"`
		ToolProfile string            `yaml:"tool-profile"`
		Env         map[string]string `yaml:"env"`
		Harness     struct {
			MaxRetries *int `yaml:"max-retries"`
		} `yaml:"harness"`
	} `yaml:"engine"`
	MaxToolDenials int `yaml:"max-tool-denials"`
	MaxAICredits   int `yaml:"max-ai-credits"`
	Network        struct {
		Allowed []string `yaml:"allowed"`
	} `yaml:"network"`
	Tools struct {
		Bash     *bool `yaml:"bash"`
		CLIProxy *bool `yaml:"cli-proxy"`
	} `yaml:"tools"`
	Sandbox struct {
		Agent struct {
			ID     string   `yaml:"id"`
			Mounts []string `yaml:"mounts"`
		} `yaml:"agent"`
		MCP struct {
			Container string `yaml:"container"`
			Version   string `yaml:"version"`
		} `yaml:"mcp"`
	} `yaml:"sandbox"`
	SafeOutputs map[string]any     `yaml:"safe-outputs"`
	Steps       []weeklyReviewStep `yaml:"steps"`
	PostSteps   []weeklyReviewStep `yaml:"post-steps"`
	Jobs        map[string]struct {
		If             string             `yaml:"if"`
		TimeoutMinutes int                `yaml:"timeout-minutes"`
		Permissions    map[string]string  `yaml:"permissions"`
		Steps          []weeklyReviewStep `yaml:"steps"`
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
		Strict          bool   `json:"strict"`
	}
	if err := json.Unmarshal([]byte(metadataJSON), &metadata); err != nil {
		t.Fatalf("parse compiler metadata: %v", err)
	}
	const compilerRevision = "a5e64668dbc0a4ea93cc0733ee3adf3aec1ebe47"
	if metadata.CompilerVersion != compilerRevision {
		t.Fatalf("compiler revision = %q, want fixed fork revision %s", metadata.CompilerVersion, compilerRevision)
	}
	if !metadata.Strict {
		t.Fatal("weekly review must retain strict compilation when selecting the fork gateway")
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
	const action = "jpmicrosoft/gh-aw/actions/setup"
	entry, ok := lock.Entries[action+"@"+metadata.CompilerVersion]
	if !ok {
		t.Fatalf("action lock has no runtime for compiler %s; regenerate with the matching gh-aw compiler", metadata.CompilerVersion)
	}
	if entry.Repo != action || entry.Version != metadata.CompilerVersion ||
		entry.SHA != compilerRevision {
		t.Fatalf("invalid locked runtime for compiler %s: %#v", metadata.CompilerVersion, entry)
	}
	for _, upstream := range []string{"github/gh-aw-actions/setup@", "github/gh-aw/actions/setup@"} {
		if strings.Contains(workflow, upstream) {
			t.Errorf("compiled workflow still references upstream runtime %s instead of the fixed fork", upstream)
		}
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

func TestWeeklyFoundryRuntimeNotUpdatedIndependently(t *testing.T) {
	var config struct {
		Updates []struct {
			Ecosystem string `yaml:"package-ecosystem"`
			Directory string `yaml:"directory"`
			Ignore    []struct {
				Name        string   `yaml:"dependency-name"`
				Versions    []string `yaml:"versions"`
				UpdateTypes []string `yaml:"update-types"`
			} `yaml:"ignore"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal([]byte(repositoryFile(t, ".github", "dependabot.yml")), &config); err != nil {
		t.Fatalf("parse Dependabot configuration: %v", err)
	}
	foundActions := false
	for _, update := range config.Updates {
		if update.Ecosystem != "github-actions" {
			continue
		}
		foundActions = true
		var ignored []string
		for _, rule := range update.Ignore {
			if len(rule.Versions) == 0 && len(rule.UpdateTypes) == 0 {
				ignored = append(ignored, rule.Name)
			}
		}
		for _, pattern := range []string{"jpmicrosoft/gh-aw", "jpmicrosoft/gh-aw/actions/*"} {
			if !slices.Contains(ignored, pattern) {
				t.Errorf("GitHub Actions updates for %q must ignore all versions of %s independently of the compiler",
					update.Directory, pattern)
			}
		}
	}
	if !foundActions {
		t.Fatal("Dependabot must retain GitHub Actions updates for unrelated actions")
	}
}

func TestWeeklyFoundryGatewayUsesScopedImmutableImage(t *testing.T) {
	authoring, compiled := weeklyReviewDocuments(t)
	const container = "ghcr.io/jpmicrosoft/gh-aw-mcpg"
	const sourceImage = "ghcr.io/github/gh-aw-mcpg:v0.4.20"
	if authoring.Sandbox.MCP.Container != "" || authoring.Sandbox.MCP.Version != "" {
		t.Fatal("strict mode requires the repository image mapping, not sandbox.mcp runtime overrides")
	}
	var config struct {
		ContainerPins map[string]struct {
			Image  string `json:"image"`
			Digest string `json:"digest"`
		} `json:"container_pins"`
	}
	if err := json.Unmarshal([]byte(repositoryFile(t, ".github", "workflows", "aw.json")), &config); err != nil {
		t.Fatalf("parse gateway pin configuration: %v", err)
	}
	pin, ok := config.ContainerPins[sourceImage]
	if len(config.ContainerPins) != 1 || !ok {
		t.Fatalf("container mapping must replace only the selected gateway %s, got %#v", sourceImage, config.ContainerPins)
	}
	if !regexp.MustCompile(`^` + regexp.QuoteMeta(container) + `:[0-9a-f]{40}$`).MatchString(pin.Image) {
		t.Fatalf("weekly gateway must use the fork's full source-revision tag, got %q", pin.Image)
	}
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(pin.Digest) {
		t.Fatalf("weekly gateway requires an immutable SHA-256 digest, got %q", pin.Digest)
	}
	pinnedImage := pin.Image + "@" + pin.Digest

	workflow := repositoryFile(t, ".github", "workflows", "weekly-foundry-capability-review.lock.yml")
	_, manifestLine, ok := strings.Cut(workflow, "\n# gh-aw-manifest: ")
	if !ok {
		t.Fatal("compiled workflow is missing its container manifest")
	}
	manifestJSON, _, _ := strings.Cut(manifestLine, "\n")
	var manifest struct {
		Containers []struct {
			Image string `json:"image"`
		} `json:"containers"`
	}
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		t.Fatalf("parse container manifest: %v", err)
	}
	locked := 0
	for _, entry := range manifest.Containers {
		// Explicit digest mappings are recorded as complete image literals.
		if entry.Image == pinnedImage {
			locked++
		} else if strings.Contains(entry.Image, "/gh-aw-mcpg") {
			t.Errorf("manifest contains an unexpected gateway image %q", entry.Image)
		}
	}
	if locked != 1 {
		t.Errorf("manifest must contain exactly one matching gateway pin, got %d", locked)
	}
	predownloads, launches := 0, 0
	for _, step := range compiled.Jobs["agent"].Steps {
		if strings.Contains(step.Run, "download_docker_images.sh") {
			predownloads++
			if !slices.Contains(strings.Fields(step.Run), pinnedImage) {
				t.Errorf("gateway predownload must use %s", pinnedImage)
			}
		}
		for _, line := range strings.Split(step.Run, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "export MCP_GATEWAY_DOCKER_COMMAND=") {
				launches++
				if !strings.HasSuffix(line, " "+pinnedImage+"'") {
					t.Errorf("gateway launch must end with the same immutable image, got %s", line)
				}
			}
		}
	}
	if predownloads != 1 || launches != 1 {
		t.Errorf("expected one pinned predownload and launch, got %d and %d", predownloads, launches)
	}
	var workflows []string
	for _, pattern := range []string{"*.yml", "*.yaml"} {
		matches, err := filepath.Glob(filepath.Join("..", ".github", "workflows", pattern))
		if err != nil {
			t.Fatalf("list other workflows: %v", err)
		}
		workflows = append(workflows, matches...)
	}
	for _, filename := range workflows {
		if filepath.Base(filename) == "weekly-foundry-capability-review.lock.yml" {
			continue
		}
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatalf("read %s: %v", filename, err)
		}
		if strings.Contains(string(data), container) {
			t.Errorf("fork gateway must remain scoped to the weekly review, not %s", filename)
		}
	}
}

func TestWeeklyFoundryOfflineValidationUsesCompiledRuntime(t *testing.T) {
	var ci weeklyReviewDocument
	if err := yaml.Unmarshal([]byte(repositoryFile(t, ".github", "workflows", "ci.yml")), &ci); err != nil {
		t.Fatalf("parse CI workflow: %v", err)
	}
	coreSteps := ci.Jobs["ci"].Steps
	guardIndex := weeklyReviewStepIndex(t, coreSteps, "Verify tests preserve the checkout")
	if guardIndex <= weeklyReviewStepIndex(t, coreSteps, "Test with the race detector") ||
		guardIndex >= weeklyReviewStepIndex(t, coreSteps, "Build") {
		t.Fatal("checkout integrity must be checked after tests and before intentional build artifacts")
	}
	guard := coreSteps[guardIndex]
	if guard.If != "${{ github.event_name != 'workflow_dispatch' }}" {
		t.Error("historical release rebuilds must retain their original test behavior")
	}
	requireText(t, guard.Run, "git diff --exit-code HEAD --", "git ls-files --others", "exit 1")
	if strings.Contains(guard.Run, "--exclude-standard") {
		t.Error("checkout integrity must include ignored test artifacts")
	}
	job, ok := ci.Jobs["weekly-review-validation"]
	if !ok {
		t.Fatal("CI must retain full repository validation without inference")
	}
	if job.If != "${{ github.event_name == 'pull_request' || (github.event_name == 'push' && github.ref == 'refs/heads/main') }}" {
		t.Errorf("offline validation must cover PR/main without changing historical release rebuilds: %s", job.If)
	}
	if job.TimeoutMinutes != 10 || !reflect.DeepEqual(job.Permissions, map[string]string{"contents": "read"}) {
		t.Errorf("offline validation must remain bounded and read-only: %#v", job)
	}
	source := job.Steps[weeklyReviewStepIndex(t, job.Steps, "Checkout FAM candidate")]
	if source.With["path"] != "fam" || source.With["persist-credentials"] != false {
		t.Error("FAM source must have its own credential-free checkout")
	}
	pinIndex := weeklyReviewStepIndex(t, job.Steps, "runtime")
	checkoutIndex := weeklyReviewStepIndex(t, job.Steps, "Checkout the pinned validation runtime")
	validateIndex := weeklyReviewStepIndex(t, job.Steps, "Validate the full FAM publication tree without inference")
	if pinIndex >= checkoutIndex || checkoutIndex >= validateIndex {
		t.Fatal("resolve the compiler pin before checking out and executing the runtime")
	}
	requireText(t, job.Steps[pinIndex].Run,
		"node scripts/Test-WeeklyReviewValidation.cjs pin",
		"^[0-9a-f]{40}$",
	)
	checkout := job.Steps[checkoutIndex]
	if checkout.With["repository"] != "jpmicrosoft/gh-aw" ||
		checkout.With["ref"] != "${{ steps.runtime.outputs.sha }}" ||
		checkout.With["path"] != "runtime" || checkout.With["persist-credentials"] != false {
		t.Fatalf("offline validation must use the compiled fork revision outside FAM: %#v", checkout.With)
	}
	install := job.Steps[weeklyReviewStepIndex(t, job.Steps, "Install the pinned runtime dependencies outside FAM")]
	if install.WorkingDirectory != "runtime/actions/setup/js" {
		t.Error("SDK dependencies must not be installed into the FAM checkout")
	}
	validation := job.Steps[validateIndex]
	if validation.WorkingDirectory != "fam" ||
		validation.Run != `node scripts/Test-WeeklyReviewValidation.cjs validate "$GITHUB_WORKSPACE/runtime"` {
		t.Fatalf("CI must exercise the full FAM publication-tree validator: %#v", validation)
	}
}

func TestWeeklyFoundryOfflinePinRejectsIncoherentConfiguration(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("Node is required for the weekly validation CI contract")
		}
		t.Skip("Node is unavailable")
	}
	script := repositoryFile(t, "scripts", "Test-WeeklyReviewValidation.cjs")
	revision := strings.Repeat("a", 40)
	cases := []struct {
		name     string
		compiler string
		runtime  string
		strict   bool
		wantPass bool
	}{
		{name: "coherent", compiler: revision, runtime: revision, strict: true, wantPass: true},
		{name: "non-strict", compiler: revision, runtime: revision},
		{name: "moving-ref", compiler: "main", runtime: "main", strict: true},
		{name: "mismatched-runtime", compiler: revision, runtime: strings.Repeat("b", 40), strict: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write := func(parts []string, data []byte) string {
				t.Helper()
				filename := filepath.Join(append([]string{root}, parts...)...)
				if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filename, data, 0o600); err != nil {
					t.Fatal(err)
				}
				return filename
			}
			filename := write([]string{"scripts", "Test-WeeklyReviewValidation.cjs"}, []byte(script))
			metadata, err := json.Marshal(map[string]any{"compiler_version": tc.compiler, "strict": tc.strict})
			if err != nil {
				t.Fatal(err)
			}
			write([]string{".github", "workflows", "weekly-foundry-capability-review.lock.yml"},
				[]byte("# gh-aw-metadata: "+string(metadata)+"\n"))
			const action = "jpmicrosoft/gh-aw/actions/setup"
			lock, err := json.Marshal(map[string]any{"entries": map[string]any{
				action + "@" + tc.compiler: map[string]string{"repo": action, "version": tc.compiler, "sha": tc.runtime},
			}})
			if err != nil {
				t.Fatal(err)
			}
			write([]string{".github", "aw", "actions-lock.json"}, lock)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, node, filename, "pin").CombinedOutput()
			if tc.wantPass {
				if err != nil || strings.TrimSpace(string(output)) != revision {
					t.Fatalf("coherent pin failed: %v\n%s", err, output)
				}
				return
			}
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 1 {
				t.Fatalf("invalid pin did not fail explicitly: %v\n%s", err, output)
			}
			var failure struct {
				Validation string   `json:"validation"`
				Errors     []string `json:"errors"`
			}
			if err := json.Unmarshal(output, &failure); err != nil {
				t.Fatalf("failure is not one valid JSON diagnostic: %v\n%s", err, output)
			}
			if failure.Validation != "failed" || len(failure.Errors) == 0 {
				t.Fatalf("invalid pin produced no diagnostic: %s", output)
			}
		})
	}
}

func TestWeeklyFoundryChangelogPublicationProtection(t *testing.T) {
	_, compiled := weeklyReviewDocuments(t)
	for _, target := range []struct {
		job  string
		step string
		env  string
	}{
		{"agent", "Generate Safe Outputs Config", "GH_AW_SAFE_OUTPUTS_CONFIG"},
		{"safe_outputs", "process_safe_outputs", "GH_AW_SAFE_OUTPUTS_HANDLER_CONFIG"},
	} {
		t.Run(target.job, func(t *testing.T) {
			steps := compiled.Jobs[target.job].Steps
			step := steps[weeklyReviewStepIndex(t, steps, target.step)]
			var config struct {
				CreatePullRequest struct {
					ProtectedFiles       []string `json:"protected_files"`
					ProtectedFilesPolicy string   `json:"protected_files_policy"`
					ExcludedFiles        []string `json:"excluded_files"`
				} `json:"create_pull_request"`
			}
			if err := json.Unmarshal([]byte(step.Env[target.env]), &config); err != nil {
				t.Fatalf("parse %s: %v", target.env, err)
			}
			pr := config.CreatePullRequest
			if pr.ProtectedFilesPolicy != "blocked" || !slices.Contains(pr.ProtectedFiles, "CHANGELOG.md") {
				t.Fatal("CHANGELOG.md must retain basename protection, including nested changelogs, with publication blocked")
			}
			if slices.Contains(pr.ProtectedFiles, "README.md") {
				t.Fatal("the explicit README.md protection exception must remain in effect")
			}
			if !reflect.DeepEqual(pr.ExcludedFiles, []string{".github/**", ".release-qualification/**", "CHANGELOG.md", "LICENSE"}) {
				t.Fatal("patch exclusions must remain unchanged; do not silently strip nested changelogs instead of blocking publication")
			}
		})
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
		source.Engine.ToolProfile != "go-repository" ||
		source.MaxToolDenials != 1 || source.MaxAICredits != 1000 ||
		source.Engine.Harness.MaxRetries == nil || *source.Engine.Harness.MaxRetries != 0 {
		t.Fatal("review must use the Copilot SDK denial limit, no retries, and a 1000-credit cap")
	}
	agent := compiled.Jobs["agent"]
	execution := agent.Steps[weeklyReviewStepIndex(t, agent.Steps, "agentic_execution")]
	for key, want := range map[string]string{
		"GH_AW_COPILOT_SDK_DRIVER":  "1",
		"GH_AW_MAX_TOOL_DENIALS":    "1",
		"GH_AW_HARNESS_MAX_RETRIES": "0",
	} {
		if execution.Env[key] != want {
			t.Errorf("compiled %s = %q, want %q", key, execution.Env[key], want)
		}
	}
	if !strings.Contains(execution.Run, "copilot_sdk_driver.cjs") {
		t.Fatal("denial limit must be wired to the SDK driver, not the CLI-only path")
	}
	sdkInstalledOutsideCheckout := false
	for _, step := range agent.Steps {
		if step.Name == "Install GitHub Copilot SDK (Node.js)" {
			sdkInstalledOutsideCheckout = strings.Contains(step.Run, "${RUNNER_TEMP}/gh-aw/copilot-sdk") &&
				!strings.Contains(step.Run, "GITHUB_WORKSPACE")
		}
	}
	if !sdkInstalledOutsideCheckout ||
		!strings.Contains(execution.Run, `export NODE_PATH="${RUNNER_TEMP}/gh-aw/copilot-sdk/node_modules"`) {
		t.Fatal("SDK dependencies must be installed and resolved outside the clean reviewed checkout")
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
	if source.Tools.Bash == nil || *source.Tools.Bash ||
		source.Tools.CLIProxy == nil || *source.Tools.CLIProxy {
		t.Fatal("review must explicitly disable general Bash and CLI proxies")
	}
	var toolConfig struct {
		Version      int `json:"version"`
		Capabilities struct {
			Bash     bool `json:"bash"`
			CLIProxy bool `json:"cliProxy"`
			Edit     bool `json:"edit"`
			MCP      bool `json:"mcp"`
		} `json:"capabilities"`
		Permissions struct {
			AllowedTools []string `json:"allowedTools"`
		} `json:"permissions"`
		ExplicitlyDisabledTools []string `json:"explicitlyDisabledTools"`
		Profile                 struct {
			ID                      string         `json:"id"`
			RepositoryDefaultBranch string         `json:"repositoryDefaultBranch"`
			Policy                  map[string]any `json:"policy"`
		} `json:"profile"`
	}
	rawToolConfig := execution.Env["GH_AW_COPILOT_SDK_TOOL_CONFIG"]
	if err := json.Unmarshal([]byte(rawToolConfig), &toolConfig); err != nil {
		t.Fatalf("parse compiler-owned SDK tool contract: %v", err)
	}
	if toolConfig.Version != 2 || toolConfig.Profile.ID != "go-repository" ||
		toolConfig.Capabilities.Bash || toolConfig.Capabilities.CLIProxy ||
		!toolConfig.Capabilities.Edit || !toolConfig.Capabilities.MCP {
		t.Fatal("compiled SDK contract must expose the native no-shell repository profile")
	}
	for _, permission := range []string{"read", "write", "go_repository", "safeoutputs"} {
		if !slices.Contains(toolConfig.Permissions.AllowedTools, permission) {
			t.Errorf("SDK contract is missing %s", permission)
		}
	}
	for _, permission := range toolConfig.Permissions.AllowedTools {
		if permission == "*" || permission == "shell" || strings.HasPrefix(permission, "shell(") {
			t.Errorf("SDK repository profile must not grant %s", permission)
		}
	}
	for _, disabled := range []string{"bash", "cli-proxy"} {
		if !slices.Contains(toolConfig.ExplicitlyDisabledTools, disabled) {
			t.Errorf("SDK contract must explicitly disable %s", disabled)
		}
	}
	if toolConfig.Profile.Policy["target-repo"] != "jpmicrosoft/fam" ||
		toolConfig.Profile.Policy["base_branch"] != "main" ||
		toolConfig.Profile.Policy["protected_files_policy"] != "blocked" {
		t.Fatal("repository tool must retain FAM's publication policy")
	}
	if toolConfig.Profile.RepositoryDefaultBranch != "${GH_AW_GITHUB_EVENT_REPOSITORY_DEFAULT_BRANCH}" ||
		execution.Env["GH_AW_GITHUB_EVENT_REPOSITORY_DEFAULT_BRANCH"] != "${{ github.event.repository.default_branch }}" {
		t.Fatal("SDK default-branch metadata must use the trusted runtime binding")
	}
	if strings.Contains(rawToolConfig, "github-token") || strings.Contains(rawToolConfig, "secrets.") {
		t.Fatal("SDK repository policy must not contain publication credentials")
	}
	if !strings.Contains(execution.Run, "mcp-config/copilot-sdk.json") {
		t.Fatal("SDK native MCP configuration must be staged in the already-mounted runtime directory")
	}
	pr, ok := source.SafeOutputs["create-pull-request"].(map[string]any)
	if !ok || pr["draft"] != true || pr["max"] != 1 ||
		pr["target-repo"] != "jpmicrosoft/fam" || pr["base-branch"] != "main" {
		t.Fatal("review must retain its single-draft-PR publication boundary")
	}
	for runtime, authoring := range map[string]string{
		"allowed_files": "allowed-files", "excluded_files": "excluded-files",
		"allowed_branches": "allowed-branches",
	} {
		if !reflect.DeepEqual(toolConfig.Profile.Policy[runtime], pr[authoring]) {
			t.Errorf("SDK policy %s differs from the publication policy", runtime)
		}
	}
	protected, ok := toolConfig.Profile.Policy["protected_files"].([]any)
	if !ok || !slices.ContainsFunc(protected, func(value any) bool {
		filename, ok := value.(string)
		return ok && filename == "CHANGELOG.md"
	}) {
		t.Fatal("SDK repository policy must preserve changelog protection by basename")
	}
}

func TestWeeklyFoundryToolUseGuidance(t *testing.T) {
	source := repositoryFile(t, ".github", "workflows", "weekly-foundry-capability-review.md")
	guidance := strings.Join(strings.Fields(source), " ")
	for _, required := range []string{
		"## Tool-use contract",
		"`view` for file contents and line ranges",
		"native `grep` for content searches and `glob` for file discovery",
		"no general shell, CLI proxy, or task/subagent tools",
		"`go_repository` actions `status` and `diff`",
		"Do not invoke Bash, PowerShell",
		"Run `go_repository` operations sequentially",
		`{"action":"readiness"}`,
		"Stop after the first permission denial",
		"The runtime aborts on the first denial",
		"Do not switch to `view` or another permitted tool after a denial",
		"Do not attempt a reporting call after a permission denial",
		"the exact projected publication tree inside AWF",
		"## Completion reporting",
		"native `safeoutputs-noop` and `safeoutputs-create_pull_request` tools",
		`{"message":"COMPLETE:`,
		`{"message":"BLOCKED:`,
		`{"action":"commit"}`,
		"The native PR tool requires committed changes",
		"A blocked no-op is a failure report, not successful completion",
	} {
		if !strings.Contains(guidance, required) {
			t.Errorf("weekly review is missing required tool-use guidance %q", required)
		}
		for _, obsolete := range []string{"safeoutputs noop --message", "safeoutputs create_pull_request . <", "git grep -n", "gofmt -l .", "go build -o fam"} {
			if strings.Contains(guidance, obsolete) {
				t.Errorf("weekly review still instructs the model to run obsolete shell command %q", obsolete)
			}
		}
	}
}

func TestWeeklyFoundryCompletionGateWiring(t *testing.T) {
	source, compiled := weeklyReviewDocuments(t)
	sourceGate := source.PostSteps[weeklyReviewStepIndex(t, source.PostSteps, "review_completion")]
	steps := compiled.Jobs["agent"].Steps
	gateIndex := weeklyReviewStepIndex(t, steps, "review_completion")
	gate := steps[gateIndex]
	sourceScript, sourceOK := sourceGate.With["script"].(string)
	script, ok := gate.With["script"].(string)
	if !sourceOK || !ok {
		t.Fatal("completion gate is missing its inline script")
	}
	sourceGate.With["script"] = strings.TrimSpace(sourceScript)
	gate.With["script"] = strings.TrimSpace(script)
	if !reflect.DeepEqual(sourceGate, gate) {
		t.Fatal("completion gate must compile unchanged from trusted inline source")
	}
	if gate.If != "success()" || gate.ContinueOnError ||
		gate.Uses != "actions/github-script@3a2844b7e9c422d3c10d287c895573f7108da1b3" ||
		gate.Env["GH_AW_AGENT_OUTPUT"] != "/tmp/gh-aw/agent_output.json" ||
		gate.TimeoutMinutes != 1 {
		t.Fatal("completion gate must be pinned, bounded, fail closed, and preserve earlier failures")
	}
	if !strings.Contains(script, "require('node:fs')") ||
		!strings.Contains(script, "core.setFailed(") ||
		strings.Contains(script, "require('./") || strings.Contains(script, "GITHUB_WORKSPACE") {
		t.Fatal("completion validation must run inline, not execute agent-writable worktree code")
	}
	order := []int{
		weeklyReviewStepIndex(t, steps, "agentic_execution"),
		weeklyReviewStepIndex(t, steps, "collect_output"),
		weeklyReviewStepIndex(t, steps, "Write agent output placeholder if missing"),
		gateIndex,
		weeklyReviewStepIndex(t, steps, "Upload agent output fallback artifact"),
		weeklyReviewStepIndex(t, steps, "Upload agent artifacts"),
	}
	for index := 1; index < len(order); index++ {
		if order[index-1] >= order[index] {
			t.Fatal("gate must follow ingestion and placeholder creation, but precede both agent uploads")
		}
	}
	for _, name := range []string{"Copy Safe Outputs", "collect_output", "Upload agent output fallback artifact", "Upload agent artifacts"} {
		step := steps[weeklyReviewStepIndex(t, steps, name)]
		if step.If != "always()" {
			t.Errorf("%s must preserve queued outputs after failure", name)
		}
		if strings.HasPrefix(name, "Upload ") && !step.ContinueOnError {
			t.Errorf("%s must retain failure-tolerant artifact recovery", name)
		}
	}
}

func TestWeeklyFoundryCompletionGateBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required to exercise the completion gate in CI")
		}
		t.Skip("node is unavailable; completion behavior is exercised by CI")
	}
	source, _ := weeklyReviewDocuments(t)
	gate := source.PostSteps[weeklyReviewStepIndex(t, source.PostSteps, "review_completion")]
	script, ok := gate.With["script"].(string)
	if !ok {
		t.Fatal("completion gate is missing its inline script")
	}
	const noop = `{"type":"noop","message":"COMPLETE: All baseline comparisons completed; no actionable changes."}`
	const pr = `{"type":"create_pull_request","title":"Correct a verified contract","body":"Evidence and validation results.","branch":"automation/foundry-capability-review-20260913","base_commit":"fixture-base"}`
	envelope := func(items string) string { return `{"items":[` + items + `],"errors":[]}` }
	validNoop := envelope(noop)
	const limit = 1024 * 1024
	tests := []struct {
		name    string
		content string
		valid   bool
	}{
		{"missing", "", false},
		{"empty file", "", false},
		{"directory", "", false},
		{"invalid JSON", `{"items":[PAYLOAD_MUST_NOT_BE_LOGGED]}`, false},
		{"null envelope", `null`, false},
		{"array envelope", `[]`, false},
		{"text-only blocked", "BLOCKED: tool failure", false},
		{"placeholder", `{"items":[]}`, false},
		{"empty collected output", envelope(""), false},
		{"missing items", `{"errors":[]}`, false},
		{"object items", `{"items":{},"errors":[]}`, false},
		{"missing errors", `{"items":[` + noop + `]}`, false},
		{"malformed errors", `{"items":[` + noop + `],"errors":{}}`, false},
		{"reported ingestion error", `{"items":[` + noop + `],"errors":["PAYLOAD_MUST_NOT_BE_LOGGED"]}`, false},
		{"null item", envelope("null"), false},
		{"string item", envelope(`"noop"`), false},
		{"missing type", envelope(`{"message":"COMPLETE: Done"}`), false},
		{"missing noop message", envelope(`{"type":"noop"}`), false},
		{"empty noop message", envelope(`{"type":"noop","message":" "}`), false},
		{"nonstring noop message", envelope(`{"type":"noop","message":true}`), false},
		{"untagged noop", envelope(`{"type":"noop","message":"No changes"}`), false},
		{"blocked noop", envelope(`{"type":"noop","message":"BLOCKED: validation failed"}`), false},
		{"completion tag without explanation", envelope(`{"type":"noop","message":"COMPLETE: "}`), false},
		{"missing tool", envelope(`{"type":"missing_tool","tool":"sed","reason":"Unavailable"}`), false},
		{"missing data", envelope(`{"type":"missing_data","data_type":"source","reason":"Unavailable"}`), false},
		{"incomplete report", envelope(`{"type":"report_incomplete","reason":"Unavailable"}`), false},
		{"missing PR title", envelope(`{"type":"create_pull_request","body":"Evidence","branch":"automation/foundry-capability-review-20260913"}`), false},
		{"empty PR body", envelope(`{"type":"create_pull_request","title":"Fix","body":" ","branch":"automation/foundry-capability-review-20260913"}`), false},
		{"missing PR branch", envelope(`{"type":"create_pull_request","title":"Fix","body":"Evidence"}`), false},
		{"wrong PR branch", envelope(`{"type":"create_pull_request","title":"Fix","body":"Evidence","branch":"main"}`), false},
		{"noop mixed with diagnostic", envelope(noop + `,{"type":"missing_data"}`), false},
		{"PR mixed with error", `{"items":[` + pr + `],"errors":["PAYLOAD_MUST_NOT_BE_LOGGED"]}`, false},
		{"duplicate PR declarations", envelope(pr + "," + pr), false},
		{"duplicate noops", envelope(noop + "," + noop), false},
		{"completed noop", validNoop, true},
		{"PR declaration before publication", envelope(pr), true},
		{"PR with completed summary", envelope(pr + "," + noop), true},
		{"at size limit", validNoop + strings.Repeat(" ", limit-len(validNoop)), true},
		{"over size limit", validNoop + strings.Repeat(" ", limit-len(validNoop)+1), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			outputPath := filepath.Join(directory, "agent_output.json")
			if test.name == "directory" {
				if err := os.Mkdir(outputPath, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if test.name != "missing" {
				if err := os.WriteFile(outputPath, []byte(test.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			patchPath := filepath.Join(directory, "aw-fixture.patch")
			const patch = "already-queued patch must survive completion failure\n"
			if err := os.WriteFile(patchPath, []byte(patch), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, node, "-e", `
const core = {
  setFailed: message => { console.error(message); process.exitCode = 1; },
  info: message => console.log(message)
};
(async () => {
`+script+`
})().catch(error => { console.error(error); process.exitCode = 2; });
`)
			command.Dir = directory
			command.Env = append(os.Environ(), "GH_AW_AGENT_OUTPUT="+outputPath)
			output, runErr := command.CombinedOutput()
			if test.valid {
				if runErr != nil {
					t.Fatalf("valid completion failed: %v\n%s", runErr, output)
				}
			} else {
				exit, ok := runErr.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "Weekly review completion") {
					t.Fatalf("invalid completion must fail explicitly: %v\n%s", runErr, output)
				}
			}
			if strings.Contains(string(output), "PAYLOAD_MUST_NOT_BE_LOGGED") {
				t.Fatal("completion diagnostics must not echo untrusted payloads")
			}
			if test.name != "missing" && test.name != "directory" {
				data, err := os.ReadFile(outputPath)
				if err != nil || string(data) != test.content {
					t.Fatal("gate must not rewrite or discard collected safe outputs")
				}
			}
			data, err := os.ReadFile(patchPath)
			if err != nil || string(data) != patch {
				t.Fatal("gate must not rewrite or discard queued patches")
			}
		})
	}
}

func TestWeeklyFoundryGoCachesMounted(t *testing.T) {
	source, compiled := weeklyReviewDocuments(t)
	wantMounts := []string{
		"${{ env.GOMODCACHE }}:${{ env.GOMODCACHE }}:ro",
		"${{ env.GOCACHE }}:${{ env.GOCACHE }}:rw",
	}
	if source.Sandbox.Agent.ID != "awf" ||
		!reflect.DeepEqual(source.Sandbox.Agent.Mounts, wantMounts) {
		t.Fatal("sandbox must mount only the verified module cache read-only and the build cache read-write")
	}
	agent := compiled.Jobs["agent"]
	execution := agent.Steps[weeklyReviewStepIndex(t, agent.Steps, "agentic_execution")]
	for _, mount := range wantMounts {
		if !strings.Contains(execution.Run, `--mount "`+mount+`"`) {
			t.Errorf("compiled sandbox is missing cache mount %q; inheriting environment variables does not expose host files", mount)
		}
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
