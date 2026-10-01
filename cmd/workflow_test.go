package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"foundry-agent-manager/internal/config"

	"gopkg.in/yaml.v3"
)

func workflowPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", ".github", "workflows", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func loadWorkflow(t *testing.T, name string) (map[string]interface{}, string) {
	t.Helper()
	data, err := os.ReadFile(workflowPath(t, name))
	if err != nil {
		t.Fatalf("failed to read %s: %v", name, err)
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatalf("%s is not valid YAML: %v", name, err)
	}
	return document, string(data)
}

// workflowTriggers returns the "on:" mapping, which YAML parses as the boolean
// key true unless it is quoted.
func workflowTriggers(t *testing.T, document map[string]interface{}) map[string]interface{} {
	t.Helper()
	for _, value := range []interface{}{document["on"], document["true"]} {
		if triggers, ok := value.(map[string]interface{}); ok {
			return triggers
		}
	}
	t.Fatalf("workflow has no trigger mapping: %#v", document)
	return nil
}

func TestWorkflowsParseAndUseLeastPrivilegeTriggers(t *testing.T) {
	tests := map[string]struct {
		triggers    []string
		permissions map[string]string
	}{
		"ci.yml": {
			triggers:    []string{"pull_request", "push", "workflow_dispatch"},
			permissions: map[string]string{"contents": "read"},
		},
		"codeql.yml": {
			triggers:    []string{"pull_request", "push", "schedule", "workflow_dispatch"},
			permissions: map[string]string{"actions": "read", "contents": "read"},
		},
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			document, raw := loadWorkflow(t, name)
			triggers := workflowTriggers(t, document)
			if len(triggers) != len(want.triggers) {
				t.Fatalf("unexpected triggers: %#v", triggers)
			}
			for _, trigger := range want.triggers {
				if _, ok := triggers[trigger]; !ok {
					t.Fatalf("missing trigger %q: %#v", trigger, triggers)
				}
			}
			// Triggers that run untrusted code with repository write access must
			// never appear.
			for _, forbidden := range []string{
				"pull_request_target", "workflow_run", "issue_comment", "workflow_call",
			} {
				if _, ok := triggers[forbidden]; ok {
					t.Fatalf("%s uses the unsafe trigger %q", name, forbidden)
				}
			}
			permissions, ok := document["permissions"].(map[string]interface{})
			if !ok {
				t.Fatalf("%s does not declare explicit permissions", name)
			}
			if len(permissions) != len(want.permissions) {
				t.Fatalf("%s declares unexpected permissions: %#v", name, permissions)
			}
			for scope, level := range want.permissions {
				if permissions[scope] != level {
					t.Fatalf("%s permission %q is %v, want %q", name, scope, permissions[scope], level)
				}
			}
			if strings.Contains(raw, "permissions: write-all") {
				t.Fatalf("%s grants write-all", name)
			}
		})
	}
}

func workflowRunScripts(t *testing.T, document map[string]interface{}) []string {
	t.Helper()
	jobs, ok := document["jobs"].(map[string]interface{})
	if !ok {
		t.Fatalf("workflow has no jobs mapping: %#v", document)
	}
	var scripts []string
	for jobName, rawJob := range jobs {
		job, ok := rawJob.(map[string]interface{})
		if !ok {
			t.Fatalf("workflow job %q is not a mapping: %#v", jobName, rawJob)
		}
		steps, ok := job["steps"].([]interface{})
		if !ok {
			t.Fatalf("workflow job %q has no steps list: %#v", jobName, job)
		}
		for stepIndex, rawStep := range steps {
			step, ok := rawStep.(map[string]interface{})
			if !ok {
				t.Fatalf("workflow job %q step %d is not a mapping: %#v", jobName, stepIndex, rawStep)
			}
			if run, ok := step["run"].(string); ok {
				scripts = append(scripts, run)
			}
		}
	}
	return scripts
}

