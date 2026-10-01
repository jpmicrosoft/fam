// Package hostedskills validates local Hosted Skill declarations and explicitly
// synchronizes instructions-only artifacts. It never publishes Azure resources
// or asserts that an application has registered a runtime provider.
package hostedskills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/skills"
	"foundry-agent-manager/schema"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	APIVersion       = "foundry-agent-manager/skills/v1"
	FileName         = "fam.skills.yaml"
	DirectoryName    = "fam_skills"
	ManifestFileName = "manifest.json"
	ModeBundle       = "bundle"
	ModeMCP          = "mcp"
)

var (
	servicePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	toolboxPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	imagePattern   = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)
)

type Document struct {
	APIVersion string            `json:"apiVersion" yaml:"apiVersion"`
	Services   map[string]Config `json:"services" yaml:"services"`
}

type Config struct {
	Mode     string         `json:"mode" yaml:"mode"`
	Language string         `json:"language" yaml:"language"`
	Skills   []Source       `json:"skills" yaml:"skills"`
	Toolbox  *Toolbox       `json:"toolbox,omitempty" yaml:"toolbox,omitempty"`
	Image    *ImageEvidence `json:"image,omitempty" yaml:"image,omitempty"`
}

// RequiresRemote reports whether synchronization needs Foundry reads. Absent
// declarations, explicit detachment, and all-local bundles require no provider.
func (config *Config) RequiresRemote() bool {
	if config == nil || len(config.Skills) == 0 {
		return false
	}
	if config.Mode == ModeMCP {
		return true
	}
	for _, source := range config.Skills {
		if source.Path == "" {
			return true
		}
	}
	return false
}

