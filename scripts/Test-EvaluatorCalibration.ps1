[CmdletBinding()]
param(
    [string]$Calibration,
    [Parameter(Mandatory)]
    [string[]]$Results,
    [string]$Output,
    [ValidateRange(1, 100)]
    [int]$MinimumRuns = 3,
    [ValidateRange(0, 1)]
    [double]$MinimumBorderlineAccuracy = 0.8
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# Polyfill for [IO.Path]::GetRelativePath which is unavailable on .NET Framework (Windows PowerShell 5.1).
function Get-RelativePath {
    param([string]$BasePath, [string]$FullPath)
    if ([IO.Path].GetMethod('GetRelativePath')) {
        return [IO.Path]::GetRelativePath($BasePath, $FullPath)
    }
    $baseUri = [Uri]::new($BasePath.TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar)
    $fullUri = [Uri]::new($FullPath)
    return [Uri]::UnescapeDataString($baseUri.MakeRelativeUri($fullUri).ToString()).Replace('/', [IO.Path]::DirectorySeparatorChar)
}

if ([string]::IsNullOrWhiteSpace($Calibration)) {
    $Calibration = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.calibration.jsonl"
}
elseif (-not [IO.Path]::IsPathRooted($Calibration)) {
    $Calibration = Join-Path $repoRoot $Calibration
}
$Calibration = [IO.Path]::GetFullPath($Calibration)

if ([string]::IsNullOrWhiteSpace($Output)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $Output = Join-Path $repoRoot ".release-qualification\evaluator-calibration\$timestamp\calibration-report.json"
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
            $items.Add(($line | ConvertFrom-Json))
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
    throw "Calibration must contain exactly 15 cases; got $($calibrationItems.Count)"
}

$calibrationByID = @{}
$categoryCounts = @{
    "clear-good" = 0
    "clear-bad" = 0
    "borderline" = 0
}
foreach ($item in $calibrationItems) {
    $id = Get-RequiredText -Item $item -Property "id" -Context "Calibration case"
    if ($calibrationByID.ContainsKey($id)) {
        throw "Calibration contains duplicate id $id"
    }
    $category = Get-RequiredText -Item $item -Property "category" -Context "Calibration case $id"
    if (-not $categoryCounts.ContainsKey($category)) {
        throw "Calibration case $id has unsupported category $category"
    }
    $expected = (Get-RequiredText -Item $item -Property "expected_label" -Context "Calibration case $id").ToUpperInvariant()
    if ($expected -notin @("PASS", "FAIL")) {
        throw "Calibration case $id expected_label must be PASS or FAIL"
    }
    if ($category -eq "clear-good" -and $expected -ne "PASS") {
        throw "Clear-good calibration case $id must expect PASS"
    }
    if ($category -eq "clear-bad" -and $expected -ne "FAIL") {
        throw "Clear-bad calibration case $id must expect FAIL"
    }
    [void](Get-RequiredText -Item $item -Property "query" -Context "Calibration case $id")
    [void](Get-RequiredText -Item $item -Property "response" -Context "Calibration case $id")
    [void](Get-RequiredText -Item $item -Property "rationale" -Context "Calibration case $id")
    $calibrationByID[$id] = [ordered]@{
        id = $id
        category = $category
        expectedLabel = $expected
    }
    $categoryCounts[$category]++
}
foreach ($category in @("clear-good", "clear-bad", "borderline")) {
    if ($categoryCounts[$category] -ne 5) {
        throw "Calibration category $category must contain exactly 5 cases; got $($categoryCounts[$category])"
    }
}

$resolvedResults = [Collections.Generic.List[string]]::new()
foreach ($path in $Results) {
    $resolved = if ([IO.Path]::IsPathRooted($path)) {
        [IO.Path]::GetFullPath($path)
    }
    else {
        [IO.Path]::GetFullPath((Join-Path $repoRoot $path))
    }
    $resolvedResults.Add($resolved)
}

$runReports = [Collections.Generic.List[object]]::new()
$labelsByCase = @{}
foreach ($id in $calibrationByID.Keys) {
    $labelsByCase[$id] = [Collections.Generic.List[string]]::new()
}

foreach ($resultPath in $resolvedResults) {
    $resultItems = @(Read-JsonLines -Path $resultPath -Kind "Result")
    $resultByID = @{}
    foreach ($item in $resultItems) {
        $id = Get-RequiredText -Item $item -Property "id" -Context "Result in $resultPath"
        if (-not $calibrationByID.ContainsKey($id)) {
            throw "Result file $resultPath contains unknown calibration id $id"
        }
        if ($resultByID.ContainsKey($id)) {
            throw "Result file $resultPath contains duplicate id $id"
        }
        $resultByID[$id] = Get-ActualLabel -Item $item -Context "Result $id in $resultPath"
    }

    $missing = @($calibrationByID.Keys | Where-Object { -not $resultByID.ContainsKey($_) } | Sort-Object)
    $clearCorrect = 0
    $borderlineCorrect = 0
    $errors = 0
    foreach ($id in $calibrationByID.Keys) {
        if (-not $resultByID.ContainsKey($id)) {
            continue
        }
        $actual = $resultByID[$id]
        $labelsByCase[$id].Add($actual)
        if ($actual -eq "ERROR") {
            $errors++
            continue
        }
        $case = $calibrationByID[$id]
        if ($actual -eq $case.expectedLabel) {
            if ($case.category -eq "borderline") {
                $borderlineCorrect++
            }
            else {
                $clearCorrect++
            }
        }
    }
    $clearAccuracy = $clearCorrect / 10.0
    $borderlineAccuracy = $borderlineCorrect / 5.0
    $runPassed = $missing.Count -eq 0 -and
        $errors -eq 0 -and
        $clearCorrect -eq 10 -and
        $borderlineAccuracy -ge $MinimumBorderlineAccuracy
    $runReports.Add([ordered]@{
        file = (Get-RelativePath $repoRoot $resultPath).Replace("\", "/")
        passed = $runPassed
        cases = $resultItems.Count
        missing = $missing
        errors = $errors
        clearCorrect = $clearCorrect
        clearTotal = 10
        clearAccuracy = $clearAccuracy
        borderlineCorrect = $borderlineCorrect
        borderlineTotal = 5
        borderlineAccuracy = $borderlineAccuracy
    })
}

$unstableCases = [Collections.Generic.List[string]]::new()
if ($resolvedResults.Count -ge 2) {
    foreach ($id in $labelsByCase.Keys) {
        $distinct = @($labelsByCase[$id] | Sort-Object -Unique)
        if ($distinct.Count -ne 1) {
            $unstableCases.Add($id)
        }
    }
}
$stable = $unstableCases.Count -eq 0
$enoughRuns = $resolvedResults.Count -ge $MinimumRuns
$allRunsPassed = @($runReports | Where-Object { -not $_.passed }).Count -eq 0
$passed = $enoughRuns -and $allRunsPassed -and $stable

$report = [ordered]@{
    schemaVersion = 1
    generatedAt = [DateTime]::UtcNow.ToString("o")
    passed = $passed
    calibration = (Get-RelativePath $repoRoot $Calibration).Replace("\", "/")
    acceptance = [ordered]@{
        minimumRuns = $MinimumRuns
        minimumBorderlineAccuracy = $MinimumBorderlineAccuracy
        clearAccuracyRequired = 1.0
        errorsAllowed = 0
        stableAcrossRuns = $true
    }
    summary = [ordered]@{
        runCount = $resolvedResults.Count
        enoughRuns = $enoughRuns
        allRunsPassed = $allRunsPassed
        stable = $stable
        unstableCases = $unstableCases
    }
    runs = $runReports
}

New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Output) | Out-Null
$report | ConvertTo-Json -Depth 20 | Set-Content -Encoding utf8 -Path $Output

if (-not $passed) {
    throw "Evaluator calibration failed. Report: $Output"
}
Write-Host "Evaluator calibration passed. Report: $Output"
