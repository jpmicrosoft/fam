package netcheck

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

var fuzzSuffixes = []string{"services.ai.azure.com", "cognitiveservices.azure.com"}

// FuzzValidateHTTPSHostAcceptsOnlyAllowedHosts asserts the host-pinning
// invariant for every token-bearing URL: acceptance implies https, no embedded
// credentials, and a host that is exactly an allowed suffix or a subdomain of
// one. Suffix spoofing such as "evil-services.ai.azure.com.attacker.test" must
// never be accepted.
func FuzzValidateHTTPSHostAcceptsOnlyAllowedHosts(f *testing.F) {
	for _, seed := range []string{
		"https://account.services.ai.azure.com",
		"https://account.services.ai.azure.com/api/projects/p",
		"https://services.ai.azure.com",
		"https://SERVICES.AI.AZURE.COM./x",
		"https://services.ai.azure.com.attacker.test/x",
		"https://attackerservices.ai.azure.com/x",
		"https://user:pass@account.services.ai.azure.com/x",
		"http://account.services.ai.azure.com/x",
		"https://169.254.169.254/metadata",
		"https://[::1]/x",
		"file:///etc/passwd",
		"",
		"   ",
		"https://",
		"https://account.services.ai.azure.com:443/x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		validated, err := validateHTTPSHost(rawURL, "project.endpoint", fuzzSuffixes, "")
		if err != nil {
			return
		}
		if validated != rawURL {
			t.Fatalf("validation rewrote %q into %q", rawURL, validated)
		}
		parsed, parseErr := url.Parse(rawURL)
		if parseErr != nil {
			t.Fatalf("accepted an unparsable URL %q", rawURL)
		}
		if parsed.Scheme != "https" {
			t.Fatalf("accepted a non-https URL %q", rawURL)
		}
		if parsed.User != nil {
			t.Fatalf("accepted a credential-bearing URL %q", rawURL)
		}
		host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
		if host == "" {
			t.Fatalf("accepted a URL without a host %q", rawURL)
		}
		allowed := false
		for _, suffix := range fuzzSuffixes {
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				allowed = true
				break
			}
		}
		if !allowed {
			t.Fatalf("accepted the unlisted host %q from %q", host, rawURL)
		}
	})
}

// FuzzAPIMTargetNeverCrossesCloudBoundaries asserts that the AzureCloud APIM
// allow-list can never accept a Government gateway.
func FuzzAPIMTargetNeverCrossesCloudBoundaries(f *testing.F) {
	for _, seed := range []string{
		"https://contoso.azure-api.net/agents/chat",
		"https://contoso.azure-api.us/agents/chat",
		"https://contoso.azure-api.net.azure-api.us/x",
		"https://contoso.AZURE-API.NET./x",
		"https://contoso.azure-api.us:443/x",
		"",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if _, err := ValidateAPIMTargetForSuffixes(rawURL, "apim.target", []string{"azure-api.net"}); err == nil {
			assertHostSuffix(t, rawURL, "azure-api.net", "azure-api.us")
		}
	})
}

func assertHostSuffix(t *testing.T, rawURL, want, forbidden string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("accepted an unparsable APIM target %q", rawURL)
	}
	host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
	if host != want && !strings.HasSuffix(host, "."+want) {
		t.Fatalf("accepted %q which is not under %q", host, want)
	}
	if host == forbidden || strings.HasSuffix(host, "."+forbidden) {
		t.Fatalf("accepted the opposite-cloud host %q", host)
	}
}

// FuzzRelativeFileReferenceStaysInsideTheManifestDirectory asserts that any
// path shape the containment check accepts still resolves inside the manifest
// directory. This is a pure path property, so it stays deterministic and does
// not touch the filesystem.
func FuzzRelativeFileReferenceStaysInsideTheManifestDirectory(f *testing.F) {
	for _, seed := range []string{
		"spec.json",
		"specs/spec.json",
		`specs\spec.json`,
		"./specs/spec.json",
		"../spec.json",
		"specs/../../spec.json",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		`c:spec.json`,
		`\\server\share\spec.json`,
		`\spec.json`,
		"",
		"..",
		"...",
		"spec..json",
	} {
		f.Add(seed)
	}
	base := filepath.Clean(`C:\manifests\project`)
	f.Fuzz(func(t *testing.T, relative string) {
		if err := ValidateRelativeFileReference(relative, "spec_file"); err != nil {
			return
		}
		if filepath.IsAbs(relative) {
			t.Fatalf("accepted the absolute path %q", relative)
		}
		joined := filepath.Clean(filepath.Join(base, relative))
		rel, err := filepath.Rel(base, joined)
		if err != nil {
			t.Fatalf("accepted %q which cannot be made relative to the manifest directory", relative)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("accepted %q which escapes the manifest directory as %q", relative, rel)
		}
		if volume := filepath.VolumeName(joined); volume != filepath.VolumeName(base) {
			t.Fatalf("accepted %q which changes the volume to %q", relative, volume)
		}
	})
}