// TestWorkflowRunStepsDoNotInterpolateUntrustedEventData guards against the
// classic GitHub Actions script-injection pattern.
func TestWorkflowRunStepsDoNotInterpolateUntrustedEventData(t *testing.T) {
	untrusted := regexp.MustCompile(`\$\{\{\s*(github\.event\.|github\.head_ref|inputs\.)`)
	for _, name := range []string{"ci.yml", "codeql.yml", "live-evaluator-calibration.yml"} {
		document, _ := loadWorkflow(t, name)
		for _, script := range workflowRunScripts(t, document) {
			if match := untrusted.FindString(script); match != "" {
				t.Fatalf("%s interpolates untrusted event data (%q) into a run script", name, match)
			}
		}
	}
}

func TestPublicationCheckoutsDoNotPersistCredentials(t *testing.T) {
	for _, name := range []string{"ci.yml", "codeql.yml", "live-evaluator-calibration.yml"} {
		document, _ := loadWorkflow(t, name)
		for jobName, rawJob := range document["jobs"].(map[string]interface{}) {
			job := rawJob.(map[string]interface{})
			for _, rawStep := range job["steps"].([]interface{}) {
				step := rawStep.(map[string]interface{})
				uses, _ := step["uses"].(string)
				if !strings.HasPrefix(uses, "actions/checkout@") {
					continue
				}
				settings, ok := step["with"].(map[string]interface{})
				if !ok || settings["persist-credentials"] != false {
					t.Errorf("%s/%s checkout %v persists credentials", name, jobName, step["name"])
				}
			}
		}
	}
}

func workflowStep(t *testing.T, document map[string]interface{}, jobName, id string) map[string]interface{} {
	t.Helper()
	job := document["jobs"].(map[string]interface{})[jobName].(map[string]interface{})
	for _, raw := range job["steps"].([]interface{}) {
		step := raw.(map[string]interface{})
		if step["id"] == id {
			return step
		}
	}
	t.Fatalf("missing workflow step %s/%s", jobName, id)
	return nil
}

func runWorkflowBash(t *testing.T, root, script string, environment ...string) (string, error) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is required for workflow execution tests")
	}
	command := exec.Command(bash, "-c", script)
	command.Dir = root
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestReleaseSourceRejectsBranchTagCollision(t *testing.T) {
	document, _ := loadWorkflow(t, "ci.yml")
	step := workflowStep(t, document, "ci", "release-source")
	script := step["run"].(string)
	for _, name := range []string{"update-native", "hosted-skills-runtime", "release"} {
		if other := workflowStep(t, document, name, "release-source"); other["run"] != script {
			t.Fatalf("%s must use the same release source validation", name)
		}
	}
	for name, raw := range document["jobs"].(map[string]interface{}) {
		job := raw.(map[string]interface{})
		for _, rawStep := range job["steps"].([]interface{}) {
			candidate := rawStep.(map[string]interface{})
			settings, _ := candidate["with"].(map[string]interface{})
			if candidate["name"] == "Checkout selected release tag" &&
				settings["ref"] != "${{ format('refs/tags/{0}', inputs.tag) }}" {
				t.Errorf("%s must select the fully qualified tag, not a same-named branch", name)
			}
		}
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("Git is required for release source tests")
	}
	root := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		flags := []string{"-c", "user.name=FAM fixture", "-c", "user.email=fixture@example.invalid",
			"-c", "commit.gpgsign=false", "-c", "tag.gpgSign=false",
			"-c", "core.hooksPath=" + filepath.Join(root, "no-hooks")}
		command := exec.Command(git, append(flags, args...)...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	runGit("init", "--quiet")
	runGit("commit", "--quiet", "--allow-empty", "-m", "Tagged fixture")
	tagCommit := runGit("rev-parse", "HEAD")
	runGit("tag", "-a", "v0.17.1", "-m", "Annotated fixture")
	for _, tag := range []string{"v0.18.0", "v1.0.0", "v0.117.0"} {
		runGit("tag", tag)
	}
	runGit("commit", "--quiet", "--allow-empty", "-m", "Same-named branch fixture")
	branchCommit := runGit("rev-parse", "HEAD")
	runGit("branch", "v0.17.1")
	for _, test := range []struct {
		name, tag, event, commit string
		historical, fail         bool
	}{
		{"historical annotated tag", "v0.17.1", "workflow_dispatch", tagCommit, true, false},
		{"same-named branch", "v0.17.1", "workflow_dispatch", branchCommit, false, true},
		{"current tag", "v0.18.0", "workflow_dispatch", tagCommit, false, false},
		{"future major", "v1.0.0", "workflow_dispatch", tagCommit, false, false},
		{"future minor", "v0.117.0", "workflow_dispatch", tagCommit, false, false},
		{"pushed old tag", "v0.17.1", "push", tagCommit, false, false},
		{"missing tag", "v0.16.0", "workflow_dispatch", tagCommit, false, true},
		{"invalid input", "v0.17.1; exit 0", "workflow_dispatch", tagCommit, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runGit("checkout", "--quiet", "--detach", test.commit)
			outputPath := filepath.Join(t.TempDir(), "outputs")
			output, err := runWorkflowBash(t, root, script, "RELEASE_TAG="+test.tag,
				"GITHUB_EVENT_NAME="+test.event, "GITHUB_OUTPUT="+outputPath)
			if (err != nil) != test.fail {
				t.Fatalf("failure=%v, want %v: %s", err, test.fail, output)
			}
			if !test.fail {
				data, err := os.ReadFile(outputPath)
				want := "historical=false"
				if test.historical {
					want = "historical=true"
				}
				if err != nil || strings.TrimSpace(string(data)) != want {
					t.Fatalf("historical output=%q, error=%v, want %s", data, err, want)
				}
			}
		})
	}
}

