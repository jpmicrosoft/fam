<#
.SYNOPSIS
    Verified installer for foundry-agent-manager (PowerShell, Windows/macOS/Linux).

.DESCRIPTION
    Downloads a prebuilt release archive from GitHub, verifies the SHA256
    checksum, and installs the foundry-agent-manager binary plus its fam
    shorthand into a configurable directory. The installer does not compile
    source code, and Go is not required to install or run the downloaded CLI.
    Private repositories can use FAM_INSTALL_TOKEN, GITHUB_TOKEN, GH_TOKEN, or
    an authenticated gh CLI.

.PARAMETER Version
    Specific published version tag to install (e.g. v0.14.1). Omit for latest release.

.PARAMETER InstallDir
    Destination directory. Defaults to $env:LOCALAPPDATA\foundry-agent-manager
    on Windows or $HOME/.local/bin on POSIX.

.PARAMETER Repo
    GitHub repository in OWNER/REPO format.
    Default: jpmicrosoft/fam

.PARAMETER ModifyProfile
    If set, appends the install directory to the PATH in the user's shell profile.
    Without this switch, the installer never modifies profiles.

.EXAMPLE
    ./scripts/install.ps1 -Version v0.14.1
    ./scripts/install.ps1 -InstallDir C:\tools
    ./scripts/install.ps1 -Repo myorg/my-repo -ModifyProfile
#>
[CmdletBinding()]
param(
    [string]$Version,
    [string]$InstallDir,
    [string]$Repo = "jpmicrosoft/fam",
    [switch]$ModifyProfile
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

# --- Detect platform and architecture ---
function Get-Platform {
    $runtime = [System.Runtime.InteropServices.RuntimeInformation]
    $platform = [System.Runtime.InteropServices.OSPlatform]
    if ($runtime::IsOSPlatform($platform::Windows)) { return "windows" }
    if ($runtime::IsOSPlatform($platform::OSX)) { return "darwin" }
    if ($runtime::IsOSPlatform($platform::Linux)) { return "linux" }
    throw "Unsupported operating system."
}

function Get-Architecture {
    $arch = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLower()
    switch ($arch) {
        "x64"   { return "amd64" }
        "arm64" { return "arm64" }
        default { throw "Unsupported architecture: $arch" }
    }
}

$platform = Get-Platform
$architecture = Get-Architecture

if ([string]::IsNullOrWhiteSpace($InstallDir)) {
    if ($platform -eq "windows") {
        $InstallDir = Join-Path $env:LOCALAPPDATA "foundry-agent-manager"
    } else {
        $InstallDir = Join-Path $HOME ".local/bin"
    }
}
if ($Repo -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
    throw "-Repo must use OWNER/REPO format."
}

# --- Resolve authentication ---
$token = if ($env:FAM_INSTALL_TOKEN) { $env:FAM_INSTALL_TOKEN }
         elseif ($env:GITHUB_TOKEN) { $env:GITHUB_TOKEN }
         elseif ($env:GH_TOKEN) { $env:GH_TOKEN }
         elseif (Get-Command gh -ErrorAction SilentlyContinue) {
             try { (& gh auth token 2>$null) } catch { $null }
         }
         else { $null }

$authHeaders = @{}
if ($token) {
    $authHeaders["Authorization"] = "token $token"
}

# --- Resolve version ---
if ([string]::IsNullOrWhiteSpace($Version) -or $Version -eq "latest") {
    Write-Host "Resolving latest release..."
    $releaseUrl = "https://api.github.com/repos/$Repo/releases/latest"
    $headers = @{ "Accept" = "application/vnd.github+json" } + $authHeaders
    try {
        $release = Invoke-RestMethod -Uri $releaseUrl -Headers $headers -ErrorAction Stop
        $Version = $release.tag_name
    }
    catch {
        throw "Could not determine latest release for $Repo`: $_"
    }
}
if ($Version -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9_.-]+)?$') {
    throw "-Version must be 'latest' or a v-prefixed semantic version tag."
}

$versionNum = $Version.TrimStart("v")
$extension = if ($platform -eq "windows") { "zip" } else { "tar.gz" }
$archive = "foundry-agent-manager_${versionNum}_${platform}_${architecture}.${extension}"
$baseUrl = "https://github.com/$Repo/releases/download/$Version"

