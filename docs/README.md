# Start Here

Choose the outcome you need; you do not need to read every guide in order.
FAM manages instructions-based **Prompt Agents** and code-based **Hosted
Agents**. AzureCloud is the supported cloud; Hosted deployment is preview.

## New to this tool? Read these first

1. **[Install FAM](../README.md#install)** and run `fam version`. The prebuilt
   executable needs no Go installation or repository clone.
2. **[Get a first result without Azure](../README.md#first-success-without-azure)**:
   create a local manifest, validate it, and inspect a plan. No login required.
3. **[Choose a deployment path](../README.md#which-path-do-i-need)** when you
   are ready to supply Azure coordinates, credentials, and permissions.

**What counts as success?** Offline validation proves local structure, not
Azure readiness. Online preflight checks access before deployment; deployment
changes Azure state. Sending a test message is a separate, billable action.

## First success paths

| Your situation | Do this |
|---|---|
| I just want to explore, with no Azure account yet | [Offline first success](../README.md#first-success-without-azure) |
| I have a Foundry project/model and want an instructions-based agent | [Prompt quickstart](../README.md#quick-start-prompt-agent) |
| I need custom code and want to start with a local workspace | [Hosted quickstart](../README.md#quick-start-hosted-agent) |
| I already have Python source but no `azure.yaml` | [Adopt existing Python source](hosted-agents.md#adopt-existing-python-source) |
| I already have an `azure.yaml` workspace | [Existing Hosted workspace](../README.md#existing-hosted-agent-workspace) |
| I need a child project or model deployment first | [Create a child project](prompt-agents.md#project-create) / [Manage a model deployment](prompt-agents.md#model-deployment-lifecycle) |
| I need to inspect Agent 365 identities, not deploy source | [Agent 365 guide](agent365.md) |

## Find a command without reading the whole catalog

```powershell
fam help
fam help prompt
fam prompt deploy --help
```

Root help shows namespaces. Namespace help narrows the command list; command
help supplies the exact flags and examples. Use the
[command reference](command-reference.md#commands) to browse by family.

## Unblock or maintain an installation

| Task | Start here |
|---|---|
| Fix installation, PATH, or download problems | [Installation FAQ](faq.md#installation-and-command-line-usage) |
| Update the FAM executable | [Update instructions](../README.md#update-an-existing-installation) and [`update` options](command-reference.md#update) |
| Diagnose a failing command | [FAQ troubleshooting](faq.md#troubleshooting) / [Readiness with doctor](../README.md#doctor--environment-readiness) |
| Understand an unfamiliar term | [Glossary](glossary.md) |
| Decide whether a capability is supported or preview | [Support boundaries](../README.md#support-status-and-release-boundaries) |

## Reference guides (read when you need them)

| Guide | When to read it |
|---|---|
| [Command Reference](command-reference.md) | Browse command families, shared options, safety levels, exit codes, and output contracts; use focused CLI help for individual flags |
| [FAQ](faq.md) | Find practical answers and common failure remedies |
| [RBAC and Separation of Duties](rbac-and-separation-of-duties.md) | Assign least-privilege roles to separate authors, deployers, infrastructure administrators, publishers, consumers, Agent 365 governance, runtimes, and audit jobs |
| [Prompt Agents](prompt-agents.md) | Deep dive: manifest schema, tools, deploy, promote, rollback, receipts, APIM, M365 |
| [Hosted Agents](hosted-agents.md) | Deep dive: workspace, azd, sessions, files, logs, drafts, scaffold, Autopilot |
| [Agent 365](agent365.md) | Blueprint, identity, principal, integration, observability, publication, and RBAC boundaries |
| [Tools and Grounding](tools-and-grounding.md) | Add documents, Toolboxes, Skills, connectors, or Memory to any agent |
| [Security and Operations](security-and-operations.md) | Trust approvals, cloud boundaries, destructive safeguards, troubleshooting |
| [Log Analytics Receipts](log-analytics-receipts.md) | Publish redacted receipts through a DCR, configure the table schema, retry failures, and query audit records |
| [CI Templates](ci-templates/) | GitHub Actions templates for team deployments |

## For experienced users and automation

- **[CI/CD with structured output](../README.md#cicd-with-structured-output-and-receipts)** —
  JSON output, receipts, and exit codes for pipelines.
- **[Command Reference — Global options](command-reference.md#global-options)** —
  `--output json`, `--quiet`, `--verbose`, environment variables.
- **[Command Reference — Exit codes](command-reference.md#exit-codes-and-error-envelope)** —
  Branch automation on stable numeric codes and error kinds.
- **[Security — CI guidance](security-and-operations.md#ci-guidance)** —
  Set trust approvals from protected environments.

## For contributors

- [Development and Releases](development-and-releases.md) — Build, test, and
  release the CLI itself.
- [CONTRIBUTING.md](../CONTRIBUTING.md) — Dev environment setup and review
  expectations.
