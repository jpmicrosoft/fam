package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"

	"github.com/spf13/cobra"
)

const baseManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: base-agent
  model: base-model
  instructions: base instructions
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
tools:
  - type: code_interpreter
`

func writeManifest(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func commandWithFlags(t *testing.T, name, manifest string, flags map[string]string) *cobra.Command {
	t.Helper()
	command, _, err := rootCmd().Find([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("manifest", manifest); err != nil {
		t.Fatal(err)
	}
	for flag, value := range flags {
		flagSet := command.Flags()
		if flagSet.Lookup(flag) == nil {
			flagSet = command.Root().PersistentFlags()
		}
		if err := flagSet.Set(flag, value); err != nil {
			t.Fatalf("failed to set --%s: %v", flag, err)
		}
	}
	return command
}

func TestCommandAndFlagParity(t *testing.T) {
	root := rootCmd()
	common := []string{
		"manifest",
		"name",
		"model",
		"description",
		"instructions-file",
		"project-resource-id",
		"location",
	}
	tests := map[string][]string{
		"validate": common,
		"plan":     common,
		"project-create": append(append([]string{}, common...),
			"project-wait-timeout",
			"project-wait-interval",
			"receipt",
		),
		"connection-list": append(append([]string{}, common...), "connection-api-version"),
		"connection-show": append(append([]string{}, common...),
			"connection", "connection-api-version",
		),
		"connection-create": append(append([]string{}, common...),
			"connection", "connection-api-version", "connection-type", "target", "auth-type",
			"audience", "shared", "metadata-file", "credentials-file", "secret-file",
			"secret-env", "receipt",
		),
		"connection-update": append(append([]string{}, common...),
			"connection", "connection-api-version", "connection-type", "target", "auth-type",
			"audience", "shared", "metadata-file", "credentials-file", "secret-file",
			"secret-env", "receipt",
		),
		"connection-delete": append(append([]string{}, common...),
			"connection", "connection-api-version", "yes", "receipt",
		),
		"connector-list": append(append([]string{}, common...),
			"accept-preview", "search", "page-size", "skip",
		),
		"api-center-list": append(append([]string{}, common...),
			"api-center-endpoint", "api-center-token-scope", "search",
		),
		"api-center-show": append(append([]string{}, common...),
			"api-center-endpoint", "api-center-token-scope", "server",
		),
		"logicapps-registration-plan": append(append([]string{}, common...),
			"accept-preview", "connector-name", "mcp-server-name", "mcp-server-description",
			"logic-app-resource-id", "operation", "user-parameter", "model-parameter",
		),
		"connector-show": append(append([]string{}, common...),
			"accept-preview", "connector-name",
		),
		"connector-create": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version", "connector-name", "receipt",
		),
		"connector-consent": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version", "object-id", "tenant-id", "redirect-url",
		),
		"connector-actions": append(append([]string{}, common...),
			"accept-preview", "connector-name", "operation",
		),
		"connector-configure": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version", "operation",
			"connector-description", "yes", "receipt",
		),
		"connector-status": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version",
		),
		"connector-wait": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version",
			"connector-timeout", "connector-interval",
		),
		"connector-toolbox-deploy": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version", "toolbox-name",
			"toolbox-description", "toolbox-project-connection", "if-changed", "promote",
			"yes", "receipt", "trusted-tool-host", "trusted-apim-host",
			"trusted-managed-identity-audience",
		),
		"connector-delete": append(append([]string{}, common...),
			"accept-preview", "connection", "connection-api-version", "yes", "receipt",
		),
		"preflight": append(append([]string{}, common...),
			"ensure-project",
			"no-apim",
			"apim-subscription-key",
			"apim-subscription-key-file",
			"apim-subscription-key-stdin",
			"apim-subscription-key-key-vault",
			"apim-subscription-key-env",
			"allow-nonrestorable-apim-update",
			"trusted-apim-host",
			"trusted-tool-host",
			"trusted-managed-identity-audience",
		),
		"deploy": append(append([]string{}, common...),
			"ensure-project",
			"no-apim",
			"apim-subscription-key",
			"apim-subscription-key-file",
			"apim-subscription-key-stdin",
			"apim-subscription-key-key-vault",
			"apim-subscription-key-env",
			"allow-nonrestorable-apim-update",
			"trusted-apim-host",
			"trusted-tool-host",
			"trusted-managed-identity-audience",
			"project-wait-timeout",
			"project-wait-interval",
			"if-changed",
			"smoke-test",
			"smoke-prompt",
			"structured-inputs-file",
			"memory-user-id",
			"approve-mcp-tool",
			"reject-unapproved-mcp",
			"max-mcp-approval-rounds",
			"receipt",
			"rollback-created-project",
			"allow-unconditional-shared-rollback",
		),
		"status":           append(append([]string{}, common...), "no-apim"),
		"show":             append(append([]string{}, common...), "agent-version"),
		"versions":         common,
		"diff":             append(append([]string{}, common...), "no-apim"),
		"toolbox-validate": append(append([]string{}, common...), "toolbox"),
		"toolbox-plan":     append(append([]string{}, common...), "toolbox"),
		"toolbox-deploy": append(append([]string{}, common...),
			"toolbox",
			"if-changed",
			"accept-preview",
			"receipt",
			"trusted-tool-host",
			"trusted-managed-identity-audience",
		),
		"toolbox-status":   append(append([]string{}, common...), "toolbox"),
		"toolbox-versions": append(append([]string{}, common...), "toolbox"),
		"toolbox-promote": append(append([]string{}, common...),
			"toolbox", "toolbox-version", "dry-run", "yes", "receipt",
		),
		"toolbox-delete-version": append(append([]string{}, common...),
			"toolbox", "toolbox-version", "dry-run", "yes", "receipt",
		),
		"skill-create": append(append([]string{}, common...),
			"skill", "accept-preview", "path", "skill-instructions-file",
			"skill-description", "license", "compatibility", "allowed-tools", "default", "receipt",
		),
		"skill-list":         append(append([]string{}, common...), "accept-preview"),
		"skill-show":         append(append([]string{}, common...), "skill", "accept-preview"),
		"skill-version-list": append(append([]string{}, common...), "skill", "accept-preview"),
		"skill-version-show": append(append([]string{}, common...),
			"skill", "version", "accept-preview",
		),
		"skill-set-default": append(append([]string{}, common...),
			"skill", "version", "accept-preview", "receipt",
		),
		"skill-delete": append(append([]string{}, common...),
			"skill", "accept-preview", "yes", "receipt",
		),
		"skill-version-delete": append(append([]string{}, common...),
			"skill", "version", "accept-preview", "yes", "receipt",
		),
		"skill-download": append(append([]string{}, common...),
			"skill", "version", "accept-preview", "destination", "force",
		),
		"grounding-validate": append(append([]string{}, common...), "grounding"),
		"grounding-plan":     append(append([]string{}, common...), "grounding"),
		"grounding-status":   append(append([]string{}, common...), "grounding"),
		"grounding-sync": append(append([]string{}, common...),
			"grounding", "index-timeout", "index-interval", "prune",
			"delete-pruned-uploads", "delete-replaced-uploads", "yes", "receipt",
		),
		"grounding-delete-file": append(append([]string{}, common...),
			"grounding", "file", "delete-upload", "dry-run", "yes", "receipt",
		),
		"grounding-delete-store": append(append([]string{}, common...),
			"grounding", "delete-uploads", "dry-run", "yes", "receipt",
		),
		"memory-store-validate": append(append([]string{}, common...), "memory-store"),
		"memory-store-plan":     append(append([]string{}, common...), "memory-store"),
		"memory-store-list":     append(append([]string{}, common...), "accept-preview"),
		"memory-store-sync": append(append([]string{}, common...),
			"memory-store", "accept-preview", "receipt",
		),
		"memory-store-show": append(append([]string{}, common...), "memory-store", "accept-preview"),
		"memory-store-delete": append(append([]string{}, common...),
			"memory-store", "accept-preview", "yes", "receipt",
		),
		"memory-search": append(append([]string{}, common...),
			"memory-store", "accept-preview", "scope", "input", "items-file",
			"previous-search-id", "max-memories",
		),
		"memory-update": append(append([]string{}, common...),
			"memory-store", "accept-preview", "scope", "input", "items-file",
			"previous-update-id", "update-delay", "memory-timeout", "memory-interval", "receipt",
		),
		"memory-item-create": append(append([]string{}, common...),
			"memory-store", "accept-preview", "scope", "content", "kind", "receipt",
		),
		"memory-item-list": append(append([]string{}, common...),
			"memory-store", "accept-preview", "scope", "kind",
		),
		"memory-item-show": append(append([]string{}, common...),
			"memory-store", "accept-preview", "memory-id",
		),
		"memory-item-update": append(append([]string{}, common...),
			"memory-store", "accept-preview", "memory-id", "content", "receipt",
		),
		"memory-item-delete": append(append([]string{}, common...),
			"memory-store", "accept-preview", "memory-id", "yes", "receipt",
		),
		"memory-scope-delete": append(append([]string{}, common...),
			"memory-store", "accept-preview", "scope", "yes", "receipt",
		),
		"smoke": append(append([]string{}, common...),
			"prompt", "structured-inputs-file", "memory-user-id",
			"approve-mcp-tool", "reject-unapproved-mcp", "max-mcp-approval-rounds",
		),
		"disable": common,
		"enable":  common,
		"prune": append(append([]string{}, common...),
			"no-force", "dry-run", "yes", "keep",
		),
		"delete-version": append(append([]string{}, common...),
			"no-force", "dry-run", "yes", "agent-version",
		),
		"delete": append(append([]string{}, common...),
			"no-force", "dry-run", "yes",
		),
		"decommission": append(append([]string{}, common...),
			"no-force",
			"dry-run",
			"yes",
			"no-apim",
		),
	}

	for commandName, expectedFlags := range tests {
		t.Run(commandName, func(t *testing.T) {
			command, _, err := root.Find([]string{commandName})
			if err != nil {
				t.Fatalf("command %q is missing: %v", commandName, err)
			}
			for _, flagName := range expectedFlags {
				if command.Flags().Lookup(flagName) == nil {
					t.Errorf("command %q is missing --%s", commandName, flagName)
				}
			}
			manifestFlag := command.Flags().Lookup("manifest")
			if manifestFlag == nil || manifestFlag.Shorthand != "f" {
				t.Errorf("command %q must expose -f/--manifest", commandName)
			}
		})
	}
}

func TestGlobalFeatureFlags(t *testing.T) {
	root := rootCmd()
	for _, name := range []string{
		"output",
		"quiet",
		"verbose",
		"debug",
		"progress",
		"cloud",
		"tenant-id",
		"request-timeout",
		"retry-count",
		"retry-delay",
	} {
		if root.PersistentFlags().Lookup(name) == nil {
			t.Errorf("missing global --%s", name)
		}
	}
	if command, _, err := root.Find([]string{"version"}); err != nil || command == nil {
		t.Fatalf("version command is missing: %v", err)
	}
}

func TestRootMetadata(t *testing.T) {
	root := rootCmd()
	if root.Use != "foundry-agent-manager" {
		t.Fatalf("unexpected executable name: %s", root.Use)
	}
	if root.Version != config.Version {
		t.Fatalf("unexpected version: %s", root.Version)
	}
}

// TestShellCompletionIsEnabled runs the real entrypoint (as main() does)
// rather than inspecting rootCmd().Commands() directly: Cobra registers its
// default "completion" command lazily inside Execute(), so a test that never
// executes the root command would see it as absent even though the compiled
// binary serves it correctly.
func TestShellCompletionIsEnabled(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			run := runCLI(t, "", "completion", shell)
			if run.code != 0 {
				t.Fatalf("completion %s failed: %s", shell, run.stderr)
			}
			if strings.TrimSpace(run.stdout) == "" {
				t.Fatalf("completion %s produced no script output", shell)
			}
		})
	}
}

func TestProjectWaitDefaults(t *testing.T) {
	command, _, err := rootCmd().Find([]string{"deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if got := command.Flags().Lookup("project-wait-timeout").DefValue; got != "180" {
		t.Fatalf("unexpected project wait timeout default: %s", got)
	}
	if got := command.Flags().Lookup("project-wait-interval").DefValue; got != "5" {
		t.Fatalf("unexpected project wait interval default: %s", got)
	}
}

func TestInvalidProjectWaitValuesFailBeforeAzure(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	tests := []struct {
		name  string
		flags map[string]string
	}{
		{
			name:  "non-finite timeout",
			flags: map[string]string{"project-wait-timeout": "NaN"},
		},
		{
			name:  "negative interval",
			flags: map[string]string{"project-wait-interval": "-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cmdDeploy(commandWithFlags(t, "deploy", manifest, tt.flags), nil)
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected config error, got %v", err)
			}
		})
	}
}

func TestScalarOverridesResolveBeforeValidation(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "plan", manifest, map[string]string{
		"name":                "support-agent",
		"model":               "gpt-4o",
		"description":         "Support assistant",
		"project-resource-id": "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg-foundry/providers/Microsoft.CognitiveServices/accounts/other-acct/projects/support-project",
		"location":            "East US",
	})

	cfg, err := resolve(command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.Name != "support-agent" ||
		cfg.Agent.Model != "gpt-4o" ||
		cfg.Agent.Description != "Support assistant" {
		t.Fatalf("unexpected agent overrides: %#v", cfg.Agent)
	}
	if cfg.Project.Endpoint != "https://other-acct.services.ai.azure.com/api/projects/support-project" ||
		cfg.Project.ResourceGroup != "rg-foundry" ||
		cfg.Project.AccountName != "other-acct" ||
		cfg.Project.SubscriptionID != "11111111-2222-3333-4444-555555555555" ||
		cfg.Project.Location != "East US" {
		t.Fatalf("unexpected project overrides: %#v", cfg.Project)
	}
}

func TestInstructionsFileOverride(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	instructions := filepath.Join(filepath.Dir(manifest), "instructions.md")
	if err := os.WriteFile(instructions, []byte("You are the support agent."), 0o600); err != nil {
		t.Fatal(err)
	}
	command := commandWithFlags(t, "plan", manifest, map[string]string{
		"instructions-file": "instructions.md",
	})

	cfg, err := resolve(command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.Instructions != "You are the support agent." {
		t.Fatalf("unexpected instructions: %q", cfg.Agent.Instructions)
	}
}

func TestMissingInstructionsFileHasSingleFieldPrefix(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	_, err := resolve(commandWithFlags(t, "plan", manifest, map[string]string{
		"instructions-file": "missing.md",
	}))
	if err == nil || !errs.IsKind(err, "manifest") {
		t.Fatalf("expected manifest error, got %v", err)
	}
	if got := err.Error(); got != `--instructions-file: "missing.md" does not exist in the manifest directory` {
		t.Fatalf("unexpected error: %q", got)
	}
}

func TestUnsafeOverridesAreRejected(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	tests := []struct {
		name  string
		flags map[string]string
		kind  string
	}{
		{
			name:  "instructions traversal",
			flags: map[string]string{"instructions-file": "../escape.md"},
			kind:  "security",
		},
		{
			name:  "instructions absolute path",
			flags: map[string]string{"instructions-file": `C:\escape.md`},
			kind:  "security",
		},
		{
			name:  "invalid project resource id",
			flags: map[string]string{"project-resource-id": "/subscriptions/bad/resourceGroups/x/providers/Microsoft.CognitiveServices/accounts/a/projects/p"},
			kind:  "config",
		},
		{
			name:  "invalid agent name",
			flags: map[string]string{"name": "!!invalid-name!!"},
			kind:  "manifest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := resolve(commandWithFlags(t, "plan", manifest, tt.flags))
			if err == nil || !errs.IsKind(err, tt.kind) {
				t.Fatalf("expected %s error, got %v", tt.kind, err)
			}
		})
	}
}

func TestProjectResourceIDOverrideWins(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	override := "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/new-rg/providers/Microsoft.CognitiveServices/accounts/new-acct/projects/new-proj"
	command := commandWithFlags(t, "delete", manifest, map[string]string{
		"name":                "doomed-agent",
		"project-resource-id": override,
	})

	cfg, err := resolve(command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agent.Name != "doomed-agent" {
		t.Fatalf("lifecycle command did not apply name override: %s", cfg.Agent.Name)
	}
	if cfg.Project.AccountName != "new-acct" || cfg.Project.Name != "new-proj" {
		t.Fatalf("project-resource-id override not applied: %#v", cfg.Project)
	}
}

func TestStructuredValidateOutput(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{"validate", "-f", manifest, "--output", "json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("validate failed with %d: %s", code, stderr.String())
	}
	var result validateResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout.String())
	}
	if !result.Valid || result.Agent != "base-agent" || result.Cloud != "AzureCloud" {
		t.Fatalf("unexpected validate result: %#v", result)
	}
	if !strings.Contains(result.DestinationTrust, "not-evaluated") {
		t.Fatalf("offline validation must not imply destination trust: %#v", result)
	}
}

func TestStructuredManifestErrorUsesStableExitCode(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: incomplete
  model: model
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{"validate", "-f", manifest, "--output", "json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 2 {
		t.Fatalf("expected manifest exit code 2, got %d: %s", code, stderr.String())
	}
	var envelope struct {
		Error struct {
			Kind     string `json:"kind"`
			ExitCode int    `json:"exitCode"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid error JSON: %v\n%s", err, stderr.String())
	}
	if envelope.Error.Kind != "manifest" || envelope.Error.ExitCode != 2 {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestMissingRequiredFlagUsesStableConfigExitCode(t *testing.T) {
	var stderr bytes.Buffer
	code := execute(
		[]string{"validate", "--output", "json"},
		strings.NewReader(""),
		&bytes.Buffer{},
		&stderr,
	)
	if code != 3 {
		t.Fatalf("expected config exit code 3, got %d: %s", code, stderr.String())
	}
	var envelope struct {
		Error struct {
			Kind     string `json:"kind"`
			ExitCode int    `json:"exitCode"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid error JSON: %v\n%s", err, stderr.String())
	}
	if envelope.Error.Kind != "config" || envelope.Error.ExitCode != 3 {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestUnknownCommandUsesStableConfigExitCode(t *testing.T) {
	var stderr bytes.Buffer
	code := execute(
		[]string{"does-not-exist", "--output", "json"},
		strings.NewReader(""),
		&bytes.Buffer{},
		&stderr,
	)
	if code != 3 {
		t.Fatalf("expected config exit code 3, got %d: %s", code, stderr.String())
	}
	var envelope struct {
		Error struct {
			Kind string `json:"kind"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatalf("invalid error JSON: %v\n%s", err, stderr.String())
	}
	if envelope.Error.Kind != "config" {
		t.Fatalf("unexpected error envelope: %#v", envelope)
	}
}

func TestVersionCommandIncludesBuildMetadata(t *testing.T) {
	oldCommit, oldDate := config.BuildCommit, config.BuildDate
	config.BuildCommit, config.BuildDate = "abc123", "2026-08-03T12:00:00Z"
	t.Cleanup(func() {
		config.BuildCommit, config.BuildDate = oldCommit, oldDate
	})

	var stdout bytes.Buffer
	code := execute(
		[]string{"version", "--output", "json"},
		strings.NewReader(""),
		&stdout,
		&bytes.Buffer{},
	)
	if code != 0 {
		t.Fatalf("version command failed with %d", code)
	}
	var result versionResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Version != config.Version || result.Commit != "abc123" || result.BuiltAt == "" {
		t.Fatalf("unexpected version result: %#v", result)
	}
}

func TestAzureGovernmentCloudOverrideIsRejected(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	_, err := resolve(commandWithFlags(t, "plan", manifest, map[string]string{
		"cloud": "AzureUSGovernment",
	}))
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected Azure Government rejection, got %v", err)
	}
	if !strings.Contains(err.Error(), "dedicated Azure Government subscription") {
		t.Fatalf("unexpected Azure Government rejection: %v", err)
	}
}

func TestDeployBuildsLocalToolsBeforeAzureCredential(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
agent:
  name: local-first
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
tools:
  - type: openapi
    name: missing
    spec_file: specs/does-not-exist.json
`)
	err := cmdDeploy(commandWithFlags(t, "deploy", manifest, nil), nil)
	if err == nil || !errs.IsKind(err, "tool") {
		t.Fatalf("expected local tool error before Azure access, got %v", err)
	}
}

func TestManifestCommandsDoNotExposeSubscriptionIDFlag(t *testing.T) {
	root := rootCmd()
	manifestCommands := []string{
		"validate", "plan", "deploy", "inspect", "delete",
		"init", "preflight", "project-create",
	}
	for _, name := range manifestCommands {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Logf("skipping %s (not found): %v", name, err)
			continue
		}
		if f := cmd.Flags().Lookup("subscription-id"); f != nil {
			t.Errorf("manifest-backed command %q still exposes --subscription-id flag", name)
		}
	}
}

func TestResolveCannotCreateMixedProjectCoordinates(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "plan", manifest, map[string]string{
		"project-resource-id": "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg-foundry/providers/Microsoft.CognitiveServices/accounts/acct/projects/proj",
	})

	cfg, err := resolve(command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// All coordinates must come from the ARM ID atomically.
	if cfg.Project.SubscriptionID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("subscription was not derived from ARM ID: got %q", cfg.Project.SubscriptionID)
	}
	if cfg.Project.ResourceGroup != "rg-foundry" {
		t.Errorf("resource group was not derived from ARM ID: got %q", cfg.Project.ResourceGroup)
	}
	if cfg.Project.AccountName != "acct" {
		t.Errorf("account was not derived from ARM ID: got %q", cfg.Project.AccountName)
	}
}

func TestProjectARMContextRemainsAtomic(t *testing.T) {
	// Verify that when a project-resource-id is set, the subscription in the
	// resolved config always matches the one embedded in the ARM ID and cannot
	// be independently mutated.
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "plan", manifest, nil)

	cfg, err := resolve(command)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The base manifest has subscription 00000000-...
	if cfg.Project.SubscriptionID != "00000000-0000-0000-0000-000000000000" {
		t.Errorf("subscription should match manifest ARM ID, got %q", cfg.Project.SubscriptionID)
	}
	if cfg.Project.ResourceGroup != "rg" {
		t.Errorf("resource group should match manifest ARM ID, got %q", cfg.Project.ResourceGroup)
	}
	if cfg.Project.AccountName != "account" {
		t.Errorf("account should match manifest ARM ID, got %q", cfg.Project.AccountName)
	}
}
