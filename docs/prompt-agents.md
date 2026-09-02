# Prompt Agents

Complete reference for Foundry Prompt agent manifest authoring, deployment,
and lifecycle management with `fam`.

## Why use the Prompt Agent path

Use Prompt Agents when the desired behavior can be expressed as instructions,
a model deployment, and declarative tools without packaging a custom hosted
runtime. The manager gives teams:

- A YAML or JSON source of truth that can be reviewed and versioned in Git.
- Offline validation and planning before Azure credentials are needed.
- Read-only preflight before a mutation is attempted.
- Immutable agent versions, remote drift inspection, and explicit promotion or
  rollback of stable endpoint traffic.
- Redacted receipts that record what a mutating command changed.
- Separate lifecycle commands for projects, connections, grounding, Toolboxes,
  Skills, Memory, publishing, and compatibility resources.

The parent Foundry account and model deployment remain externally managed. The
manager can create a child project, but it does not conceal account ownership,
model quota, cost, or RBAC decisions.

## RBAC and separation of duties

- Local init, validation, and planning require no Azure role.
- Prompt lifecycle and project data-plane operations normally use `Foundry
  User` scoped to one project.
- `prompt deploy --ensure-project` additionally requires child-project
  creation access. Managing or removing a manifest-defined APIM project
  connection additionally requires project connection write/delete access;
  `Foundry Project Manager` is the purpose-built project role for both
  operations. The CLI's project creation path also needs parent-account read
  access to confirm the account region.
- Standalone `prompt preflight --ensure-project` remains read-only but needs
  account/model read access when the child project does not exist.
- Endpoint-only callers should use `Foundry Agent Consumer` at project or agent
  scope instead of a developer role.
- Child-project and model-deployment administration are separate
  management-plane duties.
- `prompt m365 publish` requires `Foundry User` on the project plus `Azure Bot
  Service Contributor Role` on the target resource group. Azure `Contributor`
  or `Owner` are broader Bot Service alternatives. The CLI also reads the
  Microsoft.BotService provider registration, which requires
  `Microsoft.Resources/subscriptions/providers/read` at subscription scope.
- Publishing an Agent Application is a separate workflow that requires
  `Foundry Project Manager`; direct Agent Application invocation requires
  `Foundry User` or a custom invocation role because `Foundry Agent Consumer`
  covers direct agent endpoints only.
- Runtime access belongs to the agent or project identity selected by the
  connection mode, not automatically to the human deployer.

See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md#prompt-agents)
for the complete Prompt, project, model, publication, and runtime matrix.

## Manifest reference

The manifest turns agent intent into a repeatable contract instead of a set of
portal edits. The embedded schema rejects unknown fields so spelling mistakes
and unsupported configuration fail before deployment.

The canonical contract is the embedded JSON Schema at
[`../schema/manifest.schema.json`](../schema/manifest.schema.json). Unknown properties
are rejected at every level.

A minimal manifest:

```yaml
apiVersion: foundry-agent-manager/v1

agent:
  name: sample-agent
  model: <model-deployment-name>
  instructions: |
    Answer clearly and only from the information available to you.

project:
  resource_id: /subscriptions/<subscription-uuid>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<account>/projects/<project>

tools:
  - type: code_interpreter
```

### Top-level sections

| Section | Required | Notes |
|---|---|---|
| `apiVersion` | yes | Must be exactly `foundry-agent-manager/v1`. |
| `cloud` | no | `AzureCloud` (default and only supported value). |
| `agent` | yes | `name`, `model`, `instructions`; optional `description`, `metadata`, `rai_policy_id`, and `structured_inputs`. |
| `project` | no* | Coordinates for the Foundry project. *Required in practice for any online command. |
| `endpoint` | no | Desired stable-endpoint protocols, authorization schemes, and agent card. Never controls version routing. |
| `apim` | no | Optional connection to an existing APIM API. |
| `tools` | no | Tools attached directly to the prompt agent, including an existing Toolbox attachment. |
| `grounding` | no | Manager-owned document vector stores synchronized separately under `grounding`. |
| `memory_stores` | no | Preview Memory store definitions synchronized separately under `memory store`; prompt-agent tools reference them by name. |
| `toolboxes` | no | Reusable Toolbox definitions managed separately under `toolbox`. Prompt-agent `prompt deploy` does not create them. |

