# RBAC and Separation of Duties

Use the same `fam` executable with different Microsoft Entra
identities and narrowly scoped role assignments. The CLI does not elevate the
active principal, grant Azure roles, or make `--yes` a substitute for
authorization.

This guide separates:

- the human or workload identity operating the CLI;
- the Foundry project or account identity used by the service;
- the deployed agent identity used for downstream tokens;
- Microsoft Graph and Entra governance permissions;
- Azure Monitor receipt-publishing permissions.

Do not grant one principal every permission merely because one executable
contains every command.

## Authorization planes

| Plane | Examples | Authorization model |
|---|---|---|
| Local | Files-only `quickstart`, Hosted environment bootstrap, validation, offline plans, compatibility checks | No Azure identity; bootstrap changes only workspace-local azd state |
| Azure management plane | Foundry projects, model deployments, ARM project connections, Bot Service resources, Agent 365 account logging | Azure RBAC on the exact resource or an appropriate parent |
| Foundry project data plane | Prompt and Hosted agents, Toolboxes, Skills, grounding, Memory, managed connectors, sessions | Foundry RBAC, normally scoped to one project |
| Agent endpoint | Prompt or Hosted invocation | `Foundry Agent Consumer` at project or agent scope when no development access is needed |
| Microsoft Graph and Entra | Agent ID blueprints, identities, principals, owners, sponsors, app-role assignments | Graph permissions plus any required Entra directory role |
| Downstream runtime | Storage, Key Vault, Azure AI Search, remote tools, A2A, queues | Resource-specific access granted to the identity that receives the token |
| Audit ingestion | `receipt upload` and automatic receipt publishing | Azure Monitor Logs ingestion permission on the DCR |

Key-based authentication defeats granular user RBAC because possession of the
key grants the key's authority. The manager uses Microsoft Entra credentials
for its Azure operations; keep production adoption on identity-based access.

## Foundry role reference

Microsoft's Foundry role names and role definition IDs, verified against the
official role guidance on 2026-08-14, are:

| Role | Role definition ID | Intended boundary |
|---|---|---|
| `Foundry Agent Consumer` | `eed3b665-ab3a-47b6-8f48-c9382fb1dad6` | Interact with direct agent endpoints only; it does not invoke Agent Applications |
| `Foundry User` | `53ca6127-db72-4b80-b1b0-d745d6d5456d` | Foundry account/project reads and broad project data actions for creating, testing, operating, and invoking agents; no child-project, connection, application, or model control-plane writes |
| `Foundry Project Manager` | `eadc314b-1a2d-4efa-be10-5d325db5065e` | Child-project, project connection, and Agent Application management; broad project data actions; conditional assignment of `Foundry Agent Consumer` and `Foundry User`; no parent-account read action |
| `Foundry Account Owner` | `e47c6f54-e4a2-4754-9501-8e0985b135e1` | Full `Microsoft.CognitiveServices` control-plane administration without Foundry data actions; conditional assignment of an allowlisted set of Foundry, ACR, and monitoring roles |
| `Foundry Owner` | `c883944f-8b7b-4483-af10-35834be79c4a` | Broad Foundry control-plane and data-plane authority; role assignments remain restricted by an ABAC allowlist rather than granting general RBAC administration |

Microsoft recently renamed these roles. Use the role definition IDs in
automation while the names finish propagating across tools.

The current ABAC role-assignment allowlists are:

| Assigning role | Roles it may assign |
|---|---|
| `Foundry Project Manager` | `Foundry Agent Consumer`, `Foundry User` |
| `Foundry Account Owner` / `Foundry Owner` | `Foundry Agent Consumer`, `Foundry User`, `Container Registry Contributor and Data Access Configuration Administrator`, `Container Registry Repository Writer`, `Log Analytics Reader` |

These allowlists do not include arbitrary custom roles, `AcrPull`, or
`Container Registry Repository Reader`. Also, `Foundry User` is not a
read-only auditor role: its effective definition includes broad
`Microsoft.CognitiveServices/*` data actions and account-key listing.

Role definitions and preview service operations can evolve. Treat this guide
as the reviewed baseline for this CLI version, then verify the current role
definition and any exact action/scope returned in an Azure `403` before
authoring a custom role.

### Source of truth and known documentation drift

This review uses the effective Azure built-in role definitions as the
authorization source of truth. Several Microsoft summary tables currently lag
those definitions:

- `Foundry Project Manager` includes
  `Microsoft.CognitiveServices/accounts/projects/*`, so it can create a child
  project under an existing Foundry account. A high-level matrix incorrectly
  marks project creation unavailable. When the child is missing, this CLI also
  reads the parent account to confirm its region, so `project create` adds a
  narrow `Microsoft.CognitiveServices/accounts/read` requirement that Project
  Manager does not contain.
