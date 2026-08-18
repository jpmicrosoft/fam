# Glossary

Plain-language definitions for terms used throughout this documentation.
If you are new to Foundry or this CLI, read this page first.

## Core concepts

| Term | What it means |
|---|---|
| **Foundry** | Microsoft's platform for building and hosting AI agents. You access it through an Azure account. Think of it as the managed service where your agents run. |
| **Foundry account** | Your organization's top-level Foundry resource in Azure. It contains projects and model deployments. Create it before using this CLI. Subscription ownership, quota, billing approval, and RBAC remain administrative concerns. |
| **Foundry project** | A container inside a Foundry account that holds agents, connections, and tools. One account can have many projects. The CLI can create child projects for you (`project create`). |
| **Model deployment** | A named, capacity-bearing instance of an exact model version and SKU in a Foundry account. The CLI can inspect, live-plan, explicitly create, and explicitly delete it through ARM; Prompt deployment never creates it implicitly. |

## Agent types

| Term | What it means |
|---|---|
| **Prompt Agent** | An agent defined entirely by a YAML/JSON manifest: instructions, a model, and declarative tools. No custom code to deploy. This is the simpler path. |
| **Hosted Agent** | An agent that runs your custom application code (Python, .NET, or a container) in a managed runtime. More powerful, but requires additional tooling (`azd`) and infrastructure provisioning. This path is currently **preview**. |
| **Agent 365 / Microsoft Entra Agent ID** | Identity, registry, governance, and access-management capabilities for agents. In this CLI, `agent365` is a separate read-only and plan-only namespace (with the exception of `integration set`) rather than a deployment source. |
| **Agent ID blueprint** | A specialized Microsoft Entra application that acts as a template for creating agent identity instances. It is not agent code, instructions, a Prompt manifest, or a Hosted workspace. |
| **Agent ID identity** | A runtime identity instance created from a blueprint. Distinct from the blueprint itself and from Foundry project identities. |
| **Blueprint principal** | A service principal associated with a blueprint, representing the blueprint's instantiation in a directory. |
| **Instance identity** | The runtime service principal exposed by Foundry for supported agent-native authentication. Assign downstream Azure RBAC to this principal when the configured token flow uses it. |
| **Blueprint correlation** | A read-only comparison between an existing Agent ID blueprint's IDs and the blueprint fields returned by Foundry. A match is evidence, not proof that this CLI created a binding. |
| **Agent 365 integration** | The account-level Agent 365 logging capability on a Foundry account, controlled by `properties.a365LoggingEnabled`. Active only when both the logging flag is `true` and `a365Status` is `Enabled`. Scope is the entire account with storage following Entra tenant geography. |
| **Agent 365 observability** | OpenTelemetry-based telemetry collection for agents, using the Microsoft OpenTelemetry Distro or legacy Agent 365 observability SDK. Requires the `Agent365.Observability.OtelWrite` app role on the agent identity. |
| **Agent 365 publication** | The process of registering an agent identity in the Agent 365 registry, creating a distinct identity from the shared project identity. Unpublished agents share the project identity; publication triggers RBAC reassignment. |

## CLI workflow concepts

| Term | What it means |
|---|---|
| **Manifest** | A YAML or JSON file (`agent.yaml`) that declares everything about a Prompt Agent: its name, model, instructions, tools, and project. It is the source of truth you version in Git. |
| **Workspace** | For Hosted Agents, the directory containing `azure.yaml` and your application source code. Analogous to the manifest for Prompt Agents. |
| **Validate** | Check that your manifest or workspace is well-formed *without contacting Azure*. Fast, offline, safe to run anytime. |
| **Plan** | Show what the CLI would do without intentionally changing resources. Prompt/Hosted deployment plans are offline; plans that compare remote state, such as `agent365 binding plan`, are read-only online operations. |
| **Preflight** | A read-only online check: verifies credentials, project access, tool configuration, required approvals, and reachable Azure dependencies without intentionally creating or changing resources. |
| **Deploy** | Create a new immutable agent version in Azure. After the first deployment, new versions are *staged* (not serving traffic) until you explicitly promote them. |
| **Promote** | After the initial deployment, route all stable-endpoint traffic to a selected agent version as a separate, deliberate operation. |
| **Rollback** | Route traffic back to a previously verified version. |
| **Receipt** | A redacted JSON file written by deployment and supported lifecycle mutations. It records the operation and resulting state without secrets, which supports auditing and reconciliation. |
| **Log Analytics receipt publishing** | Optional upload of a completed redacted receipt to an existing Azure Monitor Logs DCR and Log Analytics custom table. The local file is preserved first. |
| **DCR (data collection rule)** | Azure Monitor resource that declares the incoming receipt stream, optional transformation, and destination Log Analytics table. |

