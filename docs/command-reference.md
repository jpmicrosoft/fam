# Command Reference

Complete command listing, global options, exit codes, and output contracts for
the current source tree (`0.16.3` is the prepared release version).

For task-oriented answers and common troubleshooting, start with the
[`FAQ`](faq.md).

Every command is invoked through the canonical `fam` executable. The exact
root flag `-version` is accepted as a compatibility spelling of `--version`.

Use this page after choosing the Prompt or Hosted path to find the command that
matches the outcome you need:

| Goal | Start with |
|---|---|
| Create a starter configuration | `quickstart`, `prompt init`, or `hosted init` |
| Prove local configuration is valid | `prompt validate`, `prompt plan`, `hosted validate`, or `hosted plan` |
| Plan or create a model deployment | `model deployment plan`, then `model deployment create` |
| Diagnose readiness without mutation | `doctor`, `prompt preflight`, or `hosted preflight` |
| Deploy only when configuration changed | `prompt deploy --if-changed` or `hosted deploy --if-changed` |
| Inspect remote state or drift | `prompt status`, `prompt show`, `prompt versions list`, `prompt diff`, or Hosted equivalents |
| Change production traffic | `prompt promote` / `prompt rollback`, or Hosted equivalents |
| Remove resources intentionally | `prompt versions prune`, `prompt versions delete`, `prompt delete`, or `prompt decommission` |
| Publish or retry an audit receipt | Configure the global `--receipt-log-*` options, or run `receipt upload` |
| Inspect an Agent 365 blueprint or compare Foundry identities | `agent365 blueprint show`, `agent365 blueprint validate`, `agent365 binding status`, or `agent365 identity list` |
| Manage Agent 365 integration logging on a Foundry account | `agent365 integration status`, `agent365 integration plan`, `agent365 integration set` |
| Check observability readiness for a Hosted workspace | `agent365 observability plan` or `agent365 observability status` |
| Plan Agent 365 publication | `agent365 publication plan`, `agent365 publication status`, or `agent365 publication admin-handoff` |

The command names separate validation, read-only inspection, mutation,
promotion, and deletion so automation can grant and review only the authority
needed for each stage. See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md)
for the operator, runtime, Graph/Entra, and audit permissions associated with
each command family.

Bare `fam help` shows the top-level resource namespaces.
Use `fam help <command path>` or
`fam <command path> --help` for focused help containing only
the selected namespace or command's subcommands, usage, examples, flags, and
related workflow. For example:

```powershell
fam help quickstart
fam prompt deploy --help
fam help hosted deploy
fam help hosted session file
fam doctor --help
```

For example, use `fam hosted deploy --help` or `fam -version`.

The previous flat names remain executable as hidden compatibility aliases.
For example, `fam hosted-deploy` still invokes
`fam hosted deploy`, but only the nested path appears in help,
documentation, and completion suggestions.

Every application command includes at least one copyable invocation. Commands
that support both `--dry-run` and `--yes` show separate preview and confirmed
execution examples.

## Commands