- The current Project Manager ABAC condition permits assignment of both
  `Foundry Agent Consumer` and `Foundry User`; some guidance mentions only
  `Foundry User`.
- `Foundry Account Owner` and `Foundry Owner` have conditional role-assignment
  allowlists. They are not substitutes for Azure `Owner` or `Role Based Access
  Control Administrator` when an arbitrary or custom role must be assigned.
- Prompt publication to Microsoft 365 uses `Foundry User` plus Azure Bot
  Service permissions. Publishing an Agent Application is a different workflow
  that requires `Foundry Project Manager`.
- The end-to-end Hosted deployment workflow requires `Foundry Project Manager`
  and registry permissions even though individual agent data-plane writes are
  also granted by `Foundry User`.

When a generic matrix conflicts with a role definition and a
service-specific permission reference, use the role definition plus the
service-specific reference.

General Azure roles remain relevant:

- `Reader` supplies management-plane read access but not Foundry project data
  actions.
- `Contributor` can create and change Azure resources but cannot assign Azure
  roles and does not by itself grant Foundry data-plane access.
- `Owner` includes broad Azure resource and role-assignment authority. Reserve
  it for controlled administrative workflows.
- `Role Based Access Control Administrator` can manage Azure role assignments
  without also granting broad resource mutation. Use it when the required role
  is outside a Foundry role's ABAC allowlist. `User Access Administrator` is
  another broader access-administration alternative.

Assign at the narrowest supported scope. Agent-level role assignments are
currently evaluated only for endpoint access; they do not grant project
development or management authority.

## Recommended personas

| Persona | Typical access | Keep separate from |
|---|---|---|
| Local author or reviewer | No cloud role; offline validation and plans only | All production credentials |
| Cloud auditor | `Reader` on the relevant Azure resources; use a custom Foundry data-plane reader if remote agent configuration must be inspected without mutation | Mutation and deletion identities |
| Project creator or team lead | `Foundry Project Manager` on the existing Foundry account plus parent-account `Reader` or an equivalent account-read custom role | Account creation and model capacity |
| Prompt developer/deployer | `Foundry User` on one Foundry project | Account creation, model capacity, role assignment |
| Hosted deployer | `Foundry Project Manager` on the project plus the required ACR push/build role | Resource provisioning and unrestricted role assignment |
| Endpoint consumer | `Foundry Agent Consumer` on one project or one agent | Agent configuration and deployment |
| Infrastructure/model administrator | `Foundry Account Owner`, `Cognitive Services Contributor` for model-only control-plane management, Azure `Contributor`, or a reviewed custom role | Foundry project data and endpoint use |
| Microsoft 365 publisher | `Foundry User` on the project plus `Azure Bot Service Contributor Role` on the target resource group and provider-registration read access at subscription scope | Model/account administration |
| Agent Application publisher | `Foundry Project Manager` on the Foundry resource | Day-to-day development and account administration |
| Agent 365 governance operator | Only the required Graph permissions and Entra role | Foundry infrastructure mutation |
| Receipt publisher | `Monitoring Metrics Publisher` or a narrower custom role on one DCR | Agent deployment and production routing |
| Runtime identity | Resource-specific downstream data roles | Human operator and CI/CD roles |

A built-in Foundry data-plane reader role that cleanly separates all inspection
from all project mutation might not cover every command surface. Where the
built-in roles are broader than policy allows, create a reviewed custom role
from the documented operations and validate it in a nonproduction project.

## Requirements by tool section

### Local authoring, validation, and planning

Commands that are explicitly offline require no Azure role. Examples include
files-only `quickstart`, Prompt and Hosted validation/plan commands, `tool-catalog`,
compatibility checks, and local Toolboxes, grounding, Memory, and Agent 365
observability plans.

Interactive Hosted quickstart can create/configure the workspace-local azd
environment. This remains a local mutation and requires no Azure RBAC role; it
does not create or update Azure resources. `--tenant-id` records tenant context
but does not authenticate azd, and `--project-id` enables azd's project-role
diagnostic without assigning any role.

`doctor --online`, preflight commands, and any plan documented as online are
not offline merely because they do not mutate. Grant only the management-plane
and data-plane read permissions required by their checks.

### Projects

