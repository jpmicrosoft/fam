# Hosted Agents

Complete reference for Foundry Hosted Agent validation, deployment, and
lifecycle management with `fam`.

Hosted Agents and the required **`azure.ai.agents` Azure Developer CLI
extension** are **preview features** available only in AzureCloud. All online
commands under `hosted` require `--accept-preview`.

## Tooling prerequisites

Online Hosted Agent operations require all of the following:

- The `fam` executable
- Azure Developer CLI (`azd`) 1.27.1 or later
- The `azure.ai.agents` azd extension at exactly `1.0.0-beta.13`
- A separate authenticated `azd` session for the target tenant

```powershell
azd extension install azure.ai.agents --version 1.0.0-beta.13
azd auth login --tenant-id <tenant-id>
```

Azure CLI (`az`) is not a replacement for `azd`. It is optional unless it is
the selected developer credential source for FAM or is needed for a separate
administrative procedure.

## Why use the Hosted Agent path

Use Hosted Agents when the agent needs custom application code, a container or
prebuilt image, controlled CPU/memory, or protocols that go beyond a
manifest-only Prompt Agent. The manager gives teams:

- A validated `azure.yaml` workspace as the deployable source of truth.
- An offline plan showing the exact noninteractive `azd` workflow.
- Pinned checks for the reviewed `azd` and Hosted Agent extension contract.
- Explicit separation between infrastructure provisioning and application
  deployment.
- Change-aware deploys, redacted receipts, status reconciliation, diagnostics,
  promotion, rollback, and protected deletion.
- Operational commands for Hosted versions, sessions, sandbox files, logs, and
  smoke invocations.

The tradeoff is additional responsibility: Hosted Agents remain preview, need
external `azd` tooling and an environment, and may provision billable
infrastructure only when the operator explicitly opts in.

## RBAC and separation of duties

- Local info, validation, and planning require no Azure role.
- `hosted environment create` and Hosted quickstart bootstrap mutate only the
  workspace's local azd environment state and require no Foundry RBAC role.
- Hosted inspection, sessions, files, logs, and post-setup lifecycle operations
  normally use `Foundry User` scoped to one project.
- End-to-end `hosted deploy` requires `Foundry Project Manager` on the project
  plus the ACR push/build role required by the selected image workflow.
- Endpoint-only callers should use `Foundry Agent Consumer` at project or agent
  scope.
- `hosted deploy --provision` is an infrastructure duty. Run it with a separate
  identity whose Azure role covers only the reviewed `azure.yaml`/IaC
  resources.
- Azure `Contributor` cannot create role assignments. The project identity's
  ACR pull assignment requires Azure `Owner` or `Role Based Access Control
  Administrator`, or `User Access Administrator`; the Project Manager
  allowlist does not cover ACR pull roles.
- Downstream resource roles belong to the token-receiving runtime identity.

See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md#hosted-agents)
for the complete Hosted and provisioning matrix.

## Pinned first-party contract

Pinning the contract prevents one workstation or CI runner from silently using
a different preview workflow than another. A tooling mismatch fails before
authentication or deployment rather than producing an unpredictable rollout.

| Contract | Required value |
|---|---|
| Azure Developer CLI | `1.27.1` or later |
| Hosted Agent extension | exactly `azure.ai.agents` `1.0.0-beta.13` |
| Workspace source of truth | `azure.yaml` |
| Agent service | `host: azure.ai.agent`, `kind: hosted` |
| Deployment modes | direct source code, Docker/container, or prebuilt image |
| Direct-code runtimes | `python_3_13`, `python_3_14`, `dotnet_10` |
| Dependency resolution | `remote_build` or `bundled` |
| Protocols | `responses`, `invocations`, `invocations_ws`, `a2a` |
| Resource range | `0.25`-`4` CPU and `0.5Gi`-`8Gi` memory |

## Quickstart environment bootstrap

Interactive Hosted quickstart first offers to adopt an existing Python source
folder or generate starter code. It then defaults to creating or reusing the
workspace-scoped azd environment and prompts for an existing Foundry project
resource ID, model deployment, required Azure location, and optional tenant.
It derives the endpoint and subscription, then configures the azd variables
before printing authentication/RBAC, preflight, and deployment guidance. Answer
no at the bootstrap prompt to create files only.

Non-interactive quickstart preserves the previous files-only behavior unless
`--bootstrap-environment` is explicit:

