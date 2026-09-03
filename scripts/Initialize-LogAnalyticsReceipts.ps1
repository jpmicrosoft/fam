<#
.SYNOPSIS
    Creates the Log Analytics table and direct DCR used by FAM receipt publishing.

.DESCRIPTION
    Uses the signed-in Azure CLI identity to create or update the
    FoundryAgentReceipts_CL custom table and create or reuse the dedicated
    direct data collection rule expected by FAM. The workspace is not created
    by this script.

    By default, both the table and DCR are configured. Use -TableOnly or
    -DcrOnly to limit the operation. The DCR is created in the workspace
    resource group and region.

.PARAMETER WorkspaceResourceId
    Full Azure resource ID of the destination Log Analytics workspace.

.PARAMETER TableOnly
    Create or update only the FoundryAgentReceipts_CL table.

.PARAMETER DcrOnly
    Create or reuse only the DCR. The compatible table must already exist.

.PARAMETER DcrName
    Name of the direct DCR. Defaults to dcr-foundry-agent-receipts.

.EXAMPLE
    .\Initialize-LogAnalyticsReceipts.ps1 `
      -WorkspaceResourceId "/subscriptions/<subscription-id>/resourceGroups/<resource-group>/providers/Microsoft.OperationalInsights/workspaces/<workspace>"

.EXAMPLE
    .\Initialize-LogAnalyticsReceipts.ps1 `
      -WorkspaceResourceId "/subscriptions/<subscription-id>/resourceGroups/<resource-group>/providers/Microsoft.OperationalInsights/workspaces/<workspace>" `
      -TableOnly

.EXAMPLE
    .\Initialize-LogAnalyticsReceipts.ps1 `
      -WorkspaceResourceId "/subscriptions/<subscription-id>/resourceGroups/<resource-group>/providers/Microsoft.OperationalInsights/workspaces/<workspace>" `
      -DcrOnly
#>
[CmdletBinding(SupportsShouldProcess, ConfirmImpact = "Medium")]
param(
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string]$WorkspaceResourceId,

    [switch]$TableOnly,

    [switch]$DcrOnly,

    [ValidatePattern("^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")]
    [string]$DcrName = "dcr-foundry-agent-receipts"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$TableName = "FoundryAgentReceipts_CL"
$StreamName = "Custom-FoundryAgentReceipts"
$TableApiVersion = "2022-10-01"
$DcrApiVersion = "2023-03-11"
$DestinationName = "receiptWorkspace"

function Format-AzureCliCommand {
    param([string[]]$Arguments)

    $safe = [Collections.Generic.List[string]]::new()
    $redactNext = $false
    foreach ($argument in $Arguments) {
        if ($redactNext) {
            $safe.Add("<json>")
            $redactNext = $false
            continue
        }
        $safe.Add($argument)
        if ($argument -eq "--body") {
            $redactNext = $true
        }
    }
    return "az " + ($safe -join " ")
}

function Invoke-AzureCli {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments,

        [switch]$AllowNotFound,

        [switch]$AsText
    )

    $output = @(& az @Arguments 2>&1)
    $exitCode = $LASTEXITCODE
    $text = ($output | ForEach-Object { "$_" }) -join "`n"
    if ($exitCode -ne 0) {
        if ($AllowNotFound -and
            $text -match "(?i)(\bResourceNotFound\b|\bResourceGroupNotFound\b|\bNotFound\b|HTTP 404|Status Code:\s*404)") {
            return $null
        }
        $command = Format-AzureCliCommand -Arguments $Arguments
        throw "Azure CLI command failed with exit code ${exitCode}: $command`n$text"
    }

    $text = $text.Trim()
    if ($AsText) {
        return $text
    }
    if ($text.Length -eq 0) {
        return $null
    }
    try {
        return $text | ConvertFrom-Json
    }
    catch {
        $command = Format-AzureCliCommand -Arguments $Arguments
        throw "Azure CLI returned invalid JSON for: $command`n$text"
    }
}

