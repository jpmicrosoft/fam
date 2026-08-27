# Security and Operations

Destination trust approvals, security controls, Azure cloud support,
reliability, destructive safeguards, limitations, and troubleshooting.

## Why these controls matter

Agent deployment combines credentials, local files, external URLs, remote
tools, and billable mutations. A malformed or malicious manifest must not be
able to decide where tokens or data are sent. These controls give operators:

- Exact, external approval of credential-bearing destinations.
- Cloud and path containment before authentication or file access.
- Stable exit codes and remediation suitable for automation.
- Bounded retries that distinguish safe reads from ambiguous mutations.
- Confirmation and ownership checks before destructive operations.
- Redacted output and receipts that remain useful for incident review.

The result is intentionally more explicit than a portal wizard: users must
approve trust and destructive decisions, but those decisions become visible,
repeatable, and auditable.

## RBAC and separation of duties

The CLI is one executable, not one security principal. Organizations can use
separate Microsoft Entra identities for local review, Foundry project
deployment, infrastructure/model administration, production publication,
endpoint consumption, Agent 365 governance, runtime resource access, and audit
receipt ingestion.

`--yes`, dry-run controls, receipts, and protected trust approvals complement
RBAC but do not grant permission. When one built-in role permits both routine
and high-risk operations, use separate workload identities, PIM, protected CI
environments, and reviewer approval to enforce the remaining duty boundary.

See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md) for the
role reference and per-tool requirements.

## Destination trust approvals

A manifest is untrusted input. Cloud suffix checks prove only that a host
belongs to an Azure service family — not that **you** intended to send
credentials or agent data there.

Approvals let the repository describe *what* integration is desired while the
operator or protected CI environment decides *where* credentials and data may
go. This prevents a manifest author from approving their own destination.

`prompt preflight`, `prompt deploy`, `toolbox deploy`, and `connector toolbox deploy`
fail closed until the exact external destination is approved from outside the
manifest.

| Approval | Flag (repeatable) | Environment variable |
|---|---|---|
| APIM gateway host | `--trusted-apim-host` | `FOUNDRY_AGENT_MANAGER_TRUSTED_APIM_HOSTS` |
| External tool host | `--trusted-tool-host` | `FOUNDRY_AGENT_MANAGER_TRUSTED_TOOL_HOSTS` |
| Managed-identity audience | `--trusted-managed-identity-audience` | `FOUNDRY_AGENT_MANAGER_TRUSTED_MANAGED_IDENTITY_AUDIENCES` |

```powershell
fam prompt deploy -f agent.yaml --if-changed `
  --trusted-apim-host contoso.azure-api.net `
  --trusted-tool-host api.contoso.com
```

### Trust policy file

```yaml
# trust-policy.yaml
apimHosts:
  - contoso.azure-api.net
toolHosts:
  - api.contoso.com
audiences:
  - https://contoso.example.com
```

```powershell
fam prompt deploy -f agent.yaml --if-changed --trust-file trust-policy.yaml
```

- Same three categories, same validation.
- Merges with flags and environment variables.
- JSON or YAML, detected automatically.
- Unrecognized fields are rejected.

### Semantics

- **Exact hosts only.** No wildcards, no leading-dot suffixes.
- **No IDN.** Non-ASCII is rejected on both sides; use punycode.
- **Normalization.** Case-insensitive; omitted port = `:443`.
- **Operator input only.** Never read from a manifest.

### CI guidance

Set approvals from a **protected** CI environment, not from repository variables
that any pull request can change.

## A2A credential and identity boundaries

A2A discovery can use a public agent card or a card protected by the same
project connection used for remote-agent calls. The secure default is
anonymous discovery: `send_credentials_for_agent_card` defaults to `false`.

When explicitly enabled, Foundry sends project connection credentials only
over HTTPS and only to an agent-card host matching the effective A2A base
host. Cross-host absolute card URLs remain anonymous. The manager additionally
requires exact `--trusted-tool-host` approval for every absolute card host,
rejects HTTP and embedded URL credentials, and exposes the choice in offline
plan output.

Connection authentication determines which principal needs downstream RBAC:

