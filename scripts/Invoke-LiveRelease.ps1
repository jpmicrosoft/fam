[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$Config,
    [string]$Binary,
    [string]$OutputDirectory,
    [switch]$RunOnline,
    [switch]$AllowMutations,
    [switch]$AllowDestructive,
    [switch]$RequireAllCommands,
    [switch]$RequireAllFlags
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($AllowDestructive -and (-not $AllowMutations -or -not $RunOnline)) {
    throw "-AllowDestructive requires both -RunOnline and -AllowMutations"
}
if ($AllowMutations -and -not $RunOnline) {
    throw "-AllowMutations requires -RunOnline"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$configPath = (Resolve-Path $Config).Path
$matrix = Get-Content -Raw -Path $configPath | ConvertFrom-Json -Depth 20
if ($matrix.schemaVersion -ne 1) {
    throw "Live release matrix schemaVersion must be 1"
}
if ($null -eq $matrix.scenarios -or @($matrix.scenarios).Count -eq 0) {
    throw "Live release matrix must contain at least one scenario"
}

if ([string]::IsNullOrWhiteSpace($Binary)) {
    $hostOS = (& go env GOOS).Trim()
    $binaryName = if ($hostOS -eq "windows") {
        "foundry-agent-manager.exe"
    }
    else {
        "foundry-agent-manager"
    }
    $Binary = Join-Path (Join-Path $repoRoot "bin") $binaryName
}
elseif (-not [IO.Path]::IsPathRooted($Binary)) {
    $Binary = Join-Path $repoRoot $Binary
}
$Binary = [IO.Path]::GetFullPath($Binary)

if (-not (Test-Path -LiteralPath $Binary -PathType Leaf)) {
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $Binary) | Out-Null
    Push-Location $repoRoot
    try {
        & go build -trimpath -o $Binary ./cmd
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to build $Binary"
        }
    }
    finally {
        Pop-Location
    }
}

if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $OutputDirectory = Join-Path (
        Join-Path $repoRoot ".release-qualification"
    ) "live-$timestamp"
}
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

function Get-CompletionChildren {
    param(
        [string]$Executable,
        [string[]]$CommandPath
    )
    $completionArguments = @("__complete") + @($CommandPath) + @("")
    $completion = & $Executable @completionArguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect command children for '$($CommandPath -join ' ')'"
    }

    $children = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::Ordinal
    )
    foreach ($line in $completion) {
        $text = ([string]$line).TrimEnd("`r")
        if ([string]::IsNullOrWhiteSpace($text) -or $text.StartsWith(":")) {
            continue
        }
        $name = ($text -split "`t", 2)[0]
        if ($name -match "^[a-z0-9][a-z0-9-]*$") {
            [void]$children.Add($name)
        }
    }
    return @($children | Sort-Object)
}

function Add-CommandPaths {
    param(
        [string]$Executable,
        [string[]]$ParentPath,
        [Collections.Generic.List[string]]$Paths
    )
    foreach ($child in @(Get-CompletionChildren -Executable $Executable -CommandPath $ParentPath)) {
        if ($ParentPath.Count -eq 0 -and $child -in @("completion", "help")) {
            continue
        }
        $path = @($ParentPath) + @($child)
        $grandchildren = @(Get-CompletionChildren -Executable $Executable -CommandPath $path)
        if ($grandchildren.Count -eq 0) {
            $Paths.Add(($path -join " "))
            continue
        }
        Add-CommandPaths -Executable $Executable -ParentPath $path -Paths $Paths
    }
}

function Get-CommandPaths {
    param([string]$Executable)
    $paths = [Collections.Generic.List[string]]::new()
    Add-CommandPaths -Executable $Executable -ParentPath @() -Paths $paths
    if ($paths.Count -eq 0) {
        throw "No commands were discovered from dynamic completion"
    }
    return @($paths | Sort-Object)
}

function Get-CommandFlags {
    param(
        [string]$Executable,
        [string]$CommandPath
    )
    $pathArguments = @($CommandPath -split " ")
    $help = & $Executable @pathArguments --help 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Could not inspect flags for $CommandPath"
    }
    $flags = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::Ordinal
    )
    foreach ($line in $help) {
        foreach ($match in [regex]::Matches($line, "--([a-z0-9][a-z0-9-]*)")) {
            $name = $match.Groups[1].Value
            if ($name -ne "help") {
                [void]$flags.Add($name)
            }
        }
    }
    return @($flags | Sort-Object)
}

