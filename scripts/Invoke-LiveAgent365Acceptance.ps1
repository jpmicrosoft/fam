<#
.SYNOPSIS
  Live acceptance harness for Agent 365 CLI commands.
  Opt-in, noninteractive, read-only by default, never exposes tokens/secrets.

.DESCRIPTION
  Verifies CLI agent365 info offline, then optionally runs read-only online
  commands (blueprint list/show/permissions/validate, binding status/plan,
  prompt/hosted status, integration status, observability status).

  Every CLI invocation is classified as read-only before execution; any
  mutation command is rejected. Outputs structured JSON results and a
  redacted summary report with passed/skipped/failed steps.

.PARAMETER Binary
  Path to a built foundry-agent-manager CLI binary.

.PARAMETER BlueprintAppId
  Optional Agent 365 blueprint application (client) ID.

.PARAMETER BlueprintObjectId
  Optional Agent 365 blueprint Microsoft Entra object ID.

.PARAMETER ExpectedTenantId
  Optional Microsoft Entra tenant ID passed to online Agent 365 commands.

.PARAMETER AgentIdentityObjectId
  Optional Agent ID identity Microsoft Entra object ID.

.PARAMETER BlueprintPrincipalObjectId
  Optional Agent ID blueprint principal Microsoft Entra object ID.

.PARAMETER PromptManifest
  Optional path to a Prompt Agent manifest for binding status/plan.

.PARAMETER HostedWorkspace
  Optional path to a Hosted Agent azd workspace for binding status/plan.

.PARAMETER HostedEnvironment
  Optional Hosted Agent azd environment name.

.PARAMETER HostedService
  Optional Hosted Agent service name.

.PARAMETER FoundrySubscriptionId
  Optional Azure subscription ID for integration status.

.PARAMETER FoundryResourceGroup
  Optional Azure resource group for integration status.

.PARAMETER FoundryAccountName
  Optional Foundry account name for integration status.

.PARAMETER RunOnline
  Switch to enable online (Graph API) read-only commands. Without this,
  only offline verification runs.

.PARAMETER EnableOwners
  Switch to enable owners/sponsors queries (may require Application.Read.All).

.PARAMETER OutputDirectory
  Optional output directory for JSON results. Defaults to
  .release-qualification/agent365-acceptance/<timestamp>.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Binary,
    [string]$BlueprintAppId,
    [string]$BlueprintObjectId,
    [string]$ExpectedTenantId,
    [string]$AgentIdentityObjectId,
    [string]$BlueprintPrincipalObjectId,
    [string]$PromptManifest,
    [string]$HostedWorkspace,
    [string]$HostedEnvironment,
    [string]$HostedService,
    [string]$FoundrySubscriptionId,
    [string]$FoundryResourceGroup,
    [string]$FoundryAccountName,
    [switch]$RunOnline,
    [switch]$EnableOwners,
    [string]$OutputDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path

# ── Resolve binary ──────────────────────────────────────────────────────────

if (-not [IO.Path]::IsPathRooted($Binary)) {
    $Binary = Join-Path $repoRoot $Binary
}
$Binary = [IO.Path]::GetFullPath($Binary)
if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
    throw "CLI binary does not exist: $Binary"
}

# ── Resolve output directory ────────────────────────────────────────────────

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $OutputDirectory = Join-Path $repoRoot ".release-qualification\agent365-acceptance\$timestamp"
}
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

# ── Read-only command classification ────────────────────────────────────────

# Allowlist of agent365 subcommands that are provably read-only (no side effects).
$script:readOnlyCommands = @(
    "agent365 info",
    "agent365 blueprint list",
    "agent365 blueprint show",
    "agent365 blueprint permissions",
    "agent365 blueprint validate",
    "agent365 blueprint owners",
    "agent365 blueprint sponsors",
    "agent365 blueprint identities",
    "agent365 identity list",
    "agent365 identity show",
    "agent365 blueprint principal list",
    "agent365 blueprint principal show",
    "agent365 binding status",
    "agent365 binding plan",
    "agent365 integration status",
    "agent365 observability plan",
    "agent365 observability status",
    "agent365 publication info",
    "agent365 publication plan",
    "agent365 publication status",
    "agent365 publication admin-handoff",
    "prompt status",
    "prompt show",
    "prompt preflight",
    "hosted status",
    "hosted show",
    "hosted preflight",
    "hosted diagnose",
    "version"
)