| Connection mode | Downstream principal |
|---|---|
| `AgenticIdentityToken` with `RemoteTool` or `RemoteA2A` | The agent's `instance_identity` service principal; unattended/application-only. |
| `ProjectManagedIdentity` | The Foundry project managed identity. |
| OAuth identity passthrough | The consented user/delegated flow, separate from agent-native authentication. |

Do not grant both managed identities by default. Inspect the configured
connection mode and assign the minimum downstream role to the principal that
actually receives the token.

## Azure cloud support

The single-cloud boundary gives users one qualified set of authorities,
endpoints, token audiences, and host suffixes. Rejecting unsupported clouds
before authentication avoids a partial deployment or accidental cross-cloud
credential request.

Cloud selection precedence: `--cloud` > `FOUNDRY_AGENT_MANAGER_CLOUD` > manifest `cloud` > `AzureCloud`.

**AzureCloud is the only supported cloud.** Azure Government aliases are
recognized only to produce a clear `config` failure (exit `3`) explaining that
this release has not been qualified against a dedicated Azure Government
subscription. Rejection occurs during cloud resolution, before endpoint
construction, credential acquisition, or network access. There is no automatic
fallback to AzureCloud.

First-party service availability is not the same as manager qualification.
Prompt agents and stable endpoints have Government service coverage, while
Hosted agents, Microsoft 365/Teams publishing, MCP, and A2A do not have the
same boundary. This mixed matrix is why the manager rejects Government
entirely until the supported subset can be qualified end to end without
accidentally enabling unsupported integrations.

| Setting | `AzureCloud` |
|---|---|
| Entra authority | `login.microsoftonline.com` |
| ARM endpoint | `https://management.azure.com` |
| Foundry scope | `https://ai.azure.com/.default` |
| Microsoft Graph endpoint / scope | `https://graph.microsoft.com` / `https://graph.microsoft.com/.default` (permissions: `AgentIdentityBlueprint.Read.All`, `AgentIdentity.Read.All`, `AgentIdentityBlueprintPrincipal.Read.All`, `Application.Read.All`) |
| Project endpoint suffixes | `services.ai.azure.com`, `cognitiveservices.azure.com`, `openai.azure.com` |
| APIM suffix | `azure-api.net` |
| Key Vault suffix | `vault.azure.net` |
| Azure Monitor Logs ingestion suffix | `ingest.monitor.azure.com` |
| Storage Queue suffix | `queue.core.windows.net` |

## Security controls

These controls are defaults rather than optional hardening. Users gain the same
credential, URL, file, redirect, routing, and redaction behavior on a laptop and
in CI instead of depending on each operator to remember a checklist.

Summary; the full threat model and trust boundary are in
[`../SECURITY.md`](../SECURITY.md).

- Foundry, APIM, and Key Vault URLs require HTTPS and cloud-specific approved
  host suffixes.
- External destinations require exact operator approval.
- Embedded URL credentials (userinfo) are rejected everywhere.
- Manifest-controlled files stay inside the manifest directory via rooted
  directory handles, bounded at 8 MiB.
- APIM keys and tokens are never written to output or receipts.
- Optional receipt publishing uses a host-pinned Azure Monitor Logs ingestion
  endpoint, the `https://monitor.azure.com/.default` audience, and the same
  centrally redacted terminal JSON written locally.
- Custom metadata is restricted to Foundry's bounded string map but is
  intentionally replicated to agent versions, receipts, and optional Log
  Analytics records. It is never an approved secret channel.
- Key Vault secret retrieval refuses redirects.
- Every routing mutation is verified, never blindly retried.
- Hosted draft versions cannot receive endpoint traffic.
- Session file paths are validated against traversal.
- Draft code archives use deterministic ZIP with SHA-256 integrity.
- `prompt m365 publish` sends `publishAsAutopilot: false` and never retries after an
  ambiguous outcome.
- A2A agent-card credentials are opt-in and same-host HTTPS only; absolute card
  hosts require independent trust approval.
