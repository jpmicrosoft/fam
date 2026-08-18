// Package redact removes known credential values from diagnostic text.
//
// Secrets reach diagnostics through several paths: ARM error bodies, wrapped
// transport errors, and deployment receipts. Redacting at each call site is
// easy to forget, so callers register the credential once and route every
// operator-visible string through these helpers.
package redact

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
)

// Placeholder replaces every occurrence of a known credential.
const Placeholder = "<redacted>"

// MinLength is the shortest value worth replacing. Shorter values would mangle
// unrelated diagnostics without protecting a meaningful credential.
const MinLength = 4

// Text replaces every known secret, and its common encodings, in a string.
func Text(text string, secrets ...string) string {
	if text == "" {
		return text
	}
	for _, secret := range secrets {
		for _, variant := range variants(secret) {
			text = strings.ReplaceAll(text, variant, Placeholder)
		}
	}
	return text
}

// Bytes replaces every known secret, and its common encodings, in a byte slice.
func Bytes(data []byte, secrets ...string) []byte {
	if len(data) == 0 {
		return data
	}
	for _, secret := range secrets {
		for _, variant := range variants(secret) {
			data = bytes.ReplaceAll(data, []byte(variant), []byte(Placeholder))
		}
	}
	return data
}

// variants returns the encodings a credential can take in Azure diagnostics.
func variants(secret string) []string {
	if len(secret) < MinLength {
		return nil
	}
	seen := map[string]bool{}
	var result []string
	add := func(value string) {
		if value == "" || value == Placeholder || seen[value] {
			return
		}
		seen[value] = true
		result = append(result, value)
	}
	add(secret)
	if encoded, err := json.Marshal(secret); err == nil && len(encoded) > 2 {
		add(string(encoded[1 : len(encoded)-1]))
	}
	add(url.QueryEscape(secret))
	add(url.PathEscape(secret))
	return result
}