| Operation | Baseline access |
|---|---|
| Inspect the parent account | Azure `Reader`, `Cognitive Services User`, `Cognitive Services Contributor`, `Foundry User`, `Foundry Account Owner`, `Foundry Owner`, or another role containing `Microsoft.CognitiveServices/accounts/read` |
| Inspect the child project | Azure `Reader`, `Cognitive Services User`, `Cognitive Services Contributor`, `Foundry User`, `Foundry Project Manager`, `Foundry Account Owner`, or `Foundry Owner` at the account or an appropriate parent scope |
| Child-project ARM create/reconcile | `Foundry Project Manager` supplies the project read/write actions; this CLI additionally requires parent-account read to confirm the region. Control-plane alternatives are `Cognitive Services Contributor`, `Foundry Account Owner`, `Foundry Owner`, Azure `Contributor`/`Owner`, or a custom role containing `Microsoft.CognitiveServices/accounts/read`, `Microsoft.CognitiveServices/accounts/projects/read`, and `Microsoft.CognitiveServices/accounts/projects/write`. |
| Complete `project create` for a missing child, including parent-region and `/agents` data-plane readiness verification | Least-privilege built-in combination: `Foundry Project Manager` plus `Reader` on the parent account. `Foundry Owner` is a broader single-role alternative. A control-plane-only alternative also needs `Foundry User` or `Cognitive Services User` at a scope inherited by the project, unless the service's automatic creator assignment has completed and propagated. |
| Build inside the created project | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` on the project |

Project creation and project use are deliberately separate duties. CLI or SDK
project creation automatically grants `Foundry User` to the creator and project
managed identity only when the creator is authorized to assign that role.
Azure `Contributor` can create the child project but cannot create those role
assignments. The CLI itself does not create the role assignments and waits for
the project by listing agents on its data plane, so control-plane permission
alone is not sufficient for the full command. The CLI never creates a parent
Foundry account.

### Model deployments

| Operation | Baseline access |
|---|---|
| `model deployment list` / `show` | `Cognitive Services User`, `Foundry User`, `Foundry Project Manager`, `Foundry Account Owner`, `Foundry Owner`, or equivalent management-plane read access on the account |
| Live `plan` catalog, quota, and capacity checks | `Cognitive Services User`, `Foundry User`, or `Foundry Project Manager` for read-only planning; account-owner roles also include the reads |
| `create` / `delete` | `Cognitive Services Contributor`, `Foundry Account Owner`, `Foundry Owner`, Azure `Contributor`/`Owner`, or a model-specific custom role |
| Invoke the deployed model through Foundry | Separate Foundry data-plane access, normally `Foundry User` |

Keep model capacity and billable deployment mutation outside the Prompt or
Hosted deployment identity. A deployment pipeline should consume an approved
model deployment name rather than gain permission to create capacity.
Cognitive Services roles in this table are operation-specific account
control-plane roles; they do not replace Foundry project roles.

### Prompt Agents

| Operation | Baseline access |
|---|---|
| Local init, validation, and plan | None |
| Preflight, status, show, diff, version inspection | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` on the project |
| `prompt preflight --ensure-project` when the project is missing | Account and model-deployment read access, such as `Cognitive Services User`, `Foundry User` at account scope, `Foundry Project Manager`, `Foundry Account Owner`, or `Foundry Owner`; this command does not create the project |
| Deploy, endpoint configuration, enable/disable, promote, rollback, prune, delete | `Foundry User` at project scope is the least-privilege built-in baseline for agent-only data-plane changes; Project Manager and Foundry Owner also include the data actions |
| `prompt deploy --ensure-project` | Add the child-project creation permissions listed under Projects |
| Prompt deploy/decommission with a managed APIM project connection | Add project connection write/delete access: `Foundry Project Manager`, `Cognitive Services Contributor`, `Foundry Account Owner`, `Foundry Owner`, Azure `Contributor`/`Owner`, or an equivalent custom role |
| Invocation-only smoke or application traffic | `Foundry Agent Consumer` at project or agent scope |
| `prompt m365 publish` | `Foundry User` on the project, `Azure Bot Service Contributor Role` on the target resource group, and `Microsoft.Resources/subscriptions/providers/read` at subscription scope for the CLI's provider-registration check (for example, through subscription `Reader` or a custom role); Azure `Contributor`/`Owner` are broader Bot Service alternatives |
| `prompt legacy status` | Management-plane read access on the project/application resources |
| `prompt legacy deploy` / `delete` | `Foundry Project Manager` or `Foundry Owner` combines the needed project data-plane and child-resource writes; `Cognitive Services Contributor`, `Foundry Account Owner`, or Azure `Contributor`/`Owner` also covers the ARM writes but needs separate `Foundry User` access when the command reads the source agent |

