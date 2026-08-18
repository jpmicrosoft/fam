package netcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

// TestReadContainedFileBoundsAreExactAt8MiB pins both sides of the documented
// size limit so a future refactor cannot quietly widen or narrow it.
func TestReadContainedFileBoundsAreExactAt8MiB(t *testing.T) {
	if MaxContainedFileBytes != 8<<20 {
		t.Fatalf("the documented bound is 8 MiB, got %d bytes", MaxContainedFileBytes)
	}
	base := t.TempDir()

	atLimit := filepath.Join(base, "at-limit.json")
	if err := os.WriteFile(atLimit, make([]byte, MaxContainedFileBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadContainedFile(base, "at-limit.json", "spec_file")
	if err != nil {
		t.Fatalf("a file exactly at the limit must be readable: %v", err)
	}
	if len(data) != MaxContainedFileBytes {
		t.Fatalf("expected %d bytes, got %d", MaxContainedFileBytes, len(data))
	}

	overLimit := filepath.Join(base, "over-limit.json")
	if err := os.WriteFile(overLimit, make([]byte, MaxContainedFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadContainedFile(base, "over-limit.json", "spec_file"); err == nil ||
		!errs.IsKind(err, "config") {
		t.Fatalf("one byte over the limit must be rejected, got %v", err)
	}
}

func TestReadContainedFileRejectsDirectories(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "specs", "spec.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := ReadContainedFile(base, "specs/spec.json", "spec_file")
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("a directory must be rejected as a contained file, got %v", err)
	}
	if !strings.Contains(err.Error(), "directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteContainedFileExclusiveCreatesOnce(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "downloads"), 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := WriteContainedFileExclusive(
		base,
		"downloads/result.txt",
		"output",
		[]byte("ready"),
		10,
	)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "ready" {
		t.Fatalf("unexpected output: %q", data)
	}
	if _, err := WriteContainedFileExclusive(
		base,
		"downloads/result.txt",
		"output",
		[]byte("overwrite"),
		10,
	); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("existing output must not be overwritten: %v", err)
	}
}

// TestReadContainedFileRejectsWindowsDeviceNames covers the Windows-specific
// path hazard where "NUL", "CON", or "COM1" inside the manifest directory would
// otherwise resolve to a device instead of a manifest-relative file.
func TestReadContainedFileRejectsWindowsDeviceNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows device names are a Windows-only path hazard")
	}
	base := t.TempDir()
	for _, name := range []string{"NUL", "CON", "nul", `specs\NUL`, "COM1"} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadContainedFile(base, name, "spec_file"); err == nil {
				t.Fatalf("reading the Windows device %q must not succeed", name)
			}
		})
	}
}

// TestReadContainedFileAcceptsBothSeparatorsOnWindows proves that operators can
// write either separator in a manifest without changing containment behavior.
func TestReadContainedFileAcceptsBothSeparators(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "specs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "specs", "spec.json"), []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	references := []string{"specs/spec.json", "./specs/spec.json"}
	if runtime.GOOS == "windows" {
		references = append(references, `specs\spec.json`, `.\specs\spec.json`)
	}
	for _, reference := range references {
		data, err := ReadContainedFile(base, reference, "spec_file")
		if err != nil {
			t.Fatalf("reference %q must resolve: %v", reference, err)
		}
		if string(data) != `{"ok":true}` {
			t.Fatalf("reference %q returned %q", reference, data)
		}
	}
}

// TestReadContainedFileRejectsEscapesBeforeTouchingTheFilesystem asserts the
// path-shape gate runs first, so no escaping path is ever opened.
func TestReadContainedFileRejectsEscapesBeforeTouchingTheFilesystem(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secrets.json")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	references := []string{
		"../secrets.json",
		"../../secrets.json",
		"specs/../../secrets.json",
		outside,
		"/etc/passwd",
	}
	if runtime.GOOS == "windows" {
		references = append(references, `C:\Windows\win.ini`, `c:secrets.json`, `\\server\share\x`)
	}
	for _, reference := range references {
		data, err := ReadContainedFile(base, reference, "spec_file")
		if err == nil || !errs.IsKind(err, "security") {
			t.Fatalf("reference %q must be a security rejection, got %v (%q)", reference, err, data)
		}
		if strings.Contains(string(data), "secret") {
			t.Fatalf("reference %q leaked file content", reference)
		}
	}
}

// TestSuffixEnvironmentExtensionIsNormalizedAndDeduplicated covers the operator
// trust-boundary environment variable used for custom APIM gateway domains.
func TestSuffixEnvironmentExtensionIsNormalizedAndDeduplicated(t *testing.T) {
	t.Setenv(apimEnv, " .Internal.Example , azure-api.net ,internal.example,")
	merged := suffixes(apimEnv, defaultAPIMSuffixes)
	seen := map[string]int{}
	for _, suffix := range merged {
		seen[suffix]++
		if suffix != strings.ToLower(suffix) {
			t.Fatalf("suffix %q was not normalized", suffix)
		}
		if strings.HasPrefix(suffix, ".") || strings.TrimSpace(suffix) != suffix {
			t.Fatalf("suffix %q was not trimmed", suffix)
		}
	}
	for suffix, count := range seen {
		if count != 1 {
			t.Fatalf("suffix %q appears %d times", suffix, count)
		}
	}
	if seen["azure-api.net"] != 1 || seen["internal.example"] != 1 {
		t.Fatalf("unexpected merged suffixes: %#v", merged)
	}
	// The default list must not be mutated by the merge.
	if len(defaultAPIMSuffixes) != 1 || defaultAPIMSuffixes[0] != "azure-api.net" {
		t.Fatalf("the default suffix list was mutated: %#v", defaultAPIMSuffixes)
	}
}

// TestFoundryAndKeyVaultAllowListsCannotBeExtendedByEnvironment guards the
// documented rule that only APIM suffixes are operator-extensible.
func TestFoundryAndKeyVaultAllowListsCannotBeExtendedByEnvironment(t *testing.T) {
	t.Setenv(apimEnv, "attacker.example")
	t.Setenv("FOUNDRY_AGENT_MANAGER_ALLOWED_ENDPOINT_SUFFIXES", "attacker.example")
	if _, err := ValidateFoundryEndpointForSuffixes(
		"https://acct.attacker.example/api/projects/p",
		"project.endpoint",
		[]string{"services.ai.azure.com"},
	); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("the Foundry allow-list must not be extensible, got %v", err)
	}
	if _, err := ValidateKeyVaultURL(
		"https://vault.attacker.example/secrets/apim",
		"--apim-subscription-key-key-vault",
		[]string{"vault.azure.net"},
	); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("the Key Vault allow-list must not be extensible, got %v", err)
	}
	if _, err := ValidateStorageQueueEndpointForSuffixes(
		"https://storage.attacker.example",
		"tools[0].input_queue.service_endpoint",
		[]string{"queue.core.windows.net"},
	); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("the Storage Queue allow-list must not be extensible, got %v", err)
	}
}
