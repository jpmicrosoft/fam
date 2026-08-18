package cliout

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrinterFormats(t *testing.T) {
	value := map[string]interface{}{"ok": true, "name": "agent"}
	tests := []struct {
		format Format
		want   string
	}{
		{Text, "agent ready\n"},
		{JSON, "\"ok\": true"},
		{YAML, "ok: true"},
	}
	for _, tt := range tests {
		t.Run(string(tt.format), func(t *testing.T) {
			var buffer bytes.Buffer
			printer := Printer{Format: tt.format, Out: &buffer}
			if err := printer.Print(value, "agent ready"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(buffer.String(), tt.want) {
				t.Fatalf("unexpected output: %q", buffer.String())
			}
		})
	}
}

func TestQuietOnlySuppressesText(t *testing.T) {
	var text bytes.Buffer
	if err := (Printer{Format: Text, Out: &text, Quiet: true}).Print(map[string]bool{"ok": true}, "hidden"); err != nil {
		t.Fatal(err)
	}
	if text.Len() != 0 {
		t.Fatalf("quiet text output was not suppressed: %q", text.String())
	}

	var structured bytes.Buffer
	if err := (Printer{Format: JSON, Out: &structured, Quiet: true}).Print(map[string]bool{"ok": true}, "hidden"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(structured.String(), "\"ok\": true") {
		t.Fatalf("structured output was suppressed: %q", structured.String())
	}
}

func TestPrintErrorShowsNextStepsInText(t *testing.T) {
	var buf bytes.Buffer
	p := Printer{Format: Text, Out: &buf}
	if err := p.PrintError("config", "bad flag", 3, "Try --help", "See docs"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "next: Try --help") {
		t.Fatalf("text error missing first next step:\n%s", out)
	}
	if !strings.Contains(out, "next: See docs") {
		t.Fatalf("text error missing second next step:\n%s", out)
	}
}

func TestPrintErrorJSONDoesNotAddTextLabels(t *testing.T) {
	var buf bytes.Buffer
	p := Printer{Format: JSON, Out: &buf}
	if err := p.PrintError("config", "bad flag", 3, "Try --help"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "next:") {
		t.Fatalf("JSON error should not contain text-mode labels:\n%s", out)
	}
	if !strings.Contains(out, "Try --help") {
		t.Fatalf("JSON error missing nextSteps:\n%s", out)
	}
}
