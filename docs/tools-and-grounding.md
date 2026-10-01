# Tools and Grounding

Declarative tools, Toolbox lifecycle, Skills, managed document grounding,
connectors, API Center discovery, and Logic Apps registration planning.

## Why use managed tools and knowledge

These capabilities let an agent do more than generate text while keeping tool,
knowledge, and connection choices reviewable outside the Foundry portal:

- Use **direct declarative tools** when one Prompt Agent owns the integration.
- Use a **Toolbox** when multiple agents should reuse one versioned tool set.
- Use **managed grounding** when local documents should be hashed,
  synchronized, and attached by logical name.
- Use **Skills** when reusable instructions and files need their own immutable
  lifecycle.
- Use **managed connectors** when an external service requires catalog
  discovery, OAuth consent, action allowlisting, and a generated MCP target.
- Use **Memory** only when the preview persistence model and billable
  search/update operations are acceptable.

The user gains repeatability, drift visibility, containment checks, and
explicit destination approval. The manager does not make an external API safe,
grant consent on a user's behalf, execute caller-owned functions, or remove the
need to review preview and billing boundaries.

## RBAC and separation of duties

Toolbox, Skill, grounding, Memory, and managed connector lifecycle commands
normally use Foundry project data-plane access such as `Foundry User`. Keep
promotion, deletion, document pruning, personal-data deletion, and billable
Memory operations in protected jobs even when one service role technically
permits them.

Azure RBAC on a Foundry project does not authorize external Storage, Key Vault,
Azure AI Search, queue, remote-tool, or A2A access. Grant the downstream role to
the agent identity, project identity, or delegated user selected by the
connection mode. OAuth consent remains a delegated identity and tenant-policy
decision rather than an Azure RBAC grant.

