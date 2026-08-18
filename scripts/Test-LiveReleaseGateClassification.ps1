<#
.SYNOPSIS
  Unit tests for Get-MinimumGate and Resolve-SecurityBoolFlag in Invoke-LiveRelease.ps1.
  Validates that destructive, mutation, and ambiguous flag scenarios are classified correctly.
#>
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ---------- bootstrap: dot-source the classification functions ----------

# We source the script in a controlled way by extracting only the function
# definitions and the command catalog arrays. To avoid running the full script
# (which requires -Config etc.), we parse and extract the needed pieces.

$scriptPath = Join-Path $PSScriptRoot "Invoke-LiveRelease.ps1"
$scriptContent = Get-Content -Raw -Path $scriptPath

# Extract functions and catalog arrays by parsing the AST.
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseInput(
    $scriptContent, [ref]$tokens, [ref]$parseErrors
)

# Collect function definitions and variable assignments for the catalog arrays.
$functionDefs = $ast.FindAll(
    { param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] },
    $false
)
$assignments = $ast.FindAll(
    { param($n)
        $n -is [Management.Automation.Language.AssignmentStatementAst] -and
        $n.Left -is [Management.Automation.Language.VariableExpressionAst] -and
        $n.Left.VariablePath.UserPath -in @(
            "offlineCommands", "mutationCommands", "destructiveCommands",
            "gateRank"
        )
    },
    $false
)

# Extract cobra value sets and their foreach population loops by finding the
# block between the first $script:cobraTrueValues line and the
# Resolve-SecurityBoolFlag function definition.
$lines = $scriptContent -split "`n"
$cobraStart = $null
$cobraEnd = $null
for ($i = 0; $i -lt $lines.Count; $i++) {
    if ($null -eq $cobraStart -and $lines[$i] -match 'cobraTrueValues') {
        $cobraStart = $i
    }
    if ($null -ne $cobraStart -and $null -eq $cobraEnd -and $lines[$i] -match 'function Resolve-SecurityBoolFlag') {
        $cobraEnd = $i - 1
        break
    }
}
$cobraBlock = if ($null -ne $cobraStart -and $null -ne $cobraEnd) {
    ($lines[$cobraStart..$cobraEnd] -join "`n")
} else { "" }

$extractedCode = @(
    $cobraBlock
    ($functionDefs | ForEach-Object { $_.Extent.Text }) -join "`n"
    ($assignments | ForEach-Object { $_.Extent.Text }) -join "`n"
) -join "`n"

# Execute in current scope.
. ([scriptblock]::Create($extractedCode))

# ---------- test harness ----------

$passed = 0
$failed = 0
$errors = [Collections.Generic.List[string]]::new()

function Assert-Equal {
    param([string]$Test, $Expected, $Actual)
    if ($Expected -ne $Actual) {
        $script:failed++
        $msg = "FAIL: $Test - expected '$Expected', got '$Actual'"
        $script:errors.Add($msg)
        Write-Host $msg -ForegroundColor Red
    }
    else {
        $script:passed++
        Write-Host "PASS: $Test" -ForegroundColor Green
    }
}

function Assert-Throws {
    param([string]$Test, [scriptblock]$Action, [string]$Pattern)
    $threw = $false
    try { & $Action } catch {
        $threw = $true
        if ($Pattern -and $_.Exception.Message -notmatch [regex]::Escape($Pattern)) {
            $script:failed++
            $msg = "FAIL: $Test - threw but message '$($_.Exception.Message)' did not match '$Pattern'"
            $script:errors.Add($msg)
            Write-Host $msg -ForegroundColor Red
            return
        }
    }
    if (-not $threw) {
        $script:failed++
        $msg = "FAIL: $Test - expected exception but none was thrown"
        $script:errors.Add($msg)
        Write-Host $msg -ForegroundColor Red
    }
    else {
        $script:passed++
        Write-Host "PASS: $Test" -ForegroundColor Green
    }
}