Write-Host "Installing prebuilt foundry-agent-manager $Version ($platform/$architecture)..."
Write-Host "Go is not required; this installer downloads a compiled release binary."

# --- Download ---
$tmpDir = Join-Path ([IO.Path]::GetTempPath()) "fam-install-$([guid]::NewGuid().ToString('N'))"
New-Item -ItemType Directory -Force -Path $tmpDir | Out-Null

try {
    $downloadHeaders = @{ "Accept" = "application/octet-stream" } + $authHeaders

    Write-Host "Downloading $archive..."
    Invoke-WebRequest -Uri "$baseUrl/$archive" -OutFile (Join-Path $tmpDir $archive) -Headers $downloadHeaders -ErrorAction Stop

    Write-Host "Downloading SHA256SUMS..."
    Invoke-WebRequest -Uri "$baseUrl/SHA256SUMS" -OutFile (Join-Path $tmpDir "SHA256SUMS") -Headers $downloadHeaders -ErrorAction Stop

    # --- Verify checksum ---
    $checksumContent = Get-Content (Join-Path $tmpDir "SHA256SUMS") -Raw
    $escapedArchive = [regex]::Escape($archive)
    $expectedLine = ($checksumContent -split "`n") |
        Where-Object { $_ -match "^[A-Fa-f0-9]{64}\s+\*?$escapedArchive\s*$" } |
        Select-Object -First 1
    if (-not $expectedLine) {
        throw "Checksum for $archive not found in SHA256SUMS."
    }
    $expectedHash = ($expectedLine.Trim() -split "\s+")[0]

    $actualHash = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmpDir $archive)).Hash.ToLowerInvariant()
    if ($actualHash -ne $expectedHash.ToLowerInvariant()) {
        throw "Checksum mismatch for $archive. Expected: $expectedHash  Actual: $actualHash"
    }
    Write-Host "Checksum verified."

    # --- Extract ---
    $binaryName = if ($platform -eq "windows") { "foundry-agent-manager.exe" } else { "foundry-agent-manager" }
    $aliasName = if ($platform -eq "windows") { "fam.exe" } else { "fam" }

    if ($extension -eq "zip") {
        Expand-Archive -Path (Join-Path $tmpDir $archive) -DestinationPath $tmpDir -Force
    } else {
        tar -xzf (Join-Path $tmpDir $archive) -C $tmpDir
    }

    # --- Install ---
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $destPath = Join-Path $InstallDir $binaryName
    $aliasPath = Join-Path $InstallDir $aliasName
    Move-Item -Path (Join-Path $tmpDir $binaryName) -Destination $destPath -Force
    $archiveAliasPath = Join-Path $tmpDir $aliasName
    if (Test-Path $archiveAliasPath) {
        Move-Item -Path $archiveAliasPath -Destination $aliasPath -Force
    }
    else {
        Copy-Item -Path $destPath -Destination $aliasPath -Force
    }

    if ($platform -ne "windows") {
        chmod +x $destPath $aliasPath
    }

    Write-Host "Installed foundry-agent-manager and fam to $InstallDir"

    # --- Optionally modify profile ---
    if ($ModifyProfile) {
        if ($platform -eq "windows") {
            $currentPath = [Environment]::GetEnvironmentVariable("Path", "User")
            if ($currentPath -notlike "*$InstallDir*") {
                [Environment]::SetEnvironmentVariable("Path", "$InstallDir;$currentPath", "User")
                Write-Host "Added $InstallDir to user PATH."
            }
        } else {
            $profileLine = "export PATH=`"${InstallDir}:`$PATH`""
            foreach ($pf in @("$HOME/.profile", "$HOME/.bashrc", "$HOME/.zshrc")) {
                if (Test-Path $pf) {
                    $content = Get-Content $pf -Raw -ErrorAction SilentlyContinue
                    if ($content -notlike "*$InstallDir*") {
                        Add-Content -Path $pf -Value $profileLine
                        Write-Host "Added $InstallDir to PATH in $pf"
                    }
                    break
                }
            }
        }
    }

    Write-Host "Done. Run 'fam -version' or 'foundry-agent-manager --version' to verify."
}
finally {
    Remove-Item -Recurse -Force -Path $tmpDir -ErrorAction SilentlyContinue
}