| Command | Azure | Purpose |
|---|---|---|
| `version` | no | Print version, commit, and build time. |
| `doctor` | no (or read-only with `--online`) | Diagnose a Prompt `--manifest` or Hosted `--workspace` without mutation. Reports scoped local/online/deployment readiness, all independently testable failures, structured check metadata, and explicit coverage gaps. `--fail-on-not-ready` provides a CI exit code after writing the report. |
| `quickstart` | no, or local mutation | Scaffold a Prompt manifest or Hosted workspace and print the next commands. `--guardrail-policy-id` selects an optional same-account custom RAI policy. Prompt omission inherits the model deployment policy. Hosted omission defaults to `Microsoft.DefaultV2`; Hosted-only `--no-guardrail` explicitly omits the agent-level policy. Interactive Hosted quickstart can adopt an existing Python source folder or generate starter code, then defaults to creating/configuring the workspace azd environment; answer no to keep it files-only. `--source` routes through the same engine as `hosted adopt`. Non-interactive use remains files-only unless `--bootstrap-environment` is passed with `--project-id`, `--model`, and `--location`. Endpoint, subscription, and the default Hosted policy ID are derived from the project resource ID; `--tenant-id` records optional cross-tenant context. Bootstrap never authenticates azd or assigns RBAC. |
| `receipt upload` | mutating external audit data | Upload one preserved manager-generated v1/v2 receipt to Azure Monitor Logs through an existing DCR. |
| `prompt init` | no | Write a schema-valid starter manifest to a new file. Optional `--guardrail-policy-id` writes `agent.rai_policy_id`; omission inherits the model deployment policy. |
| `prompt validate` | no | Validate the manifest and build every local tool payload. |
| `prompt plan` | no | Print the resolved offline deployment plan. |
| `project create` | mutating | Idempotently create or reconcile the manifest's Foundry child project, verify ARM and data-plane readiness, and write a v2 receipt. |
| `model deployment list` / `model deployment show` | read-only | Inspect account-scoped model deployments through ARM, including exact model version, format, SKU, capacity, and provisioning state. |
| `model deployment plan` | read-only | Validate the exact account and regional model catalogs, SKU capacity shape, quota, regional capacity, and optional RAI/spillover dependencies. Existing exact state returns `unchanged`; drift fails. |
| `model deployment create` | mutating, potentially billable | Explicitly create the planned deployment with create-only concurrency protection, wait for `Succeeded`, reject drift, and write a receipt. It is never called by `prompt deploy`. |
| `model deployment delete` | destructive | Preview with `--dry-run`; otherwise require confirmation, wait for ARM-confirmed absence, and write a receipt. |
| `agent365 info` | no | Explain the read-only Graph contract, identity layers, and unsupported arbitrary-blueprint binding boundary. |
| `agent365 blueprint list` | read-only (AzureCloud only) | List up to 100 Microsoft Entra Agent ID blueprints through Microsoft Graph v1.0, including each friendly display name and application/object ID, and report whether the page is truncated. Use `--all` for bounded continuation up to 5,000 results. |
| `agent365 blueprint show` | read-only (AzureCloud only) | Show selected non-secret blueprint metadata by application/client ID or directory object ID. |
| `agent365 blueprint permissions` | read-only (AzureCloud only) | Show requested resource access and all documented inheritable-permission modes. Use `--resolve-names` to resolve permission GUIDs to friendly display names (requires `Application.Read.All`). |
| `agent365 blueprint validate` | read-only (AzureCloud only) | Validate Microsoft disablement, manager applications, requested access, and inheritance configuration; `--fail-on-invalid` exits `11` after printing a blocking result. |
| `agent365 blueprint owners` | read-only (AzureCloud only) | List the directory objects that own the blueprint application registration. |
| `agent365 blueprint sponsors` | read-only (AzureCloud only) | List directory objects returned by the blueprint sponsors relationship. Requires `Application.Read.All`. |
| `agent365 blueprint identities` | read-only (AzureCloud only) | List Agent ID identities created from the selected blueprint. |
| `agent365 identity list` | read-only (AzureCloud only) | List Agent ID identity objects visible to the current identity. Requires `AgentIdentity.Read.All`. |
| `agent365 identity show` | read-only (AzureCloud only) | Show one Agent ID identity by ID. Requires `AgentIdentity.Read.All`. |
| `agent365 blueprint principal list` | read-only (AzureCloud only) | List tenant-local Agent ID blueprint principals. Requires `AgentIdentityBlueprintPrincipal.Read.All`. |
| `agent365 blueprint principal show` | read-only (AzureCloud only) | Show one blueprint principal by ID. Requires `AgentIdentityBlueprintPrincipal.Read.All`. |
| `agent365 binding status` | read-only (AzureCloud only) | Show Foundry runtime identity, blueprint identity, and blueprint reference for exactly one Prompt or Hosted target; an optional blueprint selector adds correlation. Use `--resolve-identity` to look up the associated identity object. |
| `agent365 binding plan` | plan/read-only (AzureCloud only) | Compare an existing blueprint with one Prompt or Hosted Agent and emit a non-mutating plan. No arbitrary binding API is called or implied. |
| `agent365 integration status` | read-only (AzureCloud only) | Show the Agent 365 logging flag and `a365Status` for a Foundry account. Requires `--account-id` (full Foundry account resource ID). |
| `agent365 integration plan` | plan/read-only (AzureCloud only) | Plan a change to the Agent 365 logging flag. Requires `--enabled=true` or `--enabled=false`. |
| `agent365 integration set` | mutating (AzureCloud only) | Set `properties.a365LoggingEnabled` on a Foundry account via ARM API `2026-03-15-preview` and verify with a read-back. Requires `--yes`; supports `--if-match` and `--receipt`. Does not modify `a365Status`. |
| `agent365 observability plan` | plan/read-only | Scan bounded regular Hosted source files for Microsoft OpenTelemetry Distro or legacy Agent 365 observability SDK evidence and documented config calls. `.env` and `a365.generated.config.json` are skipped; only evidence filenames are emitted. |
| `agent365 observability status` | read-only (AzureCloud only) | Check whether the deployed agent identity has the `Agent365.Observability.OtelWrite` app role (`8f71190c-00c8-461d-a63b-f74abde9ba52`) assigned. Requires `Application.Read.All`. Read-only; does not assign the role. |
| `agent365 publication info` | no | Explain the publication boundary: read-only/plan-only, no generic registry mutation. |
| `agent365 publication plan` | plan/read-only (AzureCloud only) | Plan publication for exactly one Prompt or Hosted target. Preserves modern identity/RBAC guidance and emits migration steps only for legacy identities. Does not mutate. |
| `agent365 publication status` | read-only (AzureCloud only) | Show Foundry publication and identity evidence for exactly one Prompt or Hosted target. Registry state remains unverified because no documented manager status API exists. |
| `agent365 publication admin-handoff` | plan/read-only (AzureCloud only) | Generate tenant-admin, governance, observability, and identity-appropriate RBAC handoff steps for exactly one Prompt or Hosted target. |
| `project connection list` / `project connection show` | read-only | Inspect ARM project connections; credential values are removed before output. |
| `project connection create` / `project connection update` | mutating | Create or update a project connection from non-secret flags plus a credential file/environment source; writes a v2 receipt. |
| `project connection delete` | destructive | Delete one project connection after `--yes`. |
| `connector api-center list` / `connector api-center show` | read-only | Read the documented Azure API Center MCP registry metadata endpoint. Authentication is anonymous unless an explicit `--api-center-token-scope` is supplied. |
| `connector logic-apps registration plan` | read-only, preview (AzureCloud only) | Validate a non-OAuth2 connector, selected actions, Model/User parameter sources, and the portal registration worksheet. It does not register the MCP server. |
| `connector list` / `connector show` | read-only, preview (AzureCloud only) | Browse the documented Foundry managed connector catalog and inspect OAuth type and advertised actions. |
| `connector create` | mutating, preview (AzureCloud only) | Create an OAuth2 `RemoteTool` gateway connector from an exact catalog entry. |
| `connector consent` | read-only, preview (AzureCloud only) | Mint a short-lived per-user OAuth consent URL for an existing managed connector. |
| `connector actions` | read-only, preview (AzureCloud only) | List agent-callable Logic Apps operations or inspect one operation's input schema. |
| `connector configure` | mutating, preview (AzureCloud only) | Replace the complete registered operation allowlist and parameter schemas for a managed MCP server. |
| `connector status` / `connector wait` | read-only, preview (AzureCloud only) | Inspect or wait for `Connected` status and the platform-generated MCP target. |
| `connector toolbox deploy` | mutating, preview (AzureCloud only) | Create an immutable Toolbox version from a connected managed connector, skip unchanged payloads, optionally promote, and emit Prompt/Hosted attachment configuration. |
| `connector delete` | destructive, preview (AzureCloud only) | Delete one managed connector after `--yes`. |
| `prompt preflight` | read-only | Verify credentials, project access, the exact model deployment, APIM inputs, and data-plane reachability without mutation or inference. |
| `prompt deploy` | mutating | Run preflight, then stage an immutable agent version behind the current active version (or pin the first version). |
| `prompt status` | read-only | Agent lifecycle state, latest version, active version, selector mode, and optional APIM connection status. |
| `prompt show` | read-only | Show the logical agent, or one `--agent-version`. |
| `prompt endpoint show` | read-only | Show stable-endpoint routing, instance identity, protocols, authorization, and agent card. |
| `prompt endpoint configure` | mutating | Apply manifest endpoint protocols, authorization, and agent card **without** changing which version is active. |
| `prompt versions list` | read-only | List immutable versions and provisioning status. |
| `prompt diff` | read-only | Compare manifest-managed fields with the latest remote version and APIM connection. |
| `tool-catalog` | no | List manager-supported direct, Toolbox, and Hosted runtime contracts and report that managed connector discovery is available through `connector list`. |
| `prompt compatibility` | no | Evaluate documented model/tool and region/tool combinations from the source-stamped Microsoft compatibility snapshot; uncovered combinations return `unknown`. |
| `toolbox validate` | no (AzureCloud definitions only) | Validate all managed Toolbox definitions and contained files. |
| `toolbox plan` | no (AzureCloud definitions only) | Plan immutable Toolbox version creation without Azure access. |
| `toolbox deploy` | mutating (AzureCloud only) | Create one immutable Toolbox version; later versions remain staged until promoted. |
| `toolbox status` | read-only (AzureCloud only) | Show the logical Toolbox and its promoted `default_version`. |
| `toolbox versions list` | read-only (AzureCloud only) | List immutable Toolbox versions. |
| `toolbox promote` | mutating (AzureCloud only) | Make one existing Toolbox version the consumer default. |
| `toolbox versions delete` | destructive (AzureCloud only) | Delete one non-default immutable Toolbox version. |
| `skill create` | mutating, preview (AzureCloud only) | Create an immutable Skill version from inline instructions, a directory, or a zip; optionally make it default. |
| `skill list` / `skill show` | read-only, preview (AzureCloud only) | Inspect Skills without downloading content. |
| `skill version list` / `skill version show` | read-only, preview (AzureCloud only) | Inspect immutable Skill versions. |
| `skill version set-default` | mutating, preview (AzureCloud only) | Change the logical Skill's default version. |
| `skill download` | read-only, preview (AzureCloud only) | Download the default or selected Skill version as a zip. |
| `skill delete` / `skill version delete` | destructive, preview (AzureCloud only) | Delete a Skill or one immutable version after `--yes`. |
| `grounding validate` | no | Validate document paths, formats, sizes, and hashes. |
| `grounding plan` | no | Print desired vector-store and document hashes without Azure access. |
| `grounding sync` | mutating | Create or reconcile a manager-owned vector store, upload changed documents, and wait for indexing. |
| `grounding status` | read-only | Compare local desired document hashes with remote indexing state. |
| `grounding file delete` | destructive | Detach one manager-owned document; optionally delete its project upload globally. |
| `grounding store delete` | destructive | Delete one manager-owned vector store; optionally delete its manager-owned project uploads globally. |
| `memory store validate` / `memory store plan` | no | Validate top-level preview Memory store definitions and desired hashes. |
| `memory store list` / `memory store show` | read-only, preview (AzureCloud only) | Inspect preview Memory stores. |
| `memory store sync` | mutating, preview (AzureCloud only) | Create or reconcile one manifest-managed Memory store. |
| `memory store delete` | destructive, preview (AzureCloud only) | Delete one Memory store after `--yes`. |
| `memory search` / `memory update` | preview, billable (AzureCloud only) | Search one scope or extract/consolidate memories from Responses conversation items. |
| `memory item create/list/show/update/delete` / `memory scope delete` | preview (AzureCloud only) | Create, inspect, update, or delete explicit Memory items, or delete an entire scope. |
| `prompt smoke` | mutating (billable) | Invoke the deployed prompt agent once. |
| `prompt disable` / `prompt enable` | mutating | Suspend or resume the agent endpoint. |
| `prompt promote` | mutating | Route all stable-endpoint traffic to `--agent-version`, or explicitly restore `--latest`. |
| `prompt rollback` | mutating | Route all stable-endpoint traffic back to an earlier verified `--agent-version` (rejects `--latest`). |
| `prompt m365 publish` | mutating (AzureCloud only) | Ensure an Azure Bot Service and Teams channel, then publish the stable endpoint to Microsoft 365. |
| `prompt legacy status` | read-only (AzureCloud only) | Inspect explicit legacy Agent Application compatibility resources. |
| `prompt legacy deploy` | mutating (AzureCloud only) | Ensure an explicit legacy Agent Application and Managed Responses deployment. |
| `prompt legacy delete` | destructive (AzureCloud only) | Delete explicit legacy compatibility resources. |
| `hosted info` | no | Show the verified Hosted Agent preview, tooling, cloud, mode, and protocol boundary. |
| `hosted adopt` | local mutation | Adopt an existing Python source folder as a net-new Hosted Agent workspace. The generated deployment metadata defaults to `Microsoft.DefaultV2`; use `--guardrail-policy-id` for a same-account custom policy or `--no-guardrail` for explicit opt-out. Python source is not rewritten for guardrails. Copy mode requires a new relative `--destination` and leaves `--source` untouched; explicit `--in-place` writes `azure.yaml`, merged `.agentignore`, and optional `.env.example` into the source with rollback on validation failure. Detects entry points and Python dependency metadata, excludes local secrets/caches, and can bootstrap existing-project azd context without authenticating, provisioning, or deploying. |
| `hosted validate` | no | Validate one Hosted Agent service and every contained local `$ref` in an existing `azure.yaml` workspace. |
| `hosted plan` | no | Print the exact noninteractive `azd` workflow without running a tool or authenticating. |
| `hosted environment create` | local mutation | Idempotently create or reuse the selected local azd environment and verify new environments. `--project-id` derives endpoint, subscription, and `Microsoft.DefaultV2`'s full policy ID when the workspace uses the default guardrail reference; `--model-deployment` and `--location` are required, while `--tenant-id` supplies optional cross-tenant context. The endpoint is stored as canonical `FOUNDRY_PROJECT_ENDPOINT` plus the `AZURE_AI_PROJECT_ENDPOINT` compatibility alias; the project ID enables azd's role check. Tenant configuration does not authenticate azd. |
| `hosted preflight` | read-only (AzureCloud only) | Verify the pinned `azd`/extension contract, authentication, existing environment, azd deployment identity's Foundry project reachability, and any configured RAI policy. `AZURE_AI_PROJECT_ID` must match the resolved endpoint even for `--no-guardrail`. A policy-less workspace requires explicit `--no-guardrail`; the flag is rejected when a policy is configured. Wrong-tenant, missing-policy, cross-account, and insufficient-RBAC failures stop before deployment. |
| `hosted status` | read-only (AzureCloud only) | Reconcile one deployed Hosted Agent version through verified JSON output. |
| `hosted disable` | mutating, preview (AzureCloud only) | Take a deployed Hosted Agent endpoint offline without deleting the agent or its versions. |
| `hosted enable` | mutating, preview (AzureCloud only) | Restore endpoint service for a disabled Hosted Agent. |
| `hosted deploy` | mutating, preview (AzureCloud only) | Optionally provision, deploy one selected Hosted Agent service, reconcile status, and write a v2 receipt. Project endpoint and policy checks run before mutation and again after optional provisioning. A policy-less workspace requires `--no-guardrail`, and the receipt records the explicit opt-out. |
| `hosted show` | read-only (AzureCloud only) | Show the deployed Hosted Agent or one `--agent-version`. |
| `hosted versions list` | read-only (AzureCloud only) | List immutable Hosted Agent versions; `--include-drafts` adds preview draft versions. |
| `hosted diff` | read-only (AzureCloud only) | Compare the deployable workspace snapshot with the last verified deployment receipt and remote version. |
| `hosted diagnose` | read-only (AzureCloud only) | Inspect Hosted tooling, versions, endpoint routing, failed versions, and draft-routing issues. |
| `hosted smoke` | mutating, billable (AzureCloud only) | Invoke a Hosted Agent once through `responses` or `invocations`; WebSocket (`invocations_ws`) is not supported. |
| `hosted session create` | mutating (AzureCloud only) | Create a Hosted Agent session, optionally pinned to one active version. |
| `hosted session list` | read-only (AzureCloud only) | List Hosted Agent sessions visible to the current identity. |
| `hosted session show` | read-only (AzureCloud only) | Show one Hosted Agent session. |
| `hosted session stop` | mutating (AzureCloud only) | Stop Hosted session compute while preserving persisted state. |
| `hosted session delete` | destructive (AzureCloud only) | Delete a Hosted Agent session and its persisted sandbox state. |
| `hosted session file upload` | mutating (AzureCloud only) | Upload a contained local file to a Hosted session sandbox. |
| `hosted session file list` | read-only (AzureCloud only) | List files in a Hosted session sandbox. |
| `hosted session file download` | read-only (AzureCloud only) | Download a Hosted session file to a new contained local file. |
| `hosted session file delete` | destructive (AzureCloud only) | Delete one file from a Hosted session sandbox. |
| `hosted logs` | read-only (AzureCloud only) | Read a bounded Hosted Agent session log stream; requires both `--agent-version` and `--session-id`. |
| `hosted promote` | mutating (AzureCloud only) | Route 100% of Hosted endpoint traffic to one version (`--agent-version`) or restore `--latest`. |
| `hosted rollback` | mutating (AzureCloud only) | Route 100% of Hosted endpoint traffic to a prior active version; `--agent-version` is required. |
| `hosted versions prune` | destructive (AzureCloud only) | Delete old Hosted Agent versions while protecting the latest and routed versions. |
| `hosted versions delete` | destructive (AzureCloud only) | Delete one non-latest, non-routed Hosted Agent version. |
| `hosted delete` | destructive (AzureCloud only) | Permanently delete a Hosted Agent, all versions, and active sessions. |
| `hosted draft deploy` | mutating, preview (AzureCloud only) | Create and verify a preview Hosted Agent draft version from code or prebuilt image (Docker context rejected). The command verifies and serializes the configured policy as `rai_config.rai_policy_name`; a policy-less workspace requires `--no-guardrail`, which is recorded in the receipt. |
| `hosted init` | no | Create a validated Python Hosted Agent workspace scaffold; defaults deployment metadata to `Microsoft.DefaultV2`, accepts `--guardrail-policy-id`, and supports explicit `--no-guardrail`. It can also wire Bing Grounding, Bing Custom Search, and a Foundry Toolbox runtime. |
| `autopilot info` | no | Print the pinned experimental Hosted-agent Autopilot boundary (repository, commit, required tools, manual steps). |
| `autopilot preflight` | read-only (AzureCloud only) | Validate required executables, cloud, region, preview acceptance, and the pinned sample commit. |
| `autopilot deploy` | mutating, experimental (AzureCloud only) | Check out and provision the pinned Microsoft Hosted-agent Autopilot sample into an isolated `--work-dir`. |
| `prompt versions prune` | destructive | Retain the newest `--keep N` versions and delete the rest. |
| `prompt versions delete` | destructive | Delete one explicit immutable version. |
| `prompt delete` | destructive | Delete the logical agent and all versions. |
| `prompt decommission` | destructive | Delete the agent and, unless `--no-apim`, the Foundry APIM project connection. |

