[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$ProjectEndpoint,
    [string]$Model = "gpt-5-mini",
    [string]$OutputDirectory,
    [ValidateRange(3, 20)]
    [int]$Runs = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $OutputDirectory = Join-Path $repoRoot ".release-qualification\evaluator-calibration\live\$timestamp"
}
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)

$python = Get-Command python -ErrorAction Stop
$fixture = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.calibration.jsonl"
$contract = Join-Path $repoRoot "qa\evaluator-calibration\smoke_core_contract.py"
$contractTest = Join-Path $repoRoot "qa\evaluator-calibration\test_smoke_core_contract.py"
$prompt = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.requirement-prompt.txt"
$runner = Join-Path $repoRoot "scripts\Invoke-LiveEvaluatorCalibration.py"

& $python.Source -c "import azure.ai.projects, azure.identity, openai" | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Live evaluator calibration requires azure-ai-projects, azure-identity, and openai."
}

& $python.Source $contractTest --fixture $fixture
if ($LASTEXITCODE -ne 0) {
    throw "The deterministic evaluator contract does not match the calibration fixture."
}

& $python.Source $runner `
    --endpoint $ProjectEndpoint `
    --model $Model `
    --fixture $fixture `
    --contract-code $contract `
    --requirement-prompt $prompt `
    --output-dir $OutputDirectory `
    --runs $Runs
if ($LASTEXITCODE -ne 0) {
    throw "Live evaluator execution failed."
}

$resultFiles = 1..$Runs | ForEach-Object {
    Join-Path $OutputDirectory "run-$_.jsonl"
}
$report = Join-Path $OutputDirectory "calibration-report.json"
& (Join-Path $PSScriptRoot "Test-EvaluatorCalibration.ps1") `
    -Results $resultFiles `
    -Output $report `
    -MinimumRuns $Runs

Write-Host "Live evaluator calibration passed. Report: $report"
