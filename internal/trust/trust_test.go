package trust

import (
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func mustApprovals(t *testing.T, options Options) Approvals {
	t.Helper()
	approvals, err := New(options)
	if err != nil {
		t.Fatalf("unexpected approval parse error: %v", err)
	}
	return approvals
}

func TestSuffixSharingHostIsRejectedWithoutApproval(t *testing.T) {
	approvals := mustApprovals(t, Options{APIMHosts: []string{"contoso.azure-api.net"}})
	err := approvals.RequireAPIMHost("https://attacker.azure-api.net/agents/chat", "apim.target")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security rejection for an unapproved sibling host, got %v", err)
	}
	if strings.Contains(err.Error(), "contoso.azure-api.net") {
		t.Fatalf("approval values must not leak into errors that reach receipts: %v", err)
	}
}

func TestApprovedExactHostIsAccepted(t *testing.T) {
	approvals := mustApprovals(t, Options{APIMHosts: []string{"contoso.azure-api.net"}})
	if err := approvals.RequireAPIMHost("https://contoso.azure-api.net/agents/chat", "apim.target"); err != nil {
		t.Fatalf("expected approved host to pass, got %v", err)
	}
}

func TestHostNormalizationHandlesCaseTrailingDotAndDefaultPort(t *testing.T) {
	approvals := mustApprovals(t, Options{ToolHosts: []string{"API.Contoso.COM."}})
	for _, rawURL := range []string{
		"https://api.contoso.com/orders",
		"https://API.CONTOSO.COM/orders",
		"https://api.contoso.com./orders",
		"https://api.contoso.com:443/orders",
	} {
		if err := approvals.RequireToolHost(rawURL, "tool"); err != nil {
			t.Fatalf("expected %q to match the approved host, got %v", rawURL, err)
		}
	}
}

func TestExplicitPortsMustMatchExactly(t *testing.T) {
	approvals := mustApprovals(t, Options{ToolHosts: []string{"api.contoso.com:8443"}})
	if err := approvals.RequireToolHost("https://api.contoso.com:8443/orders", "tool"); err != nil {
		t.Fatalf("expected the approved host:port to pass, got %v", err)
	}
	if err := approvals.RequireToolHost("https://api.contoso.com/orders", "tool"); err == nil {
		t.Fatal("a host:port approval must not approve the default port")
	}
	plain := mustApprovals(t, Options{ToolHosts: []string{"api.contoso.com"}})
	if err := plain.RequireToolHost("https://api.contoso.com:8443/orders", "tool"); err == nil {
		t.Fatal("a host approval must not approve an alternate port")
	}
}

func TestUserinfoAndNonHTTPSDestinationsAreRejected(t *testing.T) {
	approvals := mustApprovals(t, Options{ToolHosts: []string{"api.contoso.com"}})
	tests := map[string]string{
		"userinfo":  "https://api.contoso.com@evil.example/orders",
		"http":      "http://api.contoso.com/orders",
		"relative":  "/orders",
		"no host":   "https:///orders",
		"empty":     "   ",
		"non ascii": "https://api.cöntoso.com/orders",
	}
	for name, rawURL := range tests {
		t.Run(name, func(t *testing.T) {
			err := approvals.RequireToolHost(rawURL, "tool")
			if err == nil || !errs.IsKind(err, "security") {
				t.Fatalf("expected security rejection for %q, got %v", rawURL, err)
			}
		})
	}
}

func TestWildcardApprovalsAreRejected(t *testing.T) {
	for _, value := range []string{"*", "*.azure-api.net", ".azure-api.net"} {
		if _, err := New(Options{APIMHosts: []string{value}}); err == nil {
			t.Fatalf("expected %q to be rejected as a wildcard approval", value)
		}
	}
	if _, err := New(Options{Audiences: []string{"https://*.azure.com"}}); err == nil {
		t.Fatal("expected wildcard audience approval to be rejected")
	}
}

