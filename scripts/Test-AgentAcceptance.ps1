[CmdletBinding()]
param(
    [string]$Calibration,
    [Parameter(Mandatory)]
    [string]$Result,
    [string]$Output
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($Calibration)) {
    $Calibration = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.calibration.jsonl"
}
elseif (-not [IO.Path]::IsPathRooted($Calibration)) {
    $Calibration = Join-Path $repoRoot $Calibration
}
$Calibration = [IO.Path]::GetFullPath($Calibration)

if (-not [IO.Path]::IsPathRooted($Result)) {
    $Result = Join-Path $repoRoot $Result
}
$Result = [IO.Path]::GetFullPath($Result)

if ([string]::IsNullOrWhiteSpace($Output)) {
    $Output = Join-Path (Split-Path -Parent $Result) "agent-acceptance-report.json"
}
elseif (-not [IO.Path]::IsPathRooted($Output)) {
    $Output = Join-Path $repoRoot $Output
}
$Output = [IO.Path]::GetFullPath($Output)

function Read-JsonLines {
    param(
        [string]$Path,
        [string]$Kind
    )
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Kind file does not exist: $Path"
    }
    $items = [Collections.Generic.List[object]]::new()
    $lineNumber = 0
    foreach ($line in Get-Content -LiteralPath $Path) {
        $lineNumber++
        if ([string]::IsNullOrWhiteSpace($line)) {
            continue
        }
        try {
            $items.Add(($line | ConvertFrom-Json -Depth 20))
        }
        catch {
            throw "$Kind file $Path has invalid JSON on line $lineNumber`: $($_.Exception.Message)"
        }
    }
    return @($items)
}

function Get-RequiredText {
    param(
        [object]$Item,
        [string]$Property,
        [string]$Context
    )
    $member = $Item.PSObject.Properties[$Property]
    if ($null -eq $member -or [string]::IsNullOrWhiteSpace([string]$member.Value)) {
        throw "$Context requires non-empty $Property"
    }
    return ([string]$member.Value).Trim()
}

function Get-ActualLabel {
    param(
        [object]$Item,
        [string]$Context
    )
    $labelProperty = $Item.PSObject.Properties["actual_label"]
    if ($null -ne $labelProperty) {
        $label = ([string]$labelProperty.Value).Trim().ToUpperInvariant()
    }
    else {
        $resultProperty = $Item.PSObject.Properties["result"]
        if ($null -eq $resultProperty) {
            throw "$Context requires actual_label or result"
        }
        if ($resultProperty.Value -is [bool]) {
            $label = if ([bool]$resultProperty.Value) { "PASS" } else { "FAIL" }
        }
        else {
            $label = ([string]$resultProperty.Value).Trim().ToUpperInvariant()
        }
    }
    if ($label -notin @("PASS", "FAIL", "ERROR")) {
        throw "$Context label must be PASS, FAIL, or ERROR; got $label"
    }
    return $label
}

$calibrationItems = @(Read-JsonLines -Path $Calibration -Kind "Calibration")
if ($calibrationItems.Count -ne 15) {
    throw "Agent acceptance requires exactly 15 calibration requirements; got $($calibrationItems.Count)"
}

$expectedIDs = @{}
foreach ($item in $calibrationItems) {
    $id = Get-RequiredText -Item $item -Property "id" -Context "Calibration case"
    if ($expectedIDs.ContainsKey($id)) {
        throw "Calibration contains duplicate id $id"
    }
    $expectedIDs[$id] = $true
}

$resultItems = @(Read-JsonLines -Path $Result -Kind "Result")
$seen = @{}
$failures = [Collections.Generic.List[object]]::new()
foreach ($item in $resultItems) {
    $id = Get-RequiredText -Item $item -Property "id" -Context "Acceptance result"
    if (-not $expectedIDs.ContainsKey($id)) {
        throw "Result file contains unknown calibration id $id"
    }
    if ($seen.ContainsKey($id)) {
        throw "Result file contains duplicate id $id"
    }
    $seen[$id] = $true
    $label = Get-ActualLabel -Item $item -Context "Acceptance result $id"
    if ($label -ne "PASS") {
        $reasonProperty = $item.PSObject.Properties["reason"]
        $failures.Add([ordered]@{
            id = $id
            label = $label
            reason = if ($null -eq $reasonProperty) { "" } else { [string]$reasonProperty.Value }
        })
    }
}

$missing = @($expectedIDs.Keys | Where-Object { -not $seen.ContainsKey($_) } | Sort-Object)
$passed = $missing.Count -eq 0 -and $failures.Count -eq 0 -and $resultItems.Count -eq 15
$report = [ordered]@{
    schemaVersion = 1
    generatedAt = [DateTime]::UtcNow.ToString("o")
    passed = $passed
    calibration = [IO.Path]::GetRelativePath($repoRoot, $Calibration).Replace("\", "/")
    result = [IO.Path]::GetRelativePath($repoRoot, $Result).Replace("\", "/")
    acceptance = [ordered]@{
        requiredCases = 15
        requiredPasses = 15
        errorsAllowed = 0
    }
    summary = [ordered]@{
        cases = $resultItems.Count
        passed = $resultItems.Count - $failures.Count
        failed = $failures.Count
        missing = $missing
    }
    failures = $failures
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Output) | Out-Null
$report | ConvertTo-Json -Depth 20 | Set-Content -Encoding utf8 -Path $Output

if (-not $passed) {
    throw "Agent acceptance failed. Report: $Output"
}
Write-Host "Agent acceptance passed. Report: $Output"
