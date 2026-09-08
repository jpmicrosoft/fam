<p align="center">
  <img
    src="docs/assets/fam-logo.svg"
    alt="FAM logo: a shield containing Prompt Agent and Hosted Agent symbols"
    width="144"
  />
</p>

<h1 align="center">Foundry Agent Manager</h1>

<p align="center">
  <strong>Controlled, repeatable, and auditable deployment for Microsoft
  Foundry agents.</strong>
</p>

<p align="center">
  <a href="https://github.com/jpmicrosoft/fam/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/jpmicrosoft/fam/actions/workflows/ci.yml/badge.svg?branch=main" /></a>
  <a href="https://github.com/jpmicrosoft/fam/releases/latest"><img alt="Latest release" src="https://img.shields.io/github/v/release/jpmicrosoft/fam?sort=semver" /></a>
  <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/github/license/jpmicrosoft/fam" /></a>
  <a href="https://go.dev/"><img alt="Go version" src="https://img.shields.io/github/go-mod/go-version/jpmicrosoft/fam" /></a>
</p>

Foundry Agent Manager (FAM) is a standalone Go CLI that provides a governed
operations layer between your agent source and Microsoft Foundry. It manages
**Prompt Agents** directly from YAML or JSON manifests and operates **Hosted
Agents** through validated `azure.yaml` workspaces and a pinned `azd ai agent`
contract. Infrastructure provisioning remains explicit.

The installed executable and canonical command are both named `fam`. The CLI
requires no runtime language dependency or external state backend.

> **Independent project.** FAM is independently maintained and is not an
> official Microsoft product or supported Microsoft offering.

<a id="first-success-without-azure"></a>

## Try FAM locally

