# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project aims to follow [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

> **Version status.** `0.15.1` is the version compiled into the executable
> ([`internal/config/config.go`](internal/config/config.go)) and reported by
> `fam version`. Release archives plus a GitHub Release are
> produced only after the matching `v0.15.1` tag is pushed; see
> [`.github/workflows/ci.yml`](.github/workflows/ci.yml).

## [Unreleased]

## [0.15.1] - 2026-08-31

### Fixed

- Prompt and Hosted preflight now discover account-level RAI policies through
  the ARM collection endpoint. This correctly recognizes system-managed
  policies such as `Microsoft.DefaultV2`, which ARM lists but returns `404` for
  when requested through an individual policy URL.

## [0.15.0] - 2026-08-27

### Changed

- **Breaking:** Starting with `0.15.0`, release archives and installers
  provide only the `fam` executable (`fam.exe` on Windows). Scripts invoking
  `foundry-agent-manager` must change those invocations to `fam`. The product
  remains named Foundry Agent Manager. The installers remove the retired
  executable from the selected install directory during an upgrade and remain
  able to install releases that use the historical archive filenames.

## [0.14.1] - 2026-08-18

### Added

- Public contribution files now include a code of conduct, support policy,
  issue forms, pull request template, and repository ownership rules.
- Dependabot and CodeQL workflows provide automated dependency and code
  security coverage.
- Release archives now include the project license and generated third-party
  dependency notices.

### Changed

- Public-facing documentation now identifies FAM as an independent project and
  directs vulnerability reports to GitHub's private reporting workflow.
- GitHub Actions dependencies are pinned to immutable commits.
- Go runtime dependencies were refreshed to their qualified patch and minor
  releases.

### Fixed

- Agent drift comparison no longer adds untrusted map lengths when allocating
  its key set, eliminating a theoretical integer-overflow allocation path.

## [0.14.0] - 2026-08-18

### Added

- Long-running Hosted `azd` phases now show delayed elapsed-time progress on
  stderr. Interactive terminals use a spinner, redirected text uses periodic
  heartbeat lines, and `--progress auto|plain|off` controls the behavior.
- Hosted quickstart, init, and source adoption now attach the built-in
  `Microsoft.DefaultV2` guardrail by default. `--guardrail-policy-id` selects a
  same-account custom RAI policy, while explicit `--no-guardrail` omits the
  agent-level policy.
- Prompt quickstart and init accept optional `--guardrail-policy-id`; omission
  preserves the model deployment's guardrail. Prompt and Hosted preflight now
  verify configured policies through ARM before deployment.

### Fixed

- Foundry project resource ID validation no longer calls the value an ARM ID.
  Supplying the parent account resource ID now explains that
  `/projects/<project>` must be appended.
- Hosted preflight, deployment, and draft deployment now fail closed for
  policy-less workspaces unless the operator explicitly passes
  `--no-guardrail`; mutating receipts record that opt-out.
- Hosted draft deployment now verifies the configured same-account RAI policy,
  sends it as `rai_config.rai_policy_name`, and verifies the created draft
  retained the requested policy.
- Hosted policy validation now binds `AZURE_AI_PROJECT_ID` to the resolved
  Foundry project endpoint even when the policy is explicitly omitted, and
  repeats the binding and policy checks after optional provisioning.

## [0.13.1] - 2026-08-17

### Added

- `hosted adopt --source <existing-folder>` converts existing Python agent code
  into a validated Foundry Hosted Agent workspace. Copy mode is non-destructive;
  explicit `--in-place` mode writes the workspace contract into the source
  folder with rollback on validation failure.
- Hosted quickstart can select existing Python source interactively or through
  `quickstart --type hosted --source <existing-folder>`, using the same
  entry-point detection, dependency validation, environment bootstrap, and
  next-step generation as `hosted adopt`.

## [0.12.1] - 2026-08-17

### Fixed

- Hosted quickstart and `hosted environment create` again require and persist
  the Azure location as `AZURE_LOCATION`, which azd requires even when deploying
  to an existing Foundry project.
- Hosted preflight and deploy now reject a missing `AZURE_LOCATION` before azd
  doctor or deployment, replacing the generic deployment failure with direct
  remediation.

### Changed

- User-facing prompts, help, diagnostics, examples, and documentation now call
  Foundry project and account identifiers resource IDs instead of ARM IDs.

## [0.12.0] - 2026-08-17

### Breaking Changes

- **Canonical project resource ID.** `project.resource_id` (full Azure resource ID) is
  now the single required project identity field. The old separate fields
  `project.name`, `project.account_endpoint`, `project.subscription_id`,
  `project.resource_group`, and `project.account_name` are removed from the
  schema and no longer accepted.
- **CLI flags removed.** `--project`, `--account-endpoint`, `--resource-group`,
  and `--account-name` override flags are replaced by `--project-resource-id`.
- **Agent 365 account flags.** `--subscription-id`, `--resource-group`, and
  `--account-name` on `agent365` subcommands are replaced by a single
  `--account-id` flag accepting the full Foundry account resource ID.
- **Hosted quickstart.** Now requires only the project resource ID and model
  deployment; derives endpoint and subscription automatically.
- **Prompt quickstart / init.** Asks for the project resource ID instead of separate
  project name, account endpoint, subscription, resource group, and account name.
- **Endpoint overrides removed.** `--account-endpoint` is no longer accepted.
  All endpoints are derived from the project resource ID.

### Added

- Centralized `internal/foundryid` package for parsing and validating Foundry
  project and account resource IDs with comprehensive rejection of malformed input.
- All project coordinates (subscription, resource group, account, project name,
  endpoints) are now derived locally from the parsed resource ID without network calls.
- Release archives and installers now provide `fam` as an equivalent shorthand
  executable for `foundry-agent-manager`, including alias-specific help and
  shell completion generation.
- The root-level `-version` spelling is accepted as an exact compatibility
  alias for `--version`.

## [0.11.2] - 2026-08-17

### Added

- Hosted quickstart and `hosted environment create` can now configure
  `AZURE_AI_PROJECT_ID` and `AZURE_TENANT_ID`. The project resource ID enables azd's
  project RBAC diagnostics; tenant configuration records workspace context but
  does not replace `azd auth login` in the target tenant.

### Fixed

- Hosted `preflight` and `deploy` now run azd's own read-only Agent diagnostics
  with the deployment identity before mutation, so wrong-tenant or insufficient
  Foundry RBAC failures return actionable authorization guidance instead of a
  false-positive preflight followed by a generic deployment failure. The
  expected "agents have not been deployed" result is accepted before the first
  deployment when it is azd doctor's only failed check.
- Hosted deploy captures bounded azd stdout and stderr for fixed diagnostic
  classification without persisting subprocess output in receipts.

## [0.11.1] - 2026-08-17

### Fixed