Prompt-agent lifecycle commands require `-f/--manifest`; for `prompt init`,
`-f/--manifest` is the path to *write*, not read. Commands under `hosted`
instead take `--workspace`, because the current first-party Hosted Agent source
of truth is an existing `azure.yaml` workspace. Commands under `autopilot` take
neither input format: they operate against one pinned upstream sample. Run
`fam <command path> --help` for the authoritative flag list.

Agent 365 blueprint commands use exactly one of `--blueprint-id` (application
ID) or `--blueprint-object-id` (directory object ID). Binding commands use
exactly one target: `-f/--manifest` for Prompt or `--workspace` for Hosted.
Hosted correlation also requires `--accept-preview`. Integration commands
require `--account-id` (full Foundry account resource ID).

## Global options

```text
-o, --output text|json|yaml   Output format (default text)
    --quiet                   Suppress successful text output
-v, --verbose                 Write diagnostic progress to stderr
    --debug                   Detailed redacted diagnostics; implies --verbose
    --progress auto|plain|off Long-running operation progress (default auto)
    --cloud <name>            AzureCloud (the only supported cloud)
    --tenant-id <tenant>      Microsoft Entra tenant override
    --request-timeout 120s    Per-request HTTP timeout (must be > 0)
    --retry-count 3           Retries for safe transient requests (must be >= 0)
    --retry-delay 1s          Initial retry backoff (must be > 0)
    --metadata key=value      Custom non-secret metadata; repeatable
    --receipt-log-endpoint    Azure Monitor Logs ingestion endpoint
    --receipt-log-dcr-id      Immutable DCR ID
    --receipt-log-stream      DCR stream (default Custom-FoundryAgentReceipts)
```