- Agent 365 blueprint, identity, and principal inspection is GET-only,
  host-pinned to `graph.microsoft.com`, refuses redirects, bounds response
  bodies, selects no password/key/federated credential properties, and never
  parses generated Agent 365 configuration files. Graph pagination follows
  exact v1.0 nextLinks with host validation and a 50-page/5,000-result cap.
  `integration set` mutates only `properties.a365LoggingEnabled` via ARM with
  `--yes` and read-back verification. `observability status` is read-only.
  Publication commands are read-only/plan-only.

## Agent 365 Graph, identity, and integration controls

`agent365` uses the documented Microsoft Graph v1.0 Agent ID blueprint,
identity, and blueprint principal APIs, plus ARM API `2026-03-15-preview` for
integration logging. The documented Graph permissions are
`AgentIdentityBlueprint.Read.All`, `AgentIdentity.Read.All`,
`AgentIdentityBlueprintPrincipal.Read.All`, and `Application.Read.All` (for
sponsors, friendly names, and observability assignment inspection); delegated
non-owners also need the Agent ID Administrator role.

- `--blueprint-id` and `--blueprint-object-id` require strict GUIDs and are
  mutually exclusive.
- Graph application IDs are resolved through the typed blueprint collection;
  direct resource paths use the directory object ID.
- Only non-secret metadata is selected. Credential values are ignored even if
  a hostile or future response includes extra properties.
- `a365.generated.config.json` is never read because it can contain
  `agentBlueprintClientSecret` and `azureOpenAIApiKey`.
- `binding plan` and `binding status` use read-only Foundry and Graph requests.
  They do not write metadata, call undocumented endpoints, or claim that ID
  equality proves an operator-created binding.
- `integration set` mutates only `properties.a365LoggingEnabled` via ARM and
  verifies with a read-back. `a365Status` is read-only and cannot be modified.
- `observability status` checks app-role assignments via Graph read operations
  only; it does not assign roles.
- `publication` commands are read-only and plan-only; they do not perform
  generic registry mutation or arbitrary existing blueprint binding.
- Paginated Graph inventory endpoints follow exact `graph.microsoft.com` HTTPS
  v1.0 `@odata.nextLink` values with a maximum of 50 pages (5,000 results).
  Pagination links are validated against the same host pinning.
- Foundry runtime, project, blueprint, and publishing identities remain
  distinct principals. Azure RBAC must target the identity that actually
  receives the downstream token.
- Identity lifecycle: unpublished agents share the project identity;
  publication creates a distinct identity; RBAC does not transfer. The CLI
  outputs `shared-or-distinct-unverified` when the distinction cannot be
  proven.

## Reliability and diagnostics

Reliability rules make failures actionable without converting uncertainty into
a duplicate mutation. Safe reads may retry; version creation, invocation, and
ambiguous routing outcomes require explicit reconciliation.

- Ctrl+C and SIGTERM cancel in-flight work; process exits `130`.
- GET/HEAD/OPTIONS/PUT/DELETE use bounded exponential retries (capped 30 s).
- Agent-version and invocation POSTs are **not** automatically repeated.
- `Retry-After` is honored and clamped to 30 s.
- Every request carries `x-ms-client-request-id`.
- `--request-timeout` bounds each individual HTTP request, not the whole command.

## Log Analytics receipt publishing

Receipt publishing is an opt-in external audit sink. Configure it with
`--receipt-log-endpoint`, `--receipt-log-dcr-id`, and optionally
`--receipt-log-stream`, or their documented environment variables.

- `receipt upload` performs a network write and requires `-AllowMutations`
  (mutation gate) in the live-release matrix.
- Destination validation occurs before the receipt-producing mutation.
- The terminal local receipt is persisted before the Logs ingestion POST.
- The POST is not automatically retried. A transport failure or transient
  response can be ambiguous, so the command preserves the local file and gives
  a `receipt upload` retry command.
- HTTP `401` and `403` retain the CLI's distinct authentication and
  authorization kinds. Standard Azure RBAC action/scope details remain in
  `nextSteps`.
- Only manager-generated v1/v2 receipt JSON is accepted by the retry command.
- The principal needs DCR ingestion permission. Microsoft documents
  **Monitoring Metrics Publisher**; a custom role can grant
  `Microsoft.Insights/Telemetry/Write`.