- Hosted quickstart and `hosted environment create` now configure azd's
  canonical `FOUNDRY_PROJECT_ENDPOINT` value as well as the
  `AZURE_AI_PROJECT_ENDPOINT` compatibility alias. Hosted lifecycle commands
  prefer the canonical value and fall back to the alias for existing
  environments.
- Hosted deployment now recognizes azd's missing
  `FOUNDRY_PROJECT_ENDPOINT` diagnostic and reports endpoint remediation
  instead of only the secondary "not deployed" reconciliation result.

### Changed

- Corrected the README, FAQ, Hosted guide, command reference, start page, and
  executable help so Hosted quickstart's default local azd bootstrap and
  `hosted environment create` endpoint/model configuration are described
  consistently.

## [0.11.0] - 2026-08-14

### Added

- Interactive Hosted `quickstart` now defaults to creating or reusing the
  workspace-scoped azd environment and configuring the supplied Foundry project
  endpoint, model deployment, subscription, and location. Non-interactive
  quickstart preserves scaffold-only behavior unless
  `--bootstrap-environment` is explicitly supplied.
- `hosted environment create` can configure `AZURE_AI_PROJECT_ENDPOINT`,
  `AZURE_AI_MODEL_DEPLOYMENT_NAME`, `AZURE_SUBSCRIPTION_ID`, and
  `AZURE_LOCATION` while creating or reusing the selected environment.

## [0.10.0] - 2026-08-14

### Added

- `hosted environment create` idempotently creates a missing local azd
  environment, accepts optional subscription/location defaults, and verifies
  the result before `hosted preflight`.

### Changed

- `agent365 blueprint list` default text output now prints every returned
  blueprint's friendly display name, application ID, and directory object ID
  instead of showing only the aggregate count.
- Revalidated the complete RBAC and separation-of-duties guide against current
  Azure built-in role definitions, service-specific Hosted and Microsoft 365
  permission references, and the CLI's actual ARM/data-plane calls. Corrections
  include Project Manager child-project creation, Hosted deployment plus ACR
  roles, Microsoft 365 publishing roles, project connection management, model
  deployment roles, Agent Application invocation, Agent 365 integration, and
  conditional role-assignment boundaries.

## [0.9.0] - 2026-08-14

### Added

- A comprehensive RBAC and separation-of-duties guide with current Foundry role
  IDs, operator personas, authorization planes, per-tool permission matrices,
  runtime identity guidance, Agent 365 Graph/Entra boundaries, and independent
  Log Analytics receipt publishing. Prompt, Hosted, tools, Agent 365, security,
  FAQ, command-reference, and onboarding documentation now link to the same
  least-privilege operating model.
- A separate `agent365` command namespace for documented Microsoft Graph v1.0
  blueprint inspection: `info`, blueprint `list`, `show`, `permissions`, and
  `validate`, plus read-only Foundry `binding status` and non-mutating
  `binding plan` for Prompt and Hosted targets. The commands distinguish
  blueprint application IDs from directory object IDs, preserve all documented
  inheritable-permission modes, and state explicitly that an Agent ID blueprint
  is an identity template rather than deployable agent source.
- `agent365 blueprint list --all` follows bounded Graph pagination for up to
  5,000 blueprints visible to the current identity.
- `agent365 blueprint permissions --resolve-names` resolves permission GUIDs to
  friendly display names (requires `Application.Read.All`).
- `agent365 blueprint owners` lists directory objects that own the blueprint
  registration.
- `agent365 blueprint sponsors` lists directory objects returned by the
  blueprint sponsors relationship (requires `Application.Read.All`).
- `agent365 blueprint identities` lists Agent ID identities created from a
  blueprint.
- `agent365 identity list` and `agent365 identity show` for inspecting Agent ID
  identity objects (requires `AgentIdentity.Read.All`).
- `agent365 blueprint principal list` and `agent365 blueprint principal show`
  for inspecting blueprint-associated service principals (requires
  `AgentIdentityBlueprintPrincipal.Read.All`).
- `agent365 binding status --resolve-identity` and
  `agent365 binding plan --resolve-identity` look up the identity object
  associated with the binding through Graph.
- `agent365 integration status`, `plan`, and `set` for managing the
  `properties.a365LoggingEnabled` flag on a Foundry account through ARM API
  `2026-03-15-preview`. `set` requires `--yes`, supports `--if-match` and
  `--receipt`, mutates only the logging flag, and verifies with a read-back.
  `a365Status` (`Enabled`/`Disabled`/`NotLicensed`) is read-only. Collection
  is active only when the logging flag is `true` and status is `Enabled`.
  Scope is the entire Foundry account with no project/agent override; storage
  follows Entra tenant geography.
- `agent365 observability plan` scans bounded regular Hosted source files,
  skips `.env` and `a365.generated.config.json`, and reports evidence filenames
  for Microsoft OpenTelemetry Distro (preferred:
  `microsoft-opentelemetry`, `@microsoft/opentelemetry`,
  `Microsoft.OpenTelemetry`) or legacy Agent 365 observability SDK evidence.
- `agent365 observability status` checks the `Agent365.Observability.OtelWrite`
  app role (ID `8f71190c-00c8-461d-a63b-f74abde9ba52`) assignment on the
  deployed agent identity. Read-only; does not assign the role. Requires
  `Application.Read.All`.
- `agent365 publication info`, `plan`, `status`, and `admin-handoff` for
  read-only/plan-only publication inspection and admin handoff generation.
  These fail closed: no generic registry mutation or arbitrary existing
  blueprint binding. The Hosted executable boundary remains only the separately
  pinned autopilot sample. Registry status has no documented manager API and
  remains unverified.
- Identity lifecycle documentation: unpublished agents share the project
  identity; publication creates a distinct blueprint and identity; Azure RBAC
  does not transfer and must be reassigned. The CLI outputs
  `shared-or-distinct-unverified` when the distinction cannot be proven.
- Graph pagination follows exact `graph.microsoft.com` HTTPS v1.0 nextLinks
  with a maximum of 50 pages (5,000 results).
- `scripts/Invoke-LiveAgent365Acceptance.ps1` opt-in, read-only live harness
  with offline tests.
- Agent 365 documentation covering Graph permission and Entra role
  requirements, Foundry/runtime identity layering, Azure RBAC ownership,
  existing-ID scenarios, and the absence of a documented arbitrary-blueprint
  binding API.

### Security

- Agent 365 Graph access is GET-only, host-pinned to
  `graph.microsoft.com`, redirect-refusing, response-bounded, and restricted to
  non-secret blueprint, identity, and principal properties. Generated Agent 365
  configuration files are never parsed because they can contain blueprint
  client secrets and model API keys. Binding planning fails closed instead of
  treating metadata or ID equality as a successful mutation.
- `integration set` mutates only `properties.a365LoggingEnabled` via ARM,
  requires `--yes`, supports `--if-match` for concurrency control, and verifies
  with a read-back.