type Source struct {
	Name    string `json:"name,omitempty" yaml:"name,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Path    string `json:"path,omitempty" yaml:"path,omitempty"`
}

type Toolbox struct {
	Name    string `json:"name" yaml:"name"`
	Version string `json:"version" yaml:"version"`
}

// ImageEvidence is an operator-provided build/integration record, not runtime
// readiness. IntegrationEvidence is a workspace-relative, nonempty file.
type ImageEvidence struct {
	Reference           string `json:"reference" yaml:"reference"`
	IntegrationEvidence string `json:"integrationEvidence" yaml:"integrationEvidence"`
}

// Load returns nil when the sidecar or the selected service declaration is absent.
// It performs no provider calls and does not require synchronization to have run.
func Load(root, service string) (*Config, error) {
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil, errs.Config("cannot open Hosted Skill workspace: %v", err)
	}
	defer handle.Close()
	info, err := handle.Lstat(FileName)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, errs.Config("cannot inspect %s: %v", FileName, err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Security("%s must be a regular file, not a link", FileName)
	}
	data, err := netcheck.ReadContainedFile(root, FileName, "Hosted Skill declarations")
	if err != nil {
		return nil, err
	}
	return ParseService(data, service)
}

// Parse validates an entire sidecar without filesystem or provider operations.
func Parse(data []byte) (Document, error) {
	return ValidateDocument(data)
}

// ParseService validates the entire sidecar before selecting a service. It
// performs no filesystem or provider operations and returns nil when absent.
func ParseService(data []byte, service string) (*Config, error) {
	document, err := Parse(data)
	if err != nil {
		return nil, err
	}
	config, found := document.Services[service]
	if !found {
		return nil, nil
	}
	return &config, nil
}

// ValidateDocument validates proposed sidecar bytes without reading local Skill
// sources or contacting providers. CLI editors can call it before atomically
// writing their comment-preserving YAML. It rejects duplicate keys, multiple
// documents, unknown fields, and invalid declarations in every service.
func ValidateDocument(data []byte) (Document, error) {
	var document Document
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return Document{}, errs.Manifest("%s is invalid: %v", FileName, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Document{}, errs.Manifest("%s must contain exactly one YAML document", FileName)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Document{}, errs.Manifest("%s is invalid: %v", FileName, err)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return Document{}, errs.Manifest("%s must use JSON-compatible values: %v", FileName, err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(encoded))
	if err != nil {
		return Document{}, errs.Manifest("cannot normalize %s: %v", FileName, err)
	}
	resource, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema.SkillsBytes()))
	if err != nil {
		return Document{}, fmt.Errorf("invalid embedded Hosted Skills schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("skills.schema.json", resource); err != nil {
		return Document{}, err
	}
	compiled, err := compiler.Compile("skills.schema.json")
	if err != nil {
		return Document{}, err
	}
	if err := compiled.Validate(instance); err != nil {
		return Document{}, errs.Manifest("%s failed schema validation: %v", FileName, err)
	}
	for name, config := range document.Services {
		if err := Validate(config); err != nil {
			return Document{}, errs.Manifest("services.%s: %v", name, err)
		}
	}
	return document, nil
}

// Validate also validates programmatically constructed declarations.
func Validate(config Config) error {
	if config.Mode != ModeBundle && config.Mode != ModeMCP {
		return errs.Config("Hosted Skill mode must be bundle or mcp")
	}
	if config.Language != "python" && config.Language != "dotnet" {
		return errs.Config("Hosted Skill language must be python or dotnet")
	}
	if config.Mode == ModeBundle && config.Toolbox != nil {
		return errs.Config("bundle Skills must not declare a Toolbox")
	}
	if config.Mode == ModeMCP && len(config.Skills) != 0 && config.Toolbox == nil {
		return errs.Config("mcp Skills require an explicitly pinned same-project Toolbox")
	}
	if config.Toolbox != nil {
		if !toolboxPattern.MatchString(config.Toolbox.Name) || !pinnedVersion(config.Toolbox.Version) {
			return errs.Config("Hosted Skill Toolbox requires a valid name and immutable version")
		}
	}
	seen := make(map[string]bool)
	for _, source := range config.Skills {
		key := source.Name
		if source.Path != "" {
			if source.Name != "" || source.Version != "" || config.Mode != ModeBundle {
				return errs.Config("a local Skill path is exclusive of name/version and requires bundle mode")
			}
			if err := safeRelative(source.Path); err != nil {
				return err
			}
			key = strings.ToLower(filepath.Clean(filepath.FromSlash(source.Path)))
		} else {
			if err := skills.ValidateReference(skills.Reference{Name: source.Name, Version: source.Version}); err != nil {
				return err
			}
			if !pinnedVersion(source.Version) {
				return errs.Config("Skill %q requires an immutable version, not latest/default", source.Name)
			}
		}
		if seen[key] {
			return errs.Config("duplicate Hosted Skill source %q", key)
		}
		seen[key] = true
	}
	if config.Image != nil {
		if !imagePattern.MatchString(config.Image.Reference) {
			return errs.Config("Hosted Skill image reference must contain an immutable sha256 digest")
		}
		if err := safeRelative(config.Image.IntegrationEvidence); err != nil {
			return err
		}
	}
	return nil
}

func pinnedVersion(version string) bool {
	return versionPattern.MatchString(version) &&
		!strings.EqualFold(version, "latest") && !strings.EqualFold(version, "default")
}

func safeRelative(relative string) error {
	if relative == "" || strings.ContainsAny(relative, ":\\\x00") {
		return errs.Security("Hosted Skill path %q must be a nonempty portable workspace-relative path", relative)
	}
	if err := netcheck.ValidateRelativeFileReference(relative, "Hosted Skill path"); err != nil {
		return err
	}
	for _, part := range strings.Split(relative, "/") {
		if part == "" || part == "." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return errs.Security("Hosted Skill path %q has an unsafe component", relative)
		}
	}
	return nil
}

// DeclarationHash is stable across YAML formatting changes and binds the
// selected service, language, ordered sources, and optional image evidence.
func DeclarationHash(service string, config Config) (string, error) {
	if !servicePattern.MatchString(service) {
		return "", errs.Config("Hosted Skill service name %q is invalid", service)
	}
	if err := Validate(config); err != nil {
		return "", err
	}
	if config.Skills == nil {
		config.Skills = []Source{}
	}
	encoded, err := json.Marshal(struct {
		Service string `json:"service"`
		Config  Config `json:"config"`
	}{service, config})
	if err != nil {
		return "", err
	}
	return digest(encoded), nil
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}
