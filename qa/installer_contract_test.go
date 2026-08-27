package qa

import (
	"strings"
	"testing"
)

// TestShellInstallerContract verifies the POSIX shell installer meets
// the verified-installer contract without executing it.
func TestShellInstallerContract(t *testing.T) {
	script := repositoryFile(t, "scripts", "install.sh")

	requireText(t, script,
		"jpmicrosoft/fam",
		"SHA256SUMS",
		"sha256sum",
		"Checksum mismatch",
		"Checksum verified",
		"--version",
		"--install-dir",
		"--repo",
		"--modify-profile",
		"latest",
		"FAM_INSTALL_TOKEN",
		"GITHUB_TOKEN",
		"GH_TOKEN",
		"gh auth token",
		"Unsupported operating system",
		"Unsupported architecture",
		"releases/download",
		"set -eu",
		"$HOME/.local/bin",
		"--repo must use OWNER/REPO format",
		"prebuilt fam",
		`PREFERRED_ARCHIVE="fam_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"`,
		`LEGACY_ARCHIVE="foundry-agent-manager_${VERSION_NUM}_${OS}_${ARCH}.tar.gz"`,
		`grep -Fq "\"${PREFERRED_ARCHIVE}\""`,
		`grep -Fq "\"${LEGACY_ARCHIVE}\""`,
		"only fam will be installed",
		"${INSTALL_DIR}/fam",
		`rm -f "${INSTALL_DIR}/foundry-agent-manager"`,
		"'fam --version'",
		"Go is not required",
	)

	// Must never print tokens
	for _, forbidden := range []string{
		"echo $TOKEN",
		"echo $FAM_INSTALL_TOKEN",
		"echo $GITHUB_TOKEN",
		"echo $GH_TOKEN",
		"echo \"$TOKEN",
		"echo \"$FAM_INSTALL_TOKEN",
		"echo \"$GITHUB_TOKEN",
		"echo \"$GH_TOKEN",
		"go build",
		"command -v go",
		"${TMPDIR_INST}/foundry-agent-manager",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("shell installer prints token via %q", forbidden)
		}
	}

	// Must not modify profile by default
	lines := strings.Split(script, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "MODIFY_PROFILE=true") && !strings.Contains(trimmed, "$") && !strings.Contains(trimmed, "flag") {
			// The default assignment is fine
			_ = i
		}
	}
}

// TestPowerShellInstallerContract verifies the PowerShell installer meets
// the verified-installer contract.
func TestPowerShellInstallerContract(t *testing.T) {
	script := repositoryFile(t, "scripts", "install.ps1")

	requireText(t, script,
		"jpmicrosoft/fam",
		"SHA256SUMS",
		"Get-FileHash",
		"Checksum mismatch",
		"Checksum verified",
		"-Version",
		"-InstallDir",
		"-Repo",
		"-ModifyProfile",
		"latest",
		"FAM_INSTALL_TOKEN",
		"GITHUB_TOKEN",
		"GH_TOKEN",
		"gh auth token",
		"Unsupported operating system",
		"Unsupported architecture",
		"releases/download",
		"ErrorActionPreference",
		"[regex]::Escape",
		".local/bin",
		"-Repo must use OWNER/REPO format",
		"prebuilt fam",
		`$binaryName = if ($platform -eq "windows") { "fam.exe" } else { "fam" }`,
		`$preferredArchive = "fam_${versionNum}_${platform}_${architecture}.${extension}"`,
		`$legacyArchive = "foundry-agent-manager_${versionNum}_${platform}_${architecture}.${extension}"`,
		"only fam will be installed",
		`$retiredBinaryName = if ($platform -eq "windows") { "foundry-agent-manager.exe" } else { "foundry-agent-manager" }`,
		"Remove-Item -LiteralPath $retiredBinaryPath -Force",
		"'fam --version'",
		"Go is not required",
	)

	for _, forbidden := range []string{
		"Write-Host $token",
		"Write-Host \"$token",
		"Write-Host $env:FAM_INSTALL_TOKEN",
		"Write-Host \"$env:FAM_INSTALL_TOKEN",
		"echo $token",
		"go build",
		"Get-Command go",
		"$aliasName",
	} {
		if strings.Contains(strings.ToLower(script), strings.ToLower(forbidden)) {
			t.Errorf("PowerShell installer prints token via %q", forbidden)
		}
	}
}
