package qa

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func codeQLAutomergeDocument(t *testing.T) weeklyReviewDocument {
	t.Helper()
	var document weeklyReviewDocument
	if err := yaml.Unmarshal([]byte(repositoryFile(t, ".github", "workflows", "dependabot-codeql-automerge.yml")), &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func TestDependabotCodeQLGroups(t *testing.T) {
	var document struct {
		Updates []struct {
			Ecosystem string `yaml:"package-ecosystem"`
			Limit     int    `yaml:"open-pull-requests-limit"`
			Groups    map[string]struct {
				Patterns    []string `yaml:"patterns"`
				Exclusions  []string `yaml:"exclude-patterns"`
				UpdateTypes []string `yaml:"update-types"`
			} `yaml:"groups"`
		} `yaml:"updates"`
	}
	if err := yaml.Unmarshal([]byte(repositoryFile(t, ".github", "dependabot.yml")), &document); err != nil {
		t.Fatal(err)
	}
	for _, update := range document.Updates {
		if update.Ecosystem != "github-actions" {
			continue
		}
		if update.Limit < 3 {
			t.Fatal("CodeQL patch/minor, CodeQL major and other Actions groups need independent PR slots")
		}
		codeql, major, other := update.Groups["codeql"], update.Groups["codeql-major"], update.Groups["github-actions"]
		if !reflect.DeepEqual(codeql.Patterns, []string{"github/codeql-action/*"}) ||
			!reflect.DeepEqual(codeql.UpdateTypes, []string{"minor", "patch"}) ||
			!reflect.DeepEqual(major.Patterns, codeql.Patterns) ||
			!reflect.DeepEqual(major.UpdateTypes, []string{"major"}) ||
			!reflect.DeepEqual(other.Exclusions, codeql.Patterns) {
			t.Fatal("CodeQL patch/minor updates must be isolated from major and mixed Actions updates")
		}
		return
	}
	t.Fatal("missing github-actions Dependabot configuration")
}

func TestDependabotCodeQLAutomergeWiring(t *testing.T) {
	raw := repositoryFile(t, ".github", "workflows", "dependabot-codeql-automerge.yml")
	var trigger struct {
		On map[string]struct {
			Branches []string `yaml:"branches"`
			Types    []string `yaml:"types"`
		} `yaml:"on"`
		Concurrency struct {
			Group  string `yaml:"group"`
			Cancel bool   `yaml:"cancel-in-progress"`
		} `yaml:"concurrency"`
	}
	if err := yaml.Unmarshal([]byte(raw), &trigger); err != nil {
		t.Fatal(err)
	}
	target, ok := trigger.On["pull_request_target"]
	if !ok || len(trigger.On) != 1 || !reflect.DeepEqual(target.Branches, []string{"main"}) ||
		!reflect.DeepEqual(target.Types, []string{"opened", "reopened", "synchronize", "ready_for_review"}) ||
		trigger.Concurrency.Group != "dependabot-codeql-automerge-${{ github.event.pull_request.number }}" ||
		!trigger.Concurrency.Cancel {
		t.Fatal("automation must use base-branch code and cancel stale runs when PR heads change")
	}
	document := codeQLAutomergeDocument(t)
	job, ok := document.Jobs["auto-merge"]
	if !ok || len(document.Jobs) != 1 || len(document.Permissions) != 0 || job.TimeoutMinutes != 25 ||
		!reflect.DeepEqual(job.Permissions, map[string]string{
			"actions": "read", "contents": "read", "pull-requests": "read", "checks": "read", "statuses": "read",
		}) {
		t.Fatal("auto-merge must have bounded execution and only the required job-scoped permissions")
	}
	for _, condition := range []string{
		"github.repository == 'jpmicrosoft/fam'",
		"github.event.pull_request.user.login == 'dependabot[bot]'",
		"github.event.pull_request.head.repo.full_name == github.repository",
		"!github.event.pull_request.draft",
	} {
		if !strings.Contains(job.If, condition) {
			t.Fatalf("auto-merge is missing identity/PR gate %q", condition)
		}
	}
	steps := job.Steps
	if len(steps) != 4 {
		t.Fatal("privileged automation must not add checkout, build, or candidate execution steps")
	}
	pinned := regexp.MustCompile(`@[0-9a-f]{40}$`)
	if !strings.HasPrefix(steps[0].Uses, "dependabot/fetch-metadata@") || !pinned.MatchString(steps[0].Uses) ||
		len(steps[0].With) != 0 || steps[0].ID != "metadata" || steps[0].ContinueOnError ||
		!strings.HasPrefix(steps[1].Uses, "actions/github-script@") || !pinned.MatchString(steps[1].Uses) ||
		steps[1].ID != "policy" || steps[1].ContinueOnError {
		t.Fatal("metadata verification and policy must stay pinned and fail closed, without signature bypasses")
	}
	wantEnv := map[string]string{
		"DEPENDENCY_NAMES":  "${{ steps.metadata.outputs.dependency-names }}",
		"UPDATE_TYPE":       "${{ steps.metadata.outputs.update-type }}",
		"PACKAGE_ECOSYSTEM": "${{ steps.metadata.outputs.package-ecosystem }}",
		"DEPENDENCY_GROUP":  "${{ steps.metadata.outputs.dependency-group }}",
		"GH_TOKEN":          "${{ github.token }}",
	}
	if !reflect.DeepEqual(steps[1].Env, wantEnv) {
		t.Fatal("policy must consume verified metadata as data, not interpolated script")
	}
	if steps[2].ID != "merge-token" || steps[2].If != "steps.policy.outputs.eligible == 'true'" ||
		!strings.HasPrefix(steps[2].Uses, "actions/create-github-app-token@") || !pinned.MatchString(steps[2].Uses) ||
		!reflect.DeepEqual(steps[2].With, map[string]interface{}{
			"client-id":                "${{ vars.CODEQL_AUTOMERGE_APP_CLIENT_ID }}",
			"private-key":              "${{ secrets.CODEQL_AUTOMERGE_APP_PRIVATE_KEY }}",
			"owner":                    "${{ github.repository_owner }}",
			"repositories":             "${{ github.event.repository.name }}",
			"permission-contents":      "write",
			"permission-pull-requests": "write",
			"permission-workflows":     "write",
		}) {
		t.Fatal("only eligible PRs may obtain a revocable App token scoped to this repository and merge permissions")
	}
	const mergeScript = `set -euo pipefail
merged="$(gh api --method PUT "repos/$REPOSITORY/pulls/$PR_NUMBER/merge" \
  -f sha="$HEAD_SHA" -f merge_method=squash --jq '.merged')"
if [ "$merged" != true ]; then
  echo "::error::GitHub did not merge the verified PR head."
  exit 1
fi`
	if steps[3].If != "steps.policy.outputs.eligible == 'true'" ||
		strings.TrimSpace(steps[3].Run) != mergeScript || steps[3].ContinueOnError ||
		!reflect.DeepEqual(steps[3].Env, map[string]string{
			"GH_TOKEN":   "${{ steps.merge-token.outputs.token }}",
			"REPOSITORY": "${{ github.repository }}",
			"PR_NUMBER":  "${{ github.event.pull_request.number }}",
			"HEAD_SHA":   "${{ github.event.pull_request.head.sha }}",
		}) {
		t.Fatal("only the verified head may merge: no queued authorization, auto-approval or protection bypass")
	}
	script, ok := steps[1].With["script"].(string)
	if !ok || strings.Contains(script, "${{") || strings.Contains(script, "require('./") ||
		strings.Contains(raw, "actions/checkout@") || strings.Count(raw, "secrets.") != 1 ||
		strings.Contains(steps[3].Run, "${{") {
		t.Fatal("privileged automation must not load PR code, interpolate event data, or expose the App key outside token creation")
	}
}

func TestDependabotCodeQLAutomergeBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("node is required to exercise CodeQL auto-merge in CI")
		}
		t.Skip("node is unavailable; auto-merge behavior is exercised by CI")
	}
	script := codeQLAutomergeDocument(t).Jobs["auto-merge"].Steps[1].With["script"]
	encoded, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "-e", `
const assert = require('node:assert/strict');
const AsyncFunction = Object.getPrototypeOf(async function() {}).constructor;
const run = new AsyncFunction('github', 'context', 'core', 'require', 'setTimeout', `+string(encoded)+`);
const actions = ['init', 'autobuild', 'analyze'];
const fixture = () => {
  const patch = actions.flatMap(action => [
    '-        uses: github/codeql-action/' + action + '@' + 'a'.repeat(40) + ' # v4.38.2',
    '+        uses: github/codeql-action/' + action + '@' + 'b'.repeat(40) + ' # v4.38.3'
  ]).join('\n');
  const check = (workflowName, name) => ({workflowName, name, status: 'COMPLETED', conclusion: 'SUCCESS'});
  return {
    env: {
      DEPENDENCY_NAMES: actions.map(action => 'github/codeql-action/' + action).join(','),
      UPDATE_TYPE: 'version-update:semver-patch',
      PACKAGE_ECOSYSTEM: 'github_actions',
      DEPENDENCY_GROUP: 'codeql'
    },
    private: false,
    commits: [{sha: 'verified-head', author: {login: 'dependabot[bot]'}, commit: {verification: {verified: true}}}],
    files: [{filename: '.github/workflows/codeql.yml', status: 'modified', additions: 3, deletions: 3, patch}],
    snapshots: [{
      headRefOid: 'verified-head', state: 'OPEN', isDraft: false,
      statusCheckRollup: [
        ...['ci', 'weekly-review-validation', 'hosted-skills-runtime',
            'update-native (windows-latest)', 'update-native (macos-latest)'].map(name => check('CI and Release', name)),
        ...['go', 'python', 'csharp'].map(language => check('CodeQL', 'analyze (' + language + ')'))
      ]
    }]
  };
};
const tests = [
  ['patch', 'merge', f => {}],
  ['minor', 'merge', f => {
    f.env.UPDATE_TYPE = 'version-update:semver-minor';
    f.files[0].patch = f.files[0].patch.replaceAll('v4.38.3', 'v4.39.0');
  }],
  ['mixed dependencies', 'skip', f => { f.env.DEPENDENCY_NAMES += ',actions/setup-node'; }],
  ['major metadata', 'skip', f => { f.env.UPDATE_TYPE = 'version-update:semver-major'; }],
  ['other group', 'skip', f => { f.env.DEPENDENCY_GROUP = 'github-actions'; }],
  ['major group', 'skip', f => { f.env.DEPENDENCY_GROUP = 'codeql-major'; }],
  ['other ecosystem', 'skip', f => { f.env.PACKAGE_ECOSYSTEM = 'gomod'; }],
  ['missing dependency', 'skip', f => { f.env.DEPENDENCY_NAMES = 'github/codeql-action/init'; }],
  ['duplicate dependency', 'skip', f => { f.env.DEPENDENCY_NAMES = Array(3).fill('github/codeql-action/init').join(','); }],
  ['empty metadata', 'skip', f => { f.env.UPDATE_TYPE = ''; }],
  ['unsigned commit', 'error', f => { f.commits[0].commit.verification.verified = false; }],
  ['human commit', 'error', f => { f.commits[0].author.login = 'maintainer'; }],
  ['human appended commit', 'error', f => {
    f.commits.unshift({sha: 'earlier', author: {login: 'maintainer'}, commit: {verification: {verified: true}}});
  }],
  ['missing author', 'error', f => { f.commits[0].author = null; }],
  ['no commits', 'error', f => { f.commits = []; }],
  ['stale commit list', 'error', f => { f.commits[0].sha = 'different-head'; }],
  ['extra file', 'error', f => { f.files.push({...f.files[0], filename: 'cmd/main.go'}); }],
  ['wrong file', 'error', f => { f.files[0].filename = '.github/workflows/ci.yml'; }],
  ['renamed file', 'error', f => { f.files[0].status = 'renamed'; }],
  ['missing patch', 'error', f => { delete f.files[0].patch; }],
  ['extra line', 'error', f => { f.files[0].additions++; f.files[0].patch += '\n+run: arbitrary-code'; }],
  ['arbitrary edit', 'error', f => { f.files[0].patch = f.files[0].patch.replace(/^.*$/m, '-run: arbitrary-code'); }],
  ['short pin', 'error', f => { f.files[0].patch = f.files[0].patch.replace('b'.repeat(40), 'bbbbbbb'); }],
  ['tag pin', 'error', f => { f.files[0].patch = f.files[0].patch.replace('b'.repeat(40), 'v4'); }],
  ['indentation edit', 'error', f => { f.files[0].patch = f.files[0].patch.replace('+        uses:', '+      uses:'); }],
  ['duplicate action edit', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('/analyze@', '/init@'); }],
  ['major disguised as patch', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('v4.38.3', 'v5.0.0'); }],
  ['downgrade', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('v4.38.3', 'v4.38.1'); }],
  ['same version', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('v4.38.3', 'v4.38.2'); }],
  ['same SHA', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('b'.repeat(40), 'a'.repeat(40)); }],
  ['prerelease', 'error', f => { f.files[0].patch = f.files[0].patch.replaceAll('v4.38.3', 'v4.38.3-rc1'); }],
  ['changed head', 'error', f => { f.snapshots[0].headRefOid = 'unverified-head'; }],
  ['closed PR', 'error', f => { f.snapshots[0].state = 'CLOSED'; }],
  ['draft PR', 'error', f => { f.snapshots[0].isDraft = true; }],
  ['CI failed', 'error', f => { f.snapshots[0].statusCheckRollup[0].conclusion = 'FAILURE'; }],
  ['CodeQL failed', 'error', f => { f.snapshots[0].statusCheckRollup.at(-1).conclusion = 'FAILURE'; }],
  ['CodeQL skipped', 'error', f => { f.snapshots[0].statusCheckRollup.at(-1).conclusion = 'SKIPPED'; }],
  ['CodeQL cancelled', 'error', f => { f.snapshots[0].statusCheckRollup.at(-1).conclusion = 'CANCELLED'; }],
  ['missing check', 'error', f => { f.snapshots[0].statusCheckRollup.pop(); }],
  ['wrong workflow', 'error', f => { f.snapshots[0].statusCheckRollup.at(-1).workflowName = 'Untrusted'; }],
  ['pending timeout', 'error', f => { f.snapshots[0].statusCheckRollup[0].status = 'IN_PROGRESS'; }],
  ['checks become green', 'merge', f => {
    const pending = structuredClone(f.snapshots[0]);
    pending.statusCheckRollup[0].status = 'IN_PROGRESS';
    pending.statusCheckRollup[0].conclusion = '';
    f.snapshots.unshift(pending);
  }],
  ['head changes during wait', 'error', f => {
    const pending = structuredClone(f.snapshots[0]);
    pending.statusCheckRollup[0].status = 'IN_PROGRESS';
    f.snapshots[0].headRefOid = 'unverified-head';
    f.snapshots.unshift(pending);
  }],
  ['API error', 'error', f => { f.apiError = true; }],
  ['CLI error', 'error', f => { f.cliError = true; }],
  ['private explicit skip', 'merge', f => {
    f.private = true;
    f.snapshots[0].statusCheckRollup = f.snapshots[0].statusCheckRollup.filter(c => c.workflowName !== 'CodeQL');
    f.snapshots[0].statusCheckRollup.push({workflowName: 'CodeQL', name: 'skip-private', status: 'COMPLETED', conclusion: 'SUCCESS'});
  }],
  ['public cannot use private skip', 'error', f => {
    f.snapshots[0].statusCheckRollup = f.snapshots[0].statusCheckRollup.filter(c => c.workflowName !== 'CodeQL');
    f.snapshots[0].statusCheckRollup.push({workflowName: 'CodeQL', name: 'skip-private', status: 'COMPLETED', conclusion: 'SUCCESS'});
  }]
];
(async () => {
  for (const [name, expected, edit] of tests) {
    const f = fixture();
    edit(f);
    Object.assign(process.env, f.env);
    let eligible = false, polls = 0, notices = 0, failed = false;
    const context = {repo: {owner: 'jpmicrosoft', repo: 'fam'},
      payload: {repository: {private: f.private}, pull_request: {number: 123, head: {sha: 'verified-head'}}}};
    const github = {rest: {pulls: {listFiles: 'listFiles', listCommits: 'listCommits'}}, paginate: async (method, args) => {
      assert.ok(['listFiles', 'listCommits'].includes(method));
      assert.equal(args.pull_number, 123);
      assert.equal(args.per_page, 100);
      if (f.apiError) throw new Error('fixture API failure');
      return method === 'listFiles' ? f.files : f.commits;
    }};
    const core = {
      notice: () => { notices++; }, info: () => {},
      setOutput: (key, value) => { assert.equal(key, 'eligible'); assert.equal(value, 'true'); eligible = true; }
    };
    const fakeRequire = module => {
      assert.equal(module, 'node:child_process');
      return {execFileSync: (command, args) => {
        assert.equal(command, 'gh');
        assert.deepEqual(args, ['pr', 'view', '123', '--repo', 'jpmicrosoft/fam',
          '--json', 'headRefOid,state,isDraft,statusCheckRollup']);
        if (f.cliError) throw new Error('fixture CLI failure');
        return JSON.stringify(f.snapshots[Math.min(polls++, f.snapshots.length - 1)]);
      }};
    };
    try {
      await run(github, context, core, fakeRequire, resolve => resolve());
    } catch (error) {
      if (error.code === 'ERR_ASSERTION') throw error;
      failed = true;
    }
    assert.equal(eligible, expected === 'merge', name + ': eligibility');
    assert.equal(failed, expected === 'error', name + ': explicit failure');
    assert.equal(notices > 0, expected === 'skip', name + ': policy skip notification');
    assert.ok(polls <= 40, name + ': bounded polling');
    console.log('PASS ' + name);
  }
})().catch(error => { console.error(error); process.exitCode = 1; });
`)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("CodeQL automation regressions failed: %v\n%s", err, output)
	}
	t.Log(string(output))
}
