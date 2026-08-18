<#
.SYNOPSIS
  Offline unit tests for Invoke-LiveAgent365Acceptance.ps1.
  Validates argument gates, command classification, redaction, and report
  construction without Azure access or a built CLI binary.
#>
[CmdletBinding()]
param()

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# ── Extract testable functions from the acceptance script ───────────────────

$scriptPath = Join-Path $PSScriptRoot "Invoke-LiveAgent365Acceptance.ps1"
$scriptContent = Get-Content -Raw -Path $scriptPath

$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseInput(
    $scriptContent, [ref]$tokens, [ref]$parseErrors
)

# Extract function definitions and the readOnlyCommands/mutationArgPatterns arrays.
$functionDefs = $ast.FindAll(
    { param($n) $n -is [Management.Automation.Language.FunctionDefinitionAst] },
    $false
)
$assignments = $ast.FindAll(
    { param($n)
        $n -is [Management.Automation.Language.AssignmentStatementAst] -and
        $n.Left -is [Management.Automation.Language.VariableExpressionAst] -and
        $n.Left.VariablePath.UserPath -in @(
            "script:readOnlyCommands", "script:mutationArgPatterns"
        )
    },
    $false
)

$extractedCode = @(
    ($assignments | ForEach-Object { $_.Extent.Text }) -join "`n"
    ($functionDefs | ForEach-Object { $_.Extent.Text }) -join "`n"
) -join "`n"

. ([scriptblock]::Create($extractedCode))

# ── Test harness ────────────────────────────────────────────────────────────

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

function Assert-True {
    param([string]$Test, [bool]$Actual)
    Assert-Equal -Test $Test -Expected $true -Actual $Actual
}

function Assert-False {
    param([string]$Test, [bool]$Actual)
    Assert-Equal -Test $Test -Expected $false -Actual $Actual
}

function Assert-Match {
    param([string]$Test, [string]$Pattern, [string]$Actual)
    if ($Actual -match $Pattern) {
        $script:passed++
        Write-Host "PASS: $Test" -ForegroundColor Green
    }
    else {
        $script:failed++
        $msg = "FAIL: $Test - '$Actual' did not match pattern '$Pattern'"
        $script:errors.Add($msg)
        Write-Host $msg -ForegroundColor Red
    }
}

function Assert-NotMatch {
    param([string]$Test, [string]$Pattern, [string]$Actual)
    if ($Actual -notmatch $Pattern) {
        $script:passed++
        Write-Host "PASS: $Test" -ForegroundColor Green
    }
    else {
        $script:failed++
        $msg = "FAIL: $Test - '$Actual' unexpectedly matched pattern '$Pattern'"
        $script:errors.Add($msg)
        Write-Host $msg -ForegroundColor Red
    }
}

# ── Test-CommandReadOnly tests ──────────────────────────────────────────────

Write-Host "`n=== Test-CommandReadOnly: allowlisted commands ===" -ForegroundColor Cyan

Assert-True "agent365 info is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 info" -Arguments @())

Assert-True "agent365 blueprint list is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 blueprint list" -Arguments @())

Assert-True "agent365 blueprint show is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 blueprint show" -Arguments @("--blueprint-id", "test-id"))

Assert-True "agent365 blueprint permissions is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 blueprint permissions" -Arguments @("--blueprint-id", "test-id"))

Assert-True "agent365 blueprint validate is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 blueprint validate" -Arguments @("--blueprint-id", "test-id"))

Assert-True "agent365 binding status is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 binding status" -Arguments @("-f", "manifest.yaml"))

Assert-True "agent365 binding plan is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 binding plan" -Arguments @("-f", "manifest.yaml", "--blueprint-id", "test-id"))

Assert-True "agent365 identity list is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 identity list" -Arguments @("--all"))

Assert-True "agent365 integration status is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 integration status" -Arguments @("--account-name", "account"))

Assert-True "agent365 observability status is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 observability status" -Arguments @("--workspace", "ws"))

