// Package agent365hosted validates local Hosted Agent source for documented
// Microsoft Agent 365 observability integration evidence.
package agent365hosted

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const (
	maxFiles      = 500
	maxFileBytes  = 2 << 20
	maxTotalBytes = 16 << 20
)

// ValidationResult summarizes non-secret local observability evidence.
type ValidationResult struct {
	SourceDirectory       string   `json:"sourceDirectory" yaml:"sourceDirectory"`
	Language              string   `json:"language" yaml:"language"`
	ModernDistro          bool     `json:"modernDistro" yaml:"modernDistro"`
	LegacySDK             bool     `json:"legacySdk" yaml:"legacySdk"`
	PackageDetected       bool     `json:"packageDetected" yaml:"packageDetected"`
	ConfigurationDetected bool     `json:"configurationDetected" yaml:"configurationDetected"`
	Ready                 bool     `json:"ready" yaml:"ready"`
	EvidenceFiles         []string `json:"evidenceFiles,omitempty" yaml:"evidenceFiles,omitempty"`
	Warnings              []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

type signature struct {
	language      string
	modernPackage []string
	legacyPackage []string
	configCalls   []string
}

var signatures = []signature{
	{
		language:      "python",
		modernPackage: []string{"microsoft-opentelemetry"},
		legacyPackage: []string{"microsoft-agents-a365-observability"},
		configCalls: []string{
			"use_microsoft_opentelemetry",
			"observabilitymanager.configure",
		},
	},
	{
		language:      "javascript",
		modernPackage: []string{"@microsoft/opentelemetry"},
		legacyPackage: []string{"@microsoft/agents-a365-observability"},
		configCalls: []string{
			"usemicrosoftopentelemetry",
			"observabilitymanager.configure",
		},
	},
	{
		language:      "dotnet",
		modernPackage: []string{"microsoft.opentelemetry"},
		legacyPackage: []string{"microsoft.agents.a365.observability"},
		configCalls: []string{
			"usemicrosoftopentelemetry",
			"observabilitymanager.configure",
		},
	},
}

// ValidateSource scans bounded regular text files for package and configuration
// evidence. It never evaluates code, resolves dependencies, or reads secrets
// from environment files.
func ValidateSource(sourceDirectory string) (ValidationResult, error) {
	root, err := filepath.Abs(strings.TrimSpace(sourceDirectory))
	if err != nil {
		return ValidationResult{}, errs.Config(
			"failed to resolve Hosted Agent source directory: %v",
			err,
		)
	}
	info, err := os.Stat(root)
	if err != nil {
		return ValidationResult{}, errs.Config(
			"failed to inspect Hosted Agent source directory %q: %v",
			root,
			err,
		)
	}
	if !info.IsDir() {
		return ValidationResult{}, errs.Config(
			"Hosted Agent source path %q is not a directory",
			root,
		)
	}

	result := ValidationResult{SourceDirectory: root}
	matches := make(map[string]map[string]bool)
	for _, item := range signatures {
		matches[item.language] = map[string]bool{}
	}
	totalBytes := int64(0)
	fileCount := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security(
				"Hosted Agent observability scan refuses symbolic link %q",
				filepath.ToSlash(relative),
			)
		}
		if entry.IsDir() {
			if shouldSkipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !isTextCandidate(entry.Name()) {
			return nil
		}
		fileCount++
		if fileCount > maxFiles {
			return errs.Config(
				"Hosted Agent observability scan exceeds the %d file limit",
				maxFiles,
			)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		totalBytes += info.Size()
		if totalBytes > maxTotalBytes {
			return errs.Config(
				"Hosted Agent observability scan exceeds the %d byte limit",
				maxTotalBytes,
			)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(string(data))
		for _, item := range signatures {
			for _, value := range item.modernPackage {
				if strings.Contains(lower, value) {
					matches[item.language]["modern"] = true
					addEvidence(&result, relative)
				}
			}
			for _, value := range item.legacyPackage {
				if strings.Contains(lower, value) {
					matches[item.language]["legacy"] = true
					addEvidence(&result, relative)
				}
			}
			for _, value := range item.configCalls {
				if strings.Contains(lower, value) {
					matches[item.language]["config"] = true
					addEvidence(&result, relative)
				}
			}
		}
		return nil
	})
	if err != nil {
		return ValidationResult{}, errs.Config(
			"failed to inspect Hosted Agent observability source: %v",
			err,
		)
	}

	selected := selectLanguage(matches)
	if selected != "" {
		result.Language = selected
		result.ModernDistro = matches[selected]["modern"]
		result.LegacySDK = matches[selected]["legacy"]
		result.PackageDetected = result.ModernDistro || result.LegacySDK
		result.ConfigurationDetected = matches[selected]["config"]
	}
	result.Ready = result.PackageDetected && result.ConfigurationDetected
	switch {
	case result.Ready && result.LegacySDK && !result.ModernDistro:
		result.Warnings = append(
			result.Warnings,
			"legacy Agent 365 observability SDK evidence is present; Microsoft now recommends Microsoft OpenTelemetry Distro",
		)
	case !result.PackageDetected:
		result.Warnings = append(
			result.Warnings,
			"no Microsoft OpenTelemetry Distro or legacy Agent 365 observability package was detected",
		)
	case !result.ConfigurationDetected:
		result.Warnings = append(
			result.Warnings,
			"an observability package is present but no documented configuration call was detected",
		)
	}
	sort.Strings(result.EvidenceFiles)
	return result, nil
}

func selectLanguage(matches map[string]map[string]bool) string {
	type candidate struct {
		name  string
		score int
	}
	candidates := make([]candidate, 0, len(matches))
	for name, values := range matches {
		score := 0
		for _, value := range values {
			if value {
				score++
			}
		}
		candidates = append(candidates, candidate{name: name, score: score})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score == candidates[j].score {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].score > candidates[j].score
	})
	if len(candidates) == 0 || candidates[0].score == 0 {
		return ""
	}
	return candidates[0].name
}

func addEvidence(result *ValidationResult, relative string) {
	relative = filepath.ToSlash(relative)
	for _, existing := range result.EvidenceFiles {
		if existing == relative {
			return
		}
	}
	result.EvidenceFiles = append(result.EvidenceFiles, relative)
}

func shouldSkipDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".azure", ".foundry", ".venv", "venv", "node_modules",
		"bin", "obj", "dist", "build", "__pycache__":
		return true
	default:
		return false
	}
}

func isTextCandidate(name string) bool {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, ".env") ||
		lower == "a365.generated.config.json" {
		return false
	}
	switch filepath.Ext(lower) {
	case ".py", ".toml", ".txt", ".json", ".js", ".mjs", ".cjs", ".ts",
		".tsx", ".cs", ".csproj", ".props", ".targets", ".xml":
		return true
	default:
		switch lower {
		case "requirements", "requirements.txt", "package.json", "pyproject.toml":
			return true
		default:
			return false
		}
	}
}

// Summary returns a stable compact validation description.
func (r ValidationResult) Summary() string {
	return fmt.Sprintf(
		"language=%s package=%t configured=%t modern=%t ready=%t",
		empty(r.Language),
		r.PackageDetected,
		r.ConfigurationDetected,
		r.ModernDistro,
		r.Ready,
	)
}

func empty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}