- The CLI does not provision or modify a workspace, custom table, DCR, endpoint,
  retention policy, or role assignment.

Receipts contain no registered credentials or trust approvals, but they can
contain custom metadata, local paths, Azure IDs, names, and errors. Do not put
tokens, passwords, connection strings, personal data, or other sensitive values
in `agent.metadata`, Hosted service `metadata`, or `--metadata`. Treat the Log
Analytics table as operational audit data and apply appropriate workspace
access and retention.
See [Log Analytics Receipts](log-analytics-receipts.md).

## Model deployment mutation controls

Model deployment capacity can be billable, quota constrained, and shared by
multiple agents. The CLI therefore keeps model lifecycle separate from Prompt
Agent deployment:

- `model deployment plan` performs live account/region catalog, exact version,
  SKU shape, quota, regional capacity, RAI policy, and spillover checks without
  mutation.
- `prompt deploy` never invokes model creation.
- `model deployment create` uses a create-only conditional request. An exact
  ready deployment is unchanged; existing drift is rejected rather than
  updated silently.
- Create waits for `Succeeded` and records the deployment ID, requested action,
  final state, and reconciliation guidance in a redacted receipt.
- A transport or transient response after a mutation is reported as ambiguous.
  Inspect `model deployment show` and the receipt before retrying.
- Provider terms, pricing approval, and shared-consumer impact remain operator
  decisions even when every technical plan check passes.

## Destructive safeguards

Cleanup is separated from deployment so retention can be reviewed and automated
with narrower permissions. Dry runs show scope first, while confirmation and
version protections prevent a routine command from silently removing live
resources.

`model deployment delete`, `prompt versions prune`, `prompt versions delete`,
`prompt delete`, and `prompt decommission` require confirmation.

`grounding sync` requires the destructive approval gate when any of `--prune`,
`--delete-replaced-uploads`, or `--delete-pruned-uploads` is enabled, because
these options can cause broad deletion of project uploads.

```powershell
fam model deployment delete -f agent.yaml --dry-run
fam model deployment delete -f agent.yaml --yes
fam prompt versions prune -f agent.yaml --keep 3 --dry-run
fam prompt versions prune -f agent.yaml --keep 3 --yes
fam prompt versions delete -f agent.yaml --agent-version 7 --yes
fam prompt decommission -f agent.yaml --yes
```

- `--dry-run` prints what would be deleted without Azure mutation.
- `--yes` runs non-interactively. Required with structured output.
- `prune --keep` must be at least 1, validated locally.

### Ambiguous and duplicate security-sensitive flags

The live-release qualification runner rejects scenarios that contain duplicate
or conflicting occurrences of security-sensitive boolean flags (`--dry-run`,
`--prune`, `--delete-replaced-uploads`, `--delete-pruned-uploads`). Cobra/pflag
silently uses the last value for repeated flags; the approval gate instead fails
closed rather than guessing. Boolean flag values must use Cobra-compatible
spellings (`true`, `false`, `1`, `0`, `t`, `f`, `yes`, `no`, `y`, `n`).
Unrecognized boolean spellings are also rejected.

### Existing-source adoption

`hosted adopt` treats source code as untrusted local input. Copy mode writes
only to a new relative destination inside the current directory and never
modifies the source. `--in-place` is the explicit mutation boundary and refuses
to replace an existing `azure.yaml`.

Both modes reject symbolic links, non-regular files, traversal, unsafe
destinations, excessive file counts, individual files over 32 MiB, and source
trees over 250 MiB. Copy mode excludes `.env`, non-example `.env.*` files,
virtual environments, Python caches, `.azure`, `.foundry-agent-manager`,
`.foundry`, and `.git`. In-place writes are rolled back when the generated
workspace or merged `.agentignore` fails validation.

## Limitations and unverified external behavior

This list identifies where a successful local check is not proof of an upstream
service behavior. Users can use it to decide which preview capabilities need a
pilot, extra monitoring, or a manual operational step before production use.

