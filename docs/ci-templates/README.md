# CI/CD Deployment Templates

Starter GitHub Actions workflow templates for deploying agents with
`foundry-agent-manager`. These are **inert templates** — they do not run in
this repository. Copy them into your own repository's `.github/workflows/`
directory and customise before use.

## What users gain

These templates provide a safe starting point for moving from commands run on
one developer's machine to a repeatable team deployment process:

- **Consistency:** every run installs the selected
  `foundry-agent-manager` version and executes the same validation, preflight,
  and deployment sequence.
- **Earlier failures:** invalid manifests, invalid Hosted workspaces,
  authentication problems, and unavailable environments fail before the
  deployment step.
- **Secretless Azure authentication:** GitHub OIDC supplies short-lived Azure
  credentials instead of storing a client secret in the repository.
- **Deployment governance:** GitHub environments can require reviewers and
  restrict which branches may deploy to `dev`, `staging`, or `production`.
- **No overlapping deployments:** concurrency groups serialize runs for the
  same target environment.
- **Reduced unnecessary changes:** `--if-changed` skips deployment when the
  desired configuration matches the last verified deployment.
- **Audit evidence:** each run uploads the redacted deployment receipt as a
  retained GitHub artifact and can optionally publish the same terminal receipt
  to Log Analytics through an existing DCR.
- **Reproducible Hosted tooling:** the Hosted template installs the reviewed
  `azd` and `azure.ai.agents` extension versions instead of relying on a
  developer workstation's tool versions.

### Example team workflow

1. A developer changes the agent manifest or Hosted workspace in a pull
   request.
2. The change is reviewed and merged to `main`.
3. A path-filtered workflow deploys the change to `dev`, or an operator starts
   the workflow manually and selects another protected environment.
4. GitHub records the logs, environment approval, selected commit, and
   deployment receipt in one run.

This is most useful when agent configuration is stored in Git and multiple
people need predictable, reviewable deployments. A user experimenting locally
can continue to run the CLI directly without adopting GitHub Actions.

## What remains your responsibility

The templates deliberately do **not**:

- Create the Foundry account or create/delete a model deployment implicitly.
  Add separately approved plan and `model deployment create` jobs when CI
  should own that billable infrastructure mutation.
- Create the Azure identity, federated credential, or RBAC assignments.
- Create the Log Analytics workspace, custom table, DCR, ingestion endpoint, or
  receipt-publishing role assignment.
- Decide which external destinations should be trusted.
- Promote a staged Prompt Agent version to production traffic.
- Provision Hosted infrastructure unless the operator explicitly enables both
  provisioning and its preview acknowledgement.
- Replace repository branch protection, environment reviewers, or organization
  security policy.

## Templates

| File | Description |
|---|---|
| `deploy-prompt.yml` | Prompt-based deployment: validate → preflight → deploy --if-changed |
| `deploy-hosted.yml` | Hosted deployment: validate → plan → preflight → deploy --if-changed with optional, explicitly previewed infrastructure provisioning |

## Required Variables

Set these as GitHub Actions **repository variables** (`vars.*`) or
**environment variables** in your repository settings:

| Variable | Description | Example |
|---|---|---|
| `AZURE_CLIENT_ID` | Service principal / managed identity client ID for OIDC | `00000000-0000-0000-0000-000000000000` |
| `AZURE_TENANT_ID` | Azure AD tenant ID | `00000000-0000-0000-0000-000000000000` |
| `AZURE_SUBSCRIPTION_ID` | Target Azure subscription ID | `00000000-0000-0000-0000-000000000000` |
| `AZURE_LOCATION` | Azure region used by the job-local Hosted azd environment | `eastus` |
| `AZURE_AI_PROJECT_ID` | Full existing Foundry project resource ID used by azd project/RBAC diagnostics | `/subscriptions/<subscription>/resourceGroups/<group>/providers/Microsoft.CognitiveServices/accounts/<account>/projects/<project>` |
| `FOUNDRY_PROJECT_ENDPOINT` | Existing Foundry project endpoint used by Hosted deployment; the template also accepts legacy `AZURE_AI_PROJECT_ENDPOINT` as a fallback | `https://<account>.services.ai.azure.com/api/projects/<project>` |
| `AZURE_AI_MODEL_DEPLOYMENT_NAME` | Existing model deployment used by the Hosted Agent | `gpt-4.1` |
| `INSTALLER_REPO` | *(Optional)* Override installer source repository | `myorg/foundry-agent-manager` |
| `FAM_VERSION` | *(Optional)* Pin installer to a release tag that includes `install.sh` | `vX.Y.Z` |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_ENDPOINT` | *(Optional)* Azure Monitor Logs ingestion endpoint for completed receipts | `https://<name>.<region>-1.ingest.monitor.azure.com` |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_DCR_ID` | *(Optional)* Immutable DCR ID paired with the endpoint | `dcr-0123456789abcdef0123456789abcdef` |
| `FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_STREAM` | *(Optional)* Receipt input stream; defaults to `Custom-FoundryAgentReceipts` | `Custom-FoundryAgentReceipts` |

> **Release requirement:** `v0.4.0` predates the installer release assets used
> by these templates. `v0.5.0` and later publish `install.sh` and include it in
> `SHA256SUMS`.

If `INSTALLER_REPO` is private, add a `FAM_INSTALL_TOKEN` Actions secret with
read access to that repository. The templates use it through `gh release
download` and pass it to the installer without printing it. Public or same-repo
releases can use the job's short-lived `github.token`.

When receipt publishing variables are set, grant the workflow's OIDC principal
Logs ingestion access to the DCR. See
[Log Analytics Receipts](../log-analytics-receipts.md) for the stream schema,
RBAC, retry behavior, and KQL.

## Azure OIDC Setup

These templates use [GitHub OIDC](https://docs.github.com/en/actions/security-for-github-actions/security-hardening-your-deployments/configuring-openid-connect-in-azure) federated credentials — no long-lived secrets are stored in GitHub.

1. Create an Azure AD app registration with federated credentials for your repository.
2. Grant it the minimum Azure RBAC roles required by `foundry-agent-manager`.
3. Set the three `AZURE_*` variables above.

## Environments

The templates use GitHub [deployment environments](https://docs.github.com/en/actions/managing-workflow-runs-and-deployments/managing-deployments/managing-environments-for-deployment) for protection rules (approvals, branch policies). Create environments matching the
choices in the workflow (`dev`, `staging`, `production`).

## Before Production Use

- **Pin action versions** to commit SHAs instead of tags (e.g.
  `actions/checkout@<sha>` instead of `actions/checkout@v4`).
- **Pin `FAM_VERSION`** to a tested release tag.
- Review and tighten `permissions` to the minimum your workflow needs.
- Add required reviewers to production environments.
- Enable branch protection on `main`.

The Hosted template also installs `azd` 1.27.1, installs the reviewed
`azure.ai.agents` extension version `1.0.0-beta.8`, authenticates `azd` through
GitHub OIDC, and creates a job-local azd environment from the variables above.

## Provisioning (Hosted Only)

The Hosted template always requires `--accept-preview` because Hosted Agents
are preview. Infrastructure provisioning (`--provision
--preview-provision`) remains **off by default**.
Enable it explicitly via the `provision` workflow dispatch input or by changing
the default in your copy of the workflow. This ensures infrastructure changes
remain intentional.