# Arguments that indicate mutation intent regardless of command.
$script:mutationArgPatterns = @(
    "--deploy", "--delete", "--create", "--update", "--sync",
    "--publish", "--promote", "--rollback", "--prune",
    "--enable", "--disable", "--provision", "--allow-bot-update"
)

function Test-CommandReadOnly {
    <#
    .SYNOPSIS
      Returns $true if the command path and arguments are classified as read-only.
      Returns $false and sets $script:classificationReason for rejection.
    #>
    param(
        [Parameter(Mandatory)]
        [string]$CommandPath,
        [object[]]$Arguments
    )
    $script:classificationReason = $null

    # Check command is in allowlist.
    if ($CommandPath -notin $script:readOnlyCommands) {
        $script:classificationReason = "Command '$CommandPath' is not in the read-only allowlist"
        return $false
    }

    # Scan arguments for mutation indicators.
    foreach ($arg in $Arguments) {
        $argStr = [string]$arg
        foreach ($pattern in $script:mutationArgPatterns) {
            if ($argStr -eq $pattern -or $argStr.StartsWith("$pattern=", [StringComparison]::OrdinalIgnoreCase)) {
                $script:classificationReason = "Argument '$argStr' is a mutation indicator"
                return $false
            }
        }
    }

    return $true
}

# ── Token/secret redaction ──────────────────────────────────────────────────

function Invoke-Redact {
    <#
    .SYNOPSIS
      Redacts tokens, secrets, and bearer values from text.
    #>
    param([string]$Text)
    if ([string]::IsNullOrWhiteSpace($Text)) { return $Text }
    $result = $Text
    # Bearer tokens
    $result = $result -replace '(Bearer\s+)[A-Za-z0-9\-_\.]+', '${1}[REDACTED]'
    # Authorization headers
    $result = $result -replace '(?i)(authorization["\s:=]+)[^\s"]+', '${1}[REDACTED]'
    # Access tokens in JSON
    $result = $result -replace '(?i)("(?:access_token|token|secret|password|key)"\s*:\s*")[^"]*"', '${1}[REDACTED]"'
    # GUIDs that look like client secrets (40+ hex chars)
    $result = $result -replace '[A-Za-z0-9\-_]{40,}', '[REDACTED-LONG-VALUE]'
    return $result
}

# ── Step execution engine ───────────────────────────────────────────────────

$script:steps = [Collections.Generic.List[object]]::new()

