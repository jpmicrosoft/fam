[CmdletBinding()]
param(
    [string]$OutputDirectory,
    [switch]$SkipCoreChecks,
    [switch]$SkipRace,
    [switch]$SkipCrossCompile
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
if ([string]::IsNullOrWhiteSpace($OutputDirectory)) {
    $timestamp = [DateTime]::UtcNow.ToString("yyyyMMddTHHmmssZ")
    $OutputDirectory = Join-Path (
        Join-Path $repoRoot ".release-qualification"
    ) $timestamp
}
elseif (-not [IO.Path]::IsPathRooted($OutputDirectory)) {
    $OutputDirectory = Join-Path $repoRoot $OutputDirectory
}
$OutputDirectory = [IO.Path]::GetFullPath($OutputDirectory)
New-Item -ItemType Directory -Force -Path $OutputDirectory | Out-Null

$steps = [Collections.Generic.List[object]]::new()
$artifacts = [Collections.Generic.List[object]]::new()
$failure = $null
$currentStep = $null
$failedStep = $null

function Add-StepResult {
    param(
        [string]$Name,
        [string]$Status,
        [datetime]$Started,
        [string]$Detail = ""
    )
    $steps.Add([ordered]@{
        name = $Name
        status = $Status
        durationSeconds = [Math]::Round(
            ([DateTime]::UtcNow - $Started).TotalSeconds,
            3
        )
        detail = $Detail
    })
}

function Invoke-GateStep {
    param(
        [string]$Name,
        [scriptblock]$Action
    )
    $script:currentStep = $Name
    $started = [DateTime]::UtcNow
    Write-Host "==> $Name"
    try {
        & $Action
        Add-StepResult -Name $Name -Status "passed" -Started $started
    }
    catch {
        $script:failedStep = $Name
        Add-StepResult -Name $Name -Status "failed" -Started $started -Detail $_.Exception.Message
        throw
    }
    finally {
        $script:currentStep = $null
    }
}

function Add-SkippedStep {
    param(
        [string]$Name,
        [string]$Reason
    )
    $steps.Add([ordered]@{
        name = $Name
        status = "skipped"
        durationSeconds = 0
        detail = $Reason
    })
}

function Invoke-CheckedNative {
    param(
        [string]$Command,
        [string[]]$Arguments,
        [string]$LogName
    )
    $logPath = Join-Path $OutputDirectory $LogName
    $oldEAP = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & $Command @Arguments 2>&1 | Tee-Object -FilePath $logPath
    }
    finally {
        $ErrorActionPreference = $oldEAP
    }
    if ($LASTEXITCODE -ne 0) {
        throw "$Command exited with code $LASTEXITCODE. See $logPath"
    }
}

function Invoke-ExpectedFailure {
    param(
        [string]$Command,
        [string[]]$Arguments,
        [int]$ExpectedExitCode,
        [string]$LogName
    )
    $logPath = Join-Path $OutputDirectory $LogName
    $oldEAP = $ErrorActionPreference
    try {
        $ErrorActionPreference = "Continue"
        & $Command @Arguments 2>&1 | Tee-Object -FilePath $logPath
    }
    finally {
        $ErrorActionPreference = $oldEAP
    }
    if ($LASTEXITCODE -ne $ExpectedExitCode) {
        throw "$Command exited with code $LASTEXITCODE; expected $ExpectedExitCode. See $logPath"
    }
}

function Save-StructuredProbe {
    param(
        [string]$Binary,
        [string[]]$Arguments,
        [string]$OutputPath
    )
    $content = & $Binary @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "$Binary $($Arguments -join ' ') failed with exit code $LASTEXITCODE`: $content"
    }
    if ([string]::IsNullOrWhiteSpace(($content -join "`n"))) {
        throw "$Binary $($Arguments -join ' ') returned empty output"
    }
    $content | Set-Content -Encoding utf8 -Path $OutputPath
}