func TestHostedSkillsQualificationInputsFailClosed(t *testing.T) {
	document, _ := loadWorkflow(t, "ci.yml")
	step := workflowStep(t, document, "hosted-skills-runtime", "runtime-inputs")
	environment := step["env"].(map[string]interface{})
	if environment["HISTORICAL_REBUILD"] != "${{ steps.release-source.outputs.historical }}" {
		t.Fatal("historical exception must come from validated release source")
	}
	script := step["run"].(string)
	inputs := []string{
		"internal/skillruntime/runtime.go",
		"internal/skillruntime/templates/fam_skills_runtime.py",
		"internal/skillruntime/templates/FamSkillsRuntime.cs",
		"internal/skillruntime/testdata/requirements.txt",
		"internal/skillruntime/testdata/python_runtime_test.py",
		"examples/hosted-skills/dotnet/HostedSkills.Example.csproj",
		"examples/hosted-skills/dotnet/McpRuntimeTests.cs",
	}
	check := func(t *testing.T, root, historical string, fail, required bool) {
		t.Helper()
		outputPath := filepath.Join(t.TempDir(), "outputs")
		output, err := runWorkflowBash(t, root, script,
			"HISTORICAL_REBUILD="+historical, "GITHUB_OUTPUT="+outputPath)
		if (err != nil) != fail {
			t.Fatalf("failure=%v, want %v: %s", err, fail, output)
		}
		if !fail {
			data, err := os.ReadFile(outputPath)
			want := "required=false"
			if required {
				want = "required=true"
			}
			if err != nil || strings.TrimSpace(string(data)) != want {
				t.Fatalf("required output=%q, error=%v, want %s", data, err, want)
			}
		}
	}
	t.Run("current source missing runtime", func(t *testing.T) {
		check(t, t.TempDir(), "", true, false)
	})
	t.Run("current tag missing runtime", func(t *testing.T) {
		check(t, t.TempDir(), "false", true, false)
	})
	t.Run("validated historical absence", func(t *testing.T) {
		check(t, t.TempDir(), "true", false, false)
	})
	for _, missing := range append([]string{""}, inputs...) {
		t.Run("missing="+missing, func(t *testing.T) {
			root := t.TempDir()
			for _, input := range inputs {
				if input == missing {
					continue
				}
				path := filepath.Join(root, filepath.FromSlash(input))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			check(t, root, "false", missing != "", true)
			check(t, root, "true", missing != "", true)
		})
	}
}

func TestCIWorkflowRunsTheSameGatesAsThisSuite(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	for _, want := range []string{
		"gofmt -l .",
		"go vet ./...",
		"go test -count=1 ./...",
		"go test -count=1 -race ./...",
		"go build",
		"go-version-file: go.mod",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("ci.yml no longer runs %q", want)
		}
	}
}

