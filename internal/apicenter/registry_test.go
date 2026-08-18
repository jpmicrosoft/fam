package apicenter

import (
	"reflect"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func TestRegistryURLValidation(t *testing.T) {
	got, err := RegistryURL("https://catalog.data.eastus.azure-apicenter.ms")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://catalog.data.eastus.azure-apicenter.ms" + RegistryPath
	if got != want {
		t.Fatalf("RegistryURL() = %q, want %q", got, want)
	}
	for _, invalid := range []string{
		"http://catalog.data.eastus.azure-apicenter.ms",
		"https://example.com",
		"https://catalog.data.eastus.azure-apicenter.ms/other",
		"https://catalog.data.eastus.azure-apicenter.ms?token=value",
	} {
		if _, err := RegistryURL(invalid); err == nil {
			t.Fatalf("RegistryURL(%q) succeeded", invalid)
		}
	}
}

func TestFilterAndFindRegistryPayload(t *testing.T) {
	payload := map[string]interface{}{
		"servers": []interface{}{
			map[string]interface{}{"name": "operations", "description": "Order tools"},
			map[string]interface{}{"name": "support", "description": "Ticket tools"},
		},
		"nextLink": nil,
	}
	filtered, matches := FilterPayload(payload, "ticket")
	if matches != 1 {
		t.Fatalf("matches = %d, want 1", matches)
	}
	want := []map[string]interface{}{{"name": "support", "description": "Ticket tools"}}
	if got := FindNamedRecords(filtered, "support"); !reflect.DeepEqual(got, want) {
		t.Fatalf("FindNamedRecords() = %#v, want %#v", got, want)
	}
	if got := FindNamedRecords(payload, "missing"); len(got) != 0 {
		t.Fatalf("unexpected missing record result: %#v", got)
	}
}

func TestRegistryURLRejectsUnsafeHostAsSecurityError(t *testing.T) {
	_, err := RegistryURL("https://attacker.example")
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security error, got %v", err)
	}
}