function Invoke-AzureCliJsonBody {
    param(
        [Parameter(Mandatory)]
        [string[]]$Arguments,

        [Parameter(Mandatory)]
        [string]$Body
    )

    $tempPath = Join-Path (
        [IO.Path]::GetTempPath()
    ) "fam-log-analytics-$([guid]::NewGuid().ToString('N')).json"
    try {
        [IO.File]::WriteAllText(
            $tempPath,
            $Body,
            [Text.UTF8Encoding]::new($false)
        )
        return Invoke-AzureCli -Arguments ($Arguments + @("--body", "@$tempPath"))
    }
    finally {
        if (Test-Path -LiteralPath $tempPath) {
            try {
                Remove-Item -LiteralPath $tempPath -Force
            }
            catch {
                Write-Warning "Could not remove temporary Azure CLI body file '$tempPath': $($_.Exception.Message)"
            }
        }
    }
}

function Get-PropertyValue {
    param(
        $InputObject,

        [Parameter(Mandatory)]
        [string]$Name
    )

    if ($null -eq $InputObject) {
        return $null
    }
    $property = $InputObject.PSObject.Properties[$Name]
    if ($null -eq $property) {
        return $null
    }
    return $property.Value
}

function Get-ReceiptColumns {
    return @(
        [ordered]@{
            name        = "TimeGenerated"
            type        = "datetime"
            description = "Receipt completion time, or start time for an incomplete receipt."
        },
        [ordered]@{
            name        = "ReceiptId"
            type        = "string"
            description = "Stable receipt identifier used for reconciliation and de-duplication."
        },
        [ordered]@{
            name        = "SchemaVersion"
            type        = "string"
            description = "Manager receipt schema version."
        },
        [ordered]@{
            name        = "Operation"
            type        = "string"
            description = "Manager operation that produced the receipt."
        },
        [ordered]@{
            name        = "Status"
            type        = "string"
            description = "Terminal receipt status."
        },
        [ordered]@{
            name        = "Cloud"
            type        = "string"
            description = "Selected Azure cloud."
        },
        [ordered]@{
            name        = "AgentName"
            type        = "string"
            description = "Agent name when present in the receipt."
        },
        [ordered]@{
            name        = "ProjectName"
            type        = "string"
            description = "Foundry project name when present in the receipt."
        },
        [ordered]@{
            name        = "Metadata"
            type        = "dynamic"
            description = "Optional custom non-secret receipt metadata."
        },
        [ordered]@{
            name        = "Receipt"
            type        = "dynamic"
            description = "Complete redacted manager receipt."
        }
    )
}

function Get-Resource {
    param(
        [Parameter(Mandatory)]
        [string]$ResourceId,

        [Parameter(Mandatory)]
        [string]$ApiVersion,

        [Parameter(Mandatory)]
        [string]$SubscriptionId,

        [switch]$AllowNotFound
    )

    return Invoke-AzureCli -Arguments @(
        "rest",
        "--method", "get",
        "--url", "${ResourceId}?api-version=$ApiVersion",
        "--subscription", $SubscriptionId,
        "--only-show-errors",
        "--output", "json"
    ) -AllowNotFound:$AllowNotFound
}

function Wait-CompatibleReceiptTable {
    param(
        [Parameter(Mandatory)]
        [string]$ResourceId,

        [Parameter(Mandatory)]
        [string]$SubscriptionId,

        [Parameter(Mandatory)]
        [object[]]$ExpectedColumns
    )

    for ($attempt = 1; $attempt -le 60; $attempt++) {
        $resource = Get-Resource `
            -ResourceId $ResourceId `
            -ApiVersion $TableApiVersion `
            -SubscriptionId $SubscriptionId `
            -AllowNotFound
        if ($null -ne $resource) {
            $properties = Get-PropertyValue -InputObject $resource -Name "properties"
            $state = [string](Get-PropertyValue -InputObject $properties -Name "provisioningState")
            if ($state -ieq "Failed" -or $state -ieq "Canceled") {
                throw "$TableName provisioning ended in state '$state'."
            }
            if ([string]::IsNullOrWhiteSpace($state) -or $state -ieq "Succeeded") {
                $schema = Get-PropertyValue -InputObject $properties -Name "schema"
                $tableSubType = [string](Get-PropertyValue -InputObject $schema -Name "tableSubType")
                if ($tableSubType -ieq "Classic") {
                    throw "$TableName is a Classic custom table and cannot be used with the Logs Ingestion API until it is migrated. See https://learn.microsoft.com/azure/azure-monitor/logs/custom-logs-migrate."
                }
                if ($tableSubType -ieq "DataCollectionRuleBased") {
                    $columns = @(Get-PropertyValue -InputObject $schema -Name "columns")
                    $missing = @(Get-MissingReceiptColumns `
                        -ExistingColumns $columns `
                        -ExpectedColumns $ExpectedColumns)
                    if ($missing.Count -eq 0) {
                        return $resource
                    }
                }
            }
        }
        Start-Sleep -Seconds 5
    }
    throw "Timed out waiting for $TableName to expose the required DCR-based schema."
}