```powershell
fam quickstart --type hosted `
  --destination hosted-agent --name support-agent --environment prod `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model support-model `
  --tenant-id 00000000-0000-0000-0000-000000000000 `
  --location eastus2 --bootstrap-environment --non-interactive
```

For `quickstart`, the model flag is `--model`; `--model-deployment` belongs to
`hosted environment create`.

## Agent guardrails

Hosted quickstart, `hosted init`, and `hosted adopt` attach an agent-level RAI
policy in deployment metadata. No guardrail flag is required:

- Default: `Microsoft.DefaultV2`, derived from the Foundry project account and
  stored as `RAI_POLICY_ID` during environment bootstrap.
- Custom: `--guardrail-policy-id <full-resource-id>`. The policy must belong to
  the same Foundry account as the project.
- Explicit opt-out: `--no-guardrail`, which omits the `policies` block.

`--guardrail-policy-id` and `--no-guardrail` are mutually exclusive. Guardrails
are applied when Foundry creates the Hosted Agent version; generated or adopted
Python code is unchanged. A policy-less workspace must pass `--no-guardrail`
again to `hosted preflight`, `hosted deploy`, or `hosted draft deploy`; omission
fails closed, while using the flag with a configured policy is rejected.
Quickstart and adoption include this acknowledgement in their generated online
next commands.

All three online commands require and bind `AZURE_AI_PROJECT_ID` to the
resolved Foundry project endpoint even for an explicit policy opt-out, resolve
the effective policy, reject cross-account IDs, and verify that the policy
exists before mutation. A deploy that runs `--provision` repeats the binding
and policy checks afterward, immediately before agent deployment. Direct draft deployment
serializes the verified resource ID as `rai_config.rai_policy_name` and verifies
the created draft retained it. The manager's `DefaultAzureCredential` identity
therefore needs read access to
`Microsoft.CognitiveServices/accounts/raiPolicies` on the account; this check is
separate from azd's cached deployment identity.

## Adopt existing Python source

Use `hosted adopt` when the application code already exists and a net-new
Foundry Hosted Agent workspace is needed. Copy mode is the default: it leaves
the source unchanged and creates a new workspace whose service source is
`src/<agent-name>`.

```powershell
fam hosted adopt `
  --source C:\src\existing-python-agent `
  --destination adopted-agent `
  --name support-agent `
  --entry-point main.py `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model support-model --location eastus2 `
  --bootstrap-environment
```

Quickstart uses the same implementation:

```powershell
fam quickstart --type hosted `
  --source C:\src\existing-python-agent `
  --destination adopted-agent --name support-agent
```

Use `--in-place` only when the source folder itself should become the workspace:

```powershell
fam hosted adopt --source C:\src\existing-python-agent `
  --in-place --name support-agent
```

Adoption:

- Auto-detects `main.py`, `app.py`, `agent.py`, or one unambiguous top-level
  Python file. Use `--entry-point` to override.
- Requires `requirements.txt`, `pyproject.toml`, or `setup.py`.
- Supports `python_3_13` and `python_3_14`, with `remote_build` or `bundled`
  dependency resolution.
- Excludes `.env`, virtual environments, Python caches, local azd state,
  manager state, and Git metadata from copy mode.
- Rejects symbolic links, non-regular files, unsafe paths, oversized inputs,
  existing destinations, and an existing `azure.yaml` in in-place mode.
- Merges deployment-safe exclusions into `.agentignore` and creates
  `.env.example` when absent.
- Detects the selected protocol's Agent Framework hosting marker in the entry
  point. If it cannot prove that the source starts `ResponsesHostServer` or
  `InvocationsHostServer`, it preserves the code and emits an explicit review
  action instead of guessing how to rewrite the application.
- Can bootstrap the same workspace-scoped azd environment as Hosted quickstart,
  but never authenticates azd, assigns RBAC, provisions Azure resources, or
  deploys the agent.

The existing-project azd environment contract is:

| azd value | Derived from | Purpose |
|---|---|---|
| `FOUNDRY_PROJECT_ENDPOINT` | `--project-id` | Canonical project endpoint derived from the project resource ID. |
| `AZURE_AI_PROJECT_ENDPOINT` | `--project-id` | Compatibility alias written with the same derived endpoint. |
| `AZURE_AI_MODEL_DEPLOYMENT_NAME` | `--model` / `--model-deployment` | Model deployment injected into the generated service. |
| `RAI_POLICY_ID` | `--project-id` | Full `Microsoft.DefaultV2` policy resource ID used by default-generated Hosted workspaces. Custom policy IDs are written directly to `azure.yaml`; `--no-guardrail` omits the policy. |
| `AZURE_AI_PROJECT_ID` | `--project-id` | Full project resource ID used by azd diagnostics and bound to the resolved endpoint by Hosted preflight, deploy, and draft deploy. These online commands reject a missing or conflicting value. |
| `AZURE_TENANT_ID` | `--tenant-id` | Target tenant context stored in the local azd environment. It does not authenticate or switch azd's cached identity. |
| `AZURE_SUBSCRIPTION_ID` | `--project-id` | Subscription derived from the project resource ID. |
| `AZURE_LOCATION` | `--location` | Required location used by azd during Hosted deployment. |

All endpoint and subscription values are derived locally from the project
resource ID. No separate `--project-endpoint` or `--subscription-id` flags are needed.

Install the reviewed extension yourself; the manager never auto-installs or
upgrades it:

```powershell
azd extension install azure.ai.agents --version 1.0.0-beta.13
azd auth login --tenant-id <tenant-id>
```

Quickstart never runs `azd auth login`, assigns RBAC, provisions resources, or
deploys the agent. Before deployment, the azd identity must have
`Foundry Project Manager` on the target project.

## Workspace example

The workspace keeps application code, runtime requirements, protocols,
environment references, and resource sizing reviewable together. The manager
validates this local contract before allowing the pinned deployment workflow.

```yaml
name: support-agents

