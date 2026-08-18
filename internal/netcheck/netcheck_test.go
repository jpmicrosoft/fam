package netcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestValidateFoundryEndpoint_AcceptsAzureHosts(t *testing.T) {
	urls := []string{
		"https://acct.services.ai.azure.com",
		"https://acct.cognitiveservices.azure.com",
		"https://acct.openai.azure.com",
		"https://acct.services.ai.azure.com/api/projects/p",
	}
	for _, u := range urls {
		if _, err := ValidateFoundryEndpoint(u, "ep"); err != nil {
			t.Errorf("expected %q to be accepted, got: %v", u, err)
		}
	}
}

func TestValidateFoundryEndpoint_CaseInsensitive(t *testing.T) {
	if _, err := ValidateFoundryEndpoint("https://ACCT.Services.AI.Azure.Com", "ep"); err != nil {
		t.Errorf("expected case-insensitive match, got: %v", err)
	}
}

func TestValidateFoundryEndpoint_RejectsNonHTTPS(t *testing.T) {
	_, err := ValidateFoundryEndpoint("http://acct.services.ai.azure.com", "ep")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for non-https")
	}
}

func TestValidateFoundryEndpoint_RejectsForeignHost(t *testing.T) {
	_, err := ValidateFoundryEndpoint("https://evil.example.com", "ep")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for foreign host")
	}
}

func TestValidateFoundryEndpoint_RejectsEmbeddedCredentials(t *testing.T) {
	_, err := ValidateFoundryEndpoint("https://user:pass@acct.services.ai.azure.com", "ep")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for embedded credentials")
	}
}

func TestValidateFoundryEndpoint_RejectsMetadataIP(t *testing.T) {
	_, err := ValidateFoundryEndpoint("https://169.254.169.254/", "ep")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for metadata IP")
	}
}

func TestValidateFoundryEndpoint_RejectsSuffixSpoofing(t *testing.T) {
	_, err := ValidateFoundryEndpoint("https://acct.services.ai.azure.com.evil.com", "ep")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for suffix spoofing")
	}
}

func TestValidateFoundryEndpoint_OperatorEnvCannotWidenAllowList(t *testing.T) {
	u := "https://acct.private.example"
	if _, err := ValidateFoundryEndpoint(u, "ep"); err == nil {
		t.Fatal("expected rejection without env")
	}
	os.Setenv("FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES", "private.example")
	defer os.Unsetenv("FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES")
	if _, err := ValidateFoundryEndpoint(u, "ep"); err == nil {
		t.Fatal("Foundry token-bearing hosts must not be extendable")
	}
}

func TestAzureCloudAPIMCannotAllowGovernmentSuffix(t *testing.T) {
	os.Setenv("FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES", "azure-api.us")
	defer os.Unsetenv("FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES")
	_, err := ValidateAPIMTargetForSuffixes(
		"https://gateway.azure-api.us",
		"apim.target",
		[]string{"azure-api.net"},
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected cross-cloud APIM target rejection, got %v", err)
	}
}

func TestStorageQueueEndpointUsesAzureCloud(t *testing.T) {
	if _, err := ValidateStorageQueueEndpointForSuffixes(
		"https://account.queue.core.windows.net",
		"queue",
		[]string{"queue.core.windows.net"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateStorageQueueEndpointForSuffixes(
		"https://account.queue.core.usgovcloudapi.net",
		"queue",
		[]string{"queue.core.windows.net"},
	); err == nil {
		t.Fatal("expected Government Storage Queue endpoint to be rejected for AzureCloud")
	}
}

func TestMonitorIngestionEndpointIsHostPinnedAndBaseOnly(t *testing.T) {
	allowed := []string{"ingest.monitor.azure.com"}
	accepted, err := ValidateMonitorIngestionEndpointForSuffixes(
		"https://dce.eastus-1.ingest.monitor.azure.com/",
		"endpoint",
		allowed,
	)
	if err != nil {
		t.Fatal(err)
	}
	if accepted != "https://dce.eastus-1.ingest.monitor.azure.com" {
		t.Fatalf("unexpected normalized endpoint: %q", accepted)
	}
	for _, rejected := range []string{
		"http://dce.eastus-1.ingest.monitor.azure.com",
		"https://dce.eastus-1.ingest.monitor.azure.com/path",
		"https://dce.eastus-1.ingest.monitor.azure.com?redirect=1",
		"https://dce.eastus-1.ingest.monitor.azure.com.evil.example",
	} {
		if _, err := ValidateMonitorIngestionEndpointForSuffixes(
			rejected,
			"endpoint",
			allowed,
		); err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("expected security rejection for %q, got %v", rejected, err)
		}
	}
}

func TestValidateAPIMTarget_AcceptsAzureAPINet(t *testing.T) {
	if _, err := ValidateAPIMTarget("https://gw.azure-api.net", "apim"); err != nil {
		t.Errorf("expected acceptance, got: %v", err)
	}
}

func TestValidateAPIMTarget_RejectsSpoof(t *testing.T) {
	_, err := ValidateAPIMTarget("https://gw.azure-api.net.evil.com", "apim")
	if err == nil {
		t.Error("expected rejection for spoof")
	}
}

func TestValidateAPIMTarget_RejectsFoundryHostAsAPIM(t *testing.T) {
	_, err := ValidateAPIMTarget("https://acct.services.ai.azure.com", "apim")
	if err == nil {
		t.Error("expected rejection for foundry host as APIM")
	}
}

func TestValidateAPIMTarget_RejectsNonHTTPS(t *testing.T) {
	_, err := ValidateAPIMTarget("http://gw.azure-api.net", "apim")
	if err == nil {
		t.Error("expected rejection for non-https")
	}
}

func TestRequireContainedFile_AcceptsContainedRelativePath(t *testing.T) {
	base := t.TempDir()
	result, err := RequireContainedFile(base, "openapi/spec.json", "spec_file")
	if err != nil {
		t.Fatalf("expected acceptance, got: %v", err)
	}
	absBase, _ := filepath.Abs(base)
	rel, _ := filepath.Rel(absBase, result)
	if strings.HasPrefix(rel, "..") {
		t.Error("result is not contained in base")
	}
}

func TestRequireContainedFile_RejectsParentTraversal(t *testing.T) {
	base := t.TempDir()
	_, err := RequireContainedFile(base, "../secrets.json", "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for parent traversal")
	}
}