- `observability status` is read-only and does not assign app roles.
- Publication commands are read-only/plan-only with no generic registry
  mutation.
- Graph pagination validates nextLink hosts against the same
  `graph.microsoft.com` pinning as the initial request.
- Identity lifecycle explicitly warns that RBAC does not transfer after
  publication.

## [0.8.0] - 2026-08-14

### Added

- Flexible custom non-secret metadata through `agent.metadata`, Hosted
  `azure.yaml` service metadata, and repeatable global `--metadata key=value`
  overrides. Metadata is validated against Foundry's 16-entry string contract,
  participates in Prompt drift/version creation, is attached to supported
  Prompt and Hosted version requests, is copied into v1/v2 receipts, and is
  exposed as a `Metadata` dynamic column for Log Analytics queries.
- Optional automatic publication of terminal redacted v1/v2 receipts to Log
  Analytics through the Azure Monitor Logs Ingestion API. Global
  `--receipt-log-endpoint`, `--receipt-log-dcr-id`, and
  `--receipt-log-stream` options support flags or protected environment
  variables across every receipt-producing workflow. Local persistence happens
  first; ingestion failures return an error with a standalone
  `receipt upload` retry command and stable receipt-ID de-duplication guidance.
- Ready-to-deploy Log Analytics custom-table and direct-DCR examples, with
  step-by-step creation, RBAC, metadata migration, and ingestion-verification
  guidance.
- Account-scoped Foundry model deployment lifecycle commands:
  `model deployment list`, `show`, `plan`, `create`, and `delete`. Live planning
  validates the exact model/version/format, regional SKU constraints, quota,
  regional capacity, and optional RAI/spillover dependencies. Create rejects
  drift, uses create-only concurrency protection, waits for readiness, and
  writes a receipt; delete supports dry-run, confirmation, polling, and a
  receipt. Prompt deployment never creates models implicitly.
- Optional declarative `model_deployment` manifest state with exact deployment,
  catalog model, SKU, capacity, upgrade policy, RAI policy, and spillover
  fields. Equivalent command flags support one-off planning and creation.
- Commands are now organized into focused resource namespaces such as
  `prompt deploy`, `hosted session file upload`, `project connection list`,
  `toolbox deploy`, and `memory item list`. Root help and shell completion show
  the smaller hierarchical catalog, while every previous flat command remains
  available as a hidden compatibility alias for existing automation.
- A comprehensive user FAQ covering path selection, installation, Prompt
  instructions, project and model validation, immutable versions, Hosted
  prerequisites, tools, trust controls, troubleshooting, CI, and releases.
- Prompt `preflight` now performs an authenticated, read-only exact-name lookup
  for `agent.model` through the Foundry project deployments API. A missing,
  inaccessible, malformed, or unready deployment fails before agent version
  creation, and the check never invokes the model or consumes inference tokens.
  When `--ensure-project` targets a missing child project, preflight verifies
  the deployment on the parent account through ARM and deployment rechecks
  project-scoped accessibility after project creation.

### Fixed

- HTTP `401` responses now remain authentication failures (`auth`), while HTTP
  `403` responses are reported separately as `authorization` without changing
  the stable exit code `5`. Standard Azure RBAC denial messages are parsed for
  the exact action and scope, which are emitted in human-readable and
  structured remediation; wrapped errors and receipts preserve those steps.
- PowerShell and POSIX installers now honor the documented
  `FAM_INSTALL_TOKEN` environment variable before the generic GitHub token
  variables. Installation documentation now covers execution policy, blocked
  files, private-repository authentication, release discovery, missing assets,
  architecture, checksum, permissions, PATH, proxy, and cross-platform
  PowerShell failures.
- Prompt manifests now reject account endpoints that already contain
  `/api/projects/<project>` and project endpoints with duplicated or missing
  project paths. `plan`, `validate`, and online commands fail early with
  explicit guidance instead of deriving a duplicated URL and reaching a
  Foundry data-plane 404.

### Security

- Live release qualification now classifies `receipt upload` as a network
  mutation and treats grounding upload-deletion options as destructive.
  Security-sensitive boolean flags use fail-closed parsing and reject duplicate,
  conflicting, ambiguous, or unsupported values instead of allowing approval
  gates to be downgraded.

## [0.7.0] - 2026-08-13

### Added

- `help <command>` and `<command> --help` now stay focused on the selected
  command and include a concise related workflow. Bare `help` still shows the
  complete grouped catalog, while unknown help topics now return an actionable
  configuration error instead of silently printing every command.
- Shell completion now suggests focused help topics and documented flag values
  for output formats, Azure cloud, deployment type, Hosted protocols, and
  memory kinds. File and directory flags receive path-aware completion, and
  commands that accept no positional arguments suppress unrelated filenames.
- Focused help now includes copyable examples for every application command.
  Destructive commands with preview support show both `--dry-run` and
  explicitly confirmed `--yes` invocations.

## [0.6.2] - 2026-08-12

### Fixed

- Prompt quickstart now calls out its existing project/model prerequisites.
  Hosted authentication, environment-value, project-endpoint, and undeployed
  agent failures now retain state-specific recovery steps instead of falling
  through to generic tooling advice. The top-level Hosted quickstart and
  troubleshooting documentation also include the one-time environment setup.

## [0.6.1] - 2026-08-12

### Fixed

- Hosted quickstart output and missing-environment errors now show the required
  one-time `azd env new <environment> --cwd <workspace>` step instead of
  misclassifying the problem as missing Hosted tooling. `hosted-preflight`
  remains read-only and does not create environments.

## [0.6.0] - 2026-08-12

### Added

- Direct and Toolbox A2A tools now support `agent_card_path` and
  `send_credentials_for_agent_card`, including fail-closed URL validation,
  secure-default anonymous discovery, plan visibility, and exact trust review
  for absolute agent-card hosts.
- `doctor` now reports scoped local, online, and deployment readiness; collects
  independent Hosted tooling/authentication/environment failures in one run;
  performs a read-only Hosted project data-plane access probe; exposes
  category, severity, timing, observed/required values, summaries, and explicit
  unverified coverage boundaries; and supports `--fail-on-not-ready` for CI.
- Added a global `--debug` option that implies verbose output and writes
  detailed redacted command and HTTP timing diagnostics to stderr without
  exposing arguments, environment values, query strings, headers, or bodies.

### Changed

- CI and release publication now run in one workflow. The release job has an
  explicit `needs: ci` dependency, so a tag cannot build or publish until the
  exact tagged source passes formatting, vet, tests, the race detector, build,
  and executable qualification.
- Hosted doctor diagnostics are dependency-aware: an unsupported `azd` version
  or missing/unreviewed `azure.ai.agents` extension blocks extension-backed
  commands plus project endpoint and credential probes instead of executing an
  untrusted command surface.
- Publishing, Autopilot, legacy identity, connection authentication, RBAC, and
  Azure Government documentation now reflects the current verified first-party
  boundaries.