See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md#project-connections-and-managed-connectors)
for connector requirements and
[Toolboxes, Skills, grounding, and Memory](rbac-and-separation-of-duties.md#toolboxes-skills-grounding-and-memory)
for the remaining tool sections.

## Declarative tools

The top-level `tools` list is the shortest path for capabilities owned by one
Prompt Agent. It attaches tools directly to that agent version. See
[`../examples/agent.full.example.yaml`](../examples/agent.full.example.yaml).

| Type | Required configuration | Notes |
|---|---|---|
| `code_interpreter` | none | Optional `container`. |
| `file_search` | `vector_store_ids` or logical `vector_store` | Logical name resolves a `grounding.vector_stores` entry. |
| `web_search` | none | Optional location and custom-search config. |
| `bing_grounding` | `search_configurations[].project_connection_id` | Existing Grounding with Bing Search connection. |
| `bing_custom_search_preview` | `search_configurations[].project_connection_id` and `instance_name` | Preview. |
| `azure_ai_search` | `indexes` with `project_connection_id` and `index_name` | Existing connections and indexes. |
| `openapi` | `name` and `spec` or `spec_file` | Anonymous, managed-identity, or connection auth. |
| `mcp` | `server_label`, `server_url` | Existing MCP endpoint; approval defaults to `always`. |
| `a2a` | `a2a_version: "1.0"`, `project_connection_id` | Stable A2A contract; optional protected agent-card retrieval. |
| `a2a_preview` | `project_connection_id` | Legacy preview compatibility; requires `--accept-preview`. |
| `browser_automation_preview` | `project_connection_id` | Preview. |
| `computer_use_preview` | `display_width`, `display_height`, `environment` | Preview. |
| `fabric_iq_preview` | `project_connection_id` | Preview. |
| `work_iq_preview` | `project_connection_id` | Preview. |
| `sharepoint_grounding_preview` | `project_connection_ids` | Preview. |
| `image_generation` | none | Optional model, quality, size. |
| `memory_search_preview` | `memory_store_name`, `scope` | Preview. |
| `custom_code_interpreter` | `server_label`, `server_url` | MCP contract for Container Apps Dynamic Sessions. |
| `function` | `function.name` | Caller-executed Function Calling; CLI does not run functions. |
| `toolbox` | `name`, `project_connection_id` | Attaches existing same-project Toolbox via MCP. |
| `azure_function` | `function`, `input_queue`, `output_queue` | Queue-backed Foundry Azure Function tool. |

### Preview acceptance

`prompt preflight` and `prompt deploy` reject preview tools until `--accept-preview` is supplied.

### OpenAPI contract

- Provide `spec` (inline) or `spec_file` (contained reference), never both.
- `spec_file` is resolved through rooted containment, limited to 8 MiB.
- Every effective `servers[].url` must be absolute `https` without userinfo.
- Templated server URLs and `servers[].variables` are rejected.
- External `$ref` values are rejected.
- Every server host must be approved with `--trusted-tool-host`.

### A2A agent-card and identity contract

Direct and Toolbox tools support the stable `a2a` contract. Set
`a2a_version: "1.0"` explicitly:

```yaml
tools:
  - type: a2a
    a2a_version: "1.0"
    project_connection_id: remote-agent
    base_url: https://agent.contoso.com
    agent_card_path: /private/agent-card.json
    send_credentials_for_agent_card: true
```

`a2a_preview` remains accepted for existing manifests and retains its preview
acceptance boundary. Stable `a2a` is not classified as preview.

`agent_card_path` is optional and defaults in Foundry to
`/.well-known/agent-card.json`. It may be a relative URL reference or an
absolute HTTPS URL. The manager rejects empty paths, HTTP, scheme-relative
URLs, embedded credentials, backslashes, and fragments.

`send_credentials_for_agent_card` is optional and defaults to `false`. When it
is `true`, Foundry may send the selected project connection credentials while
retrieving the card, but only over HTTPS and only when the card host matches
the effective A2A base host. An HTTP card or cross-host absolute card URL is
retrieved anonymously by the service. The manager still rejects HTTP rather
than depending on that anonymous fallback.

An absolute `agent_card_path` adds a separate runtime destination. Its host
must be approved with `--trusted-tool-host` even when the fetch is anonymous.
A relative path needs no second approval because it resolves against the
already reviewed A2A connection/base destination. `prompt plan` output includes the
path and credential-send setting so reviewers can see this choice before
deployment.

The project identity and agent identity have different jobs:

- The Foundry project managed identity authenticates the project/agent
  blueprint.
- Each modern agent has an `instance_identity` service principal for
  agent-native downstream authentication.
- A project connection using `AgenticIdentityToken` with `RemoteA2A` or
  `RemoteTool` obtains an unattended, application-only token for the agent
  identity and configured downstream audience. Grant downstream RBAC to the
  agent's `instance_identity.principal_id`.
- A connection using `ProjectManagedIdentity` obtains the downstream token as
  the project managed identity instead. Grant RBAC to that principal.
- OAuth identity passthrough is a separate delegated user-consent flow. Do not
  describe `AgenticIdentityToken` as user passthrough or grant its RBAC as
  though a signed-in user were the caller.

Inspect the connection authentication mode before selecting the RBAC assignee;
the manager intentionally does not guess which identity a downstream service
should trust.

### MCP contract

- `server_url` must be absolute `https` without embedded credentials.
- `headers` accepts static, non-secret string headers only.
- `require_approval` defaults to `always`. Overlap in per-tool policies is rejected.
- At `prompt preflight`/`prompt deploy`, the host must be exactly approved.

### Toolbox attachment contract

`type: toolbox` is translated to an MCP attachment:

```text
{project-endpoint}/toolboxes/{name}/mcp?api-version=v1
```

Same-project derived endpoints are exempt from external-host approval.

## Managed document grounding

Managed grounding gives teams a reproducible connection between files in Git
or a controlled workspace and a Foundry vector store. Hash comparison avoids
unnecessary uploads and exposes when remote indexing does not match the
expected document set.

```yaml
grounding:
  vector_stores:
    - name: product-docs
      description: Product documentation.
      files:
        - path: knowledge/product-guide.md

tools:
  - type: file_search
    vector_store: product-docs
```

```powershell
fam grounding validate -f agent.yaml
fam grounding plan -f agent.yaml
fam grounding sync -f agent.yaml
fam grounding status -f agent.yaml
```

- Files are SHA-256 hashed before upload.
- Removed files require `grounding sync --prune --yes`.
- Logical name deployment requires a completed, hash-verified store.

## Memory lifecycle

Memory provides preview, scoped persistence across interactions when an
application needs recall beyond one conversation. Adopt it only when its data
retention, model dependencies, billing, and current network limitations fit the
application's requirements.

```yaml
memory_stores:
  - name: assistant-memory
    chat_model: <chat-model-deployment>
    embedding_model: <embedding-model-deployment>
```

```powershell
fam memory store sync -f agent.yaml --memory-store assistant-memory --accept-preview
fam memory search -f agent.yaml --memory-store assistant-memory --scope user-123 --input "query" --accept-preview
```

Every online Memory command requires `--accept-preview`. The preview currently
lacks VNet integration.

## Skills lifecycle

Skills package reusable instructions and supporting files independently from an
agent version. Teams can version, review, download, and change the default Skill
without duplicating that content across every agent manifest.

Online Skills lifecycle operations use
`Foundry-Features: Skills=V1Preview` and require `--accept-preview`. Local
validation and attachment editing do not contact Azure.

| Desired outcome | Workflow | Boundary |
|---|---|---|
| Reuse instructions in Hosted Python/.NET without runtime MCP | Local or pinned-remote `bundle`, then explicit sync and application provider registration | Local-only bundles need no upload; remote bundles download during sync, not startup. |
| Load instructions through Foundry MCP in Hosted Python/.NET | Publish Skill, create immutable Toolbox version, attach both pins in `mcp` mode, sync, register provider | Requires runtime project access; never follows a logical default. |
| Manage a native Prompt declaration | Publish Skill, attach pinned `agent.skills`, preflight, experimental deploy | Creation/lifecycle passed, but native runtime consumption failed and remains unqualified. |
| Change only shared Skill content/default | `skill create` / `skill version set-default` | Does not update a pinned consumer, deploy an agent, or change agent traffic. |

The Hosted paths have representative live qualification with the pinned SDKs,
not a guarantee for every application or packaging combination. See the
[coverage record](../examples/hosted-skills/README.md#live-qualification-boundary).

The following single-line commands use portable paths and work in PowerShell
or a POSIX shell. Supply an existing `agent.yaml` for your project and a reviewed
Skill directory beside it before publishing:

```powershell
fam skill create -f agent.yaml --skill summarize --path skills/summarize --default --accept-preview
fam skill version list -f agent.yaml --skill summarize --accept-preview
fam skill version set-default -f agent.yaml --skill summarize --version 2 --accept-preview
```

Use a version actually returned by the service in place of `2`. `skill create`
publishes an immutable version; it is not a local scaffold command. Its `--path`
is relative to the manifest directory, while `skill validate --path` is relative
to the current working directory. Existing lifecycle commands can upload fuller
packages; the agent integrations below intentionally accept only instructions.

### Skills integration policy

The new agent integration accepts **instructions-only** packages: a directory
named for its Skill containing only `SKILL.md`, or a downloaded ZIP containing
one root `SKILL.md`. Supporting resources, scripts, unsafe archive entries,
duplicate identities, malformed frontmatter, and an empty instruction body
are errors, not silently skipped content. The existing general-purpose
`skill create` upload contract remains independent and can upload fuller
packages.

For a minimal local package, create `skills/greeting/SKILL.md` in a text editor
with the following UTF-8 contents and no other files in `skills/greeting`:

```markdown
---
name: greeting
description: Give a brief friendly greeting.
---
When asked for a greeting, greet the user briefly and offer help.
```

Then validate it from the directory containing `skills`:

```powershell
fam skill validate --path skills/greeting
```

The Foundry Skills documentation requires a name of at most 64 characters
using lowercase letters, digits, and single hyphens, without leading/trailing
hyphens; a description of at most 1,024 characters; unquoted name/description
frontmatter; and a nonempty Markdown body. Skill metadata does not grant
tools or authorize execution.

No Skills-specific count, body-size, aggregate-content, or archive-size quota
was established from the reviewed Azure documentation. FAM's existing bounded filesystem,
archive, and download processing protects the manager; those guards are
**not Azure quotas**. Agent Framework archive extraction defaults are
SDK-specific behavior, not Foundry service limits.
Accepted packages still require sufficient host memory and an appropriate
concurrency policy; see [Host resources and memory](hosted-agents.md#host-resources-and-memory).

[Prompt native attachments](prompt-agents.md#native-skills-preview) use
`agent.skills`. [Hosted integration](hosted-agents.md#hosted-skills)
uses an explicit filesystem bundle (no MCP at runtime) or an MCP Skills
provider. A Toolbox Skill reference or an attached MCP tool alone does not
prove that an agent discovers or loads Skill instructions.

### Publish and pin a Hosted MCP Skill

This is an alternative to the no-MCP [Hosted bundle workflow](hosted-agents.md#hosted-skills),
not an automatic next step. Use a manifest and Hosted workspace targeting the
**same project**. Replace quoted placeholders with returned immutable versions.

1. Validate the directory, then publish it:

   ```powershell
   fam skill validate --path skills/greeting
   fam skill create -f agent.yaml --skill greeting --path skills/greeting --accept-preview
   ```

2. Add `greeting` and the returned Skill version to the desired Toolbox's
   `toolboxes[].skills` in `agent.yaml`, following the
   [Toolbox example](../examples/agent.toolbox.example.yaml). Do not place this
   reference in `agent.skills` for a Hosted consumer. Create the Toolbox version:

   ```powershell
   fam toolbox validate -f agent.yaml
   fam toolbox plan -f agent.yaml
   fam toolbox deploy -f agent.yaml --toolbox shared-tools --if-changed --accept-preview
   fam toolbox versions list -f agent.yaml --toolbox shared-tools
   ```

3. Select both immutable pins for an existing Python workspace:

   ```powershell
   fam hosted skill attach --workspace hosted-agent --skill greeting --version "<skill-version>" --mode mcp --language python --toolbox shared-tools --toolbox-version "<toolbox-version>"
   fam hosted skill sync --workspace hosted-agent --accept-preview
   ```

   Use `--language dotnet` for .NET. Sync verifies that the pinned Toolbox
   contains the selected Skill version and validates the complete downloaded
   package. It does not publish missing Skills or create a Toolbox.

4. Register the [Python or .NET provider](../examples/hosted-skills/README.md)
   in the existing application, then validate, preflight, and deploy using the
   [Hosted workflow](hosted-agents.md#deployment-commands). Verify actual
   instruction discovery/loading before deliberately promoting the agent version.

Changing a Skill or Toolbox default does not move either pin. To update, publish
new content, create a new Toolbox version, update the attachment pins, sync,
and rebuild/redeploy the application. Promoting a Toolbox changes its default
for default-following consumers; it is not required to consume an explicitly
pinned version and does not promote agent traffic.

### Remove, retain, and recover

`prompt skill remove` and `hosted skill remove` edit local declarations only.
Apply Prompt detachment through a separate experimental deployment. For Hosted
detachment, sync the resulting `skills: []`, rebuild/redeploy, and separately
promote as appropriate. Removing an attachment never deletes the original local
directory, a shared Skill, or a Toolbox.

Keep immutable Skill/Toolbox versions and built artifacts needed by retained
agent versions; deleting a pinned dependency can break runtime loading or
future synchronization. To preserve package bytes, download an exact version:

```powershell
fam skill download -f agent.yaml --skill greeting --version "<skill-version>" --destination "<backup.zip>" --accept-preview
```

Recreating a deleted resource is not a promise to recover its old version ID.
Prefer rollback to a retained, qualified agent and its matching artifacts.

Treat Skill instructions as reviewed content, not a security boundary. The
instructions-only policy does not prevent instructions from influencing tools
already available to the application. Keep normal tool approvals and runtime
permissions in place.

## Foundry Toolbox lifecycle

A Toolbox gives multiple Prompt or Hosted agents one reusable, immutable tool
bundle. New versions can be deployed and reviewed while consumers continue
using the current default, then promoted deliberately.

```powershell
fam toolbox validate -f agent.yaml
fam toolbox plan -f agent.yaml
fam toolbox deploy -f agent.yaml --toolbox shared-tools --if-changed
fam toolbox status -f agent.yaml --toolbox shared-tools
fam toolbox versions list -f agent.yaml --toolbox shared-tools
fam toolbox promote -f agent.yaml --toolbox shared-tools --toolbox-version "<version>" --yes
fam toolbox versions delete -f agent.yaml --toolbox shared-tools --toolbox-version "<non-default-version>" --yes
```

The first created version becomes `default_version` automatically. Every later
version remains staged until `toolbox promote`.

The deployment example above is for a Toolbox without preview capabilities.
If it contains Skills, add `--accept-preview` to **`toolbox deploy` only**;
status, version listing, promotion, and deletion do not accept that flag.

## Tool catalog and compatibility

Use the catalog to discover which contracts the manager understands and the
compatibility command to catch known model/region/tool mismatches before
deployment. Compatibility data is source-stamped guidance, not a live Azure
availability promise.

```powershell
fam tool-catalog --cloud AzureCloud --output json
fam prompt compatibility -f agent.yaml --model-name gpt-4.1 --region eastus2
```

## Managed MCP connector lifecycle

The commands under `connector` implement the documented preview Foundry Connector
Namespace flow for **OAuth2** connectors (AzureCloud only).

This workflow turns catalog discovery, user consent, action selection, readiness
waiting, and Toolbox attachment into explicit steps. It prevents broad connector
access from being treated as one opaque portal action.

```powershell
fam connector list -f agent.yaml --search github --accept-preview
fam connector create -f agent.yaml --connection github-actions --connector-name github --accept-preview
fam connector consent -f agent.yaml --connection github-actions --object-id <id> --tenant-id <tid> --accept-preview
fam connector configure -f agent.yaml --connection github-actions --operation CreateIssue --operation GetIssue --accept-preview
fam connector wait -f agent.yaml --connection github-actions --accept-preview
fam connector toolbox deploy -f agent.yaml --connection github-actions --toolbox-name operations --if-changed --accept-preview --trusted-tool-host <host>
```

`connector list --search` matches connector names using the catalog's supported
name filter. Use `connector show --connector-name` after discovery for the exact
catalog record.

Non-OAuth2 connectors remain on the separate Logic Apps Standard registration
workflow.

## Logic Apps connector registration planning

Use this command when the connector requires a portal registration workflow
rather than managed OAuth2 automation. The output is a validated handoff
worksheet, so an operator knows exactly what must be registered without the CLI
claiming it completed the external mutation.

```powershell
fam connector logic-apps registration plan -f agent.yaml `
  --connector-name rss --mcp-server-name rss-tools `
  --mcp-server-description "Read approved RSS feeds." `
  --operation ListFeedItems --user-parameter ListFeedItems/feedUrl `
  --accept-preview
```

Validates and generates a registration worksheet; does not perform the mutation.

## API Center registry discovery

API Center discovery helps users find registered MCP metadata before deciding
whether to configure or trust an integration. It is read-only and does not
attach the discovered service to an agent.

```powershell
fam connector api-center list -f agent.yaml `
  --api-center-endpoint https://<service>.data.<region>.azure-apicenter.ms `
  --search orders
```

Read-only discovery pinned to HTTPS and `.azure-apicenter.ms`.