`agent.rai_policy_id` must be the full resource ID of an RAI policy that already
exists on the same Foundry account as the project. It is referenced, never
created. `prompt preflight` verifies the policy through ARM before deployment.
When this field is omitted, the Prompt Agent inherits the model deployment's
guardrail.

`agent.metadata` is an optional map of custom non-secret strings:

```yaml
agent:
  name: sample-agent
  model: <model-deployment-name>
  metadata:
    owner: platform-team
    environment: production
  instructions: Be helpful.
```

The map is managed as part of the immutable Prompt Agent version. A metadata
change is visible to `prompt diff` and causes `prompt deploy --if-changed` to
create a new candidate version. Repeatable `--metadata key=value` values
override matching manifest keys. The same resolved map is recorded in receipts
and, when configured, the Log Analytics `Metadata` dynamic column.

When neither `agent.metadata` nor `--metadata` is supplied, metadata is
unmanaged: existing values on the latest Prompt Agent version are ignored for
drift and preserved if another managed change creates a new version. Set
`metadata: {}` explicitly to clear existing metadata.

Foundry limits the map to 16 entries, keys to 64 characters, and values to 512
characters. Metadata is visible outside the local process and must not contain
tokens, passwords, connection strings, or other secrets.

### Project endpoint resolution

All project coordinates are derived from `project.resource_id`:

- **Subscription ID** — extracted from the ARM path
- **Resource group** — extracted from the ARM path
- **Account name** — extracted from the ARM path
- **Project name** — extracted from the ARM path
- **Account endpoint** — `https://<account>.services.ai.azure.com`
- **Project endpoint** — `https://<account>.services.ai.azure.com/api/projects/<project>`

No network calls are made to derive these values. The resource ID is validated for
correct provider (`Microsoft.CognitiveServices`), valid UUID subscription,
non-empty segments, absence of control characters, and no extra path segments.

Every derived endpoint is revalidated against the selected cloud's allowed
suffixes before a Foundry token is sent.

### CLI overrides

Available on every manifest command and applied **before** schema and endpoint
validation:

```text
--name  --model  --description  --instructions-file  --project-resource-id
--location
```

`--instructions-file` is read through rooted containment: relative to the real
manifest directory, no absolute paths, no drive-letter paths, no `..`, no
symlink or junction escape, and bounded in size.

### Example manifests

| File | Purpose |
|---|---|
| [`../examples/agent.example.yaml`](../examples/agent.example.yaml) | Minimal public-cloud deployment. |
| [`../examples/agent.base.example.yaml`](../examples/agent.base.example.yaml) | Shared manifest driven by CLI overrides. |
| [`../examples/agent.full.example.yaml`](../examples/agent.full.example.yaml) | Every supported public-cloud declarative tool, plus an `endpoint` section. |
| [`../examples/agent.grounding.example.yaml`](../examples/agent.grounding.example.yaml) | Managed document upload, indexing, and logical File Search attachment. |
| [`../examples/agent.toolbox.example.yaml`](../examples/agent.toolbox.example.yaml) | Reusable Toolbox lifecycle, Tool Search, a skill reference, and prompt-agent attachment. |
| [`../examples/agent.apim.example.yaml`](../examples/agent.apim.example.yaml) | Existing APIM gateway integration (public cloud). |
| [`../examples/publication.example.yaml`](../examples/publication.example.yaml) | Microsoft 365 publication configuration for `prompt m365 publish --publication`. |

## `prompt init`

Use `prompt init` to avoid starting from a blank file. It writes a starter manifest,
adds safe defaults, and validates the result against the embedded schema:

```powershell
fam prompt init -f agent.yaml --name support-agent --model gpt-4o \
  --project-resource-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/contoso/projects/support
```

Seed values come from `--name`, `--model`, `--description`,
`--instructions-file`, `--project-resource-id`,
and `--location`. `--cloud` accepts
only `AzureCloud`; `--no-tools` omits the default `code_interpreter` tool;
`--force` allows overwriting an existing file.

`quickstart` and the minimal `prompt init` form assume the named Foundry child
project and model deployment already exist. If the project is missing, the
target project resource ID is still deterministic; `project create` will create it
under the parent account. `project.location` is required only by
`project create` and defaults to the Foundry account's region.

