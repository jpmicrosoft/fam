# Start Here

This page helps you find the right documentation for where you are right now.

## New to this tool? Read these first

1. **[Glossary](glossary.md)** — Plain-language definitions of every term
   (Foundry, manifest, preflight, receipt, etc.). Read this if anything in the
   docs is unclear.
2. **[README — Install](../README.md#install)** — Get the CLI on your machine
   from a prebuilt release; no Go installation or compilation is required.
3. **[README — Which path do I need?](../README.md#which-path-do-i-need)** —
   Answer one question to know whether you need the Prompt or Hosted path.

## First success paths

| Your situation | Do this |
|---|---|
| I have a Foundry account and model, and I want the simplest agent | [Quick start: Prompt Agent](../README.md#quick-start-prompt-agent) |
| I need to run custom code (Python/.NET/container) | [Quick start: Hosted Agent](../README.md#quick-start-hosted-agent) |
| I just want to explore offline, no Azure account yet | Run `foundry-agent-manager quickstart --type prompt`, or choose Hosted and answer **no** to environment bootstrap; then validate and plan the generated files |

## Reference guides (read when you need them)

| Guide | When to read it |
|---|---|
| [Command Reference](command-reference.md) | Look up any command, its flags, safety level, exit codes, or output format |
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