func TestURLShapedHostApprovalsAreRejected(t *testing.T) {
	for _, value := range []string{
		"https://contoso.azure-api.net",
		"contoso.azure-api.net/agents",
		"user@contoso.azure-api.net",
		"",
	} {
		if _, err := New(Options{ToolHosts: []string{value}}); err == nil {
			t.Fatalf("expected %q to be rejected as a host approval", value)
		}
	}
}

func TestBuiltInAudienceIsAllowedWithoutApproval(t *testing.T) {
	approvals := mustApprovals(t, Options{})
	builtIn := []string{"https://cognitiveservices.azure.com"}
	for _, audience := range []string{
		"https://cognitiveservices.azure.com",
		"https://cognitiveservices.azure.com/",
		"https://CognitiveServices.Azure.com",
	} {
		if err := approvals.RequireAudience(audience, "apim.audience", builtIn); err != nil {
			t.Fatalf("expected built-in audience %q to pass, got %v", audience, err)
		}
	}
}

func TestDangerousAudienceRequiresApproval(t *testing.T) {
	builtIn := []string{"https://cognitiveservices.azure.com"}
	approvals := mustApprovals(t, Options{})
	err := approvals.RequireAudience("https://management.azure.com", "apim.audience", builtIn)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected an unapproved audience to fail closed, got %v", err)
	}
	approved := mustApprovals(t, Options{Audiences: []string{"api://orders-api"}})
	if err := approved.RequireAudience("api://Orders-API/", "apim.audience", builtIn); err != nil {
		t.Fatalf("expected an approved custom audience to pass, got %v", err)
	}
	if err := approved.RequireAudience("api://other-api", "apim.audience", builtIn); err == nil {
		t.Fatal("an approval for one audience must not approve another")
	}
}

func TestDefaultScopeAudiencesAreRejected(t *testing.T) {
	approvals := mustApprovals(t, Options{})
	err := approvals.RequireAudience(
		"https://cognitiveservices.azure.com/.default",
		"apim.audience",
		[]string{"https://cognitiveservices.azure.com"},
	)
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected /.default scope rejection, got %v", err)
	}
	if _, err := New(Options{Audiences: []string{"https://cognitiveservices.azure.com/.default"}}); err == nil {
		t.Fatal("expected /.default approvals to be rejected")
	}
}

func TestAudienceWithCredentialsIsRejected(t *testing.T) {
	approvals := mustApprovals(t, Options{})
	err := approvals.RequireAudience("https://user:pass@contoso.com", "apim.audience", nil)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected embedded-credential audience rejection, got %v", err)
	}
}

func TestSplitListAcceptsCIFriendlySeparators(t *testing.T) {
	got := SplitList(" a.example.com, b.example.com;c.example.com\n d.example.com ")
	want := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com"}
	if len(got) != len(want) {
		t.Fatalf("unexpected split: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected split: %#v", got)
		}
	}
	if len(SplitList("")) != 0 {
		t.Fatal("an empty environment variable must produce no approvals")
	}
}

func TestEmptyApprovalsApproveNothing(t *testing.T) {
	approvals := mustApprovals(t, Options{})
	if err := approvals.RequireAPIMHost("https://contoso.azure-api.net", "apim.target"); err == nil {
		t.Fatal("no approvals must mean no trusted destinations")
	}
	if err := approvals.RequireToolHost("https://api.contoso.com", "tool"); err == nil {
		t.Fatal("no approvals must mean no trusted destinations")
	}
	if approvals.ApprovedHostCount() != 0 {
		t.Fatal("unexpected approved host count")
	}
}

func TestAPIMAndToolApprovalsAreSeparate(t *testing.T) {
	approvals := mustApprovals(t, Options{APIMHosts: []string{"contoso.azure-api.net"}})
	if err := approvals.RequireToolHost("https://contoso.azure-api.net/orders", "tool"); err == nil {
		t.Fatal("an APIM approval must not approve an external tool destination")
	}
}