Both commands accept optional `--guardrail-policy-id <full-resource-id>`.
Omitting the flag preserves model-policy inheritance; Prompt Agents do not use
Hosted's `--no-guardrail` flag.

## `project create`

Creates the manifest's Foundry **child project** under an existing Foundry
account without deploying an agent. This separates project/RBAC readiness from
agent deployment, making failures easier to diagnose and the project reusable
across later deployments:

```powershell
fam project create -f agent.yaml
```

The manifest must provide `project.resource_id` (the target project resource ID).
The parent account is derived from the project ID. `project.location` is
required for creation and defaults to the Foundry account's region.
The command is idempotent: an existing project is inspected rather than replaced.

## Model deployment lifecycle

Model deployment ownership is explicit and separate from Prompt Agent
deployment. `prompt deploy` requires `agent.model` to exist and never creates,
resizes, replaces, or deletes a model deployment as a side effect.

To manage an account-scoped deployment, provide the project resource ID (from
which account coordinates are derived) and desired model state:

```yaml
agent:
  model: support-prod

project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry/projects/support

model_deployment:
  deployment_name: support-prod
  model_name: gpt-5-mini
  model_version: "2025-08-07"
  model_format: OpenAI
  sku_name: GlobalStandard
  capacity: 10
```

Run the live read-only gate before mutation:

```powershell
fam model deployment list -f agent.yaml
fam model deployment plan -f agent.yaml
fam model deployment create -f agent.yaml
fam prompt preflight -f agent.yaml
```

Planning checks the exact account and regional model catalogs, SKU constraints,
quota, regional placement capacity, and optional RAI/spillover dependencies.
Create is idempotent only for an exact `Succeeded` match. Existing drift fails
closed; replacement requires an explicit delete and recreate decision.

```powershell
fam model deployment show -f agent.yaml
fam model deployment delete -f agent.yaml --dry-run
fam model deployment delete -f agent.yaml --yes
```

Hosted/azd-managed model deployments remain declared under the applicable
`azure.yaml` service `deployments[]` and are provisioned through explicit
`azd provision`; do not split ownership between both workflows accidentally.

## Structured inputs

Structured inputs let one immutable agent version accept typed values at
invocation time instead of hardcoding user-, tenant-, or environment-specific
data into instructions. Defaults make common calls simpler while required
schemas give callers a contract they can validate.

`agent.structured_inputs` declares typed runtime variables in the immutable
agent definition. Each key can define `description`, `schema`, `required`, and
`default_value`; templates in instructions and supported tool fields use
`{{variableName}}`. Foundry requires every optional input to define
`default_value`, so set `required: true` or provide a default that satisfies the
declared schema.

```yaml
agent:
  name: inventory-agent
  model: <model-deployment-name>
  instructions: Summarize stores {{storeIds}} for region {{region}}.
  structured_inputs:
    storeIds:
      required: true
      schema:
        type: array
        items: {type: string}
    region:
      default_value: west
      schema: {type: string}
```

## Preflight

Use preflight as the deployment go/no-go gate. It performs the checks that can
be completed safely before version creation, so authentication, project,
model-deployment, secret, tool, and destination-approval problems fail without
leaving a new agent version behind.

`prompt preflight` performs all local work before any Azure mutation:

1. Load CLI overrides and validate the canonical schema.
2. Resolve and parse contained instructions and OpenAPI files.
3. Build every Foundry tool payload.
4. **Enforce operator approval of every external destination.**
5. Resolve the selected Azure cloud and the APIM secret source.
6. Check ARM project state when coordinates are available.
7. Probe the Foundry data plane for existing projects.
8. Read the exact `agent.model` deployment from the selected Foundry project.
9. Validate and inspect the optional APIM project connection.

The model check uses Foundry's read-only deployment resource and does not send
a prompt or consume inference tokens. A missing deployment, a name mismatch,
an authorization failure, or an invalid service response fails preflight. When
`--ensure-project` targets a missing child project, preflight verifies that the
deployment exists and has succeeded on the parent account through ARM; deploy
checks project-scoped accessibility again after creating the child project.

## Deploy, receipts, and recovery