## [0.5.1] - 2026-08-11

### Changed

- Interactive `quickstart` prompts now explain what each requested value
  controls, what local files will be created, and that scaffolding does not
  contact Azure, provision resources, or deploy an agent.

## [0.5.0] - 2026-08-11

### Added

- Beginner-oriented root help groups commands by getting started, offline Prompt
  work, Prompt deployment/operations, tools/integrations, Hosted Agents, and
  experimental Autopilot. Text-mode failures now show the same actionable next
  steps already available in structured error envelopes.
- A plain-language glossary, first-success documentation path, command safety
  guidance, expected quickstart outcomes, common next steps, and concise
  troubleshooting for IT professionals at varied experience levels.
- `foundry-agent-manager quickstart` command scaffolds a Prompt manifest or
  Hosted workspace and prints the next commands to run.
- `foundry-agent-manager doctor` reports local environment readiness; optional
  `--online` flag adds Prompt/Hosted connectivity checks. Returns a diagnostic
  report with `ready` — never mutates resources.
- Verified installers: `scripts/install.ps1` (PowerShell) and
  `scripts/install.sh` (POSIX). Both support repository override (`--repo`),
  version pinning (`--version`), SHA256 checksum verification, configurable
  install directory, optional PATH modification (`--modify-profile`), and
  private-repo token via `GITHUB_TOKEN`/`GH_TOKEN`.
- Inert CI/CD GitHub Actions templates in `docs/ci-templates/` for Prompt and
  Hosted deployment workflows. Templates are not active in this repository.
- VS Code YAML and JSON schema integration (`.vscode/settings.json`) mapping
  agent and publication manifests to their JSON Schemas, and
  `.vscode/extensions.json` recommending `redhat.vscode-yaml`.
- Cobra `Examples` field on common commands for `--help` output.
- Structured error envelopes may include an optional `nextSteps` array with
  actionable remediation guidance.
- Restructured documentation: detailed content moved from README into a
  `docs/` hierarchy (`command-reference.md`, `prompt-agents.md`,
  `hosted-agents.md`, `tools-and-grounding.md`, `security-and-operations.md`,
  `development-and-releases.md`) with a `docs/README.md` index. README is now
  a concise onboarding page.

### Changed

- Reworked onboarding and capability documentation to explain when each path is
  useful, what operational outcome users gain, and which Azure, trust,
  provisioning, promotion, and governance responsibilities remain external.
- Withdrew Azure Government support pending complete live qualification in a
  dedicated Government subscription. `AzureCloud` is now the only supported
  cloud; Government aliases fail with exit `3` during cloud resolution, before
  credential acquisition or network access. Removed Government examples,
  feature-specific gates, preflight warnings, and current support claims.

## [0.4.0] - 2026-08-11

### Changed

- Documented an explicit product support matrix separating supported,
  preview-supported, experimental, plan/read-only, and maintainer-only release
  tooling. Evaluator calibration remains outside the single-binary CLI.
- Updated legacy Agent Application compatibility to ARM
  `2026-05-15-preview`, include the required application `agents` collection,
  preserve agents during routing updates, accept documented string agent
  identifiers, and reconcile the service's normalized `azureml://` references
  and omitted preview metadata.
- Updated Memory item listing to the live preview's POST body contract and
  honor `has_more`, preventing regenerated cursors from causing an unbounded
  pagination loop.
- `hosted-logs` now rejects a `--duration` greater than or equal to
  `--request-timeout` before Hosted preflight.
- Renamed the product and executable to `foundry-agent-manager` across the Go
  module, imports, CLI output, shell completion, build artifacts, GitHub
  Actions, documentation, examples, tests, comments, and errors. The GitHub
  repository remains `jpmicrosoft/fam`.
- Renamed the manifest API to `foundry-agent-manager/v1`, the publication API
  to `foundry-agent-manager/publication/v1`, receipt schemas to
  `foundry-agent-manager/receipt/v1` and `/v2`, and the local receipt directory
  to `.foundry-agent-manager`.
- Renamed all product-owned environment variables from the prior prefix to
  `FOUNDRY_AGENT_MANAGER_*`.

### Added

- Hybrid evaluator calibration with a fixed 15-case human gold set,
  deterministic requirement gates, a native Foundry `label_model` judge,
  strict logical-AND verdicts, three-run stability enforcement, and isolation
  of gold labels and human rationales from all Foundry requests.
- A separate live sample-agent acceptance runner that invokes the real stable
  endpoint for all 15 requirements and requires 15/15 with zero evaluator
  errors. The `0.4.0` release sample passed this gate.
- Pinned Python dependencies and a manually triggered, environment-scoped
  GitHub Actions workflow that authenticates with Azure workload identity and
  archives complete live evaluator calibration evidence.
- Automated release qualification: dynamic Cobra command/flag coverage,
  executable and example probes, cross-platform builds and checksums, CI/release
  gating, plus an opt-in live Azure scenario runner with separate online,
  mutation, and destructive permissions and machine-readable coverage reports.
- `project-create` for idempotent creation and reconciliation of a Foundry
  child project under an existing account, with ARM read-back, cloud-pinned
  endpoint validation, data-plane readiness polling, and a v2 operation
  receipt. `deploy --ensure-project` remains available for deployment-coupled
  creation.
- Generic project connection lifecycle through `connection-list`,
  `connection-show`, `connection-create`, `connection-update`, and
  `connection-delete`. Inspection strips credentials; mutations accept secrets
  only from explicit files/environment sources, redact errors, and write v2
  receipts.
- Preview Skill lifecycle through `skill-create`, list/show/version inspection,
  default-version promotion, zip download, and guarded deletion. Directory
  uploads preserve contained relative paths, reject symlinks, and require a
  root `SKILL.md`.
- Preview Memory lifecycle through declarative `memory_stores`, store
  validation/reconciliation, semantic search, conversation extraction with
  polling, item CRUD, scope deletion, `memory_search_preview`, and
  `x-memory-user-id` smoke invocation support.
- Structured agent inputs with complete-definition deployment, drift hashing,
  JSON Schema validation, and `--structured-inputs-file` runtime values.
- Grounding with Bing Search, Bing Custom Search, Toolbox custom search, and a
  `custom_code_interpreter` alias that emits the documented Dynamic
  Sessions-backed MCP contract. Azure Government rejects Bing, Memory, and the
  custom interpreter where availability or data boundaries are unsuitable.
- Hosted Agent Grounding with Bing Search runtime declarations and Python
  scaffolding through `hosted-init --bing-grounding-connection`; generated code
  resolves the existing project connection and attaches the documented Agent
  Framework tool.
- Preview OAuth2 managed MCP connector lifecycle: remote catalog list/show,
  gateway project-connection creation, per-principal consent links, Logic Apps
  operation discovery, complete action/schema registration, status/wait, and
  guarded deletion through `connector-*`.
