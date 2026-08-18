package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/trust"

	"gopkg.in/yaml.v3"
)

// allCommands is the full manifest-command surface, used to prove that flag
// registration, help, and trust enforcement stay consistent across it.
var allCommands = []string{
	"init", "validate", "plan", "project-create",
	"model-deployment-list", "model-deployment-show", "model-deployment-plan",
	"model-deployment-create", "model-deployment-delete",
	"connection-list", "connection-show", "connection-create", "connection-update", "connection-delete",
	"api-center-list", "api-center-show", "logicapps-registration-plan",
	"connector-list", "connector-show", "connector-create", "connector-consent",
	"connector-actions", "connector-configure", "connector-status", "connector-wait",
	"connector-toolbox-deploy", "connector-delete",
	"doctor", "preflight", "deploy", "status", "show", "endpoint-show",
	"endpoint-configure", "versions", "diff", "smoke", "disable", "enable",
	"promote", "rollback", "publish-m365", "legacy-status", "legacy-deploy",
	"legacy-delete", "tool-catalog", "compatibility",
	"toolbox-validate", "toolbox-plan", "toolbox-deploy",
	"toolbox-status", "toolbox-versions", "toolbox-promote",
	"toolbox-delete-version",
	"skill-create", "skill-list", "skill-show", "skill-version-list", "skill-version-show",
	"skill-set-default", "skill-delete", "skill-version-delete", "skill-download",
	"grounding-validate", "grounding-plan",
	"grounding-sync", "grounding-status", "grounding-delete-file",
	"grounding-delete-store",
	"memory-store-validate", "memory-store-plan", "memory-store-sync", "memory-store-list",
	"memory-store-show", "memory-store-delete", "memory-search", "memory-update",
	"memory-item-create", "memory-item-list", "memory-item-show", "memory-item-update",
	"memory-item-delete", "memory-scope-delete",
	"agent365-binding-plan", "agent365-binding-status",
	"agent365-publication-plan", "agent365-publication-status",
	"agent365-publication-admin-handoff",
	"prune", "delete-version", "delete", "decommission",
}

// enforcingCommands are the only commands that send this deployment's
// credentials or data to a manifest-named destination.
var enforcingCommands = []string{"doctor", "preflight", "deploy", "toolbox-deploy", "connector-toolbox-deploy"}

type cliRun struct {
	code   int
	stdout string
	stderr string
}

