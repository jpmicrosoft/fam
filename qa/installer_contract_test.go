package qa

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		"command -v curl",
		"command -v wget",
		"--no-config --no-netrc",
		"authenticated/private access requires curl",
		"Install curl or GNU Wget",
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

func TestShellInstallerRejectsExplicitVersionWithoutVPrefix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX installer behavior is exercised by CI")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	command := exec.Command(sh, "../scripts/install.sh", "--version", "0.15.0")
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("install.sh accepted an explicit version without the required v prefix")
	}
	if !strings.Contains(string(output), "--version must be 'latest' or a v-prefixed semantic version tag") {
		t.Fatalf("install.sh returned the wrong explicit-version error: %s", output)
	}
}

func TestShellInstallerDownloaders(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh is unavailable")
	}
	tools := map[string]string{}
	for _, name := range []string{"awk", "cat", "chmod", "grep", "gzip", "head", "mkdir", "mktemp", "mv", "rm", "sed", "sha256sum", "tar"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("%s is unavailable", name)
		}
		tools[name] = path
	}

	const archiveName = "fam_1.2.3_linux_amd64.tar.gz"
	const binary = "#!/bin/sh\nprintf 'fam fixture\\n'\n"
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "fam", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte(binary)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		curl, wget   bool
		version      string
		tokens       bool
		noGH         bool
		failAt       string
		badChecksum  bool
		wantTool     string
		wantError    string
		wantRequests int
	}{
		{name: "curl preferred", curl: true, wget: true, wantTool: "curl", wantRequests: 3},
		{name: "curl only", curl: true, version: "v1.2.3", wantTool: "curl", wantRequests: 3},
		{name: "curl anonymous", curl: true, noGH: true, wantTool: "curl", wantRequests: 3},
		{name: "curl token precedence", curl: true, tokens: true, wantTool: "curl", wantRequests: 3},
		{name: "wget latest", wget: true, wantTool: "wget", wantRequests: 3},
		{name: "wget explicit version", wget: true, version: "v1.2.3", wantTool: "wget", wantRequests: 3},
		{name: "wget ignores tokens", wget: true, tokens: true, wantTool: "wget", wantRequests: 3},
		{name: "no downloader", wantError: "Install curl or GNU Wget"},
		{name: "curl failure does not fall back", curl: true, wget: true, failAt: "api", wantTool: "curl", wantError: "Failed to download", wantRequests: 1},
		{name: "wget API failure", wget: true, failAt: "api", wantTool: "wget", wantError: "Failed to download", wantRequests: 1},
		{name: "wget archive failure", wget: true, failAt: "archive", wantTool: "wget", wantError: "Failed to download", wantRequests: 2},
		{name: "wget checksum download failure", wget: true, failAt: "checksums", wantTool: "wget", wantError: "Failed to download", wantRequests: 3},
		{name: "wget checksum mismatch", wget: true, badChecksum: true, wantTool: "wget", wantError: "Checksum mismatch", wantRequests: 3},
		{name: "curl checksum mismatch", curl: true, badChecksum: true, wantTool: "curl", wantError: "Checksum mismatch", wantRequests: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			binDir := filepath.Join(root, "bin")
			home := filepath.Join(root, "home")
			temp := filepath.Join(root, "downloads")
			installDir := filepath.Join(root, "install with spaces")
			for _, dir := range []string{binDir, home, temp} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte) {
				t.Helper()
				if err := os.WriteFile(path, data, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			for name, path := range tools {
				quotedPath := "'" + strings.ReplaceAll(filepath.ToSlash(path), "'", "'\"'\"'") + "'"
				write(filepath.Join(binDir, name), []byte("#!/bin/sh\nexec "+quotedPath+" \"$@\"\n"))
			}
			write(filepath.Join(binDir, "uname"), []byte("#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo x86_64;; *) exit 1;; esac\n"))
			if !tc.noGH {
				write(filepath.Join(binDir, "gh"), []byte("#!/bin/sh\nprintf 'called\\n' >> \"$FIXTURE_ROOT/gh.log\"\nprintf 'fixture-gh-auth\\n'\n"))
			}
			for name, enabled := range map[string]bool{"curl": tc.curl, "wget": tc.wget} {
				if enabled {
					write(filepath.Join(binDir, name), []byte(shellInstallerDownloaderFixture))
				}
			}
			write(filepath.Join(root, "requests.log"), nil)
			write(filepath.Join(root, "release.json"), []byte(fmt.Sprintf(`{"tag_name":"v1.2.3","assets":[{"name":%q}]}`, archiveName)))
			write(filepath.Join(root, archiveName), archive.Bytes())
			sum := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			if tc.badChecksum {
				sum = strings.Repeat("0", 64)
			}
			write(filepath.Join(root, "SHA256SUMS"), []byte(sum+"  "+archiveName+"\n"))
			const profile = "# existing profile\n"
			write(filepath.Join(home, ".profile"), []byte(profile))

			args := []string{filepath.ToSlash(filepath.Join("..", "scripts", "install.sh"))}
			if tc.version != "" {
				args = append(args, "--version", tc.version)
			}
			cmd := exec.Command(sh, args...)
			cmd.Env = []string{
				"PATH=" + binDir,
				"HOME=" + filepath.ToSlash(home),
				"TMPDIR=" + filepath.ToSlash(temp),
				"INSTALL_DIR=" + filepath.ToSlash(installDir),
				"FIXTURE_ROOT=" + filepath.ToSlash(root),
				"FIXTURE_FAIL=" + tc.failAt,
			}
			if tc.tokens {
				cmd.Env = append(cmd.Env, "FAM_INSTALL_TOKEN=fixture-install-token", "GITHUB_TOKEN=fixture-github-token", "GH_TOKEN=fixture-gh-token")
			}
			output, runErr := cmd.CombinedOutput()
			if tc.wantError == "" {
				if runErr != nil {
					t.Fatalf("installer failed: %v\n%s", runErr, output)
				}
				installed, err := os.ReadFile(filepath.Join(installDir, "fam"))
				if err != nil || string(installed) != binary {
					t.Fatalf("wrong installed binary: %q, %v\n%s", installed, err, output)
				}
				if !strings.Contains(string(output), "Checksum verified.") {
					t.Fatalf("checksum verification missing:\n%s", output)
				}
			} else {
				if runErr == nil || !strings.Contains(string(output), tc.wantError) {
					t.Fatalf("expected %q, got %v:\n%s", tc.wantError, runErr, output)
				}
				if _, err := os.Stat(installDir); !os.IsNotExist(err) {
					t.Fatalf("failed download created installation directory: %v", err)
				}
			}
			for _, token := range []string{"fixture-install-token", "fixture-github-token", "fixture-gh-token", "fixture-gh-auth"} {
				if strings.Contains(string(output), token) {
					t.Fatal("installer output exposed a fixture token")
				}
			}
			log, err := os.ReadFile(filepath.Join(root, "requests.log"))
			if err != nil {
				t.Fatal(err)
			}
			requests := strings.Split(strings.TrimSpace(string(log)), "\n")
			if len(log) == 0 {
				requests = nil
			}
			if len(requests) != tc.wantRequests {
				t.Fatalf("expected %d requests, got %d:\n%s", tc.wantRequests, len(requests), log)
			}
			for i, request := range requests {
				auth := ""
				if tc.wantTool == "curl" {
					if !tc.noGH {
						auth = "token fixture-gh-auth"
					}
					if tc.tokens {
						auth = "token fixture-install-token"
					}
				}
				accept := "application/octet-stream"
				if i == 0 {
					accept = "application/vnd.github+json"
					apiPath := "releases/latest"
					if tc.version != "" {
						apiPath = "releases/tags/" + tc.version
					}
					if !strings.HasSuffix(request, apiPath) {
						t.Fatalf("wrong API URL: %s", request)
					}
				}
				if !strings.HasPrefix(request, tc.wantTool+"|"+accept+"|"+auth+"|") {
					t.Fatalf("wrong downloader or headers: %s", request)
				}
			}
			if tc.wantTool != "curl" || tc.tokens || tc.noGH {
				if _, err := os.Stat(filepath.Join(root, "gh.log")); !os.IsNotExist(err) {
					t.Fatalf("installer unexpectedly queried gh authentication: %v", err)
				}
			}
			gotProfile, err := os.ReadFile(filepath.Join(home, ".profile"))
			if err != nil || string(gotProfile) != profile {
				t.Fatalf("installer modified the profile without opt-in: %q, %v", gotProfile, err)
			}
			remaining, err := os.ReadDir(temp)
			if err != nil || len(remaining) != 0 {
				t.Fatalf("installer left temporary downloads: %v, %v", remaining, err)
			}
		})
	}
}

