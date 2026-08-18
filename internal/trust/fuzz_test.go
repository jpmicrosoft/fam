package trust

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzHostApprovalNeverOverMatches asserts the core fail-closed property of the
// destination allow-list: an approval may only ever accept the one exact host it
// names. The check re-derives the destination host with a plain url.Parse so a
// bug in the normalization helpers cannot mask itself.
func FuzzHostApprovalNeverOverMatches(f *testing.F) {
	seeds := [][2]string{
		{"contoso.azure-api.net", "https://contoso.azure-api.net/agents/chat"},
		{"contoso.azure-api.net", "https://attacker.azure-api.net/agents/chat"},
		{"contoso.azure-api.net", "https://contoso.azure-api.net.attacker.example/x"},
		{"contoso.azure-api.net", "https://CONTOSO.AZURE-API.NET./x"},
		{"contoso.azure-api.net", "https://contoso.azure-api.net:443/x"},
		{"contoso.azure-api.net", "https://contoso.azure-api.net:8443/x"},
		{"api.contoso.com:8443", "https://api.contoso.com:8443/orders"},
		{"api.contoso.com", "https://api.contoso.com@evil.example/orders"},
		{"api.contoso.com", "http://api.contoso.com/orders"},
		{"api.contoso.com", "https://api.c%6Fntoso.com/orders"},
		{"*.contoso.com", "https://api.contoso.com/orders"},
		{".contoso.com", "https://api.contoso.com/orders"},
		{"api.cöntoso.com", "https://api.cöntoso.com/orders"},
		{"", ""},
		{"   ", "https:///orders"},
		{"api.contoso.com", "///orders"},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, approval, rawURL string) {
		approvals, err := New(Options{ToolHosts: []string{approval}})
		if err != nil {
			return
		}
		if err := approvals.RequireToolHost(rawURL, "tool"); err != nil {
			return
		}
		parsed, parseErr := url.Parse(strings.TrimSpace(rawURL))
		if parseErr != nil {
			t.Fatalf("accepted an unparsable destination %q", rawURL)
		}
		if !strings.EqualFold(parsed.Scheme, "https") {
			t.Fatalf("accepted a non-https destination %q", rawURL)
		}
		if parsed.User != nil {
			t.Fatalf("accepted a credential-bearing destination %q", rawURL)
		}
		host := strings.ToLower(strings.TrimRight(parsed.Hostname(), "."))
		if host == "" {
			t.Fatalf("accepted a destination without a host %q", rawURL)
		}
		if !isASCII(host) {
			t.Fatalf("accepted a non-ASCII destination host %q", rawURL)
		}
		approvedHost, approvedPort := splitApproval(approval)
		if host != approvedHost {
			t.Fatalf("approval %q accepted the different host %q", approval, host)
		}
		port := parsed.Port()
		if port == "443" {
			port = ""
		}
		if port != approvedPort {
			t.Fatalf("approval %q (port %q) accepted port %q", approval, approvedPort, port)
		}
	})
}

// splitApproval derives the intended host and port from an approval value
// independently of the production normalization code.
func splitApproval(approval string) (string, string) {
	value := strings.ToLower(strings.TrimSpace(approval))
	host, port := value, ""
	if index := strings.LastIndex(value, ":"); index >= 0 {
		host, port = value[:index], value[index+1:]
	}
	if port == "443" {
		port = ""
	}
	return strings.TrimRight(host, "."), port
}

// FuzzApprovalParsingRejectsAmbiguousValues asserts that every approval the
// parser accepts is an exact, lower-case, wildcard-free host key.
func FuzzApprovalParsingRejectsAmbiguousValues(f *testing.F) {
	for _, seed := range []string{
		"contoso.azure-api.net",
		"CONTOSO.AZURE-API.NET.",
		"api.contoso.com:8443",
		"*.contoso.com",
		".contoso.com",
		"https://contoso.com",
		"contoso.com/agents",
		"user@contoso.com",
		"contoso.com?a=1",
		"contoso.com#f",
		"contoso.com:notaport",
		"contoso.com ",
		"",
		"::1",
		"[::1]:8443",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		hosts, err := hostSet([]string{value}, FlagToolHost, EnvToolHosts)
		if err != nil {
			return
		}
		for key := range hosts {
			if key == "" {
				t.Fatalf("approval %q produced an empty key", value)
			}
			if strings.ContainsAny(key, "*/@?# \t\r\n") {
				t.Fatalf("approval %q produced the ambiguous key %q", value, key)
			}
			if key != strings.ToLower(key) {
				t.Fatalf("approval %q produced the mixed-case key %q", value, key)
			}
			if strings.HasSuffix(key, ":443") {
				t.Fatalf("approval %q kept the default port in key %q", value, key)
			}
			if !isASCII(key) {
				t.Fatalf("approval %q produced the non-ASCII key %q", value, key)
			}
		}
	})
}