- Preview API surfaces can change without notice.
- Catalog surfaces (`tool-catalog`, `connector list`, `connector api-center list`) are separate.
- Compatibility is source-stamped, not live validation.
- Memory is not a trusted instruction channel.
- Hosted Agent extension is pinned preview software.
- Hosted `invocations_ws` not supported by `hosted smoke`.
- Draft versions cannot receive endpoint traffic.
- Scaffold does not use `azd`.
- Prompt preflight validates the configured model deployment with a read-only
  project lookup and verifies an explicitly configured RAI policy through ARM.
- Hosted preflight, deploy, and draft deploy fail closed when the workspace has
  no RAI policy unless the operator passes `--no-guardrail`; that flag is
  rejected when a policy is configured, and mutating receipts preserve the
  explicit opt-out.
- Those commands bind `AZURE_AI_PROJECT_ID` to the resolved deployment
  endpoint even for an explicit policy opt-out, resolve the deployment-time
  policy, enforce same-account scope, and verify it through ARM. Deploy repeats
  these checks after optional provisioning and before agent deployment. Draft
  deployment serializes and verifies
  `rai_config.rai_policy_name`. The manager's `DefaultAzureCredential` identity
  needs read access to `Microsoft.CognitiveServices/accounts/raiPolicies`;
  azd's deployment identity is checked separately.
- Model planning validates optional deployment-level RAI and spillover
  references, but it does not accept cross-account fine-tuned source fields or
  provider-specific Marketplace purchase terms.
- Toolbox and grounding synchronization are separate from agent deployment.
- Function Calling execution is caller-owned.
- No concurrency token for shared ARM resources.
- Smoke tests cost money.
- Microsoft 365 tenant admin approval is external.
- Autopilot is experimental and reviewed at exactly one commit.
- No prompt-agent Autopilot path.
- Legacy compatibility is not a migration tool.
- Azure Government must not be enabled until dedicated subscription qualification is complete.

## Troubleshooting

Start here when a command fails: the symptom maps the stable error to the
operator decision or missing prerequisite that resolves it. Structured output
also includes `error.nextSteps` for automation and UI surfaces.