function Wait-ReceiptDcr {
    param(
        [Parameter(Mandatory)]
        [string]$DcrResourceId,

        [Parameter(Mandatory)]
        [string]$SubscriptionId
    )

    for ($attempt = 1; $attempt -le 60; $attempt++) {
        $resource = Get-Resource `
            -ResourceId $DcrResourceId `
            -ApiVersion $DcrApiVersion `
            -SubscriptionId $SubscriptionId `
            -AllowNotFound
        if ($null -ne $resource) {
            $properties = Get-PropertyValue -InputObject $resource -Name "properties"
            $state = [string](Get-PropertyValue -InputObject $properties -Name "provisioningState")
            if ($state -ieq "Failed" -or $state -ieq "Canceled") {
                throw "$DcrName provisioning ended in state '$state'."
            }
            $immutableId = [string](Get-PropertyValue -InputObject $properties -Name "immutableId")
            $endpoints = Get-PropertyValue -InputObject $properties -Name "endpoints"
            $logsIngestion = [string](Get-PropertyValue -InputObject $endpoints -Name "logsIngestion")
            if (([string]::IsNullOrWhiteSpace($state) -or $state -ieq "Succeeded") -and
                -not [string]::IsNullOrWhiteSpace($immutableId) -and
                -not [string]::IsNullOrWhiteSpace($logsIngestion)) {
                return $resource
            }
        }
        Start-Sleep -Seconds 5
    }
    throw "Timed out waiting for $DcrName to expose its immutable ID and Logs ingestion endpoint."
}

function Get-MissingReceiptColumns {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [object[]]$ExistingColumns,

        [Parameter(Mandatory)]
        [object[]]$ExpectedColumns
    )

    $missing = [Collections.Generic.List[object]]::new()
    foreach ($expected in $ExpectedColumns) {
        $matches = @($ExistingColumns | Where-Object {
            [string]$_.name -ceq [string]$expected.name
        })
        if ($matches.Count -eq 0) {
            $missing.Add($expected)
            continue
        }
        if ($matches.Count -ne 1) {
            throw "The table contains duplicate '$($expected.name)' columns."
        }
        if ([string]$matches[0].type -ine [string]$expected.type) {
            throw "Column '$($expected.name)' must use type '$($expected.type)', but the existing table uses '$($matches[0].type)'."
        }
    }
    return @($missing)
}

function Get-ColumnSignature {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [object[]]$Columns
    )

    return (@($Columns | Sort-Object { [string]$_.name } | ForEach-Object {
        "$([string]$_.name)`0$(([string]$_.type).ToLowerInvariant())"
    }) -join "`n")
}

function Assert-DcrBasedReceiptTable {
    param([Parameter(Mandatory)]$Table)

    $properties = Get-PropertyValue -InputObject $Table -Name "properties"
    $schema = Get-PropertyValue -InputObject $properties -Name "schema"
    $tableSubType = [string](Get-PropertyValue -InputObject $schema -Name "tableSubType")
    if ($tableSubType -ieq "Classic") {
        throw "$TableName is a Classic custom table and cannot be used with the Logs Ingestion API until it is migrated. See https://learn.microsoft.com/azure/azure-monitor/logs/custom-logs-migrate."
    }
    if ($tableSubType -ine "DataCollectionRuleBased") {
        throw "$TableName must have tableSubType 'DataCollectionRuleBased'; Azure returned '$tableSubType'."
    }
}

function Assert-CompatibleReceiptTable {
    param([Parameter(Mandatory)]$Table)

    Assert-DcrBasedReceiptTable -Table $Table
    $properties = Get-PropertyValue -InputObject $Table -Name "properties"
    $schema = Get-PropertyValue -InputObject $properties -Name "schema"
    $existingColumns = @(Get-PropertyValue -InputObject $schema -Name "columns")
    $missing = @(Get-MissingReceiptColumns `
        -ExistingColumns $existingColumns `
        -ExpectedColumns @(Get-ReceiptColumns))
    if ($missing.Count -gt 0) {
        $names = ($missing | ForEach-Object { $_.name }) -join ", "
        throw "The existing $TableName table is missing required columns: $names. Run the script without -DcrOnly or use -TableOnly first."
    }
}