func TestReleaseJobRequiresCIGate(t *testing.T) {
	document, raw := loadWorkflow(t, "ci.yml")
	for _, want := range []string{
		"release:",
		"needs: [ci, update-native, hosted-skills-runtime]",
		"startsWith(github.ref, 'refs/tags/v')",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("combined workflow no longer requires the CI gate %q", want)
		}
	}
	jobs := document["jobs"].(map[string]interface{})
	release := jobs["release"].(map[string]interface{})
	needs, ok := release["needs"].([]interface{})
	if !ok || len(needs) != 3 || needs[0] != "ci" || needs[1] != "update-native" || needs[2] != "hosted-skills-runtime" {
		t.Fatalf("release job needs = %#v, want ci, update-native and hosted-skills-runtime", release["needs"])
	}

	permissions, ok := release["permissions"].(map[string]interface{})
	if !ok {
		t.Fatalf("release job has no explicit permissions: %#v", release)
	}
	for scope, level := range map[string]string{
		"contents":     "write",
		"id-token":     "write",
		"attestations": "write",
	} {
		if permissions[scope] != level {
			t.Fatalf("release permission %q = %#v, want %q", scope, permissions[scope], level)
		}
	}
}

func TestUpdateNativeWorkflow(t *testing.T) {
	document, _ := loadWorkflow(t, "ci.yml")
	jobs := document["jobs"].(map[string]interface{})
	job, ok := jobs["update-native"].(map[string]interface{})
	if !ok {
		t.Fatal("missing native updater gate")
	}
	if job["runs-on"] != "${{ matrix.os }}" {
		t.Fatalf("native updater must use its platform matrix: %#v", job)
	}
	strategy := job["strategy"].(map[string]interface{})
	matrix := strategy["matrix"].(map[string]interface{})
	platforms := matrix["os"].([]interface{})
	if len(platforms) != 2 || platforms[0] != "windows-latest" || platforms[1] != "macos-latest" {
		t.Fatalf("native updater platforms: %#v", platforms)
	}
	for _, raw := range job["steps"].([]interface{}) {
		step := raw.(map[string]interface{})
		if step["run"] == "go test -count=1 ./internal/update" {
			return
		}
	}
	t.Fatal("native updater gate does not run updater tests")
}

