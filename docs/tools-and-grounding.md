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

Skills use `Foundry-Features: Skills=V1Preview`; every command requires
`--accept-preview`.

```powershell
fam skill create -f agent.yaml --skill summarize --path .\skills\summarize --default --accept-preview
fam skill version list -f agent.yaml --skill summarize --accept-preview
fam skill version set-default -f agent.yaml --skill summarize --version 2 --accept-preview
```

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
fam toolbox promote -f agent.yaml --toolbox shared-tools --toolbox-version <version> --yes
fam toolbox versions delete -f agent.yaml --toolbox shared-tools --toolbox-version <non-default-version> --yes
```

The first created version becomes `default_version` automatically. Every later
version remains staged until `toolbox promote`.

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