function Assert-ExactColumnContract {
    param(
        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [object[]]$ActualColumns,

        [Parameter(Mandatory)]
        [AllowEmptyCollection()]
        [object[]]$ExpectedColumns,

        [Parameter(Mandatory)]
        [string]$DisplayName
    )

    if ($ActualColumns.Count -ne $ExpectedColumns.Count) {
        throw "$DisplayName must contain exactly $($ExpectedColumns.Count) columns."
    }
    foreach ($expected in $ExpectedColumns) {
        $matches = @($ActualColumns | Where-Object {
            [string]$_.name -ceq [string]$expected.name
        })
        if ($matches.Count -ne 1 -or
            [string]$matches[0].type -ine [string]$expected.type) {
            throw "$DisplayName column '$($expected.name)' must use type '$($expected.type)'."
        }
    }
}

function ConvertTo-DcrColumnType {
    param(
        [Parameter(Mandatory)]
        [string]$TableColumnType
    )

    $normalized = $TableColumnType.ToLowerInvariant()
    switch ($normalized) {
        "guid" { return "string" }
        "datetime" { return "datetime" }
        "string" { return "string" }
        "int" { return "int" }
        "long" { return "long" }
        "real" { return "real" }
        "boolean" { return "boolean" }
        "dynamic" { return "dynamic" }
        default {
            throw "Table column type '$TableColumnType' cannot be represented in a DCR stream declaration."
        }
    }
}

