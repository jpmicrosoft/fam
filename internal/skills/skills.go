// Package skills validates instructions-only Skills for agent integration.
//
// Content and SHA256 describe the exact SKILL.md bytes, not a ZIP container.
// ZIPs must contain only a root SKILL.md, matching FAM's existing package upload
// layout; wrapped directories and supporting entries are not silently stripped.
// Local packages must contain only SKILL.md and their directory name must match
// the frontmatter name. Metadata never grants tools or triggers resource loading.
//
// The 64 MiB file and 256 MiB compressed archive guards mirror FAM's existing
// Skill upload/download safety guards. They are not Azure Skills quotas.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	errs "foundry-agent-manager/internal/errors"

	"gopkg.in/yaml.v3"
)

const (
	maxSkillFileBytes    = 64 << 20
	maxSkillArchiveBytes = 256 << 20
)

type Reference struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

type Package struct {
	Name         string
	Description  string
	Instructions string
	Content      []byte
	SHA256       string
}

func ValidateReference(reference Reference) error {
	if err := validateName(reference.Name); err != nil {
		return err
	}
	version := reference.Version
	if version == "" || version == "." || version == ".." ||
		strings.EqualFold(version, "latest") || strings.EqualFold(version, "default") ||
		!utf8.ValidString(version) || strings.ContainsAny(version, `/\:%?#*`) ||
		strings.ContainsFunc(version, func(r rune) bool {
			return unicode.IsSpace(r) || unicode.IsControl(r)
		}) {
		return errs.Config("skill %q requires an explicit immutable version, not a default, alias, or path", reference.Name)
	}
	return nil
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > 64 || name[0] == '-' ||
		name[len(name)-1] == '-' || strings.Contains(name, "--") {
		return errs.Config("skill name must be 1-64 lowercase ASCII letters or digits, with no leading, trailing, or consecutive hyphens")
	}
	for _, char := range name {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
			return errs.Config("skill name must be 1-64 lowercase ASCII letters or digits, with no leading, trailing, or consecutive hyphens")
		}
	}
	return nil
}

// Parse validates documented unquoted name/description frontmatter without
// interpreting or executing the Markdown body. Instructions preserve body bytes.
func Parse(content []byte) (Package, error) {
	if len(content) > maxSkillFileBytes {
		return Package{}, errs.Config("SKILL.md exceeds the %d byte FAM file safety guard (not an Azure quota)", maxSkillFileBytes)
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return Package{}, errs.Config("SKILL.md must be UTF-8 text without NUL bytes")
	}
	header, body, err := splitFrontmatter(content)
	if err != nil {
		return Package{}, err
	}
	var document yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(header))
	if err := decoder.Decode(&document); err != nil {
		return Package{}, errs.Config("SKILL.md has malformed YAML frontmatter")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return Package{}, errs.Config("SKILL.md frontmatter must contain exactly one YAML document")
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Package{}, errs.Config("SKILL.md frontmatter must be a YAML mapping")
	}
	fields, err := frontmatterFields(document.Content[0])
	if err != nil {
		return Package{}, err
	}
	name, err := requiredPlainText(fields, "name")
	if err != nil {
		return Package{}, err
	}
	if err := validateName(name); err != nil {
		return Package{}, err
	}
	description, err := requiredPlainText(fields, "description")
	if err != nil {
		return Package{}, err
	}
	if utf8.RuneCountInString(description) > 1024 {
		return Package{}, errs.Config("SKILL.md description must not exceed 1024 characters")
	}
	if strings.TrimSpace(string(body)) == "" {
		return Package{}, errs.Config("SKILL.md requires a nonempty Markdown instruction body")
	}
	digest := sha256.Sum256(content)
	return Package{
		Name:         name,
		Description:  description,
		Instructions: string(body),
		Content:      bytes.Clone(content),
		SHA256:       hex.EncodeToString(digest[:]),
	}, nil
}

func splitFrontmatter(content []byte) ([]byte, []byte, error) {
	line, rest, found := bytes.Cut(content, []byte("\n"))
	if !found || string(bytes.TrimSuffix(line, []byte("\r"))) != "---" {
		return nil, nil, errs.Config("SKILL.md must start with YAML frontmatter delimited by --- lines")
	}
	start := len(content) - len(rest)
	for offset := start; offset < len(content); {
		line, rest, found = bytes.Cut(content[offset:], []byte("\n"))
		if string(bytes.TrimSuffix(line, []byte("\r"))) == "---" {
			return content[start:offset], rest, nil
		}
		if !found {
			break
		}
		offset = len(content) - len(rest)
	}
	return nil, nil, errs.Config("SKILL.md is missing the closing --- frontmatter delimiter")
}

func frontmatterFields(node *yaml.Node) (map[string]*yaml.Node, error) {
	if err := validateYAML(node); err != nil {
		return nil, err
	}
	fields := make(map[string]*yaml.Node)
	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "name", "description":
		case "license", "compatibility", "allowed-tools":
			if value.Kind != yaml.ScalarNode || value.Tag != "!!str" {
				return nil, errs.Config("SKILL.md %s must be text", key)
			}
		case "metadata":
			if value.Kind != yaml.MappingNode {
				return nil, errs.Config("SKILL.md metadata must be a string mapping")
			}
			for _, child := range value.Content {
				if child.Kind != yaml.ScalarNode || child.Tag != "!!str" {
					return nil, errs.Config("SKILL.md metadata must be a string mapping")
				}
			}
		default:
			return nil, errs.Config("SKILL.md frontmatter contains an unsupported field at line %d", node.Content[i].Line)
		}
		fields[key] = value
	}
	return fields, nil
}

func validateYAML(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Tag == "!!merge" {
		return errs.Config("SKILL.md frontmatter must not use YAML anchors, aliases, or merge keys")
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]bool)
		for i := 0; i < len(node.Content); i += 2 {
			key := node.Content[i]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errs.Config("SKILL.md frontmatter keys must be strings")
			}
			if seen[key.Value] {
				return errs.Config("SKILL.md frontmatter contains a duplicate key at line %d", key.Line)
			}
			seen[key.Value] = true
		}
	}
	for _, child := range node.Content {
		if err := validateYAML(child); err != nil {
			return err
		}
	}
	return nil
}

func requiredPlainText(fields map[string]*yaml.Node, key string) (string, error) {
	value := fields[key]
	if value == nil || value.Kind != yaml.ScalarNode || value.Style != 0 ||
		strings.TrimSpace(value.Value) == "" {
		return "", errs.Config("SKILL.md %s is required and must be plain, unquoted YAML text", key)
	}
	// Use the lexeme rather than YAML's implicit scalar type: names such as
	// "123" and "true" still satisfy the documented unquoted name grammar.
	return value.Value, nil
}