| Symptom | Cause | Fix |
|---|---|---|
| `project.resource_id is invalid` (exit `3`) | The Azure resource ID is malformed, has an invalid subscription UUID, wrong provider, or extra segments | Provide a valid project resource ID: `/subscriptions/<uuid>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<account>/projects/<project>` |
| `destination host "..." is not approved` (exit `4`) | Host not approved | Add `--trusted-apim-host` / `--trusted-tool-host` |
| `must be a bare host with an optional port` (exit `3`) | Approval is a URL | Pass `host:port` only |
| `wildcard and suffix approvals are not supported` (exit `4`) | Wildcard used | Approve each exact host |
| `AzureUSGovernment is unsupported` (exit `3`) | Government cloud selected | Use `AzureCloud` |
| `Azure Developer CLI version is too old` (exit `3`) | `azd` < 1.27.1 | Upgrade `azd` |
| `required Foundry azd extension is not installed` (exit `3`) | Missing extension | `azd extension install azure.ai.agents --version 1.0.0-beta.8` |
| `Hosted Agent preview was not explicitly accepted` (exit `3`) | No `--accept-preview` | Add `--accept-preview` |
| `apim.auth=api_key requires a subscription key` (exit `3`) | No secret source | Set env var or pass source flag |
| `--yes is required for destructive operations with --output json` (exit `3`) | Interactive prompt in structured mode | Add `--yes` |
| HTTP `401`, error kind `auth` (exit `5`) | Azure rejected the credential or the CLI could not acquire one for the correct tenant/audience | Authenticate again, verify tenant and audience, and use `--tenant-id` when required |
| HTTP `403`, error kind `authorization` (exit `5`) | Azure authenticated the principal but RBAC denied the operation | Follow `error.nextSteps`; when Azure reports them, the CLI names the exact denied action and scope. Grant only a least-privilege role containing that action, wait for propagation, refresh the credential, and retry |
| `Foundry project "..." does not exist` (exit `6`) | Project missing | Use `project create` or `--ensure-project` |
| `model deployment "..." does not exist` (exit `6`) | `agent.model` does not exactly match a deployment available to the selected project/account | Correct `agent.model`, or define `model_deployment`, run `model deployment plan` and `model deployment create`, then rerun `prompt preflight` |
| `quota ... has ... available units` (exit `7`) | Requested model capacity exceeds current regional quota | Reduce capacity or obtain quota, then rerun `model deployment plan` |
| `regional capacity ... requests ...` (exit `7`) | Azure does not currently advertise enough placement capacity for the exact model version/SKU | Select an available version/SKU/region under your architecture rules or retry planning later |
| `already exists with different configuration` (exit `7`) | The deployment name is occupied by drifted managed state | Inspect with `model deployment show`; choose a new name or deliberately delete and recreate it |
| `model deployment "..." is not ready` (exit `7`) | The parent-account deployment exists but ARM reports a provisioning state other than `Succeeded` | Repair or wait for the deployment in Azure, then rerun `prompt preflight` |
| `azd environment "..." does not exist` (exit `3`) | Hosted environment was never created for the workspace | Run `fam hosted environment create --workspace <workspace> --environment <environment>` or rerun Hosted quickstart with `--bootstrap-environment` |
| `Hosted Agent Foundry project endpoint could not be resolved` or azd reports `FOUNDRY_PROJECT_ENDPOINT` is not set (exit `3`) | Provisioning did not populate the environment, or bootstrap was never run | Provision intentionally or rerun `hosted environment create` with `--project-id` and `--model-deployment`; it derives and configures both the canonical azd value and compatibility alias |
| azd doctor skips `Developer has required role on Foundry project` because `AZURE_AI_PROJECT_ID` is not set | Environment was not configured with the project resource ID | Rerun `hosted environment create` with `--project-id <project-resource-id>`; the manager validates and stores the full project resource ID for azd diagnostics |
| `Hosted Agent Foundry project access check failed` with HTTP 403 (exit `5`) | azd is authenticated to the wrong tenant, or its identity lacks deployment RBAC | Reauthenticate azd with `azd auth login --tenant-id <tenant-id>` and assign `Foundry Project Manager` on the target project |
| RAI policy inspection returns `404` or `403` | The configured policy does not exist on the project account, or the credential cannot read account RAI policies | Correct the same-account policy resource ID, or grant the deployment/preflight identity the narrowest approved role containing `Microsoft.CognitiveServices/accounts/raiPolicies/read` |
| `AZURE_AI_PROJECT_ID does not match the resolved Foundry project endpoint` (exit `3`) | The azd environment contains conflicting project identity and endpoint values | Rerun `hosted environment create` with the intended `--project-id`, then inspect both project endpoint values before retrying |
| `Hosted workspace declares no agent-level RAI policy` (exit `3`) | The `policies` block is absent, so intent cannot be inferred | Add a same-account RAI policy to `azure.yaml`, or pass `--no-guardrail` to the online preflight/deploy/draft command to explicitly accept the opt-out |
| `Hosted Agent is not deployed in the selected azd environment` (exit `3`) | Wrong service/environment or no Hosted deployment yet | Review `hosted plan`, then run `hosted deploy` |
| Deploy error with receipt path | Partial mutation | Inspect receipt; act on `reconcile-*` steps |
| Receipt Logs ingestion `401` (exit `5`) | Azure Monitor rejected or could not obtain the credential | Authenticate in the correct tenant and retry `receipt upload` |
| Receipt Logs ingestion `403` (exit `5`) | The principal lacks DCR ingestion permission | Use the reported action/scope when present; assign least privilege on the DCR, wait for propagation, then retry the preserved receipt |
| Receipt Logs ingestion transport/transient failure (exit `8`) | Azure may have accepted the POST but the response was lost | Query by `ReceiptId`; retry with `receipt upload` and de-duplicate that ID if necessary |
| `invocations_ws ... not supported by hosted smoke` (exit `3`) | WebSocket protocol | Use a dedicated WebSocket client |
| `Hosted Agent draft version ... cannot receive endpoint traffic` (exit `3`) | Draft targeted | Deploy a regular version first |