# ---------- Resolve-SecurityBoolFlag tests ----------

Write-Host "`n=== Resolve-SecurityBoolFlag ===" -ForegroundColor Cyan

Assert-Equal "bare flag absent returns null" `
    $null (Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--output", "json"))

Assert-Equal "bare --dry-run returns true" `
    $true (Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run", "--output", "json"))

Assert-Equal "--dry-run=true returns true" `
    $true (Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run=true"))

Assert-Equal "--dry-run=false returns false" `
    $false (Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run=false"))

Assert-Equal "--prune=1 returns true" `
    $true (Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=1"))

Assert-Equal "--prune=0 returns false" `
    $false (Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=0"))

Assert-Equal "--prune=yes returns true" `
    $true (Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=yes"))

Assert-Equal "--prune=t returns true" `
    $true (Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=t"))

Assert-Equal "--prune=TRUE case insensitive" `
    $true (Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=TRUE"))

Assert-Throws "duplicate --dry-run --dry-run rejects" `
    { Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run", "--dry-run") } `
    "Duplicate security-sensitive flag"

Assert-Throws "conflicting --dry-run --dry-run=false rejects" `
    { Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run", "--dry-run=false") } `
    "Conflicting duplicate security-sensitive flag"

Assert-Throws "unrecognized value --prune=maybe rejects" `
    { Resolve-SecurityBoolFlag -FlagName "prune" -Arguments @("--prune=maybe") } `
    "Unrecognized boolean value"

Assert-Throws "ambiguous bare --dry-run followed by false rejects" `
    { Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-run", "false") } `
    "Ambiguous security-sensitive flag"

Assert-Equal "unrelated --dry-runner does not match --dry-run" `
    $null (Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments @("--dry-runner"))

# ---------- Get-MinimumGate tests ----------

Write-Host "`n=== Get-MinimumGate ===" -ForegroundColor Cyan

# Finding #2: receipt upload must be mutation
Assert-Equal "receipt upload -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "receipt upload" -Arguments @("--file", "r.json"))

Assert-Equal "receipt upload with --dry-run -> online-read (dry-run neutralizes mutation)" `
    "online-read" (Get-MinimumGate -CommandName "receipt upload" -Arguments @("--file", "r.json", "--dry-run"))

# Finding #1: grounding sync destructive flags
Assert-Equal "grounding sync plain -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--manifest", "m.yaml"))

Assert-Equal "grounding sync --prune -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune", "--yes"))

Assert-Equal "grounding sync --prune=true -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune=true", "--yes"))

Assert-Equal "grounding sync --prune=1 -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune=1", "--yes"))

Assert-Equal "grounding sync --delete-replaced-uploads -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--delete-replaced-uploads", "--yes"))

Assert-Equal "grounding sync --delete-replaced-uploads --yes -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--delete-replaced-uploads", "--yes"))

Assert-Equal "grounding sync --delete-pruned-uploads --prune -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--delete-pruned-uploads", "--prune", "--yes"))

Assert-Equal "grounding sync --prune --dry-run -> online-read (dry-run neutralizes)" `
    "online-read" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune", "--dry-run"))

Assert-Equal "grounding sync --prune=false -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune=false"))

# Exploit string from finding: --dry-run=false must NOT neutralize
Assert-Equal "grounding sync --prune --dry-run=false -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune", "--dry-run=false"))

Assert-Equal "prompt delete --dry-run=false -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "prompt delete" -Arguments @("--dry-run=false", "--yes"))

# Fail-closed: duplicate/conflicting flags must throw
Assert-Throws "grounding sync --prune --prune duplicate rejects" `
    { Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune", "--prune") } `
    "Duplicate security-sensitive flag"

Assert-Throws "grounding sync --dry-run --dry-run=false conflict rejects" `
    { Get-MinimumGate -CommandName "grounding sync" -Arguments @("--prune", "--dry-run", "--dry-run=false") } `
    "Conflicting duplicate security-sensitive flag"

# Safe/read-only commands
Assert-Equal "grounding validate -> offline" `
    "offline" (Get-MinimumGate -CommandName "grounding validate" -Arguments @("--manifest", "m.yaml"))

Assert-Equal "prompt status -> online-read" `
    "online-read" (Get-MinimumGate -CommandName "prompt status" -Arguments @("--manifest", "m.yaml"))

Assert-Equal "version -> offline" `
    "offline" (Get-MinimumGate -CommandName "version" -Arguments @())

Assert-Equal "agent365 info -> offline" `
    "offline" (Get-MinimumGate -CommandName "agent365 info" -Arguments @())

Assert-Equal "agent365 blueprint show -> online-read" `
    "online-read" (Get-MinimumGate -CommandName "agent365 blueprint show" -Arguments @("--blueprint-id", "00001111-aaaa-2222-bbbb-3333cccc4444"))

Assert-Equal "agent365 identity list -> online-read" `
    "online-read" (Get-MinimumGate -CommandName "agent365 identity list" -Arguments @("--all"))

Assert-Equal "agent365 observability plan -> offline" `
    "offline" (Get-MinimumGate -CommandName "agent365 observability plan" -Arguments @("--workspace", "ws"))

Assert-Equal "agent365 publication info -> offline" `
    "offline" (Get-MinimumGate -CommandName "agent365 publication info" -Arguments @())

Assert-Equal "agent365 integration status -> online-read" `
    "online-read" (Get-MinimumGate -CommandName "agent365 integration status" -Arguments @("--account-name", "account"))

Assert-Equal "agent365 integration set -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "agent365 integration set" -Arguments @("--enabled=true", "--yes"))

# Conditional local mutation
Assert-Equal "quickstart -> offline" `
    "offline" (Get-MinimumGate -CommandName "quickstart" -Arguments @("--type", "hosted", "--non-interactive"))
Assert-Equal "quickstart bootstrap -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "quickstart" -Arguments @("--type", "hosted", "--bootstrap-environment"))
Assert-Equal "quickstart bootstrap false -> offline" `
    "offline" (Get-MinimumGate -CommandName "quickstart" -Arguments @("--type", "hosted", "--bootstrap-environment=false"))
Assert-Equal "hosted adopt -> offline" `
    "offline" (Get-MinimumGate -CommandName "hosted adopt" -Arguments @("--source", "src", "--destination", "workspace"))
Assert-Equal "hosted adopt bootstrap -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "hosted adopt" -Arguments @("--source", "src", "--destination", "workspace", "--bootstrap-environment"))
Assert-Equal "hosted adopt bootstrap false -> offline" `
    "offline" (Get-MinimumGate -CommandName "hosted adopt" -Arguments @("--source", "src", "--destination", "workspace", "--bootstrap-environment=false"))

# Standard mutation commands
Assert-Equal "hosted environment create -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "hosted environment create" -Arguments @("--workspace", "ws", "--environment", "dev"))

Assert-Equal "prompt deploy -> mutation" `
    "mutation" (Get-MinimumGate -CommandName "prompt deploy" -Arguments @("--manifest", "m.yaml"))

Assert-Equal "prompt delete -> destructive" `
    "destructive" (Get-MinimumGate -CommandName "prompt delete" -Arguments @("--yes"))

Assert-Equal "prompt delete --dry-run -> online-read (dry-run safe)" `
    "online-read" (Get-MinimumGate -CommandName "prompt delete" -Arguments @("--dry-run"))

# ---------- summary ----------

Write-Host "`n=== Summary ===" -ForegroundColor Cyan
Write-Host "Passed: $passed" -ForegroundColor Green
if ($failed -gt 0) {
    Write-Host "Failed: $failed" -ForegroundColor Red
    foreach ($e in $errors) { Write-Host "  $e" -ForegroundColor Red }
    throw "Gate classification tests failed: $failed failure(s)"
}
Write-Host "All gate classification tests passed."
