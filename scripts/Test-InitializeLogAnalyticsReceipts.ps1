[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$SetupScript = Join-Path $PSScriptRoot "Initialize-LogAnalyticsReceipts.ps1"
$SubscriptionId = "11111111-2222-3333-4444-555555555555"
$WorkspaceResourceId = "/subscriptions/$SubscriptionId/resourceGroups/receipts-rg/providers/Microsoft.OperationalInsights/workspaces/receipts-law"
$TableResourceId = "$WorkspaceResourceId/tables/FoundryAgentReceipts_CL"
$DcrResourceId = "/subscriptions/$SubscriptionId/resourceGroups/receipts-rg/providers/Microsoft.Insights/dataCollectionRules/dcr-foundry-agent-receipts"
$ExpectedColumnNames = @(
    "TimeGenerated",
    "ReceiptId",
    "SchemaVersion",
    "Operation",
    "Status",
    "Cloud",
    "AgentName",
    "ProjectName",
    "Metadata",
    "Receipt"
)

$global:FamLawMockTableExists = $false
$global:FamLawMockDcrExists = $false
$global:FamLawMockExtraTableColumn = $false
$global:FamLawMockMissingReceiptColumn = $false
$global:FamLawMockClassicTable = $false
$global:FamLawMockPendingTableUpdate = $false
$global:FamLawMockStaleTableReads = 0
$global:FamLawMockChangeOnSecondTableGet = $false
$global:FamLawMockTableGetCount = 0
$global:FamLawMockSharedDcr = $false
$global:FamLawMockCalls = [Collections.Generic.List[string]]::new()

function Assert-Equal {
    param(
        $Actual,
        $Expected,
        [string]$Message
    )

    if ($Actual -ne $Expected) {
        throw "$Message. Expected '$Expected', got '$Actual'."
    }
}

function Get-ArgumentValue {
    param(
        [string[]]$Arguments,
        [string]$Name
    )

    $index = [Array]::IndexOf($Arguments, $Name)
    if ($index -lt 0 -or $index + 1 -ge $Arguments.Count) {
        throw "Mock Azure CLI call omitted $Name."
    }
    return $Arguments[$index + 1]
}

function Read-BodyArgument {
    param([string[]]$Arguments)

    $value = Get-ArgumentValue -Arguments $Arguments -Name "--body"
    if (-not $value.StartsWith("@", [StringComparison]::Ordinal)) {
        throw "Azure CLI JSON bodies must be passed through a file."
    }
    $path = $value.Substring(1)
    if (-not (Test-Path -LiteralPath $path)) {
        throw "Azure CLI JSON body file does not exist: $path"
    }
    return Get-Content -Raw -LiteralPath $path | ConvertFrom-Json
}

function New-MockTable {
    $columnNames = [Collections.Generic.List[string]]::new()
    foreach ($name in $ExpectedColumnNames) {
        $columnNames.Add($name)
    }
    if ($global:FamLawMockExtraTableColumn) {
        $columnNames.Add("ExistingContext")
    }
    $columns = foreach ($name in $columnNames) {
        if ($global:FamLawMockMissingReceiptColumn -and $name -eq "Metadata") {
            continue
        }
        [ordered]@{
            name = $name
            type = if ($name -eq "TimeGenerated") {
                "datetime"
            }
            elseif ($name -eq "ExistingContext") {
                "guid"
            }
            elseif ($name -eq "Metadata" -or $name -eq "Receipt") {
                "dynamic"
            }
            else {
                "string"
            }
        }
    }
    return [ordered]@{
        id         = $TableResourceId
        name       = "FoundryAgentReceipts_CL"
        properties = [ordered]@{
            provisioningState   = "Succeeded"
            retentionInDays      = 90
            totalRetentionInDays = 365
            schema               = [ordered]@{
                name         = "FoundryAgentReceipts_CL"
                tableSubType = if ($global:FamLawMockClassicTable) {
                    "Classic"
                }
                else {
                    "DataCollectionRuleBased"
                }
                columns      = @($columns)
            }
        }
    }
}

function New-MockDcr {
    $columns = @((New-MockTable).properties.schema.columns | ForEach-Object {
        [ordered]@{
            name = $_.name
            type = if ([string]$_.type -ieq "guid") {
                "string"
            }
            else {
                ([string]$_.type).ToLowerInvariant()
            }
        }
    })
    $streamDeclarations = [ordered]@{
        "Custom-FoundryAgentReceipts" = [ordered]@{
            columns = $columns
        }
    }
    $dataFlows = [Collections.Generic.List[object]]::new()
    $dataFlows.Add([ordered]@{
        streams      = @("Custom-FoundryAgentReceipts")
        destinations = @("receiptWorkspace")
        transformKql = "source"
        outputStream = "Custom-FoundryAgentReceipts_CL"
    })
    if ($global:FamLawMockSharedDcr) {
        $streamDeclarations["Custom-Unrelated"] = [ordered]@{
            columns = @(
                [ordered]@{
                    name = "Message"
                    type = "string"
                }
            )
        }
        $dataFlows.Add([ordered]@{
            streams      = @("Custom-Unrelated")
            destinations = @("receiptWorkspace")
            transformKql = "source"
            outputStream = "Custom-Unrelated_CL"
        })
    }
    return [ordered]@{
        id         = $DcrResourceId
        name       = "dcr-foundry-agent-receipts"
        kind       = "Direct"
        location   = "eastus"
        properties = [ordered]@{
            provisioningState = "Succeeded"
            immutableId       = "dcr-0123456789abcdef0123456789abcdef"
            endpoints         = [ordered]@{
                logsIngestion = "https://dcr-receipts.eastus-1.ingest.monitor.azure.com"
            }
            streamDeclarations = $streamDeclarations
            destinations       = [ordered]@{
                logAnalytics = @(
                    [ordered]@{
                        workspaceResourceId = $WorkspaceResourceId
                        name                = "receiptWorkspace"
                    }
                )
            }
            dataFlows          = @($dataFlows)
        }
    }
}

function Reset-MockState {
    param(
        [bool]$TableExists = $false,
        [bool]$DcrExists = $false,
        [bool]$ExtraTableColumn = $false,
        [bool]$MissingReceiptColumn = $false,
        [bool]$ClassicTable = $false,
        [int]$StaleTableReads = 0,
        [bool]$ChangeOnSecondTableGet = $false,
        [bool]$SharedDcr = $false
    )

    $global:FamLawMockTableExists = $TableExists
    $global:FamLawMockDcrExists = $DcrExists
    $global:FamLawMockExtraTableColumn = $ExtraTableColumn
    $global:FamLawMockMissingReceiptColumn = $MissingReceiptColumn
    $global:FamLawMockClassicTable = $ClassicTable
    $global:FamLawMockPendingTableUpdate = $false
    $global:FamLawMockStaleTableReads = $StaleTableReads
    $global:FamLawMockChangeOnSecondTableGet = $ChangeOnSecondTableGet
    $global:FamLawMockTableGetCount = 0
    $global:FamLawMockSharedDcr = $SharedDcr
    $global:FamLawMockCalls.Clear()
}

function Start-Sleep {
    param([int]$Seconds)
}

function az {
    $arguments = @($args | ForEach-Object { [string]$_ })
    $global:LASTEXITCODE = 0

    if ($arguments.Count -ge 2 -and $arguments[0] -eq "account" -and $arguments[1] -eq "show") {
        $global:FamLawMockCalls.Add("account show")
        return '{"id":"11111111-2222-3333-4444-555555555555"}'
    }
    if ($arguments.Count -ge 2 -and $arguments[0] -eq "cloud" -and $arguments[1] -eq "show") {
        $global:FamLawMockCalls.Add("cloud show")
        return "AzureCloud"
    }
    if ($arguments.Count -ge 4 -and
        $arguments[0] -eq "monitor" -and
        $arguments[1] -eq "log-analytics" -and
        $arguments[2] -eq "workspace" -and
        $arguments[3] -eq "show") {
        $global:FamLawMockCalls.Add("workspace show")
        return [ordered]@{
            id       = $WorkspaceResourceId
            name     = "receipts-law"
            location = "eastus"
        } | ConvertTo-Json -Compress
    }
    if ($arguments.Count -gt 0 -and $arguments[0] -eq "rest") {
        $method = Get-ArgumentValue -Arguments $arguments -Name "--method"
        $url = Get-ArgumentValue -Arguments $arguments -Name "--url"
        $global:FamLawMockCalls.Add("$method $url")

        if ($url -like "$TableResourceId*") {
            if ($method -eq "get") {
                if (-not $global:FamLawMockTableExists) {
                    $global:LASTEXITCODE = 3
                    return '{"error":{"code":"ResourceNotFound"}}'
                }
                $global:FamLawMockTableGetCount++
                if ($global:FamLawMockChangeOnSecondTableGet -and
                    $global:FamLawMockTableGetCount -ge 2) {
                    $global:FamLawMockExtraTableColumn = $true
                }
                if ($global:FamLawMockPendingTableUpdate) {
                    if ($global:FamLawMockStaleTableReads -gt 0) {
                        $global:FamLawMockStaleTableReads--
                    }
                    else {
                        $global:FamLawMockMissingReceiptColumn = $false
                        $global:FamLawMockPendingTableUpdate = $false
                    }
                }
                return New-MockTable | ConvertTo-Json -Depth 20 -Compress
            }
            if ($method -eq "put" -or $method -eq "patch") {
                if ($global:FamLawMockTableExists -and $method -ne "patch") {
                    throw "Existing table schema updates must use PATCH."
                }
                $headers = Get-ArgumentValue -Arguments $arguments -Name "--headers"
                $expectedHeaders = if ($method -eq "patch") {
                    "If-Match=*"
                }
                else {
                    "If-None-Match=*"
                }
                Assert-Equal $headers $expectedHeaders "Unexpected table precondition header"
                $body = Read-BodyArgument -Arguments $arguments
                Assert-Equal $body.properties.schema.name "FoundryAgentReceipts_CL" "Unexpected table schema name"
                $names = @($body.properties.schema.columns | ForEach-Object { $_.name })
                foreach ($expectedName in $ExpectedColumnNames) {
                    if ($names -cnotcontains $expectedName) {
                        throw "Table payload omitted $expectedName."
                    }
                }
                $global:FamLawMockTableExists = $true
                if ($method -eq "patch") {
                    $global:FamLawMockPendingTableUpdate = $true
                }
                else {
                    $global:FamLawMockMissingReceiptColumn = $false
                }
                return New-MockTable | ConvertTo-Json -Depth 20 -Compress
            }
        }

        if ($url -like "$DcrResourceId*") {
            if ($method -eq "get") {
                if (-not $global:FamLawMockDcrExists) {
                    $global:LASTEXITCODE = 3
                    return '{"error":{"code":"ResourceNotFound"}}'
                }
                return New-MockDcr | ConvertTo-Json -Depth 20 -Compress
            }
            if ($method -eq "put") {
                $headers = Get-ArgumentValue -Arguments $arguments -Name "--headers"
                Assert-Equal $headers "If-None-Match=*" "DCR creation must use a create-only precondition"
                $body = Read-BodyArgument -Arguments $arguments
                Assert-Equal $body.kind "Direct" "DCR must use direct ingestion"
                Assert-Equal $body.properties.dataFlows[0].transformKql "source" "Unexpected DCR transformation"
                Assert-Equal $body.properties.dataFlows[0].outputStream "Custom-FoundryAgentReceipts_CL" "Unexpected DCR output stream"
                Assert-Equal $body.properties.destinations.logAnalytics[0].workspaceResourceId $WorkspaceResourceId "Unexpected DCR workspace"
                $stream = $body.properties.streamDeclarations.PSObject.Properties["Custom-FoundryAgentReceipts"]
                if ($null -eq $stream) {
                    throw "DCR payload omitted the FAM receipt stream."
                }
                $streamColumnNames = @($stream.Value.columns | ForEach-Object { $_.name })
                if ($global:FamLawMockExtraTableColumn -and
                    $streamColumnNames -cnotcontains "ExistingContext") {
                    throw "DCR payload omitted the existing destination table column."
                }
                if ($global:FamLawMockExtraTableColumn) {
                    $existingContext = @($stream.Value.columns | Where-Object {
                        $_.name -ceq "ExistingContext"
                    })
                    Assert-Equal $existingContext[0].type "string" "GUID table columns must map to DCR string columns"
                }
                $global:FamLawMockDcrExists = $true
                return New-MockDcr | ConvertTo-Json -Depth 20 -Compress
            }
        }
    }

    $global:LASTEXITCODE = 2
    return "Unexpected mock Azure CLI call: az $($arguments -join ' ')"
}

Reset-MockState
$both = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -Confirm:$false
Assert-Equal $both.Mode "Both" "Default mode must configure both resources"
Assert-Equal $both.TableStatus "created" "Default mode must create the table"
Assert-Equal $both.DcrStatus "created" "Default mode must create the DCR"
Assert-Equal $both.DcrImmutableId "dcr-0123456789abcdef0123456789abcdef" "Unexpected immutable DCR ID"
Assert-Equal $both.StreamName "Custom-FoundryAgentReceipts" "Unexpected input stream"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $TableResourceId*" }).Count 1 "Default mode table PUT count"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $DcrResourceId*" }).Count 1 "Default mode DCR PUT count"

