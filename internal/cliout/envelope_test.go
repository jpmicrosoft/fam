package cliout

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParseFormatAcceptsDocumentedValuesOnly(t *testing.T) {
	accepted := map[string]Format{
		"":      Text,
		"text":  Text,
		" TEXT": Text,
		"json":  JSON,
		"JSON":  JSON,
		"yaml":  YAML,
		"yml":   YAML,
		" Yaml": YAML,
	}
	for value, want := range accepted {
		got, err := ParseFormat(value)
		if err != nil || got != want {
			t.Fatalf("ParseFormat(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"xml", "table", "j son", "yamll", "0"} {
		if _, err := ParseFormat(value); err == nil {
			t.Fatalf("ParseFormat(%q) must be rejected", value)
		}
	}
}

func TestPrintErrorEnvelopeIsStableInEveryFormat(t *testing.T) {
	t.Run("json", func(t *testing.T) {
		var buffer bytes.Buffer
		printer := Printer{Format: JSON, Out: &buffer, Quiet: true}
		if err := printer.PrintError("security", "host is not approved", 4); err != nil {
			t.Fatal(err)
		}
		var envelope ErrorEnvelope
		if err := json.Unmarshal(buffer.Bytes(), &envelope); err != nil {
			t.Fatalf("invalid JSON envelope %q: %v", buffer.String(), err)
		}
		if envelope.Error.Kind != "security" ||
			envelope.Error.Message != "host is not approved" ||
			envelope.Error.ExitCode != 4 {
			t.Fatalf("unexpected envelope: %#v", envelope)
		}
	})
	t.Run("yaml", func(t *testing.T) {
		var buffer bytes.Buffer
		printer := Printer{Format: YAML, Out: &buffer, Quiet: true}
		if err := printer.PrintError("manifest", "bad manifest", 2); err != nil {
			t.Fatal(err)
		}
		var envelope ErrorEnvelope
		if err := yaml.Unmarshal(buffer.Bytes(), &envelope); err != nil {
			t.Fatalf("invalid YAML envelope %q: %v", buffer.String(), err)
		}
		if envelope.Error.Kind != "manifest" || envelope.Error.ExitCode != 2 {
			t.Fatalf("unexpected envelope: %#v", envelope)
		}
	})
	t.Run("text", func(t *testing.T) {
		var buffer bytes.Buffer
		printer := Printer{Format: Text, Out: &buffer, Quiet: true}
		if err := printer.PrintError("config", "--manifest is required", 3); err != nil {
			t.Fatal(err)
		}
		if got := buffer.String(); got != "error: --manifest is required\n" {
			t.Fatalf("quiet must not suppress an error: %q", got)
		}
	})
}

func TestPrintErrorEnvelopeIncludesOptionalNextSteps(t *testing.T) {
	var out bytes.Buffer
	printer := Printer{Format: JSON, Out: &out}
	if err := printer.PrintError(
		"auth",
		"credential unavailable",
		5,
		"Authenticate to AzureCloud.",
		"Verify the tenant.",
	); err != nil {
		t.Fatal(err)
	}
	var envelope ErrorEnvelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Error.NextSteps) != 2 ||
		envelope.Error.NextSteps[0] != "Authenticate to AzureCloud." {
		t.Fatalf("unexpected next steps: %#v", envelope.Error.NextSteps)
	}
}

func TestJSONOutputDoesNotEscapeHTMLOrLoseNewlines(t *testing.T) {
	var buffer bytes.Buffer
	printer := Printer{Format: JSON, Out: &buffer}
	value := map[string]string{"target": "https://contoso.azure-api.net/agents?a=1&b=2"}
	if err := printer.Print(value, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buffer.String(), "a=1&b=2") {
		t.Fatalf("HTML escaping corrupted the value: %q", buffer.String())
	}
	if !strings.HasSuffix(buffer.String(), "\n") {
		t.Fatalf("JSON output must end with a newline: %q", buffer.String())
	}
}

func TestTextOutputAlwaysEndsWithASingleNewline(t *testing.T) {
	for _, text := range []string{"agent ready", "agent ready\n"} {
		var buffer bytes.Buffer
		if err := (Printer{Format: Text, Out: &buffer}).Print(nil, text); err != nil {
			t.Fatal(err)
		}
		if buffer.String() != "agent ready\n" {
			t.Fatalf("unexpected text output: %q", buffer.String())
		}
	}
	var empty bytes.Buffer
	if err := (Printer{Format: Text, Out: &empty}).Print(nil, ""); err != nil {
		t.Fatal(err)
	}
	if empty.Len() != 0 {
		t.Fatalf("an empty text result must print nothing: %q", empty.String())
	}
}

func TestPrintWithoutWriterIsSafe(t *testing.T) {
	if err := (Printer{Format: JSON}).Print(map[string]bool{"ok": true}, "ok"); err != nil {
		t.Fatalf("a nil writer must not fail: %v", err)
	}
}

func TestUnsupportedFormatIsReported(t *testing.T) {
	var buffer bytes.Buffer
	if err := (Printer{Format: Format("xml"), Out: &buffer}).Print(nil, "x"); err == nil {
		t.Fatal("an unsupported format must be reported")
	}
}