services:
  ai-project:
    host: azure.ai.project
    endpoint: https://<account>.services.ai.azure.com/api/projects/<project>

  support-agent:
    host: azure.ai.agent
    kind: hosted
    name: support-agent
    project: src/support-agent
    uses:
      - ai-project
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
      dependencyResolution: remote_build
    metadata:
      owner: platform-team
      environment: production
    protocols:
      - protocol: responses
        version: 2.0.0
    env:
      FOUNDRY_MODEL_NAME: ${FOUNDRY_MODEL_NAME}
      TOOLBOX_NAME: shared-tools
      BING_GROUNDING_CONNECTION_NAME: ${BING_GROUNDING_CONNECTION_NAME}
    container:
      resources:
        cpu: "1"
        memory: 2Gi
```

The selected `azure.ai.agent` service can contain up to 16 custom string
metadata entries. The manager validates the Foundry limits, copies the map to
operation receipts, and leaves `azd deploy` to attach it to the Hosted Agent
version. The Foundry `authors` metadata key may also use its documented string
list form. A repeatable global `--metadata key=value` value is also recorded in
the receipt; for normal azd deployments, put values that must exist on the
remote Hosted Agent in `azure.yaml`. Direct `hosted draft deploy` merges both
sources into the draft version.

Never store secrets in metadata: it can be visible in Foundry, local receipts,
and Log Analytics.

## Deployment commands

The sequence below moves from no-Azure local checks to read-only online checks
and finally to mutation. This lets users stop at the confidence level they need
without making validation itself a deployment.

```powershell
# No azd execution or authentication:
fam hosted info
fam hosted validate --workspace C:\src\hosted-agent
fam hosted plan --workspace C:\src\hosted-agent --environment prod

# One-time local azd environment setup and existing-project context:
fam hosted environment create `
  --workspace C:\src\hosted-agent --environment prod `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model-deployment support-model --location eastus2

# Read-only online checks:
fam hosted preflight --workspace C:\src\hosted-agent `
  --environment prod --accept-preview
# Add --no-guardrail only when azure.yaml intentionally has no policies block.

# Deploy into already provisioned resources:
fam hosted deploy --workspace C:\src\hosted-agent `
  --environment prod --accept-preview
# The same explicit --no-guardrail acknowledgement is required for a
# policy-less workspace.

# Provision only with explicit operator intent:
fam hosted deploy --workspace C:\src\hosted-agent `
  --environment prod --accept-preview --provision --preview-provision
```

`hosted deploy` always calls `azd deploy <service> --no-prompt`, never `azd up`.
`--provision` is off by default and is the only path that runs `azd provision`.
Provisioning is a trust decision, not just a convenience switch.

`hosted preflight` is read-only and therefore never creates an `azd`
environment. If the selected name does not exist, create it once with:

```powershell
fam hosted environment create `
  --workspace <workspace> --environment <environment> `
  --project-id <project-resource-id> `
  --model-deployment <deployment> --location <azure-location>