This workflow gives teams a reviewable difference before mutation, avoids
creating duplicate versions, and records enough redacted state to distinguish a
clean failure from one that needs reconciliation.

### Change-aware deployment

```powershell
fam prompt diff -f agent.yaml --output json
fam prompt deploy -f agent.yaml --if-changed
```

`prompt diff` compares only the fields this tool manages: description, prompt kind,
model, instructions, tools, RAI policy reference, and non-secret APIM
connection properties.

The first deployment activates the initial version. After that, `prompt deploy` never
moves existing production traffic by itself: every new immutable candidate is
staged behind whatever version is currently active.

### Receipts

Every deploy writes an atomic JSON receipt, by default under:

```text
<manifest-directory>\.foundry-agent-manager\receipts\<timestamp>-<agent-name>.json
```

Receipts **never** contain APIM keys, Azure tokens, or trust approvals.

#### Receipt schema versions (v1 and v2)

| Schema | Written by | Notable fields |
|---|---|---|
| `foundry-agent-manager/receipt/v1` | `prompt deploy` only | `project`, `apim`, `agent`, `prompt smoke`. |
| `foundry-agent-manager/receipt/v2` | All other mutating commands | `operation`, `resources[]`, `externalActions[]`. |

### Failure, compensation, and reconciliation

On failure the manager removes **only the exact immutable agent version created
by that run**, then records remaining state for manual reconciliation.

Terminal receipt statuses are `succeeded`, `unchanged`, `failed-compensated`,
and `failed-partial`.

## Staged versions, promotion, and rollback

Staging separates “create and verify a candidate” from “send production traffic
to it.” Teams can smoke-test a new version, require an approval, and then
promote or roll back without rebuilding the agent.

| Situation | What `prompt deploy` does |
|---|---|
| Agent does not exist yet (first deployment) | Creates the first version, then pins the stable endpoint to it. |
| Agent exists and currently tracks `@latest` | Pins the stable endpoint to the current latest, then creates the candidate. |
| Agent exists and is already pinned | Creates the candidate behind that pin. |
| Agent's selector splits traffic | Refuses with a `conflict` error. |

Moving traffic is always a separate, explicit step:

```powershell
fam prompt promote -f agent.yaml --agent-version 7
fam prompt promote -f agent.yaml --latest
fam prompt rollback -f agent.yaml --agent-version 6 --yes
```

## Stable endpoint configuration

Use endpoint configuration to manage protocols, authorization, and the agent
card independently from version rollout. This avoids coupling a metadata or
access change to a production traffic change.

```powershell
fam prompt endpoint show -f agent.yaml
fam prompt endpoint configure -f agent.yaml
```

`prompt endpoint configure` applies the manifest's `endpoint` section and **never
changes which version is active**.

## Microsoft 365 and Teams publishing

`prompt m365 publish` is **AzureCloud only**.
It turns a promoted stable endpoint into a Microsoft 365/Teams channel while
keeping tenant approval and the publication configuration explicit and
separate from agent deployment.

The command uses the dedicated public publishing contract:

```text
POST /agents/{name}/microsoft365/publish?api-version=v1
```

It explicitly sends `publishAsAutopilot: false`. When first-party pages use
broader or older wording, the manager follows this dedicated REST contract
rather than treating migration guidance that describes portal-only
publication as the current automation boundary.

```powershell
fam prompt promote -f agent.yaml --agent-version 7
fam prompt m365 publish -f agent.yaml --publication examples\publication.example.yaml
```

Prerequisites: the agent is pinned to one concrete active version and has a
system-assigned `instance_identity.client_id`. FAM uses that application/client
ID as the Azure Bot Service `msaAppId`; `instance_identity.principal_id` is the
service-principal object ID used for Azure RBAC and directory correlation, not
the Bot Service application ID. A separate publication
configuration file (`foundry-agent-manager/publication/v1`) is required.

The service supports `@latest` and fixed-ratio routing, but the manager does
not publish an ambiguous production target. `prompt deploy` stages a new immutable
version, `prompt promote` makes routing explicit, and `prompt m365 publish` requires one
concrete active version.

Standard Microsoft 365 publishing reuses the modern agent's existing
`instance_identity`; it does not create a replacement downstream identity.
Keep RBAC assigned to that agent identity when its project connections use
`AgenticIdentityToken`. Tenant administration and approval remain outside this
CLI.