Reset-MockState
$tableOnly = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -TableOnly -Confirm:$false
Assert-Equal $tableOnly.Mode "TableOnly" "Table-only mode name"
Assert-Equal $tableOnly.TableStatus "created" "Table-only mode must create the table"
Assert-Equal $tableOnly.DcrStatus "not requested" "Table-only mode must not create the DCR"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $DcrResourceId*" }).Count 0 "Table-only DCR PUT count"

Reset-MockState -TableExists $true -MissingReceiptColumn $true -StaleTableReads 2
$tableUpdate = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -TableOnly -Confirm:$false
Assert-Equal $tableUpdate.TableStatus "updated" "Existing table schema update status"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "patch $TableResourceId*" }).Count 1 "Existing table PATCH count"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $TableResourceId*" }).Count 0 "Existing table PUT count"
if (@($global:FamLawMockCalls | Where-Object { $_ -like "get $TableResourceId*" }).Count -lt 4) {
    throw "Table update polling did not tolerate stale successful GET responses."
}

Reset-MockState -TableExists $true -MissingReceiptColumn $true -ChangeOnSecondTableGet $true
$concurrentTableChangeFailed = $false
try {
    & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -TableOnly -Confirm:$false
}
catch {
    $concurrentTableChangeFailed = $_.Exception.Message -like "*changed after it was inspected*"
}
if (-not $concurrentTableChangeFailed) {
    throw "A concurrent table schema change must abort the update."
}
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put *" -or $_ -like "patch *" }).Count 0 "Concurrent table mutation count"

