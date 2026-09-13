# Development and Releases

Testing, CI/CD, repository layout, evaluator calibration, and release workflow.

This page is for maintainers of `fam`, not for users who only
install the executable and deploy agents. The release process provides users
with reproducible binaries, checksums, platform coverage, stable version
metadata, and evidence that the CLI behavior was qualified before publication.

## Testing

The complete gate catches failures that a package-level unit test cannot:
cross-platform compilation, the canonical `fam` executable output contract,
shipped examples, completion generation, installer syntax, negative exit
codes, and artifact checksums.

Run the complete local release gate:

```powershell
.\scripts\Test-Release.ps1
```

The gate checks formatting, vet, all Go tests, the race detector (where
supported), a host build, executable metadata, shell completions, all shipped
manifest examples, tool catalogs, negative exit-code probes, `git diff --check`,
cross-compilations, and SHA-256 checksums.

```powershell
go test -count=1 ./...
go vet ./...
gofmt -l .                   # must print nothing
```

### Fuzzing

Eight seeded fuzz targets guard host pinning, path containment, and approval
parsing. Run one target at a time:

```powershell
go test ./internal/netcheck -run=Fuzz -fuzz=FuzzValidateHTTPSHostAcceptsOnlyAllowedHosts -fuzztime=30s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzHostApprovalNeverOverMatches            -fuzztime=30s
```

### Race detector

The Go race detector is unavailable on `windows/arm64`. CI runs it on
`ubuntu-latest` as a required step.

## CI and releases

CI protects changes before merge; the release workflow turns an approved tag
into the six downloadable platform archives and checksum metadata consumed by
the installers. Keeping those stages separate prevents an unreviewed source
change from becoming a published binary.

