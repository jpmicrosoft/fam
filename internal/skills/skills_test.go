package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"

	"gopkg.in/yaml.v3"
)

const testSkill = "---\nname: docs\ndescription: Summarize documents.\n---\n# Instructions\nRead and summarize.\n"

func TestValidateReference(t *testing.T) {
	for _, name := range []string{"a", "0", "docs-v2", strings.Repeat("a", 64)} {
		for _, version := range []string{"1", "v2", "2026-09-30", "revision_abc.1"} {
			if err := ValidateReference(Reference{Name: name, Version: version}); err != nil {
				t.Fatalf("valid reference %q/%q rejected: %v", name, version, err)
			}
		}
	}
	for _, name := range []string{
		"", strings.Repeat("a", 65), "-docs", "docs-", "two--words",
		"Docs", "two_words", "two words", "../docs", "caf\u00e9", "docs\x00",
	} {
		t.Run("name="+name, func(t *testing.T) {
			if err := ValidateReference(Reference{Name: name, Version: "1"}); !errs.IsKind(err, "config") {
				t.Fatalf("invalid name must fail with config error: %v", err)
			}
		})
	}
	for _, version := range []string{
		"", " ", " 1", "1 ", "1\n", "\u00a01", ".", "..", "latest", "LATEST", "default",
		"1/2", `1\2`, "c:1", "1?x", "1#x", "%31", "*", "1\x00", string([]byte{0xff}),
	} {
		t.Run("version="+version, func(t *testing.T) {
			if err := ValidateReference(Reference{Name: "docs", Version: version}); !errs.IsKind(err, "config") {
				t.Fatalf("invalid version must fail with config error: %v", err)
			}
		})
	}
}

func TestReferenceSerialization(t *testing.T) {
	reference := Reference{Name: "docs", Version: "1"}
	data, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"name":"docs","version":"1"}` {
		t.Fatalf("unexpected reference JSON: %s", data)
	}
	data, err = yaml.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "name: docs\nversion: \"1\"\n" {
		t.Fatalf("unexpected reference YAML: %s", data)
	}
}

func TestParsePreservesContentAndProvenance(t *testing.T) {
	for _, text := range []string{testSkill, strings.ReplaceAll(testSkill, "\n", "\r\n")} {
		content := []byte(text)
		pkg, err := Parse(content)
		if err != nil {
			t.Fatal(err)
		}
		body := text[strings.Index(text, "# Instructions"):]
		sum := sha256.Sum256(content)
		if pkg.Name != "docs" || pkg.Description != "Summarize documents." ||
			pkg.Instructions != body || !bytes.Equal(pkg.Content, content) ||
			pkg.SHA256 != hex.EncodeToString(sum[:]) {
			t.Fatalf("incorrect parsed package: %#v", pkg)
		}
		content[0] = 'x'
		if string(pkg.Content) != text {
			t.Fatal("package content must not alias its caller's buffer")
		}
	}
}

func TestParseDocumentedBoundaries(t *testing.T) {
	for _, name := range []string{"a", strings.Repeat("a", 64), "123", "true", "null"} {
		text := strings.Replace(testSkill, "name: docs", "name: "+name, 1)
		pkg, err := Parse([]byte(text))
		if err != nil || pkg.Name != name {
			t.Fatalf("valid unquoted name %q rejected: %v", name, err)
		}
	}
	for _, character := range []string{"x", "\u00e9", "\U0001f680"} {
		for _, count := range []int{1024, 1025} {
			description := strings.Repeat(character, count)
			text := strings.Replace(testSkill, "Summarize documents.", description, 1)
			pkg, err := Parse([]byte(text))
			if count == 1024 {
				if err != nil || pkg.Description != description {
					t.Fatalf("%d-character description must pass: %v", count, err)
				}
			} else if !errs.IsKind(err, "config") {
				t.Fatalf("%d-character description must fail: %v", count, err)
			}
		}
	}
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]string{
		"empty":               "",
		"no frontmatter":      "# Instructions",
		"leading text":        "text\n" + testSkill,
		"missing close":       "---\nname: docs\ndescription: Summarize.",
		"empty frontmatter":   "---\n---\nInstructions",
		"sequence":            "---\n- docs\n---\nInstructions",
		"malformed YAML":      "---\nname: [\n---\nInstructions",
		"missing name":        "---\ndescription: Summarize.\n---\nInstructions",
		"missing description": "---\nname: docs\n---\nInstructions",
		"empty body":          "---\nname: docs\ndescription: Summarize.\n---",
		"whitespace body":     "---\nname: docs\ndescription: Summarize.\n---\n \t\r\n",
		"duplicate name":      strings.Replace(testSkill, "name: docs", "name: docs\nname: docs", 1),
		"duplicate nested":    strings.Replace(testSkill, "name: docs", "name: docs\nmetadata:\n  author: a\n  author: b", 1),
		"unknown field":       strings.Replace(testSkill, "name: docs", "name: docs\nscripts: run.py", 1),
		"quoted name":         strings.Replace(testSkill, "name: docs", "name: 'docs'", 1),
		"quoted description":  strings.Replace(testSkill, "Summarize documents.", `"Summarize documents."`, 1),
		"block description":   strings.Replace(testSkill, "Summarize documents.", "|\n  Summarize documents.", 1),
		"tagged name":         strings.Replace(testSkill, "name: docs", "name: !!str docs", 1),
		"empty description":   strings.Replace(testSkill, "Summarize documents.", "", 1),
		"mapping name":        strings.Replace(testSkill, "name: docs", "name: {value: docs}", 1),
		"anchor":              strings.Replace(testSkill, "name: docs", "name: &skill docs", 1),
		"alias":               strings.Replace(testSkill, "name: docs", "name: *skill", 1),
		"merge":               strings.Replace(testSkill, "name: docs", "name: docs\n<<: {license: MIT}", 1),
		"nonstring key":       strings.Replace(testSkill, "name: docs", "name: docs\n1: value", 1),
		"metadata type":       strings.Replace(testSkill, "name: docs", "name: docs\nmetadata: []", 1),
		"metadata value":      strings.Replace(testSkill, "name: docs", "name: docs\nmetadata: {count: 1}", 1),
		"long name":           strings.Replace(testSkill, "name: docs", "name: "+strings.Repeat("a", 65), 1),
		"uppercase name":      strings.Replace(testSkill, "name: docs", "name: Docs", 1),
		"edge hyphen":         strings.Replace(testSkill, "name: docs", "name: docs-", 1),
		"consecutive hyphen":  strings.Replace(testSkill, "name: docs", "name: two--words", 1),
		"invalid UTF8":        testSkill + string([]byte{0xff}),
		"NUL body":            testSkill + "\x00",
		"after YAML end":      strings.Replace(testSkill, "description: Summarize documents.", "description: Summarize documents.\n...\nname: ignored", 1),
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			pkg, err := Parse([]byte(content))
			if !errs.IsKind(err, "config") || pkg.Content != nil {
				t.Fatalf("invalid document returned a package or wrong error: %#v, %v", pkg, err)
			}
		})
	}
}

func TestParsePreservesInertMetadata(t *testing.T) {
	content := strings.Replace(testSkill, "name: docs", `name: docs
license: MIT
compatibility: An offline agent.
metadata:
  author: example
  revision: "1"
allowed-tools: Bash Read`, 1) + "\n[Reference](https://example.invalid/private)\n```sh\nprintf example\n```\n"
	pkg, err := Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if string(pkg.Content) != content || !strings.Contains(pkg.Instructions, "https://example.invalid/private") {
		t.Fatal("metadata and Markdown must be preserved, not evaluated or rewritten")
	}
}