function Invoke-Step {
    <#
    .SYNOPSIS
      Executes one acceptance step, capturing structured output.
    #>
    param(
        [Parameter(Mandatory)]
        [string]$Name,
        [Parameter(Mandatory)]
        [string]$CommandPath,
        [object[]]$Arguments = @(),
        [string]$SkipReason,
        [switch]$RequiresOnline,
        [switch]$RequiresOwners
    )

    $step = [ordered]@{
        name       = $Name
        command    = $CommandPath
        status     = "pending"
        reason     = ""
        output     = $null
        exitCode   = $null
        startedAt  = $null
        duration   = $null
    }

    # Check skip conditions.
    if (-not [string]::IsNullOrWhiteSpace($SkipReason)) {
        $step.status = "skipped"
        $step.reason = $SkipReason
        $script:steps.Add($step)
        Write-Host "SKIP: $Name -- $SkipReason" -ForegroundColor Yellow
        return
    }

    if ($RequiresOnline -and -not $RunOnline) {
        $step.status = "skipped"
        $step.reason = "RunOnline not enabled"
        $script:steps.Add($step)
        Write-Host "SKIP: $Name -- RunOnline not enabled" -ForegroundColor Yellow
        return
    }

    if ($RequiresOwners -and -not $EnableOwners) {
        $step.status = "skipped"
        $step.reason = "EnableOwners not enabled (may require Application.Read.All)"
        $script:steps.Add($step)
        Write-Host "SKIP: $Name -- EnableOwners not enabled" -ForegroundColor Yellow
        return
    }

    # Enforce read-only classification.
    if (-not (Test-CommandReadOnly -CommandPath $CommandPath -Arguments $Arguments)) {
        $step.status = "failed"
        $step.reason = "REJECTED: $($script:classificationReason)"
        $script:steps.Add($step)
        Write-Host "FAIL: $Name -- $($step.reason)" -ForegroundColor Red
        return
    }

    # Build argument list.
    $cliArgs = @($CommandPath -split " ") + @($Arguments)
    if ($RequiresOnline -and -not [string]::IsNullOrWhiteSpace($ExpectedTenantId)) {
        $cliArgs += @("--tenant-id", $ExpectedTenantId)
    }
    $cliArgs += @("--output", "json")

    $step.startedAt = [DateTime]::UtcNow.ToString("o")
    $sw = [Diagnostics.Stopwatch]::StartNew()

    try {
        $raw = & $Binary @cliArgs 2>&1
        $step.exitCode = $LASTEXITCODE
        $sw.Stop()
        $step.duration = "{0:F1}s" -f $sw.Elapsed.TotalSeconds

        $stdoutLines = @($raw | Where-Object { $_ -is [string] -or $_.GetType().Name -ne 'ErrorRecord' })
        $stderrLines = @($raw | Where-Object { $_ -is [Management.Automation.ErrorRecord] })
        $stdout = ($stdoutLines -join "`n").Trim()

        # Attempt JSON parse.
        try {
            $step.output = $stdout | ConvertFrom-Json -Depth 20
        }
        catch {
            $step.output = $stdout
        }

        if ($step.exitCode -eq 0) {
            $step.status = "passed"
            Write-Host "PASS: $Name" -ForegroundColor Green
        }
        else {
            $stderrText = ($stderrLines | ForEach-Object { $_.ToString() }) -join "`n"
            # Classify known non-fatal exit codes.
            if ($stderrText -match "(?i)not licensed|license") {
                $step.status = "skipped"
                $step.reason = "Not licensed for this operation"
            }
            elseif ($stderrText -match "(?i)permission|forbidden|403|unauthorized|401") {
                $step.status = "skipped"
                $step.reason = "Missing permission"
            }
            elseif ($stderrText -match "(?i)not found|404|no target|does not exist") {
                $step.status = "skipped"
                $step.reason = "No target found"
            }
            elseif ($stderrText -match "(?i)unsupported.*publication|publication.*contract|not supported") {
                $step.status = "skipped"
                $step.reason = "Unsupported publication contract"
            }
            else {
                $step.status = "failed"
                $step.reason = Invoke-Redact -Text $stderrText
            }
            Write-Host "$($step.status.ToUpper()): $Name -- $($step.reason)" -ForegroundColor $(
                if ($step.status -eq "skipped") { "Yellow" } else { "Red" }
            )
        }
    }
    catch {
        $sw.Stop()
        $step.duration = "{0:F1}s" -f $sw.Elapsed.TotalSeconds
        $step.status = "failed"
        $step.reason = Invoke-Redact -Text $_.Exception.Message
        Write-Host "FAIL: $Name -- $($step.reason)" -ForegroundColor Red
    }

    $script:steps.Add($step)
}

# ── Acceptance steps ────────────────────────────────────────────────────────

$hasBlueprintId = -not [string]::IsNullOrWhiteSpace($BlueprintAppId) -or
                  -not [string]::IsNullOrWhiteSpace($BlueprintObjectId)

$blueprintArgs = @()
if (-not [string]::IsNullOrWhiteSpace($BlueprintAppId)) {
    $blueprintArgs += @("--blueprint-id", $BlueprintAppId)
}
if (-not [string]::IsNullOrWhiteSpace($BlueprintObjectId)) {
    $blueprintArgs += @("--blueprint-object-id", $BlueprintObjectId)
}

$hasPromptTarget = -not [string]::IsNullOrWhiteSpace($PromptManifest)
$hasHostedTarget = -not [string]::IsNullOrWhiteSpace($HostedWorkspace)
$hasIdentity = -not [string]::IsNullOrWhiteSpace($AgentIdentityObjectId)
$hasPrincipal = -not [string]::IsNullOrWhiteSpace($BlueprintPrincipalObjectId)
$hasFoundryAccount = -not [string]::IsNullOrWhiteSpace($FoundrySubscriptionId) -and
                     -not [string]::IsNullOrWhiteSpace($FoundryResourceGroup) -and
                     -not [string]::IsNullOrWhiteSpace($FoundryAccountName)

