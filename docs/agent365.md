# Agent 365 Blueprints, Identity, Integration, Observability, and Publication

`fam agent365` is a separate, primarily **read-only and
plan-only** command namespace. It inspects Microsoft Entra Agent ID blueprints, identities,
and blueprint principals through documented Microsoft Graph v1.0 APIs,
correlates their identifiers with the identity fields returned by Foundry
Prompt or Hosted Agents, manages Foundry account integration logging, inspects
observability readiness, and plans publication handoff.

It does **not** deploy agent source from a blueprint. A blueprint is an identity
template, not agent code, instructions, model configuration, or an
`azure.yaml` workspace.

## Contents

- [Supported boundary](#supported-boundary)
- [Authentication and authorization](#authentication-and-authorization)
- [Separation of duties](#separation-of-duties)
- [Blueprint IDs](#blueprint-ids)
- [Blueprint commands](#blueprint-commands)
- [Blueprint owners and sponsors](#blueprint-owners-and-sponsors)
- [Blueprint identities](#blueprint-identities)
- [Identity commands](#identity-commands)
- [Blueprint principal commands](#blueprint-principal-commands)
- [Binding status and plan](#binding-status-and-plan)
- [Integration commands](#integration-commands)
- [Observability commands](#observability-commands)
- [Publication commands](#publication-commands)
- [Identity lifecycle](#identity-lifecycle)
- [Identity layering and RBAC](#identity-layering-and-rbac)
- [Graph pagination](#graph-pagination)
- [Secret handling](#secret-handling)
- [Microsoft documentation](#microsoft-documentation)

## Supported boundary

| Capability | Status |
|---|---|
| List and show Agent ID blueprints | Read-only |
| Follow blueprint pagination in the selected tenant (`--all`) | Read-only |
| Show requested and inheritable permissions | Read-only |
| Resolve permission friendly names (`--resolve-names`) | Read-only |
| Validate documented blueprint properties | Read-only |
| Show blueprint owners and sponsors | Read-only |
| Show blueprint identities | Read-only |
| List and show Agent ID identities | Read-only |
| List and show blueprint principals | Read-only |
| Show Foundry runtime/blueprint identity fields | Read-only |
| Compare an existing blueprint with a Prompt or Hosted Agent | Plan/read-only |
| Resolve identity for binding (`--resolve-identity`) | Read-only |
| Foundry account Agent 365 integration status | Read-only |
| Foundry account Agent 365 integration plan | Plan/read-only |
| Foundry account Agent 365 integration set | Mutating (ARM) |
| Hosted workspace observability status | Read-only |
| Hosted workspace observability plan | Plan/read-only |
| Publication info, plan, status, and admin handoff | Plan/read-only |
| Bind, unbind, create, update, or delete a blueprint | Unsupported |
| Generic registry mutation or arbitrary existing blueprint binding | Unsupported |

No documented Foundry mutation API currently binds an arbitrary existing Agent
365 blueprint or Agent ID to an existing Prompt Agent, Hosted Agent, immutable
version, endpoint, Agent Application, or Autopilot. The manager therefore has no
`agent365 binding create` or `delete` command and never treats a local metadata
field as a successful binding.

## Authentication and authorization

The commands use:

- Endpoint: `https://graph.microsoft.com`
- Token scope: `https://graph.microsoft.com/.default`
- Documented Graph permissions:
  - `AgentIdentityBlueprint.Read.All` — blueprints, permissions, validation
  - `AgentIdentity.Read.All` — identities
  - `AgentIdentityBlueprintPrincipal.Read.All` — blueprint principals
  - `Application.Read.All` — sponsors, friendly permission names, and
    observability app-role assignment inspection

For delegated access, a blueprint owner can read the blueprint without an
Agent ID role. A non-owner also needs the documented **Agent ID Administrator**
Microsoft Entra role. Use the global `--tenant-id` option when the blueprint is
owned by a tenant other than the credential's default tenant.

Only `AzureCloud` is supported. Redirects are refused, response bodies are
bounded, and every Graph operation is `GET`-only. The separately approved
`integration set` mutation uses ARM rather than Graph.

## Separation of duties

Agent 365 uses several independent authorization systems:

- Graph application/delegated permissions for blueprint, identity, principal,
  sponsor, friendly-name, and app-role inspection.
- The `Agent ID Administrator` Entra role for delegated non-owner blueprint
  access.
- Azure management-plane read or write access for Foundry account integration
  status and `integration set`.
- `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` access for
  Prompt or Hosted binding/publication evidence.
- The `Agent365.Observability.OtelWrite` application role on the deployed
  runtime identity.
- Downstream Azure RBAC on the project, agent, or published identity that
  receives the token.

`integration status` accepts any account-read role. `integration set` requires
both `Microsoft.CognitiveServices/accounts/read` and
`Microsoft.CognitiveServices/accounts/write`, supplied by `Foundry Account
Owner`, `Foundry Owner`, `Cognitive Services Contributor`, Azure
`Contributor`/`Owner`, or an equivalent custom role.

Do not combine these controls into one "Agent 365 administrator" identity.
See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md#agent-365)
for the per-command matrix and publication identity transition.

## Blueprint IDs

Microsoft exposes two different GUIDs:

- `--blueprint-id`: blueprint application/client ID (`appId`)
- `--blueprint-object-id`: blueprint Microsoft Entra directory object ID (`id`)

Use exactly one. The direct Graph resource path requires the object ID; when
the application ID is supplied, the manager first resolves it through the
documented blueprint list API.

## Blueprint commands

```powershell
fam agent365 info
fam agent365 blueprint list --limit 100
fam agent365 blueprint list --all
fam agent365 blueprint show `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
fam agent365 blueprint permissions `
  --blueprint-object-id 08be1f79-37a1-49c0-b444-3075e74d1e8c
fam agent365 blueprint permissions `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  --resolve-names
fam agent365 blueprint validate `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  --fail-on-invalid
fam agent365 blueprint owners `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
fam agent365 blueprint sponsors `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
fam agent365 blueprint identities `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444
```

`blueprint list` returns at most Graph's documented 100-object page and reports
`truncated: true` when another page exists. Use `--all` to follow bounded
pagination for up to 5,000 blueprints visible to the current identity. Default
text output prints one row per blueprint with its friendly display name,
application ID, and directory object ID.

`blueprint permissions` returns both:

- `requiredResourceAccess`: permissions the blueprint requests.
- `inheritablePermissions`: permissions identities created from the blueprint
  may inherit without additional consent.

Use `--resolve-names` to resolve permission GUIDs to their friendly display
names. This requires `Application.Read.All` to look up the resource
application's published permission definitions.

All three documented inheritance modes are valid and preserved:
`allAllowed`, `enumerated`, and `none`. The validator does not incorrectly
treat a deliberately enumerated or empty inheritance policy as malformed.

Validation fails for a blueprint disabled by Microsoft or an unrecognized
inheritance mode. Missing manager applications, requested permissions, or
inheritable permissions are warnings because their suitability depends on the
intended management and access model.

### Blueprint owners and sponsors

`blueprint owners` lists the directory objects that own the blueprint
application registration.

`blueprint sponsors` lists the directory objects returned by the blueprint's
sponsors relationship. This requires `Application.Read.All`.

### Blueprint identities

`blueprint identities` lists the Agent ID identities that were created from
the selected blueprint.

## Identity commands

```powershell
fam agent365 identity list
fam agent365 identity show `
  --identity-object-id 22223333-dddd-4444-eeee-5555ffff6666
```

Identity commands inspect Agent ID identity objects through Microsoft Graph.
These are the runtime identity instances created from blueprints, distinct from
the blueprints themselves. Requires `AgentIdentity.Read.All`.

## Blueprint principal commands

```powershell
fam agent365 blueprint principal list
fam agent365 blueprint principal show `
  --principal-object-id 33334444-eeee-5555-ffff-6666aaaa7777
```

Blueprint principal commands inspect the service principals associated with a
blueprint. Requires `AgentIdentityBlueprintPrincipal.Read.All`.

## Binding status and plan

Choose exactly one Foundry target:

```powershell
# Prompt Agent
fam agent365 binding status -f agent.yaml
fam agent365 binding plan `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  -f agent.yaml

# Hosted Agent
fam agent365 binding status `
  --workspace C:\src\hosted-agent --environment prod --accept-preview
fam agent365 binding plan `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  --workspace C:\src\hosted-agent --environment prod --accept-preview

# With identity resolution
fam agent365 binding status -f agent.yaml --resolve-identity
```

`binding status` shows the Foundry response's:

- `instance_identity`: runtime identity used by supported agent-native
  downstream authentication.
- `blueprint`: Foundry-managed blueprint identity metadata when exposed.
- `blueprint_reference`: the opaque blueprint reference returned by Foundry.

Add a blueprint selector to `binding status` to compare those fields with a
specific existing blueprint. Use `--resolve-identity` to look up the identity
object associated with the binding through Graph.

`binding plan` requires a selector and reports
`matched`, `not-matched`, or `insufficient-data`.

A match is **correlation evidence only**. It does not prove that this CLI
created a binding. A non-match produces a non-executable plan because there is
no supported write operation. The command does not patch metadata, call an
undocumented endpoint, or modify the agent.

## Integration commands

Integration commands manage the Agent 365 logging flag on a Foundry account.
The scope is the entire Foundry account; there is no per-project or per-agent
override. Storage follows the Entra tenant geography.

```powershell
# Check current integration state
fam agent365 integration status `
  --account-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry

# Plan a change
fam agent365 integration plan `
  --account-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry `
  --enabled=true

# Apply
fam agent365 integration set `
  --account-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry `
  --enabled=true --yes
```

Integration commands use ARM API `2026-03-15-preview` and require a single
`--account-id` flag with the full Foundry account resource ID.
`plan` and `set` require an explicit `--enabled=true` or `--enabled=false`.
`set` requires `--yes` for confirmation and supports `--if-match` for
concurrency control and `--receipt` for mutation evidence.

`set` mutates only `properties.a365LoggingEnabled` and verifies the change
with a read-back. `a365Status` is a read-only field with values `Enabled`,
`Disabled`, or `NotLicensed` and cannot be changed by the CLI.

Agent 365 collection is active only when the logging flag is `true` **and**
`a365Status` is `Enabled`. A `NotLicensed` status means the account's tenant
does not have the required Agent 365 license; enabling the logging flag alone
does not activate collection.

## Observability commands

Observability commands inspect the OpenTelemetry readiness of a Hosted Agent
workspace without modifying the workspace or assigning any roles.

```powershell
# Scan source for instrumentation evidence
fam agent365 observability plan `
  --workspace C:\src\hosted-agent

# Check deployed identity app-role assignment
fam agent365 observability status `
  --workspace C:\src\hosted-agent --environment prod --accept-preview
```

`observability plan` scans bounded regular source files while skipping `.env`
files and `a365.generated.config.json`. It emits evidence filenames, not file
contents, and looks for:

- **Microsoft OpenTelemetry Distro** (preferred): packages named
  `microsoft-opentelemetry` (Python), `@microsoft/opentelemetry` (Node.js),
  or `Microsoft.OpenTelemetry` (.NET).
- **Legacy Agent 365 observability SDK**: earlier SDK references and
  documented configuration calls.

`observability status` checks whether the deployed agent identity has the
`Agent365Observability` app role `Agent365.Observability.OtelWrite`
(ID `8f71190c-00c8-461d-a63b-f74abde9ba52`) assigned. This check requires
`Application.Read.All` to inspect app-role assignments on the identity's
service principal. The command is **read-only** and does not assign the role.

## Publication commands

Publication commands are **read-only and plan-only**. They fail closed: no
generic registry mutation or arbitrary existing blueprint binding is performed.

```powershell
fam agent365 publication info
fam agent365 publication plan -f agent.yaml
fam agent365 publication status `
  --workspace C:\src\hosted-agent --environment prod --accept-preview `
  --resolve-identity
fam agent365 publication admin-handoff -f agent.yaml
```

The Hosted executable boundary remains only the separately pinned autopilot
sample, which is not an arbitrary existing-agent publisher.

Prompt Agents support Agent 365 registry synchronization after standard
Microsoft 365 publication, but Prompt Autopilot publishing is unsupported.
Use `fam prompt m365 publish` for the standard Prompt path. Registry status has
no documented manager API and remains unverified.

## Identity lifecycle

Understanding identity lifecycle is important for RBAC planning:

- **New-model agents** receive a unique blueprint and `instance_identity` when
  created. Standard Microsoft 365 publication and Agent 365 registry
  synchronization retain that identity and its Azure RBAC assignments.
- **Legacy agents** have no `instance_identity` and can use the shared project
  identity. Migrating to a new-model agent creates a unique identity, so the
  required downstream roles must be reassigned to the new `principal_id`.
- **Legacy Agent Applications** are separate resources with separate
  identities. Replacing one with a new-model agent also requires an explicit
  RBAC migration review.
- `instance_identity.client_id` is an application/client ID, including the
  Azure Bot Service `msaAppId`; `instance_identity.principal_id` is the
  service-principal object ID used for Azure RBAC and Graph correlation.

The CLI reports `modern-unique-agent-identity` when `instance_identity` is
present and `legacy-shared-project-identity` when it is absent. Optional Graph
resolution correlates the returned principal and blueprint IDs but does not
change the lifecycle classification.

## Identity layering and RBAC

An agent can expose more than one lifecycle identity:

- Foundry project and runtime identities.
- Foundry-managed blueprint identity information.
- Legacy Agent Application identities.
- New-model agent identities created with the agent and retained through
  standard publication and registry synchronization.

These identities do not automatically share permissions. Assign Azure RBAC to
the principal that actually receives the downstream token. For example,
`AgenticIdentityToken` uses the agent `instance_identity`, while
`ProjectManagedIdentity` uses the project identity.

## Graph pagination

Paginated Graph inventory endpoints follow exact `graph.microsoft.com` HTTPS
v1.0 `@odata.nextLink` values. The manager follows up to 50 pages (5,000
results) before stopping and reporting truncation. Pagination links are
validated against the same `graph.microsoft.com` host pinning as the initial
request; unexpected hosts are refused.

## Secret handling

The manager requests only non-secret blueprint properties. It never selects,
parses, prints, or persists blueprint password, key, or federated credential
properties.

Do not point this tool at `a365.generated.config.json`. That generated file can
contain values such as `agentBlueprintClientSecret` and
`azureOpenAIApiKey`. Copy only a non-secret application or object ID into the
corresponding command flag.

## Microsoft documentation

- [List agentIdentityBlueprint objects](https://learn.microsoft.com/graph/api/agentidentityblueprint-list?view=graph-rest-1.0)
- [Get agentIdentityBlueprint](https://learn.microsoft.com/graph/api/agentidentityblueprint-get?view=graph-rest-1.0)
- [List inheritablePermission objects](https://learn.microsoft.com/graph/api/agentidentityblueprint-list-inheritablepermissions?view=graph-rest-1.0)
- [agentIdentityBlueprint resource](https://learn.microsoft.com/graph/api/resources/agentidentityblueprint?view=graph-rest-1.0)
