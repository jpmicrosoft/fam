package arm

import (
	"strings"
	"testing"
)

func TestResourceURLEscapesSegmentsAndQuery(t *testing.T) {
	got, err := ResourceURL(
		"2025-04-01-preview&unexpected=true",
		"subscriptions", "sub",
		"resourceGroups", "rg with space",
		"connections", "conn/name",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "rg%20with%20space") {
		t.Fatalf("resource group was not escaped: %s", got)
	}
	if !strings.Contains(got, "conn%2Fname") {
		t.Fatalf("connection name was not escaped as one segment: %s", got)
	}
	if strings.Contains(got, "&unexpected=true") {
		t.Fatalf("api-version escaped into a separate query parameter: %s", got)
	}
}

func TestValidateNextLinkRequiresARMOrigin(t *testing.T) {
	got, err := ValidateNextLink(
		Endpoint,
		"https://management.azure.com/subscriptions/sub/resources?skipToken=next",
	)
	if err != nil || got == "" {
		t.Fatalf("expected valid ARM nextLink, got %q / %v", got, err)
	}
	for _, raw := range []string{
		"https://evil.example.com/subscriptions/sub/resources",
		"https://user@management.azure.com/subscriptions/sub/resources",
		"https://management.azure.com/subscriptions/sub/resources#fragment",
	} {
		if _, err := ValidateNextLink(Endpoint, raw); err == nil {
			t.Fatalf("expected nextLink rejection for %q", raw)
		}
	}
}