```

The command is idempotent and verifies the environment through `azd env list`.
It also configures any supplied `--project-id`, `--model-deployment`,
`--tenant-id`, and `--location` values through one non-interactive `azd env set`.
Project endpoint and subscription are derived from the project resource ID.
Endpoints are written to the canonical `FOUNDRY_PROJECT_ENDPOINT` value required
by azd and to the `AZURE_AI_PROJECT_ENDPOINT` compatibility alias. Direct
`azd env new <environment> --cwd <workspace>` remains an external alternative.

For an existing project, preflight also runs azd's read-only Agent diagnostics
with the same azd identity that deployment will use. An HTTP 403 therefore
fails before mutation with guidance to select the project tenant and assign
`Foundry Project Manager` on the target project. Before the first deployment,
doctor's single expected "agents have not been deployed" check does not fail
preflight. If `AZURE_AI_PROJECT_ID` is absent, azd skips its project-role check;
rerun environment creation with `--project-id` to enable it.

## Inspection and diagnostics

Use inspection commands after deployment to reconcile local intent, the last
receipt, remote versions, endpoint routing, and pinned tooling without changing
the agent. This is the fastest way to distinguish drift from a failed rollout.

### `hosted show`

```powershell
fam hosted show --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted show --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3
```

### `hosted versions list`

```powershell
fam hosted versions list --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted versions list --workspace C:\src\hosted-agent --environment prod --accept-preview --include-drafts
```

### `hosted diff`

Compares the current deployable workspace state with the last verified deployment.

### `hosted diagnose`

Reports agent state, selector mode, tooling verification, issues, warnings, and failed versions.

## Sessions and files

Sessions provide stateful Hosted execution without changing the deployed agent
version. File commands move bounded inputs and outputs through the session
sandbox while enforcing local and remote path containment.

```powershell
fam hosted session create --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted session list --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted session show --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id>
fam hosted session stop --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id>
fam hosted session delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id> --yes
```

### Session files

```powershell
fam hosted session file upload --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id> --file data/input.csv --remote-path uploads/input.csv
fam hosted session file list --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id>
fam hosted session file download --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id> --remote-path outputs/result.csv --output-file downloads/result.csv
fam hosted session file delete --workspace C:\src\hosted-agent --environment prod --accept-preview --session-id <id> --remote-path uploads/input.csv --yes
```

## Hosted Agent logs

Logs provide bounded evidence for one version and session, which makes incident
diagnosis possible without downloading an unbounded service stream.

```powershell
fam hosted logs --workspace C:\src\hosted-agent `
  --environment prod --accept-preview `
  --agent-version 5 --session-id <session-id>
```

Both `--agent-version` and `--session-id` are required. Bounded by
`--max-lines`, `--max-bytes`, and `--duration`.

## Smoke tests

Use a Hosted smoke test after deployment or promotion to prove the selected
protocol can return a response. It is an online, billable invocation rather
than a substitute for local validation.

```powershell
fam hosted smoke --workspace C:\src\hosted-agent `
  --environment prod --accept-preview --prompt "Reply with READY."
```

`--protocol` selects `responses` or `invocations`. **`invocations_ws`
(WebSocket) is explicitly not supported** by `hosted smoke`.

## Promotion, rollback, and destructive safeguards

Promotion and rollback change traffic without rebuilding code. Separate delete
and prune commands keep cleanup explicit and protect routed or latest versions
from accidental removal.

```powershell
fam hosted promote --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 5
fam hosted promote --workspace C:\src\hosted-agent --environment prod --accept-preview --latest
fam hosted rollback --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 3
```

Both route **100% of Hosted endpoint traffic** to a single version. Draft versions
cannot receive endpoint traffic.

### `hosted versions prune`

```powershell
fam hosted versions prune --workspace C:\src\hosted-agent --environment prod --accept-preview --keep 3 --yes
```

### `hosted versions delete` / `hosted delete`

```powershell
fam hosted versions delete --workspace C:\src\hosted-agent --environment prod --accept-preview --agent-version 2 --yes
fam hosted delete --workspace C:\src\hosted-agent --environment prod --accept-preview --yes
```

## Draft deployment

Drafts let teams validate a code or prebuilt-image package against the preview
service without making that version eligible for endpoint traffic.

```powershell
fam hosted draft deploy --workspace C:\src\hosted-agent --environment prod --accept-preview
```

Docker context mode is rejected. Code archives are deterministic ZIP with
SHA-256 integrity header. The command verifies and serializes the configured
RAI policy into the draft. A policy-less workspace requires
`--no-guardrail`, and the receipt records that explicit opt-out.