Push-Location $repoRoot
try {
    $goVersion = (& go version).Trim()
    if ($LASTEXITCODE -ne 0) {
        throw "Go is required to run release qualification"
    }
    $goos = (& go env GOOS).Trim()
    $goarch = (& go env GOARCH).Trim()

    if (-not $SkipCoreChecks) {
        Invoke-GateStep "gofmt" {
            $unformatted = @(& gofmt -l . 2>&1)
            $unformatted | Set-Content -Encoding utf8 -Path (Join-Path $OutputDirectory "gofmt.log")
            if ($LASTEXITCODE -ne 0) {
                throw "gofmt failed"
            }
            if ($unformatted.Count -gt 0) {
                throw "gofmt reported unformatted files: $($unformatted -join ', ')"
            }
        }

        Invoke-GateStep "go vet" {
            Invoke-CheckedNative -Command "go" -Arguments @("vet", "./...") -LogName "go-vet.log"
        }

        Invoke-GateStep "go test" {
            Invoke-CheckedNative -Command "go" -Arguments @("test", "-count=1", "./...") -LogName "go-test.log"
        }
    }
    else {
        Add-SkippedStep "gofmt" "Skipped by -SkipCoreChecks"
        Add-SkippedStep "go vet" "Skipped by -SkipCoreChecks"
        Add-SkippedStep "go test" "Skipped by -SkipCoreChecks"
    }

    if ($SkipRace) {
        Add-SkippedStep "go test -race" "Skipped by -SkipRace"
    }
    elseif ("$goos/$goarch" -eq "windows/arm64") {
        Add-SkippedStep "go test -race" "The Go race detector is unavailable on windows/arm64"
    }
    else {
        Invoke-GateStep "go test -race" {
            Invoke-CheckedNative -Command "go" -Arguments @("test", "-count=1", "-race", "./...") -LogName "go-test-race.log"
        }
    }

    $hostBuildDirectory = Join-Path $OutputDirectory "host"
    New-Item -ItemType Directory -Force -Path $hostBuildDirectory | Out-Null
    $hostBinaryName = if ($goos -eq "windows") {
        "foundry-agent-manager.exe"
    }
    else {
        "foundry-agent-manager"
    }
    $hostBinary = Join-Path $hostBuildDirectory $hostBinaryName
    $hostAliasName = if ($goos -eq "windows") { "fam.exe" } else { "fam" }
    $hostAlias = Join-Path $hostBuildDirectory $hostAliasName
    Invoke-GateStep "host build" {
        Invoke-CheckedNative -Command "go" -Arguments @(
            "build",
            "-trimpath",
            "-o",
            $hostBinary,
            "./cmd"
        ) -LogName "go-build.log"
        Copy-Item -Path $hostBinary -Destination $hostAlias -Force
    }

    Invoke-GateStep "executable metadata" {
        Save-StructuredProbe -Binary $hostBinary -Arguments @("version") `
            -OutputPath (Join-Path $OutputDirectory "version.txt")
        Save-StructuredProbe -Binary $hostBinary -Arguments @("version", "--output", "json") `
            -OutputPath (Join-Path $OutputDirectory "version.json")
        Save-StructuredProbe -Binary $hostBinary -Arguments @("version", "--output", "yaml") `
            -OutputPath (Join-Path $OutputDirectory "version.yaml")
        Save-StructuredProbe -Binary $hostBinary -Arguments @("--help") `
            -OutputPath (Join-Path $OutputDirectory "root-help.txt")
        Save-StructuredProbe -Binary $hostAlias -Arguments @("-version") `
            -OutputPath (Join-Path $OutputDirectory "fam-version.txt")
        Save-StructuredProbe -Binary $hostAlias -Arguments @("--help") `
            -OutputPath (Join-Path $OutputDirectory "fam-root-help.txt")
    }

    Invoke-GateStep "shell completions" {
        $completionDirectory = Join-Path $OutputDirectory "completions"
        New-Item -ItemType Directory -Force -Path $completionDirectory | Out-Null
        foreach ($shell in @("bash", "zsh", "fish", "powershell")) {
            Save-StructuredProbe -Binary $hostBinary -Arguments @("completion", $shell) `
                -OutputPath (Join-Path $completionDirectory "foundry-agent-manager.$shell")
            Save-StructuredProbe -Binary $hostAlias -Arguments @("completion", $shell) `
                -OutputPath (Join-Path $completionDirectory "fam.$shell")
        }
    }

    Invoke-GateStep "installer scripts" {
        $powerShellInstaller = Join-Path $repoRoot "scripts\install.ps1"
        [void][scriptblock]::Create((Get-Content -Raw $powerShellInstaller))

        $shell = Get-Command sh -ErrorAction SilentlyContinue
        if ($null -ne $shell) {
            & $shell.Source -n (Join-Path $repoRoot "scripts\install.sh")
            if ($LASTEXITCODE -ne 0) {
                throw "install.sh failed the POSIX shell syntax check"
            }
        }
    }

    Invoke-GateStep "live release gate classification" {
        & (Join-Path $PSScriptRoot "Test-LiveReleaseGateClassification.ps1")
    }

    Invoke-GateStep "Agent 365 acceptance harness contract" {
        & (Join-Path $PSScriptRoot "Test-LiveAgent365Acceptance.ps1")
    }

    Invoke-GateStep "example manifests" {
        $exampleDirectory = Join-Path $OutputDirectory "examples"
        New-Item -ItemType Directory -Force -Path $exampleDirectory | Out-Null
        $examples = @(Get-ChildItem -Path (Join-Path $repoRoot "examples") -Filter "agent*.example.yaml" |
            Sort-Object Name)
        if ($examples.Count -eq 0) {
            throw "No shipped agent example manifests were found"
        }
        foreach ($example in $examples) {
            $baseName = [IO.Path]::GetFileNameWithoutExtension($example.Name)
            Save-StructuredProbe -Binary $hostBinary -Arguments @(
                "prompt",
                "validate",
                "--manifest",
                $example.FullName,
                "--output",
                "json"
            ) -OutputPath (Join-Path $exampleDirectory "$baseName.validate.json")
            Save-StructuredProbe -Binary $hostBinary -Arguments @(
                "prompt",
                "plan",
                "--manifest",
                $example.FullName,
                "--output",
                "json"
            ) -OutputPath (Join-Path $exampleDirectory "$baseName.plan.json")
        }
    }

    Invoke-GateStep "evaluator calibration contract" {
        $calibrationReport = Join-Path $OutputDirectory "evaluator-calibration-report.json"
        & (Join-Path $PSScriptRoot "Test-EvaluatorCalibration.ps1") `
            -Results @("qa\evaluator-calibration\results.example.jsonl") `
            -MinimumRuns 1 `
            -Output $calibrationReport
    }

    Invoke-GateStep "tool catalog" {
        Save-StructuredProbe -Binary $hostBinary -Arguments @(
            "tool-catalog",
            "--cloud",
            "AzureCloud",
            "--output",
            "json"
        ) -OutputPath (Join-Path $OutputDirectory "tool-catalog.AzureCloud.json")
    }

    Invoke-GateStep "negative executable probes" {
        Invoke-ExpectedFailure -Command $hostBinary -Arguments @(
            "does-not-exist",
            "--output",
            "json"
        ) -ExpectedExitCode 3 -LogName "negative-unknown-command.log"
        Invoke-ExpectedFailure -Command $hostBinary -Arguments @(
            "version",
            "--output",
            "xml"
        ) -ExpectedExitCode 3 -LogName "negative-output-format.log"
        Invoke-ExpectedFailure -Command $hostBinary -Arguments @(
            "version",
            "--request-timeout",
            "0",
            "--output",
            "json"
        ) -ExpectedExitCode 3 -LogName "negative-timeout.log"
    }

    Invoke-GateStep "git patch hygiene" {
        Invoke-CheckedNative -Command "git" -Arguments @("diff", "--check") -LogName "git-diff-check.log"
    }

    if ($SkipCrossCompile) {
        Add-SkippedStep "cross compile" "Skipped by -SkipCrossCompile"
    }
    else {
        Invoke-GateStep "cross compile" {
            $crossDirectory = Join-Path $OutputDirectory "cross"
            New-Item -ItemType Directory -Force -Path $crossDirectory | Out-Null
            $targets = @(
                @("linux", "amd64"),
                @("linux", "arm64"),
                @("darwin", "amd64"),
                @("darwin", "arm64"),
                @("windows", "amd64"),
                @("windows", "arm64")
            )
            $oldGOOS = $env:GOOS
            $oldGOARCH = $env:GOARCH
            $oldCGO = $env:CGO_ENABLED
            try {
                $env:CGO_ENABLED = "0"
                foreach ($target in $targets) {
                    $env:GOOS = $target[0]
                    $env:GOARCH = $target[1]
                    $extension = if ($env:GOOS -eq "windows") { ".exe" } else { "" }
                    $name = "foundry-agent-manager_$($env:GOOS)_$($env:GOARCH)$extension"
                    $output = Join-Path $crossDirectory $name
                    & go build -trimpath -o $output ./cmd
                    if ($LASTEXITCODE -ne 0) {
                        throw "Cross-compilation failed for $($env:GOOS)/$($env:GOARCH)"
                    }
                    Copy-Item -Path $output -Destination (
                        Join-Path $crossDirectory "fam_$($env:GOOS)_$($env:GOARCH)$extension"
                    ) -Force
                }
            }
            finally {
                $env:GOOS = $oldGOOS
                $env:GOARCH = $oldGOARCH
                $env:CGO_ENABLED = $oldCGO
            }
        }
    }

    Invoke-GateStep "artifact checksums" {
        $files = @(Get-ChildItem -Path $OutputDirectory -File -Recurse |
            Where-Object { $_.Name -ne "release-report.json" -and $_.Name -ne "SHA256SUMS" } |
            Sort-Object FullName)
        $checksumLines = foreach ($file in $files) {
            $hash = Get-FileHash -Algorithm SHA256 -Path $file.FullName
            $relative = (Get-RelativePath $OutputDirectory $file.FullName).Replace("\", "/")
            $artifacts.Add([ordered]@{
                path = $relative
                sha256 = $hash.Hash.ToLowerInvariant()
                bytes = $file.Length
            })
            "$($hash.Hash.ToLowerInvariant())  $relative"
        }
        $checksumLines | Set-Content -Encoding ascii -Path (Join-Path $OutputDirectory "SHA256SUMS")
    }
}
catch {
    $failure = $_
}
finally {
    Pop-Location
    $report = [ordered]@{
        schemaVersion = 1
        generatedAt = [DateTime]::UtcNow.ToString("o")
        passed = $null -eq $failure
        failedStep = $failedStep
        failure = if ($null -eq $failure) { "" } else { $failure.Exception.Message }
        goVersion = if (Get-Variable -Name goVersion -ErrorAction SilentlyContinue) { $goVersion } else { "" }
        platform = if (
            (Get-Variable -Name goos -ErrorAction SilentlyContinue) -and
            (Get-Variable -Name goarch -ErrorAction SilentlyContinue)
        ) {
            "$goos/$goarch"
        }
        else {
            ""
        }
        steps = $steps
        artifacts = $artifacts
    }
    $report | ConvertTo-Json -Depth 8 |
        Set-Content -Encoding utf8 -Path (Join-Path $OutputDirectory "release-report.json")
}

if ($null -ne $failure) {
    throw "Release qualification failed: $($failure.Exception.Message)"
}

Write-Host "Release qualification passed. Report: $(Join-Path $OutputDirectory 'release-report.json')"