Use separate deployment and production-routing workload identities even when
the service role technically allows both. Protected environments, PIM, and
reviewed receipts provide the additional separation that a broad data-plane
role cannot express by itself.

`Foundry Agent Consumer` covers direct agent endpoints only. A published Agent
Application uses
`Microsoft.CognitiveServices/accounts/AIServices/applications/invoke/action`;
assign `Foundry User` on the Agent Application or use a custom invocation role.
Publishing an Agent Application is distinct from `prompt m365 publish` and
requires `Foundry Project Manager` on the Foundry resource.

### Hosted Agents

| Operation | Baseline access |
|---|---|
| Local info, validation, and plan | None |
| `hosted environment create` or Hosted quickstart environment bootstrap | None; this is an explicit local azd configuration mutation, not an Azure resource mutation |
| Preflight, status, versions, diff, diagnose, sessions, files, and logs | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` on the project |
| `hosted deploy` | `Foundry Project Manager` on the project; add `Container Registry Repository Writer` or `AcrPush` on the ACR when the selected build path pushes an image |
| Draft creation, routing, enable/disable, version deletion, and agent deletion after setup | `Foundry User` is the least-privilege built-in data-plane baseline; Project Manager and Foundry Owner also work |
| Invocation-only access | `Foundry Agent Consumer` at project or agent scope |
| `hosted deploy --provision` | Azure `Contributor`/`Owner` for the reviewed resource-group resources, `Foundry Project Manager` for Hosted deployment, and the applicable ACR build/push role |

The project managed identity needs `Container Registry Repository Reader` or
`AcrPull` on the ACR. Azure `Contributor` cannot assign it, and the current
Project Manager ABAC allowlist does not include those pull roles. Provision
that assignment with Azure `Owner` or `Role Based Access Control
Administrator`, or `User Access Administrator`, preferably through a separate
infrastructure job. Remote-build and ABAC-enabled registries can require
additional ACR task/configuration roles; review the selected Hosted build mode
rather than assuming `AcrPush` is always sufficient.

### Project connections and managed connectors

| Operation | Baseline access |
|---|---|
| ARM project connection list/show | Azure `Reader`, `Foundry User`, `Foundry Project Manager`, `Foundry Account Owner`, `Foundry Owner`, or another role with project connection read access |
| ARM project connection create/update/delete | `Foundry Project Manager` at project/account scope; alternatives are `Cognitive Services Contributor`, `Foundry Account Owner`, `Foundry Owner`, Azure `Contributor`/`Owner`, or a connection-specific custom role |
| Managed connector list/show/status/actions | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` |
| Managed connector create/configure/toolbox/delete | `Foundry User` is the least-privilege built-in data-plane baseline; Project Manager and Foundry Owner also work |
| OAuth consent | The consenting user's delegated provider permissions and tenant consent policy; Azure RBAC alone is insufficient |
| API Center discovery | Anonymous unless the operator explicitly configures an API Center token audience; then the registry's own authorization policy applies |

Connection administration does not grant the deployed agent access to the
downstream system. Inspect the connection authentication mode and grant the
downstream role to the agent identity, project identity, or delegated user that
actually receives the token.

### Toolboxes, Skills, grounding, and Memory

| Tool section | Baseline online access | Additional separation |
|---|---|---|
| Toolbox lifecycle | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` | Restrict promotion and deletion to protected release jobs |
| Skill lifecycle | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` | Separate content authors from default-version promotion/deletion |
| Grounding sync/status/delete | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` | Separate document approval from upload/prune/delete authority |
| Memory store/item/search/update | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` | Restrict billable operations and personal-data deletion to approved operators |

External Storage, Key Vault, Azure AI Search, queues, remote tools, and A2A
services retain their own authorization. Grant only the needed downstream data
role to the runtime principal selected by the connection mode.

These project data-plane roles are mutation-capable. If auditors must inspect
Toolboxes, Skills, grounding, connectors, or Memory without mutation, use a
custom role containing only the required read data actions.

### Agent 365

| Operation | Required authorization |
|---|---|
| Blueprint inspection | Microsoft Graph `AgentIdentityBlueprint.Read.All` |
| Identity inspection | Microsoft Graph `AgentIdentity.Read.All` |
| Blueprint principal inspection | Microsoft Graph `AgentIdentityBlueprintPrincipal.Read.All` |
| Sponsors, friendly permission names, observability assignment inspection | Microsoft Graph `Application.Read.All` |
| Delegated non-owner blueprint access | The documented `Agent ID Administrator` Entra role in addition to the Graph permission |
| Foundry binding/publication evidence | `Foundry User`, `Foundry Project Manager`, or `Foundry Owner` on the selected Prompt or Hosted target, plus Graph access when identity resolution is requested |
| Observability assignment status | Foundry target read access plus Microsoft Graph `Application.Read.All` |
| Integration status/plan | Azure `Reader` or any Foundry role containing `Microsoft.CognitiveServices/accounts/read` on the account |
| `agent365 integration set` | `Foundry Account Owner`, `Foundry Owner`, `Cognitive Services Contributor`, Azure `Contributor`/`Owner`, or a custom role containing `Microsoft.CognitiveServices/accounts/read` and `Microsoft.CognitiveServices/accounts/write` |

