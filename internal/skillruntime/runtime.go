// Package skillruntime provides source-contained, instructions-only Hosted
// Skills adapters. Artifact validation alone is not provider readiness.
package skillruntime

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// PythonRequirements is the same pinned dependency manifest used for isolated
// adapter tests. It is additive to an existing app's identity/dotenv requirements.
//
//go:embed testdata/requirements.txt
var PythonRequirements string

//go:embed templates
var runtimeTemplates embed.FS

// ErrProviderUnverified means the requested provider template is not available
// for emission. Callers must not continue with a skills-free agent on this error.
var ErrProviderUnverified = errors.New("Hosted Skills provider compatibility is not verified")

// ErrUnsupportedLanguage means the requested language has no runtime adapter.
var ErrUnsupportedLanguage = errors.New("unsupported Hosted Skills runtime language")

// Files returns portable source-relative helper files under fam_skills for an existing
// application's explicit provider integration. It never replaces an application
// entry point. The Python helper's open_runtime keyword-only arguments are
// manifest_path, project_endpoint and credential. Its synchronous context
// manager must yield one ready provider and retain all resources until exit.
//
// Python uses agent-framework-core 1.19.0 and MCP 1.30.0. MCP sessions use the
// immutable same-project Toolbox endpoint and remain open until context exit.
// Keys use portable slashes, matching hostedskills.SyncOptions.RuntimeFiles.
func Files(language string) (map[string]string, error) {
	var filename string
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "python":
		filename = "fam_skills_runtime.py"
	case "dotnet":
		filename = "FamSkillsRuntime.cs"
	default:
		return nil, fmt.Errorf("%w: use python or dotnet", ErrUnsupportedLanguage)
	}
	content, err := runtimeTemplates.ReadFile("templates/" + filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s has not been supplied", ErrProviderUnverified, filename)
	}
	if err != nil {
		return nil, fmt.Errorf("read embedded Skills runtime %s: %w", filename, err)
	}
	if strings.TrimSpace(string(content)) == "" {
		return nil, fmt.Errorf("%w: %s is empty", ErrProviderUnverified, filename)
	}
	return map[string]string{"fam_skills/" + filename: string(content)}, nil
}