func runCLI(t *testing.T, stdin string, args ...string) cliRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := execute(args, strings.NewReader(stdin), &stdout, &stderr)
	return cliRun{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func decodeErrorEnvelope(t *testing.T, run cliRun) struct {
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	ExitCode int    `json:"exitCode"`
} {
	t.Helper()
	var envelope struct {
		Error struct {
			Kind     string `json:"kind"`
			Message  string `json:"message"`
			ExitCode int    `json:"exitCode"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(run.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not a JSON error envelope (%v): %q", err, run.stderr)
	}
	if envelope.Error.ExitCode != run.code {
		t.Fatalf("envelope exit code %d does not match process exit %d", envelope.Error.ExitCode, run.code)
	}
	return envelope.Error
}

func examplesDir(t *testing.T) string {
	t.Helper()
	directory, err := filepath.Abs(filepath.Join("..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

// shippedExamples lists only agent-manifest examples (foundry-agent-manager/v1).
// examples/publication.example.yaml uses a different schema
// (foundry-agent-manager/publication/v1) and is intentionally excluded: it is
// consumed only by `publish-m365 --publication` and is never itself a
// manifest passed to validate/plan.
func shippedExamples(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(examplesDir(t), "agent*.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no shipped example manifests were found")
	}
	return matches
}

func TestEveryCommandHelpSucceeds(t *testing.T) {
	root := runCLI(t, "", "--help")
	if root.code != 0 || !strings.Contains(root.stdout, "Getting Started:") {
		t.Fatalf("root help failed (%d): %q / %q", root.code, root.stdout, root.stderr)
	}
	for _, name := range append([]string{"version"}, allCommands...) {
		t.Run(name, func(t *testing.T) {
			legacy := runCLI(t, "", name, "--help")
			if legacy.code != 0 {
				t.Fatalf("%s --help exited %d: %s", name, legacy.code, legacy.stderr)
			}
			if !strings.Contains(legacy.stdout, "Usage:") ||
				!strings.Contains(legacy.stdout, "foundry-agent-manager "+name) {
				t.Fatalf("%s --help did not print compatibility usage: %q", name, legacy.stdout)
			}

			canonicalArgs := canonicalCommandArgs(name)
			if len(canonicalArgs) == 1 && canonicalArgs[0] == name {
				return
			}
			canonical := runCLI(t, "", append(canonicalArgs, "--help")...)
			if canonical.code != 0 {
				t.Fatalf("%s --help exited %d: %s", strings.Join(canonicalArgs, " "), canonical.code, canonical.stderr)
			}
			expectedUsage := "foundry-agent-manager " + strings.Join(canonicalArgs, " ")
			if !strings.Contains(canonical.stdout, "Usage:") ||
				!strings.Contains(canonical.stdout, expectedUsage) {
				t.Fatalf("%s --help did not print canonical usage: %q", name, canonical.stdout)
			}
		})
	}
}

func TestVersionOutputFormatsAreStable(t *testing.T) {
	textRun := runCLI(t, "", "version")
	if textRun.code != 0 || !strings.HasPrefix(textRun.stdout, "foundry-agent-manager ") {
		t.Fatalf("unexpected text version output: %q", textRun.stdout)
	}
	rootFlag := runCLI(t, "", "--version")
	if rootFlag.code != 0 || strings.TrimSpace(rootFlag.stdout) != strings.TrimSpace(textRun.stdout) {
		t.Fatalf("--version and the version command disagree: %q vs %q", rootFlag.stdout, textRun.stdout)
	}
	singleDashFlag := runCLI(t, "", "-version")
	if singleDashFlag.code != 0 || strings.TrimSpace(singleDashFlag.stdout) != strings.TrimSpace(textRun.stdout) {
		t.Fatalf("-version and the version command disagree: %q vs %q", singleDashFlag.stdout, textRun.stdout)
	}

	jsonRun := runCLI(t, "", "version", "--output", "json")
	var fromJSON versionResult
	if err := json.Unmarshal([]byte(jsonRun.stdout), &fromJSON); err != nil {
		t.Fatalf("invalid JSON version output %q: %v", jsonRun.stdout, err)
	}

	yamlRun := runCLI(t, "", "version", "--output", "yaml")
	var fromYAML versionResult
	if err := yaml.Unmarshal([]byte(yamlRun.stdout), &fromYAML); err != nil {
		t.Fatalf("invalid YAML version output %q: %v", yamlRun.stdout, err)
	}
	if fromJSON.Version != fromYAML.Version || fromJSON.Version == "" {
		t.Fatalf("version differs between formats: %#v vs %#v", fromJSON, fromYAML)
	}

	quietRun := runCLI(t, "", "version", "--quiet")
	if quietRun.code != 0 || quietRun.stdout != "" {
		t.Fatalf("--quiet must suppress successful text output: %q", quietRun.stdout)
	}
	quietJSON := runCLI(t, "", "version", "--quiet", "--output", "json")
	if !strings.Contains(quietJSON.stdout, "\"version\"") {
		t.Fatalf("--quiet must not suppress structured output: %q", quietJSON.stdout)
	}
}

func TestFamExecutableAlias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := executeNamed("fam.exe", []string{"-version"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fam -version exited %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "fam ") {
		t.Fatalf("fam -version used the wrong executable name: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = executeNamed("fam", []string{"--help"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fam --help exited %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Usage:\n  fam") {
		t.Fatalf("fam --help omitted alias usage: %q", stdout.String())
	}
}

func TestSingleDashVersionNormalizationIsRootOnly(t *testing.T) {
	args := []string{"quickstart", "--description", "-version"}
	normalized := normalizeRootArgs(args)
	if normalized[2] != "-version" {
		t.Fatalf("subcommand argument was rewritten: %#v", normalized)
	}
}

func TestStructuredErrorsUseStableKindsAndExitCodes(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	tests := []struct {
		name string
		args []string
		kind string
		code int
	}{
		{"unknown command", []string{"does-not-exist"}, "config", 3},
		{"missing manifest flag", []string{"validate"}, "config", 3},
		{"missing agent-version", []string{"delete-version", "-f", manifest}, "config", 3},
		{"unexpected argument", []string{"validate", "-f", manifest, "extra"}, "config", 3},
		{"unknown flag", []string{"validate", "-f", manifest, "--nope"}, "config", 3},
		{"bad cloud", []string{"validate", "-f", manifest, "--cloud", "AzureChinaCloud"}, "config", 3},
		{"zero request timeout", []string{"validate", "-f", manifest, "--request-timeout", "0"}, "config", 3},
		{"negative retry count", []string{"validate", "-f", manifest, "--retry-count", "-1"}, "config", 3},
		{"zero retry delay", []string{"validate", "-f", manifest, "--retry-delay", "0s"}, "config", 3},
		{"missing manifest file", []string{"validate", "-f", "does-not-exist.yaml"}, "manifest", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runCLI(t, "", append(tt.args, "--output", "json")...)
			if run.code != tt.code {
				t.Fatalf("expected exit %d, got %d: %s", tt.code, run.code, run.stderr)
			}
			detail := decodeErrorEnvelope(t, run)
			if detail.Kind != tt.kind {
				t.Fatalf("expected kind %q, got %q", tt.kind, detail.Kind)
			}
			if detail.Message == "" {
				t.Fatal("structured errors must carry a message")
			}
			if run.stdout != "" {
				t.Fatalf("errors must not be written to stdout: %q", run.stdout)
			}
		})
	}
}

func TestUnsupportedOutputFormatIsRejected(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	for _, format := range []string{"xml", "table", "toml"} {
		run := runCLI(t, "", "validate", "-f", manifest, "--output", format)
		if run.code != 3 {
			t.Fatalf("--output %s must exit 3, got %d: %s", format, run.code, run.stderr)
		}
		if !strings.Contains(run.stderr, "unsupported output format") {
			t.Fatalf("unexpected error for --output %s: %q", format, run.stderr)
		}
		if run.stdout != "" {
			t.Fatalf("--output %s must not print a result: %q", format, run.stdout)
		}
	}
}

func TestErrorsAreRenderedInTheSelectedFormatEvenWhenParsingFails(t *testing.T) {
	yamlRun := runCLI(t, "", "validate", "--output", "yaml")
	if yamlRun.code != 3 {
		t.Fatalf("expected exit 3, got %d", yamlRun.code)
	}
	var envelope struct {
		Error struct {
			Kind     string `yaml:"kind"`
			ExitCode int    `yaml:"exitCode"`
		} `yaml:"error"`
	}
	if err := yaml.Unmarshal([]byte(yamlRun.stderr), &envelope); err != nil {
		t.Fatalf("stderr is not a YAML error envelope (%v): %q", err, yamlRun.stderr)
	}
	if envelope.Error.Kind != "config" || envelope.Error.ExitCode != 3 {
		t.Fatalf("unexpected YAML envelope: %#v", envelope)
	}

	// The short form and the attached form must select the same renderer.
	for _, args := range [][]string{
		{"validate", "-o", "json"},
		{"validate", "-ojson"},
		{"validate", "--output=json"},
	} {
		run := runCLI(t, "", args...)
		if detail := decodeErrorEnvelope(t, run); detail.Kind != "config" {
			t.Fatalf("args %v produced %#v", args, detail)
		}
	}
}

func TestEmptyApprovalValuesAreRejectedRatherThanDropped(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	tests := []struct {
		name   string
		values []string
	}{
		{"only empty", []string{""}},
		{"only blank", []string{"   "}},
		{"valid then empty", []string{"contoso.azure-api.net", ""}},
		{"empty then valid", []string{"", "contoso.azure-api.net"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
				trust.FlagAPIMHost: tt.values,
			})
			values := getStringArrayFlag(command, trust.FlagAPIMHost)
			if len(values) != len(tt.values) {
				t.Fatalf("approval values were not read verbatim: %#v", values)
			}
			_, err := approveDestinations(command, prepareForTest(t, command))
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("an empty approval must be reported as a config error, got %v", err)
			}
			if !strings.Contains(err.Error(), "empty values") {
				t.Fatalf("the error must explain the empty approval: %v", err)
			}
		})
	}
}

func TestShippedExamplesValidateAndPlanOffline(t *testing.T) {
	for _, example := range shippedExamples(t) {
		name := filepath.Base(example)
		t.Run(name, func(t *testing.T) {
			text := runCLI(t, "", "validate", "-f", example)
			if text.code != 0 {
				t.Fatalf("validate failed (%d): %s", text.code, text.stderr)
			}
			if !strings.Contains(text.stdout, "not-evaluated") {
				t.Fatalf("offline validate must not imply destination trust: %q", text.stdout)
			}

			structured := runCLI(t, "", "validate", "-f", example, "--output", "json")
			var validated validateResult
			if err := json.Unmarshal([]byte(structured.stdout), &validated); err != nil {
				t.Fatalf("invalid JSON: %v (%q)", err, structured.stdout)
			}
			if !validated.Valid || validated.Agent == "" {
				t.Fatalf("unexpected validate result: %#v", validated)
			}
			if !strings.HasPrefix(validated.DestinationTrust, "not-evaluated") {
				t.Fatalf("destination trust must be reported as not evaluated: %#v", validated)
			}

			planText := runCLI(t, "", "plan", "-f", example)
			if planText.code != 0 {
				t.Fatalf("plan failed (%d): %s", planText.code, planText.stderr)
			}
			if !strings.Contains(planText.stdout, "trust:") ||
				!strings.Contains(planText.stdout, "not-evaluated") {
				t.Fatalf("plan must report destination trust as not evaluated: %q", planText.stdout)
			}

			planJSON := runCLI(t, "", "plan", "-f", example, "--output", "json")
			var planned planResult
			if err := json.Unmarshal([]byte(planJSON.stdout), &planned); err != nil {
				t.Fatalf("invalid plan JSON: %v (%q)", err, planJSON.stdout)
			}
			if !strings.HasPrefix(planned.DestinationTrust, "not-evaluated") {
				t.Fatalf("plan JSON must report destination trust as not evaluated: %#v", planned)
			}

			planYAML := runCLI(t, "", "plan", "-f", example, "--output", "yaml")
			var plannedYAML planResult
			if err := yaml.Unmarshal([]byte(planYAML.stdout), &plannedYAML); err != nil {
				t.Fatalf("invalid plan YAML: %v (%q)", err, planYAML.stdout)
			}
			if plannedYAML.Cloud != planned.Cloud || plannedYAML.Agent != planned.Agent {
				t.Fatalf("JSON and YAML plans disagree: %#v vs %#v", planned, plannedYAML)
			}
		})
	}
}

func TestAzureGovernmentManifestIsRejected(t *testing.T) {
	manifest := writeManifest(t, `apiVersion: foundry-agent-manager/v1
cloud: AzureUSGovernment
agent:
  name: gov-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
`)
	run := runCLI(t, "", "plan", "-f", manifest, "--output", "json")
	if run.code != 3 {
		t.Fatalf("expected Azure Government to fail with exit 3, got %d: %s", run.code, run.stderr)
	}
	detail := decodeErrorEnvelope(t, run)
	if detail.Kind != "config" ||
		!strings.Contains(detail.Message, "dedicated Azure Government subscription") {
		t.Fatalf("unexpected Azure Government rejection: %#v", detail)
	}
}

func TestTrustFlagsAreAbsentFromNonEnforcingCommands(t *testing.T) {
	root := rootCmd()
	trustFlags := []string{trust.FlagAPIMHost, trust.FlagToolHost, trust.FlagAudience}
	enforcing := map[string]bool{}
	for _, name := range enforcingCommands {
		enforcing[name] = true
	}
	for _, name := range allCommands {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
		for _, flag := range trustFlags {
			present := command.Flags().Lookup(flag) != nil
			if enforcing[name] && !present {
				t.Errorf("enforcing command %q must expose --%s", name, flag)
			}
			if !enforcing[name] && present {
				t.Errorf("command %q must not imply destination trust with --%s", name, flag)
			}
		}
	}
}

func TestNonEnforcingCommandsRejectTrustFlags(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	for _, name := range []string{"validate", "plan", "status", "diff", "decommission"} {
		t.Run(name, func(t *testing.T) {
			run := runCLI(t, "",
				name, "-f", manifest,
				"--"+trust.FlagAPIMHost, "contoso.azure-api.net",
				"--output", "json",
			)
			if run.code != 3 {
				t.Fatalf("expected a config error for an unsupported flag, got %d: %s", run.code, run.stderr)
			}
		})
	}
}

func TestOfflineCommandsNeedNoApprovalsForAPIMManifests(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	for _, name := range []string{"validate", "plan"} {
		run := runCLI(t, "", name, "-f", manifest)
		if run.code != 0 {
			t.Fatalf("%s must stay usable without approvals (%d): %s", name, run.code, run.stderr)
		}
	}
}

func TestAgentWithoutExternalDestinationsNeedsNoApprovals(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	command := commandWithFlags(t, "deploy", manifest, nil)
	approved, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("a local-only agent must not require approvals: %v", err)
	}
	if len(approved) != 0 {
		t.Fatalf("a local-only agent has no approved destinations: %#v", approved)
	}
}

func TestMalformedTrustApprovalsAreRejectedWithStableKinds(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	tests := []struct {
		name  string
		flag  string
		value string
		kind  string
		code  int
	}{
		{"wildcard host", trust.FlagAPIMHost, "*.azure-api.net", "security", 4},
		{"suffix host", trust.FlagAPIMHost, ".azure-api.net", "security", 4},
		{"url host", trust.FlagAPIMHost, "https://contoso.azure-api.net", "config", 3},
		{"host with path", trust.FlagToolHost, "api.contoso.com/orders", "config", 3},
		{"host with userinfo", trust.FlagToolHost, "user@api.contoso.com", "config", 3},
		{"empty host", trust.FlagAPIMHost, "", "config", 3},
		{"blank host", trust.FlagAPIMHost, "   ", "config", 3},
		{"idn host", trust.FlagToolHost, "api.cöntoso.com", "security", 4},
		{"wildcard audience", trust.FlagAudience, "https://*.azure.com", "security", 4},
		{"scope audience", trust.FlagAudience, "https://cognitiveservices.azure.com/.default", "config", 3},
		{"empty audience", trust.FlagAudience, "", "config", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := runCLI(t, "", "preflight", "-f", manifest, "--"+tt.flag, tt.value, "--output", "json")
			if run.code != tt.code {
				t.Fatalf("expected exit %d, got %d: %s", tt.code, run.code, run.stderr)
			}
			if detail := decodeErrorEnvelope(t, run); detail.Kind != tt.kind {
				t.Fatalf("expected kind %q, got %q (%s)", tt.kind, detail.Kind, detail.Message)
			}
		})
	}
}

func TestMalformedEnvironmentApprovalsAreRejected(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	t.Setenv(trust.EnvAPIMHosts, "contoso.azure-api.net,*.azure-api.net")
	run := runCLI(t, "", "preflight", "-f", manifest, "--output", "json")
	if run.code != 4 {
		t.Fatalf("expected the wildcard environment approval to be rejected, got %d: %s", run.code, run.stderr)
	}
	if detail := decodeErrorEnvelope(t, run); !strings.Contains(detail.Message, "wildcard") {
		t.Fatalf("unexpected rejection: %s", detail.Message)
	}
}

func TestRepeatedAndEnvironmentApprovalsCombine(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	t.Setenv(trust.EnvToolHosts, "unused-a.example.com;unused-b.example.com")
	command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"first.azure-api.net", "contoso.azure-api.net", "last.azure-api.net"},
	})
	if _, err := approveDestinations(command, prepareForTest(t, command)); err != nil {
		t.Fatalf("a repeated approval list must include every value: %v", err)
	}
}

func TestAPIMHostApprovalNormalizationThroughTheCLI(t *testing.T) {
	tests := []struct {
		name     string
		target   string
		approval string
		wantCode int
	}{
		{"case insensitive", "https://CONTOSO.AZURE-API.NET/agents/chat", "contoso.azure-api.net", 0},
		{"trailing dot destination", "https://contoso.azure-api.net./agents/chat", "contoso.azure-api.net", 0},
		{"trailing dot approval", "https://contoso.azure-api.net/agents/chat", "contoso.azure-api.net.", 0},
		{"explicit default port", "https://contoso.azure-api.net:443/agents/chat", "contoso.azure-api.net", 0},
		{"approved default port", "https://contoso.azure-api.net/agents/chat", "contoso.azure-api.net:443", 0},
		{"non-default port needs exact approval", "https://contoso.azure-api.net:8443/agents/chat", "contoso.azure-api.net", 4},
		{"non-default port approved", "https://contoso.azure-api.net:8443/agents/chat", "contoso.azure-api.net:8443", 0},
		{"sibling host", "https://attacker.azure-api.net/agents/chat", "contoso.azure-api.net", 4},
		{"suffix extension", "https://contoso.azure-api.net.attacker.example/agents/chat", "contoso.azure-api.net", 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := writeManifest(t, strings.Replace(
				apimManifest, "https://contoso.azure-api.net/agents/chat", tt.target, 1,
			))
			command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
				trust.FlagAPIMHost: {tt.approval},
			})
			prepared, err := prepareAgent(command)
			if err != nil {
				if tt.wantCode == 0 {
					t.Fatalf("unexpected preparation error: %v", err)
				}
				if errs.ExitCode(err) != tt.wantCode {
					t.Fatalf("expected exit %d, got %d (%v)", tt.wantCode, errs.ExitCode(err), err)
				}
				return
			}
			_, err = approveDestinations(command, prepared)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("expected the destination to be approved, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected the destination to be rejected")
			}
			if errs.ExitCode(err) != tt.wantCode {
				t.Fatalf("expected exit %d, got %d (%v)", tt.wantCode, errs.ExitCode(err), err)
			}
		})
	}
}

func TestSpecFileContainmentFailureUsesTheSecurityExitCode(t *testing.T) {
	manifest := writeManifest(t, toolManifest(`  - type: openapi
    name: orders
    spec_file: specs/orders.json
`))
	// A junction/symlink swap is the runtime case; a directory in place of the
	// spec file exercises the same rooted-read failure path deterministically.
	specDirectory := filepath.Join(filepath.Dir(manifest), "specs", "orders.json")
	if err := os.MkdirAll(specDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	run := runCLI(t, "", "validate", "-f", manifest, "--output", "json")
	if run.code != 9 {
		t.Fatalf("a directory spec_file is a tool-build failure (exit 9), got %d: %s", run.code, run.stderr)
	}

	traversal := writeManifest(t, toolManifest(`  - type: openapi
    name: orders
    spec_file: ../orders.json
`))
	traversalRun := runCLI(t, "", "validate", "-f", traversal, "--output", "json")
	if traversalRun.code != 2 {
		t.Fatalf("schema-level containment rejection is a manifest failure (exit 2), got %d: %s",
			traversalRun.code, traversalRun.stderr)
	}
}

func TestNonObjectManifestSectionsAreRejected(t *testing.T) {
	tests := map[string]string{
		"agent as string":   "apiVersion: foundry-agent-manager/v1\nagent: not-an-object\n",
		"project as string": "apiVersion: foundry-agent-manager/v1\nagent:\n  name: a\n  model: m\n  instructions: i\nproject: not-an-object\n",
		"agent as list":     "apiVersion: foundry-agent-manager/v1\nagent:\n  - name: a\n",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := writeManifest(t, contents)
			run := runCLI(t, "", "validate", "-f", manifest, "--output", "json")
			if run.code != 2 {
				t.Fatalf("expected a manifest failure (exit 2), got %d: %s", run.code, run.stderr)
			}
			detail := decodeErrorEnvelope(t, run)
			if !strings.Contains(detail.Message, "must be an object") {
				t.Fatalf("unexpected message: %s", detail.Message)
			}
		})
	}
}

func TestPruneRejectsInvalidRetentionBeforeAzureAccess(t *testing.T) {
	manifest := writeManifest(t, baseManifest)
	for _, keep := range []string{"0", "-1"} {
		t.Run("keep="+keep, func(t *testing.T) {
			command := commandWithFlags(t, "prune", manifest, map[string]string{
				"keep": keep,
				"yes":  "true",
			})
			err := cmdPrune(command, nil)
			if err == nil || !errs.IsKind(err, "config") {
				t.Fatalf("expected a local config rejection, got %v", err)
			}
			if !strings.Contains(err.Error(), "--keep") {
				t.Fatalf("the error must name the flag: %v", err)
			}
		})
	}
}

func TestStructuredDestructiveCommandsRequireExplicitConfirmation(t *testing.T) {
	for _, name := range []string{"prune", "delete-version", "delete", "decommission"} {
		t.Run(name, func(t *testing.T) {
			root := rootCmd()
			command, _, err := root.Find([]string{name})
			if err != nil {
				t.Fatal(err)
			}
			for _, flag := range []string{"yes", "dry-run", "no-force"} {
				if command.Flags().Lookup(flag) == nil {
					t.Fatalf("destructive command %q is missing --%s", name, flag)
				}
			}
			if err := root.PersistentFlags().Set("output", "json"); err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.SetErr(&stderr)
			command.SetIn(strings.NewReader("yes\n"))
			confirmErr := confirmDestructive(command, "Delete everything?")
			if confirmErr == nil || !errs.IsKind(confirmErr, "config") {
				t.Fatalf("structured output must require --yes, got %v", confirmErr)
			}
			if stderr.Len() != 0 {
				t.Fatalf("structured mode must not write a prompt: %q", stderr.String())
			}
		})
	}
}

func TestInteractiveConfirmationAcceptsOnlyYes(t *testing.T) {
	tests := map[string]bool{
		"yes\n":   true,
		"YES\n":   true,
		" yes \n": true,
		"y\n":     false,
		"no\n":    false,
		"":        false,
	}
	for answer, accepted := range tests {
		t.Run(strings.TrimSpace(answer)+"/", func(t *testing.T) {
			command, _, err := rootCmd().Find([]string{"delete"})
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			command.SetErr(&stderr)
			command.SetIn(strings.NewReader(answer))
			confirmErr := confirmDestructive(command, "Delete agent?")
			if accepted && confirmErr != nil {
				t.Fatalf("answer %q must be accepted, got %v", answer, confirmErr)
			}
			if !accepted && confirmErr == nil {
				t.Fatalf("answer %q must be rejected", answer)
			}
			if !strings.Contains(stderr.String(), "Delete agent?") {
				t.Fatalf("the prompt must be written to stderr: %q", stderr.String())
			}
		})
	}
}

func TestConfirmationIsSkippedForYesAndDryRun(t *testing.T) {
	for _, flag := range []string{"yes", "dry-run"} {
		command, _, err := rootCmd().Find([]string{"delete"})
		if err != nil {
			t.Fatal(err)
		}
		if err := command.Flags().Set(flag, "true"); err != nil {
			t.Fatal(err)
		}
		var stderr bytes.Buffer
		command.SetErr(&stderr)
		command.SetIn(strings.NewReader(""))
		if err := confirmDestructive(command, "Delete agent?"); err != nil {
			t.Fatalf("--%s must skip confirmation, got %v", flag, err)
		}
		if stderr.Len() != 0 {
			t.Fatalf("--%s must not prompt: %q", flag, stderr.String())
		}
	}
}