JSON and YAML use stable field names and are intended for CI/CD. `--quiet`
suppresses only successful **text** output; structured output and errors are
always emitted. Diagnostics from `--verbose` and `--debug` go to stderr, never
stdout. Debug output excludes command arguments, environment values, HTTP query
strings, headers, and bodies.

In `auto` mode, long-running Hosted `azd` phases show a spinner on an
interactive terminal or periodic elapsed-time heartbeat lines when text output
is redirected. Structured output remains quiet unless `--verbose` is enabled.
`--quiet` and `--progress off` disable progress.

### Custom metadata

`--metadata key=value` is repeatable. Later values replace earlier values with
the same case-sensitive key. For Prompt manifests, command-line values override
`agent.metadata`.

```yaml
agent:
  name: support-agent
  model: <model-deployment-name>
  metadata:
    owner: platform-team
    environment: production
```

```powershell
fam prompt deploy -f agent.yaml --if-changed `
  --metadata owner=operations-team `
  --metadata changeTicket=CHG-0000
```

Resolved metadata is copied to generated receipts and the optional Log
Analytics `Metadata` dynamic column. Prompt deployment sends it as agent-version
metadata, and metadata changes count as managed drift under `--if-changed`.
When Prompt metadata is not configured at all, existing remote metadata remains
unmanaged and is preserved on a new version; `metadata: {}` explicitly clears
it.
Hosted `azure.yaml` services can declare the same `metadata` map; `azd deploy`
sends that map with the Hosted Agent version. `hosted init --metadata key=value`
writes it into the generated service.