- Connector-to-Toolbox automation through `connector-toolbox-deploy`, including
  immutable version comparison/creation, optional confirmed promotion, Prompt
  attachment templates, Hosted environment output, trust enforcement, and
  receipts.
- Prompt and Hosted Responses MCP approval continuation with exact
  `<server_label>/<tool_name>` allowlists, explicit unmatched rejection,
  bounded rounds, multiple approval requests, and fail-closed defaults.
- Static non-secret MCP headers, list/filter `allowed_tools`, and non-overlapping
  per-tool `require_approval` policies for direct MCP and Toolbox attachment
  contracts.
- Hosted Agent Bing Custom Search and `FoundryToolbox` runtime generation
  through `hosted-init`, including `TOOLBOX_APPROVAL_MODE=always_require`.
- Azure API Center registry metadata discovery through `api-center-list` and
  `api-center-show`, with `.azure-apicenter.ms` host pinning, anonymous access,
  or an explicit caller-provided Entra token scope.
- `logicapps-registration-plan` for non-OAuth2 connector action and parameter
  validation plus a truthful Foundry/Azure portal handoff. The command reports
  `automated: false` because Microsoft publishes no registration mutation API.
- `tool-catalog` for offline manager-supported contract discovery and
  `compatibility` for source-stamped Microsoft Learn model/region checks with
  explicit `unknown` results instead of guessed support.
- Managed document grounding through `grounding-validate`, `grounding-plan`,
  `grounding-sync`, `grounding-status`, `grounding-delete-file`, and
  `grounding-delete-store`: contained local document uploads, SHA-256
  idempotency, Foundry vector-store creation and indexing polls, explicit
  pruning/global upload deletion, ambiguous-mutation reconciliation, v2
  receipts, and logical `file_search` resolution for prompt agents and
  Toolboxes.
- `examples/agent.grounding.example.yaml` and
  `examples/knowledge/product-guide.md`; the full example now demonstrates a
  manager-owned logical File Search source.
- Reusable Foundry Toolbox lifecycle management (AzureCloud only):
  `toolbox-validate`, `toolbox-plan`, `toolbox-deploy`, `toolbox-status`,
  `toolbox-versions`, `toolbox-promote`, and `toolbox-delete-version`.
  Versions are immutable, the first version becomes default, later versions
  remain staged, promotion is explicit, and the default version cannot be
  deleted.
- Managed Toolbox definitions in the agent manifest, including MCP, Web Search,
  Azure AI Search, Code Interpreter, File Search, OpenAPI, A2A, Browser
  Automation, Fabric IQ, Work IQ, Tool Search, Reminder, same-project skill
  references, RAI policy references, preview headers, exact destination
  approvals, `--if-changed`, and v2 operation receipts.
- Prompt-agent attachment to existing same-project Toolboxes through derived
  default or immutable-version MCP endpoints and existing remote-tool project
  connections.
- Direct prompt-agent support for Web Search, Azure AI Search, A2A, Browser
  Automation, Computer Use, Fabric IQ, Work IQ, SharePoint Grounding, Image
  Generation, caller-executed Function Calling, and Toolbox attachment, with
  fail-closed preview acceptance and Government availability gates.
- Hosted Agent Toolbox runtime awareness through `TOOLBOX_NAME` or
  `TOOLBOX_ENDPOINT`, including same-project endpoint validation, agent-identity
  guidance, and explicit runtime approval obligations.
- `examples/agent.toolbox.example.yaml` and an expanded
  `examples/agent.full.example.yaml` covering the complete direct-tool catalog.
- Generic Foundry Hosted Agent deployment for existing `azure.yaml` workspaces:
  `hosted-info`, `hosted-validate`, `hosted-plan`, `hosted-preflight`,
  `hosted-status`, `hosted-disable`, `hosted-enable`, and `hosted-deploy`.
- Hosted endpoint disable/enable through the documented Foundry REST actions,
  with deployed-name resolution, narrow `azd env get-value` project-endpoint
  lookup, endpoint revalidation before authentication, no-op state detection,
  and post-mutation reconciliation for ambiguous outcomes.
- AzureCloud-only capability gating, exact `azure.ai.agents`
  `1.0.0-beta.8` pinning, `azd >=1.27.1` validation, noninteractive
  shell-free command execution, explicit provisioning, verified JSON status
  reconciliation, partial-deployment recovery, and command-level v2 receipts.
- Hosted workspace validation for contained recursive `$ref` files and sibling
  overlays, source directories, deployment modes, runtimes, protocols, reserved
  environment variables, the documented CPU/memory ranges, custom contained
  Dockerfile/context paths, and service/environment argument injection.
- Deprecated config-nested agent-definition compatibility with migration
  warnings, `invocations_ws` support, bounded reference graphs, rejection of
  azd hooks, and HTTPS host/path pinning for existing Foundry project endpoints.
- Pre-deployment version baselines prevent an unchanged, previously active
  Hosted Agent version from being mistaken for a successfully reconciled deploy.
- Online Hosted commands now enforce an operator-configurable azd deadline and
  verify that the selected azd environment already exists even when provisioning
  is explicitly requested.
- **Hosted Agent lifecycle expansion:**
  - `hosted-show` and `hosted-versions` (`--include-drafts`) for read-only
    inspection of the deployed Hosted Agent and its immutable versions.
  - `hosted-diff` for comparing the deployable workspace snapshot against the
    last verified deployment receipt and remote version state.
  - `hosted-diagnose` for surfacing tooling verification, failed versions,
    malformed routing, and draft versions in endpoint routing.
  - `hosted-smoke` for single-request invocations through `responses` or
    `invocations` (raw JSON body or contained workspace file); explicit
    WebSocket (`invocations_ws`) non-support with an actionable error.
  - `hosted-session-create`, `hosted-session-list`, `hosted-session-show`,
    `hosted-session-stop`, and `hosted-session-delete` for Hosted Agent
    session lifecycle with `--isolation-key` support and v2 receipts.
  - `hosted-session-file-upload`, `hosted-session-file-list`,
    `hosted-session-file-download`, and `hosted-session-file-delete` for
    contained session sandbox file management.
  - `hosted-promote` and `hosted-rollback` with one `FixedRatio` rule at
    100%; no traffic splitting; draft versions cannot receive endpoint
    traffic.
  - `hosted-prune`, `hosted-delete-version`, and `hosted-delete` with
    `--dry-run`, `--yes`, `--no-force`, `--include-drafts`, active-session
    termination warnings, and independent post-deletion verification.
  - `hosted-logs` for bounded SSE log streams with `--max-lines`,
    `--max-bytes`, and `--duration`; both `--agent-version` and
    `--session-id` are required.
  - `hosted-draft-deploy` for creating preview draft versions from code ZIP
    (deterministic archive, 250 MiB limit, SHA-256 `x-ms-code-zip-sha256`
    header) or prebuilt image; Docker-context mode is rejected;
    `.agentignore` supports a documented gitignore-style glob subset with
    mandatory secret/build exclusions (`.env`, `.venv`, `.azure`, `*.pyc`,
    `__pycache__/`) and rejects negation, bracket, and escape syntax;
    environment values resolved one variable at a time; subscription behavior
    may create regular versions instead of drafts.
  - `hosted-init` for scaffolding a validated Python Hosted Agent workspace
    (`azure.yaml`, `main.py`, `requirements.txt`, `.agentignore`,
    `.env.example`); fully offline, does not invoke `azd`.
  - `hosted-deploy --if-changed` skips `azd deploy` when all three conditions
    are met: successful receipt exists, deployable snapshot hash matches,
    and remote latest version matches and is active/idle.