Reset-MockState -TableExists $true -MissingReceiptColumn $true
$whatIf = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -WhatIf -Confirm:$false
Assert-Equal $whatIf.TableStatus "planned" "WhatIf table update status"
Assert-Equal $whatIf.DcrStatus "planned" "WhatIf DCR create status"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put *" -or $_ -like "patch *" }).Count 0 "WhatIf mutation count"

Reset-MockState -TableExists $true
$dcrOnly = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -DcrOnly -Confirm:$false
Assert-Equal $dcrOnly.Mode "DcrOnly" "DCR-only mode name"
Assert-Equal $dcrOnly.TableStatus "not requested" "DCR-only mode must not update the table"
Assert-Equal $dcrOnly.DcrStatus "created" "DCR-only mode must create the DCR"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $TableResourceId*" }).Count 0 "DCR-only table PUT count"

Reset-MockState -TableExists $true -ExtraTableColumn $true
$extraColumn = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -DcrOnly -Confirm:$false
Assert-Equal $extraColumn.DcrStatus "created" "DCR must include compatible pre-existing table columns"

Reset-MockState -TableExists $true -DcrExists $true
$existing = & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -Confirm:$false
Assert-Equal $existing.TableStatus "unchanged" "Compatible existing table status"
Assert-Equal $existing.DcrStatus "unchanged" "Compatible existing DCR must be reused"
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put *" }).Count 0 "Compatible existing resource PUT count"