Foundry accepts at most 16 string entries, keys up to 64 characters, and values
up to 512 characters. Metadata is replicated to Azure, local receipts, and
potentially Log Analytics, so it must never contain secrets.

### Log Analytics receipt options

When `--receipt-log-endpoint` and `--receipt-log-dcr-id` are set, every command
that writes a receipt publishes its terminal redacted JSON after the local file
is safely persisted. `--receipt-log-stream` defaults to
`Custom-FoundryAgentReceipts`.

Publishing uses the Azure Monitor Logs Ingestion API and the current
`DefaultAzureCredential`. The CLI does not provision the workspace, custom
table, DCR, endpoint, or RBAC. If publishing fails, the operation returns an
error, keeps the local receipt, and provides a retry command:

```powershell
fam receipt upload `
  --file artifacts\deploy-receipt.json `
  --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com `
  --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef
```

See [Log Analytics Receipts](log-analytics-receipts.md) for the exact stream
schema, required DCR access, KQL, and ambiguous-ingestion handling.

### Model deployment options

`model deployment plan` and `model deployment create` read desired state from the optional
`model_deployment` manifest section. Command flags with the same meaning can
provide or override `--deployment-name`, `--model-name`, `--model-version`,
`--model-format`, `--sku-name`, `--capacity`, `--rai-policy-name`,
`--version-upgrade-option`, and `--spillover-deployment-name`.
`deployment_name` defaults to `agent.model`; the remaining core fields are
required. Create/delete accept `--wait-timeout` and `--wait-interval`; both
write receipts, while delete also supports `--dry-run` and `--yes`.
Every model deployment command requires `project.resource_id` to identify
the Foundry account.

### Doctor-specific options

```text
    --online               Run read-only authentication and target-access checks
    --fail-on-not-ready    Exit 11 after writing a non-ready report
    --check-provision      Inspect the Hosted provision command contract
    --environment <name>   Existing Hosted azd environment
    --azd-timeout 1h       Total Hosted diagnostic timeout
