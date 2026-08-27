package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type initResult struct {
	Manifest          string `json:"manifest" yaml:"manifest"`
	Cloud             string `json:"cloud" yaml:"cloud"`
	Agent             string `json:"agent" yaml:"agent"`
	Project           string `json:"project" yaml:"project"`
	Tools             bool   `json:"tools" yaml:"tools"`
	GuardrailPolicyID string `json:"guardrailPolicyId,omitempty" yaml:"guardrailPolicyId,omitempty"`
}

// cmdInit writes a schema-valid starter manifest to --manifest. It is
// entirely offline: no credential, network call, or existing manifest is
// required. The generated file is validated against the embedded schema
// before it is kept, so init never leaves behind a manifest that
// `fam prompt validate` would reject.
func cmdInit(cmd *cobra.Command, _ []string) error {
	result, err := createPromptScaffold(cmd)
	if err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"wrote manifest: %s\n  agent: %s (cloud=%s)\n  next: %s -f %s",
		result.Manifest,
		result.Agent,
		result.Cloud,
		canonicalCommandText("validate"),
		result.Manifest,
	))
}

func createPromptScaffold(cmd *cobra.Command) (initResult, error) {
	path := getFlag(cmd, "manifest")
	if path == "" {
		path = getFlag(cmd, "destination")
	}
	if path == "" {
		return initResult{}, errs.Config("--manifest is required")
	}
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return initResult{}, errs.Config("%s is a directory", path)
		}
		if !getBoolFlag(cmd, "force") {
			return initResult{}, errs.Config("%s already exists; rerun with --force to overwrite", path)
		}
	} else if !os.IsNotExist(err) {
		return initResult{}, fmt.Errorf("failed to inspect %s: %w", path, err)
	}

	cloudFlag := getFlag(cmd, "cloud")
	profile, err := azcloud.Resolve(cloudFlag)
	if err != nil {
		return initResult{}, err
	}

	name := getFlag(cmd, "name")
	if name == "" {
		name = "new-agent"
	}
	model := getFlag(cmd, "model")
	if model == "" {
		model = "<model-deployment-name>"
	}
	description := getFlag(cmd, "description")
	if description == "" {
		description = "Describe what this agent does."
	}
	instructions := "Replace these instructions with your agent's system prompt, or regenerate with --instructions-file."
	if instructionsFile := getFlag(cmd, "instructions-file"); instructionsFile != "" {
		data, err := os.ReadFile(instructionsFile)
		if err != nil {
			return initResult{}, fmt.Errorf("failed to read --instructions-file %s: %w", instructionsFile, err)
		}
		text := strings.TrimRight(string(data), "\r\n")
		if strings.TrimSpace(text) == "" {
			return initResult{}, errs.Config("--instructions-file %s is empty", instructionsFile)
		}
		instructions = text
	}
	projectResourceID := getFlag(cmd, "project-resource-id")
	guardrailPolicyID := strings.TrimSpace(getFlag(cmd, "guardrail-policy-id"))
	if guardrailPolicyID != "" {
		policy, parseErr := foundryid.ParseRAIPolicyID(guardrailPolicyID)
		if parseErr != nil {
			return initResult{}, errs.Config("--guardrail-policy-id is invalid: %v", parseErr)
		}
		guardrailPolicyID = policy.String()
		if projectResourceID != "" {
			project, projectErr := foundryid.ParseProjectID(projectResourceID)
			if projectErr != nil {
				return initResult{}, errs.Config(
					"--project-resource-id must be valid when --guardrail-policy-id is supplied: %v",
					projectErr,
				)
			}
			if !policy.SameAccount(project.Account()) {
				return initResult{}, errs.Config(
					"--guardrail-policy-id must reference the same Foundry account as --project-resource-id",
				)
			}
		}
	}
	if projectResourceID == "" {
		projectResourceID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/REPLACE-ME/providers/Microsoft.CognitiveServices/accounts/REPLACE-ME/projects/REPLACE-ME"
	}
	includeTools := !getBoolFlag(cmd, "no-tools")

	var body strings.Builder
	fmt.Fprintf(&body, "# Manifest scaffolded by `%s`. Review every value below\n", canonicalCommandText("init"))
	fmt.Fprintf(&body, "# before deploying, especially agent.model, project.resource_id, and\n")
	fmt.Fprintf(&body, "# agent.instructions.\n")
	fmt.Fprintf(&body, "#\n")
	fmt.Fprintf(&body, "# Next steps:\n")
	fmt.Fprintf(&body, "#   %s -f %s\n", canonicalCommandText("validate"), path)
	fmt.Fprintf(&body, "#   %s -f %s\n", canonicalCommandText("plan"), path)
	fmt.Fprintf(&body, "#   %s -f %s\n", canonicalCommandText("preflight"), path)
	fmt.Fprintf(&body, "#   %s -f %s --if-changed\n", canonicalCommandText("deploy"), path)
	fmt.Fprintf(&body, "#\n")
	fmt.Fprintf(&body, "# Adding an apim connection, or an openapi/mcp tool, requires destination\n")
	fmt.Fprintf(&body, "# approval flags at preflight/deploy time; see %s --help.\n", canonicalCommandText("preflight"))
	fmt.Fprintf(&body, "\n")
	fmt.Fprintf(&body, "apiVersion: foundry-agent-manager/v1\n")
	fmt.Fprintf(&body, "\n")
	if cloudFlag != "" {
		fmt.Fprintf(&body, "cloud: %s\n\n", profile.Name)
	}
	fmt.Fprintf(&body, "agent:\n")
	fmt.Fprintf(&body, "  name: %s\n", yamlScalar(name))
	fmt.Fprintf(&body, "  model: %s\n", yamlScalar(model))
	if guardrailPolicyID != "" {
		fmt.Fprintf(&body, "  rai_policy_id: %s\n", yamlScalar(guardrailPolicyID))
	}
	fmt.Fprintf(&body, "  description: %s\n", yamlScalar(description))
	metadata := commandMetadata(cmd)
	if len(metadata) > 0 {
		fmt.Fprintf(&body, "  metadata:\n")
		keys := make([]string, 0, len(metadata))
		for key := range metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&body, "    %s: %s\n", yamlScalar(key), yamlScalar(metadata[key]))
		}
	}
	fmt.Fprintf(&body, "  instructions: |\n%s\n", yamlBlockLiteral(instructions, 4))
	fmt.Fprintf(&body, "\n")
	fmt.Fprintf(&body, "project:\n")
	fmt.Fprintf(&body, "  resource_id: %s\n", yamlScalar(projectResourceID))
	if location := getFlag(cmd, "location"); location != "" {
		fmt.Fprintf(&body, "  location: %s\n", yamlScalar(location))
	}
	if includeTools {
		fmt.Fprintf(&body, "\ntools:\n  - type: code_interpreter\n")
	}

	if err := writeManifestFile(path, body.String()); err != nil {
		return initResult{}, err
	}

	// The generated manifest must always pass its own schema. If this ever
	// failed it would be a bug in this command's template, not a user error,
	// so the half-written file is removed rather than left behind.
	if verifyErr := verifyGeneratedManifest(path); verifyErr != nil {
		_ = os.Remove(path)
		return initResult{}, fmt.Errorf("generated manifest failed self-validation (this is a bug): %w", verifyErr)
	}

	result := initResult{
		Manifest:          path,
		Cloud:             profile.Name,
		Agent:             name,
		Project:           projectResourceID,
		Tools:             includeTools,
		GuardrailPolicyID: guardrailPolicyID,
	}
	return result, nil
}

func writeManifestFile(path, content string) error {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", path, err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("failed to write %s: %w", path, err)
	}
	return nil
}

func verifyGeneratedManifest(path string) error {
	doc, err := config.LoadManifest(path)
	if err != nil {
		return err
	}
	return config.ValidateManifest(doc)
}

// yamlScalar renders a Go string as a single-line YAML scalar, quoting it
// whenever the bare form would change meaning or fail to parse.
func yamlScalar(value string) string {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		// yaml.Marshal of a string cannot fail; this is unreachable in
		// practice, but a quoted fallback keeps output valid regardless.
		return fmt.Sprintf("%q", value)
	}
	return strings.TrimRight(string(encoded), "\n")
}

// yamlBlockLiteral indents every line of value for use under a YAML `|`
// block scalar, so arbitrary operator-supplied text (including blank lines,
// colons, and '#') is embedded literally rather than re-parsed as YAML.
func yamlBlockLiteral(value string, indent int) string {
	prefix := strings.Repeat(" ", indent)
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = ""
			continue
		}
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}