### Fixed

- Toolbox OpenAPI definitions now use the documented nested `openapi` wire
  object and destination extraction reads that same shape.
- Toolbox validation enforces Foundry's single unnamed-tool limit across Web
  Search, Azure AI Search, Code Interpreter, and File Search.
- Hosted workspaces that omit `protocols` now report the pinned beta.8 default,
  `invocations` version `2.0.0`, instead of the obsolete `responses` default.
- Hosted preflight verifies environments with `azd env list` and no longer
  requests or handles environment values.
- Release runs no longer fail in non-public repositories when GitHub artifact
  attestations are unavailable. Public repositories still publish provenance
  attestations, and maintainers can manually recover an existing immutable tag
  after fixing the workflow on `main`.

## [0.3.0] - 2026-08-04

Changes made after the `v0.2.0` tag was cut.

### Added

- `foundry-agent-manager init`: writes a schema-valid starter manifest to a new file,
  offline, seeded from `--name`, `--model`, `--description`,
  `--instructions-file` (read once and embedded literally), `--project`,
  `--account-endpoint`, `--account-name`, `--resource-group`,
  `--subscription-id`, and `--location`. The generated manifest is validated
  against the embedded schema before it is kept. `--no-tools` omits the
  default `code_interpreter` tool; `--force` allows overwriting.
