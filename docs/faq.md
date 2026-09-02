# Frequently Asked Questions

Practical answers for installing, configuring, deploying, operating, and
troubleshooting `fam`.

Use [`command-reference.md`](command-reference.md) for the complete command and
flag catalog, and run `fam help <command>` for focused,
copyable examples.

## Contents

- [Choosing a deployment path](#choosing-a-deployment-path)
- [Installation and command-line usage](#installation-and-command-line-usage)
- [Prompt Agent manifests and instructions](#prompt-agent-manifests-and-instructions)
- [Projects, endpoints, and model deployments](#projects-endpoints-and-model-deployments)
- [Deployment, versions, and traffic](#deployment-versions-and-traffic)
- [Hosted Agents](#hosted-agents)
- [Agent 365 blueprints, identities, and integration](#agent-365-blueprints-identities-and-integration)
- [Tools, grounding, and integrations](#tools-grounding-and-integrations)
- [Security, credentials, and automation](#security-credentials-and-automation)
- [Troubleshooting](#troubleshooting)
- [Builds and releases](#builds-and-releases)

## Choosing a deployment path

### What does this tool manage?

It manages two Microsoft Foundry agent paths:

- **Prompt Agents:** a YAML or JSON manifest defines the model deployment,
  instructions, tools, project, and optional endpoint configuration.
- **Hosted Agents:** an `azure.yaml` workspace and application source are
  deployed through a pinned Azure Developer CLI (`azd`) and Hosted Agent
  extension contract.

The tool also provides related project, connection, grounding, Toolbox, Skill,
Memory, publishing, diagnostics, and lifecycle commands.

### Should I use a Prompt Agent or a Hosted Agent?

Use a **Prompt Agent** when the behavior can be expressed with instructions, a
model deployment, and declarative tools. Use a **Hosted Agent** when you need
custom Python or .NET code, a container or prebuilt image, controlled
CPU/memory, or application-owned runtime behavior.

### Can I evaluate the tool without Azure access?

Yes. Commands such as `prompt init`, `prompt validate`, `prompt plan`,
`hosted validate`, and `hosted plan` run locally. Non-interactive `quickstart`
also remains files-only unless `--bootstrap-environment` is supplied.
Interactive Hosted quickstart asks before creating/configuring local azd
environment state; it does not create Azure resources or deploy an agent.

### Does the tool create every required Azure resource?

No. The parent Foundry account must already exist. The tool can explicitly
create an account-scoped model deployment after a live `model deployment plan`,
and it can create a child Foundry project when complete ARM coordinates are
provided. Hosted infrastructure provisioning is available only through an
explicit opt-in.

### Which Azure clouds are supported?

Only `AzureCloud` is supported. Azure Government aliases are rejected before
credential acquisition or network access because this release has not been
qualified end to end in a dedicated Government subscription.

## Installation and command-line usage

### What tooling must I install?

The answer depends on the workflow:

| Workflow | Tooling |
|---|---|
| Offline validation, planning, and scaffolding | The published `fam` executable only |
| Online Prompt and other direct Foundry operations | `fam` plus a supported Azure identity. Azure CLI (`az`) is optional and can provide a local developer credential through `DefaultAzureCredential`. |
| Online Hosted Agent operations | `fam`, Azure Developer CLI (`azd`) 1.32.0 or later, and the **`azure.ai.agents`** azd extension at exactly **`1.0.0-beta.13`** |
| Building from source | Go 1.25 or later |

The Hosted extension is named `azure.ai.agents`. Install the required version
with:

```powershell
azd extension install azure.ai.agents --version 1.0.0-beta.13
```

Microsoft's `microsoft.foundry` bundle can install this component, but FAM
validates `azure.ai.agents` itself. Version `1.0.0-beta.13` declares
`azd >=1.32.0`; an older `azd` version is not a supported pairing.

Azure CLI and `azd` do not share authentication automatically. Run `az login`
when Azure CLI should supply FAM's local developer credential, and authenticate
`azd` separately for Hosted deployment:

```powershell
azd auth login --tenant-id <tenant-id>
```

### Do users need Go installed?

No. Published releases contain a standalone executable. Go 1.25 or later is
needed only to build or test the project from source.

### Does `go mod download` install Azure CLI or `azd`?

No. `go mod download` only downloads the Go modules declared in `go.mod` into
the Go module cache. It does not install:

- The `fam` executable
- Azure CLI (`az`)
- Azure Developer CLI (`azd`)
- The Hosted Agent `azure.ai.agents` extension

Install those command-line tools separately when the selected workflow requires
them. The tooling table above identifies which dependencies apply to each
workflow.

### Which operating systems and architectures are published?

Release archives are produced for Windows, macOS, and Linux on both `amd64`
and `arm64`.

### How do I confirm which executable I am running?

```powershell
fam version
fam version --output json
fam -version
```

Published binaries include version metadata and may include the source commit
and build timestamp.

### How do I get help for one command without printing the entire catalog?

```powershell
fam help deploy
fam hosted preflight --help
```

Focused help includes that command's usage, examples, flags, and related
workflow. Bare `fam help` prints the complete grouped catalog.

### Does the tool support shell completion?

Yes. It generates completion scripts for PowerShell, Bash, Zsh, and Fish:

```powershell
fam completion powershell | Out-String | Invoke-Expression
```

Run `fam completion <shell> --help` for persistent
installation guidance.

### How should I run a downloaded `install.ps1`?

Save it locally, open PowerShell in that directory, and run:

```powershell
.\install.ps1
```

If the file is elsewhere, pass its full path. On macOS or Linux, run it with
PowerShell 7:

```powershell
pwsh ./install.ps1
```

Do not run a PowerShell script with Bash, `cmd.exe`, or by double-clicking it.

### Why does PowerShell say that running scripts is disabled?

PowerShell execution policy blocked the downloaded script before the installer
started. After reviewing that the script came from the intended release or
repository, use a process-scoped policy rather than weakening the machine-wide
policy:

```powershell
Set-ExecutionPolicy -Scope Process -ExecutionPolicy Bypass
.\install.ps1
```

If an organization policy still blocks execution, use the manually downloaded
release archive or ask the administrator for the approved installation path.

### Why does PowerShell say the script is not digitally signed or is blocked?

Windows may mark downloaded files as originating from the internet. After
reviewing the script and its source, remove only that file's download marker:

```powershell
Unblock-File -Path .\install.ps1
.\install.ps1
```

Do not disable endpoint protection or signature enforcement globally.

### Why is `.\install.ps1` not recognized?

The script is not in the current directory, the filename changed during
download, or the shell is not PowerShell. Confirm:

```powershell
Get-ChildItem .\install.ps1
```

Then change to the correct directory or run the script by its full path.

### Why does the installer report `Could not determine latest release`?

The installer could not read GitHub's latest-release API. Common causes are:

- The repository is private and no usable token is available.
- GitHub API rate limiting.
- A proxy, firewall, TLS inspection policy, or DNS issue blocks GitHub.
- The repository has no published latest release.
- `-Repo` points to the wrong repository.

Try a known published tag to avoid latest-release discovery:

```powershell
.\install.ps1 -Version v0.16.2
```

For a private repository, authenticate `gh` or expose a read-capable token
through `FAM_INSTALL_TOKEN`, `GITHUB_TOKEN`, or `GH_TOKEN`.

### Why does a private repository return 404 or fail to download assets?

GitHub commonly hides private resources from unauthenticated callers. Confirm
that `-Repo` uses `OWNER/REPO` format and that the selected identity can read
the repository and its release assets:

```powershell
gh auth status
.\install.ps1 -Repo owner/repository -Version v0.16.2
```

The installer checks `FAM_INSTALL_TOKEN`, then `GITHUB_TOKEN`, then `GH_TOKEN`,
and finally `gh auth token`. Tokens are sent only as HTTP authorization headers
and are never printed.

### Why does `-Version` fail validation?

Use `latest` or a `v`-prefixed semantic version:

```powershell
.\install.ps1 -Version latest
.\install.ps1 -Version v0.16.2
```

`0.16.2` without the leading `v` is rejected.

### Why does the archive download return 404?

The selected tag might not exist, the release might not contain an archive for
the detected platform and architecture, or the repository/token might be
wrong. Confirm the tag and look for the exact expected asset:

```text
fam_<version>_windows_amd64.zip
fam_<version>_windows_arm64.zip
```

The installer supports only `x64` (`amd64`) and `arm64`.

### What should I do about `Unsupported architecture`?

The published installer contract supports only `x64` and `arm64`. Use a
supported machine or build from source on another architecture if the Go
toolchain and project dependencies support it. Do not rename an archive for a
different architecture.

### What should I do when checksum verification fails?

Stop. Do not bypass the checksum check. Delete the downloaded archive, confirm
the repository and version, and retry from a trusted network. A mismatch can
indicate a corrupted download, an incomplete release, a proxy rewriting
content, or an unexpected asset.

`Checksum for ... not found in SHA256SUMS` means the release checksum file does
not list the exact archive the installer selected. That release should be
treated as incomplete.

### Why does installation fail with access denied?

The selected install directory might not be writable, or an existing
`fam.exe` may still be running and locked. Close running
instances and choose a user-writable directory:

```powershell
.\install.ps1 -InstallDir "$env:LOCALAPPDATA\foundry-agent-manager"
```

Avoid running PowerShell as administrator unless the approved destination
actually requires administrative access.

### Why does installation finish but `fam` is not recognized?

The installer does not modify PATH unless `-ModifyProfile` is supplied. Either
run the full executable path printed by the installer or reinstall with:

```powershell
.\install.ps1 -InstallDir C:\tools -ModifyProfile
```

Open a new terminal afterward. User PATH changes do not update the already
running PowerShell process.

### Why does `install.ps1` fail on macOS or Linux?

Use PowerShell 7 (`pwsh`), not Windows PowerShell syntax interpreted by another
shell. The POSIX path also requires `tar` to extract `.tar.gz` archives and a
writable `$HOME/.local/bin` or explicit `-InstallDir`.

### What if my organization blocks GitHub downloads?

The installer requires access to the GitHub API for `latest` resolution and to
GitHub release assets for the archive and `SHA256SUMS`. Use a configured
enterprise proxy, request the required allowlisting, or follow the approved
manual process for transferring the release archive and checksum file. Do not
disable TLS validation or checksum verification.

### Can output be consumed by automation?

Yes. Use `--output json` or `--output yaml`. Successful structured output is
written to stdout. Structured errors are written to stderr with stable
`kind`, `message`, and `exitCode` fields, plus `nextSteps` when specific
remediation is available.

### What is the difference between `--verbose` and `--debug`?

`--verbose` writes progress diagnostics to stderr. `--debug` adds detailed,
redacted diagnostics and implies `--verbose`. Debug output excludes command
arguments, environment values, HTTP query strings, headers, and bodies.

### How do I know a long Hosted operation is still running?

Long-running Hosted `azd` phases show elapsed-time progress on stderr after a
short delay. Interactive terminals use a spinner; redirected text output uses
periodic heartbeat lines. Use `--progress plain` for stable log lines or
`--progress off` to disable them. `--quiet` also disables progress, and
structured output remains quiet unless `--verbose` is enabled.

## Prompt Agent manifests and instructions

### Where are Prompt Agent instructions defined?

Define them directly in the manifest:

```yaml
agent:
  name: support-agent
  model: <model-deployment-name>
  instructions: |
    Answer clearly and concisely.
    Say when required information is unavailable.
```

Instructions are part of the immutable Prompt Agent version.

### Can deployment instructions come from a separate file?

Yes:

```powershell
fam prompt deploy -f agent.yaml `
  --instructions-file instructions\support.md `
  --if-changed
```

The path must remain inside the manifest directory. Absolute paths, drive
paths, `..` traversal, and symlink or junction escapes are rejected. The file
is read with a size limit and embedded into the deployed version.

### Can instructions be passed as plain text on the `prompt deploy` command?

No. There is no plain-text `--instructions` flag. Put plain text in
`agent.instructions`, or use `--instructions-file`. Avoiding instruction text
in process arguments also keeps it out of shell history and process listings.

### Can I add or change instructions without creating another version?

No. Prompt Agent versions are immutable. Edit `agent.instructions` or supply
`--instructions-file`, then run `prompt deploy`. If the agent already has an active
version, the new version is staged and does not receive production traffic
until it is promoted.

### Does `--instructions-file` rewrite the manifest?

No. It is a command-line override for that command execution. Update the
manifest itself when the new instructions should become the repository's
long-term source of truth.

### Can instructions use runtime values?

Yes. Declare `agent.structured_inputs` and reference supported values with
`{{variableName}}` templates. The input schema becomes part of the immutable
agent definition, while callers provide the actual values at invocation time.
Each input must either set `required: true` or provide a `default_value` that
matches its schema; Foundry rejects optional inputs without defaults.

### Does the schema allow unknown fields?

No. Unknown properties are rejected. This catches misspellings and unsupported
configuration before deployment. The canonical schema is
[`../schema/manifest.schema.json`](../schema/manifest.schema.json).

### Can command-line flags override common manifest values?

Yes. Manifest commands support overrides including `--name`, `--model`,
`--description`, `--instructions-file`, `--project-resource-id`,
and `--location`.
Overrides are applied before schema and endpoint validation.

## Projects, endpoints, and model deployments

### How does project identity work?

`project.resource_id` is the full Azure resource ID of the Foundry project:

```yaml
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/contoso/projects/support
```

All coordinates are derived locally from this single field:
- Subscription ID, resource group, account name, project name
- Account endpoint: `https://contoso.services.ai.azure.com`
- Project endpoint: `https://contoso.services.ai.azure.com/api/projects/support`

No separate endpoint, subscription, resource group, or account name fields are
needed or accepted.

### Can the tool create a missing Foundry project?

Yes, but only the child project. Supply `project.resource_id` (the target
project resource ID) and `project.location`, then run `project create` or use
`deploy --ensure-project`. The parent account is derived from the project ID
and remains externally managed. Model deployment management is available through
the separate `model deployment` command family.

### Does `prompt preflight` verify the configured model deployment?

Yes. For an existing project, it performs an authenticated, read-only lookup
of the exact `agent.model` deployment through the Foundry project API. It does
not invoke the model or consume inference tokens.

When `--ensure-project` targets a missing child project, preflight verifies the
deployment and its provisioning state on the parent account through ARM.
Deployment checks project-scoped accessibility again after creating the child
project.

### What happens if `agent.model` is wrong?

Preflight fails with a `not_found` error before creating an agent version.
Ensure `agent.model` exactly matches a deployment available to the selected
project or account.

### Can the tool create a model deployment?

Yes, through an explicit account-scoped workflow:

```powershell
fam model deployment plan -f agent.yaml
fam model deployment create -f agent.yaml
```

`prompt deploy` never creates a model implicitly. The operator must define the
exact model, version, format, SKU, and capacity, review the live plan, and run
the create command separately.

### What is the difference between `agent.model`, `deployment_name`, and `model_name`?

- `agent.model` is the deployment name the Prompt Agent calls at runtime.
- `model_deployment.deployment_name` is the ARM deployment resource name and
  defaults to `agent.model`.
- `model_deployment.model_name` is the exact catalog or fine-tuned model
  identifier placed inside that deployment.

These values can be different. For example, deployment `support-prod` can
point to catalog model `gpt-5-mini`.

### How do I declare a model deployment?

```yaml
agent:
  model: support-prod

model_deployment:
  deployment_name: support-prod
  model_name: gpt-5-mini
  model_version: "2025-08-07"
  model_format: OpenAI
  sku_name: GlobalStandard
  capacity: 10
```

Optional fields include `rai_policy_name`, `version_upgrade_option`, and
`spillover_deployment_name`. The same desired fields can be supplied as flags;
run `model deployment plan --help` for the complete flag set.

### What does `model deployment plan` validate?

It is read-only but online. It verifies:

- The deployment name is absent, or already exists with the exact desired
  configuration and a `Succeeded` provisioning state.
- The exact model name, version, and format are available to the account and
  advertised in the account region.
- The requested SKU exists and the requested capacity satisfies its live
  minimum, maximum, step, or allowed-value constraints.
- The regional quota metric has enough available units.
- Azure reports enough current regional model capacity.
- Optional RAI policy and spillover deployment references exist.

The plan fails rather than warning when a required check cannot prove the
deployment is ready to create.

### Does `model deployment create` update an existing deployment?

No. An exact ready match returns `unchanged`. Any managed-field drift is
rejected so the command cannot silently replace or resize a billable
deployment. Delete and recreate deliberately when replacement is intended.

### Can model deployment creation incur charges?

Yes. Capacity can reserve or consume billable service resources, and
fine-tuned model deployments can have hosting charges. The create command is
always explicit and writes a redacted operation receipt. Review the plan,
pricing, quota, and organizational approval before creation.

### What permissions do model deployment commands need?

The selected `DefaultAzureCredential` identity needs ARM read access to the
Foundry account, deployment resources, model catalogs, usage, regional
capacity, and any referenced RAI policy. Create additionally needs deployment
write permission; delete needs deployment delete permission. Assign the
narrowest organization-approved Azure role that grants those actions rather
than relying on a broad owner role.

### How is model deployment deletion protected?

Preview the action with `--dry-run`. Actual deletion requires interactive
confirmation or `--yes`, waits until ARM confirms absence, and writes a
receipt. Deleting a deployment can break every agent or application that
references its deployment name.

### Does the model command manage Hosted Agent infrastructure?

Not by default. Hosted/azd workspaces keep model deployments in the selected
service's `azure.yaml` `deployments[]` declaration and provision them through
an explicit `azd provision`. The ARM `model deployment` commands are intended
for independently managed account deployments, especially Prompt Agents.

### Are all model providers and fine-tuned deployments supported?

The generic stable ARM deployment contract supports standard catalog models
and account-local fine-tuned model identifiers. Cross-account or cross-tenant
fine-tuned deployments require source-account fields and auxiliary
authorization that this initial workflow does not accept. Marketplace or
partner models may also require provider terms or fields outside the generic
contract; use the provider-specific Azure workflow when plan cannot validate
the requested model and SKU.

### Does `prompt preflight` create or change anything?

Standalone `prompt preflight` is read-only. It validates local inputs, resolves
approved destinations and secrets, checks project access, probes the data
plane, verifies the model deployment, resolves managed grounding references,
and inspects the optional APIM connection.

## Deployment, versions, and traffic

### What does `prompt deploy` do?

`prompt deploy` runs preflight and then creates an immutable Prompt Agent version when
needed. It can also reconcile optional project and APIM dependencies covered by
the manifest and selected flags. It writes a redacted receipt.

### What does `--if-changed` do?

It compares the desired manifest-managed fields with the latest remote version.
If they are equivalent, deployment reports `unchanged` instead of creating a
duplicate version.

```powershell
fam prompt deploy -f agent.yaml --if-changed
```

### Does every deployment immediately receive production traffic?

No. The first deployment activates the initial version. Later deployments
stage the new candidate behind the currently active version.

### How do I move traffic to a staged Prompt Agent version?

Review `prompt status`, `prompt versions list`, and `prompt show`, then promote the selected version:

```powershell
fam prompt promote -f agent.yaml --agent-version 3
```

Use `prompt rollback` to return traffic to an earlier verified version:

```powershell
fam prompt rollback -f agent.yaml --agent-version 2 --dry-run
fam prompt rollback -f agent.yaml --agent-version 2 --yes
```

### Can I modify an existing immutable version?

No. Changes to model, instructions, tools, structured inputs, description, or
RAI policy reference require another immutable version.

### Does `prompt endpoint configure` change the active version?

No. It applies stable-endpoint protocols, authorization, and agent-card
configuration without changing version routing.

### What are deployment receipts?

Receipts are redacted JSON records of mutating operations. They capture steps,
resource state, outcomes, and reconciliation guidance without storing tokens,
APIM keys, or trust-approval values.

### Can each user add their own receipt fields?

Yes. Use an arbitrary string map in `agent.metadata` or repeat the global
`--metadata key=value` option:

```powershell
fam prompt deploy -f agent.yaml --if-changed `
  --metadata owner=platform-team `
  --metadata environment=production
```

Command-line values override matching Prompt manifest keys. The resolved map is
written to the receipt and the Log Analytics `Metadata` dynamic column. Prompt
Agent versions receive the same map. Hosted Agent versions receive metadata
declared on their selected `azure.ai.agent` service in `azure.yaml`; Hosted
draft deployment also merges `--metadata` values.

Foundry allows 16 string entries, with keys up to 64 characters and values up
to 512 characters. Metadata is not a secret store because it can be visible in
Foundry, local receipts, and Log Analytics.

### What happens if I do not provide custom metadata?

Metadata is optional. A new agent version receives no metadata, receipts omit
the metadata field, and Log Analytics retains the earlier payload shape.
Existing Prompt Agent metadata is unmanaged: it does not cause drift and is
preserved if another change creates a new version. Use an explicit
`metadata: {}` in the Prompt manifest only when existing metadata should be
cleared.

### Can receipts be written to a Log Analytics workspace?

Yes. Configure an existing Azure Monitor Logs ingestion endpoint and DCR with
`--receipt-log-endpoint` and `--receipt-log-dcr-id`. Every receipt-producing
command then writes the local file first and publishes the completed redacted
receipt. The stream defaults to `Custom-FoundryAgentReceipts`.

See [Log Analytics Receipts](log-analytics-receipts.md) for the table schema,
DCR stream, metadata migration, RBAC, environment variables, and KQL.

### Why is a DCR required, and how do I create one?

The Log Analytics workspace stores records, but the DCR defines the accepted
JSON schema, destination table, stream mapping, and optional transformation.
The Logs Ingestion API also requires the DCR's immutable `dcr-...` identifier
on every request. The DCR resource ID is different and is used for RBAC scope.

Follow the copy-ready
[custom table and DCR creation procedure](log-analytics-receipts.md#create-the-custom-table-and-dcr).
It includes the supplied table definition and ARM template, exact stream names,
role assignment, endpoint and immutable-ID discovery, and an ingestion
verification query.

### What happens when Log Analytics receipt publishing fails?

The Azure or Foundry operation does not become a false success. The CLI returns
an error, preserves the terminal local receipt, and prints a retry command.
Use `fam receipt upload --file <receipt-path>` with the same
`--receipt-log-*` settings. Because a lost POST response can be ambiguous,
de-duplicate retries by the stable `ReceiptId`.

### What permissions are required to publish receipts?

The `DefaultAzureCredential` principal needs Logs ingestion access to the DCR.
Microsoft documents **Monitoring Metrics Publisher** as the built-in role, or a
custom role can grant the narrower `Microsoft.Insights/Telemetry/Write` data
action. The CLI does not create role assignments.

### Does publishing expose secrets?

Registered credentials are redacted before both local persistence and upload.
Receipts can still contain local paths, resource IDs, names, and error details,
so protect the workspace with appropriate RBAC, retention, and export policies.

### What should I do when deployment reports a receipt after an error?

Inspect the receipt before retrying. A mutation may have reached Azure even
when the client lost the response. Follow any `reconcile-*` guidance instead
of blindly repeating a non-idempotent operation.

### Is `prompt smoke` part of preflight?

No. `prompt smoke` invokes the model and can incur normal inference charges.
Preflight intentionally avoids billable invocation.

### Can old versions be removed automatically?

Use `prompt versions prune` with a reviewed retention count:

```powershell
fam prompt versions prune -f agent.yaml --keep 3 --dry-run
fam prompt versions prune -f agent.yaml --keep 3 --yes
```

Destructive commands require confirmation and protect versions that cannot be
safely deleted.

## Hosted Agents

### What additional software do Hosted Agents require?

Hosted Agents require:

- `azd` 1.32.0 or later
- Hosted Agent extension `azure.ai.agents` exactly `1.0.0-beta.13`
- An `azure.yaml` workspace
- `--accept-preview` for online Hosted commands

The manager validates this contract but never installs or upgrades the
extension automatically.

### What does Hosted quickstart configure?

Interactive Hosted quickstart asks whether to bootstrap the generated
workspace's azd environment and defaults to **yes**. When accepted, it creates
or reuses the selected environment and configures the supplied Foundry project
resource ID, model deployment, required location, and optional tenant. Endpoint
and subscription are derived locally. This changes local azd state only; it does not
authenticate azd, assign RBAC, provision Azure resources, or deploy the agent.
The project endpoint is stored as azd's canonical
`FOUNDRY_PROJECT_ENDPOINT` value and as the `AZURE_AI_PROJECT_ENDPOINT`
compatibility alias. `--project-id` stores `AZURE_AI_PROJECT_ID`, which enables
azd's project RBAC diagnostic. `--tenant-id` stores `AZURE_TENANT_ID` but does
not change azd's cached login.

For automation, quickstart remains files-only unless
`--bootstrap-environment` is explicit. Use `--project-id`, `--model`, and
`--location` with that flag; add optional `--tenant-id` for cross-tenant context.
Endpoint and subscription are derived from the project resource ID.

### How do guardrails work for Prompt and Hosted Agents?

Prompt quickstart and `prompt init` inherit the model deployment guardrail when
`--guardrail-policy-id` is omitted. Supplying the flag writes
`agent.rai_policy_id` and requires a full policy resource ID from the same
Foundry account.

Hosted quickstart, `hosted init`, and `hosted adopt` default to
`Microsoft.DefaultV2`. Use `--guardrail-policy-id` for a same-account custom
policy or explicit `--no-guardrail` to omit the agent-level policy. Hosted
guardrails are deployment metadata; BYO Python code does not need an
`RAI_POLICY_ID` block.

For a policy-less workspace, `hosted preflight`, `hosted deploy`, and
`hosted draft deploy` require `--no-guardrail` again as an explicit online
acknowledgement. The flag is rejected when the workspace already configures a
policy. `AZURE_AI_PROJECT_ID` must still match the resolved project endpoint.
Configured policies are verified before mutation, deploy repeats the checks
after optional provisioning, and draft deployment also serializes and verifies
the policy on the created version. The manager's
`DefaultAzureCredential` identity therefore needs account-level RAI policy read
permission; azd authentication is checked separately.

### How do I deploy an existing Python agent as a new Hosted Agent?

Use `fam hosted adopt`. Copy mode is non-destructive and creates a new managed
workspace:

```powershell
fam hosted adopt `
  --source C:\src\existing-python-agent `
  --destination adopted-agent --name support-agent `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model support-model --location eastus2 `
  --bootstrap-environment
```

The same workflow is available through
`fam quickstart --type hosted --source C:\src\existing-python-agent`.
Use `--in-place` instead of `--destination` only when the original folder should
become the workspace.

The command detects a conventional Python entry point and accepts
`requirements.txt`, `pyproject.toml`, or `setup.py`. If the selected entry point
does not contain the expected `ResponsesHostServer` or
`InvocationsHostServer` marker, adoption still preserves the source but prints
a review action; arbitrary application frameworks cannot be rewritten
safely without knowing their agent-construction contract.

### Why does `hosted preflight` say the azd environment does not exist?

`hosted preflight` is read-only and does not create environments. Create one
once from the workspace:

```powershell
fam hosted environment create `
  --workspace C:\src\hosted-agent --environment prod `
  --project-id /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project `
  --model-deployment support-model --location eastus2
```

Add `--tenant-id` for cross-tenant context. The command derives the project
endpoint and subscription
from the project resource ID, creates/reuses the environment, and sets all required
azd values before preflight. Interactive Hosted `quickstart` performs the same
bootstrap by default; non-interactive quickstart does so when
`--bootstrap-environment` is passed.

### Why does azd doctor skip the project role check?

The role check requires `AZURE_AI_PROJECT_ID`, the full Foundry project resource ID.
An endpoint alone is not enough. Rerun quickstart or `hosted environment create`
with `--project-id <project-resource-id>`, then rerun `hosted preflight`.

### Why does Hosted preflight report HTTP 403 after quickstart?

Quickstart configures local azd project/model/tenant context, but it does not
change the global azd login or assign Azure roles. A 403 means azd is
authenticated to the wrong tenant or its deployment identity lacks access to
the target project.

Reauthenticate azd to the project tenant when necessary:

```powershell
azd auth logout
azd auth login --tenant-id <tenant-id>
```

Then ensure that identity has `Foundry Project Manager` on the target project
and rerun `hosted preflight`. Preflight uses azd's own read-only diagnostics so
this identity mismatch fails before `hosted deploy`.

### What does `--cwd` mean in an `azd` command?

It tells `azd` which workspace directory to operate from without requiring you
to change the current PowerShell directory first.

### Does `hosted deploy` provision infrastructure automatically?

No. It runs `azd deploy <service> --no-prompt` by default and never runs
`azd up`. Provisioning requires explicit `--provision --preview-provision`
approval.

### Where do Hosted Agent instructions live?

Hosted instructions and behavior are application-owned. Define them in the
workspace source code or its configuration, then redeploy the Hosted Agent.
The Prompt Agent `agent.instructions` and `--instructions-file` surfaces do
not patch Hosted application behavior.

### Can Hosted Agent behavior be changed without redeployment?

Application code and configuration changes require another Hosted deployment.
Session state and sandbox files can be managed independently, but they do not
replace the deployed application definition.

### Can `hosted smoke` test every protocol?

No. It supports the documented `responses` and `invocations` flows.
`invocations_ws` requires a dedicated WebSocket client.

### Can a Hosted draft version receive endpoint traffic?

No. Draft versions cannot receive endpoint traffic. Deploy a regular version
before promoting or routing traffic.

## Agent 365 blueprints, identities, and integration

### Can the manager deploy an agent from an Agent 365 blueprint?

No. A blueprint is an identity template, not agent source, instructions, model
configuration, or a Hosted `azure.yaml` workspace. Deploy the agent through the
normal `prompt` or `hosted` path. Use `agent365 blueprint` commands to inspect
the existing identity template separately.

### Can the manager bind an existing Agent 365 ID or blueprint to an existing agent?

Not through a documented API. No currently documented Foundry mutation binds
an arbitrary existing blueprint or Agent ID to an existing Prompt Agent, Hosted
Agent, immutable version, endpoint, Agent Application, or Autopilot. The
manager omits binding create/delete commands and never reports success after
writing only local metadata.

### What can the Agent 365 commands do?

They can list, show, and validate Agent ID blueprints; show requested and
inheritable permissions (with optional `--resolve-names`); show blueprint
owners, sponsors, and identities; list and show Agent ID identities and
blueprint principals; show Foundry identity fields; compare an existing
blueprint with a Prompt or Hosted Agent; manage Foundry account integration
logging; inspect observability readiness; and plan publication handoff:

```powershell
fam agent365 blueprint validate `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444

fam agent365 binding status `
  --blueprint-id 00001111-aaaa-2222-bbbb-3333cccc4444 `
  -f agent.yaml

fam agent365 identity list

fam agent365 integration status `
  --account-id /subscriptions/$env:AZURE_SUBSCRIPTION_ID/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/contoso-foundry
```

The output is read-only correlation evidence, not a write operation (except
`integration set`, which mutates the logging flag with `--yes`).

### Which blueprint ID should I pass?

Use `--blueprint-id` for the blueprint application/client ID (`appId`). Use
`--blueprint-object-id` for the Microsoft Entra directory object ID (`id`).
They are different GUIDs and the flags are mutually exclusive.

### What Microsoft Graph permission is required?

The following Graph permissions are used:

- `AgentIdentityBlueprint.Read.All` — blueprint commands
- `AgentIdentity.Read.All` — identity commands
- `AgentIdentityBlueprintPrincipal.Read.All` — blueprint principal commands
- `Application.Read.All` — sponsor display names, permission friendly names
  (`--resolve-names`), and observability app-role assignment inspection

For delegated access, owners can read their
blueprints without an Agent ID role; non-owners also need the **Agent ID
Administrator** role. Use `--tenant-id` when the blueprint belongs to a
different tenant.

### Can a Foundry agent have both Foundry and Agent 365 identities?

Multiple lifecycle identities can coexist. Foundry can expose a runtime
`instance_identity`, blueprint identity information, and a blueprint reference;
supported publishing can create or use additional managed identities. This
does not merge the identities or their permissions.

### Which identity receives Azure RBAC?

Grant RBAC to the principal that actually receives the downstream token.
`AgenticIdentityToken` uses the agent `instance_identity`;
`ProjectManagedIdentity` uses the project identity. Agent 365 governance or a
blueprint relationship does not automatically copy Azure RBAC between
principals.

### Can I deploy first and attach Agent 365 later?

You can deploy the Foundry agent first and later inspect any identity or
blueprint information the service exposes. The manager cannot attach an
arbitrary Agent 365 blueprint later until Microsoft documents a supported
Foundry mutation for that operation.

### Does `binding plan` make changes?

No. It uses only read operations. `matched` means a Foundry blueprint client ID
or blueprint reference equals the requested blueprint application/object ID.
That is correlation evidence only. `not-matched` or `insufficient-data` returns
a non-executable plan instead of guessing at an undocumented write.

### Can the manager read `a365.generated.config.json`?

No, intentionally. Generated Agent 365 configuration can contain
`agentBlueprintClientSecret`, `azureOpenAIApiKey`, and other secrets. Copy only
the non-secret blueprint application or object ID into the corresponding flag.

### What does `agent365 integration set` change?

It sets `properties.a365LoggingEnabled` on the Foundry account through ARM
API `2026-03-15-preview` and verifies with a read-back. It does not modify
`a365Status`, which is a read-only field (`Enabled`, `Disabled`, or
`NotLicensed`). Collection is active only when the logging flag is `true` and
`a365Status` is `Enabled`. The scope is the entire Foundry account; there is no
per-project or per-agent override.

### What does `agent365 observability` check?

`observability plan` scans bounded regular Hosted source files while skipping
`.env` and `a365.generated.config.json`. It reports evidence filenames for
Microsoft OpenTelemetry Distro packages
(`microsoft-opentelemetry`, `@microsoft/opentelemetry`,
`Microsoft.OpenTelemetry`) or legacy Agent 365 observability SDK evidence.
`observability status` checks whether the deployed identity has the
`Agent365.Observability.OtelWrite` app role assigned. It is read-only and does
not assign the role.

### What can `agent365 publication` do?

Publication commands (`info`, `plan`, `status`, `admin-handoff`) are read-only
and plan-only. They do not perform generic registry mutation or arbitrary
existing blueprint binding. Official docs currently contain conflicting
information about Prompt vs. Hosted support; the CLI does not claim Prompt
execution. Registry status has no documented manager API and remains
unverified.

### How does identity lifecycle work after publication?

New-model agents receive a unique blueprint and `instance_identity` when they
are created. Standard Microsoft 365 publication and Agent 365 registry
synchronization retain that identity, so existing RBAC remains assigned to its
`principal_id`. Legacy agents without `instance_identity` can use the shared
project identity, and legacy Agent Applications have separate identities.
Migrating either legacy form to a new-model agent creates a new identity and
requires deliberate downstream RBAC reassignment.

## Tools, grounding, and integrations

### How do I see which tools the manager supports?

```powershell
fam tool-catalog
```

The manifest supports direct tools such as Code Interpreter, File Search,
OpenAPI, MCP, A2A, Azure AI Search, Toolbox attachments, and other documented
stable or preview surfaces.

### What is the difference between a direct tool and a Toolbox?

A direct tool is attached to one Prompt Agent version. A Toolbox is a reusable,
independently versioned collection that multiple agents can consume. Toolbox
deployment and promotion are separate from Prompt Agent deployment.

### Does deploying a Prompt Agent automatically create its Toolbox?

No. Manage Toolboxes under the `toolbox` namespace, then attach the existing
Toolbox from the Prompt manifest.

### How are local documents added to File Search?

Declare a manager-owned vector store under `grounding.vector_stores`, validate
and synchronize it with `grounding sync`, then reference its logical name from
a `file_search` tool. Files are contained, size-checked, and SHA-256 hashed.

### Does the tool execute Function Calling functions?

No. It deploys the function schema. The caller or application owns function
execution and the return of tool results to the model.

### Are Memory operations stable and free?

No. Memory is preview. Online Memory commands require `--accept-preview`, and
search or update operations can invoke models and incur charges.

### Does an external MCP, OpenAPI, APIM, or A2A destination need approval?

Yes. Credential-bearing or data-egress destinations require exact operator
approval from flags, protected environment variables, or a trust policy file.
The manifest cannot approve its own destinations.

## Security, credentials, and automation

### Why are trusted-host flags required?

Cloud suffix validation proves that a hostname belongs to a service family; it
does not prove that the operator intended to send credentials or data there.
Exact approvals prevent a changed manifest from redirecting tokens or agent
traffic to an unreviewed destination.

### Can trusted-host approvals use wildcards?

No. Wildcards and suffix approvals are rejected. Approve each exact host, with
an optional explicit port.

### Should trust approvals be stored in the manifest?

No. Supply them from operator-controlled flags, protected CI environment
variables, or a separate trust policy file. Keeping approvals outside the
manifest preserves the trust boundary.

### How should APIM subscription keys be supplied?

Prefer an environment variable, contained file, stdin, or Key Vault source.
Supplying a key directly as a process argument is supported where documented
but raises a safety warning because process arguments can be observable.

### Are secrets written to output or receipts?

No. Tokens, APIM keys, and trust-approval values are redacted and are not
persisted in receipts.

### Can safe read requests retry automatically?

Yes. Safe HTTP methods use bounded retries for transient failures and honor a
clamped `Retry-After`. Non-repeatable version creation and invocation POSTs are
not silently retried because their server-side outcome could be ambiguous.

### How do I safely automate destructive commands?

Use `--dry-run` when the command supports it, review the scope, then use
`--yes` for noninteractive execution. Structured output refuses interactive
confirmation.

### What happens when I press Ctrl+C?

In-flight work is cancelled and the process exits with code `130`.

### Which exit codes should automation use?

Branch on the stable process exit code and structured error `kind`, not on
message text. Common values include:

- `2`: manifest
- `3`: configuration
- `4`: security
- `5`: authentication (`auth`) or authorization (`authorization`)
- `6`: not found
- `7`: conflict
- `8`: transient
- `10`: other Foundry or Azure service failure
- `11`: doctor completed but requested readiness checks did not pass
- `130`: cancellation

See [`command-reference.md#exit-codes-and-error-envelope`](command-reference.md#exit-codes-and-error-envelope)
for the complete table.

## Troubleshooting

### Where should troubleshooting start?

Run the narrowest non-mutating checks first:

```powershell
fam prompt validate -f agent.yaml
fam prompt plan -f agent.yaml
fam doctor -f agent.yaml --online
fam prompt preflight -f agent.yaml
```

For Hosted Agents, use `hosted validate`, `hosted plan`,
`hosted preflight`, and `hosted diagnose`.

### Why does preflight return a Foundry data-plane 404?

First verify the project endpoint shape. Use either the account origin plus
`project.name`, or one complete `project.endpoint`. A duplicated
`/api/projects/<project>` path targets a resource that does not exist.

If the endpoint is correct, verify the child project exists and that the
current identity can read it.

### Why does preflight report that the model deployment is missing?

`agent.model` is a deployment name, not necessarily the underlying catalog
model name. Confirm the exact deployment name in the selected Foundry project
or parent account. If it should be created by this CLI, add
`model_deployment` desired state, run `model deployment plan`, create it
explicitly, and rerun `prompt preflight`.

### Why does model deployment plan report insufficient quota or capacity?

Quota is your subscription/account limit; regional capacity is Azure's current
ability to place that exact model version and SKU in the account region. Reduce
capacity, choose an available SKU/version/region under your organization's
architecture rules, or request quota. Do not treat a quota increase as proof
that regional capacity is available; plan validates both independently.

### Why does model deployment create report existing drift?

The deployment name already exists, but its model, version, format, SKU,
capacity, or optional managed policy fields do not match the desired state.
The command will not update it in place. Inspect it with
`model deployment show`, review its consumers, then choose a new deployment
name or explicitly delete and recreate it.

### Why does an online command fail while `prompt validate` and `prompt plan` pass?

`prompt validate` and `prompt plan` are offline. They prove local schema, containment, and
payload construction, but they cannot prove authentication, project access,
model existence, downstream RBAC, service availability, or quota.

### Why do I receive an authentication or authorization error?

The error kind distinguishes the failure:

- `auth` means Azure returned HTTP `401` or the CLI could not obtain a usable
  credential. Authenticate again, verify the tenant and token audience, and use
  `--tenant-id` when the target resource belongs to another tenant.
- `authorization` means Azure authenticated the principal but returned HTTP
  `403`. When Azure's denial message identifies an RBAC action and scope, the
  CLI copies those exact values into `error.nextSteps`. Ask an administrator to
  assign the active principal a least-privilege role containing that action at
  that scope or an appropriate parent scope.

Both retain exit code `5` for automation compatibility. The CLI does not grant
roles or elevate the principal. After a new assignment propagates, refresh the
credential and retry.

### Can separate teams use the same executable with different RBAC roles?

Yes. The executable contains every command, but Azure and Microsoft Entra
authorize the active human or workload identity for each request. Use separate
identities for project development, infrastructure/model administration,
publication, endpoint consumption, Agent 365 governance, runtime access, and
receipt ingestion.

Offline validation and planning need no cloud role. Foundry project data-plane,
Azure management-plane, Microsoft Graph/Entra, downstream resource, and Azure
Monitor DCR permissions remain independent. See
[RBAC and Separation of Duties](rbac-and-separation-of-duties.md) for the
per-tool matrix and recommended personas.

### Why is a destination rejected even though it is an Azure hostname?

The cloud suffix is valid, but the exact operator approval is missing. Review
the destination and supply the corresponding trusted-host or audience approval.

### Why did the tool refuse to retry a failed deployment?

The service may have committed a mutation before the connection failed.
Automatic repetition could create a duplicate immutable version or overwrite
state. Inspect remote status and the receipt, then reconcile deliberately.

### Why does Azure Government fail before login?

That is intentional. The CLI rejects unsupported cloud selection before
credential acquisition so it cannot accidentally request or send a token
across cloud boundaries.

### Where are common error remedies documented?

See the troubleshooting table in
[`security-and-operations.md#troubleshooting`](security-and-operations.md#troubleshooting).
Structured errors may also provide a `nextSteps` array.

## Builds and releases

### Does committing and pushing to `main` run a build?

Yes. The GitHub Actions CI workflow runs on pushes and pull requests to
`main`. It checks formatting, vet, tests, race detection, build behavior, and
executable qualification.

### Does pushing a commit automatically publish a release?

No. Release publication is tag-driven. A matching `vX.Y.Z` tag triggers the
release path only after the exact tagged source passes CI.

### What does a release publish?

The release workflow cross-compiles six platform archives, generates
`SHA256SUMS`, and creates the GitHub release after qualification succeeds.

### How is the release version selected?

The source version in `internal/config/config.go`, changelog version, and pushed
tag must agree. The workflow rejects mismatched release metadata.

### How can maintainers run the complete local release gate?

```powershell
.\scripts\Test-Release.ps1
```

The gate covers formatting, vet, tests, race detection where supported, host
builds, executable metadata, completions, examples, negative probes, patch
hygiene, cross-compilation, and checksums.