## Safety and trust concepts

| Term | What it means |
|---|---|
| **Destination trust approval** | Before the CLI sends credentials to any external host (APIM gateway, MCP server, etc.), *you* must explicitly approve that host. The manifest cannot approve its own destinations. |
| **`--accept-preview`** | A flag you must pass to use preview features (like Hosted Agents). It exists so you consciously acknowledge you are using a feature whose upstream API may change. |
| **Immutable version** | Once an agent version is created, it cannot be modified. You deploy a *new* version and promote it. This makes rollback reliable. |
| **Fail closed** | When something is uncertain, the CLI refuses to proceed rather than guessing. Unknown hosts, unapproved destinations, and ambiguous outcomes produce errors, not silent fallbacks. |

## Tool and knowledge concepts

| Term | What it means |
|---|---|
| **Toolbox** | A reusable, versioned bundle of tools that multiple agents can share. Managed separately from agent deployment. |
| **Grounding** | Connecting an agent to supported local documents through a vector store. The CLI hashes and synchronizes files; the Prompt Agent can then reference the store by logical name. |
| **Skill** | Reusable instructions and files packaged with their own versioned lifecycle, independent of any single agent. Preview feature. |
| **Connector** | A managed OAuth2 integration with an external service (e.g., GitHub, Salesforce) discovered from the Foundry catalog. Preview feature. |
| **Memory** | Preview persistence that lets an agent recall information across conversations. Billable and currently lacks VNet support. |

## Infrastructure and tooling

| Term | What it means |
|---|---|
| **`azd` (Azure Developer CLI)** | A separate Microsoft CLI required only for Hosted Agents. It handles infrastructure provisioning and code deployment. The foundry-agent-manager orchestrates `azd` but never auto-installs it. |
| **`azd` extension** | A plugin for `azd` that adds Hosted Agent support. Must be installed at the exact pinned version (`azure.ai.agents 1.0.0-beta.8`). |
| **ARM** | Azure Resource Manager — the control plane for Azure resources. Some CLI commands use ARM to discover projects, create connections, or check resource state. |
| **APIM** | Azure API Management — an existing API gateway your agent can connect to. The CLI creates a *connection* to APIM; it never creates or modifies the APIM service itself. |
| **DefaultAzureCredential** | The Azure SDK credential chain used by online commands. The active environment must provide one of its supported identities, such as an applicable developer credential or managed identity. |

## Command safety labels

Every command in the [Command Reference](command-reference.md) is labeled with its safety level:

| Label | What it means | Example |
|---|---|---|
| **Offline / no Azure** | Runs entirely on your machine. No credentials, no network. | `prompt validate`, `prompt plan`, `prompt init` |
| **Read-only** | Contacts Azure but only reads. Nothing is created, changed, or deleted. | `prompt preflight`, `prompt status`, `prompt show` |
| **Mutating** | Creates or updates Azure resources. A receipt is written. | `prompt deploy`, `prompt promote`, `project create` |
| **Destructive** | Deletes resources and requires confirmation. Use `--yes` for approved non-interactive execution. | `prompt delete`, `prompt versions prune`, `prompt decommission` |
| **Billable** | Invokes the AI model, which incurs usage cost. | `prompt smoke`, `hosted smoke`, `memory search` |