Reset-MockState -TableExists $true -DcrExists $true -SharedDcr $true
$sharedDcrFailed = $false
try {
    & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -DcrOnly -Confirm:$false
}
catch {
    $sharedDcrFailed = $_.Exception.Message -like "*not dedicated exclusively*"
}
if (-not $sharedDcrFailed) {
    throw "An existing shared DCR must be rejected instead of overwritten."
}
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put $DcrResourceId*" }).Count 0 "Shared DCR PUT count"

Reset-MockState -TableExists $true -ClassicTable $true
$classicTableFailed = $false
try {
    & $SetupScript -WorkspaceResourceId $WorkspaceResourceId -TableOnly -Confirm:$false
}
catch {
    $classicTableFailed = $_.Exception.Message -like "*Classic custom table*"
}
if (-not $classicTableFailed) {
    throw "A Classic custom table must be rejected before modification."
}
Assert-Equal @($global:FamLawMockCalls | Where-Object { $_ -like "put *" -or $_ -like "patch *" }).Count 0 "Classic table mutation count"

$mutualExclusionFailed = $false
try {
    & $SetupScript `
        -WorkspaceResourceId $WorkspaceResourceId `
        -TableOnly `
        -DcrOnly `
        -Confirm:$false
}
catch {
    $mutualExclusionFailed = $_.Exception.Message -like "*cannot be used together*"
}
if (-not $mutualExclusionFailed) {
    throw "Using -TableOnly and -DcrOnly together must fail."
}

Write-Host "Log Analytics receipt setup script contract passed."