func TestReleaseWorkflowTagPatternAcceptsOnlySemVer(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	pattern := regexp.MustCompile(`\^v\(0\|\[1-9\]\[0-9\]\*\)[^"]*\$`)
	found := pattern.FindString(raw)
	if found == "" {
		t.Fatalf("combined workflow no longer validates the tag shape:\n%s", raw)
	}
	// The workflow uses a POSIX ERE that RE2 also accepts.
	tagPattern, err := regexp.Compile(strings.ReplaceAll(found, `\\`, `\`))
	if err != nil {
		t.Fatalf("the release tag pattern does not compile: %v", err)
	}
	accepted := []string{"v0.2.0", "v1.0.0", "v10.20.30", "v1.0.0-rc.1", "v1.0.0+build.5", "v1.0.0-rc.1+build.5"}
	rejected := []string{
		"0.2.0", "v01.2.0", "v1.2", "v1.2.0.1", "v1.2.3-", "vlatest", "v1.2.3 ", "release-v1.2.3", "v-1.2.3",
	}
	for _, tag := range accepted {
		if !tagPattern.MatchString(tag) {
			t.Errorf("tag %q must be accepted by %s", tag, found)
		}
	}
	for _, tag := range rejected {
		if tagPattern.MatchString(tag) {
			t.Errorf("tag %q must be rejected by %s", tag, found)
		}
	}
}

func TestReleaseWorkflowRequiresTagToMatchSourceVersion(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	for _, want := range []string{
		`TAG_VERSION="${RELEASE_TAG#v}"`,
		`git show-ref --verify --quiet "refs/tags/$RELEASE_TAG"`,
		`go run ./cmd version --output json`,
		`SOURCE_VERSION=`,
		`[ "$TAG_VERSION" != "$SOURCE_VERSION" ]`,
		`does not match source version`,
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("combined workflow no longer enforces %q", want)
		}
	}
}

func TestReleaseWorkflowBuildsEveryDocumentedTargetWithoutCGO(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	for _, target := range []string{
		"linux/amd64", "linux/arm64",
		"darwin/amd64", "darwin/arm64",
		"windows/amd64", "windows/arm64",
	} {
		if !strings.Contains(raw, `"`+target+`"`) {
			t.Errorf("combined workflow no longer builds %s", target)
		}
	}
	for _, want := range []string{
		"CGO_ENABLED=0",
		"-trimpath",
		"sha256sum *.tar.gz *.zip install.sh install.ps1 LICENSE THIRD_PARTY_NOTICES.txt > SHA256SUMS",
		"actions/attest-build-provenance",
		"gh release create",
		`${RELEASE_TAG#v}`,
		"git rev-parse --short HEAD",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("combined workflow no longer contains %q", want)
		}
	}
}

func TestReleaseWorkflowRecoversExistingTagsWithoutMovingThem(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	for _, want := range []string{
		"workflow_dispatch:",
		"Existing release tag to rebuild without moving tag history",
		"ref: ${{ format('refs/tags/{0}', env.RELEASE_TAG) }}",
		`git show-ref --verify --quiet "refs/tags/$RELEASE_TAG"`,
		"github.event.repository.visibility == 'public'",
		"github.event.repository.visibility != 'public'",
		"Checkout current release tooling",
		".release-tooling/scripts/Generate-ThirdPartyNotices.ps1",
		"-SourceRoot",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("combined workflow no longer contains recovery control %q", want)
		}
	}
	for _, forbidden := range []string{"git tag -f", "git push --force", "git push -f"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("combined workflow can rewrite release history with %q", forbidden)
		}
	}
}

// TestReleaseLdflagsMatchTheBuildMetadataVariables keeps the injected symbol
// paths in step with internal/config, so a rename cannot silently produce
// releases that report the source default version.
func TestReleaseLdflagsMatchTheBuildMetadataVariables(t *testing.T) {
	_, raw := loadWorkflow(t, "ci.yml")
	if !strings.Contains(raw, `PKG="foundry-agent-manager/internal/config"`) {
		t.Fatal("combined workflow no longer injects metadata into foundry-agent-manager/internal/config")
	}
	for _, symbol := range []string{
		"-X ${PKG}.Version=${VERSION}",
		"-X ${PKG}.BuildCommit=${COMMIT}",
		"-X ${PKG}.BuildDate=${DATE}",
	} {
		if !strings.Contains(raw, symbol) {
			t.Fatalf("combined workflow no longer injects %q", symbol)
		}
	}
	// The variables must exist and be settable strings in the target package.
	oldVersion, oldCommit, oldDate := config.Version, config.BuildCommit, config.BuildDate
	t.Cleanup(func() { config.Version, config.BuildCommit, config.BuildDate = oldVersion, oldCommit, oldDate })
	config.Version, config.BuildCommit, config.BuildDate = "9.9.9", "deadbee", "2026-01-01T00:00:00Z"
	if got := buildMetadata(); !strings.Contains(got, "9.9.9") ||
		!strings.Contains(got, "commit=deadbee") ||
		!strings.Contains(got, "built=2026-01-01T00:00:00Z") {
		t.Fatalf("injected metadata is not surfaced by the version command: %q", got)
	}
}

// TestSourceVersionIsSemVer keeps the fallback version usable when a binary is
// built without release ldflags.
func TestSourceVersionIsSemVer(t *testing.T) {
	semver := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	if !semver.MatchString(config.Version) {
		t.Fatalf("config.Version %q is not semantic versioning", config.Version)
	}
}