func TestRequireContainedFile_RejectsDeepTraversal(t *testing.T) {
	base := t.TempDir()
	_, err := RequireContainedFile(base, "../../etc/passwd", "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for deep traversal")
	}
}

func TestRequireContainedFile_RejectsAbsolutePath(t *testing.T) {
	base := t.TempDir()
	// Use a Windows-style absolute path on Windows, POSIX on others
	outside := `C:\foundry_agent_manager_outside_probe.json`
	_, err := RequireContainedFile(base, outside, "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Error("expected security error for absolute path")
	}
}

func TestValidateRelativeFileReferenceRejectsCrossPlatformAbsolutePaths(t *testing.T) {
	paths := []string{
		"/tmp/spec.json",
		`\server\share\spec.json`,
		`C:\spec.json`,
		"C:spec.json",
		"specs/foo..bar.json",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			err := ValidateRelativeFileReference(path, "spec_file")
			if err == nil || !errs.IsKind(err, "security") {
				t.Fatalf("expected security error for %q, got %v", path, err)
			}
		})
	}
}

func TestRequireContainedFile_RejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked.json")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are unavailable in this environment: %v", err)
	}

	_, err := RequireContainedFile(base, "linked.json", "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security error for symlink escape, got %v", err)
	}
}

func TestReadContainedFile_ReadsContainedFile(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "specs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "specs", "spec.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadContainedFile(base, "specs/spec.json", "spec_file")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{"ok":true}` {
		t.Fatalf("unexpected contents: %s", data)
	}
}

func TestReadContainedFile_RejectsUnsafeReferences(t *testing.T) {
	base := t.TempDir()
	for name, relative := range map[string]string{
		"parent traversal": "../secrets.json",
		"absolute path":    `C:\foundry_agent_manager_outside_probe.json`,
		"posix absolute":   "/etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadContainedFile(base, relative, "spec_file"); err == nil ||
				!errs.IsKind(err, "security") {
				t.Fatalf("expected security error for %q", relative)
			}
		})
	}
}

func TestReadContainedFile_ReportsMissingFileWithoutSecurityKind(t *testing.T) {
	base := t.TempDir()
	_, err := ReadContainedFile(base, "specs/missing.json", "spec_file")
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error for a missing file, got %v", err)
	}
}

func TestReadContainedFile_RejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(base, "linked.json")); err != nil {
		t.Skipf("symlinks are unavailable in this environment: %v", err)
	}
	data, err := ReadContainedFile(base, "linked.json", "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security error for symlink escape, got %v (%q)", err, data)
	}
}

// TestReadContainedFile_RejectsDirectoryJunctionEscape covers the Windows
// junction case that filepath.EvalSymlinks does not reliably resolve.
func TestReadContainedFile_RejectsDirectoryJunctionEscape(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("directory junctions are Windows-specific")
	}
	base := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "outside.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(base, "link")
	if err := exec.Command("cmd", "/c", "mklink", "/J", junction, outsideDir).Run(); err != nil {
		t.Skipf("directory junctions are unavailable in this environment: %v", err)
	}
	data, err := ReadContainedFile(base, `link\outside.json`, "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security error for junction escape, got %v (%q)", err, data)
	}
}

// TestReadContainedFile_RejectsSwappedDirectory approximates the check-then-open
// race: the validated path is replaced by an escaping link before the read.
func TestReadContainedFile_RejectsSwappedDirectory(t *testing.T) {
	base := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "spec.json"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(base, "specs")
	if err := os.Mkdir(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inside, "spec.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContainedFile(base, "specs/spec.json", "spec_file"); err != nil {
		t.Fatalf("unexpected error before the swap: %v", err)
	}
	if err := os.RemoveAll(inside); err != nil {
		t.Fatal(err)
	}
	if err := linkDirectory(outsideDir, inside); err != nil {
		t.Skipf("directory links are unavailable in this environment: %v", err)
	}
	data, err := ReadContainedFile(base, "specs/spec.json", "spec_file")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security error after the directory swap, got %v (%q)", err, data)
	}
}

// linkDirectory creates a directory link, falling back to a Windows junction
// when the process cannot create symlinks.
func linkDirectory(target, link string) error {
	if err := os.Symlink(target, link); err == nil {
		return nil
	} else if runtime.GOOS != "windows" {
		return err
	}
	return exec.Command("cmd", "/c", "mklink", "/J", link, target).Run()
}

func TestReadContainedFile_RejectsOversizedFile(t *testing.T) {
	base := t.TempDir()
	large := make([]byte, MaxContainedFileBytes+1)
	if err := os.WriteFile(filepath.Join(base, "spec.json"), large, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContainedFile(base, "spec.json", "spec_file"); err == nil ||
		!errs.IsKind(err, "config") {
		t.Fatalf("expected an oversized-file config error, got %v", err)
	}
}