```

`ready` remains the requested-scope result for compatibility. New fields
distinguish `localReady`, `onlineReady`, `deploymentReady`, `checksComplete`,
and `coverageComplete`. Every check includes `category`, `severity`, and
optional `durationMs`, `observed`, `required`, and `nextSteps`.

Environment variables used for automation:

| Variable | Purpose |
|---|---|
| `FOUNDRY_AGENT_MANAGER_CLOUD` | Selects the Azure cloud when `--cloud` is not passed. Overrides the manifest `cloud` field. |
| `FOUNDRY_AGENT_MANAGER_TRUSTED_APIM_HOSTS` | Approved APIM gateway hosts. See [Destination trust approvals](security-and-operations.md#destination-trust-approvals). |
| `FOUNDRY_AGENT_MANAGER_TRUSTED_TOOL_HOSTS` | Approved external direct-tool and Toolbox hosts. |
| `FOUNDRY_AGENT_MANAGER_TRUSTED_MANAGED_IDENTITY_AUDIENCES` | Approved managed-identity token audiences. |
| `FOUNDRY_AGENT_MANAGER_TRUST_FILE` | Path to a JSON/YAML trust policy file, merged with the approvals above. |
| `FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY` | Default APIM subscription-key source (name changeable with `--apim-subscription-key-env`). |
| `FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES` | Extra APIM host **suffixes** accepted by the cloud allow-list. Never a substitute for an exact host approval. |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_ENDPOINT` | Azure Monitor Logs ingestion endpoint for automatic receipt publishing and `receipt upload`. |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_DCR_ID` | Immutable DCR ID used for receipt publishing. |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_STREAM` | DCR input stream; defaults to `Custom-FoundryAgentReceipts`. |