func TestGoModDeclaresTheDocumentedToolchain(t *testing.T) {
	const minimumGo = "1.26"
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "go "+minimumGo) {
		t.Fatalf("go.mod no longer declares the documented Go %s toolchain:\n%s", minimumGo, data)
	}
	if !strings.Contains(string(data), "module foundry-agent-manager") {
		t.Fatalf("unexpected module path:\n%s", data)
	}
	for _, name := range []string{"README.md", "CONTRIBUTING.md", filepath.Join("docs", "faq.md")} {
		document, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(document), "Go "+minimumGo+" or later") {
			t.Errorf("%s must document Go %s or later for source builds", name, minimumGo)
		}
	}
}

func TestCodeQLWorkflowIsSafeWhilePrivate(t *testing.T) {
	document, raw := loadWorkflow(t, "codeql.yml")

	// Must gate analysis on public visibility so it succeeds in private repos.
	for _, want := range []string{
		"github.event.repository.visibility == 'public'",
		"is_public != 'true'",
		"CodeQL analysis skipped (private repository)",
		"workflow_dispatch:",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("codeql.yml missing private-safety control %q", want)
		}
	}

	// The analyze job must require security-events: write for uploading SARIF.
	jobs := document["jobs"].(map[string]interface{})
	analyze := jobs["analyze"].(map[string]interface{})
	perms := analyze["permissions"].(map[string]interface{})
	if perms["security-events"] != "write" {
		t.Fatalf("analyze job must have security-events: write, got %v", perms["security-events"])
	}

	// The skip-private job must exist for a clean workflow result while private.
	if _, ok := jobs["skip-private"]; !ok {
		t.Fatal("codeql.yml must have a skip-private job for clean results while private")
	}
}

func TestCodeQLWorkflowUsesSHAPinnedActions(t *testing.T) {
	_, raw := loadWorkflow(t, "codeql.yml")
	for _, want := range []string{
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e",
		"github/codeql-action/init@2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2",
		"github/codeql-action/autobuild@2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2",
		"github/codeql-action/analyze@2892aa5e19bbd11bc0cff5427e3b750a04d9e3c2",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("codeql.yml missing immutable SHA pin %q", want)
		}
	}
}

func TestCodeQLCoversHostedSkillsLanguages(t *testing.T) {
	document, raw := loadWorkflow(t, "codeql.yml")
	jobs := document["jobs"].(map[string]interface{})
	analyze := jobs["analyze"].(map[string]interface{})
	strategy, ok := analyze["strategy"].(map[string]interface{})
	if !ok {
		t.Fatal("CodeQL must analyze each implementation language")
	}
	matrix, ok := strategy["matrix"].(map[string]interface{})
	if !ok {
		t.Fatal("CodeQL language matrix is missing")
	}
	languages, ok := matrix["language"].([]interface{})
	if !ok || len(languages) != 3 || languages[0] != "go" || languages[1] != "python" || languages[2] != "csharp" {
		t.Fatalf("CodeQL languages = %#v, want go, python and csharp", matrix["language"])
	}
	for _, want := range []string{
		"languages: ${{ matrix.language }}",
		"matrix.language == 'csharp'",
		"examples/hosted-skills/dotnet/HostedSkills.Example.csproj",
		"actions/setup-dotnet@a98b56852c35b8e3190ac28c8c2271da59106c68",
		"--no-incremental",
	} {
		if !strings.Contains(raw, want) {
			t.Errorf("CodeQL runtime coverage missing %q", want)
		}
	}
}

func TestAllWorkflowsUseSHAPinnedActions(t *testing.T) {
	unpinned := regexp.MustCompile(`uses:\s+[a-zA-Z0-9_-]+/[a-zA-Z0-9_/-]+@v\d`)
	for _, name := range []string{"ci.yml", "codeql.yml", "live-evaluator-calibration.yml"} {
		_, raw := loadWorkflow(t, name)
		if match := unpinned.FindString(raw); match != "" {
			t.Fatalf("%s has an unpinned action reference (tag instead of SHA): %q", name, match)
		}
	}
}