function Get-CoveredFlags {
    param([object[]]$Arguments)
    $flags = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::Ordinal
    )
    foreach ($argument in $Arguments) {
        $text = [string]$argument
        if ($text -match "^--([a-z0-9][a-z0-9-]*)(=.*)?$") {
            [void]$flags.Add($Matches[1])
        }
    }
    return @($flags)
}

# Cobra/pflag-compatible boolean values (case-insensitive).
$script:cobraTrueValues = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase
)
foreach ($v in @("1", "t", "true", "yes", "y")) {
    [void]$script:cobraTrueValues.Add($v)
}
$script:cobraFalseValues = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::OrdinalIgnoreCase
)
foreach ($v in @("0", "f", "false", "no", "n")) {
    [void]$script:cobraFalseValues.Add($v)
}

<#
.SYNOPSIS
  Resolves the effective boolean value of a security-sensitive flag from an
  argument list, failing closed on ambiguity, duplicates, or conflicts.

.DESCRIPTION
  Supports Cobra/pflag accepted forms:
    --flag             (implies true)
    --flag=<value>     (inline value)
  Does NOT consume a separated positional value (--flag <value>) because
  boolean flags in Cobra do not consume a following positional by default;
  however, scenarios that place a boolean-shaped token immediately after a
  bare --flag are rejected as ambiguous to prevent misclassification.

  Duplicate or conflicting occurrences are rejected (fail-closed).

.PARAMETER FlagName
  The long flag name without leading dashes (e.g. "dry-run").

.PARAMETER Arguments
  The full argument list for the scenario command.

.OUTPUTS
  [System.Nullable[bool]] -- $true, $false, or $null if the flag is absent.
  Throws on duplicate, conflicting, or unrecognized boolean spelling.
#>
function Resolve-SecurityBoolFlag {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory)]
        [string]$FlagName,
        [object[]]$Arguments
    )
    $prefix = "--$FlagName"
    $occurrences = [Collections.Generic.List[bool]]::new()

    for ($i = 0; $i -lt $Arguments.Count; $i++) {
        $arg = [string]$Arguments[$i]

        if ($arg -ceq $prefix) {
            # Bare --flag implies true in Cobra for bool flags.
            # Check if the next token looks like a boolean value -- if so, reject
            # as ambiguous rather than guessing whether Cobra would consume it.
            if ($i + 1 -lt $Arguments.Count) {
                $next = [string]$Arguments[$i + 1]
                if ($script:cobraTrueValues.Contains($next) -or
                    $script:cobraFalseValues.Contains($next)) {
                    throw "Ambiguous security-sensitive flag: '$prefix $next' -- use '$prefix=$next' for clarity"
                }
            }
            $occurrences.Add($true)
            continue
        }

        if ($arg.StartsWith("$prefix=", [StringComparison]::Ordinal)) {
            $raw = $arg.Substring($prefix.Length + 1)
            if ($script:cobraTrueValues.Contains($raw)) {
                $occurrences.Add($true)
            }
            elseif ($script:cobraFalseValues.Contains($raw)) {
                $occurrences.Add($false)
            }
            else {
                throw "Unrecognized boolean value for security-sensitive flag '$prefix=$raw'"
            }
            continue
        }
    }

    if ($occurrences.Count -eq 0) {
        return $null
    }
    if ($occurrences.Count -gt 1) {
        $distinct = @($occurrences | Sort-Object -Unique)
        if ($distinct.Count -gt 1) {
            throw "Conflicting duplicate security-sensitive flag '$prefix' with both true and false values"
        }
        throw "Duplicate security-sensitive flag '$prefix' -- specify it exactly once"
    }
    return $occurrences[0]
}

function Test-GateAuthorized {
    param([string]$Gate)
    switch ($Gate) {
        "offline" { return $true }
        "online-read" { return [bool]$RunOnline }
        "mutation" { return [bool]($RunOnline -and $AllowMutations) }
        "destructive" {
            return [bool]($RunOnline -and $AllowMutations -and $AllowDestructive)
        }
        default { throw "Unknown live scenario gate $Gate" }
    }
}

