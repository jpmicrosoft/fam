# Contributing

Thanks for improving `fam`. This is a security-sensitive deployment
tool: a bug can send an Azure credential or agent data to the wrong host. The
checks below are not ceremony.

- [Prerequisites](#prerequisites)
- [Get the code running](#get-the-code-running)
- [Required checks](#required-checks)
- [Fuzzing](#fuzzing)
- [Platform note: the race detector](#platform-note-the-race-detector)
- [Keep everything in sync](#keep-everything-in-sync)
- [Azure cloud support rules](#azure-cloud-support-rules)
- [Security-sensitive review checklist](#security-sensitive-review-checklist)
- [Commit and pull request workflow](#commit-and-pull-request-workflow)
- [Release workflow](#release-workflow)
- [Code style](#code-style)

## Prerequisites

### CLI-only development

Go 1.26 or later is required for CLI-only development.

| Tool | Version | Notes |
|---|---|---|
| Go | 1.26 or later | [`go.mod`](go.mod) declares `go 1.26.0`; use a supported toolchain satisfying that minimum. |
| Git | any recent | — |
| Azure subscription | **not required for offline checks** | Go and real-SDK adapter tests use local fixtures/fake transports, not live Azure. Dependency installation may download packages. |

Building the Go CLI does not require Python or .NET. Core Go tests do not
require the real Python/.NET SDK packages; Python scaffold tests use an
interpreter when available and otherwise skip. A Go-only run therefore does
not establish scaffold coverage or real-SDK adapter compatibility.
The repository's `.ps1` qualification runners also require PowerShell.

### Separate qualification toolchains

| Tool | Version | Scope |
|---|---|---|
| Python | **3.13** | Hosted Skills real-SDK gate; install the exact [`internal/skillruntime/testdata/requirements.txt`](internal/skillruntime/testdata/requirements.txt) pins in an isolated environment. |
| .NET SDK | **10.0.401** | Hosted Skills real-SDK build and publish checks using [`HostedSkills.Example.csproj`](examples/hosted-skills/dotnet/HostedSkills.Example.csproj), which compiles the embedded adapter. |
| Python | **3.12** | Separate, billable live-evaluator qualification with [`qa/evaluator-calibration/requirements.txt`](qa/evaluator-calibration/requirements.txt); use a different environment from the Skills suite. |

The Python 3.13 and .NET gates are offline runtime checks, not merely live
evaluator prerequisites. See the [separate runtime commands](#hosted-skills-real-sdk-checks).
Formatting and Go vet checks use `gofmt` and `go vet`; real-SDK tests and
[CodeQL analysis](docs/development-and-releases.md#codeql-analysis-coverage)
are distinct checks.

## Get the code running

```powershell
git clone https://github.com/jpmicrosoft/fam.git
cd fam
go build -trimpath -o bin\fam.exe .\cmd
bin\fam.exe prompt validate -f examples\agent.example.yaml
bin\fam.exe prompt plan -f examples\agent.full.example.yaml
```

`bin/` and `*.exe` are ignored by [`.gitignore`](.gitignore). Never commit a
built binary, a receipt directory (`.foundry-agent-manager/`), anything from
`.env*`, or [azd projection recovery files](docs/hosted-agents.md#azd-projection-and-recovery).

## Required checks

Run the Go/CLI release gate before opening a pull request, plus the separate
runtime checks when changing Skills adapters, dependencies, scaffolding, or
packaging. The local Go runner is **not a complete mirror** of
[`.github/workflows/ci.yml`](.github/workflows/ci.yml).

```powershell
.\scripts\Test-Release.ps1
```

This is the preferred Go/CLI release gate. It writes a machine-readable report,
checks the compiled executable and examples, and cross-compiles every release
target in addition to the source checks below. It does not run the separate
Python/.NET real-SDK qualification, all native-platform updater checks, or
CodeQL analysis.

```powershell
gofmt -l .                 # must print nothing
go vet ./...
go test -count=1 ./...
```

Scope a run while iterating:

```powershell
go test -count=1 ./internal/trust ./internal/netcheck
go test -count=1 ./cmd -run TestSpecFileContainmentFailureUsesTheSecurityExitCode
```

`-count=1` is required; it disables the test result cache so a green run means
the tests actually ran.

### Hosted Skills real-SDK checks

Run these separately from the Go gate, using the exact toolchains above:

1. Follow the repository-root PowerShell or POSIX
   [Python qualification commands](examples/hosted-skills/README.md#qualification-checks):
   create an isolated Python 3.13 environment, install the pinned requirements,
   and invoke `python_runtime_test.py` directly, not through unittest discovery.
   Its entry point rejects zero executed tests and any skipped tests.
2. Follow the [.NET build and publish commands](examples/hosted-skills/dotnet/README.md#dependencies-and-commands)
   with SDK 10.0.401. Execute the self-tests from **both** built and published
   outputs. Strict Linux qualification uses `--self-test-require-symlinks`;
   a Windows symlink-privilege skip does not satisfy that requirement.
3. Compare complete relative-file/SHA-256 inventories of built and published
   `fam_skills`, including the helper against its source template, as required
   by the [Hosted Skills runtime CI gate](docs/development-and-releases.md#hosted-skills-runtime-ci-gate).

These checks use real SDKs with fake models/transports and require no Azure
credentials. They do not qualify a live deployment, an arbitrary container,
or native Prompt consumption. Rerun after review fixes and retain results,
skips, tool versions, and the tested revision in PR/release evidence rather
than freezing passing-test counts in documentation.

The release dependencies are `ci`, `update-native`, and
`hosted-skills-runtime`. CodeQL runs in its own workflow; it is not another
release `needs` entry. See [CI and releases](docs/development-and-releases.md#ci-and-releases)
for job scopes and historical-tag exceptions. Missing runtime qualification
inputs fail current builds. The adapter steps may be omitted only for a
validated manual pre-0.18 historical rebuild whose entire
`internal/skillruntime` path is absent, not merely one missing file.

## Fuzzing

Eight seeded fuzz targets guard host pinning, path containment, and approval
parsing. `go test ./...` executes their seed corpora. Run a real fuzz session when
you touch [`internal/netcheck`](internal/netcheck) or
[`internal/trust`](internal/trust) — one target at a time:

```powershell
go test ./internal/netcheck -run=Fuzz -fuzz=FuzzValidateHTTPSHostAcceptsOnlyAllowedHosts        -fuzztime=60s
go test ./internal/netcheck -run=Fuzz -fuzz=FuzzAPIMTargetNeverCrossesCloudBoundaries           -fuzztime=60s
go test ./internal/netcheck -run=Fuzz -fuzz=FuzzRelativeFileReferenceStaysInsideTheManifestDirectory -fuzztime=60s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzHostApprovalNeverOverMatches                    -fuzztime=60s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzApprovalParsingRejectsAmbiguousValues           -fuzztime=60s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzAudienceApprovalNeverOverMatches                -fuzztime=60s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzFileHostValidationMatchesFlagHostValidation     -fuzztime=60s
go test ./internal/trust    -run=Fuzz -fuzz=FuzzFileAudienceValidationMatchesFlagAudienceValidation -fuzztime=60s
```

If fuzzing finds a failure, Go writes the input to `testdata/fuzz/<Target>/`.
**Commit that file** with the fix: it becomes a permanent regression seed.

## Platform note: the race detector

```powershell
go test -count=1 -race ./...
```

The Go race detector is **unavailable on `windows/arm64`**, so this command
cannot run on those developer machines. CI runs it on `ubuntu-latest` as a
required step, and that is the gate for race findings. If you develop on
`windows/arm64`, run the other checks locally and rely on CI for `-race`; do not
disable or skip the CI step.

## Keep everything in sync

Documentation, schema, help text, and examples are part of the contract. A change
to behavior is incomplete until all of these agree.

| If you change… | Also update… |
|---|---|
| A flag name, default, or help string | [`docs/command-reference.md`](docs/command-reference.md) tables, [`README.md`](README.md) quickstarts if applicable, and example comments |
| A manifest field | [`schema/manifest.schema.json`](schema/manifest.schema.json), its `description`, [`docs/prompt-agents.md`](docs/prompt-agents.md), and at least one example |
| An error kind or exit code | [`docs/command-reference.md`](docs/command-reference.md) exit-code table, [`docs/security-and-operations.md`](docs/security-and-operations.md) troubleshooting, and [`SECURITY.md`](SECURITY.md) if `security` |
| Trust approval semantics | [`docs/security-and-operations.md`](docs/security-and-operations.md), `SECURITY.md`, the trust fuzz targets, and approval-bearing examples |
| Cloud profile values | [`docs/security-and-operations.md`](docs/security-and-operations.md) cloud support section and `SECURITY.md` [Azure cloud boundary](SECURITY.md#azure-cloud-boundary) |
| Direct-tool translation or destination extraction | [`examples/agent.full.example.yaml`](examples/agent.full.example.yaml) and [`examples/specs/sample-openapi.json`](examples/specs/sample-openapi.json) |
| Toolbox schema, translation, REST lifecycle, or promotion semantics | [`examples/agent.toolbox.example.yaml`](examples/agent.toolbox.example.yaml), [`docs/tools-and-grounding.md`](docs/tools-and-grounding.md), Hosted Toolbox guidance, and `SECURITY.md` |
| Grounding schema, hashing, upload, indexing, pruning, or logical-name resolution | [`examples/agent.grounding.example.yaml`](examples/agent.grounding.example.yaml), [`docs/tools-and-grounding.md`](docs/tools-and-grounding.md), and `SECURITY.md` |
| A new example manifest | The [`docs/prompt-agents.md`](docs/prompt-agents.md) example table, and confirm it passes `prompt validate` and `prompt plan` |
| The cloud capability contract (`internal/azcloud/profile.go`) | [`README.md`](README.md) support table, [`docs/security-and-operations.md`](docs/security-and-operations.md), and `SECURITY.md` |
| The publication schema or `publish-m365` behavior | [`examples/publication.example.yaml`](examples/publication.example.yaml) and [`docs/prompt-agents.md`](docs/prompt-agents.md) M365 section |
| The pinned Hosted Agent azd/extension contract or `azure.yaml` validation | [`internal/hosted`](internal/hosted), `hosted-info` output, [`docs/hosted-agents.md`](docs/hosted-agents.md), and `SECURITY.md` |
| Skills adapters, SDK pins, provider lifetime, or artifact packaging | The [shared/Python example](examples/hosted-skills/README.md), [.NET example](examples/hosted-skills/dotnet/README.md), [Hosted guide](docs/hosted-agents.md#hosted-skills), and separate real-SDK gate evidence |
| The pinned Autopilot sample commit or required executables | [`internal/hostedautopilot`](internal/hostedautopilot), `autopilot-info` output, and [`docs/hosted-agents.md`](docs/hosted-agents.md) Autopilot section |
| A receipt field or v2 receipt-writing command | [`docs/prompt-agents.md`](docs/prompt-agents.md) receipt schema section |
| Anything user-visible | [`CHANGELOG.md`](CHANGELOG.md) under `Unreleased` |

Every shipped agent manifest example must pass both offline commands:

```powershell
Get-ChildItem examples\agent*.example.yaml | ForEach-Object {
  bin\fam.exe prompt validate -f $_.FullName
  bin\fam.exe prompt plan     -f $_.FullName
}
```

[`examples/publication.example.yaml`](examples/publication.example.yaml) is not
an agent manifest (it uses the `foundry-agent-manager/publication/v1` schema and is
consumed only by `publish-m365 --publication`), so it is intentionally excluded
from the glob above and from `agent*.example.yaml` in
[`cmd/cli_test.go`](cmd/cli_test.go)'s `shippedExamples`.

Rules for examples:

- Examples must **not** contain real hostnames, real subscription ids, real
  resource names, or secrets. Use `<placeholder>` or `example.com`.
- Examples must **not** embed trust policy. Destination approvals belong on the
  command line or in the environment; showing them in a comment is correct,
  putting them in a manifest field is not.
- `validate` and `plan` examples must stay runnable **without** trust flags.
  Only `preflight`/`deploy` snippets show approval flags.

## Azure cloud support rules

AzureCloud is the only supported cloud. When you touch anything cloud-aware:

1. Keep [`internal/azcloud/profile.go`](internal/azcloud/profile.go), the
   manifest schema, CLI help, README, and tests aligned on AzureCloud-only
   support.
2. Keep Azure Government aliases on the explicit rejection path so they fail
   during cloud resolution, before credentials or network access.
3. Do not add Government examples, endpoint tables, availability claims, or
   feature-specific fallback behavior.
4. Preserve defensive rejection of endpoint and audience hosts that belong to
   another Azure cloud; those checks protect AzureCloud deployments and do not
   advertise support for the rejected cloud.
5. Do not re-enable Azure Government until maintainers have a dedicated
   subscription and complete live qualification of every supported lifecycle,
   security boundary, rollback path, and cleanup operation.

## Security-sensitive review checklist

Apply this to any change under `cmd/`, `internal/netcheck`, `internal/trust`,
`internal/secret`, `internal/redact`, `internal/receipt`, `internal/tools`, or
`internal/azcloud`.

- [ ] Does any new manifest-controlled value reach a URL, file path, or token
      audience? If yes, it is validated and — for network destinations — subject
      to exact operator approval.
- [ ] Can a manifest influence a **trust decision**? It must not. Approvals come
      only from flags and environment variables.
- [ ] Does approval still happen **before** secret resolution and before any
      Azure mutation?
- [ ] Are new failures classified with the right kind? `security` (exit `4`) for
      pinning, containment, and unapproved destinations; `config` (exit `3`) for
      malformed operator input; `tool` (exit `9`) for tool construction.
- [ ] Does a wrapped error preserve its `security` kind (`errs.SecurityWrap`)
      instead of being downgraded?
- [ ] Does any new output path print a secret? Route it through
      [`internal/redact`](internal/redact) and register the credential with the
      receipt store.
- [ ] Are approvals still absent from receipts?
- [ ] Does new parsing fail **closed** on ambiguity (missing servers, templates,
      variables, remote `$ref`, unknown enum values)?
- [ ] Does a new Hosted Toolbox path clearly distinguish approval metadata from
      runtime enforcement, which belongs to application code?
- [ ] Does a Toolbox mutation preserve immutable versioning, explicit promotion,
      default-version deletion refusal, and ambiguous-outcome reconciliation?
- [ ] Are file reads still rooted and size-bounded?
- [ ] Do grounding uploads remain streamed, hash-verified, and fail closed if
      the file changes during upload?
- [ ] Does logical grounding resolution still require exactly one completed
      manager-owned store with the current desired hash?
- [ ] Is global project-file deletion explicit, confirmed, and receipt-recorded?
- [ ] Is a new destructive input validated locally before any Azure call?
- [ ] Are new host or path parsers covered by a fuzz target or added seed?
- [ ] Are both cloud profiles updated, and is cross-cloud rejection symmetric?

Changes that weaken a boundary — broadening an allow-list, adding wildcard
matching, defaulting an approval to permissive, or adding a new automatic
shared-resource rollback — need an explicit justification in the pull request
description and a corresponding update to `SECURITY.md`.

## Commit and pull request workflow

1. Branch from `main`.
2. Make focused commits. Write imperative subjects under ~72 characters
   (`Reject templated OpenAPI server URLs`), and explain **why** in the body.
3. Run the [required checks](#required-checks).
4. Update docs, schema, examples, and `CHANGELOG.md` in the **same** pull
   request as the behavior change.
5. Open a pull request against `main` describing what changed, why, the security
   impact (even if "none"), and how you verified it. Paste relevant command
   output.
6. Required CI must be green, including `ci`, `update-native`, and
   `hosted-skills-runtime`. Go checks alone do not establish a passing runtime
   gate; review the separate CodeQL analyses as well.

Please do **not** rewrite published history. Do not force-push over a branch
others are reviewing, and do not rebase or amend commits that are already on
`main`. Add follow-up commits instead; the maintainer decides how a pull request
is merged.

Do not commit: built binaries, `.foundry-agent-manager/` receipt directories, `.env`
files, azd projection recovery material, real Azure resource names, real
endpoints, or any credential. Ignore rules do not prevent force-adding or
manually archiving recovery files.

## Release workflow

Releases are tag-driven and run by the release job in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml), after the same tagged
source passes `ci`, `update-native`, and `hosted-skills-runtime`, subject to the
documented historical-tag exceptions. Contributors do not publish releases;
the maintainer does.

1. Land all changes on `main` with CI green.
2. Update `Version` in [`internal/config/config.go`](internal/config/config.go).
3. Move `CHANGELOG.md` `Unreleased` entries under the new version heading with a
   real date.
4. The maintainer pushes a tag `vX.Y.Z` matching that version. The workflow
   rejects any tag that is not `v` + semantic version or does not match the
   source `Version`.
5. The required jobs rerun their Go, native-updater, and real-SDK qualification.
   After they succeed, the release job cross-compiles six CGO-free targets
   with `-trimpath` and stamped `ldflags`, publishes archives plus `SHA256SUMS`,
   conditionally attests build provenance for public repositories, and creates
   the GitHub release.

The prepared **0.18.0 candidate for 2026-10-01 (UTC)** is **not yet tagged or
published**; the latest published release is **0.17.1**. Source version changes
and candidate documentation do not authorize or establish publication. See
[release status and gates](docs/development-and-releases.md#release-job).

Before the first public launch or a capability-expanding release, copy
[`qa/live-release.example.json`](qa/live-release.example.json), replace every
placeholder, and run [`scripts/Invoke-LiveRelease.ps1`](scripts/Invoke-LiveRelease.ps1)
against dedicated Prompt and Hosted resources. `-RequireAllCommands` and
`-RequireAllFlags` turn uncovered command or switch surfaces into release
failures; exclusions require written reasons. Real deletion requires the
separate `-AllowDestructive` gate and must target disposable qualification
resources.

Calibrate the hybrid evaluator separately, then run the actual sample agent
through the 15-case acceptance gate:

```powershell
python -m pip install -r qa\evaluator-calibration\requirements.txt
.\scripts\Invoke-LiveEvaluatorCalibration.ps1 `
  -ProjectEndpoint "https://<account>.services.ai.azure.com/api/projects/<project>"
.\scripts\Invoke-LiveAgentAcceptance.ps1 `
  -Manifest "path\to\disposable-agent.yaml" `
  -ProjectEndpoint "https://<account>.services.ai.azure.com/api/projects/<project>"
```

The evaluator must be stable across three runs, and the sample agent must
achieve 15/15 with zero errors. GitHub's manually triggered
`live-evaluator-calibration` workflow uses a protected environment and OIDC;
it is intentionally excluded from pull requests and schedules because every
run is billed.

If a release run fails because of the workflow rather than the tagged source,
fix the workflow on `main`, then use its manual `workflow_dispatch` input with
the existing tag. Never move a published release tag to pick up a workflow fix.
Manual source checkouts use the fully qualified `refs/tags/<tag>`, not an
ambiguous branch name. Shared validation checks the release-tag form, tag
existence, and `HEAD` against the tag's peeled commit before tests/builds.
CI and publication checkouts in this workflow use `persist-credentials: false`,
including the separate current-release-tooling checkout. See
[release source validation](docs/development-and-releases.md#release-source-validation).

Verify a release by running `fam version` from the archive and
confirming `version`, `commit`, and `builtAt` match the tag.

## Code style

- `gofmt` decides formatting. There is no separate style debate.
- Package names are lowercase and single-word; each package has a doc comment
  explaining its purpose and, where relevant, its security role.
- Exported identifiers carry doc comments.
- Comments explain **why**, especially for security decisions. The existing
  fail-closed comments in `internal/trust`, `internal/netcheck`, and
  `internal/tools` are the model to follow.
- Errors are lowercase, actionable, and name the offending field or flag. Prefer
  the typed constructors in [`internal/errors`](internal/errors) over
  `fmt.Errorf` at boundaries, so the exit code stays correct.
- Tests are named for the behavior they protect
  (`TestPruneRejectsInvalidRetentionBeforeAzureAccess`), not the function they
  call.
- Keep table-driven tests table-driven; add a row rather than a near-duplicate
  test.