Prompt Agents support Agent 365 registry synchronization after standard
Microsoft 365 publication, but Prompt Autopilot publishing is not supported.
The Hosted Autopilot implementation and sample are separate and
Hosted-specific. `prompt m365 publish` is the supported Prompt publishing path.

### Agent 365 blueprint and identity inspection

Agent 365 blueprint and identity inspection is separate from Prompt deployment
and Microsoft 365 publishing:

```powershell
fam agent365 binding status -f agent.yaml
fam agent365 binding status -f agent.yaml --resolve-identity
fam agent365 binding plan `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  -f agent.yaml
```

These commands can correlate the Prompt Agent's `instance_identity`,
`blueprint`, and `blueprint_reference` with an existing Agent ID blueprint.
They do not attach the blueprint or change the agent. A blueprint is not a
Prompt manifest, and no documented Foundry mutation currently binds an
arbitrary existing blueprint to a Prompt Agent.

**Identity lifecycle note:** New-model Prompt Agents receive a unique
`instance_identity` when created, and standard Microsoft 365 publication does
not replace that identity. The CLI reports
`modern-unique-agent-identity` when the field is present. Legacy agents without
`instance_identity` use the shared project identity and are reported as
`legacy-shared-project-identity`; migrating them to a new-model agent requires
reassigning only the required downstream RBAC roles to the new
`principal_id`. Legacy Agent Applications are separate resources with separate
identities and require the same explicit migration review.

See [Agent 365 Blueprints, Identity, Integration, Observability, and Publication](agent365.md).

## Legacy Agent Application compatibility

`prompt legacy status`, `prompt legacy deploy`, and `prompt legacy delete` are **explicit,
compatibility-only** operations (AzureCloud only). Uses ARM API
`2026-05-15-preview`.

```powershell
fam prompt legacy status -f agent.yaml --application-name legacy-app --deployment-name legacy-deploy
fam prompt legacy deploy -f agent.yaml --application-name legacy-app --deployment-name legacy-deploy `
  --agent-version 7 --route --yes
fam prompt legacy delete -f agent.yaml --application-name legacy-app --deployment-name legacy-deploy `
  --application --yes
```

This surface exists only to keep existing legacy integrations working while
you migrate to stable endpoints. Do not build new integrations against it.
Legacy Agent Application publication creates a distinct application identity;
it does not preserve the modern agent's `instance_identity`. Reassign
downstream RBAC to the legacy identity when compatibility requires this path,
and move it back deliberately during migration.

## APIM connection and secret sources

The `apim` section creates a **Foundry project connection** to an API that
already exists in Azure API Management. The manager never creates, modifies, or
deletes the APIM service itself.

This lets an agent reuse an organization-owned gateway without placing the
subscription key in the manifest or receipt. Host approval occurs before the
selected secret source is read.

For `apim.auth: api_key`, choose exactly one secret source:

```text
--apim-subscription-key <value>
--apim-subscription-key-file <path>
--apim-subscription-key-stdin
--apim-subscription-key-key-vault https://<vault-host>/secrets/<name>[/<version>]
--apim-subscription-key-env <environment-variable>
```

**Host approval happens before secret resolution.** A hostile manifest cannot
cause a subscription key to be loaded for a gateway you never approved.

## Project connection lifecycle

`project connection create`, `project connection update`, and `project connection delete` manage ARM
project connections:

Use these commands to make connection changes repeatable and receipted instead
of relying on portal-only edits. Secret material remains external to the
non-secret connection definition.

```powershell
fam project connection create -f agent.yaml `
  --connection bing-grounding `
  --connection-type ApiKey `
  --target https://<service-endpoint> `
  --auth-type ApiKey `
  --secret-env BING_CONNECTION_KEY
```

## Smoke tests

Smoke tests answer a narrow operational question: can the deployed agent
respond through the expected Foundry API now? They complement validation and
preflight, but they invoke the model and therefore incur normal service cost.

```powershell
fam prompt deploy -f agent.yaml --smoke-test
fam prompt smoke -f agent.yaml --prompt "Reply with READY."
```

A smoke test sends one **billable** request through the Foundry Responses API.