# Resolve manifest path.
$promptArgs = @()
if ($hasPromptTarget) {
    $manifestPath = $PromptManifest
    if (-not [IO.Path]::IsPathRooted($manifestPath)) {
        $manifestPath = Join-Path $repoRoot $manifestPath
    }
    $manifestPath = [IO.Path]::GetFullPath($manifestPath)
    $promptArgs = @("-f", $manifestPath)
}

$hostedArgs = @()
if ($hasHostedTarget) {
    $wsPath = $HostedWorkspace
    if (-not [IO.Path]::IsPathRooted($wsPath)) {
        $wsPath = Join-Path $repoRoot $wsPath
    }
    $wsPath = [IO.Path]::GetFullPath($wsPath)
    $hostedArgs = @("--workspace", $wsPath)
    if (-not [string]::IsNullOrWhiteSpace($HostedEnvironment)) {
        $hostedArgs += @("--environment", $HostedEnvironment)
    }
    if (-not [string]::IsNullOrWhiteSpace($HostedService)) {
        $hostedArgs += @("--service", $HostedService)
    }
}

# Step 1: Offline info.
Invoke-Step -Name "agent365-info-offline" -CommandPath "agent365 info"

# Step 2: Blueprint list (online).
Invoke-Step -Name "agent365-blueprint-list" -CommandPath "agent365 blueprint list" `
    -Arguments @("--all") `
    -RequiresOnline

# Step 3: Blueprint show (online, needs ID).
Invoke-Step -Name "agent365-blueprint-show" -CommandPath "agent365 blueprint show" `
    -Arguments $blueprintArgs `
    -RequiresOnline `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 4: Blueprint permissions (online, needs ID).
Invoke-Step -Name "agent365-blueprint-permissions" -CommandPath "agent365 blueprint permissions" `
    -Arguments $blueprintArgs `
    -RequiresOnline `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 5: Blueprint validate (online, needs ID).
Invoke-Step -Name "agent365-blueprint-validate" -CommandPath "agent365 blueprint validate" `
    -Arguments $blueprintArgs `
    -RequiresOnline `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 6: Blueprint owners.
Invoke-Step -Name "agent365-blueprint-owners" -CommandPath "agent365 blueprint owners" `
    -Arguments ($blueprintArgs + @("--all")) `
    -RequiresOnline `
    -RequiresOwners `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 7: Blueprint sponsors.
Invoke-Step -Name "agent365-blueprint-sponsors" -CommandPath "agent365 blueprint sponsors" `
    -Arguments ($blueprintArgs + @("--all")) `
    -RequiresOnline `
    -RequiresOwners `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 8: Blueprint-associated identity inventory.
Invoke-Step -Name "agent365-blueprint-identities" -CommandPath "agent365 blueprint identities" `
    -Arguments ($blueprintArgs + @("--all")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasBlueprintId) { "No blueprint app/object ID supplied" })

# Step 9: Tenant identity inventory with bounded pagination.
Invoke-Step -Name "agent365-identity-list" -CommandPath "agent365 identity list" `
    -Arguments @("--all") `
    -RequiresOnline

# Step 10: One Agent ID identity.
Invoke-Step -Name "agent365-identity-show" -CommandPath "agent365 identity show" `
    -Arguments @("--identity-object-id", $AgentIdentityObjectId) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasIdentity) { "No Agent ID identity object ID supplied" })

# Step 11: Blueprint principal inventory.
Invoke-Step -Name "agent365-blueprint-principal-list" -CommandPath "agent365 blueprint principal list" `
    -Arguments @("--all") `
    -RequiresOnline

# Step 12: One blueprint principal.
Invoke-Step -Name "agent365-blueprint-principal-show" -CommandPath "agent365 blueprint principal show" `
    -Arguments @("--principal-object-id", $BlueprintPrincipalObjectId) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasPrincipal) { "No blueprint principal object ID supplied" })