## Shell completion

Cobra's completion command generates scripts for Bash, Zsh, Fish, and
PowerShell. The scripts complete commands, focused `help` topics, flags,
supported values such as output formats and Hosted protocols, and relevant
file or directory paths. Completion is static and offline; it does not query
Azure resources.

```powershell
fam completion powershell > fam.ps1
fam completion bash > fam.bash
fam completion zsh > _fam
fam completion fish > fam.fish
```

Run `fam completion <shell> --help` for shell-specific
installation instructions.

## Exit codes and error envelope

| Code | Kind | Meaning |
|---:|---|---|
| `0` | — | Success |
| `1` | `internal` | Internal or unclassified failure |
| `2` | `manifest` | Manifest load or schema failure |
| `3` | `config` | Configuration, flag, availability, or cancelled confirmation |
| `4` | `security` | Security validation failure (host pinning, containment, unapproved destination) |
| `5` | `auth` | Authentication failure: Azure rejected or could not obtain a usable credential, including HTTP 401 |
| `5` | `authorization` | Authorization failure: Azure authenticated the principal but denied the operation with HTTP 403 |
| `6` | `not_found` | Resource not found |
| `7` | `conflict` | Conflict |
| `8` | `transient` | Transient failure or deadline exceeded |
| `9` | `tool` | Tool construction failure |
| `10` | `foundry` | Other Foundry or Azure service failure |
| `11` | `not_ready` | `doctor --fail-on-not-ready` wrote a complete report whose requested checks were not ready |
| `130` | `cancelled` | Signal cancellation (Ctrl+C, SIGTERM) |

