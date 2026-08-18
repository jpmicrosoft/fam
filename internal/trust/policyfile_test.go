package trust

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func writePolicyFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadPolicyFileJSON(t *testing.T) {
	path := writePolicyFile(t, "trust.json", `{
		"apimHosts": ["contoso.azure-api.net"],
		"toolHosts": ["api.contoso.com", "mcp.contoso.com"],
		"audiences": ["api://orders-api"]
	}`)
	file, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if file.Path != path {
		t.Fatalf("unexpected path: %s", file.Path)
	}
	if len(file.APIMHosts) != 1 || file.APIMHosts[0] != "contoso.azure-api.net" {
		t.Fatalf("unexpected apimHosts: %#v", file.APIMHosts)
	}
	if len(file.ToolHosts) != 2 {
		t.Fatalf("unexpected toolHosts: %#v", file.ToolHosts)
	}
	if len(file.Audiences) != 1 || file.Audiences[0] != "api://orders-api" {
		t.Fatalf("unexpected audiences: %#v", file.Audiences)
	}
}

func TestLoadPolicyFileYAML(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", `
apimHosts:
  - contoso.azure-api.net
toolHosts:
  - api.contoso.com
audiences:
  - api://orders-api
`)
	file, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(file.APIMHosts) != 1 || file.APIMHosts[0] != "contoso.azure-api.net" {
		t.Fatalf("unexpected apimHosts: %#v", file.APIMHosts)
	}
	if len(file.ToolHosts) != 1 || file.ToolHosts[0] != "api.contoso.com" {
		t.Fatalf("unexpected toolHosts: %#v", file.ToolHosts)
	}
}

func TestLoadPolicyFileMissing(t *testing.T) {
	_, err := LoadPolicyFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error for a missing file, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to read trust policy file") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestLoadPolicyFileEmptyMappingIsValid(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "{}\n")
	file, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if file.APIMHosts != nil || file.ToolHosts != nil || file.Audiences != nil {
		t.Fatalf("expected an empty policy file to yield no approvals, got %#v", file)
	}
}

func TestLoadPolicyFileNotAMapping(t *testing.T) {
	// An empty (null) YAML document decodes successfully to a nil map, unlike
	// a list or scalar document, which yaml.v3 rejects as a type mismatch
	// before LoadPolicyFile ever sees it. This is the case that actually
	// reaches the "must be a mapping" check.
	path := writePolicyFile(t, "trust.yaml", "")
	if _, err := LoadPolicyFile(path); err == nil || !strings.Contains(err.Error(), "must be a mapping") {
		t.Fatalf("expected a mapping-required error, got %v", err)
	}
}

func TestLoadPolicyFileWrongTopLevelTypeIsRejected(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "- contoso.azure-api.net\n- api.contoso.com\n")
	if _, err := LoadPolicyFile(path); err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error for a non-mapping document, got %v", err)
	}
}

func TestLoadPolicyFileUnknownField(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "apimHost:\n  - contoso.azure-api.net\n")
	err := requireLoadPolicyFileError(t, path)
	if !strings.Contains(err.Error(), "unrecognized field(s) apimHost") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestLoadPolicyFileFieldNotAList(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "apimHosts: contoso.azure-api.net\n")
	err := requireLoadPolicyFileError(t, path)
	if !strings.Contains(err.Error(), "apimHosts must be a list of strings") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestLoadPolicyFileItemNotAString(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "apimHosts:\n  - 123\n")
	err := requireLoadPolicyFileError(t, path)
	if !strings.Contains(err.Error(), "apimHosts[0] must be a string") {
		t.Fatalf("unexpected error text: %v", err)
	}
}

func TestLoadPolicyFileInvalidSyntax(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "apimHosts: [unterminated\n")
	if _, err := LoadPolicyFile(path); err == nil || !strings.Contains(err.Error(), "not valid JSON or YAML") {
		t.Fatalf("expected a parse error, got %v", err)
	}
}

func requireLoadPolicyFileError(t *testing.T, path string) error {
	t.Helper()
	_, err := LoadPolicyFile(path)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error, got %v", err)
	}
	return err
}

// --- New() integration: merging a policy file with flags ---

func TestNewMergesPolicyFileWithFlags(t *testing.T) {
	path := writePolicyFile(t, "trust.yaml", "apimHosts:\n  - file-host.azure-api.net\n")
	approvals, err := New(Options{
		APIMHosts: []string{"flag-host.azure-api.net"},
		File:      &FileApprovals{Path: path, APIMHosts: []string{"file-host.azure-api.net"}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := approvals.RequireAPIMHost("https://flag-host.azure-api.net/x", "apim.target"); err != nil {
		t.Fatalf("expected the flag-approved host to pass: %v", err)
	}
	if err := approvals.RequireAPIMHost("https://file-host.azure-api.net/x", "apim.target"); err != nil {
		t.Fatalf("expected the file-approved host to pass: %v", err)
	}
	if err := approvals.RequireAPIMHost("https://other-host.azure-api.net/x", "apim.target"); err == nil {
		t.Fatal("expected an unapproved host to still be rejected")
	}
}

func TestNewWithoutPolicyFileIsUnaffected(t *testing.T) {
	approvals, err := New(Options{APIMHosts: []string{"contoso.azure-api.net"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := approvals.RequireAPIMHost("https://contoso.azure-api.net/x", "apim.target"); err != nil {
		t.Fatalf("expected the approved host to pass: %v", err)
	}
}

func TestNewPolicyFileWildcardHostRejected(t *testing.T) {
	_, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		APIMHosts: []string{"*.evil.example"},
	}})
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for a wildcard file entry, got %v", err)
	}
	if !strings.Contains(err.Error(), "trust policy file trust.yaml: apimHosts[0]") {
		t.Fatalf("expected the error to be attributed to the file and field, got %v", err)
	}
}

func TestNewPolicyFileEmptyHostRejected(t *testing.T) {
	_, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		ToolHosts: []string{"  "},
	}})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config rejection for an empty file entry, got %v", err)
	}
	if !strings.Contains(err.Error(), "toolHosts[0]") {
		t.Fatalf("expected the error to be attributed to toolHosts[0], got %v", err)
	}
}

func TestNewPolicyFileNonASCIIHostRejected(t *testing.T) {
	_, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		ToolHosts: []string{"api.cöntoso.com"},
	}})
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for a non-ASCII file entry, got %v", err)
	}
}

func TestNewPolicyFileAudienceWildcardRejected(t *testing.T) {
	_, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		Audiences: []string{"https://*.azure.com"},
	}})
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for a wildcard audience, got %v", err)
	}
}

func TestNewPolicyFileAudienceOAuthScopeRejected(t *testing.T) {
	_, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		Audiences: []string{"https://cognitiveservices.azure.com/.default"},
	}})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config rejection for an OAuth scope audience, got %v", err)
	}
	if !strings.Contains(err.Error(), "audiences[0]") {
		t.Fatalf("expected the error to be attributed to audiences[0], got %v", err)
	}
}

func TestNewPolicyFileAudienceIsAccepted(t *testing.T) {
	approvals, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		Audiences: []string{"api://orders-api"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := approvals.RequireAudience("api://orders-api", "apim.audience", nil); err != nil {
		t.Fatalf("expected the file-approved audience to pass: %v", err)
	}
}

func TestNewPolicyFileValuesNeverLeakApprovalIntoUnrelatedError(t *testing.T) {
	approvals, err := New(Options{File: &FileApprovals{
		Path:      "trust.yaml",
		APIMHosts: []string{"contoso.azure-api.net"},
	}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	err = approvals.RequireAPIMHost("https://attacker.azure-api.net/x", "apim.target")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected an unapproved host to be rejected, got %v", err)
	}
	if strings.Contains(err.Error(), "contoso.azure-api.net") {
		t.Fatalf("approval values must not leak into errors that reach receipts: %v", err)
	}
}