# Step 13: Binding status for Prompt target.
Invoke-Step -Name "agent365-binding-status-prompt" -CommandPath "agent365 binding status" `
    -Arguments ($blueprintArgs + $promptArgs + @("--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasPromptTarget) { "No Prompt manifest supplied" })

# Step 14: Binding plan for Prompt target.
Invoke-Step -Name "agent365-binding-plan-prompt" -CommandPath "agent365 binding plan" `
    -Arguments ($blueprintArgs + $promptArgs + @("--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasPromptTarget -or -not $hasBlueprintId) { "No Prompt manifest or blueprint ID supplied" })

# Step 15: Binding status for Hosted target.
Invoke-Step -Name "agent365-binding-status-hosted" -CommandPath "agent365 binding status" `
    -Arguments ($blueprintArgs + $hostedArgs + @("--accept-preview", "--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasHostedTarget) { "No Hosted workspace supplied" })

# Step 16: Binding plan for Hosted target.
Invoke-Step -Name "agent365-binding-plan-hosted" -CommandPath "agent365 binding plan" `
    -Arguments ($blueprintArgs + $hostedArgs + @("--accept-preview", "--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasHostedTarget -or -not $hasBlueprintId) { "No Hosted workspace or blueprint ID supplied" })

# Step 17: Account-level integration status.
$accountArgs = @()
if ($hasFoundryAccount) {
    $accountArgs = @(
        "--subscription-id", $FoundrySubscriptionId,
        "--resource-group", $FoundryResourceGroup,
        "--account-name", $FoundryAccountName
    )
}
Invoke-Step -Name "agent365-integration-status" -CommandPath "agent365 integration status" `
    -Arguments $accountArgs `
    -RequiresOnline `
    -SkipReason $(if (-not $hasFoundryAccount) { "No complete Foundry account coordinates supplied" })

# Step 18: Hosted observability status.
Invoke-Step -Name "agent365-observability-status" -CommandPath "agent365 observability status" `
    -Arguments ($hostedArgs + @("--accept-preview")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasHostedTarget) { "No Hosted workspace supplied" })

# Step 19: Prompt publication evidence.
Invoke-Step -Name "agent365-publication-status-prompt" -CommandPath "agent365 publication status" `
    -Arguments ($promptArgs + @("--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasPromptTarget) { "No Prompt manifest supplied" })

# Step 20: Hosted publication evidence.
Invoke-Step -Name "agent365-publication-status-hosted" -CommandPath "agent365 publication status" `
    -Arguments ($hostedArgs + @("--accept-preview", "--resolve-identity")) `
    -RequiresOnline `
    -SkipReason $(if (-not $hasHostedTarget) { "No Hosted workspace supplied" })

# ── Build report ────────────────────────────────────────────────────────────

$passedCount = @($script:steps | Where-Object { $_.status -eq "passed" }).Count
$skippedCount = @($script:steps | Where-Object { $_.status -eq "skipped" }).Count
$failedCount = @($script:steps | Where-Object { $_.status -eq "failed" }).Count

$report = [ordered]@{
    schemaVersion = 1
    generatedAt   = [DateTime]::UtcNow.ToString("o")
    binary        = $Binary
    runOnline     = [bool]$RunOnline
    enableOwners  = [bool]$EnableOwners
    summary       = [ordered]@{
        total   = $script:steps.Count
        passed  = $passedCount
        skipped = $skippedCount
        failed  = $failedCount
    }
    steps         = @($script:steps | ForEach-Object {
        $s = [ordered]@{
            name     = $_.name
            command  = $_.command
            status   = $_.status
            reason   = $_.reason
            exitCode = $_.exitCode
            duration = $_.duration
        }
        # Redact output in summary report — full JSON saved separately.
        $s
    })
}

# Write full step outputs (with redaction) to individual files.
foreach ($s in $script:steps) {
    if ($null -ne $s.output) {
        $stepFile = Join-Path $OutputDirectory "$($s.name).json"
        $rawJson = $s.output | ConvertTo-Json -Depth 20
        Invoke-Redact -Text $rawJson | Set-Content -Encoding utf8 -Path $stepFile
    }
}

$reportPath = Join-Path $OutputDirectory "agent365-acceptance-report.json"
$report | ConvertTo-Json -Depth 20 | Set-Content -Encoding utf8 -Path $reportPath

Write-Host "`n=== Agent 365 Acceptance Summary ===" -ForegroundColor Cyan
Write-Host "Passed:  $passedCount" -ForegroundColor Green
Write-Host "Skipped: $skippedCount" -ForegroundColor Yellow
Write-Host "Failed:  $failedCount" -ForegroundColor $(if ($failedCount -gt 0) { "Red" } else { "Green" })
Write-Host "Report:  $reportPath"

if ($failedCount -gt 0) {
    throw "Agent 365 acceptance failed with $failedCount failure(s). Report: $reportPath"
}