- Shell completion: Cobra's built-in `completion` command (bash, zsh, fish,
  PowerShell) is now enabled; see
  [Command Reference](docs/command-reference.md#shell-completion).
- Trust policy file: `--trust-file`/`FOUNDRY_AGENT_MANAGER_TRUST_FILE` load a
  reviewable, version-controlled JSON or YAML file of `apimHosts`,
  `toolHosts`, and/or `audiences` approvals, merged with the existing
  `--trusted-apim-host`/`--trusted-tool-host`/`--trusted-managed-identity-audience`
  flags and their environment-variable equivalents. Validated with the exact
  same rules (no wildcards, ASCII only, exact host/audience); see
  [Security and Operations](docs/security-and-operations.md#trust-policy-file).
- **Staged versions, promotion, and rollback.** `deploy` no longer moves
  production traffic by itself: creating a new immutable version now stages it
  behind whichever version is currently active (explicitly pinning the current
  latest version first if the endpoint still tracked `@latest`), and reports
  `status: staged`. The very first deployment for an agent is the one
  exception — it explicitly pins the first version it creates, since there is
  no prior active version to protect. `promote -f agent.yaml
  (--agent-version <n> | --latest)` and `rollback -f agent.yaml --agent-version
  <n> [--yes]` are the only commands that move traffic afterward, and both
  refuse to run against a stable endpoint whose selector currently splits
  traffic across more than one version. See
  [Prompt Agents](docs/prompt-agents.md#staged-versions-promotion-and-rollback).
- **Ambiguous-mutation reconciliation, never silent retries.** Every routing
  PATCH (`deploy`'s staging pin, `promote`, `rollback`, `endpoint-configure`)
  and the Microsoft 365 publish POST independently re-verify the resulting
  state after an ambiguous (unknown server-side outcome) failure instead of
  retrying automatically. `deploy` specifically keeps the stable endpoint
  pinned, rather than restoring `@latest` tracking, when a staged version's
  creation itself was ambiguous. See
  [Prompt Agents](docs/prompt-agents.md#failure-compensation-and-reconciliation).
- **`endpoint-show` / `endpoint-configure`.** Read-only inspection and
  merge-PATCH configuration of the agent's stable endpoint (`endpoint`
  manifest section: protocols, authorization schemes, agent card), which never
  changes which version is active. See
  [Prompt Agents](docs/prompt-agents.md#stable-endpoint-configuration).
- **`publish-m365`: Microsoft 365 and Teams publishing (AzureCloud only).**
  Ensures an Azure Bot Service and its Microsoft Teams channel (refusing an
  identity/tenant/endpoint change unless `--allow-bot-update`), then publishes
  the agent's stable endpoint to Microsoft 365 with `publishAsAutopilot: false`
  always. Requires a separate `foundry-agent-manager/publication/v1` file
  (`--publication`, schema at `schema/publication.schema.json`; example at
  `examples/publication.example.yaml`), an agent already pinned to exactly one
  concrete active version, and a system-assigned instance identity. `Tenant`-scoped
  publications record Microsoft 365 tenant administrator approval as a
  pending, external action in the receipt — this tool cannot perform or poll
  for that approval. The publish POST is never retried after an ambiguous
  outcome. Unavailable outside `AzureCloud`, with no commercial-cloud
  fallback. See
  [Prompt Agents](docs/prompt-agents.md#microsoft-365-and-teams-publishing).
- **`legacy-status` / `legacy-deploy` / `legacy-delete`: legacy Agent
  Application compatibility (AzureCloud only).** Explicit, compatibility-only
  operations against the older ARM-based Agent Application and
  `agentDeployment` resources, bound to an explicit `--agent-version` (never
  `@latest`). Unavailable in Azure Government, with no cross-cloud fallback.
  See
  [Prompt Agents](docs/prompt-agents.md#legacy-agent-application-compatibility).
- **`autopilot-info` / `autopilot-preflight` / `autopilot-deploy`: experimental
  Hosted-agent Autopilot (AzureCloud only).** An opt-in wrapper around exactly
  one pinned, reviewed Microsoft sample commit
  (`a2de504ff6b69149bd40d89edd1c86dc11c6af57` in
  `microsoft-foundry/foundry-samples`), requiring both `--accept-preview` and
  an exact `--approve-sample-commit` match. Verifies the checked-out `HEAD` SHA
  before running `azd provision`/`azd env get-values` in an isolated
  `--work-dir`. Rejects Azure Government outright. Hosted-agent only; there is
  no supported prompt-agent Autopilot path. Blueprint approval in the
  Microsoft 365 admin center, Teams Developer Portal configuration, and
  instance creation remain manual, external steps. See
  [Hosted Agents](docs/hosted-agents.md#experimental-hosted-agent-autopilot).
- **Receipt schema v2.** `promote`, `rollback`, `endpoint-configure`,
  `publish-m365`, `legacy-deploy`, `legacy-delete`, and `autopilot-deploy` write
  `foundry-agent-manager/receipt/v2` operation receipts (`activeVersionBefore/After`,
  `selectorBefore/After`, `resources[]` for auxiliary resources like Bot
  Service/Teams/legacy applications, `externalActions[]` for actions this tool
  cannot compensate, such as Microsoft 365 publication and tenant approval).
  `deploy` continues to write the existing `foundry-agent-manager/receipt/v1` shape.
  See [Prompt Agents](docs/prompt-agents.md#receipt-schema-versions-v1-and-v2).
- **`--allow-active-apim-update` (`deploy` only).** Explicit, off-by-default
  override to update a shared APIM connection that is already attached to an
  agent with an active version, after acknowledging that the update takes
  effect before the staged version is promoted. See
  [Prompt Agents](docs/prompt-agents.md#apim-connection-and-secret-sources).
- **Feature capability matrix.** `internal/azcloud/profile.go` now exposes
  `StableAgentEndpoints`, `M365Publishing`, `HostedAgents`, `HostedAutopilot`,
  and `LegacyApplications` per cloud; Azure Government has only
  `StableAgentEndpoints`. See
  [Security and Operations](docs/security-and-operations.md#azure-cloud-support).
- `examples/publication.example.yaml`: a complete, schema-valid Microsoft 365
  publication configuration for `publish-m365`.
- `examples/agent.full.example.yaml`: added an `endpoint` section illustrating
  protocols, authorization schemes, and an agent card.
- `examples/agent.gov.example.yaml`: added explicit Government capability
  notes (what is supported versus unavailable, with no commercial fallback)
  and a commented Government-safe `endpoint` example.

### Changed

- Internal: introduced overridable factory function variables (`newCredentialFn`,
  `newHTTPClientFn`) in `cmd/runtime.go` to enable unit-testing online commands
  (status, show, versions, diff, smoke, disable, enable, prune, delete-version,
  delete, decommission, deploy) without weakening production security validation;
  raised `cmd` package statement coverage from 51.5 % to 81.8 %.
- `SECURITY.md`: the reporting-a-vulnerability section now states plainly
  that this repository is private, that GitHub's native private
  vulnerability reporting requires GitHub Advanced Security on private
  repositories (confirmed not enabled here), and that a regular issue in this
  private repository is the primary supported reporting path today.
- `SECURITY.md`: added threat-model entries, trust-boundary treatment of the
  `--publication` file, Azure Government isolation bullets, and known/accepted
  operational risk rows for the production-safe publishing feature set above.
- `README.md`: added dedicated sections for staged versions/promotion/rollback,
  stable endpoint configuration, Microsoft 365 and Teams publishing, legacy
  Agent Application compatibility, and experimental Hosted-agent Autopilot;
  expanded the commands table, the feature capability matrix, the Azure
  Government section, the repository layout, the security controls summary,
  the limitations section, and the troubleshooting table to match.
- `CONTRIBUTING.md`: the manual example-check command now targets
  `examples\agent*.example.yaml` explicitly, with a note that
  `publication.example.yaml` uses a different schema and is intentionally
  excluded from the agent-manifest glob.

### Fixed

- Autopilot preflight and pinned-commit safety failures now use stable typed
  `config`, `security`, or `tool` exit codes instead of generic internal errors.
- `publish-m365` now refuses default `@latest` routing and requires one
  explicitly pinned concrete version before any Bot Service mutation.
- `endpoint-configure` now removes remotely enabled protocols that are absent
  from the manifest instead of falsely reporting the endpoint unchanged, and
  preserves/verifies service-managed authorization isolation metadata.
- Azure Government rejects unverified Bot-Service-dependent endpoint settings,
  and Foundry clients no longer default a missing token scope to AzureCloud.
- Updated `golang.org/x/crypto` to `v0.52.0`, resolving the open Dependabot
  advisories affecting the previous transitive version.
- `cmd/cli_test.go` (`shippedExamples`), `internal/config/config_test.go`
  (`TestAllShippedExamplesValidate`), and `internal/tools/tools_test.go`
  (`TestAllShippedExamplesBuildTools`) globbed every `examples/*.yaml`/`*.yml`
  file as if it were an agent manifest. Adding `examples/publication.example.yaml`
  (a different schema, `foundry-agent-manager/publication/v1`) broke all three; each
  now matches only agent manifests (`agent*.example.yaml` / a `"agent"` name
  prefix), consistent with how the repository's other example files are named.

## [0.2.0] - 2026-08-04

Complete rewrite from the previous Python implementation to a single Go
executable, plus production hardening from a full Red Team and QA pass and a
full documentation rewrite. Both waves of work compile to the same `0.2.0`
version and are covered by the same `v0.2.0` tag.

### Added

- **Go CLI.** `cmd/` with `version`, `validate`, `plan`, `preflight`, `deploy`,
  `status`, `show`, `versions`, `diff`, `smoke`, `disable`, `enable`, `prune`,
  `delete-version`, `delete`, and `decommission`. Built with `go build`; no
  runtime language dependency.
- **Stable automation contract.** `--output text|json|yaml`, stable field names,
  exit codes `0`-`10` plus `130`, and an `{"error": {kind, message, exitCode}}`
  envelope on stderr whose `exitCode` always matches the process exit code.
- **Embedded manifest schema.** `schema/manifest.schema.json` is compiled into
  the binary; unknown properties are rejected at every level.
- **Declarative tools.** `code_interpreter`, `file_search`, `openapi`, `mcp`,
  and `azure_function`, translated to Foundry wire format offline.
- **Change-aware deployment.** Canonical drift comparison over manifest-managed
  fields only, with `--if-changed` to skip unchanged immutable-version creation
  while still reconciling the APIM connection.
- **Deployment receipts.** Atomic JSON receipts under
  `<manifest-directory>/.foundry-agent-manager/receipts/`, or `--receipt <path>`,
  recording hashes, resource identifiers, ordered steps, smoke status, and
  compensation outcomes.
- **Recovery.** Compensation removes the exact immutable agent version created by
  a failed run. Shared-resource rollback is disabled by default and recorded as
  `reconcile-*` steps for manual reconciliation; `--allow-unconditional-shared-rollback`
  (deploy only) and `--rollback-created-project` are explicit opt-ins.
  `--allow-nonrestorable-apim-update` is required when Azure will not return an
  existing API-key connection's secret.
- **Smoke tests.** `deploy --smoke-test` pins the invocation to the version that
  run selected or created; the standalone `smoke` command invokes the current
  version.
- **Optional dependencies.** `--ensure-project` creates a missing Foundry
  project; the APIM section creates a Foundry project connection to an existing
  APIM API.
- **APIM secret sources.** Mutually exclusive direct value, file, stdin, Key
  Vault, or environment variable (default `FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY`),
  with cloud-scoped Key Vault retrieval through a redirect-refusing client.
- **Azure Government support.** `AzureUSGovernment` profile covering the Entra
  authority, ARM endpoint and audience, Foundry scope, project endpoint suffix,
  APIM suffix, Key Vault suffix, Storage Queue suffix, supported Foundry regions
  (`usgovvirginia`, `usgovarizona`), portal URLs, and rejection of `openapi` and
  `mcp` tools. Cloud selection precedence is `--cloud`, `FOUNDRY_AGENT_MANAGER_CLOUD`,
  manifest `cloud`, then `AzureCloud`.
- **Destination trust approvals.** Repeatable `--trusted-apim-host`,
  `--trusted-tool-host`, and `--trusted-managed-identity-audience`, with the CI
  equivalents `FOUNDRY_AGENT_MANAGER_TRUSTED_APIM_HOSTS`,
  `FOUNDRY_AGENT_MANAGER_TRUSTED_TOOL_HOSTS`, and
  `FOUNDRY_AGENT_MANAGER_TRUSTED_MANAGED_IDENTITY_AUDIENCES`. Approvals are exact
  `host`/`host:port` or exact audiences, are enforced on `preflight` and `deploy`
  before any Azure mutation and before secret resolution, are never read from a
  manifest, and are never written to receipts.
- **Fuzz coverage.** Six seeded fuzz targets across `internal/netcheck` and
  `internal/trust` for host pinning, cross-cloud rejection, path containment, and
  approval parsing.
- **CI and release automation.** `ci.yml` runs `gofmt`, `go vet`, tests, tests
  with the race detector on Linux, and a build. `release.yml` validates the `v*`
  semantic tag, re-verifies the source, cross-compiles six CGO-free targets with
  `-trimpath` and stamped build metadata, publishes `SHA256SUMS`, and attaches
  build-provenance attestations.
- `SECURITY.md`: trust boundary and threat model, enforced controls, destination
  approval semantics, secret-handling guidance, Azure Government isolation rules,
  secure deployment guidance, known and accepted operational risks, out-of-scope
  statement, and vulnerability reporting guidance.
- `CONTRIBUTING.md`: Go prerequisites, build and required checks, fuzzing
  instructions, the `windows/arm64` race-detector limitation and its CI gate,
  documentation/schema/help/example synchronization rules, Azure Government
  overlay rules, a security-sensitive review checklist, and commit, pull request,
  and release workflows.
- `CHANGELOG.md`: this file.

### Changed

- Replaced the Python package, its `requirements.txt`, and its test runner with
  Go packages under `cmd/`, `internal/`, and `schema/`.
- Reorganized examples into standalone manifests
  (`agent.example.yaml`, `agent.base.example.yaml`, `agent.full.example.yaml`,
  `agent.apim.example.yaml`, `agent.gov.example.yaml`) with contained
  instructions and OpenAPI files.
- ARM project-endpoint selection now sorts the advertised endpoint keys before
  choosing one, so the selected endpoint, receipts, and diagnostics are
  deterministic across runs.
- Failed deployments now append `deployment receipt: <path>` to the returned
  error while preserving the original error kind and exit code.
- Offline `validate` and `plan` explicitly report
  `destinationTrust: not-evaluated`, so their success is never read as a trust
  decision.
- Preflight reports the model deployment and RAI policy references as validated
  later by Foundry instead of implying it checked them.
- Preflight warns, rather than asserts, on Azure Government APIM
  project-connection availability and on the Azure Government availability of the
  RAI preview feature header.
- `README.md` rewritten as operator-grade documentation: install and
  distribution expectations (source builds and release archives; `bin/` is an
  ignored local build artifact), the full command table, global options
  including `FOUNDRY_AGENT_MANAGER_CLOUD`, the stable exit-code table and structured
  error envelope, the `version` output contract for text/JSON/YAML, the manifest
  and tool contracts, `spec_file` failure classification, destination trust
  approval semantics and failure classification, APIM secret source ordering and
  exclusivity, preflight and deploy flow, receipts and reconciliation,
  destructive safeguards, a Government-only section separated from public-cloud
  examples, reliability behavior, security controls, testing and fuzzing, CI and
  release workflows, repository layout, limitations, and a troubleshooting table.
- Example manifest comments now show secure `preflight`/`deploy` invocations with
  the exact trust flags they require, while keeping offline `validate` and `plan`
  runnable without any approval flags. Trust policy remains outside the manifest.
- The release workflow now rejects a semantic version tag when it does not match
  the version compiled from `internal/config`.

### Security

- Manifests are treated as untrusted input; operator flags and environment
  variables are the trust boundary.
- HTTPS and cloud-pinned host suffixes are enforced for Foundry, APIM, Key Vault,
  and Storage Queue endpoints, with symmetric cross-cloud rejection and rejection
  of URL-embedded credentials.
- OpenAPI destination extraction fails closed: every effective `servers[].url`
  (document root, path items, operations, `webhooks`, `components.pathItems`)
  must be an absolute `https` URL with an approved host; missing servers,
  templated URLs, server variables, and non-local `$ref` values are rejected.
- MCP `require_approval` accepts only `always` (default) or `never`.
- Manifest-referenced files are read through a rooted directory handle, defeating
  post-validation symlink, junction, and directory swaps, and are bounded at
  8 MiB.
- Trust approvals reject wildcards, leading-dot suffixes, URLs, and non-ASCII
  values; host comparison is case-insensitive, ignores a trailing dot, treats an
  omitted port and `:443` as equivalent, and requires an exact match for any
  other port. Audience approvals reject `/.default` scope forms.
- `FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES` remains an additional suffix boundary
  and never replaces an exact `--trusted-apim-host` approval; Foundry and Key
  Vault hosts cannot be extended.
- Credential redaction is centralized in `internal/redact` and applied by the
  receipt store to the raw value and its JSON, query, and path encodings before
  any write.
- Security failures keep their kind through wrapping, so containment and
  destination failures exit `4` instead of being downgraded.
- Destructive inputs are validated locally before any Azure call, including
  `prune --keep >= 1`; machine-readable output requires `--yes` for destructive
  operations.
- ARM routing fails closed when a cloud's ARM endpoint or token scope is
  unresolved, preventing a silent fallback to another cloud.

### Fixed

- Repeatable approval flags are read verbatim instead of through pflag's CSV
  round-trip, so an empty or malformed approval is rejected with an actionable
  error instead of silently disappearing.
- A present-but-non-mapping top-level manifest section is rejected rather than
  silently replaced during override application.
- `--output` is applied before command parsing, so early flag and usage errors
  are emitted in the requested machine-readable format.
- Missing `--instructions-file` diagnostics no longer repeat the flag name.

## Earlier history

Earlier revisions of this repository contained a Python implementation of the
same concept. It was removed in `0.2.0` and is not documented here.