function Get-ExistingDcr {
    param(
        [Parameter(Mandatory)]
        [string]$DcrResourceId,

        [Parameter(Mandatory)]
        [string]$SubscriptionId,

        [Parameter(Mandatory)]
        [string]$WorkspaceId,

        [Parameter(Mandatory)]
        [object[]]$TableColumns
    )

    $dcr = Get-Resource `
        -ResourceId $DcrResourceId `
        -ApiVersion $DcrApiVersion `
        -SubscriptionId $SubscriptionId `
        -AllowNotFound
    if ($null -eq $dcr) {
        return $null
    }
    if ([string]$dcr.kind -ine "Direct") {
        throw "DCR '$DcrName' already exists but is not a direct-ingestion DCR. Choose a different -DcrName."
    }
    $properties = Get-PropertyValue -InputObject $dcr -Name "properties"
    $streamDeclarations = Get-PropertyValue -InputObject $properties -Name "streamDeclarations"
    $streamProperties = @()
    if ($null -ne $streamDeclarations) {
        $streamProperties = @($streamDeclarations.PSObject.Properties)
    }
    if ($streamProperties.Count -ne 1 -or $streamProperties[0].Name -cne $StreamName) {
        throw "DCR '$DcrName' already exists but is not dedicated exclusively to FAM receipts. Choose a different -DcrName."
    }
    $stream = $streamProperties[0].Value
    $expectedDcrColumns = @($TableColumns | ForEach-Object {
        [ordered]@{
            name = $_.name
            type = ConvertTo-DcrColumnType -TableColumnType ([string]$_.type)
        }
    })
    Assert-ExactColumnContract `
        -ActualColumns @($stream.columns) `
        -ExpectedColumns $expectedDcrColumns `
        -DisplayName "DCR '$DcrName' stream"

    $destinations = Get-PropertyValue -InputObject $properties -Name "destinations"
    $destinationProperties = @()
    if ($null -ne $destinations) {
        $destinationProperties = @($destinations.PSObject.Properties)
    }
    if ($destinationProperties.Count -ne 1 -or
        $destinationProperties[0].Name -cne "logAnalytics") {
        throw "DCR '$DcrName' already exists but contains non-FAM destinations. Choose a different -DcrName."
    }
    $logAnalyticsDestinations = Get-PropertyValue -InputObject $destinations -Name "logAnalytics"
    $workspaceDestinations = @($logAnalyticsDestinations)
    if ($workspaceDestinations.Count -ne 1 -or
        [string]$workspaceDestinations[0].workspaceResourceId -ine $WorkspaceId -or
        [string]$workspaceDestinations[0].name -cne $DestinationName) {
        throw "DCR '$DcrName' already targets a different or shared Log Analytics destination. Choose a different -DcrName."
    }

    $dataFlows = @(Get-PropertyValue -InputObject $properties -Name "dataFlows")
    if ($dataFlows.Count -ne 1) {
        throw "DCR '$DcrName' already exists but contains a different or shared data flow. Choose a different -DcrName."
    }
    $dataFlow = $dataFlows[0]
    $flowStreams = @($dataFlow.streams)
    $flowDestinations = @($dataFlow.destinations)
    if ($flowStreams.Count -ne 1 -or
        [string]$flowStreams[0] -cne $StreamName -or
        $flowDestinations.Count -ne 1 -or
        [string]$flowDestinations[0] -cne $DestinationName -or
        [string]$dataFlow.transformKql -cne "source" -or
        [string]$dataFlow.outputStream -cne "Custom-$TableName") {
        throw "DCR '$DcrName' already exists but contains a different or shared data flow. Choose a different -DcrName."
    }

    $dataSources = Get-PropertyValue -InputObject $properties -Name "dataSources"
    if ($null -ne $dataSources) {
        $dataSourceProperties = @($dataSources.PSObject.Properties)
        if ($dataSourceProperties.Count -gt 0) {
            throw "DCR '$DcrName' already exists but contains data sources outside the FAM receipt contract. Choose a different -DcrName."
        }
    }
    $endpoints = Get-PropertyValue -InputObject $properties -Name "endpoints"
    $endpoint = [string](Get-PropertyValue -InputObject $endpoints -Name "logsIngestion")
    if ([string]::IsNullOrWhiteSpace($endpoint)) {
        throw "DCR '$DcrName' does not expose a direct Logs ingestion endpoint. Azure cannot add endpoints to an existing older DCR; choose a different -DcrName."
    }
    return $dcr
}

if ($TableOnly -and $DcrOnly) {
    throw "-TableOnly and -DcrOnly cannot be used together."
}

if ($null -eq (Get-Command az -ErrorAction SilentlyContinue)) {
    throw "Azure CLI is required. Install it, run 'az login', and retry."
}

try {
    $null = Invoke-AzureCli -Arguments @(
        "account", "show",
        "--only-show-errors",
        "--output", "json"
    )
}
catch {
    throw "Azure CLI authentication is required. Run 'az login' and retry. $($_.Exception.Message)"
}

$cloudName = Invoke-AzureCli -Arguments @(
    "cloud", "show",
    "--query", "name",
    "--only-show-errors",
    "--output", "tsv"
) -AsText
if ($cloudName -ne "AzureCloud") {
    throw "FAM Log Analytics receipt publishing currently supports AzureCloud. Azure CLI is using '$cloudName'."
}

$WorkspaceResourceId = $WorkspaceResourceId.Trim().TrimEnd("/")
$workspacePattern = "^/subscriptions/([^/]+)/resourceGroups/([^/]+)/providers/Microsoft\.OperationalInsights/workspaces/([^/]+)$"
$workspaceMatch = [regex]::Match(
    $WorkspaceResourceId,
    $workspacePattern,
    [Text.RegularExpressions.RegexOptions]::IgnoreCase
)
if (-not $workspaceMatch.Success) {
    throw "-WorkspaceResourceId must be a complete Microsoft.OperationalInsights/workspaces resource ID."
}

$SubscriptionId = $workspaceMatch.Groups[1].Value
$ResourceGroupName = $workspaceMatch.Groups[2].Value
$WorkspaceName = $workspaceMatch.Groups[3].Value
$parsedSubscriptionId = [guid]::Empty
if (-not [guid]::TryParse($SubscriptionId, [ref]$parsedSubscriptionId)) {
    throw "The subscription segment in -WorkspaceResourceId must be a GUID."
}

$workspace = Invoke-AzureCli -Arguments @(
    "monitor", "log-analytics", "workspace", "show",
    "--ids", $WorkspaceResourceId,
    "--subscription", $SubscriptionId,
    "--only-show-errors",
    "--output", "json"
)
$workspaceLocation = [string](Get-PropertyValue -InputObject $workspace -Name "location")
if ($null -eq $workspace -or [string]::IsNullOrWhiteSpace($workspaceLocation)) {
    throw "Could not read the Log Analytics workspace location from $WorkspaceResourceId."
}
$Location = $workspaceLocation

$createTable = -not $DcrOnly
$createDcr = -not $TableOnly
$mode = if ($TableOnly) { "TableOnly" } elseif ($DcrOnly) { "DcrOnly" } else { "Both" }
$TableResourceId = "$WorkspaceResourceId/tables/$TableName"
$DcrResourceId = "/subscriptions/$SubscriptionId/resourceGroups/$ResourceGroupName/providers/Microsoft.Insights/dataCollectionRules/$DcrName"
$expectedColumns = @(Get-ReceiptColumns)
$tableStatus = "not requested"
$dcrStatus = "not requested"
$table = $null
$dcr = $null
$targetTableColumns = $null

if ($createTable) {
    $table = Get-Resource `
        -ResourceId $TableResourceId `
        -ApiVersion $TableApiVersion `
        -SubscriptionId $SubscriptionId `
        -AllowNotFound
    $tableExisted = $null -ne $table
    $existingColumns = @()
    if ($null -ne $table) {
        Assert-DcrBasedReceiptTable -Table $table
        $existingColumns = @($table.properties.schema.columns)
    }
    $missingColumns = @(Get-MissingReceiptColumns `
        -ExistingColumns $existingColumns `
        -ExpectedColumns $expectedColumns)

    if ($null -ne $table -and $missingColumns.Count -eq 0) {
        $targetTableColumns = $existingColumns
        $tableStatus = "unchanged"
        Write-Host "$TableName is already compatible."
    }
    else {
        $columns = [Collections.Generic.List[object]]::new()
        foreach ($column in $existingColumns) {
            $columns.Add($column)
        }
        foreach ($column in $missingColumns) {
            $columns.Add($column)
        }
        $targetTableColumns = @($columns)
        $tableBody = [ordered]@{
            properties = [ordered]@{
                schema = [ordered]@{
                    name    = $TableName
                    columns = @($columns)
                }
            }
        } | ConvertTo-Json -Depth 20 -Compress

        if ($PSCmdlet.ShouldProcess($TableResourceId, "Create or update the FAM receipt table")) {
            $tableMethod = if ($tableExisted) { "patch" } else { "put" }
            $tableHeaders = if ($tableExisted) { "If-Match=*" } else { "If-None-Match=*" }
            if ($tableExisted) {
                $currentTable = Get-Resource `
                    -ResourceId $TableResourceId `
                    -ApiVersion $TableApiVersion `
                    -SubscriptionId $SubscriptionId
                Assert-DcrBasedReceiptTable -Table $currentTable
                $currentColumns = @($currentTable.properties.schema.columns)
                if ((Get-ColumnSignature -Columns $currentColumns) -cne
                    (Get-ColumnSignature -Columns $existingColumns)) {
                    throw "$TableName changed after it was inspected. No update was made; rerun the script."
                }
            }
            $null = Invoke-AzureCliJsonBody -Arguments @(
                "rest",
                "--method", $tableMethod,
                "--url", "${TableResourceId}?api-version=$TableApiVersion",
                "--subscription", $SubscriptionId,
                "--headers", $tableHeaders,
                "--only-show-errors",
                "--output", "json"
            ) -Body $tableBody
            $table = Wait-CompatibleReceiptTable `
                -ResourceId $TableResourceId `
                -SubscriptionId $SubscriptionId `
                -ExpectedColumns $expectedColumns
            $tableStatus = if ($tableExisted) { "updated" } else { "created" }
            Write-Host "$TableName $tableStatus."
        }
        else {
            $tableStatus = "planned"
        }
    }
}

if ($createDcr) {
    $tableColumnsForDcr = $expectedColumns
    if ($WhatIfPreference -and $createTable -and $null -ne $targetTableColumns) {
        $tableColumnsForDcr = @($targetTableColumns)
    }
    else {
        $table = Get-Resource `
            -ResourceId $TableResourceId `
            -ApiVersion $TableApiVersion `
            -SubscriptionId $SubscriptionId `
            -AllowNotFound
        if ($null -eq $table) {
            if ($DcrOnly -or -not $WhatIfPreference) {
                throw "$TableName does not exist. Run the script without -DcrOnly or use -TableOnly first."
            }
        }
        else {
            Assert-CompatibleReceiptTable -Table $table
            $tableColumnsForDcr = @($table.properties.schema.columns)
        }
    }

    $existingDcr = Get-ExistingDcr `
        -DcrResourceId $DcrResourceId `
        -SubscriptionId $SubscriptionId `
        -WorkspaceId $WorkspaceResourceId `
        -TableColumns $tableColumnsForDcr
    $dcrColumns = @($tableColumnsForDcr | ForEach-Object {
        [ordered]@{
            name = $_.name
            type = ConvertTo-DcrColumnType -TableColumnType ([string]$_.type)
        }
    })
    $dcrBody = [ordered]@{
        location   = $Location
        kind       = "Direct"
        properties = [ordered]@{
            streamDeclarations = [ordered]@{
                $StreamName = [ordered]@{
                    columns = $dcrColumns
                }
            }
            destinations       = [ordered]@{
                logAnalytics = @(
                    [ordered]@{
                        workspaceResourceId = $WorkspaceResourceId
                        name                = $DestinationName
                    }
                )
            }
            dataFlows          = @(
                [ordered]@{
                    streams      = @($StreamName)
                    destinations = @($DestinationName)
                    transformKql = "source"
                    outputStream = "Custom-$TableName"
                }
            )
        }
    } | ConvertTo-Json -Depth 20 -Compress

    if ($null -ne $existingDcr) {
        $dcr = $existingDcr
        $dcrStatus = "unchanged"
        Write-Host "$DcrName is already compatible."
    }
    elseif ($PSCmdlet.ShouldProcess($DcrResourceId, "Create the FAM receipt DCR")) {
        $null = Invoke-AzureCliJsonBody -Arguments @(
            "rest",
            "--method", "put",
            "--url", "${DcrResourceId}?api-version=$DcrApiVersion",
            "--subscription", $SubscriptionId,
            "--headers", "If-None-Match=*",
            "--only-show-errors",
            "--output", "json"
        ) -Body $dcrBody
        $dcr = Wait-ReceiptDcr `
            -DcrResourceId $DcrResourceId `
            -SubscriptionId $SubscriptionId
        $dcrStatus = "created"
        Write-Host "$DcrName $dcrStatus."
    }
    else {
        $dcrStatus = "planned"
    }
}

$DcrImmutableId = ""
$LogsIngestionEndpoint = ""
if ($null -ne $dcr) {
    $dcrProperties = Get-PropertyValue -InputObject $dcr -Name "properties"
    $DcrImmutableId = [string](Get-PropertyValue -InputObject $dcrProperties -Name "immutableId")
    $dcrEndpoints = Get-PropertyValue -InputObject $dcrProperties -Name "endpoints"
    $LogsIngestionEndpoint = [string](Get-PropertyValue -InputObject $dcrEndpoints -Name "logsIngestion")
    if ([string]::IsNullOrWhiteSpace($DcrImmutableId) -or
        [string]::IsNullOrWhiteSpace($LogsIngestionEndpoint)) {
        throw "The direct DCR was created but Azure did not return its immutable ID and Logs ingestion endpoint."
    }

    Write-Host ""
    Write-Host "FAM receipt publishing values:"
    Write-Host "  --receipt-log-endpoint $LogsIngestionEndpoint"
    Write-Host "  --receipt-log-dcr-id $DcrImmutableId"
    Write-Host "  --receipt-log-stream $StreamName"
    Write-Host ""
    Write-Host "Grant the publishing identity 'Monitoring Metrics Publisher' on:"
    Write-Host "  $DcrResourceId"
}

[pscustomobject]@{
    Mode                  = $mode
    WorkspaceResourceId   = $WorkspaceResourceId
    WorkspaceName         = $WorkspaceName
    ResourceGroupName     = $ResourceGroupName
    SubscriptionId        = $SubscriptionId
    Location              = $Location
    TableName             = $TableName
    TableResourceId       = $TableResourceId
    TableStatus           = $tableStatus
    DcrName               = if ($createDcr) { $DcrName } else { "" }
    DcrResourceId         = if ($createDcr) { $DcrResourceId } else { "" }
    DcrStatus             = $dcrStatus
    DcrImmutableId        = $DcrImmutableId
    LogsIngestionEndpoint = $LogsIngestionEndpoint
    StreamName            = $StreamName
}