Graph permissions, Entra directory roles, Azure RBAC, and the
`Agent365.Observability.OtelWrite` application role are different controls. Do
not describe any one of them as granting the others. New-model agents keep the
unique `instance_identity` created with the agent through standard Microsoft
365 publication and registry synchronization. Legacy agents can use the shared
project identity, while legacy Agent Applications have separate identities;
migration to a new-model agent creates a new principal and requires explicit
downstream RBAC reassignment.

### Receipt publishing

`receipt upload` requires `Monitoring Metrics Publisher` on the DCR, or a
custom role containing only `Microsoft.Insights/Telemetry/Write`. It does not
require Foundry project access.

Automatic publishing uses the same active credential as the mutating command.
For strict separation, leave automatic publishing disabled in deployment jobs
and use a separate audit pipeline identity to run `receipt upload` against
preserved receipts.

### Autopilot

`autopilot deploy` provisions a pinned experimental sample. Use an isolated,
time-bound infrastructure identity whose scope is limited to the approved
work area. Review the sample's resources before granting its required Azure
permissions; do not reuse a general production deployment identity.

## Invocation authorization

Inbound caller authorization and outbound runtime authorization are separate:

| Invocation surface | Least-privilege built-in access |
|---|---|
| Direct Prompt or Hosted agent endpoint | `Foundry Agent Consumer` at project or individual-agent scope |
| Agent Application | `Foundry User` on the Agent Application; use a custom role for invocation-only access |
| Middle tier sending `x-ms-user-identity` | A custom role containing `Microsoft.CognitiveServices/accounts/AIServices/agents/endpoints/UserIdentityImpersonation/action`; no current built-in role includes it |

## Runtime identity assignments

Operator authorization answers who may configure the agent. Runtime
authorization answers what the deployed agent may access.

| Connection or token mode | RBAC assignee |
|---|---|
| `AgenticIdentityToken` | The agent's `instance_identity` service principal |
| `ProjectManagedIdentity` | The Foundry project managed identity |
| OAuth identity passthrough | The consented user/delegated identity |
| Published Agent 365 identity | The distinct published identity after its lifecycle state is verified |

Do not grant both the project and agent identities access by default. Select
the principal from the configured token flow, grant the minimum downstream
role, and retest after publication or identity replacement.

## Implementation checklist

1. Create one workload identity per duty instead of one identity per
   executable.
2. Scope Foundry developer roles to a project and endpoint-consumer roles to a
   project or individual agent.
3. Keep project/model/account administration outside agent deployment jobs.
4. Keep Hosted provisioning outside normal Hosted deployment unless the
   infrastructure change is explicitly approved.
5. Keep Graph/Entra Agent 365 governance separate from Azure infrastructure
   mutation.
6. Grant runtime resource roles to the token-receiving identity, not to the
   human operator.
7. Use protected environments, PIM, and two-person review for production
   routing and destructive commands when Azure RBAC cannot distinguish them.
8. Publish receipts with a separate DCR identity when audit independence is
   required.
9. Test each persona in a nonproduction project and record the exact denied
   action and scope before creating a custom role.
10. Revalidate assignments after project creation, publication, identity
    replacement, or role-name changes.

## Microsoft documentation

- [Role-based access control for Microsoft Foundry](https://learn.microsoft.com/azure/foundry/concepts/rbac-foundry)
- [Foundry built-in role definitions](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles/ai-machine-learning)
- [Hosted agent permissions reference](https://learn.microsoft.com/azure/foundry/agents/concepts/hosted-agent-permissions)
- [Publish agents to Microsoft 365 and Teams](https://learn.microsoft.com/azure/foundry/agents/how-to/publish-copilot)
- [Azure built-in roles](https://learn.microsoft.com/azure/role-based-access-control/built-in-roles)
- [Create or update Azure custom roles](https://learn.microsoft.com/azure/role-based-access-control/custom-roles)
- [Managed identities for Azure resources](https://learn.microsoft.com/entra/identity/managed-identities-azure-resources/overview)
