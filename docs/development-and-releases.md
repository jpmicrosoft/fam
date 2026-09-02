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
executable qualification probes.

The `release` job in
[`../.github/workflows/ci.yml`](../.github/workflows/ci.yml) runs only after the
same tagged source passes the `ci` job. It cross-compiles six CGO-free targets,
packages only the `fam` executable,
generates `SHA256SUMS`, conditionally attests build provenance, and creates the
GitHub release.

The current application version is **0.16.2**
([`../internal/config/config.go`](../internal/config/config.go)).

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

## Release workflow

1. Land changes on `main` with CI green.
2. Update `Version` in `internal/config/config.go`.
3. Move `CHANGELOG.md` `Unreleased` to the new version heading.
4. Push tag `vX.Y.Z`. The workflow rejects mismatched tags.
5. Workflow cross-compiles, checksums, and publishes a GitHub release.