// FuzzAudienceApprovalNeverOverMatches asserts that an audience approval accepts
// only that audience, and never an OAuth scope form.
func FuzzAudienceApprovalNeverOverMatches(f *testing.F) {
	seeds := [][2]string{
		{"api://orders-api", "api://orders-api"},
		{"api://orders-api", "api://orders-api/"},
		{"api://orders-api", "api://Orders-API"},
		{"api://orders-api", "api://orders-api-2"},
		{"api://orders-api", "https://management.azure.com"},
		{"https://cognitiveservices.azure.com", "https://cognitiveservices.azure.com/.default"},
		{"", ""},
	}
	for _, seed := range seeds {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, approval, audience string) {
		approvals, err := New(Options{Audiences: []string{approval}})
		if err != nil {
			return
		}
		if err := approvals.RequireAudience(audience, "apim.audience", nil); err != nil {
			return
		}
		if strings.HasSuffix(strings.ToLower(strings.TrimRight(strings.TrimSpace(audience), "/")), "/.default") {
			t.Fatalf("approval %q accepted the OAuth scope %q", approval, audience)
		}
		approvedKey, approvedErr := normalizeAudience(approval, "approval")
		audienceKey, audienceErr := normalizeAudience(audience, "audience")
		if approvedErr != nil || audienceErr != nil {
			t.Fatalf("accepted audience %q against approval %q despite normalization errors", audience, approval)
		}
		if approvedKey != audienceKey {
			t.Fatalf("approval %q accepted the different audience %q", approval, audience)
		}
	})
}

// FuzzFileHostValidationMatchesFlagHostValidation asserts that a trust policy
// file host entry is accepted if and only if the same value would be accepted
// from --trusted-apim-host/--trusted-tool-host, and normalizes to the same
// comparison key when both accept it. This guards against the file-sourced
// validation path (duplicated for accurate error attribution) silently
// drifting from the flag path and becoming a wildcard or ASCII bypass.
func FuzzFileHostValidationMatchesFlagHostValidation(f *testing.F) {
	for _, seed := range []string{
		"contoso.azure-api.net",
		"CONTOSO.AZURE-API.NET.",
		"api.contoso.com:8443",
		"*.contoso.com",
		".contoso.com",
		"https://contoso.com",
		"contoso.com/agents",
		"user@contoso.com",
		"contoso.com?a=1",
		"contoso.com#f",
		"contoso.com ",
		"",
		"   ",
		"api.cöntoso.com",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		flagHosts, flagErr := hostSet([]string{value}, FlagToolHost, EnvToolHosts)
		fileHosts, fileErr := fileHostSet([]string{value}, "trust.yaml", "toolHosts")
		if (flagErr == nil) != (fileErr == nil) {
			t.Fatalf("value %q: flag path err=%v, file path err=%v (acceptance must match)", value, flagErr, fileErr)
		}
		if flagErr == nil {
			var flagKey, fileKey string
			for key := range flagHosts {
				flagKey = key
			}
			for key := range fileHosts {
				fileKey = key
			}
			if flagKey != fileKey {
				t.Fatalf("value %q: flag path key %q != file path key %q", value, flagKey, fileKey)
			}
		}
	})
}

// FuzzFileAudienceValidationMatchesFlagAudienceValidation is the audience
// equivalent of FuzzFileHostValidationMatchesFlagHostValidation.
func FuzzFileAudienceValidationMatchesFlagAudienceValidation(f *testing.F) {
	for _, seed := range []string{
		"api://orders-api",
		"API://Orders-API/",
		"https://*.azure.com",
		"https://cognitiveservices.azure.com/.default",
		"user@contoso.com",
		"",
		"   ",
		"api://örders-api",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		flagAudiences, flagErr := audienceSet([]string{value})
		fileAudiences, fileErr := fileAudienceSet([]string{value}, "trust.yaml", "audiences")
		if (flagErr == nil) != (fileErr == nil) {
			t.Fatalf("value %q: flag path err=%v, file path err=%v (acceptance must match)", value, flagErr, fileErr)
		}
		if flagErr == nil {
			var flagKey, fileKey string
			for key := range flagAudiences {
				flagKey = key
			}
			for key := range fileAudiences {
				fileKey = key
			}
			if flagKey != fileKey {
				t.Fatalf("value %q: flag path key %q != file path key %q", value, flagKey, fileKey)
			}
		}
	})
}
