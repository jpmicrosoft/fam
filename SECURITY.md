# Security

This document describes the security model of `foundry-agent-manager`, the guarantees it
does and does not make, and how to operate and report issues against it.

- [Supported versions](#supported-versions)
- [Trust boundary](#trust-boundary)
- [Threat model](#threat-model)
- [Controls](#controls)
- [Destination approvals](#destination-approvals)
- [Secret handling](#secret-handling)
- [Azure cloud boundary](#azure-cloud-boundary)
- [Secure deployment guidance](#secure-deployment-guidance)
- [Known and accepted operational risks](#known-and-accepted-operational-risks)
- [Out of scope](#out-of-scope)
- [Reporting a vulnerability](#reporting-a-vulnerability)

## Supported versions

| Version | Status |
|---|---|
| `0.14.x` (current application version) | Supported. Fixes land here. |
| Current `main` | Supported. This is where fixes are developed. |
| Anything older | Not supported. |

`0.14.0` is the version compiled into the binary
([`internal/config/config.go`](internal/config/config.go)). Release assets are
created only after the matching `v0.14.0` tag is pushed.

Always report against the current `main` if you can reproduce there.

## Trust boundary

The single most important rule:

> **The manifest is untrusted input. The operator's command line and CI
> environment are the trust boundary.**

| Input | Trust | Why |
|---|---|---|
| Manifest YAML/JSON (`-f`) | **Untrusted** | May be authored by anyone with repository write access, or generated; includes direct tools and managed Toolbox definitions. |
| Files the manifest references (`spec_file`, grounding documents, `--instructions-file`) | **Untrusted** | Content and path are manifest-controlled. Grounding documents are uploaded to the selected Foundry project. |
| OpenAPI documents, including every `servers[].url` | **Untrusted** | Determine where prompt-agent or Toolbox traffic and tokens go. |
| Destination approval flags and their environment variables | **Trusted** | Supplied by the operator or a protected CI environment. |
| Trust policy file (`--trust-file`/`FOUNDRY_AGENT_MANAGER_TRUST_FILE`) | **Trusted** | Path is operator/CI-supplied, never read from the manifest; its contents are validated with the same rules as the approval flags before use. |
| Publication configuration (`--publication`, `publish-m365` only) | **Trusted, but schema-validated** | A separate operator/CI-supplied file (`foundry-agent-manager/publication/v1`); never part of, or read from, the agent manifest. It carries the Microsoft 365 publish scope and Bot Service coordinates, so treat it with the same review rigor as the manifest's APIM/tool destinations. |
| Hosted Agent `azure.yaml`, local `$ref` files, and source tree | **Untrusted** | Control what `azd` packages, builds, uploads, provisions, and runs. |
| Foundry managed connector catalog entries and operation schemas | **Semi-trusted** | Publisher-controlled metadata determines available actions and model-visible parameter descriptions; exact connector and operation selection remains operator-controlled. |
| APIM subscription key source selection | **Trusted** | Operator chooses the source; the manifest cannot. |
| Azure credential from `DefaultAzureCredential` | **Trusted** | Belongs to the caller's environment. |
| ARM responses (project endpoints) | **Semi-trusted** | Revalidated against the selected cloud before a token is sent. |
| Agent 365 blueprint IDs (`--blueprint-id`, `--blueprint-object-id`) | **Trusted operator input** | Strict GUIDs supplied outside manifests; the two ID types are mutually exclusive. |
| Agent 365 integration account coordinates (`--subscription-id`, `--resource-group`, `--account-name`) | **Trusted operator input** | ARM coordinates supplied outside manifests; `integration set` requires `--yes`. |
| Microsoft Graph Agent ID blueprint, identity, and principal responses | **Semi-trusted** | Read through a fixed Graph v1.0 endpoint, bounded and schema-decoded; only non-secret properties are selected and identifiers are revalidated. Pagination follows exact `graph.microsoft.com` HTTPS v1.0 nextLinks with max 50 pages/5,000 results. |
| Deployment receipts | **Output by default; explicit input to `receipt upload`** | Manager-generated v1/v2 JSON is centrally redacted. Retry upload accepts an operator-selected local receipt file only. |
| Azure Monitor Logs ingestion endpoint and DCR | **Trusted operator configuration** | Never read from a manifest. The endpoint is HTTPS-pinned to AzureCloud before token acquisition or receipt upload. |

A manifest can request anything. It can never *authorize* anything.

## Threat model

Threats the design explicitly addresses:

| Threat | Control |
|---|---|
| Hostile manifest redirects an APIM subscription key to an attacker gateway | Cloud suffix pinning **plus** exact `--trusted-apim-host` approval, enforced before the secret is resolved. |
| Hostile manifest exfiltrates agent conversation data through an external direct tool or Toolbox tool | Every effective external destination host must be exactly approved with `--trusted-tool-host`. |
| Hostile A2A manifest redirects authenticated agent-card discovery to an attacker | Agent-card credential sending is off by default. When enabled, Foundry restricts it to same-host HTTPS; the manager rejects HTTP, scheme-relative URLs, embedded credentials, backslashes, and fragments, and requires exact approval for every absolute card host. |
| Hostile manifest directs an identity token to an attacker resource | Managed-identity audiences must be the built-in cloud default or exactly approved; `/.default` scope forms are rejected. |
| OpenAPI spec hides its real destination behind templating or server variables | Templated URLs and `variables` are rejected as ambiguous. |
| OpenAPI spec pulls in an uninspected remote document | Non-local `$ref` values are rejected. |
| Manifest reads files outside its directory (path traversal) | Relative-path validation plus a rooted directory handle. |
| Symlink/junction swapped in **after** validation (TOCTOU) | Reads go through `os.OpenRoot`, so the kernel resolves each component against the directory handle. |
| Oversized file exhausts memory | Instructions and OpenAPI reads are capped at 8 MiB. Grounding documents are capped at 512 MiB and streamed rather than loaded into memory. |
| Grounding file changes between validation and upload | Size and SHA-256 are rechecked through the rooted file handle; the uploaded project file is deleted and the operation fails if content changed. |
| Duplicate manager-owned logical vector stores make deployment target selection ambiguous | Logical-name resolution requires exactly one manager-owned match and fails closed on duplicates. |
| A network failure makes a grounding create/upload/attach/delete outcome unknown | Non-repeatable POSTs are not silently retried; the command independently re-reads state when possible and otherwise records `uncertain` reconciliation guidance in a v2 receipt. |
| A cleanup option deletes a project file still used by another vector store | Global upload deletion is off by default, explicitly named, confirmed with `--yes` for structured automation, and recorded in the receipt. |
| Credentials embedded in URLs | Userinfo is rejected in endpoints, APIM targets, tool URLs, audiences, and approvals. |
| Generated Agent 365 configuration exposes client secrets or model keys | The manager never reads `a365.generated.config.json` and never selects Graph password, key, or federated credential properties. |
| ID equality is mistaken for a successful Agent 365 binding | `binding plan` and `binding status` are GET-only correlation surfaces; no metadata write or undocumented Foundry mutation is available. |
| Integration set silently changes the wrong account | ARM coordinates are explicit operator input; `--yes` is required; `set` verifies with a read-back and supports `--if-match` for concurrency control. |
| Observability status falsely claims a role is assigned | `observability status` reads app-role assignments via Graph and never assigns roles; it requires `Application.Read.All`. |
| Publication commands mutate registry state | All publication commands are read-only/plan-only; no generic registry mutation or arbitrary existing blueprint binding is performed. |
| Identity lifecycle RBAC is silently inherited | The CLI explicitly warns that publication creates a distinct identity and that RBAC does not transfer; it outputs `shared-or-distinct-unverified` when the distinction cannot be proven. |
| Lookalike/IDN host approved by mistake | Non-ASCII hosts and audiences are rejected on both sides; punycode is required. |
| Over-broad approval (`*.example.com`) | Wildcard and suffix approvals are rejected. |
| Secret leaks into logs, errors, or receipts | Central redaction of every registered credential, including JSON, query, and path encodings. |
| Generic connection credentials leak through inspection | `connection-list` and `connection-show` remove the entire ARM `credentials` object; create/update register every string credential for error redaction and receipts contain identifiers only. |
| Runtime structured inputs replace an approved tool destination | Dynamic MCP destination URLs remain unsupported; structured input values are schema-validated and cannot alter the statically inspected endpoint contract. |
| Memory content becomes a persistent prompt-injection or cross-user disclosure channel | Every low-level operation requires an explicit scope, agent `{{$userId}}` values are passed only through `x-memory-user-id`, scope deletion is supported, and Government/VNet-unsupported use fails closed. Applications must still use unguessable scope identifiers and evaluate retrieved content as untrusted. |
| Skill upload escapes its selected directory | Directory uploads reject symlinks, require a root `SKILL.md`, validate contained relative paths, and enforce per-file and aggregate memory limits before multipart construction. |
| Control-plane call silently falls back to the wrong cloud | ARM routing fails closed when the cloud ARM endpoint or scope is unresolved; cross-cloud hosts are rejected in both directions. |
| Key Vault fetch redirected to another host | The Key Vault client refuses redirects and pins the cloud's vault suffix. |
| Non-deterministic endpoint choice hides where traffic went | ARM-advertised endpoint keys are sorted before selection. |
| Failed deploy leaves undocumented mutations | Atomic receipts record every step; unresolved shared-resource state is flagged `required` for manual reconciliation. |
| Optional receipt publishing sends audit data or a bearer token to an attacker | The destination is operator/CI configuration rather than manifest data, HTTPS-only, path/query/fragment-free, and pinned to `ingest.monitor.azure.com`; redirects are refused and the token audience is fixed to `https://monitor.azure.com/.default`. |
| A lost Logs ingestion response causes a duplicate audit record | Receipt POSTs are never automatically retried. The local file is written first, failures include a standalone retry command, and the stable receipt ID supports de-duplication. |
| An APIM connection update before promotion silently changes production behavior | Shared connections attached to an agent that already has an active version refuse update by default; `--allow-active-apim-update` is a deliberate, off-by-default override. |
| A network failure during a routing PATCH (`deploy` staging, `promote`, `rollback`, `endpoint-configure`) or the Microsoft 365 publish POST leaves the real outcome unknown, and a naive retry double-applies it | Every such mutation is followed by an independent re-read to verify convergence; on an unresolved ambiguous outcome the command fails closed and is never retried automatically, and the receipt records `reconcile-*`/`unknown` guidance instead. |
| An unreviewed or tampered Autopilot sample is executed | The wrapper clones only the pinned, reviewed commit and verifies the checked-out `HEAD` SHA matches exactly before running anything; `--accept-preview` and an exact `--approve-sample-commit` match are both mandatory. |
| A preview Hosted Agent extension changes behavior underneath automation | Online `hosted-*` commands require exactly the reviewed `azure.ai.agents` version, verify the installed version through JSON, and check the documented deploy/show/provision help tokens before mutation. |
| Hosted doctor executes an unreviewed extension while collecting failures | Diagnostic checks are dependency-aware: unsupported `azd` versions and missing or mismatched `azure.ai.agents` versions block every `azd ai agent` command. Project endpoint resolution and `DefaultAzureCredential` access probes remain blocked until the complete tooling trust contract passes. |
| Hostile Hosted Agent workspace escapes its source root or pulls a remote `$ref` | The workspace, source directory, and every local `$ref` are opened through rooted directory handles; absolute paths, parent traversal, remote references, symlink escapes, and junction escapes are rejected. Nested/list-entry references and sibling overlays are resolved recursively and included in the workspace hash. |
| Hosted Agent YAML executes a local command through an azd hook | Top-level and service-level `hooks` are rejected before executable lookup or authentication. |
| Hosted Agent YAML redirects azd to an untrusted project endpoint | Existing project endpoints are HTTPS-pinned to an account subdomain of `services.ai.azure.com` and must use the exact `/api/projects/<project>` shape without a custom port, query, or fragment. |
| A Hosted service or environment value becomes an `azd` option | Service and environment names are constrained locally, and every subprocess is executed directly with an argument array rather than through a shell. |
| Hosted preflight exposes environment secrets | Preflight verifies the selected environment through `azd env list --output json`; it never requests environment values. Lifecycle commands retrieve only canonical `FOUNDRY_PROJECT_ENDPOINT`, with a narrow `AZURE_AI_PROJECT_ENDPOINT` compatibility fallback, and never request the complete environment. Preflight/deploy run azd's read-only Agent diagnostics with bounded in-memory output so tenant/RBAC failures stop before mutation; subprocess output is never persisted, and receipts store only command names, args, directory, duration, and exit code. |
| Hosted quickstart unexpectedly deploys, authenticates, or provisions resources | Interactive Hosted quickstart asks before bootstrapping; non-interactive use requires `--bootstrap-environment`. Bootstrap validates supplied tenant UUID, project resource ID, and Azure location values, is limited to workspace-local `azd env new` / `azd env set` operations, and never runs `azd auth login`, assigns RBAC, provisions, deploys, or invokes. |
| Doctor or debug output exposes credentials | Hosted doctor resolves only the validated canonical project endpoint, with a narrow compatibility fallback, needed for its read-only project probe and redacts the project name in output. Global `--debug` records command phase/executable, HTTP method/host/path, status, timing, and retry timing only; it excludes command arguments, environment values, query strings, headers, bodies, and subprocess output. |
| A Hosted lifecycle command sends a bearer token to a substituted endpoint | A literal or narrowly retrieved project endpoint is revalidated against the AzureCloud Foundry host and exact project-path rules before `DefaultAzureCredential` is invoked or an HTTP request is built. Redirects are refused. |
| A Hosted disable/enable request fails after reaching Foundry | Lifecycle POSTs are non-repeatable and never automatically retried. The command independently re-reads logical state, reports `succeeded-reconciled` semantics only when the target state is proven, and otherwise fails as an ambiguous mutation; a manual rerun reads state before deciding whether another POST is necessary. |
| `azd deploy` fails after creating a Hosted Agent version | The tool snapshots the prior version, then reconciles through verified `azd ai agent show --output json`; only a newly active version can produce `succeeded-reconciled`. An unchanged prior version or unresolved outcome is recorded as `unknown` and is never silently retried. |
| Hosted session file paths escape the sandbox root | Session file remote paths reject absolute paths, `..` traversal, null bytes, and colon characters; workspace-relative upload and download paths use the same rooted containment as other manifest-referenced files. |
| Hosted draft deployment builds a non-deterministic archive | The ZIP uses lexicographic file ordering, fixed 1980-01-01 timestamps, and normalized permissions; the SHA-256 digest is sent as `x-ms-code-zip-sha256` so the server can verify archive integrity. |
| Hosted draft environment values leak through bulk retrieval | Environment variable references are resolved one at a time with `azd env get-value`, never `azd env get-values`; each resolved value is registered as a secret with the receipt store for redaction. |
| A draft version is accidentally routed to production | `hosted-promote` and `hosted-rollback` reject draft versions; `hosted-diagnose` surfaces drafts in endpoint routing as an actionable issue. |
| A Toolbox create, promotion, or deletion has an unknown server-side outcome | Non-repeatable create and promotion requests are not silently retried; every unresolved mutation records the uncertain resource in a v2 receipt and requires a logical Toolbox/version re-read before retry. |
| A Hosted runtime treats Toolbox `require_approval` metadata as an enforcement boundary | Generated code defaults to Agent Framework `always_require`; `hosted-smoke` submits approval responses only for exact `<server>/<tool>` allowlist entries. Other clients must implement the same review/continue-or-reject round trip. |
| A Hosted Agent sends sensitive prompts through Bing Grounding without accounting for the service boundary | Hosted plans and scaffold documentation state that Bing queries, tool parameters, and the resource key cross the Azure compliance boundary; the integration is AzureCloud-only and requires normal network access without VPN or private endpoints. |
| A managed connector exposes destructive or excessive SaaS actions to the model | `connector-configure` registers only explicit `--operation` values, excludes webhook/notification triggers, treats the supplied set as the complete allowlist, and requires `--yes` before replacing an existing configuration. |
| A connector consent URL leaks or is used for the wrong identity | Consent links are generated for an explicit object ID and home tenant ID, returned only to the caller, and never written to receipts. Operators must deliver the short-lived URL only to the named principal. |
| A cloud-gated workflow is used where it is unavailable or unverified | `LegacyApplications`/`M365Publishing`/`HostedAgents`/`HostedAutopilot`/`Toolboxes` capability gates fail closed with a `config` error before any Azure call, with no cross-cloud fallback. |

## Controls

Enforced by the code, with tests and fuzz targets covering them:

- HTTPS-only, cloud-pinned hosts for Foundry, APIM, Key Vault, Storage Queue,
  and Azure Monitor Logs ingestion endpoints.
- Exact operator approval of APIM hosts, tool hosts, and non-default
  managed-identity audiences, at `preflight`, `deploy`, `toolbox-deploy`, and
  `connector-toolbox-deploy`.
- Fail-closed OpenAPI destination extraction (root, path item, operation,
  `webhooks`, `components.pathItems`).
- MCP headers reject credential-bearing names; authentication belongs in
  project connections.
- MCP `require_approval` defaults to `always` and supports non-overlapping
  per-tool `always`/`never` filters.
- Prompt and Hosted Responses smoke tests continue approval requests only for
  exact operator allowlist entries, reject unmatched calls only when explicitly
  requested, and cap continuation rounds. An `--approve-mcp-tool` entry trusts
  that exact server/tool pair for the bounded invocation chain; it is not an
  argument hash or a replay of a previously displayed request.
- Rooted, size-bounded reads for all manifest-referenced files.
- Streaming SHA-256 validation for grounding uploads, manager ownership
  metadata, desired-state hashes, bounded indexing polls, and fail-closed
  logical-name resolution.
- Central credential redaction before any receipt or diagnostic write.
- Terminal receipts are redacted before optional Azure Monitor upload; only
  manager v1/v2 schemas are accepted for manual retry, with a 1 MiB bound.
- Stable error kinds and exit codes so automation can fail closed on `4`
  (security) without string matching.
- Local validation of destructive inputs (for example `prune --keep >= 1`) before
  any Azure call.
- AzureCloud capability gating for Hosted Agents before executable lookup.
- Exact `azd`/extension version and command-contract checks before Hosted Agent
  provisioning or deployment.
- Recursive, contained Hosted Agent `$ref` resolution with cycle, depth,
  file-count, per-file, and aggregate-size limits.
- Rejection of local-execution `hooks` and host-pinning of existing Foundry
  project endpoints.
- Narrow canonical `azd env get-value FOUNDRY_PROJECT_ENDPOINT` lifecycle
  resolution with an `AZURE_AI_PROJECT_ENDPOINT` compatibility fallback,
  revalidation before token acquisition, idempotent state checks, and
  post-mutation state reconciliation.
- Bounded subprocess output, an operator-configurable total Hosted operation
  deadline, and no shell execution for generic Hosted Agent workflows.
- Draft version routing rejection prevents preview artifacts from serving
  production traffic.
- Session file remote paths reject absolute paths, parent traversal, and
  unsafe characters; upload and download use workspace-rooted containment.
- Code archive determinism (sorted entries, fixed timestamps, normalized
  permissions) and SHA-256 integrity headers for draft deployment.
- Environment variable references resolved individually with value-level
  secret registration, never bulk environment retrieval.
- AzureCloud-only capability gating for Toolboxes, immutable Toolbox versioning,
  explicit promotion, default-version deletion refusal, and v2 receipts.

Fuzz targets specifically guard the approval and pinning logic:
`FuzzValidateHTTPSHostAcceptsOnlyAllowedHosts`,
`FuzzAPIMTargetNeverCrossesCloudBoundaries`,
`FuzzRelativeFileReferenceStaysInsideTheManifestDirectory`,
`FuzzHostApprovalNeverOverMatches`, `FuzzApprovalParsingRejectsAmbiguousValues`,
and `FuzzAudienceApprovalNeverOverMatches`.

## Destination approvals

Full semantics are documented in
[Destination trust approvals](docs/security-and-operations.md#destination-trust-approvals).
Security essentials:

- Approvals exist on `preflight`, `deploy`, `toolbox-deploy`, and
  `connector-toolbox-deploy`. Offline validation and planning report structure
  only; never read their success as a trust decision.
- Approvals are exact `host` or `host:port`. No wildcards, no suffixes, no URLs,
  no non-ASCII.
- An omitted port and `:443` are equivalent; any other port must match exactly.
- Approvals never widen cloud suffix checks or unsupported-cloud rejection.
- Approvals are never read from a manifest and never written to a receipt.
- `--trust-file`/`FOUNDRY_AGENT_MANAGER_TRUST_FILE` load the same three approval
  categories from a JSON/YAML file, validated with the identical rules and
  merged with the flags above; see
  [Trust policy file](docs/security-and-operations.md#trust-policy-file). The
  file path itself is operator/CI-supplied and is still subject to the same
  trust boundary — it is never derived from the manifest.
- `FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES` widens only the *suffix* allow-list. It
  is an additional boundary, never a replacement for an exact approval.

Failure semantics you can rely on in automation:

| Situation | Exit |
|---|---:|
| Destination not approved, or approval too broad (wildcard/suffix/non-ASCII) | `4` |
| Approval value malformed (URL form, path/query/fragment, empty, `/.default` audience) | `3` |
| Trust policy file missing, unreadable, invalid JSON/YAML, or malformed | `3` |

## Secret handling

- The only secret this tool handles directly is the **APIM subscription key**.
  Azure access tokens are obtained by the Azure SDK and never persisted.
- Sources are mutually exclusive: direct value, file, stdin, Key Vault, or an
  environment variable (default `FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY`).
- **Host approval happens before secret resolution.** A hostile manifest cannot
  cause a key to be loaded for an unapproved gateway.
- The secret is registered with the receipt store before the first write, so no
  step, error, or ARM diagnostic can persist it.
- Redaction covers the raw value plus its JSON-escaped, query-escaped, and
  path-escaped encodings. Values shorter than 4 characters are not redacted,
  because replacing them would corrupt unrelated diagnostics without protecting a
  meaningful credential — do not use trivially short keys.
- Key Vault retrieval is cloud-scoped and refuses redirects.

**Preference order for operators:** Key Vault or a protected CI secret >
environment variable > file > stdin > command-line flag.

`--apim-subscription-key` puts the key in process arguments, where it is visible
to other users on the host, to process listings, to shell history, and often to
CI logs. Preflight raises an `apim-secret-safety` warning when it is used. Rotate
any key that has been passed this way.

## Azure cloud boundary

- AzureCloud is the only supported cloud.
- Azure Government aliases fail with a `config` error (exit `3`) during cloud
  resolution, before endpoint construction, credential acquisition, trust
  approval, secret resolution, or network access.
- Unsupported cloud input never falls back to AzureCloud automatically.
- The AzureCloud profile rejects endpoint and managed-identity audience hosts
  that belong to another Azure cloud before any operator approval is considered.
  These defensive checks prevent cross-cloud credential or data egress and do
  not imply support for the rejected cloud.
- Azure Government must not be re-enabled until a dedicated subscription is
  available for complete live lifecycle, security, and cleanup qualification.

## Secure deployment guidance

1. **Review the manifest as code.** Require review for changes to `apim.target`,
   `apim.gateway_url`, direct or Toolbox tool endpoints, OpenAPI `servers`,
   A2A `agent_card_path` and `send_credentials_for_agent_card`, `spec_file`,
   grounding document paths, project connection identifiers, and any
   `audience`.
2. **Keep approvals out of the repository the manifest lives in**, or at least
   under different reviewers. An author who can edit both the manifest and the
   approval list has defeated the control.
3. **Store approvals in a protected CI environment**, not in variables a pull
   request can modify.
4. **Run `preflight` before `deploy`** in a separate, non-mutating job.
5. **Use `--output json` and branch on `exitCode`.** Treat exit `4` as a security
   stop, not a retryable error.
6. **Prefer `apim.auth: managed_identity`** where the APIM API can accept the
   project identity: it removes the subscription key entirely and stays
   restorable during rollback.
7. **Use `--if-changed`** to avoid redundant deployment work when the desired
   snapshot, successful receipt, and verified remote version still match.
8. **Archive receipts** with your deployment logs. They are the only durable
   record of what was mutated.
9. **Do not run concurrent deployments** against the same project or connection,
   especially with `--allow-unconditional-shared-rollback`.
10. **Grant least privilege.** The deploying identity needs data-plane access to
    the Foundry project, and control-plane rights only for `project-create`,
    the optional `--ensure-project`, and APIM connection operations.
11. **Verify downloaded binaries** with `SHA256SUMS` and, when available, the
    published build provenance attestation; otherwise build from source.
12. **Treat Hosted Agent source as deployment code.** Review `azure.yaml`,
    Dockerfiles, package manifests, startup commands, `.agentignore`, and every
    local `$ref` before `hosted-deploy`.
13. **Treat grounding cleanup as global deletion.** Use
    `--delete-replaced-uploads`, `--delete-pruned-uploads`, `--delete-upload`,
    or `--delete-uploads` only after confirming the project file is not shared.

## Known and accepted operational risks

These are documented, opt-in, or externally imposed. They are not treated as
vulnerabilities on their own.

| Risk | Why it exists | Mitigation |
|---|---|---|
| `--allow-unconditional-shared-rollback` can revert or delete a shared APIM connection or project that another run legitimately changed | ARM exposes no documented conditional concurrency token for these resources | Off by default; the default is manual reconciliation. Never combine with concurrent deployments. |
| `--rollback-created-project` can delete a project | Same missing concurrency token | Requires the unconditional flag as well; off by default. |
| `--allow-nonrestorable-apim-update` accepts an unrecoverable prior state | Azure commonly does not return an existing API-key connection's secret | Off by default; deployment refuses the update without it. Prefer managed identity. |
| `--apim-subscription-key` exposes a secret in process arguments | Retained for compatibility | Preflight warns; use another source and rotate exposed keys. |
| MCP `require_approval: never` removes per-call human approval | Some servers are fully trusted and automated | Defaults to `always`; `never` must be written explicitly. |
| Preview API surfaces (`2025-04-01-preview` connections, RAI preview header) can change or be unavailable | Upstream service contracts | Pin API versions in the manifest when you need stability and requalify after upstream contract changes. |
| Approved external tool services can mishandle agent data | Outside this tool's control | Approve only hosts you own or have vetted contractually. |
| Smoke tests are billable model invocations | They exercise the real endpoint | Opt-in via `--smoke-test` or the `smoke` command. |
| Receipts contain resource identifiers, endpoints, and hashes | They must be useful for reconciliation | Store them with the same controls as deployment logs. They contain no credentials. |
| `--allow-active-apim-update` lets `deploy` update a shared APIM connection while it is serving the active version | The connection can be shared across every version of an agent; there is no version boundary to protect production traffic once it is updated | Off by default; `deploy` refuses and suggests a dedicated `apim.connection_name` instead. Review the change's impact before using the override. |
| `--allow-bot-update` lets `publish-m365` change an existing Azure Bot Service's identity, tenant, or messaging endpoint | The bot may already be serving a live Microsoft Teams channel | Off by default; `publish-m365` refuses the change and reports the conflicting field(s) without it. |
| `microsoft365.bot_service.allow_update: true` grants the same Bot Service update permission from the publication file | Protected automation may prefer reviewed configuration over a CLI flag | Treat publication files as security-sensitive code and require the same review controls as manifests. |
| `publish-m365`'s publish POST is never retried after an ambiguous outcome, so a transient failure may require manual reconciliation instead of an automatic retry | Retrying a publish request whose prior outcome is unknown risks a duplicate or conflicting Microsoft 365 publication | By design; the receipt's external action records `unknown` status and reconciliation guidance instead of retrying. |
| Promoting or rolling back to a historical immutable version reactivates the tools and destinations captured in that version | Release routing does not rebuild or rewrite immutable historical definitions | Inspect the target with `show --agent-version`, confirm its destinations remain approved, and use protected release approvals before moving traffic. |
| `--accept-preview` and `--approve-sample-commit` unlock the experimental Autopilot wrapper, which runs `azd provision` against a third-party sample outside this tool's control | The workflow is explicitly unsupported and pinned to one reviewed commit | Both flags are mandatory and off by default; only the one pinned, reviewed commit is accepted; the workflow runs only in AzureCloud. |
| `--accept-preview` unlocks generic Hosted Agent orchestration through preview `azd` tooling | Hosted Agent schemas and command behavior can change before GA | Exact extension pinning, help-contract checks, AzureCloud-only gating, explicit `--provision`, and atomic command receipts. |
| A Hosted Agent container or source build executes untrusted build scripts remotely | Package managers, Dockerfiles, and build hooks are part of the agent source | Require code review, lock dependencies, prefer direct code only when remote dependency resolution is acceptable, and use `bundled` or a reviewed image for reproducibility. |
| `--provision` executes workspace-controlled infrastructure configuration through azd | Bicep or Terraform files can change Azure resources and may invoke their own local tooling | Off by default. Review `azure.yaml`, `infra/`, modules, providers, and provisioner behavior before explicitly enabling it. Hosted YAML hooks remain prohibited. |
| Hosted deployment may create a new immutable version without proving stable-endpoint routing | `azd ai agent show` reports a version, but not the complete endpoint selector contract | Receipts record the created version and intentionally leave `activeVersionAfter` empty; inspect endpoint routing separately before production use. |
| Hosted Toolbox approval depends on the Responses client | Agent Framework can emit an MCP approval request, but the caller must return the approval response | Generated code defaults to `always_require`; use `hosted-smoke --approve-mcp-tool <server>/<tool>` for exact reviewed calls, and implement the same round trip in other clients. |
| Static MCP headers accidentally contain reusable credentials | Manifest content is untrusted and is persisted in source control | Authorization, cookie, API-key, APIM subscription-key, and Azure Functions key header names are rejected. Use `project_connection_id`; only non-secret routing/context headers are accepted. |
| API Center discovery requests a guessed Entra audience | API Center registry authentication configuration is deployment-specific | Anonymous access is the default. Authenticated discovery requires an explicit `--api-center-token-scope`; the CLI never invents one. |
| A portal-only Logic Apps workflow is presented as automated | Microsoft documents non-OAuth2 registration through preview Foundry/Azure portal steps, not a public mutation API | `logicapps-registration-plan` returns `automated: false`, validates the worksheet, and collects no static User parameter values. |
| Hosted Bing Grounding resolves a project connection and invokes a service outside the Azure compliance boundary | The Agent Framework integration runs inside custom Hosted application code and the manager cannot inspect each runtime query | Review the generated code, grant the Hosted identity only the required project access, avoid sensitive input, and do not deploy this integration behind VPN or private endpoints. |
| A Toolbox default promotion changes every unversioned consumer | The logical Toolbox endpoint follows `default_version` | Inspect staged versions first, pin critical consumers to an immutable version when needed, and require protected promotion approval. |
| Toolbox skills and selected tool types use preview contracts | Upstream feature headers and wire formats can change | `toolbox-deploy --accept-preview` is mandatory when preview capabilities are present; update Foundry Agent Manager only after contract review. |
| Memory and Skills use preview APIs | Their schemas, behavior, retention, and availability can change | Every online command requires `--accept-preview`; mutation receipts preserve reconciliation evidence. |
| Memory stores retain extracted user information | Long-term personalization inherently retains content and summaries | Use isolated, non-guessable scopes, define retention externally, review extracted memories, and run `memory-scope-delete` when erasure is required. |
| A generic connection credentials JSON file contains unsupported or malformed auth fields | Public data-plane APIs expose read operations but not generic CRUD; ARM auth shapes vary by connection type | The CLI sends the operator-reviewed JSON object without inventing an auth schema, never prints it, and surfaces the ARM validation error. Prefer the dedicated API-key shortcut or managed identity where possible. |
| A protected A2A agent card receives credentials for the wrong principal or host | Project connections can authenticate as the agent identity, project managed identity, or a delegated user, and agent-card discovery has a separate credential-send switch | Keep `send_credentials_for_agent_card: false` unless the card is protected and same-host. Inspect the connection auth mode, approve every absolute card host, and grant downstream RBAC only to the principal that receives the token. |
| A managed connector publisher or downstream service handles data differently from Foundry | Connector actions can transmit prompts, parameters, and retrieved data to Microsoft or non-Microsoft systems | Review the catalog publisher tier and service terms, select the minimum actions, use least-privilege OAuth consent, and require `--accept-preview` for every connector command. |
| Updating a managed connector silently removes previously registered actions | The service replaces `mcpserverConfigProperties` wholesale | `connector-configure` is declarative: pass the complete intended operation set, and use `--yes` when replacing existing actions. |
| Replaced grounding uploads remain in the project by default | A project file may be referenced by another vector store, so automatic global deletion is unsafe | Retention is the safe default; use `--delete-replaced-uploads --yes` only after checking sharing. |
| Grounding status and logical deployment resolution require local documents | Desired state is content-addressed and cannot be proven from path names alone | Keep the manifest-relative documents available in CI and deployment environments. |
| Foundry controls document parsing, chunking, embedding, token limits, and retrieval quality | Those behaviors are service-owned rather than configured by this CLI | Validate source quality in Foundry and evaluate the deployed agent; the CLI proves upload/index state, not answer quality. |
| Hosted draft deployment may create a regular version if drafts are not enabled for the subscription | Foundry ignores the `draft: true` flag on subscriptions that have not opted in | The command detects this, records `created-as-regular-version`, and fails partial so the operator can inspect and route or delete the version. |
| Deleting a Hosted Agent terminates active sessions | `hosted-delete` warns that active sessions will be terminated | Confirmation is required; `--dry-run` previews the impact. |

## Out of scope

- Provisioning or hardening of the Foundry account, APIM service or policies,
  unmanaged vector stores, RAI policies, Azure Functions, storage, Bing/Search
  services, Container Apps Dynamic Sessions, or other downstream services
  referenced by a project connection.
- Cross-account/cross-tenant fine-tuned model source authorization and
  provider-specific Marketplace purchase terms; the model deployment workflow
  manages only fields covered by its stable generic ARM contract.
- Foundry's document parsing, chunking, embedding model, retrieval ranking, or
  grounding-answer quality.
- Runtime behavior, content safety, or data handling of a deployed agent.
- Security of external OpenAPI or MCP services you approve.
- Trustworthiness of Memory content or uploaded Skill instructions.
- Security, retention, availability, or correctness of managed connector
  publishers and downstream services.
- Execution of Function Calling handlers or Hosted Toolbox approval loops
  outside the CLI's bounded `hosted-smoke` continuation.
- Azure RBAC design and identity governance for the deploying principal.
- Availability guarantees for any preview Azure API surface.
- Installing, upgrading, authenticating, or scaffolding Azure Developer CLI and
  its Foundry extensions.

## Reporting a vulnerability

**This repository is private.** It is not publicly visible, so only invited
collaborators can see it at all — there is no "public issue" risk of the kind
that applies to open-source repositories.

GitHub's native **Private vulnerability reporting** ("Report a vulnerability" on
the Security tab) requires GitHub Advanced Security on private repositories,
which is a licensed feature not currently enabled for this repository
(confirmed: `PUT /repos/.../private-vulnerability-reporting` returns `404` here).
If GitHub Advanced Security is licensed for this repository in the future, the
owner should enable it from **Settings → Security → Private vulnerability
reporting** as an additional, more discoverable channel.

Until then, use this instead:

1. **Open a regular GitHub issue in this repository.** Because the repository is
   private, this is already restricted to repository collaborators — it is the
   primary supported reporting path today.
2. If you are not a collaborator and cannot open an issue, contact the repository
   owner (`jpmicrosoft`) directly through a channel you already have. Do not
   guess at an email address, and do not describe the issue in an unauthenticated
   or public channel.

Please include:

- affected version (`foundry-agent-manager version` output) and commit;
- operating system and architecture;
- the exact command line, with secrets and real hostnames redacted;
- a minimal manifest that reproduces the problem;
- observed behavior, expected behavior, and the exit code;
- impact assessment, especially whether a credential, token, or agent data could
  reach an unapproved destination.

There is no published response-time commitment for this repository. Do not
disclose publicly until you have agreed on a timeline with the owner.

Findings that are **not** vulnerabilities here: the documented opt-in risks in
[Known and accepted operational risks](#known-and-accepted-operational-risks),
missing features, and behavior of external Azure or third-party services.