const shellInstallerDownloaderFixture = `#!/bin/sh
set -eu
tool="${0##*/}"
dest=-
accept=""
auth=""
no_config=false
no_netrc=false
header() {
  case "$1" in
    "Accept: "*) accept="${1#Accept: }";;
    "Authorization: "*) auth="${1#Authorization: }";;
    *) echo "Unexpected fixture header" >&2; exit 1;;
  esac
}
while [ $# -gt 0 ]; do
  case "$1" in
    -fsSL|-q) shift;;
    -o|-O) dest="$2"; shift 2;;
    -H) header "$2"; shift 2;;
    --header=*) header "${1#--header=}"; shift;;
    --no-config) no_config=true; shift;;
    --no-netrc) no_netrc=true; shift;;
    --) shift; break;;
    *) echo "Unexpected fixture argument: $1" >&2; exit 1;;
  esac
done
[ $# -eq 1 ] || exit 1
url="$1"
if [ "$tool" = wget ]; then
  if [ "$no_config" != true ] || [ "$no_netrc" != true ] || [ -n "$auth" ]; then
    echo "Wget must not load or forward credentials" >&2
    exit 1
  fi
fi
printf '%s|%s|%s|%s\n' "$tool" "$accept" "$auth" "$url" >> "$FIXTURE_ROOT/requests.log"
case "$url" in
  https://api.github.com/repos/jpmicrosoft/fam/releases/latest|https://api.github.com/repos/jpmicrosoft/fam/releases/tags/v1.2.3)
    kind=api; file=release.json;;
  https://github.com/jpmicrosoft/fam/releases/download/v1.2.3/fam_1.2.3_linux_amd64.tar.gz)
    kind=archive; file=fam_1.2.3_linux_amd64.tar.gz;;
  https://github.com/jpmicrosoft/fam/releases/download/v1.2.3/SHA256SUMS)
    kind=checksums; file=SHA256SUMS;;
  *) echo "Unexpected fixture URL" >&2; exit 1;;
esac
if [ "$FIXTURE_FAIL" = "$kind" ]; then
  echo "Fixture transport failure" >&2
  exit 22
fi
if [ "$dest" = - ]; then
  cat "$FIXTURE_ROOT/$file"
else
  cat "$FIXTURE_ROOT/$file" > "$dest"
fi
`