$offlineCommands = @(
    "agent365 info",
    "agent365 observability plan",
    "agent365 publication info",
    "autopilot info",
    "prompt compatibility",
    "quickstart",
    "grounding plan",
    "grounding validate",
    "hosted adopt",
    "hosted info",
    "hosted init",
    "hosted plan",
    "hosted validate",
    "prompt init",
    "memory store plan",
    "memory store validate",
    "prompt plan",
    "tool-catalog",
    "toolbox plan",
    "toolbox validate",
    "prompt validate",
    "version"
)
$mutationCommands = @(
    "agent365 integration set",
    "autopilot deploy",
    "project connection create",
    "project connection delete",
    "project connection update",
    "connector configure",
    "connector create",
    "connector delete",
    "connector toolbox deploy",
    "prompt decommission",
    "prompt delete",
    "prompt versions delete",
    "prompt deploy",
    "prompt disable",
    "prompt enable",
    "prompt endpoint configure",
    "grounding file delete",
    "grounding store delete",
    "grounding sync",
    "hosted delete",
    "hosted environment create",
    "hosted versions delete",
    "hosted deploy",
    "hosted disable",
    "hosted draft deploy",
    "hosted enable",
    "hosted promote",
    "hosted versions prune",
    "hosted rollback",
    "hosted session create",
    "hosted session delete",
    "hosted session file delete",
    "hosted session file upload",
    "hosted session stop",
    "prompt legacy delete",
    "prompt legacy deploy",
    "memory item create",
    "memory item delete",
    "memory item update",
    "memory scope delete",
    "memory store delete",
    "memory store sync",
    "memory update",
    "model deployment create",
    "project create",
    "prompt promote",
    "prompt versions prune",
    "prompt m365 publish",
    "prompt rollback",
    "receipt upload",
    "skill create",
    "skill delete",
    "skill version set-default",
    "skill version delete",
    "toolbox versions delete",
    "toolbox deploy",
    "toolbox promote"
)
$destructiveCommands = @(
    "project connection delete",
    "connector delete",
    "prompt decommission",
    "prompt delete",
    "prompt versions delete",
    "grounding file delete",
    "grounding store delete",
    "hosted delete",
    "hosted versions delete",
    "hosted versions prune",
    "hosted session delete",
    "hosted session file delete",
    "prompt legacy delete",
    "memory item delete",
    "memory scope delete",
    "memory store delete",
    "model deployment delete",
    "prompt versions prune",
    "skill delete",
    "skill version delete",
    "toolbox versions delete"
)
$gateRank = @{
    "offline" = 0
    "online-read" = 1
    "mutation" = 2
    "destructive" = 3
}

function Get-MinimumGate {
    param(
        [string]$CommandName,
        [object[]]$Arguments
    )
    # Resolve security-sensitive boolean flags with fail-closed semantics.
    # Throws on duplicates, conflicts, or unrecognized boolean spellings.
    $dryRunValue = Resolve-SecurityBoolFlag -FlagName "dry-run" -Arguments $Arguments
    $pruneValue = Resolve-SecurityBoolFlag -FlagName "prune" -Arguments $Arguments
    $deleteReplacedValue = Resolve-SecurityBoolFlag -FlagName "delete-replaced-uploads" -Arguments $Arguments
    $deletePrunedValue = Resolve-SecurityBoolFlag -FlagName "delete-pruned-uploads" -Arguments $Arguments
    $bootstrapEnvironmentValue = Resolve-SecurityBoolFlag -FlagName "bootstrap-environment" -Arguments $Arguments

    $dryRun = $dryRunValue -eq $true

    if (@("quickstart", "hosted adopt") -contains $CommandName -and
        $bootstrapEnvironmentValue -eq $true) {
        return "mutation"
    }

    # Grounding sync with --prune, --delete-replaced-uploads, or
    # --delete-pruned-uploads enables broad deletion and requires destructive.
    if ($CommandName -eq "grounding sync" -and -not $dryRun) {
        if ($pruneValue -eq $true -or
            $deleteReplacedValue -eq $true -or
            $deletePrunedValue -eq $true) {
            return "destructive"
        }
    }

    if ($destructiveCommands -contains $CommandName -and -not $dryRun) {
        return "destructive"
    }
    if ($mutationCommands -contains $CommandName -and -not $dryRun) {
        return "mutation"
    }
    if ($offlineCommands -contains $CommandName) {
        return "offline"
    }
    return "online-read"
}

