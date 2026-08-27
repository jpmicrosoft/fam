[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Manifest,
    [Parameter(Mandatory)]
    [string]$ProjectEndpoint,
    [string]$Model = "gpt-5-mini",
    [string]$Binary,
    [string]$OutputDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
if (-not [IO.Path]::IsPathRooted($Manifest)) {
    $Manifest = Join-Path $repoRoot $Manifest
}
$Manifest = [IO.Path]::GetFullPath($Manifest)
if (-not (Test-Path -LiteralPath $Manifest -PathType Leaf)) {
    throw "Agent manifest does not exist: $Manifest"
}

if ([string]::IsNullOrWhiteSpace($Binary)) {
    $executableName = if ($IsWindows) {
        "fam.exe"
    }
    else {
        "fam"
    }
    $candidate = Join-Path $repoRoot (Join-Path "bin" $executableName)
    if (Test-Path -LiteralPath $candidate -PathType Leaf) {
        $Binary = $candidate
    }
    else {
        $Binary = (Get-Command $executableName -ErrorAction Stop).Source
    }
}
elseif (-not [IO.Path]::IsPathRooted($Binary)) {
    $Binary = Join-Path $repoRoot $Binary
}
$Binary = [IO.Path]::GetFullPath($Binary)
if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
    throw "fam binary does not exist: $Binary"
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $OutputDirectory = Join-Path $repoRoot ".release-qualification\agent-acceptance\$timestamp"
}
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$python = Get-Command python -ErrorAction Stop
$fixture = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.calibration.jsonl"
$contract = Join-Path $repoRoot "qa\evaluator-calibration\smoke_core_contract.py"
$contractTest = Join-Path $repoRoot "qa\evaluator-calibration\test_smoke_core_contract.py"
$prompt = Join-Path $repoRoot "qa\evaluator-calibration\smoke-core.requirement-prompt.txt"
$runner = Join-Path $repoRoot "scripts\Invoke-LiveEvaluatorCalibration.py"

& $python.Source -c "import azure.ai.projects, azure.identity, openai" | Out-Null
if ($LASTEXITCODE -ne 0) {
    throw "Live agent acceptance requires the packages in qa/evaluator-calibration/requirements.txt."
}

& $python.Source $contractTest --fixture $fixture
if ($LASTEXITCODE -ne 0) {
    throw "The deterministic evaluator contract does not match the calibration fixture."
}

$sourceCases = [Collections.Generic.List[object]]::new()
$lineNumber = 0
foreach ($line in Get-Content -LiteralPath $fixture) {
    $lineNumber++
    if ([string]::IsNullOrWhiteSpace($line)) {
        continue
    }
    try {
        $sourceCases.Add(($line | ConvertFrom-Json -Depth 20))
    }
    catch {
        throw "Calibration fixture has invalid JSON on line $lineNumber`: $($_.Exception.Message)"
    }
}
if ($sourceCases.Count -ne 15) {
    throw "Agent acceptance requires exactly 15 calibration requirements; got $($sourceCases.Count)"
}

$responsePath = Join-Path $OutputDirectory "agent-responses.jsonl"
$responseLines = [Collections.Generic.List[string]]::new()
foreach ($case in $sourceCases) {
    $arguments = @(
        "prompt",
        "smoke",
        "-f", $Manifest,
        "--prompt", [string]$case.query,
        "--output", "json"
    )
    $raw = & $Binary @arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Agent invocation failed for calibration case $($case.id)."
    }
    try {
        $invocation = $raw | ConvertFrom-Json -Depth 20
    }
    catch {
        throw "Agent invocation for $($case.id) did not return valid JSON: $($_.Exception.Message)"
    }
    if ([string]::IsNullOrWhiteSpace([string]$invocation.outputText)) {
        throw "Agent invocation for $($case.id) returned no outputText."
    }
    $responseLines.Add((
        [ordered]@{
            id = [string]$case.id
            query = [string]$case.query
            response = [string]$invocation.outputText
            expected_behavior = [string]$case.expected_behavior
            validation_rules = $case.validation_rules
            agent = [string]$invocation.agent
            response_id = [string]$invocation.responseId
        } | ConvertTo-Json -Compress -Depth 20
    ))
}
$responseLines | Set-Content -Encoding utf8 -Path $responsePath

& $python.Source $runner `
    --endpoint $ProjectEndpoint `
    --model $Model `
    --fixture $responsePath `
    --contract-code $contract `
    --requirement-prompt $prompt `
    --output-dir $OutputDirectory `
    --runs 1 `
    --purpose "agent-acceptance" `
    --evaluation-name-prefix "smoke-core-v3-agent-acceptance"
if ($LASTEXITCODE -ne 0) {
    throw "Live agent acceptance evaluation failed."
}

$result = Join-Path $OutputDirectory "run-1.jsonl"
$report = Join-Path $OutputDirectory "agent-acceptance-report.json"
& (Join-Path $PSScriptRoot "Test-AgentAcceptance.ps1") `
    -Calibration $fixture `
    -Result $result `
    -Output $report

Write-Host "Live agent acceptance passed. Report: $report"
