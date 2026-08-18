package foundryid

import (
	"strings"
	"testing"
)

func TestParseProjectID_Valid(t *testing.T) {
	id := "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/my-rg/providers/Microsoft.CognitiveServices/accounts/my-account/projects/my-project"
	p, err := ParseProjectID(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.SubscriptionID != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("subscription = %q", p.SubscriptionID)
	}
	if p.ResourceGroup != "my-rg" {
		t.Errorf("rg = %q", p.ResourceGroup)
	}
	if p.AccountName != "my-account" {
		t.Errorf("account = %q", p.AccountName)
	}
	if p.ProjectName != "my-project" {
		t.Errorf("project = %q", p.ProjectName)
	}
}

func TestParseProjectID_CaseInsensitiveKeywords(t *testing.T) {
	id := "/Subscriptions/00000000-1111-2222-3333-444444444444/ResourceGroups/RG/Providers/microsoft.cognitiveservices/Accounts/acct/Projects/proj"
	p, err := ParseProjectID(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.SubscriptionID != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("subscription = %q", p.SubscriptionID)
	}
	if p.AccountName != "acct" {
		t.Errorf("account = %q", p.AccountName)
	}
}

func TestParseProjectID_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", "must not be empty"},
		{"no leading slash", "subscriptions/x", "must start with /"},
		{"bad uuid", "/subscriptions/not-a-uuid/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/p", "not a valid UUID"},
		{"wrong provider", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.Storage/accounts/a/projects/p", "Microsoft.CognitiveServices"},
		{"extra segments", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/p/extra", "exactly 10"},
		{"account resource ID", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a", "append /projects/<project>"},
		{"empty project", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/", "must not be empty"},
		{"control char", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/p\x01", "control characters"},
		{"dot segment", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/../providers/Microsoft.CognitiveServices/accounts/a/projects/p", "must not be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProjectID(tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseAccountID_Valid(t *testing.T) {
	id := "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct"
	a, err := ParseAccountID(id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a.SubscriptionID != "00000000-1111-2222-3333-444444444444" {
		t.Errorf("sub = %q", a.SubscriptionID)
	}
	if a.AccountName != "acct" {
		t.Errorf("acct = %q", a.AccountName)
	}
}

func TestParseAccountID_Invalid(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"extra segment (project)", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/p", "exactly 8"},
		{"wrong type", "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.Storage/accounts/a", "Microsoft.CognitiveServices"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseAccountID(tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseRAIPolicyID(t *testing.T) {
	raw := "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/raiPolicies/Microsoft.DefaultV2"
	policy, err := ParseRAIPolicyID(raw)
	if err != nil {
		t.Fatal(err)
	}
	if policy.PolicyName != "Microsoft.DefaultV2" || policy.String() != raw {
		t.Fatalf("unexpected policy: %#v", policy)
	}
	account, err := ParseAccountID(
		"/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.SameAccount(account) {
		t.Fatal("policy account did not match")
	}
}

func TestParseRAIPolicyIDRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: "Microsoft.DefaultV2", want: "must start with /"},
		{
			value: "/subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/acct/projects/project",
			want:  "raiPolicies",
		},
	} {
		if _, err := ParseRAIPolicyID(test.value); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("ParseRAIPolicyID(%q) error = %v, want %q", test.value, err, test.want)
		}
	}
}

func TestProjectID_Derivation(t *testing.T) {
	p := ProjectID{
		SubscriptionID: "00000000-1111-2222-3333-444444444444",
		ResourceGroup:  "rg",
		AccountName:    "myaccount",
		ProjectName:    "my project",
	}
	if got := p.AccountEndpoint(); got != "https://myaccount.services.ai.azure.com" {
		t.Errorf("AccountEndpoint = %q", got)
	}
	if got := p.ProjectEndpoint(); got != "https://myaccount.services.ai.azure.com/api/projects/my%20project" {
		t.Errorf("ProjectEndpoint = %q", got)
	}
	if got := p.AccountResourceID(); !strings.Contains(got, "/accounts/myaccount") {
		t.Errorf("AccountResourceID = %q", got)
	}
	if got := p.String(); !strings.Contains(got, "/projects/my project") {
		t.Errorf("String = %q", got)
	}
}

func TestProjectID_URLEscaping(t *testing.T) {
	p := ProjectID{
		SubscriptionID: "00000000-1111-2222-3333-444444444444",
		ResourceGroup:  "rg",
		AccountName:    "acct",
		ProjectName:    "hello/world",
	}
	endpoint := p.ProjectEndpoint()
	if !strings.Contains(endpoint, "hello%2Fworld") {
		t.Errorf("expected URL-escaped slash in project endpoint: %q", endpoint)
	}
}

func TestProjectIDMatchesProjectEndpoint(t *testing.T) {
	project := ProjectID{
		SubscriptionID: "00000000-1111-2222-3333-444444444444",
		ResourceGroup:  "rg",
		AccountName:    "account",
		ProjectName:    "support",
	}
	if !project.MatchesProjectEndpoint("https://ACCOUNT.services.ai.azure.com/api/projects/SUPPORT/") {
		t.Fatal("matching endpoint was rejected")
	}
	for _, endpoint := range []string{
		"https://other.services.ai.azure.com/api/projects/support",
		"https://account.services.ai.azure.com/api/projects/other",
	} {
		if project.MatchesProjectEndpoint(endpoint) {
			t.Fatalf("mismatched endpoint was accepted: %s", endpoint)
		}
	}
}

func TestIsUUID(t *testing.T) {
	if !IsUUID("00000000-1111-2222-3333-444444444444") {
		t.Error("should be valid")
	}
	if IsUUID("not-a-uuid") {
		t.Error("should be invalid")
	}
}