## Change detection (`--if-changed`)

Change detection reduces unnecessary Hosted builds and deployments while still
refusing to trust a stale receipt when the remote latest version changed.

Skips `azd deploy` when all three conditions are met:

1. A successful receipt exists matching the workspace.
2. The deployable snapshot hash matches.
3. The receipt's version matches the current remote latest version.

## Workspace scaffold (`hosted init`)

Use the scaffold to get a contract-valid starting layout with the required
source, dependency, environment, and ignore files. It accelerates local setup
without authenticating, provisioning, or hiding the generated code.

```powershell
fam hosted init --destination my-agent --name support-agent `
  --protocol responses --metadata owner=platform-team

fam hosted init --destination my-bing-agent `
  --name current-events-agent --protocol responses `
  --bing-grounding-connection bing-search

fam hosted init --destination my-tool-agent `
  --name operations-agent --protocol responses `
  --toolbox-name operations
```

Creates a validated Python Hosted Agent workspace scaffold in a **new**
directory. The command is fully offline and does not scaffold through `azd`.

Generated files: `azure.yaml`, `src/<name>/main.py`,
`src/<name>/requirements.txt`, `src/<name>/.agentignore`,
`src/<name>/.env.example`.

The manager does not create the Bing resource, connection, or Toolbox in this
offline command. Provisioning is a separate `hosted deploy --provision` step.

## Experimental Hosted-agent Autopilot

`autopilot info`, `autopilot preflight`, and `autopilot deploy` are an
**experimental, opt-in wrapper** around exactly one pinned Microsoft sample
([`a2de504ff6b69149bd40d89edd1c86dc11c6af57`](https://github.com/microsoft-foundry/foundry-samples/tree/a2de504ff6b69149bd40d89edd1c86dc11c6af57/samples/csharp/foundry-autopilot-agent)).

The wrapper provides a reproducible, reviewed entry point for evaluating that
sample; it is not a general Autopilot deployment abstraction or a supported
replacement for the Prompt/Hosted paths.

The Agent 365 support table is broader than the currently published
programmatic implementation guidance. This wrapper follows the concrete
Hosted-agent how-to and pinned Hosted sample; the table is not treated as a
stable Prompt Agent request contract.

- AzureCloud only.
- Preview and commit acceptance are both mandatory and separate.
- Required local tools: `git`, `az`, `azd`, `pwsh`, `docker`, `dotnet`.
- There is **no prompt-agent Autopilot path**. Use `prompt m365 publish`, which sends
  `publishAsAutopilot: false`, for Prompt Agent Microsoft 365 publishing.

## Agent 365 blueprint, identity, and observability inspection

The general `agent365` namespace is separate from the pinned Autopilot sample.
It can inspect an existing blueprint, identity, and principal; correlate them
with a deployed Hosted Agent; check observability readiness; and plan
publication without provisioning or modifying anything:

```powershell
fam agent365 binding status `
  --workspace C:\src\hosted-agent --environment prod --accept-preview

fam agent365 binding plan `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  --workspace C:\src\hosted-agent --environment prod --accept-preview

fam agent365 observability plan `
  --workspace C:\src\hosted-agent

fam agent365 observability status `
  --workspace C:\src\hosted-agent --environment prod --accept-preview
```

Hosted target resolution still uses the pinned preview `azd` contract and
therefore requires `--accept-preview`. The blueprint does not replace
`azure.yaml` or application source, and the plan never binds an arbitrary
existing Agent ID.

`observability plan` scans the workspace source for Microsoft OpenTelemetry
Distro (preferred: `microsoft-opentelemetry`, `@microsoft/opentelemetry`,
`Microsoft.OpenTelemetry`) or legacy Agent 365 observability SDK evidence
without reading `.env` files or secrets. `observability status` checks the
`Agent365.Observability.OtelWrite` app role assignment on the deployed identity
(read-only; it does not assign the role).

See [Agent 365 Blueprints, Identity, Integration, Observability, and Publication](agent365.md).

## Disable / Enable

Disable temporarily removes endpoint service without deleting versions, which
supports incident containment and maintenance with a reversible operation.

```powershell
fam hosted disable --workspace C:\src\hosted-agent --environment prod --accept-preview
fam hosted enable --workspace C:\src\hosted-agent --environment prod --accept-preview
```

Takes the endpoint offline or restores service without deleting the agent or versions.