**Start here if you are evaluating FAM.** You need only the
[installed `fam` executable](#install): no Azure account, Go, `azd`, or
repository clone.

In a new working directory, run:

```powershell
fam prompt init -f agent.yaml --no-tools
fam prompt validate -f agent.yaml
fam prompt plan -f agent.yaml
```

**Expected result:** a new `agent.yaml`, a structure-only validation result,
and a plan describing the agent, model, and deployment intent. Nothing has
been deployed. The generated model and project values are placeholders;
successful offline commands do not prove Azure access or model availability.

Try changing `agent.instructions` in the file and run validation and planning
again. This is the edit/review loop you will use before deployment.

| Ready for the next step? | Go to |
|---|---|
| Deploy instructions and tools using an existing project/model | [Prompt quickstart](#quick-start-prompt-agent) |
| Create a workspace for custom code, or adopt existing Python code | [Hosted quickstart](#quick-start-hosted-agent) |
| Work with an existing `azure.yaml` workspace | [Existing Hosted workspace](#existing-hosted-agent-workspace) |
| Understand the choices or find a specific guide | [Documentation hub](docs/README.md) |
| A command failed | [Quick troubleshooting](#quick-troubleshooting) |

Online operations support **AzureCloud only**. Hosted Agent deployment is
**preview** and requires additional tooling and explicit acceptance; the
quickstarts separate those requirements from offline exploration.

## Why teams adopt FAM

- **[Prove before mutation](#doctor--environment-readiness):** separate
  validation, planning, and online preflight commands.
- **[Fail closed](#security-principles):** reject missing guardrail intent,
  conflicting project identities, unsafe references, and unverifiable
  dependencies.
- **[Control provisioning](#choose-your-deployment-path):** deploying an agent
  does not silently authorize infrastructure creation.
- **[Manage the lifecycle](#common-next-steps-after-your-first-deployment):**
  immutable versions, change detection, promotion, rollback, pruning, status,
  and diagnostics.
- **[Preserve evidence](#cicd-with-structured-output-and-receipts):** supported
  mutations produce redacted receipts with verified outcomes and recovery
  guidance.
- **[Use one operational model](#command-organization):** manage Prompt and
  Hosted Agents through consistent command and output conventions.
- **[Bring existing code](#existing-hosted-agent-workspace):** adopt existing
  Python agents without rewriting their application logic.

## FAM and `azd`

FAM does not replace Microsoft Foundry, `azd`, or your agent framework. Hosted
deployments use the reviewed `azd ai agent` contract underneath.

Use `azd` directly when you want the simplest greenfield development and
deployment path.

Use FAM when you need stronger controls around that path: existing
infrastructure, multiple environments or operators, explicit provisioning
boundaries, guardrail enforcement, repeatable lifecycle management, and
auditable deployment evidence.

> **`azd` optimizes the developer deployment path. FAM makes that path
> controlled, repeatable, and auditable.**

## Operational boundaries

- **Fails closed.** Manifests are untrusted input; credential-bearing and
  data-egress destinations require explicit operator approval.
- **Auditable.** Supported mutations write redacted JSON receipts, and failures
  return stable exit codes and structured error envelopes.
- **Explicit provisioning.** Agent deployment does not silently authorize
  infrastructure creation.
- **AzureCloud only.** Azure Government is rejected before credential
  acquisition or network access until dedicated qualification is complete.

> **Preview status.** This tool is version 0.17.0. Hosted Agents require
> `--accept-preview`. See [Support status](#support-status-and-release-boundaries)
> for the full boundary table.

Use the [documentation hub](docs/README.md) to choose a task,
the [FAQ](docs/faq.md) for practical answers, and the
[Glossary](docs/glossary.md) to look up unfamiliar terms as needed.

## Contents

- [Try FAM locally](#try-fam-locally)
- [Documentation hub](docs/README.md)
- [Why teams adopt FAM](#why-teams-adopt-fam)
- [FAM and `azd`](#fam-and-azd)
- [Operational boundaries](#operational-boundaries)
- [Which path do I need?](#which-path-do-i-need)
- [Frequently asked questions](docs/faq.md)
- [Support status and release boundaries](#support-status-and-release-boundaries)
- [Prerequisites](#prerequisites)
- [RBAC and separation of duties](docs/rbac-and-separation-of-duties.md)
- [Install](#install)
- [Update an existing installation](#update-an-existing-installation)
- [Command organization](#command-organization)
- [Quick start: Prompt agent](#quick-start-prompt-agent)
- [Quick start: Hosted agent](#quick-start-hosted-agent)
- [Agent 365 blueprint inspection](#agent-365-blueprint-inspection)
- [Choose your deployment path](#choose-your-deployment-path)
- [Doctor — environment readiness](#doctor--environment-readiness)
- [VS Code integration](#vs-code-integration)
- [CI/CD templates](#cicd-templates)
- [Security principles](#security-principles)
- [Detailed documentation](#detailed-documentation)
- [Project documents](#project-documents)
- [License](#license)

## Which path do I need?

Answer one question: **Does your agent need custom application code?**

| Answer | Path | What you need before starting |
|---|---|---|
| **No** — my agent is instructions + a model + declarative tools | **[Prompt Agent](#quick-start-prompt-agent)** | A Foundry account, an existing or explicitly planned model deployment, and a supported Azure identity such as an applicable developer credential or managed identity |
| **Yes** — I need Python, .NET, or a container runtime | **[Hosted Agent](#quick-start-hosted-agent)** | A Foundry account plus `azd` 1.32.0+ and the pinned Hosted extension; model infrastructure remains declared in `azure.yaml` |
| **I already have an Agent 365 blueprint** | **[Inspect and correlate it](#agent-365-blueprint-inspection)** | Microsoft Graph `AgentIdentityBlueprint.Read.All`; this path does not deploy source or bind the blueprint |
| **I'm not sure yet** | [Try FAM locally](#try-fam-locally) | The `fam` executable only; no Azure coordinates or login |

The Azure prerequisites above apply to deployment, not to trying the CLI.
You can also [create and inspect a Hosted workspace offline](#quick-start-hosted-agent)
before installing `azd`.

## Support status and release boundaries

Use this table to decide what can be treated as a stable production contract
and what still requires preview acceptance, additional review, or a manual
handoff. The labels prevent a successful command from being mistaken for a
long-term support promise when an upstream service is still preview.

| Status | Meaning |
|---|---|
| **Supported** | Versioned product behavior covered by offline qualification and live release scenarios. |
| **Preview-supported** | Tested but dependent on an upstream preview API or pinned preview tool. |
| **Experimental** | Narrow opt-in with no compatibility promise. |
| **Plan/read-only** | Validation, discovery, or handoff output only. |
| **Release tooling** | Maintainer qualification code, not end-user. |

| Capability | Manager status | Cloud |
|---|---|---|
| Manifest validation, planning, diff, receipts | **Supported** | Offline / cloud-independent |
| Receipt publishing to Log Analytics through a DCR | **Supported, opt-in** | AzureCloud |
| Foundry project creation and prompt-agent lifecycle | **Supported** | AzureCloud |
| Foundry model deployment discovery, live planning, creation, and deletion | **Supported** | AzureCloud |
| Core prompt tools, document grounding | **Supported** | AzureCloud |
| Structured inputs and function-calling schema | **Supported** | AzureCloud |
| Microsoft 365 and Teams publishing | **Supported** | AzureCloud |
| Project connections, APIM connection management | **Preview-supported** | AzureCloud |
| Hosted Agent lifecycle, sessions, files, logs, scaffold | **Preview-supported** | AzureCloud |
| Foundry Toolboxes and connector automation | **Preview-supported** | AzureCloud |
| Memory, Skills, managed OAuth2 connectors, legacy apps | **Preview-supported** | AzureCloud |
| API Center discovery, Logic Apps registration planning | **Plan/read-only** | — |
| Agent 365 blueprint, identity, principal inspection and correlation | **Plan/read-only** | AzureCloud |
| Agent 365 integration logging | **Preview-supported** | AzureCloud |
| Agent 365 observability readiness | **Plan/read-only** | AzureCloud |
| Agent 365 publication planning and admin handoff | **Plan/read-only** | AzureCloud |
| Hosted-agent Autopilot wrapper | **Experimental** | AzureCloud |
| Evaluator calibration | **Release tooling** | — |

### Publishing and identity boundaries

- Prompt deployment stages immutable versions; production routing changes only
  through explicit promotion. The service's `@latest` and ratio selectors are
  not used as an implicit rollout shortcut.
- `prompt m365 publish` uses the stable `v1` Microsoft 365 REST operation, explicitly
  disables Autopilot, and reuses the modern agent's `instance_identity`.
- Hosted Autopilot is a separate experimental pinned-sample workflow. There is
  no Prompt Autopilot publishing path.
- `agent365` can inspect an existing Agent ID blueprint, identity, and
  principal; compare it with Foundry identity fields; manage integration
  logging on a Foundry account; inspect observability readiness; and plan
  publication. It cannot bind an arbitrary blueprint or Agent ID because no
  documented Foundry mutation API exposes that operation.
- New-model agents have a unique identity at creation, and standard Microsoft
  365 publication does not replace it. Legacy agents can use the shared project
  identity; migrating a legacy agent or Agent Application to a new-model agent
  requires deliberate downstream RBAC reassignment.
- `AgenticIdentityToken` uses the agent identity for unattended downstream
  tokens; `ProjectManagedIdentity` uses the project identity; OAuth identity
  passthrough is a separate user-consent flow.
- A2A agent-card credentials are off by default. Protected cards are supported
  only with explicit same-host HTTPS credential sending and exact trust
  approval for any absolute card URL.

## Prerequisites

The CLI intentionally builds on resources and identities you already control.
It does not create a parent Foundry account and does not hide which Azure
identity performs online operations. Model deployment creation is a separate,
explicit workflow with live quota and capacity validation; Prompt Agent
deployment never creates a model implicitly.

### Tooling prerequisites

| Workflow | Required local tooling |
|---|---|
| Offline validation, planning, and scaffolding | The `fam` executable only |
| Online Prompt, project, model, connection, or Agent 365 operations | `fam` plus an identity that `DefaultAzureCredential` can resolve. Azure CLI (`az`) is one optional developer credential source, not a universal requirement. |
| Online Hosted Agent operations | `fam`, Azure Developer CLI (`azd`) 1.32.0 or later, and the `azure.ai.agents` azd extension at exactly `1.0.0-beta.13` |
| Optional source build | Go 1.25 or later |

Install the pinned Hosted Agent extension explicitly:

```powershell
azd extension install azure.ai.agents --version 1.0.0-beta.13
```

The `microsoft.foundry` extension bundle can supply this component, but FAM
checks the installed `azure.ai.agents` component directly and requires the
exact reviewed version. That component declares `azd` 1.32.0 as its minimum
version, so FAM rejects earlier `azd` releases before invoking it.

Azure CLI and Azure Developer CLI maintain separate authentication context.
Use `az login` only when Azure CLI should provide the local developer
credential for FAM. Authenticate `azd` separately before an online Hosted
workflow:

```powershell
azd auth login --tenant-id "<tenant-id>"
```

> **Source dependency scope:** `go mod download` downloads only the Go modules
> declared in `go.mod`. It does not install the `fam` executable, Azure CLI
> (`az`), Azure Developer CLI (`azd`), or the Hosted Agent
> `azure.ai.agents` extension described above.

### Azure resources and access prerequisites

| Prerequisite | Detail |
|---|---|
| Foundry account | Must already exist |
| Model deployment | May already exist or be explicitly created with `model deployment plan` followed by `model deployment create`; `agent.model` is the deployment name |
| Azure identity | Any `DefaultAzureCredential` can resolve (online commands only) |
| RBAC model | Assign separate operator, infrastructure, publication, governance, runtime, and audit identities; see [RBAC and Separation of Duties](docs/rbac-and-separation-of-duties.md) |
| Agent 365 inspection | Microsoft Graph `AgentIdentityBlueprint.Read.All`, `AgentIdentity.Read.All`, `AgentIdentityBlueprintPrincipal.Read.All`; `Application.Read.All` for sponsors, friendly names, and observability assignment inspection; delegated non-owners also need the Agent ID Administrator role |

`prompt validate` and `prompt plan` are fully offline and need no Azure identity.

## Install

> [!IMPORTANT]
> **Breaking command rename:** Starting with `0.15.0`, release archives
> and installers provide only `fam` (`fam.exe` on Windows). Scripts that invoke
> `foundry-agent-manager` must change those invocations to `fam`. The product
> remains named **Foundry Agent Manager**.

> **Most users do not need Go, a compiler, or a clone of this repository.**
> Downloading a published release gives you the complete, self-contained CLI
> executable for your operating system.

This installs the **`fam` command-line application** on your
computer:

- Windows receives one executable named `fam.exe`.
- macOS and Linux receive one executable named `fam`.
- The executable is the CLI used by every example in this documentation, such
  as `fam quickstart` and `fam prompt deploy`.

Installing the CLI does **not** deploy an agent, create Azure resources, install
Go, or install Azure Developer CLI (`azd`). Prompt Agent users can run the
offline commands immediately. Hosted Agent users must also install the
versioned `azd` tooling listed under [Prerequisites](#prerequisites) before
online operations, not before scaffolding, validation, or planning.

Choose one installation path:

- **Installer:** easiest option; downloads the correct prebuilt executable,
  verifies its checksum, and places it in an install directory.
- **Release archive:** download and extract the prebuilt executable yourself.
- **Build from source:** optional maintainer/developer path that requires Go.

### 1. Checksum-verifying installers (recommended)

**PowerShell (Windows / macOS / Linux):**

Download [`scripts/install.ps1`](scripts/install.ps1), save it as
`install.ps1`, and review it before running. In PowerShell, from the directory
where you saved it:

```powershell
.\install.ps1 -ModifyProfile
```

`-ModifyProfile` explicitly opts in to adding the install directory to PATH.
Open a new terminal afterward. To leave PATH unchanged, omit that switch and
use the full executable path printed by the installer. On macOS or Linux,
invoke the saved script with `pwsh ./install.ps1 -ModifyProfile`.

**POSIX shell (macOS / Linux):**

Save the installer locally, review it, then run it. These commands do not
require a repository clone or an executable permission change:

```bash
curl -fsSL https://raw.githubusercontent.com/jpmicrosoft/fam/main/scripts/install.sh -o install.sh
# Review install.sh before the next command.
sh ./install.sh --modify-profile
```

Open a new terminal after the PATH change. Alternatively, omit
`--modify-profile` and use the printed full executable path.

**Confirm installation, then continue:**

```powershell
fam version
```

You should see `fam` and the installed version. Next,
[try FAM locally](#try-fam-locally), or use
[installer troubleshooting](#common-powershell-installer-issues) if the
command is not found.

<details>
<summary>Advanced installer options: version, directory, and private repository</summary>

Choose the option you need; these are alternatives, not a sequence.
Use a tag that is already published.

```powershell
.\install.ps1 -Version v0.16.3
.\install.ps1 -InstallDir C:\tools -ModifyProfile
.\install.ps1 -Repo myorg/fam
```

POSIX equivalents, using the saved installer:

```bash
sh ./install.sh --version v0.16.3
sh ./install.sh --install-dir "$HOME/.local/bin" --modify-profile
sh ./install.sh --repo myorg/fam
```

</details>

Both installers:
- Download the release archive matching your OS/architecture.
- Verify the **SHA256 checksum** against the published `SHA256SUMS` file.
- Install only `fam` (`fam.exe` on Windows).
- Remove the retired `foundry-agent-manager` executable from the selected
  install directory when upgrading from an earlier release.
- Install to a configurable directory (default: `$LOCALAPPDATA\foundry-agent-manager` on Windows, `$HOME/.local/bin` on POSIX).
- **Never modify PATH** unless `-ModifyProfile` / `--modify-profile` is explicitly passed.
- Support private repositories via `GITHUB_TOKEN` / `GH_TOKEN` environment variable or `FAM_INSTALL_TOKEN` secret (token is used only as an HTTP authorization header and never printed).
- Accept `--repo` / `-Repo` to override the source GitHub repository.

#### Common PowerShell installer issues

| Symptom | Resolution |
|---|---|
| `running scripts is disabled` | After reviewing the downloaded script, use `Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass`; do not weaken machine-wide policy |
| Script is blocked or not digitally signed | Review its source, then run `Unblock-File .\install.ps1` |
| `Could not determine latest release` | Check GitHub access/private-repo authentication, or pass a known tag such as `-Version v0.16.3` |
| Release or asset download returns 404 | Verify `-Repo OWNER/REPO`, the `v`-prefixed tag, token access, and the platform archive exists |
| Checksum missing or mismatched | Stop and retry from the intended release; never bypass checksum verification |
| Access denied while installing | Close a running executable or select a user-writable `-InstallDir` |
| Install succeeds but command is not found | Use the printed full path, or rerun with `-ModifyProfile` and open a new terminal |

See the
[PowerShell installer questions in the FAQ](docs/faq.md#how-should-i-run-a-downloaded-installps1)
for private-repository tokens, architecture support, proxies, and macOS/Linux
PowerShell requirements.

### 2. Download a prebuilt release archive

Open the
[`fam` Releases page](https://github.com/jpmicrosoft/fam/releases)
and download the archive matching your computer:

| Computer | Release archive |
|---|---|
| Windows x64 | `fam_<version>_windows_amd64.zip` |
| Windows Arm64 | `fam_<version>_windows_arm64.zip` |
| macOS Intel | `fam_<version>_darwin_amd64.tar.gz` |
| macOS Apple silicon | `fam_<version>_darwin_arm64.tar.gz` |
| Linux x64 | `fam_<version>_linux_amd64.tar.gz` |
| Linux Arm64 | `fam_<version>_linux_arm64.tar.gz` |

Each archive contains exactly one executable named `fam` or `fam.exe`, plus
`LICENSE` and `THIRD_PARTY_NOTICES.txt`. Also download `SHA256SUMS`, confirm
the archive checksum, extract the executable, and place it in a directory on
PATH. This path requires no source code and no Go installation.

### 3. Build from source (optional)

This path is for contributors or users who intentionally want to compile the
CLI themselves. It is not required for normal installation.

```powershell
git clone https://github.com/jpmicrosoft/fam.git
cd fam
go build -trimpath -o bin\fam.exe .\cmd
bin\fam.exe version
```

### 4. Enable shell completion

The executable generates completion scripts for PowerShell, Bash, Zsh, and
Fish. Completions include commands, focused `help` topics, flags, documented
enum values, and relevant file or directory paths without contacting Azure.

Load PowerShell completion for the current session:

```powershell
fam completion powershell | Out-String | Invoke-Expression
```

Run `fam completion <shell> --help` for persistent
installation instructions for your shell.

### Update an existing installation

```powershell
fam update --check                   # check without changing the executable
fam update                          # latest stable release, with confirmation
fam version                         # show the version now installed
```

To select an exact published stable release instead of latest, use
`fam update --version v0.17.0`. Use `--yes` to skip confirmation in automation.

The updater verifies the release archive against `SHA256SUMS` and replaces the
executable you invoked, resolving symlinks rather than choosing another copy
from PATH. It supports standalone installations on Windows, macOS, and Linux
(`amd64`/`arm64`), requires a writable installation directory, and never
elevates permissions automatically. Use your package manager instead for
package-managed installations. Source builds are replaced with the published
binary, not rebuilt.

An older FAM without this command must first be upgraded using the installer
or a release archive. Equal versions are unchanged; downgrades and prereleases
are refused. This updates only FAM, not agents, Azure resources, `azd`, or its
extensions. See the [update command contract](docs/command-reference.md#update)
for authentication, automation, and recovery details.

### How the downloadable binaries are produced

Pushing a `v`-prefixed tag runs the release workflow which cross-compiles for
`linux`, `darwin`, and `windows` on `amd64` and `arm64`, publishes `SHA256SUMS`,
and attaches build-provenance attestations (public repos).

## Command organization

The CLI is one executable organized into resource namespaces. Root help stays
small and points to focused areas:

```powershell
fam help
fam help prompt
fam help hosted session
fam help project connection
fam help memory item
```

Commands use noun-first paths such as `prompt deploy`, `hosted deploy`,
`project connection list`, and `memory item create`. Shell completion follows
the same hierarchy.

The earlier flat names remain supported as hidden compatibility aliases, so
existing automation such as `fam hosted-deploy` continues to
run. They are intentionally omitted from root help and completion suggestions;
new scripts and documentation should use the nested command paths.

## Quick start: Prompt agent

**Goal:** deploy an instructions-based agent to an existing Foundry project
and inspect its first version. No custom application runtime is needed.

### 1. Gather the Azure inputs

| You need | What to use |
|---|---|
| A Foundry account and child project in AzureCloud | The project's full ARM resource ID, not its HTTPS endpoint |
| An existing model deployment | Its **deployment name**, which can differ from the catalog model name |
| An Azure identity FAM can resolve | A supported developer credential or workload identity with the [Prompt operator permissions](docs/rbac-and-separation-of-duties.md#prompt-agents) |

The parent account must already exist. If the project or model is missing,
use the [explicit resource-creation workflow](#prompt-agent-with-a-new-foundry-child-project)
and [model deployment guide](docs/prompt-agents.md#model-deployment-lifecycle)
before online preflight. Creation is not part of the default quickstart.

### 2. Create and review the manifest

Replace both quoted placeholders below with your Azure values. The new
filename keeps this manifest separate from the offline `agent.yaml` example:

```powershell
fam prompt init -f azure-agent.yaml --name support-agent --no-tools `
  --model "<model-deployment-name>" `
  --project-resource-id "<full-foundry-project-resource-id>"
```

Open `azure-agent.yaml` and replace `agent.instructions` with the behavior
you want. Keep the other generated fields, then review the local result:

```powershell
fam prompt validate -f azure-agent.yaml
fam prompt plan -f azure-agent.yaml
```

**Expected result:** structure-only validation succeeds and the plan shows
the intended project, agent, and model. `--no-tools` keeps the first deployment
small; [add tools later](docs/tools-and-grounding.md). The default guardrail is
inherited from the model deployment; a custom policy is
[optional](docs/prompt-agents.md#manifest-reference).

Prefer guided input? Run `fam quickstart --type prompt` **instead of**
the init command, choose a new destination, and follow its printed next steps.

### 3. Check access, then deploy

Run the read-only Azure check first:

```powershell
fam prompt preflight -f azure-agent.yaml
```

Continue only after it succeeds. The next command creates an agent version
in Azure; normal service charges apply.

```powershell
fam prompt deploy -f azure-agent.yaml --if-changed
fam prompt status -f azure-agent.yaml
```

**Expected result:** the first deployment activates its initial version and
writes a redacted receipt under `.foundry-agent-manager/receipts/`. Status
shows the deployed and active version. No test message has been sent.

Later deployments stage changes behind the active version. Use the actual
version returned by FAM when [reviewing and promoting a later release](docs/prompt-agents.md#staged-versions-promotion-and-rollback);
do not assume it is version `1`.

**If blocked:** start with [Prompt troubleshooting](docs/faq.md#where-should-troubleshooting-start).
**Next:** [invoke the agent](docs/prompt-agents.md#smoke-tests) (billable) or
[explore lifecycle operations](#common-next-steps-after-your-first-deployment).

## Quick start: Hosted agent

**Goal:** create a workspace for custom application code, then deploy it using
the pinned Hosted Agent workflow. **Hosted Agents are preview features.**

### 1. Create and inspect a workspace offline

Start in a directory where `hosted-demo` does not already exist:

```powershell
fam hosted init --destination hosted-demo --name support-agent
fam hosted validate --workspace hosted-demo
fam hosted plan --workspace hosted-demo
```

**Expected result:** a new `hosted-demo` workspace containing `azure.yaml` and
starter source, successful local validation, and a description of the proposed
deployment actions. No Azure resources, azd environment, or authentication
session have been created. You can stop here without installing `azd`.

<details>
<summary>Already have Python source? Adopt it instead of generating starter code</summary>

Replace the source path with your application's directory. Copy mode leaves
the original source untouched and requires a new destination:

```powershell
fam hosted adopt --source .\existing-python-agent --destination adopted-agent --name support-agent
fam hosted validate --workspace adopted-agent
fam hosted plan --workspace adopted-agent
```

Continue below with `adopted-agent` instead of `hosted-demo`. Review any
reported source compatibility actions before deployment. See the
[adoption guide](docs/hosted-agents.md#adopt-existing-python-source) for
dependency requirements, entry points, and explicit in-place adoption.
If you already have `azure.yaml`, use the
[existing-workspace path](#existing-hosted-agent-workspace) instead.

</details>

### 2. Prepare for Azure deployment

| You need before online operations | Requirement |
|---|---|
| Foundry coordinates | An existing AzureCloud project ARM resource ID, model deployment name, and matching Azure location |
| Azure Developer CLI | `azd` 1.32.0 or later |
| Hosted extension | Exactly `azure.ai.agents` `1.0.0-beta.13` |
| Credentials and permissions | Separately authenticated azd and a FAM-resolvable Azure identity with the [Hosted operator permissions](docs/rbac-and-separation-of-duties.md#hosted-agents) |
| Deployment resources | Already-provisioned infrastructure, or an explicit decision to use the provisioning alternative below |

Install the extension and authenticate azd for the target tenant. Replace the
quoted placeholder; FAM must also be able to resolve an Azure identity with
the required permissions:

```powershell
azd extension install azure.ai.agents --version 1.0.0-beta.13
azd auth login --tenant-id "<tenant-id>"
```

Configure the workspace's local environment. Replace all quoted placeholders:

```powershell
fam hosted environment create `
  --workspace hosted-demo --environment dev `
  --project-id "<full-foundry-project-resource-id>" `
  --model-deployment "<model-deployment-name>" `
  --location "<azure-location>"
```

**Expected result:** a local azd environment with project, model, region, and
derived endpoint settings. This does not create Azure resources, grant RBAC,
or log in. Default-generated workspaces use `Microsoft.DefaultV2`; see
[guardrail options](docs/hosted-agents.md#agent-guardrails) for deliberate changes.

Prefer guided setup? Run `fam quickstart --type hosted` **instead of** manually
creating a workspace and environment. It offers source adoption and asks
before environment bootstrap. Answer **no** to bootstrap for files-only use.

### 3. Preflight, deploy, and inspect

Check the environment and permissions without mutation:

```powershell
fam hosted preflight --workspace hosted-demo --environment dev --accept-preview
```

After preflight succeeds, deploy into **already-provisioned resources**:

```powershell
fam hosted deploy --workspace hosted-demo --environment dev --accept-preview --if-changed
fam hosted status --workspace hosted-demo --environment dev --accept-preview
```

**Expected result:** a deployed Hosted Agent version, status information, and
a redacted deployment receipt. Normal Azure charges apply. FAM does not send
a test message as part of deployment.

<details>
<summary>Need infrastructure provisioning? Use this explicit, potentially billable alternative</summary>

A generated workspace is not provisioned infrastructure. Review `azure.yaml`
and any infrastructure definitions and obtain the required
[provisioning permissions](docs/rbac-and-separation-of-duties.md#hosted-agents)
before opting in.

First inspect the proposed workflow offline:

```powershell
fam hosted plan --workspace hosted-demo --environment dev --provision --preview-provision
```

Only after review, replace the non-provisioning deploy command with:

```powershell
fam hosted deploy --workspace hosted-demo --environment dev --accept-preview --provision --preview-provision
```

This can create billable infrastructure. `--provision` is off by default.
See the [deployment contract](docs/hosted-agents.md#deployment-commands)
for environment setup and preview boundaries.

</details>

**If blocked:** use [Hosted inspection and diagnostics](docs/hosted-agents.md#inspection-and-diagnostics).
**Next:** [invoke the agent](docs/hosted-agents.md#smoke-tests) (billable) or
explore [versions, sessions, and logs](docs/hosted-agents.md).

## What commands are safe to run?

Every command falls into one of these categories. When in doubt, start with
offline commands and work your way down.

| Safety level | What happens | Examples |
|---|---|---|
| **Offline** | No Azure authentication or API calls; scaffolding still writes local files. | `prompt validate`, `prompt plan`, `prompt init`, `hosted init`, `hosted validate`, `hosted plan`, non-interactive `quickstart` without `--bootstrap-environment`, `version` |
| **Local mutation** | Changes local workspace/tool state, not Azure resources. Some operations contact external services. | `hosted adopt`, `hosted environment create`, Hosted `quickstart` environment bootstrap, `update` (GitHub download; `--check` does not replace the executable) |
| **Read-only online** | Contacts Azure to inspect or verify state without intentional mutation. | `prompt preflight`, `model deployment list/show/plan`, `prompt status`, `prompt diff`, `agent365 blueprint show`, `agent365 binding status`, `agent365 identity list`, `agent365 integration status`, `agent365 observability status`, `agent365 publication status`, targeted `doctor --online` |
| **Mutating** | Creates, updates, or routes resources. Mutation receipts are written where the command contract provides them. | `model deployment create`, `prompt deploy`, `prompt promote`, `project create`, `agent365 integration set` |
| **Billable invocation** | Invokes an AI capability and may incur normal service usage charges. | `prompt smoke`, `hosted smoke`, `memory search` |
| **Destructive** | Deletes resources and requires confirmation; `--yes` enables non-interactive use. | `model deployment delete`, `prompt delete`, `prompt versions prune`, `prompt decommission` |

Use each command's `--help` before a mutation. Commands such as `prompt versions prune` that
support `--dry-run` can preview their deletion scope. See the full
[Command Reference](docs/command-reference.md) for command families and shared
options; focused command help lists individual flags.
Bare `fam help` shows the top-level namespaces and getting-started commands, while
`fam help quickstart` and other `help <command path>` requests
show only that namespace or command's subcommands, usage, examples, flags, and
related workflow.

## Agent 365 blueprint inspection

Agent 365 blueprints are identity templates, not agent source. The manager can
list, show, validate, and inspect blueprint permissions, owners, sponsors, and
identities through Microsoft Graph; list and show Agent ID identities and
blueprint principals; compare a blueprint with a deployed Prompt or Hosted
Agent; manage Foundry account integration logging; inspect observability
readiness; and plan publication handoff:

```powershell
fam agent365 blueprint show `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444

fam agent365 blueprint permissions `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  --resolve-names

fam agent365 identity list

fam agent365 binding plan `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  -f agent.yaml

fam agent365 integration status `
  --account-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry

fam agent365 observability plan `
  --workspace C:\src\hosted-agent
```

The binding plan is intentionally non-executable. A matching ID is correlation
evidence; a non-match is not repaired with metadata or an undocumented API.
See [Agent 365 Blueprints, Identity, Integration, Observability, and Publication](docs/agent365.md).

## Choose your deployment path

Use the shortest workflow that matches what you are deploying.

### Prompt agent with a new Foundry child project

The parent Foundry account must already exist. This workflow gives the agent
its own child project and deployable manifest. A missing model deployment can
be planned and created explicitly before Prompt preflight; it is never created
as a side effect of Prompt deployment.

```powershell
fam prompt init -f agent.yaml `
  --name support-agent `
  --model my-model-deployment `
  --no-tools `
  --metadata owner=platform-team `
  --metadata environment=development `
  --project-resource-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/my-resource-group/providers/Microsoft.CognitiveServices/accounts/my-foundry-account/projects/support-project `
  --location eastus

fam prompt validate -f agent.yaml
fam project create -f agent.yaml
```

The model name above must refer to an existing deployment. **If it does not
exist**, first add the exact `model_deployment` desired state from the
[model deployment guide](docs/prompt-agents.md#model-deployment-lifecycle),
then run `fam model deployment plan -f agent.yaml` and
`fam model deployment create -f agent.yaml`. The init command does not generate
model creation settings, and creation can incur Azure charges.

Once both project and model deployment exist, continue:

```powershell
fam prompt preflight -f agent.yaml
fam prompt deploy -f agent.yaml --if-changed
fam prompt status -f agent.yaml
```

Custom metadata is an optional string map. Put durable values under
`agent.metadata` or pass repeatable global `--metadata key=value` overrides.
The resolved values are copied to Prompt Agent versions, every generated
receipt, and the optional Log Analytics `Metadata` dynamic column. Foundry
allows at most 16 entries; never place credentials or other secrets in
metadata.

### New Hosted Agent workspace

`hosted init` creates the local workspace only. Review the generated files,
create or select the required `azd` environment, and set its required values
before online commands. The result is a working starter repository structure,
not a deployed Azure resource, so teams can review the code and infrastructure
contract before accepting preview behavior or provisioning.

```powershell
fam hosted init `
  --destination support-hosted `
  --name support-agent `
  --protocol responses

fam hosted validate --workspace support-hosted
fam hosted plan --workspace support-hosted --environment prod

# One-time local azd environment setup and existing-project context:
fam hosted environment create `
  --workspace support-hosted --environment prod `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model-deployment support-model --location eastus2

fam hosted preflight --workspace support-hosted `
  --environment prod --accept-preview

fam hosted deploy --workspace support-hosted `
  --environment prod --accept-preview --if-changed

fam hosted status --workspace support-hosted `
  --environment prod --accept-preview
```

Replace the example project, deployment, and location with your values.
The deployment above assumes already-provisioned resources and the required
credentials/tooling. If infrastructure is missing, review the
[explicit provisioning alternative](#quick-start-hosted-agent) instead of
treating workspace creation as Azure provisioning.

### Existing Hosted Agent workspace

Use this path to bring an existing `azure.yaml` workspace under consistent
validation, preflight, change detection, receipts, and lifecycle management
without regenerating or replacing the application.

```powershell
fam hosted validate --workspace C:\src\hosted-agent
fam hosted plan --workspace C:\src\hosted-agent --environment prod
fam hosted environment create `
  --workspace C:\src\hosted-agent --environment prod `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model-deployment support-model --location eastus2
fam hosted preflight --workspace C:\src\hosted-agent `
  --environment prod --accept-preview
fam hosted deploy --workspace C:\src\hosted-agent `
  --environment prod --accept-preview --if-changed
fam hosted status --workspace C:\src\hosted-agent `
  --environment prod --accept-preview
```

`hosted environment create` is idempotent for an existing workspace. Supply
`--project-id` and `--model-deployment` to configure existing-project context.
Add `--tenant-id` to record the target tenant. Endpoint and subscription are
derived from the project resource ID. `--location` is required because azd
uses `AZURE_LOCATION` during Hosted deployment. The endpoint is written to azd's canonical
`FOUNDRY_PROJECT_ENDPOINT` value and the `AZURE_AI_PROJECT_ENDPOINT`
compatibility alias. Setting `AZURE_TENANT_ID` does not authenticate azd;
sign in to that tenant separately.

### CI/CD with structured output and receipts

Use structured output when another system needs to make decisions from command
results. Stable JSON fields go to stdout, diagnostics go to stderr, and the
receipt preserves redacted mutation evidence for later review.

```powershell
fam prompt preflight -f agent.yaml --output json
fam prompt deploy -f agent.yaml `
  --if-changed --output json --receipt artifacts\deploy-receipt.json
```

Structured results are written to stdout, diagnostics and error envelopes to
stderr, and mutation evidence to the explicit receipt path.

To publish each completed receipt to Log Analytics, configure an existing Logs
ingestion endpoint and DCR:

```powershell
fam prompt deploy -f agent.yaml --if-changed `
  --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com `
  --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef
```

The local file is always written first. If ingestion fails, retry it with
`fam receipt upload --file <receipt-path>`. See
[Log Analytics Receipts](docs/log-analytics-receipts.md) for the stream schema,
RBAC, metadata migration, environment variables, KQL, and the standalone
[table and DCR setup script](https://raw.githubusercontent.com/jpmicrosoft/fam/main/scripts/Initialize-LogAnalyticsReceipts.ps1).

## Doctor — environment readiness

Use `doctor` before a deployment to separate local configuration, manifest,
workspace, authentication, and tooling problems from an actual deployment
failure. It is useful for onboarding a developer machine and for proving that
a CI runner is ready without changing Azure resources.

```powershell
# Check the binary and AzureCloud boundary:
fam doctor

# Validate one Prompt manifest locally:
fam doctor -f agent.yaml

# Validate one Hosted workspace locally:
fam doctor --workspace hosted-agent

# Add read-only Prompt authentication and project checks:
fam doctor -f agent.yaml --online --fail-on-not-ready

# Add Hosted tooling, authentication, environment, project-access, and
# provision-contract checks:
fam doctor --workspace hosted-agent --environment prod `
  --online --accept-preview --check-provision

# Add detailed redacted support diagnostics on stderr:
fam doctor -f agent.yaml --online --debug
```

`doctor` **never mutates resources**. It reports every independent check it can
complete instead of stopping at the first problem. Each check includes a
category, severity, duration, observed/required values when applicable, and
remediation.

Readiness fields distinguish what was actually proven:

| Field | Meaning |
|---|---|
| `ready` | All checks requested by the selected `scope` passed. |
| `localReady` | The target and its contained local dependencies are valid. |
| `onlineReady` | Requested authentication, tooling, environment, and connectivity checks passed. Omitted unless `--online` is used. |
| `deploymentReady` | All supported local and online deployment prerequisites passed. Omitted unless `--online` is used. |
| `checksComplete` | No requested check was blocked by a failed prerequisite. |
| `coverageComplete` | Whether every possible external dependency was verified. This is normally `false` because doctor intentionally skips billable invocations and service-owned validation boundaries. |

By default, diagnostic failures remain a report with process exit `0`; use
`--fail-on-not-ready` in CI to return exit `11` after the complete report is
written. `--debug` is global, implies verbose diagnostics, and writes only
redacted request/command metadata to stderr. It never prints command arguments,
environment values, HTTP query strings, headers, or bodies.

Known unverified boundaries are explicit `skipped` checks rather than hidden
assumptions. These include quota/capacity, provisioning roles, downstream
identity RBAC, and billable smoke tests. Configured Prompt and Hosted RAI
policies are verified through ARM before deployment. Hosted `azd`
authentication and the tool's
`DefaultAzureCredential` project probe are also reported separately because
they can resolve to different identities. If the supported `azd` version or
reviewed Hosted Agent extension cannot be proven first, extension commands,
project endpoint resolution, and credential probes are blocked rather than
executed.

## Common next steps after your first deployment

| What you want to do next | Command |
|---|---|
| See what is deployed | `fam prompt status -f agent.yaml` or `fam hosted status --workspace hosted-demo --environment dev --accept-preview` |
| Check for drift between your manifest and Azure | `fam prompt diff -f agent.yaml` |
| Send a test message to the deployed agent | `fam prompt smoke -f agent.yaml --prompt "Hello"` (billable) |
| Deploy a new version without disrupting production | `fam prompt deploy -f agent.yaml --if-changed` (stages later versions) |
| Send production traffic to a reviewed version | `fam prompt promote -f agent.yaml --agent-version "<version>"` |
| Preview a rollback to an earlier version | `fam prompt rollback -f agent.yaml --agent-version "<version>" --dry-run`; omit `--dry-run` to apply with confirmation |
| Add documents the agent can search | See [Grounding](docs/tools-and-grounding.md#managed-document-grounding) |
| Publish to Microsoft Teams | See [M365 publishing](docs/prompt-agents.md#microsoft-365-and-teams-publishing) |
| Set up CI/CD | See [CI Templates](docs/ci-templates/) |
| Preview cleanup of old versions | `fam prompt versions prune -f agent.yaml --keep 3 --dry-run`; replace `--dry-run` with `--yes` only after review |

Use your actual manifest path (`azure-agent.yaml` in the Prompt quickstart)
and a version returned by FAM in place of `"<version>"`.

## Quick troubleshooting

| Problem | Likely cause | Fix |
|---|---|---|
| `project.resource_id is invalid` | Malformed Azure resource ID | Provide a valid Foundry project resource ID with the correct provider and UUID subscription |
| `destination host "..." is not approved` | The manifest references an external host you haven't approved | Add `--trusted-apim-host` or `--trusted-tool-host` with the exact hostname |
| `Foundry project "..." does not exist` | The project hasn't been created yet | If creation is intended and authorized, run `fam project create -f agent.yaml` first |
| `Hosted Agent preview was not explicitly accepted` | Missing flag | Add `--accept-preview` to the command |
| `Azure Developer CLI version is too old` | `azd` needs updating | Install `azd` 1.32.0 or later |
| Commands work locally but fail in CI | CI runner missing credentials or tools | Run `fam doctor -f agent.yaml --online` or the Hosted workspace equivalent in CI |

For task-specific answers, use the [FAQ troubleshooting section](docs/faq.md#troubleshooting).
For the full error/remediation table, see
[Security and Operations — Troubleshooting](docs/security-and-operations.md#troubleshooting).

## VS Code integration

Editor integration catches schema mistakes while the user is writing a
manifest, before a terminal or CI run is needed. Users gain field completion,
inline validation, and hover documentation tied to the same schemas enforced
by the CLI.

The repository includes `.vscode/settings.json` configuring YAML and JSON
schema validation for agent manifests and publication configs, and
`.vscode/extensions.json` recommending the
[`redhat.vscode-yaml`](https://marketplace.visualstudio.com/items?itemName=redhat.vscode-yaml)
extension.

When VS Code prompts to install recommended extensions, accept the YAML
extension to get inline validation, completion, and hover documentation for:
- `**/agent*.yaml` / `**/agent*.yml` → `schema/manifest.schema.json`
- `**/publication*.yaml` / `**/publication*.yml` → `schema/publication.schema.json`
- Matching JSON files use VS Code's built-in JSON schema support.

No additional configuration is needed; the schema paths are workspace-relative.

## CI/CD templates

Inert GitHub Actions workflow templates are provided in
[`docs/ci-templates/`](docs/ci-templates/):

The templates turn a manifest or Hosted workspace stored in Git into a
repeatable deployment process for a team. Instead of asking an operator to
install tools and run each command manually, GitHub Actions uses the same
reviewed sequence every time:

1. A change under `agents/` is merged to `main`, or an operator starts the
   workflow manually and selects `dev`, `staging`, or `production`.
2. The workflow signs in to Azure with short-lived GitHub OIDC credentials.
3. It installs the selected CLI version, validates the source, and runs
   read-only preflight checks before deployment.
4. It deploys only when the desired configuration changed and saves the
   redacted deployment receipt as a GitHub artifact.

This gives users consistent deployments, fewer workstation-specific mistakes,
protected-environment approvals, serialized deployments, and downloadable
evidence of what each run changed.

| Template | Description |
|---|---|
| [`deploy-prompt.yml`](docs/ci-templates/deploy-prompt.yml) | Prompt: validate → preflight → deploy with `--if-changed` |
| [`deploy-hosted.yml`](docs/ci-templates/deploy-hosted.yml) | Hosted: validate → plan → preflight → deploy with optional provisioning |

These are **templates, not active workflows**. Copy them to your repository's
`.github/workflows/` directory and customise placeholder values. See
[`docs/ci-templates/README.md`](docs/ci-templates/README.md) for required
variables, Azure OIDC setup, and environment configuration.

The templates do not create the Foundry account, create/delete a model
deployment implicitly, configure Azure OIDC/RBAC, approve external
destinations, or make Hosted infrastructure provisioning implicit. Add a
separately approved plan and `model deployment create` job when CI should own
that billable infrastructure mutation. The Prompt template also does not
promote a staged version to production traffic.

Key template features:
- Azure OIDC login (no long-lived secrets).
- Installer step with version pinning (`FAM_VERSION`), repository override
  (`INSTALLER_REPO`), and private-repo token support (`FAM_INSTALL_TOKEN`).
- Concurrency groups to prevent parallel deploys.
- Receipt artifact upload for audit.
- Hosted template installs pinned `azd` 1.32.0 and extension `1.0.0-beta.13`.

## Security principles

These controls exist because the CLI handles Azure credentials and
manifest-selected destinations. They keep trust decisions with the operator,
make automation fail predictably, and leave evidence without exposing secrets.

- **Manifest is untrusted input.** Every external destination must be
  explicitly approved from outside the manifest before deployment.
- **Fails closed.** Unknown hosts, ambiguous mutations, and unapproved
  destinations produce errors, never silent fallbacks.
- **No implicit credential egress.** Host pinning and exact approval prevent a
  manifest from directing tokens to unintended hosts.
- **Receipts are redacted.** APIM keys, tokens, and approval values never
  appear in receipts or output.
- **Immutable versions.** The first deployment activates the initial version.
  Later deployments stage candidates; `prompt promote` or `prompt rollback` changes existing
  production traffic.
- **AzureCloud boundary.** Cross-cloud endpoint and audience injection is
  rejected. Azure Government remains disabled until dedicated requalification.

See [`SECURITY.md`](SECURITY.md) for the full threat model.

## Detailed documentation

The README is the onboarding path. Use the detailed guides when you need to
choose a capability, understand its operational outcome, or configure its full
contract.

| Document | Contents |
|---|---|
| [docs/faq.md](docs/faq.md) | Practical answers for setup, deployment, instructions, safety, troubleshooting, and releases |
| [docs/glossary.md](docs/glossary.md) | Plain-language definitions of all key terms |
| [docs/command-reference.md](docs/command-reference.md) | All commands, global options, exit codes, error envelopes |
| [docs/rbac-and-separation-of-duties.md](docs/rbac-and-separation-of-duties.md) | Per-tool RBAC requirements, Foundry roles, operator personas, runtime identities, and separation-of-duties patterns |
| [docs/prompt-agents.md](docs/prompt-agents.md) | Manifest reference, tools, deploy, promote, M365, legacy |
| [docs/hosted-agents.md](docs/hosted-agents.md) | Hosted lifecycle, workspace, sessions, scaffold, Autopilot |
| [docs/agent365.md](docs/agent365.md) | Agent ID blueprint, identity, and principal inspection; permissions; Foundry correlation; integration logging; observability; publication; identity lifecycle and binding boundaries |
| [docs/tools-and-grounding.md](docs/tools-and-grounding.md) | Toolbox, Skills, grounding, connectors, API Center |
| [docs/security-and-operations.md](docs/security-and-operations.md) | Trust approvals, cloud support, reliability, troubleshooting |
| [docs/log-analytics-receipts.md](docs/log-analytics-receipts.md) | DCR-based Log Analytics receipt publishing, retries, RBAC, schema, and KQL |
| [docs/development-and-releases.md](docs/development-and-releases.md) | Testing, CI, repository layout, release process |
| [docs/ci-templates/](docs/ci-templates/) | Inert GitHub Actions workflow templates |

## Project documents

- [SECURITY.md](SECURITY.md) — threat model, trust boundary, vulnerability
  reporting.
- [CONTRIBUTING.md](CONTRIBUTING.md) — dev environment, required checks, review
  expectations.
- [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) — participation and enforcement
  expectations.
- [SUPPORT.md](SUPPORT.md) — supported channels for questions, defects, feature
  requests, and security reports.
- [CHANGELOG.md](CHANGELOG.md) — release history in Keep a Changelog format.

## License

This project is licensed under the [MIT License](LICENSE).