$disallowedLiteralSecretFlags = @(
    "--apim-subscription-key"
)
$discoveredCommands = @(Get-CommandPaths -Executable $Binary)
$discoveredSet = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::Ordinal
)
foreach ($commandName in $discoveredCommands) {
    [void]$discoveredSet.Add($commandName)
}
$coveredCommands = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::Ordinal
)
$excludedCommands = [Collections.Generic.HashSet[string]]::new(
    [StringComparer]::Ordinal
)
$scenarioFlags = @{}
$results = [Collections.Generic.List[object]]::new()
$failed = $false

$exclusionsProperty = $matrix.PSObject.Properties["exclusions"]
$exclusions = if ($null -eq $exclusionsProperty) {
    @()
}
else {
    @($exclusionsProperty.Value)
}
foreach ($exclusion in $exclusions) {
    $commandName = [string]$exclusion.command
    $reason = [string]$exclusion.reason
    if (-not $discoveredSet.Contains($commandName)) {
        throw "Exclusion references unknown command $commandName"
    }
    if ([string]::IsNullOrWhiteSpace($reason)) {
        throw "Exclusion for $commandName requires a reason"
    }
    [void]$excludedCommands.Add($commandName)
}

Push-Location $repoRoot
try {
    $index = 0
    foreach ($scenario in @($matrix.scenarios)) {
        $index++
        $name = [string]$scenario.name
        $commandName = [string]$scenario.command
        $gate = [string]$scenario.gate
        $enabledProperty = $scenario.PSObject.Properties["enabled"]
        $enabled = if ($null -eq $enabledProperty) {
            $true
        }
        else {
            [bool]$enabledProperty.Value
        }
        $requiredProperty = $scenario.PSObject.Properties["required"]
        $required = if ($null -eq $requiredProperty) {
            $true
        }
        else {
            [bool]$requiredProperty.Value
        }
        $arguments = @($scenario.arguments | ForEach-Object { [string]$_ })
        $expectedExitCodes = @($scenario.expectedExitCodes | ForEach-Object { [int]$_ })
        if ($expectedExitCodes.Count -eq 0) {
            $expectedExitCodes = @(0)
        }
        if ([string]::IsNullOrWhiteSpace($name)) {
            throw "Scenario $index requires a name"
        }
        if ($commandName -notmatch "^[a-z0-9][a-z0-9-]*( [a-z0-9][a-z0-9-]*)*$") {
            throw "Scenario $name contains an invalid command path"
        }
        if (-not $discoveredSet.Contains($commandName)) {
            throw "Scenario $name references unknown command $commandName"
        }
        if (-not $gateRank.ContainsKey($gate)) {
            throw "Scenario $name uses unknown gate $gate"
        }
        if (-not $enabled) {
            $results.Add([ordered]@{
                name = $name
                command = $commandName
                gate = $gate
                required = $required
                status = "skipped"
                exitCode = $null
                durationSeconds = 0
                log = ""
                detail = "Scenario is disabled in the matrix"
            })
            continue
        }
        if ($excludedCommands.Contains($commandName)) {
            throw "Command $commandName cannot be both excluded and covered"
        }
        $minimumGate = Get-MinimumGate -CommandName $commandName -Arguments $arguments
        if ($gateRank[$gate] -lt $gateRank[$minimumGate]) {
            throw "Scenario $name declares gate $gate, but $commandName requires at least $minimumGate"
        }
        foreach ($argument in $arguments) {
            if ($argument.Contains("`0") -or $argument.Contains("`r") -or $argument.Contains("`n")) {
                throw "Scenario $name contains an invalid argument"
            }
        }
        foreach ($secretFlag in $disallowedLiteralSecretFlags) {
            if ($arguments -contains $secretFlag -or
                @($arguments | Where-Object { $_.StartsWith("$secretFlag=", [StringComparison]::Ordinal) }).Count -gt 0) {
                throw "Scenario $name uses $secretFlag directly; use a file, environment, stdin, or Key Vault source"
            }
        }

        if (-not (Test-GateAuthorized -Gate $gate)) {
            $results.Add([ordered]@{
                name = $name
                command = $commandName
                gate = $gate
                required = $required
                status = if ($required) { "failed" } else { "skipped" }
                exitCode = $null
                durationSeconds = 0
                log = ""
                detail = "The required execution gate was not enabled"
            })
            if ($required) {
                $failed = $true
            }
            continue
        }

        $safeName = ($name -replace "[^A-Za-z0-9._-]", "-").Trim("-")
        if ([string]::IsNullOrWhiteSpace($safeName)) {
            $safeName = "scenario-$index"
        }
        $logPath = Join-Path $OutputDirectory ("{0:D3}-{1}.log" -f $index, $safeName)
        $started = [DateTime]::UtcNow
        Write-Host "==> [$gate] $name"
        $commandPath = @($commandName -split " ")
        & $Binary @commandPath @arguments 2>&1 | Tee-Object -FilePath $logPath
        $exitCode = $LASTEXITCODE
        $passed = $expectedExitCodes -contains $exitCode
        $results.Add([ordered]@{
            name = $name
            command = $commandName
            gate = $gate
            required = $required
            status = if ($passed) { "passed" } else { "failed" }
            exitCode = $exitCode
            durationSeconds = [Math]::Round(
                ([DateTime]::UtcNow - $started).TotalSeconds,
                3
            )
            log = [IO.Path]::GetRelativePath($OutputDirectory, $logPath).Replace("\", "/")
            detail = if ($passed) {
                ""
            }
            else {
                "Expected exit code(s): $($expectedExitCodes -join ', ')"
            }
        })
        if ($passed) {
            [void]$coveredCommands.Add($commandName)
            if (-not $scenarioFlags.ContainsKey($commandName)) {
                $scenarioFlags[$commandName] = [Collections.Generic.HashSet[string]]::new(
                    [StringComparer]::Ordinal
                )
            }
            foreach ($flag in Get-CoveredFlags -Arguments $arguments) {
                [void]$scenarioFlags[$commandName].Add($flag)
            }
        }
        else {
            $failed = $true
        }
    }
}
finally {
    Pop-Location
}

$commandCoverage = [Collections.Generic.List[object]]::new()
$missingCommands = [Collections.Generic.List[string]]::new()
$missingFlagCount = 0
foreach ($commandName in $discoveredCommands) {
    $flags = @(Get-CommandFlags -Executable $Binary -CommandPath $commandName)
    $covered = if ($scenarioFlags.ContainsKey($commandName)) {
        @($scenarioFlags[$commandName] | Sort-Object)
    }
    else {
        @()
    }
    $missingFlags = @($flags | Where-Object { $_ -notin $covered })
    $excluded = $excludedCommands.Contains($commandName)
    if (-not $coveredCommands.Contains($commandName) -and -not $excluded) {
        $missingCommands.Add($commandName)
    }
    if (-not $excluded) {
        $missingFlagCount += $missingFlags.Count
    }
    $commandCoverage.Add([ordered]@{
        command = $commandName
        excluded = $excluded
        covered = $coveredCommands.Contains($commandName)
        flags = $flags
        coveredFlags = $covered
        missingFlags = if ($excluded) { @() } else { $missingFlags }
    })
}

if ($RequireAllCommands -and $missingCommands.Count -gt 0) {
    $failed = $true
}
if ($RequireAllFlags -and $missingFlagCount -gt 0) {
    $failed = $true
}

$report = [ordered]@{
    schemaVersion = 1
    generatedAt = [DateTime]::UtcNow.ToString("o")
    passed = -not $failed
    gates = [ordered]@{
        online = [bool]$RunOnline
        mutations = [bool]$AllowMutations
        destructive = [bool]$AllowDestructive
    }
    requirements = [ordered]@{
        allCommands = [bool]$RequireAllCommands
        allFlags = [bool]$RequireAllFlags
    }
    missingCommands = $missingCommands
    missingFlagCount = $missingFlagCount
    commandCoverage = $commandCoverage
    scenarios = $results
}
$reportPath = Join-Path $OutputDirectory "live-release-report.json"
$report | ConvertTo-Json -Depth 10 | Set-Content -Encoding utf8 -Path $reportPath

if ($failed) {
    throw "Live release qualification failed. Report: $reportPath"
}
Write-Host "Live release qualification passed. Report: $reportPath"
