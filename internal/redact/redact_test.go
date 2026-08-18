package redact

import (
	"strings"
	"testing"
)

func TestTextRedactsRawSecret(t *testing.T) {
	secret := "sub-key-abc123"
	got := Text("ARM rejected key="+secret, secret)
	if strings.Contains(got, secret) {
		t.Fatalf("secret survived redaction: %s", got)
	}
	if !strings.Contains(got, Placeholder) {
		t.Fatalf("placeholder missing: %s", got)
	}
}

func TestTextRedactsJSONEscapedSecret(t *testing.T) {
	secret := `key"with\quotes`
	body := `{"credentials":{"key":"key\"with\\quotes"}}`
	got := Text(body, secret)
	if strings.Contains(got, `key\"with\\quotes`) {
		t.Fatalf("JSON-escaped secret survived redaction: %s", got)
	}
}

func TestTextRedactsPercentEncodedSecret(t *testing.T) {
	secret := "abc def+ghi"
	got := Text("target=abc+def%2Bghi", secret)
	if strings.Contains(got, "abc+def%2Bghi") {
		t.Fatalf("query-encoded secret survived redaction: %s", got)
	}
}

func TestShortValuesAreNotRedacted(t *testing.T) {
	if got := Text("a short value", "a"); got != "a short value" {
		t.Fatalf("short secrets must not mangle diagnostics: %s", got)
	}
}

func TestBytesRedacts(t *testing.T) {
	got := Bytes([]byte(`{"key":"secret-value"}`), "secret-value")
	if strings.Contains(string(got), "secret-value") {
		t.Fatalf("secret survived redaction: %s", got)
	}
}

func TestEmptyInputsAreSafe(t *testing.T) {
	if Text("", "secret-value") != "" {
		t.Fatal("empty text must stay empty")
	}
	if len(Bytes(nil, "secret-value")) != 0 {
		t.Fatal("empty data must stay empty")
	}
	if got := Text("unchanged", ""); got != "unchanged" {
		t.Fatalf("empty secret must not change text: %s", got)
	}
}