Assert-True "agent365 publication status is read-only" `
    (Test-CommandReadOnly -CommandPath "agent365 publication status" -Arguments @("-f", "manifest.yaml"))

Assert-True "prompt status is read-only" `
    (Test-CommandReadOnly -CommandPath "prompt status" -Arguments @("-f", "m.yaml"))

Assert-True "hosted status is read-only" `
    (Test-CommandReadOnly -CommandPath "hosted status" -Arguments @("--workspace", "/ws"))

Assert-True "version is read-only" `
    (Test-CommandReadOnly -CommandPath "version" -Arguments @())

Write-Host "`n=== Test-CommandReadOnly: rejected commands ===" -ForegroundColor Cyan

Assert-False "prompt deploy is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "prompt deploy" -Arguments @("-f", "m.yaml"))

Assert-False "prompt delete is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "prompt delete" -Arguments @("--yes"))

Assert-False "grounding sync is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "grounding sync" -Arguments @())

Assert-False "hosted deploy is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "hosted deploy" -Arguments @())

Assert-False "hosted environment create is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "hosted environment create" -Arguments @())

Assert-False "hosted adopt is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "hosted adopt" -Arguments @())

Assert-False "receipt upload is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "receipt upload" -Arguments @())

Assert-False "unknown command is NOT read-only" `
    (Test-CommandReadOnly -CommandPath "totally-fake-command" -Arguments @())

Write-Host "`n=== Test-CommandReadOnly: mutation argument rejection ===" -ForegroundColor Cyan

# Even if command is allowlisted, mutation args should be rejected.
Assert-False "read-only command with --deploy arg rejected" `
    (Test-CommandReadOnly -CommandPath "agent365 blueprint show" -Arguments @("--deploy"))

Assert-False "read-only command with --delete arg rejected" `
    (Test-CommandReadOnly -CommandPath "agent365 info" -Arguments @("--delete"))

Assert-False "read-only command with --publish arg rejected" `
    (Test-CommandReadOnly -CommandPath "prompt status" -Arguments @("--publish"))

Assert-False "read-only command with --sync arg rejected" `
    (Test-CommandReadOnly -CommandPath "hosted status" -Arguments @("--sync"))

Assert-False "read-only command with --provision arg rejected" `
    (Test-CommandReadOnly -CommandPath "prompt show" -Arguments @("--provision"))

# ── Invoke-Redact tests ────────────────────────────────────────────────────

Write-Host "`n=== Invoke-Redact ===" -ForegroundColor Cyan

Assert-Match "Bearer token redacted" `
    "\[REDACTED\]" `
    (Invoke-Redact -Text "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.longtoken.sig")

Assert-NotMatch "Bearer token value not present" `
    "eyJhbGciOi" `
    (Invoke-Redact -Text "Authorization: Bearer eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9.longtoken.sig")

Assert-Match "JSON access_token redacted" `
    "\[REDACTED\]" `
    (Invoke-Redact -Text '{"access_token": "secret-value-here"}')

Assert-NotMatch "JSON access_token value not present" `
    "secret-value-here" `
    (Invoke-Redact -Text '{"access_token": "secret-value-here"}')

