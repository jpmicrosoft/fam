<#
.SYNOPSIS
    Generates THIRD_PARTY_NOTICES.txt from Go module dependencies of ./cmd.

.DESCRIPTION
    Determines runtime (non-standard-library) Go module dependencies for ./cmd,
    locates their LICENSE/LICENCE/COPYING/NOTICE files in the module cache, and
    emits a deterministic THIRD_PARTY_NOTICES.txt. Fails if any runtime dependency
    is missing licensing material.

.PARAMETER OutputPath
    Path to write the generated notices file. Defaults to THIRD_PARTY_NOTICES.txt
    in the repository root.

.PARAMETER ModulePath
    Relative path to the Go module containing the binary entry point. Defaults to ./cmd.

.PARAMETER SourceRoot
    Absolute path to the Go module root to operate against. Defaults to the
    repository root (parent of the directory containing this script). Use this
    when running current tooling against a separately checked-out tagged tree.
#>
[CmdletBinding()]
param(
    [string]$OutputPath,
    [string]$ModulePath = "./cmd",
    [string]$SourceRoot
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($SourceRoot)) {
    $repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
}
else {
    if (-not [IO.Path]::IsPathRooted($SourceRoot)) {
        throw "-SourceRoot must be an absolute path, got: $SourceRoot"
    }
    if (-not (Test-Path $SourceRoot)) {
        throw "-SourceRoot path does not exist: $SourceRoot"
    }
    $repoRoot = $SourceRoot
}

if ([string]::IsNullOrWhiteSpace($OutputPath)) {
    $OutputPath = Join-Path $repoRoot "THIRD_PARTY_NOTICES.txt"
}
elseif (-not [IO.Path]::IsPathRooted($OutputPath)) {
    $OutputPath = Join-Path $repoRoot $OutputPath
}

Push-Location $repoRoot
try {
    # Get the list of runtime module dependencies (excluding the main module and stdlib).
    $raw = & go list -m -json all 2>&1
    if ($LASTEXITCODE -ne 0) { throw "go list -m -json all failed: $raw" }

    # Parse JSON objects (go list emits concatenated JSON objects, not an array).
    $jsonText = "[$($raw -join "`n" -replace '}\s*{', '},{')]"
    $allModules = $jsonText | ConvertFrom-Json

    # Get modules actually imported by ./cmd at runtime.
    $depsRaw = & go list -deps -f '{{if not .Standard}}{{.Module.Path}}@{{.Module.Version}}{{end}}' $ModulePath 2>&1
    if ($LASTEXITCODE -ne 0) { throw "go list -deps failed: $depsRaw" }

    $runtimeDeps = @($depsRaw | Where-Object { $_ -match '@' -and $_ -notmatch '^foundry-agent-manager@' } | Sort-Object -Unique)

    if ($runtimeDeps.Count -eq 0) {
        throw "No runtime dependencies found for $ModulePath — unexpected for this project."
    }

    # Build a lookup of module path+version to directory.
    $gopath = (& go env GOPATH).Trim()
    $modCache = Join-Path $gopath "pkg" "mod"

    # Ensure modules are downloaded.
    & go mod download 2>&1 | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "go mod download failed" }

    $licenseFileNames = @("LICENSE", "LICENCE", "COPYING", "LICENSE.txt", "LICENCE.txt", "LICENSE.md", "LICENCE.md", "COPYING.txt")
    $noticeFileNames = @("NOTICE", "NOTICE.txt", "NOTICE.md")

    $entries = [System.Collections.Generic.List[string]]::new()
    $missing = [System.Collections.Generic.List[string]]::new()

    foreach ($dep in $runtimeDeps) {
        $parts = $dep -split '@', 2
        $modPath = $parts[0]
        $modVersion = $parts[1]

        # Module cache directory uses escaped uppercase as !lowercase.
        $escapedPath = ($modPath.ToCharArray() | ForEach-Object {
            if ([char]::IsUpper($_)) { "!$([char]::ToLower($_))" } else { "$_" }
        }) -join ''
        $modDir = Join-Path $modCache "$escapedPath@$modVersion"

        if (-not (Test-Path $modDir)) {
            # Try to find via replace directives — fall back to checking allModules
            $missing.Add($dep)
            continue
        }

        # Find license file.
        $licenseFile = $null
        foreach ($name in $licenseFileNames) {
            $candidate = Join-Path $modDir $name
            if (Test-Path $candidate) {
                $licenseFile = $candidate
                break
            }
        }

        if (-not $licenseFile) {
            # Walk up parent module directories (for nested modules like azure-sdk-for-go/sdk/azcore).
            $parentPath = $modPath
            $found = $false
            while ($parentPath -match '/') {
                $parentPath = $parentPath -replace '/[^/]+$', ''
                # Check if parent module exists in cache
                $parentMod = $allModules | Where-Object { $_.Path -eq $parentPath } | Select-Object -First 1
                if ($parentMod -and $parentMod.Version) {
                    $escapedParent = ($parentPath.ToCharArray() | ForEach-Object {
                        if ([char]::IsUpper($_)) { "!$([char]::ToLower($_))" } else { "$_" }
                    }) -join ''
                    $parentDir = Join-Path $modCache "$escapedParent@$($parentMod.Version)"
                    if (Test-Path $parentDir) {
                        foreach ($name in $licenseFileNames) {
                            $candidate = Join-Path $parentDir $name
                            if (Test-Path $candidate) {
                                $licenseFile = $candidate
                                $found = $true
                                break
                            }
                        }
                    }
                }
                if ($found) { break }
            }
        }

        if (-not $licenseFile) {
            $missing.Add($dep)
            continue
        }

        $licenseText = (Get-Content -Raw -Path $licenseFile).TrimEnd()

        # Find optional NOTICE file.
        $noticeText = $null
        foreach ($name in $noticeFileNames) {
            $candidate = Join-Path $modDir $name
            if (Test-Path $candidate) {
                $noticeText = (Get-Content -Raw -Path $candidate).TrimEnd()
                break
            }
        }

        $entry = "===============================================================================`n"
        $entry += "Module:  $modPath`nVersion: $modVersion`n"
        $entry += "===============================================================================`n`n"
        $entry += $licenseText
        if ($noticeText) {
            $entry += "`n`n--- NOTICE ---`n`n$noticeText"
        }
        $entry += "`n"

        $entries.Add($entry)
    }

    if ($missing.Count -gt 0) {
        $missingList = $missing -join "`n  "
        throw "The following runtime dependencies are missing license files:`n  $missingList`n`nCannot generate third-party notices without complete licensing material."
    }

    # Emit deterministic output (entries already sorted since $runtimeDeps is sorted).
    $header = @"
THIRD-PARTY SOFTWARE NOTICES AND INFORMATION

This project incorporates material from the third-party projects listed below.
The original copyright notices and the licenses under which this project
received such material are set forth below.

"@

    $content = $header + "`n" + ($entries -join "`n")
    # Normalize line endings to LF for cross-platform determinism.
    $content = $content -replace "`r`n", "`n"
    [System.IO.File]::WriteAllText($OutputPath, $content, [System.Text.UTF8Encoding]::new($false))
    Write-Host "Generated $OutputPath with $($entries.Count) module notices."
}
finally {
    Pop-Location
}
