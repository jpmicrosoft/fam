package main

import (
	"os"
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
	for _, name := range []string{"ci.yml"} {
		document, _ := loadWorkflow(t, name)
		for _, script := range workflowRunScripts(t, document) {
			if match := untrusted.FindString(script); match != "" {
				t.Fatalf("%s interpolates untrusted event data (%q) into a run script", name, match)
			}
		}
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
		"needs: ci",
		"startsWith(github.ref, 'refs/tags/v')",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("combined workflow no longer requires the CI gate %q", want)
		}
	}
	jobs := document["jobs"].(map[string]interface{})
	release := jobs["release"].(map[string]interface{})
	if release["needs"] != "ci" {
		t.Fatalf("release job needs = %#v, want ci", release["needs"])
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
		"ref: ${{ env.RELEASE_TAG }}",
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
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "go 1.25") {
		t.Fatalf("go.mod no longer declares the documented Go 1.25 toolchain:\n%s", data)
	}
	if !strings.Contains(string(data), "module foundry-agent-manager") {
		t.Fatalf("unexpected module path:\n%s", data)
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
		"github/codeql-action/init@db488ddef3bf6cb639b32c2e9a7c0a7ea8271d28",
		"github/codeql-action/autobuild@db488ddef3bf6cb639b32c2e9a7c0a7ea8271d28",
		"github/codeql-action/analyze@db488ddef3bf6cb639b32c2e9a7c0a7ea8271d28",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("codeql.yml missing immutable SHA pin %q", want)
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