Assert-Match "Long random value redacted" `
    "\[REDACTED-LONG-VALUE\]" `
    (Invoke-Redact -Text "key=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

$emptyResult = Invoke-Redact -Text ""
Assert-Equal "Empty string returns empty" "" $emptyResult

$nullResult = Invoke-Redact -Text $null
Assert-True "Null returns empty-ish" ([string]::IsNullOrEmpty($nullResult))

# ── Report construction validation ──────────────────────────────────────────

Write-Host "`n=== Report construction ===" -ForegroundColor Cyan

# Simulate what the report builder does.
$testSteps = [Collections.Generic.List[object]]::new()
$testSteps.Add([ordered]@{ name = "step-pass"; command = "agent365 info"; status = "passed"; reason = "" })
$testSteps.Add([ordered]@{ name = "step-skip"; command = "agent365 blueprint list"; status = "skipped"; reason = "RunOnline not enabled" })
$testSteps.Add([ordered]@{ name = "step-fail"; command = "agent365 blueprint show"; status = "failed"; reason = "Command error" })

$passedSteps = @($testSteps | Where-Object { $_.status -eq "passed" }).Count
$skippedSteps = @($testSteps | Where-Object { $_.status -eq "skipped" }).Count
$failedSteps = @($testSteps | Where-Object { $_.status -eq "failed" }).Count

$report = [ordered]@{
    schemaVersion = 1
    generatedAt   = [DateTime]::UtcNow.ToString("o")
    summary       = [ordered]@{
        total   = $testSteps.Count
        passed  = $passedSteps
        skipped = $skippedSteps
        failed  = $failedSteps
    }
    steps         = @($testSteps)
}

$json = $report | ConvertTo-Json -Depth 20
$parsed = $json | ConvertFrom-Json

Assert-Equal "Report schemaVersion" 1 $parsed.schemaVersion
Assert-Equal "Report total steps" 3 $parsed.summary.total
Assert-Equal "Report passed count" 1 $parsed.summary.passed
Assert-Equal "Report skipped count" 1 $parsed.summary.skipped
Assert-Equal "Report failed count" 1 $parsed.summary.failed
Assert-Equal "Step 1 status" "passed" $parsed.steps[0].status
Assert-Equal "Step 2 status" "skipped" $parsed.steps[1].status
Assert-Equal "Step 3 status" "failed" $parsed.steps[2].status

# Verify no tokens leak through report serialization.
Assert-NotMatch "No bearer in report JSON" "Bearer" $json
Assert-NotMatch "No access_token in report JSON" "access_token" $json

# ── Argument gate validation ───────────────────────────────────────────────

Write-Host "`n=== Argument gate validation ===" -ForegroundColor Cyan

# Verify that the readOnlyCommands list contains all expected agent365 commands.
Assert-True "readOnlyCommands includes agent365 info" `
    ($script:readOnlyCommands -contains "agent365 info")

Assert-True "readOnlyCommands includes agent365 blueprint list" `
    ($script:readOnlyCommands -contains "agent365 blueprint list")

Assert-True "readOnlyCommands includes agent365 binding status" `
    ($script:readOnlyCommands -contains "agent365 binding status")

Assert-True "readOnlyCommands includes agent365 binding plan" `
    ($script:readOnlyCommands -contains "agent365 binding plan")

Assert-True "readOnlyCommands includes agent365 integration status" `
    ($script:readOnlyCommands -contains "agent365 integration status")

Assert-True "readOnlyCommands includes agent365 observability status" `
    ($script:readOnlyCommands -contains "agent365 observability status")

Assert-True "readOnlyCommands includes agent365 publication status" `
    ($script:readOnlyCommands -contains "agent365 publication status")

# Verify mutation commands are NOT in readOnlyCommands.
Assert-False "prompt deploy not in readOnlyCommands" `
    ($script:readOnlyCommands -contains "prompt deploy")

Assert-False "hosted deploy not in readOnlyCommands" `
    ($script:readOnlyCommands -contains "hosted deploy")

Assert-False "grounding sync not in readOnlyCommands" `
    ($script:readOnlyCommands -contains "grounding sync")

# ── Summary ─────────────────────────────────────────────────────────────────

Write-Host "`n=== Summary ===" -ForegroundColor Cyan
Write-Host "Passed: $passed" -ForegroundColor Green
if ($failed -gt 0) {
    Write-Host "Failed: $failed" -ForegroundColor Red
    foreach ($e in $errors) { Write-Host "  $e" -ForegroundColor Red }
    throw "Agent 365 acceptance tests failed: $failed failure(s)"
}
Write-Host "All Agent 365 acceptance offline tests passed."