With `--output json` or `--output yaml`, every failure is written to **stderr**
as a single envelope whose `exitCode` always equals the process exit code:

For standard Azure RBAC `403` responses, `nextSteps` includes the exact denied
action and scope reported by Azure. If the service omits those values, the CLI
returns generic least-privilege RBAC guidance instead of guessing a role.

```json
{
  "error": {
    "kind": "security",
    "message": "tools[3] mcp \"sample_mcp\" server_url: destination host \"mcp.example.com\" is not approved ...",
    "exitCode": 4
  }
}
```

The readiness exit `11` is the exception: `doctor` has already written the
complete report to stdout, so it does not add a second stderr error envelope.

### Structured error `nextSteps`

Error envelopes may include an optional `nextSteps` array with actionable
remediation guidance:

```json
{
  "error": {
    "kind": "not_found",
    "message": "Foundry project \"my-project\" does not exist",
    "exitCode": 6,
    "nextSteps": [
      "Create the project with: fam project create -f agent.yaml",
      "Or add --ensure-project to the deploy command"
    ]
  }
}
```

`nextSteps` is informational; automation should branch on `exitCode` and `kind`.

In text mode the same failure is written to stderr as `error: <message>`.

## `version` output contract

```powershell
fam version
# fam 0.16.3 commit=<commit> built=<timestamp>
```

| Format | Contract |
|---|---|
| `text` | `fam <version>`, then ` commit=<commit>` and ` built=<builtAt>` only when those values were stamped at build time. |
| `json` | Object with `version`, `commit`, `builtAt`. `commit` and `builtAt` are **omitted** when empty. |
| `yaml` | Keys `version`, `commit`, `builtAt`, with the same omission rule. |

```json
{
  "version": "0.16.3",
  "commit": "<commit>",
  "builtAt": "<timestamp>"
}
```

An unstamped `go build` prints `fam 0.16.3` and `{"version": "0.16.3"}`.
`fam --version` prints only `fam <version>`; use the
`version` subcommand when you need commit and build time.
