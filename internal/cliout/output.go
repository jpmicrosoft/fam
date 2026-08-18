// Package cliout renders stable command results for humans and automation.
package cliout

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

type Format string

const (
	Text Format = "text"
	JSON Format = "json"
	YAML Format = "yaml"
)

// ParseFormat validates an output format.
func ParseFormat(value string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "text":
		return Text, nil
	case "json":
		return JSON, nil
	case "yaml", "yml":
		return YAML, nil
	default:
		return "", fmt.Errorf("unsupported output format %q; use text, json, or yaml", value)
	}
}

// Printer writes command results to the selected output stream.
type Printer struct {
	Format Format
	Out    io.Writer
	Quiet  bool
}

// Print emits structured data or its human-readable text equivalent.
func (p Printer) Print(value any, text string) error {
	if p.Out == nil {
		p.Out = io.Discard
	}
	switch p.Format {
	case JSON:
		encoder := json.NewEncoder(p.Out)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case YAML:
		data, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		_, err = p.Out.Write(data)
		return err
	case Text:
		if p.Quiet || text == "" {
			return nil
		}
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		_, err := io.WriteString(p.Out, text)
		return err
	default:
		return fmt.Errorf("unsupported output format %q", p.Format)
	}
}

// ErrorEnvelope is the stable machine-readable error contract.
type ErrorEnvelope struct {
	Error ErrorDetail `json:"error" yaml:"error"`
}

type ErrorDetail struct {
	Kind      string   `json:"kind" yaml:"kind"`
	Message   string   `json:"message" yaml:"message"`
	ExitCode  int      `json:"exitCode" yaml:"exitCode"`
	NextSteps []string `json:"nextSteps,omitempty" yaml:"nextSteps,omitempty"`
}

// PrintError emits an error without suppressing it in quiet mode.
// In text mode, actionable next steps are appended so operators can self-serve.
func (p Printer) PrintError(kind, message string, exitCode int, nextSteps ...string) error {
	p.Quiet = false
	textMsg := "error: " + message
	if p.Format == Text && len(nextSteps) > 0 {
		for _, step := range nextSteps {
			textMsg += "\n  next: " + step
		}
	}
	return p.Print(
		ErrorEnvelope{Error: ErrorDetail{
			Kind:      kind,
			Message:   message,
			ExitCode:  exitCode,
			NextSteps: append([]string(nil), nextSteps...),
		}},
		textMsg,
	)
}
