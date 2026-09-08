package qa

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

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
