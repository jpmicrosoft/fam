# Log Analytics Receipts

Publish completed, redacted manager receipts to a Log Analytics workspace
(LAW) through the Azure Monitor Logs Ingestion API.

The feature is opt-in. Local receipt persistence remains the source of recovery:
the CLI writes the terminal local file first, then submits one record to Azure
Monitor. If ingestion fails, the command returns an error, preserves the local
receipt, and prints a `receipt upload` retry command.

## Quickstart: create the table and DCR

> [!TIP]
> **Quickstart:** Download the standalone
> [`Initialize-LogAnalyticsReceipts.ps1`](https://raw.githubusercontent.com/jpmicrosoft/fam/main/scripts/Initialize-LogAnalyticsReceipts.ps1)
> script, review it, and run it with the full Log Analytics workspace resource
> ID. It uses Azure CLI and creates both the custom table and direct DCR by
> default.
>
> ```powershell
> Invoke-WebRequest `
>   -Uri "https://raw.githubusercontent.com/jpmicrosoft/fam/main/scripts/Initialize-LogAnalyticsReceipts.ps1" `
>   -OutFile ".\Initialize-LogAnalyticsReceipts.ps1"
>
> Unblock-File ".\Initialize-LogAnalyticsReceipts.ps1"
>
> .\Initialize-LogAnalyticsReceipts.ps1 `
>   -WorkspaceResourceId "/subscriptions/<subscription-id>/resourceGroups/<resource-group>/providers/Microsoft.OperationalInsights/workspaces/<workspace>"
> ```
>
> Use `-TableOnly` or `-DcrOnly` to create only one resource. `-DcrOnly`
> requires the compatible table to exist. Use `-DcrName <name>` when the
> default `dcr-foundry-agent-receipts` name is already assigned. The script
> assumes `az login` is complete and the required resource providers are
> registered. It does not create the workspace or role assignment.

The script requires a new or existing `DataCollectionRuleBased` custom table.
It fails before modifying a legacy `Classic` custom table. Migrate a classic
table by following Microsoft's
[Data Collector API migration guidance](https://learn.microsoft.com/azure/azure-monitor/logs/custom-logs-migrate),
or use a new table.

## Why a DCR is required

A Log Analytics workspace is only the storage destination. It does not by
itself define the JSON contract accepted by the Logs Ingestion API. The data
collection rule (DCR) is the server-side contract that:

- declares the incoming receipt columns and their types;
- identifies the destination workspace and custom table;
- maps the input stream to the output table;
- applies a transformation when the incoming and destination schemas differ;
- exposes an immutable DCR ID used by every ingestion request.

The CLI therefore needs three related values:

| Value | Purpose |
|---|---|
| Logs ingestion endpoint | Network address that receives the HTTPS request |
| Immutable DCR ID, such as `dcr-...` | Selects the DCR contract; pass this to `--receipt-log-dcr-id` |
| Input stream `Custom-FoundryAgentReceipts` | Selects the matching stream declaration in that DCR |

The DCR also has a normal Azure resource ID beginning with `/subscriptions/`.
That resource ID is used as the RBAC scope; it is **not** the value accepted by
`--receipt-log-dcr-id`.

## Azure prerequisites

Create these resources before enabling publishing:

1. A Log Analytics workspace and custom table.
2. A data collection rule (DCR) that routes the receipt stream to that table.
3. A DCR or data collection endpoint Logs ingestion URI.
4. RBAC on the DCR for the identity selected by `DefaultAzureCredential`.

The workspace and DCR must be in the same Azure region. A direct DCR provides
its own Logs ingestion endpoint and is the simplest public-ingestion setup. A
separate data collection endpoint (DCE) is optional unless the workspace uses
Azure Monitor Private Link.

Microsoft documents **Monitoring Metrics Publisher** as the built-in role for
Logs ingestion. A custom role can instead grant the narrower
`Microsoft.Insights/Telemetry/Write` data action. Assign access at the DCR scope
or an appropriate parent scope and allow time for propagation.

The `fam` executable does not create the workspace, table, DCR, endpoint, or
role assignment. The standalone repository script in the quickstart can create
the table and direct DCR, but intentionally leaves workspace creation and RBAC
to the administrator.

For strict separation of duties, do not grant DCR ingestion to the production
deployment identity. Preserve local receipts and use a separate audit workload
identity to run `receipt upload`. Automatic publishing intentionally uses the
same active credential as the mutating command and therefore combines
deployment and audit-publishing authority.

See [RBAC and Separation of Duties](rbac-and-separation-of-duties.md#receipt-publishing)
for the audit-publisher persona.

## Incoming stream schema

The default stream is `Custom-FoundryAgentReceipts`. Declare these columns in
the DCR input stream and in the destination custom table:

| Column | Type | Meaning |
|---|---|---|
| `TimeGenerated` | `datetime` | Receipt completion time, or start time for an incomplete preserved receipt |
| `ReceiptId` | `string` | Stable receipt identifier used for reconciliation and de-duplication |
| `SchemaVersion` | `string` | `foundry-agent-manager/receipt/v1` or `/v2` |
| `Operation` | `string` | Operation name; v1 Prompt deployment receipts use `prompt-deploy` |
| `Status` | `string` | Terminal receipt status |
| `Cloud` | `string` | Selected Azure cloud |
| `AgentName` | `string` | Agent name when the receipt contains one |
| `ProjectName` | `string` | Project name when the receipt contains one |
| `Metadata` | `dynamic` | Optional custom non-secret key/value metadata copied from the receipt |
| `Receipt` | `dynamic` | Complete redacted receipt JSON |

Example DCR stream declaration:

```json
{
  "streamDeclarations": {
    "Custom-FoundryAgentReceipts": {
      "columns": [
        { "name": "TimeGenerated", "type": "datetime" },
        { "name": "ReceiptId", "type": "string" },
        { "name": "SchemaVersion", "type": "string" },
        { "name": "Operation", "type": "string" },
        { "name": "Status", "type": "string" },
        { "name": "Cloud", "type": "string" },
        { "name": "AgentName", "type": "string" },
        { "name": "ProjectName", "type": "string" },
        { "name": "Metadata", "type": "dynamic" },
        { "name": "Receipt", "type": "dynamic" }
      ]
    }
  }
}
```

When the destination table uses the same columns, the DCR data flow can use
`source` as its transformation and
`Custom-FoundryAgentReceipts_CL` as its output stream.

Existing destinations created before custom metadata support must add the
`Metadata` dynamic column to the input stream and destination table before a
receipt containing metadata is uploaded. Receipts without metadata omit the
column and remain compatible with the earlier payload shape.

## Create the custom table and DCR manually

Use this procedure when you prefer to inspect and run each provisioning command
from a repository clone instead of using the standalone quickstart script. It
creates the exact table and direct-DCR contracts expected by the manager and
does not create a Log Analytics workspace.

Microsoft documents the underlying resources in the
[Logs Ingestion API overview](https://learn.microsoft.com/azure/azure-monitor/logs/logs-ingestion-api-overview)
and the
[Resource Manager tutorial](https://learn.microsoft.com/azure/azure-monitor/logs/tutorial-logs-ingestion-api).

### 1. Select the workspace

Sign in to the intended tenant and subscription, then set the resource names:

```powershell
az login
az account set --subscription "<subscription-id-or-name>"
az provider register --namespace Microsoft.Insights

$WorkspaceResourceGroup = "<workspace-resource-group>"
$WorkspaceName = "<log-analytics-workspace-name>"
$DcrResourceGroup = $WorkspaceResourceGroup
$DcrName = "dcr-foundry-agent-receipts"

$WorkspaceId = az monitor log-analytics workspace show `
  --resource-group $WorkspaceResourceGroup `
  --workspace-name $WorkspaceName `
  --query id --output tsv

$Location = az monitor log-analytics workspace show `
  --resource-group $WorkspaceResourceGroup `
  --workspace-name $WorkspaceName `
  --query location --output tsv
```

The location is read from the workspace instead of entered independently
because Azure requires the DCR and destination workspace to be in the same
region.

### 2. Create the custom table

Run this command from the repository root:

```powershell
az rest `
  --method put `
  --url "https://management.azure.com$WorkspaceId/tables/FoundryAgentReceipts_CL?api-version=2022-10-01" `
  --body '@examples\log-analytics-receipts.table.json'
```

The `_CL` suffix identifies a Log Analytics custom table. The provided table
definition includes both `Metadata` and the complete redacted `Receipt` as
dynamic JSON columns. To migrate an existing table created before metadata
support, run the same command with `--method patch` instead of `--method put`.
`PATCH` preserves table properties omitted from the schema payload, including
existing retention settings.

### 3. Deploy the direct DCR

```powershell
az deployment group create `
  --name "foundry-agent-receipt-ingestion" `
  --resource-group $DcrResourceGroup `
  --template-file "examples\log-analytics-receipts.dcr.json" `
  --parameters `
    dataCollectionRuleName=$DcrName `
    location=$Location `
    workspaceResourceId=$WorkspaceId
```

The template deliberately uses:

- `kind: Direct` so the DCR exposes a Logs ingestion endpoint;
- `Custom-FoundryAgentReceipts` as the manager's input stream;
- `transformKql: source` because the input and table schemas already match;
- `Custom-FoundryAgentReceipts_CL` as the destination table stream.

If any column name, case, or type differs between the table and DCR, Azure can
reject ingestion even though authentication succeeds.

### 4. Obtain the endpoint and immutable ID

```powershell
$DcrResourceId = az monitor data-collection rule show `
  --resource-group $DcrResourceGroup `
  --name $DcrName `
  --query id --output tsv

$DcrImmutableId = az rest `
  --method get `
  --url "https://management.azure.com$DcrResourceId?api-version=2023-03-11" `
  --query properties.immutableId --output tsv

$LogsIngestionEndpoint = az rest `
  --method get `
  --url "https://management.azure.com$DcrResourceId?api-version=2023-03-11" `
  --query properties.endpoints.logsIngestion --output tsv

$DcrImmutableId
$LogsIngestionEndpoint
```

Use `$DcrImmutableId`, not `$DcrResourceId`, for
`--receipt-log-dcr-id`. Use `$DcrResourceId` as the role-assignment scope.

### 5. Grant the publishing identity access

For an interactive user, obtain the signed-in user's Microsoft Entra object ID:

```powershell
$PrincipalObjectId = az ad signed-in-user show --query id --output tsv
```

For a managed identity, workload identity, or service principal, use that
identity's object/principal ID instead. Then grant ingestion access:

```powershell
az role assignment create `
  --assignee-object-id $PrincipalObjectId `
  --role "Monitoring Metrics Publisher" `
  --scope $DcrResourceId
```

This assignment authorizes data submission through the DCR. Reader or
Contributor access to the workspace alone does not grant the
`Microsoft.Insights/Telemetry/Write` data action required for ingestion.

### 6. Configure and verify the manager

```powershell
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_ENDPOINT = $LogsIngestionEndpoint
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_DCR_ID = $DcrImmutableId
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_STREAM = "Custom-FoundryAgentReceipts"

fam receipt upload `
  --file "<path-to-an-existing-manager-receipt.json>"
```

After ingestion completes, verify the record:

```kusto
FoundryAgentReceipts_CL
| where TimeGenerated > ago(30m)
| project TimeGenerated, ReceiptId, Operation, Status, Metadata
| order by TimeGenerated desc
```

Azure role assignments can take several minutes to propagate. An immediate
`403` after creating the assignment does not necessarily mean the DCR is
misconfigured; confirm the assignment scope and retry after propagation.

## Automatic publishing

Pass the destination as global options on any command that writes a receipt:

```powershell
fam prompt deploy -f agent.yaml --if-changed `
  --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com `
  --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef `
  --receipt-log-stream Custom-FoundryAgentReceipts
```

The stream flag is optional and defaults to
`Custom-FoundryAgentReceipts`. Protected automation can use environment
variables instead:

```powershell
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_ENDPOINT = "https://my-dce.eastus-1.ingest.monitor.azure.com"
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_DCR_ID = "dcr-0123456789abcdef0123456789abcdef"
$env:FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_STREAM = "Custom-FoundryAgentReceipts"
```

Flag values take precedence over environment variables.

## Retry a preserved receipt

Use the standalone command when automatic publishing failed or when importing
an earlier manager-generated receipt:

```powershell
fam receipt upload `
  --file artifacts\deploy-receipt.json `
  --receipt-log-endpoint https://my-dce.eastus-1.ingest.monitor.azure.com `
  --receipt-log-dcr-id dcr-0123456789abcdef0123456789abcdef
```

The command accepts only valid manager v1/v2 receipt JSON. Upload POSTs are not
automatically retried because a lost response can make ingestion ambiguous.
Use `ReceiptId` to de-duplicate if a manual retry creates a second record.

## Query examples

```kusto
FoundryAgentReceipts_CL
| where TimeGenerated > ago(24h)
| project TimeGenerated, ReceiptId, Operation, Status, AgentName, ProjectName,
    Owner=tostring(Metadata.owner), Environment=tostring(Metadata.environment), Cloud
| order by TimeGenerated desc
```

```kusto
FoundryAgentReceipts_CL
| where Status !startswith "succeeded"
| extend ReceiptError = tostring(Receipt.error)
| project TimeGenerated, ReceiptId, Operation, Status, ReceiptError
```

## Security and operational behavior

- Only AzureCloud ingestion hosts under `ingest.monitor.azure.com` are allowed.
- The endpoint must use HTTPS and contain no path, query, fragment, or embedded
  credentials.
- Tokens use the `https://monitor.azure.com/.default` audience and redirects
  are refused.
- Receipts are centrally redacted before local persistence and publishing.
- Custom metadata is intentionally copied to the agent, local receipt, and LAW
  record. It must contain operational labels only, never credentials or secrets.
- Receipts can still contain local paths, Azure resource identifiers, agent
  names, project names, and error details. Apply appropriate workspace RBAC,
  retention, and export controls.
- Each request is limited to 1 MiB, matching the Logs Ingestion API limit.
- HTTP `401` remains `auth`; HTTP `403` is `authorization` and preserves any
  action and scope Azure reports.