[`../.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs on pushes and
PRs to `main`: `gofmt`, `go vet`, tests, tests with `-race`, build, and
executable qualification probes. It also runs the PowerShell live-release gate
classification regressions, including check-only self-update enforcement.

The `update-native` job also runs the self-updater tests on Windows and macOS,
including replacement of a disposable running executable. Linux coverage is
part of `ci`. These tests use local fixtures, not a real release installation.

The `release` job in
[`../.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs only after the
same tagged source passes `ci` and `update-native`. Historical rebuilds from
before the updater existed skip its native tests. It cross-compiles six CGO-free targets,
packages only the `fam` executable,
generates `SHA256SUMS`, conditionally attests build provenance, and creates the
GitHub release.

`fam update` consumes the same release archives and `SHA256SUMS` as the
installers. Keep the root `fam`/`fam.exe` archive entry, platform asset naming,
and exact checksum filenames compatible with the updater. An archive hash is
verified before extraction; provenance attestation verification is not part
of the self-update command.

The current application version is **0.17.1**
([`../internal/config/config.go`](../internal/config/config.go)).

## Weekly Foundry capability review

[`../.github/workflows/weekly-foundry-capability-review.md`](../.github/workflows/weekly-foundry-capability-review.md)
defines a GitHub Agentic Workflow that runs every Wednesday at 10:00 AM
`America/New_York` and can also be started manually. Its compiled, executable
workflow is
[`../.github/workflows/weekly-foundry-capability-review.lock.yml`](../.github/workflows/weekly-foundry-capability-review.lock.yml).

The agent job has read-only repository permissions. It compares FAM with
allowlisted Microsoft Learn pages and Microsoft-owned REST, SDK, and sample
repositories. It may edit only FAM implementation, tests, schemas, examples,
and documentation. Only the generated `safe_outputs` job can push a branch and
open one draft pull request against `main`; it cannot merge or publish a
release. Runs without a validated high-confidence change do not create an empty
pull request.

Before inference, trusted setup installs Go from `go.mod`, downloads and verifies
the module dependencies, and compiles the packages and tests with downloads
disabled. The sandbox inherits the selected toolchain and explicitly mounts
`GOMODCACHE` read-only and `GOCACHE` read-write at their prepared host paths.
Passing their environment variables alone does not expose those directories
inside the container. Custom mounts use Actions `env` expressions because the
compiler quotes plain shell-variable references literally. These mounts expose
only the two Go caches, not the runner's entire home directory.
Automatic toolchain switching, module downloads,
and module-file updates remain disabled during inference. Setup failures stop
the agent before it spends inference credits. The agent also checks compilation
inside the sandbox before researching sources or making changes.

Repository inspection uses `view` for files and line ranges, `ls` for
directories, and `git grep` or `grep` for searches; `head` and `tail` may limit
inspection output. The prompt explicitly rules out unapproved `find` and `sed`
calls instead of adding broader shell grants. Readiness and validation commands
must retain their real exit status, without output-filtering pipelines.

The Copilot SDK driver aborts on the first permission denial, with inference
retries disabled and the existing 1,000-AI-credit limit. The agent must not retry,
simplify the command, switch tools, or attempt another reporting call after
denial; the runtime records that failure. Unavailable prerequisites or sources
and failed validation also require stopping, not runner repair or alternate
package mirrors. These controls do not broaden the tool or network allowlists.

Completion is recorded through the existing `safeoutputs` CLI, not a bare native
tool call or final assistant text. A finished review with no PR uses
`safeoutputs noop --message "COMPLETE: ..."`; an incomplete review uses
`BLOCKED:` with the actual failure. A validated PR uses
`safeoutputs create_pull_request . < /tmp/gh-aw/foundry-review-output.json`,
with the JSON payload written by an editing tool, without heredocs or extra
shell helpers.

A trusted inline post-step checks `/tmp/gh-aw/agent_output.json` after ingestion
and before both agent-artifact uploads. It accepts a nonblank `COMPLETE:` no-op
or a PR declaration with title, body, and an allowed automation branch; a PR
number or URL is not required before publication. Missing, malformed, blocked,
diagnostic-only, duplicate, or ingestion-error output fails the agent job.
The input is limited to 1 MiB, and errors never print raw payloads. The gate
does not execute agent-modifiable worktree code, rewrite outputs, or discard
patches. It skips after an earlier failure so the original diagnostic stays
primary. This verifies the completion protocol, not the truth of the agent's
research. QA executes the inline validator with Node, which is required in CI.

The detection job runs only when the agent produced safe outputs or a patch.
This job-level guard avoids the gh-aw v0.88.4 skipped-detection conclusion bug
that reports a missing detector even though installation was intentionally
skipped. Real outputs and patches still go through the existing threat detector;
safe-output publication still requires successful detection. Agent failures
remain visible in the run and the framework's failure reporting.

The repository requires these Actions secrets:

| Secret | Purpose | Minimum scope |
|---|---|---|
| `COPILOT_GITHUB_TOKEN` | Copilot inference | Fine-grained PAT with account-level **Copilot Requests: Read** |
| `GH_AW_CI_TRIGGER_TOKEN` | Trigger normal CI for the generated PR | Fine-grained PAT limited to this repository with **Contents: Read and write** |

The repository Actions settings must also allow GitHub Actions to create pull
requests.

### Pinned gh-aw fork

All gh-aw workflows currently use
[`jpmicrosoft/gh-aw` at `f8cd109d60`](https://github.com/jpmicrosoft/gh-aw/commit/f8cd109d6040cc4feda3e6ee9c4d94f42ddd859e).
This fixes scoped Git command permissions and bounded Copilot SDK shutdown
after repeated denials. A separate compatibility change retains protection of
`CHANGELOG.md` by basename, so nested changelog edits still block publication.
The fork changes preserve the workflow's command/network allowlists and
publication/recovery policy. The workflow-specific first-denial and completion
checks described above do not require another fork change.

The compiler and setup runtime must come from the same commit. The fix is on
the fork's `main`, but compilation pins the full commit SHA rather than a moving
branch. The installed upstream `gh aw` extension is not used for regeneration.
The fork compiler requires Go 1.26.7 or later.

From the FAM checkout, set `$ghAwSource` to a separate, clean checkout of the
fork at the pinned commit. The example uses the sibling checkout created during
setup. Build the compiler with its source revision recorded, then regenerate
**all** gh-aw workflows:

```powershell
$ghAwSource = '..\gh-aw'
$forkCommit = 'f8cd109d6040cc4feda3e6ee9c4d94f42ddd859e'
if ((git -C $ghAwSource rev-parse HEAD) -ne $forkCommit) {
    throw "Check out gh-aw commit $forkCommit before compiling."
}
if (git -C $ghAwSource status --porcelain) {
    throw 'Build the compiler from a clean fork checkout.'
}
go -C $ghAwSource build -ldflags "-X main.version=$forkCommit" -o gh-aw.exe .\cmd\gh-aw
if ($LASTEXITCODE -ne 0) { throw 'Failed to build the pinned gh-aw compiler.' }

& "$ghAwSource\gh-aw.exe" compile --strict --approve --validate --no-check-update `
    --action-mode action --actions-repo jpmicrosoft/gh-aw/actions --action-tag $forkCommit
if ($LASTEXITCODE -ne 0) { throw 'Agentic workflow compilation failed.' }
```

The `/actions` suffix is required by the source fork's directory layout. The
generated setup references must resolve to
`jpmicrosoft/gh-aw/actions/setup@f8cd109d6040cc4feda3e6ee9c4d94f42ddd859e`.

This compiler emits trailing spaces in its banner comments. Normalize only
top-level comment whitespace and line endings after generation; do not edit
the generated workflow's behavior by hand:

```powershell
Get-ChildItem .github\workflows\*.lock.yml | ForEach-Object {
    $text = [IO.File]::ReadAllText($_.FullName)
    $text = [regex]::Replace($text, '(?m)^(#[^\r\n]*?)[ \t]+(?=\r?$)', '$1')
    [IO.File]::WriteAllText(
        $_.FullName, $text.Replace("`r`n", "`n"), [Text.UTF8Encoding]::new($false)
    )
}
```

Commit the generated `.lock.yml` and `.github/aw/actions-lock.json` together.
Dependabot ignores this runtime because independent pin updates can select
scripts that do not match the compiler. Ignore both the repository name,
`jpmicrosoft/gh-aw`, and subdirectory actions, `jpmicrosoft/gh-aw/actions/*`.
Dependabot's parser can name SHA-pinned subdirectory actions by their full
action path, so the repository-only rule is insufficient coverage.
The workflow contract test requires both ignore rules and keeps the compiler
metadata, every setup reference, and the action lock on the fixed fork revision.
It also enforces the unchanged changelog protection and patch-exclusion policy
in both generated configuration copies.

To adopt a newer fork commit or return to an upstream release, regenerate with
the matching compiler and update the runtime contract and Dependabot ignore
rules in the same change. Do not replace only the setup SHA.

## Repository layout

```text
cmd/                            CLI commands, preflight, deploy transaction, trust wiring
internal/agentdiff/             Canonical remote drift comparison
internal/arm/                   Cloud-aware ARM URL construction
internal/azcloud/               AzureCloud profile and unsupported-cloud rejection boundary
internal/botservice/            Azure Bot Service and Teams channel ensure
internal/cliout/                Text, JSON, YAML, and error-envelope output
internal/config/                Manifest loading, validation, resolution, version metadata
internal/connection/            Generic and APIM-specific project connection lifecycle
internal/errors/                Typed error kinds and stable exit-code mapping
internal/foundry/               Foundry prompt-agent, Toolbox, Memory, and Skills REST clients
internal/grounding/             Managed document validation, hashing, ownership metadata
internal/hosted/                Hosted Agent azure.yaml validation, azd orchestration, scaffold
internal/hostedautopilot/       Experimental Autopilot sample wrapper
internal/httpx/                 Safe bounded retries and request diagnostics
internal/legacyapp/             Legacy Agent Application ARM client
internal/m365publish/           Microsoft 365 publish request client
internal/memory/                Preview Memory manifest parsing
internal/netcheck/              URL host pinning and rooted file containment
internal/project/               Foundry project control-plane operations
internal/publication/           Microsoft 365 publication config schema and loader
internal/receipt/               Atomic, redacted deployment receipts (v1 and v2)
internal/redact/                Central credential redaction
internal/secret/                APIM secret source resolution
internal/tools/                 Direct-tool and Toolbox translation and destination extraction
internal/trust/                 Operator destination approvals (exact, fail-closed)
schema/                         Canonical embedded manifest and publication JSON Schemas
examples/                       Standalone example manifests and referenced files
qa/                             Live release qualification matrix templates
scripts/                        Offline and live release qualification runners
.github/workflows/              CI and release automation
docs/                           Detailed reference documentation
docs/ci-templates/              Inert GitHub Actions workflow templates
```

## Evaluator calibration and agent acceptance

Evaluator calibration is **release tooling**, not a supported CLI command. It
requires Python 3.12, pinned SDK dependencies, and creates billed Foundry runs.

```powershell
python -m pip install -r qa\evaluator-calibration\requirements.txt
.\scripts\Invoke-LiveEvaluatorCalibration.ps1 -ProjectEndpoint "https://..." -Model "gpt-5-mini"
.\scripts\Invoke-LiveAgentAcceptance.ps1 -Manifest "path\to\agent.yaml" -ProjectEndpoint "https://..." -Model "gpt-5-mini"
```

## Live release qualification

```powershell
Copy-Item qa\live-release.example.json qa\live-release.local.json
.\scripts\Invoke-LiveRelease.ps1 -Config qa\live-release.local.json -RunOnline -AllowMutations -RequireAllCommands
```

| Gate | Required switch | Operations |
|---|---|---|
| `offline` | none | Local validation, planning |
| `online-read` | `-RunOnline` | Inspection, diagnostics, dry runs |
| `mutation` | `-RunOnline -AllowMutations` | Deployment, reversible changes |
| `destructive` | `-RunOnline -AllowMutations -AllowDestructive` | Real deletion in disposable resources |

The matrix permits only `update --check` (`online-read`); applying a self-update
is rejected even with mutation authorization, so qualification cannot replace
the executable under test. The example matrix excludes this command in favor
of native disposable-executable tests; replace that exclusion with a check-only
scenario if release availability is part of your acceptance criteria.

## Release workflow

1. Land changes on `main` with CI green.
2. Update `Version` in `internal/config/config.go`.
3. Move `CHANGELOG.md` `Unreleased` to the new version heading.
4. Push tag `vX.Y.Z`. The workflow rejects mismatched tags.
5. Workflow cross-compiles, checksums, and publishes a GitHub release.
