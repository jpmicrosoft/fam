package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/azcloud"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hosted"
	"foundry-agent-manager/internal/receipt"
)

type hostedRunnerFunc func(hosted.Command) (hosted.Execution, error)

func (f hostedRunnerFunc) Run(_ context.Context, command hosted.Command) (hosted.Execution, error) {
	return f(command)
}

func TestHostedCommandSurface(t *testing.T) {
	root := rootCmd()
	expected := map[string][]string{
		"hosted-info":                  {},
		"hosted-adopt":                 {"source", "destination", "in-place", "name", "protocol", "runtime", "entry-point", "dependency-resolution", "environment", "bootstrap-environment", "project-id", "model", "location", "tenant-id", "azd-timeout", "non-interactive", "guardrail-policy-id", "no-guardrail"},
		"hosted-init":                  {"destination", "name", "protocol", "bing-grounding-connection", "bing-custom-search-connection", "bing-custom-search-instance", "toolbox-name", "guardrail-policy-id", "no-guardrail"},
		"hosted-validate":              {"workspace", "service", "environment"},
		"hosted-plan":                  {"workspace", "service", "environment", "provision", "preview-provision"},
		"hosted-environment-create":    {"workspace", "service", "environment", "tenant-id", "project-id", "model-deployment", "location", "azd-timeout"},
		"hosted-preflight":             {"workspace", "service", "environment", "provision", "preview-provision", "accept-preview", "azd-timeout", "no-guardrail"},
		"hosted-status":                {"workspace", "service", "environment", "accept-preview", "azd-timeout"},
		"hosted-show":                  {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version"},
		"hosted-versions":              {"workspace", "service", "environment", "accept-preview", "azd-timeout", "include-drafts"},
		"hosted-diff":                  {"workspace", "service", "environment", "accept-preview", "azd-timeout"},
		"hosted-diagnose":              {"workspace", "service", "environment", "accept-preview", "azd-timeout"},
		"hosted-smoke":                 {"workspace", "service", "environment", "accept-preview", "azd-timeout", "protocol", "prompt", "input", "input-file", "session-id", "previous-response-id", "conversation-id", "isolation-key", "approve-mcp-tool", "reject-unapproved-mcp", "max-mcp-approval-rounds"},
		"hosted-session-create":        {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version", "isolation-key", "receipt"},
		"hosted-session-list":          {"workspace", "service", "environment", "accept-preview", "azd-timeout", "isolation-key"},
		"hosted-session-show":          {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key"},
		"hosted-session-stop":          {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "receipt"},
		"hosted-session-delete":        {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "dry-run", "yes", "receipt"},
		"hosted-session-file-upload":   {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "file", "remote-path", "receipt"},
		"hosted-session-file-list":     {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "remote-path"},
		"hosted-session-file-download": {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "remote-path", "output-file", "max-bytes"},
		"hosted-session-file-delete":   {"workspace", "service", "environment", "accept-preview", "azd-timeout", "session-id", "isolation-key", "remote-path", "dry-run", "yes", "receipt"},
		"hosted-promote":               {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version", "latest", "receipt"},
		"hosted-rollback":              {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version", "receipt"},
		"hosted-prune":                 {"workspace", "service", "environment", "accept-preview", "azd-timeout", "keep", "include-drafts", "no-force", "dry-run", "yes", "receipt"},
		"hosted-delete-version":        {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version", "no-force", "dry-run", "yes", "receipt"},
		"hosted-delete":                {"workspace", "service", "environment", "accept-preview", "azd-timeout", "no-force", "dry-run", "yes", "receipt"},
		"hosted-logs":                  {"workspace", "service", "environment", "accept-preview", "azd-timeout", "agent-version", "session-id", "max-lines", "max-bytes", "duration"},
		"hosted-draft-deploy":          {"workspace", "service", "environment", "accept-preview", "azd-timeout", "no-guardrail", "description", "receipt"},
		"hosted-disable":               {"workspace", "service", "environment", "accept-preview", "azd-timeout"},
		"hosted-enable":                {"workspace", "service", "environment", "accept-preview", "azd-timeout"},
		"hosted-deploy":                {"workspace", "service", "environment", "provision", "preview-provision", "accept-preview", "azd-timeout", "no-guardrail", "receipt", "if-changed"},
	}

	for commandName, flags := range expected {
		command, _, err := root.Find([]string{commandName})
		if err != nil {
			t.Fatalf("missing %s: %v", commandName, err)
		}
		for _, flag := range flags {
			if command.Flags().Lookup(flag) == nil {
				t.Errorf("%s is missing --%s", commandName, flag)
			}
		}
	}
}

func TestValidateHostedRAIPolicyVerifiesAccountPolicy(t *testing.T) {
	const policyID = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies": route(
			http.StatusOK,
			`{"value":[{"name":"custom"}]}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	profile, err := azcloud.Resolve(azcloud.AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	runner := hostedRunnerFunc(func(command hosted.Command) (hosted.Execution, error) {
		if command.Phase != "environment-value" || len(command.Args) < 3 {
			t.Fatalf("unexpected command: %#v", command)
		}
		switch command.Args[2] {
		case hosted.RAIPolicyEnv:
			return hosted.Execution{ExitCode: 0, Stdout: policyID}, nil
		case "AZURE_AI_PROJECT_ID":
			return hosted.Execution{
				ExitCode: 0,
				Stdout:   "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
			}, nil
		default:
			t.Fatalf("unexpected environment value %q", command.Args[2])
			return hosted.Execution{}, nil
		}
	})
	resolvedPolicyID, err := validateHostedRAIPolicy(
		context.Background(),
		rootCmd(),
		profile,
		runner,
		"azd",
		hosted.Workspace{
			Root: t.TempDir(),
			Selected: hosted.Service{RAIPolicy: &hosted.RAIPolicy{
				PolicyID:            "${RAI_POLICY_ID}",
				UnresolvedReference: true,
			}},
		},
		"dev",
		"https://account.services.ai.azure.com/api/projects/support",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedPolicyID != policyID {
		t.Fatalf("unexpected resolved policy ID %q", resolvedPolicyID)
	}
	if len(httpClient.requests) != 1 {
		t.Fatalf("expected one RAI policy ARM request, got %d", len(httpClient.requests))
	}
	request := httpClient.requests[0]
	if request.Method != http.MethodGet ||
		!strings.HasSuffix(request.URL.Path, "/accounts/account/raiPolicies") ||
		request.URL.Query().Get("api-version") != "2025-06-01" {
		t.Fatalf("unexpected RAI policy list request: %s %s", request.Method, request.URL)
	}
}

func TestValidateHostedRAIPolicyRejectsCrossAccountPolicy(t *testing.T) {
	const policyID = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/other-account/raiPolicies/custom"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{}}
	stubCredentialAndHTTP(t, httpClient)
	profile, err := azcloud.Resolve(azcloud.AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	runner := hostedRunnerFunc(func(command hosted.Command) (hosted.Execution, error) {
		if command.Phase != "environment-value" || len(command.Args) < 3 ||
			command.Args[2] != "AZURE_AI_PROJECT_ID" {
			t.Fatalf("unexpected command: %#v", command)
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		}, nil
	})
	_, err = validateHostedRAIPolicy(
		context.Background(),
		rootCmd(),
		profile,
		runner,
		"azd",
		hosted.Workspace{
			Root: t.TempDir(),
			Selected: hosted.Service{RAIPolicy: &hosted.RAIPolicy{
				PolicyID: policyID,
			}},
		},
		"dev",
		"https://account.services.ai.azure.com/api/projects/support",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "must match the Foundry project account") {
		t.Fatalf("cross-account policy was not rejected: %v", err)
	}
	if len(httpClient.requests) != 0 {
		t.Fatalf("cross-account policy reached ARM: %#v", httpClient.requests)
	}
}

func TestValidateHostedRAIPolicyRejectsPolicylessProjectEndpointMismatch(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{}}
	stubCredentialAndHTTP(t, httpClient)
	profile, err := azcloud.Resolve(azcloud.AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	runner := hostedRunnerFunc(func(command hosted.Command) (hosted.Execution, error) {
		if command.Phase != "environment-value" || len(command.Args) < 3 ||
			command.Args[2] != "AZURE_AI_PROJECT_ID" {
			t.Fatalf("unexpected command: %#v", command)
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		}, nil
	})
	_, err = validateHostedRAIPolicy(
		context.Background(),
		rootCmd(),
		profile,
		runner,
		"azd",
		hosted.Workspace{
			Root:     t.TempDir(),
			Selected: hosted.Service{},
		},
		"dev",
		"https://other.services.ai.azure.com/api/projects/support",
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "does not match the resolved Foundry project endpoint") {
		t.Fatalf("mismatched project binding was not rejected: %v", err)
	}
	if len(httpClient.requests) != 0 {
		t.Fatalf("mismatched project binding reached ARM: %#v", httpClient.requests)
	}
}

func TestVerifyHostedDeployedRAIPolicyRejectsUnresolvedReference(t *testing.T) {
	const policyID = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/Microsoft.DefaultV2"
	profile, err := azcloud.Resolve(azcloud.AzureCloud)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name      string
		deployed  string
		wantError bool
	}{
		{name: "resolved", deployed: policyID},
		{name: "unresolved", deployed: "${RAI_POLICY_ID}", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
				"/agents/hosted-agent/versions/2": route(
					http.StatusOK,
					`{"name":"hosted-agent","version":"2","status":"active","definition":{"kind":"hosted","rai_config":{"rai_policy_name":"`+tt.deployed+`"}}}`,
				),
			}}
			stubCredentialAndHTTP(t, httpClient)
			err := verifyHostedDeployedRAIPolicy(
				context.Background(),
				rootCmd(),
				profile,
				"https://account.services.ai.azure.com/api/projects/project",
				"hosted-agent",
				"2",
				policyID,
			)
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "RAI policy mismatch") {
					t.Fatalf("unresolved deployed policy was not rejected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolved deployed policy was rejected: %v", err)
			}
		})
	}
}

func TestHostedDeployRevalidatesProjectBindingAfterProvision(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, false)
	runner := &hostedCommandFakeRunner{changeEndpointAfterProvision: true}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	run := runCLI(
		t,
		"",
		"hosted-deploy",
		"--workspace", root,
		"--environment", "prod",
		"--accept-preview",
		"--no-guardrail",
		"--provision",
		"--receipt", receiptPath,
		"--output", "json",
	)
	if run.code != 3 ||
		!strings.Contains(run.stderr, "does not match the resolved Foundry project endpoint") {
		t.Fatalf("post-provision binding drift was not rejected: %#v", run)
	}
	if runner.provisionCalls != 1 || runner.deployCalls != 0 ||
		runner.projectEndpointCalls != 2 || runner.projectIDCalls != 2 {
		t.Fatalf("unexpected post-provision validation calls: %#v", runner)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var operation receipt.ReceiptV2
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "failed-partial" {
		t.Fatalf("post-provision validation failure was not recorded as partial: %#v", operation)
	}
}

func TestRequireHostedGuardrailIntentFailsClosed(t *testing.T) {
	withPolicy := hosted.Workspace{
		Selected: hosted.Service{RAIPolicy: &hosted.RAIPolicy{
			PolicyID: "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom",
		}},
	}
	withoutPolicy := hosted.Workspace{Selected: hosted.Service{}}

	command, _, err := rootCmd().Find([]string{"hosted-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	if err := requireHostedGuardrailIntent(command, withoutPolicy); err == nil ||
		!strings.Contains(err.Error(), "--no-guardrail") {
		t.Fatalf("policy-less workspace was not rejected: %v", err)
	}
	if err := command.Flags().Set("no-guardrail", "true"); err != nil {
		t.Fatal(err)
	}
	if err := requireHostedGuardrailIntent(command, withoutPolicy); err != nil {
		t.Fatalf("explicit opt-out was rejected: %v", err)
	}
	if err := requireHostedGuardrailIntent(command, withPolicy); err == nil ||
		!strings.Contains(err.Error(), "declares an RAI policy") {
		t.Fatalf("conflicting explicit opt-out was not rejected: %v", err)
	}
}

func TestHostedDraftDefinitionSerializesRAIPolicy(t *testing.T) {
	const policyID = "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/Microsoft.DefaultV2"
	definition := hostedDraftDefinition(hosted.Workspace{}, nil, policyID)
	if got := hostedDraftRAIPolicyID(definition); got != policyID {
		t.Fatalf("draft definition omitted RAI policy: %#v", definition)
	}

	withoutPolicy := hostedDraftDefinition(hosted.Workspace{}, nil, "")
	if _, ok := withoutPolicy["rai_config"]; ok {
		t.Fatalf("explicit opt-out serialized rai_config: %#v", withoutPolicy)
	}
}

type hostedEnvironmentCommandRunner struct {
	commands []hosted.Command
}

func (r *hostedEnvironmentCommandRunner) Run(
	_ context.Context,
	command hosted.Command,
) (hosted.Execution, error) {
	r.commands = append(r.commands, command)
	switch command.Phase {
	case "environment-list-before":
		return hosted.Execution{ExitCode: 0, Stdout: `[]`}, nil
	case "environment-create":
		return hosted.Execution{ExitCode: 0}, nil
	case "environment-list-after":
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   `[{"Name":"dev","IsDefault":true}]`,
		}, nil
	case "environment-configure":
		return hosted.Execution{ExitCode: 0}, nil
	default:
		return hosted.Execution{ExitCode: 1}, errors.New("unexpected command phase")
	}
}

func TestHostedEnvironmentCreateUsesDedicatedIdempotentMutation(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedEnvironmentCommandRunner{}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	run := runCLI(
		t,
		"",
		"hosted", "environment", "create",
		"--workspace", workspace,
		"--environment", "dev",
		"--tenant-id", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--model-deployment", "support-model",
		"--location", "eastus2",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted environment create failed: %s", run.stderr)
	}
	var result hostedEnvironmentCreateResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Environment != "dev" || !result.Created || result.Reconciled ||
		len(result.Commands) != 4 || len(result.Configured) < 5 {
		t.Fatalf("unexpected environment result: %#v", result)
	}
	if got := strings.Join(runner.commands[1].Args, " "); got !=
		"env new dev --no-prompt --subscription 11111111-2222-3333-4444-555555555555 --location eastus2" {
		t.Fatalf("unexpected azd env new arguments: %s", got)
	}
	if got := strings.Join(runner.commands[3].Args, " "); got !=
		"env set AZURE_SUBSCRIPTION_ID=11111111-2222-3333-4444-555555555555 AZURE_TENANT_ID=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee AZURE_LOCATION=eastus2 AZURE_AI_PROJECT_ID=/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support FOUNDRY_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_MODEL_DEPLOYMENT_NAME=support-model --environment dev --no-prompt" {
		t.Fatalf("unexpected azd env set arguments: %s", got)
	}
	for _, command := range runner.commands {
		if command.Directory != workspace {
			t.Fatalf("azd command escaped the selected workspace: %#v", command)
		}
		if command.Environment["AZD_NON_INTERACTIVE"] != "true" {
			t.Fatalf("azd command was not forced non-interactive: %#v", command)
		}
	}
}

func TestHostedCommandErrorsPreserveStateSpecificRemediation(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{name: "authentication", err: hosted.ErrAuthentication, expected: "azd auth login"},
		{name: "environment", err: hosted.ErrEnvironment, expected: "hosted environment create"},
		{name: "project endpoint", err: hosted.ErrProjectEndpoint, expected: "hosted environment create"},
		{name: "project ID", err: hosted.ErrProjectID, expected: "--project-id"},
		{name: "project access", err: hosted.ErrProjectAccess, expected: "Foundry Project Manager"},
		{name: "not deployed", err: hosted.ErrAgentNotDeployed, expected: "hosted deploy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classified := hostedCommandError(test.err)
			steps := errs.Remediation(classified)
			if len(steps) == 0 || !strings.Contains(strings.Join(steps, "\n"), test.expected) {
				t.Fatalf("state-specific remediation missing %q: %#v", test.expected, steps)
			}
			if test.name == "not deployed" && errs.KindOf(classified) != "not_found" {
				t.Fatalf("undeployed Hosted Agent must be not_found, got %q", errs.KindOf(classified))
			}
			if test.name != "authentication" &&
				strings.Contains(strings.Join(steps, "\n"), "hosted info") {
				t.Fatalf("state failure was misclassified as missing tooling: %#v", steps)
			}
		})
	}
}

func TestHostedLogsRejectsDurationAtOrAboveRequestTimeoutBeforePreflight(t *testing.T) {
	for _, duration := range []string{"2m", "3m"} {
		t.Run(duration, func(t *testing.T) {
			run := runCLI(
				t,
				"",
				"hosted-logs",
				"--workspace", t.TempDir(),
				"--service", "agent",
				"--environment", "qa",
				"--accept-preview",
				"--agent-version", "1",
				"--session-id", "session-1",
				"--duration", duration,
				"--request-timeout", "2m",
				"--output", "json",
			)
			if run.code != 3 {
				t.Fatalf("expected config exit for duration %s, got %d: %s", duration, run.code, run.stderr)
			}
			detail := decodeErrorEnvelope(t, run)
			if !strings.Contains(detail.Message, "--request-timeout") {
				t.Fatalf("unexpected error: %#v", detail)
			}
		})
	}
}

type hostedLifecycleFakeRunner struct {
	commands         []hosted.Command
	projectEndpoint  string
	extensionVersion string
}

func (f *hostedLifecycleFakeRunner) Run(
	ctx context.Context,
	command hosted.Command,
) (hosted.Execution, error) {
	f.commands = append(f.commands, command)
	if err := ctx.Err(); err != nil {
		return hosted.Execution{}, err
	}
	switch command.Phase {
	case "azd-version":
		return hosted.Execution{ExitCode: 0, Stdout: "azd version " + hosted.MinimumAZDVersion}, nil
	case "azd-extensions":
		version := f.extensionVersion
		if version == "" {
			version = hosted.RequiredExtensionVer
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   `[{"id":"azure.ai.agents","installedVersion":"` + version + `"}]`,
		}, nil
	case "agent-extension-version":
		return hosted.Execution{ExitCode: 0, Stdout: "Version: " + hosted.RequiredExtensionVer}, nil
	case "deploy-contract":
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   "azd deploy <service>\n--no-prompt\n--environment",
		}, nil
	case "provision-contract":
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   "azd provision\n--preview\n--no-prompt",
		}, nil
	case "status-contract":
		return hosted.Execution{ExitCode: 0, Stdout: "azd ai agent show [name]\n--output\n--no-prompt"}, nil
	case "azd-environment":
		return hosted.Execution{ExitCode: 0, Stdout: `[{"Name":"prod","IsDefault":true}]`}, nil
	case "environment-value":
		return hosted.Execution{ExitCode: 0, Stdout: "eastus"}, nil
	case "status":
		return hosted.Execution{
			ExitCode: 0,
			Stdout: `{
  "id": "agent-id",
  "name": "deployed-hosted-agent",
  "version": "3",
  "status": "active",
  "agent_endpoints": {"Responses": "https://account.services.ai.azure.com/agents/deployed-hosted-agent"}
}`,
		}, nil
	case "project-endpoint":
		return hosted.Execution{ExitCode: 0, Stdout: f.projectEndpoint}, nil
	default:
		return hosted.Execution{ExitCode: 0}, nil
	}
}

func writeHostedLifecycleWorkspace(t *testing.T, literalEndpoint bool) string {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "src", "agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := ""
	uses := ""
	if literalEndpoint {
		project = `  project:
    host: azure.ai.project
    endpoint: https://account.services.ai.azure.com/api/projects/project
`
		uses = "    uses: [project]\n"
	}
	contents := `name: hosted-project
services:
` + project + `  agent:
    host: azure.ai.agent
    kind: hosted
    name: workspace-agent-name
    project: src/agent
` + uses + `    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`
	if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestHostedLifecycleCommandsUseVerifiedRESTActions(t *testing.T) {
	tests := []struct {
		command       string
		action        string
		previousState string
		targetState   string
	}{
		{command: "hosted-disable", action: "disable", previousState: "enabled", targetState: "disabled"},
		{command: "hosted-enable", action: "enable", previousState: "disabled", targetState: "enabled"},
	}
	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			workspace := writeHostedLifecycleWorkspace(t, false)
			runner := &hostedLifecycleFakeRunner{
				projectEndpoint: "https://account.services.ai.azure.com/api/projects/project",
			}
			scripted := &scriptedHTTP{routes: map[string]scriptedRoute{
				"/agents/deployed-hosted-agent": routeSequence(
					route(http.StatusOK, `{"id":"agent-id","name":"deployed-hosted-agent","state":"`+tt.previousState+`","versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}}}`),
					route(http.StatusOK, `{"id":"agent-id","name":"deployed-hosted-agent","state":"`+tt.targetState+`","versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}}}`),
				),
				"/agents/deployed-hosted-agent:" + tt.action: route(http.StatusOK, `{}`),
			}}
			stubCredentialAndHTTP(t, scripted)
			oldLookPath := hostedLookPathFn
			oldRunner := newHostedRunnerFn
			hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
			newHostedRunnerFn = func() hosted.Runner { return runner }
			t.Cleanup(func() {
				hostedLookPathFn = oldLookPath
				newHostedRunnerFn = oldRunner
			})

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			code := execute(
				[]string{
					tt.command,
					"--workspace", workspace,
					"--environment", "prod",
					"--accept-preview",
					"--output", "json",
				},
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			if code != 0 {
				t.Fatalf("%s failed with %d: %s", tt.command, code, stderr.String())
			}
			var result hostedLifecycleResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("invalid result: %v\n%s", err, stdout.String())
			}
			if result.Action != tt.action ||
				result.Service != "agent" ||
				result.AgentName != "deployed-hosted-agent" ||
				result.PreviousState != tt.previousState ||
				result.State != tt.targetState ||
				!result.Changed ||
				result.Reconciled {
				t.Fatalf("unexpected lifecycle result: %#v", result)
			}
			if len(scripted.requests) != 3 {
				t.Fatalf("expected GET, POST, GET; got %d requests", len(scripted.requests))
			}
			if scripted.requests[1].Method != http.MethodPost ||
				scripted.requests[1].URL.Path !=
					"/api/projects/project/agents/deployed-hosted-agent:"+tt.action ||
				scripted.requests[1].URL.Query().Get("api-version") != "v1" {
				t.Fatalf("unexpected lifecycle request: %s %s", scripted.requests[1].Method, scripted.requests[1].URL)
			}
			joinedCommands := make([]string, 0, len(runner.commands))
			for _, command := range runner.commands {
				joinedCommands = append(joinedCommands, strings.Join(command.Args, " "))
			}
			allCommands := strings.Join(joinedCommands, "\n")
			if strings.Contains(allCommands, "env get-values") {
				t.Fatal("Hosted lifecycle requested all azd environment values")
			}
			if !strings.Contains(
				allCommands,
				"env get-value FOUNDRY_PROJECT_ENDPOINT --no-prompt --environment prod",
			) {
				t.Fatalf("missing narrow project endpoint lookup:\n%s", allCommands)
			}
		})
	}
}

func TestHostedLifecycleNoOpSkipsMutation(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedLifecycleFakeRunner{}
	scripted := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/deployed-hosted-agent": route(
			http.StatusOK,
			`{"id":"agent-id","name":"deployed-hosted-agent","state":"disabled","versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}}}`,
		),
	}}
	stubCredentialAndHTTP(t, scripted)
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{
			"hosted-disable",
			"--workspace", workspace,
			"--environment", "prod",
			"--accept-preview",
			"--output", "json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("hosted-disable failed with %d: %s", code, stderr.String())
	}
	var result hostedLifecycleResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Reconciled || result.State != "disabled" {
		t.Fatalf("unexpected no-op result: %#v", result)
	}
	if len(scripted.requests) != 1 || scripted.requests[0].Method != http.MethodGet {
		t.Fatalf("no-op lifecycle must not POST: %#v", scripted.requests)
	}
	for _, command := range runner.commands {
		if command.Phase == "project-endpoint" {
			t.Fatal("literal project endpoint unexpectedly read from the environment")
		}
	}
}

func TestHostedLifecycleReconcilesAmbiguousMutation(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedLifecycleFakeRunner{}
	scripted := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/deployed-hosted-agent": routeSequence(
			route(http.StatusOK, `{"id":"agent-id","name":"deployed-hosted-agent","state":"enabled","versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}}}`),
			route(http.StatusOK, `{"id":"agent-id","name":"deployed-hosted-agent","state":"disabled","versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}}}`),
		),
		"/agents/deployed-hosted-agent:disable": routeError(errors.New("connection reset")),
	}}
	stubCredentialAndHTTP(t, scripted)
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{
			"hosted-disable",
			"--workspace", workspace,
			"--environment", "prod",
			"--accept-preview",
			"--output", "json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("ambiguous disable was not reconciled: %d %s", code, stderr.String())
	}
	var result hostedLifecycleResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Changed || !result.Reconciled || result.State != "disabled" {
		t.Fatalf("unexpected reconciled result: %#v", result)
	}
}

func TestHostedLifecycleHonorsCancelledContext(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedLifecycleFakeRunner{}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	command, _, err := rootCmd().Find([]string{"hosted-disable"})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"workspace":      workspace,
		"environment":    "prod",
		"accept-preview": "true",
	} {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command.SetContext(ctx)
	err = cmdHostedDisable(command, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
}

type hostedCommandFakeRunner struct {
	failDeploy                   bool
	advance                      bool
	doctorForbidden              bool
	missingLocation              bool
	changeEndpointAfterProvision bool
	statusCalls                  int
	deployCalls                  int
	doctorCalls                  int
	provisionCalls               int
	projectEndpointCalls         int
	projectIDCalls               int
}

func (f *hostedCommandFakeRunner) Run(
	_ context.Context,
	command hosted.Command,
) (hosted.Execution, error) {
	switch command.Phase {
	case "azd-version":
		return hosted.Execution{ExitCode: 0, Stdout: "azd version " + hosted.MinimumAZDVersion}, nil
	case "azd-extensions":
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   `[{"id":"azure.ai.agents","installedVersion":"` + hosted.RequiredExtensionVer + `"}]`,
		}, nil
	case "agent-extension-version":
		return hosted.Execution{ExitCode: 0, Stdout: "Version: " + hosted.RequiredExtensionVer}, nil
	case "deploy-contract":
		return hosted.Execution{ExitCode: 0, Stdout: "azd deploy <service>\n--no-prompt\n--environment"}, nil
	case "provision-contract":
		return hosted.Execution{ExitCode: 0, Stdout: "azd provision\n--preview\n--no-prompt"}, nil
	case "status-contract":
		return hosted.Execution{ExitCode: 0, Stdout: "azd ai agent show [name]\n--output\n--no-prompt"}, nil
	case "azd-environment":
		return hosted.Execution{ExitCode: 0, Stdout: `[{"Name":"prod","IsDefault":true}]`}, nil
	case "environment-value":
		if f.missingLocation {
			return hosted.Execution{ExitCode: 1}, errors.New("AZURE_LOCATION is not set")
		}
		if len(command.Args) >= 3 && command.Args[2] == "AZURE_AI_PROJECT_ID" {
			f.projectIDCalls++
			return hosted.Execution{
				ExitCode: 0,
				Stdout:   "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project",
			}, nil
		}
		return hosted.Execution{ExitCode: 0, Stdout: "eastus"}, nil
	case "project-endpoint":
		f.projectEndpointCalls++
		if f.changeEndpointAfterProvision && f.provisionCalls > 0 {
			return hosted.Execution{
				ExitCode: 0,
				Stdout:   "https://other.services.ai.azure.com/api/projects/project",
			}, nil
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout:   "https://account.services.ai.azure.com/api/projects/project",
		}, nil
	case "provision":
		f.provisionCalls++
		return hosted.Execution{ExitCode: 0}, nil
	case "doctor":
		f.doctorCalls++
		if f.doctorForbidden {
			return hosted.Execution{
				ExitCode: 1,
				Stdout:   "Foundry returned HTTP 403 (wrong tenant or insufficient RBAC).",
			}, errors.New("exit status 1")
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout: "Foundry project endpoint reachable\n" +
				"Developer has required role on Foundry project\n" +
				"10 passed, 0 failed, 3 skipped",
		}, nil
	case "deploy":
		f.deployCalls++
		if f.failDeploy {
			return hosted.Execution{ExitCode: 1}, errors.New("deploy failed after upload")
		}
		return hosted.Execution{ExitCode: 0}, nil
	case "status":
		f.statusCalls++
		version := "8"
		if f.advance && f.statusCalls > 1 {
			version = "9"
		}
		return hosted.Execution{
			ExitCode: 0,
			Stdout: `{
  "id": "agent-id",
  "name": "hosted-agent",
  "version": "` + version + `",
  "status": "active",
  "agent_endpoints": {"Responses": "https://account.services.ai.azure.com/agent"},
  "instance_identity": {"principal_id": "principal-id", "client_id": "client-id"}
}`,
		}, nil
	default:
		return hosted.Execution{ExitCode: 0}, nil
	}
}

func TestHostedPreflightRejectsMissingLocationBeforeDoctor(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedCommandFakeRunner{missingLocation: true}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	run := runCLI(
		t,
		"",
		"hosted-preflight",
		"--workspace", workspace,
		"--environment", "prod",
		"--accept-preview",
		"--no-guardrail",
		"--output", "json",
	)
	if run.code != 3 {
		t.Fatalf("expected configuration exit, got %d: %s", run.code, run.stderr)
	}
	detail := decodeErrorEnvelope(t, run)
	if detail.Kind != "config" || !strings.Contains(detail.Message, "AZURE_LOCATION") {
		t.Fatalf("unexpected missing-location error: %#v", detail)
	}
	if runner.doctorCalls != 0 || runner.deployCalls != 0 {
		t.Fatalf(
			"missing location reached doctor/deploy: doctor=%d deploy=%d",
			runner.doctorCalls,
			runner.deployCalls,
		)
	}
}

func TestHostedPreflightUsesAZDDeploymentIdentity(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedCommandFakeRunner{doctorForbidden: true}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	run := runCLI(
		t,
		"",
		"hosted-preflight",
		"--workspace", workspace,
		"--environment", "prod",
		"--accept-preview",
		"--no-guardrail",
		"--output", "json",
	)
	if run.code != 5 {
		t.Fatalf("expected authorization exit, got %d: %s", run.code, run.stderr)
	}
	detail := decodeErrorEnvelope(t, run)
	if detail.Kind != "authorization" ||
		!strings.Contains(detail.Message, "project access check failed") {
		t.Fatalf("unexpected preflight error: %#v", detail)
	}
	if runner.doctorCalls != 1 || runner.deployCalls != 0 {
		t.Fatalf(
			"preflight doctor calls=%d deploy calls=%d",
			runner.doctorCalls,
			runner.deployCalls,
		)
	}
}

func TestHostedDeployPersistsCommandsAndReconciles(t *testing.T) {
	for _, failDeploy := range []bool{false, true} {
		name := "normal"
		if failDeploy {
			name = "reconciled"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "src", "agent")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(`name: hosted-project
services:
  project:
    host: azure.ai.project
    endpoint: https://account.services.ai.azure.com/api/projects/project
  agent:
    host: azure.ai.agent
    kind: hosted
    name: hosted-agent
    project: src/agent
    uses: [project]
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
    container:
      resources:
        cpu: "1"
        memory: 2Gi
    env:
      TOOLBOX_NAME: operations
`), 0o600); err != nil {
				t.Fatal(err)
			}

			oldLookPath := hostedLookPathFn
			oldRunner := newHostedRunnerFn
			hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
			newHostedRunnerFn = func() hosted.Runner {
				return &hostedCommandFakeRunner{failDeploy: failDeploy, advance: true}
			}
			t.Cleanup(func() {
				hostedLookPathFn = oldLookPath
				newHostedRunnerFn = oldRunner
			})

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			customReceipt := filepath.Join(t.TempDir(), "hosted-deploy.json")
			code := execute(
				[]string{
					"hosted-deploy",
					"--workspace", root,
					"--environment", "prod",
					"--accept-preview",
					"--no-guardrail",
					"--receipt", customReceipt,
					"--output", "json",
				},
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			if code != 0 {
				t.Fatalf("hosted deploy failed with %d: %s", code, stderr.String())
			}
			var result hostedDeployResult
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatalf("invalid result: %v\n%s", err, stdout.String())
			}
			if result.AgentVersion != "9" || result.AgentIdentityID != "principal-id" {
				t.Fatalf("unexpected deploy result: %#v", result)
			}
			if result.Reconciled != failDeploy {
				t.Fatalf("reconciled=%t, want %t", result.Reconciled, failDeploy)
			}
			if result.Toolbox == nil ||
				result.Toolbox.Name != "operations" ||
				!result.Toolbox.RuntimeApprovalRequired {
				t.Fatalf("Hosted Toolbox obligations missing from result: %#v", result.Toolbox)
			}
			data, err := os.ReadFile(result.Receipt)
			if err != nil {
				t.Fatal(err)
			}
			var operation receipt.ReceiptV2
			if err := json.Unmarshal(data, &operation); err != nil {
				t.Fatal(err)
			}
			if len(operation.Commands) != 13 {
				t.Fatalf("expected 13 persisted commands, got %d", len(operation.Commands))
			}
			foundDoctor := false
			for _, command := range operation.Commands {
				if command.Phase == "doctor" {
					foundDoctor = true
				}
			}
			if !foundDoctor {
				t.Fatalf("receipt must record the pre-mutation azd diagnostics: %#v", operation.Commands)
			}
			if operation.Agent.CreatedVersion != "9" ||
				operation.Agent.ActiveVersionAfter != "" {
				t.Fatalf("receipt must record the version without assuming endpoint routing: %#v", operation.Agent)
			}
			foundToolboxResource := false
			for _, resource := range operation.Resources {
				if resource.Kind == "hosted-toolbox-runtime-reference" &&
					resource.Name == "operations" &&
					resource.Status == "application-code-required" {
					foundToolboxResource = true
				}
			}
			if !foundToolboxResource {
				t.Fatalf("receipt must record Hosted Toolbox runtime ownership: %#v", operation.Resources)
			}
			foundToolboxStep := false
			for _, step := range operation.Steps {
				if step.Action == "toolbox-runtime" &&
					step.Status == "operator-action-required" {
					foundToolboxStep = true
				}
			}
			if !foundToolboxStep {
				t.Fatalf("receipt must record Toolbox approval ownership: %#v", operation.Steps)
			}
			workspace, err := hosted.LoadWorkspace(root, "agent")
			if err != nil {
				t.Fatal(err)
			}
			indexedAgent := &foundry.Agent{Name: "hosted-agent"}
			indexedAgent.Versions.Latest = foundry.AgentVersion{Version: "9"}
			found, indexedPath, err := latestHostedDeployReceipt(&hostedRESTRuntime{
				Profile:         azcloud.Profile{Name: azcloud.AzureCloud},
				Workspace:       workspace,
				Deployment:      hosted.Status{Version: "9", Status: "active"},
				ProjectEndpoint: "https://account.services.ai.azure.com/api/projects/project",
				Agent:           indexedAgent,
			})
			if err != nil {
				t.Fatal(err)
			}
			if found == nil || filepath.Clean(indexedPath) == filepath.Clean(customReceipt) ||
				found.Receipt.Agent.CreatedVersion != "9" {
				t.Fatalf("custom Hosted receipt was not indexed: path=%q receipt=%#v", indexedPath, found)
			}
		})
	}
}

func TestHostedDeployDoesNotReconcileToUnchangedBaseline(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "src", "agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "azure.yaml"), []byte(`name: hosted-project
services:
  agent:
    host: azure.ai.agent
    kind: hosted
    name: hosted-agent
    project: src/agent
    codeConfiguration:
      runtime: python_3_13
      entryPoint: main.py
`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner {
		return &hostedCommandFakeRunner{failDeploy: true, advance: false}
	}
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	command, _, err := rootCmd().Find([]string{"hosted-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"workspace":      root,
		"environment":    "prod",
		"accept-preview": "true",
		"no-guardrail":   "true",
		"receipt":        receiptPath,
	} {
		if err := command.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
	}

	err = cmdHostedDeploy(command, nil)
	if err == nil || !errs.IsAmbiguousMutation(err) {
		t.Fatalf("expected an ambiguous deployment failure, got %v", err)
	}

	data, readErr := os.ReadFile(receiptPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var operation receipt.ReceiptV2
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status != "unknown" || operation.Agent.CreatedVersion != "" {
		t.Fatalf("unchanged baseline must not be claimed as deployed: %#v", operation)
	}
}

func TestHostedDeployReportsPlatformDeduplicationAsUnchanged(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedCommandFakeRunner{advance: false}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	receiptPath := filepath.Join(t.TempDir(), "receipt.json")
	run := runCLI(
		t,
		"",
		"hosted-deploy",
		"--workspace", root,
		"--environment", "prod",
		"--accept-preview",
		"--no-guardrail",
		"--receipt", receiptPath,
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("deduplicated Hosted deploy failed: %#v", run)
	}
	var result hostedDeployResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Changed || result.Status != "unchanged" || result.AgentVersion != "8" {
		t.Fatalf("deduplicated Hosted deploy was reported as changed: %#v", result)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var operation receipt.ReceiptV2
	if err := json.Unmarshal(data, &operation); err != nil {
		t.Fatal(err)
	}
	if operation.Agent.Changed || operation.Agent.CreatedVersion != "" ||
		operation.Agent.LatestVersionAfter != "8" {
		t.Fatalf("deduplicated receipt claimed a new version: %#v", operation.Agent)
	}
}

func TestHostedDeployIfChangedSkipsVerifiedUnchangedSnapshot(t *testing.T) {
	root := writeHostedLifecycleWorkspace(t, true)
	workspace, err := hosted.LoadWorkspace(root, "agent")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := hosted.ComputeDeploymentSnapshot(workspace, "prod")
	if err != nil {
		t.Fatal(err)
	}
	previousPath := receipt.OperationPath(
		workspace.AzureYAML,
		"hosted-deploy",
		"hosted-agent",
		time.Now().Add(-time.Minute),
	)
	previous := receipt.NewOperation(
		previousPath,
		"hosted-deploy",
		"AzureCloud",
		receipt.ManifestReference{
			Path:        workspace.AzureYAML,
			Hash:        workspace.Hash,
			DesiredHash: snapshot.Hash,
		},
		receipt.ResourceReference{
			Name:     workspace.Name,
			Endpoint: "https://account.services.ai.azure.com/api/projects/project",
		},
		"hosted-agent",
	)
	previous.Receipt.Agent.LatestVersionAfter = "8"
	previous.Receipt.Agent.CreatedVersion = "8"
	previous.Receipt.Agent.Changed = true
	if err := previous.Complete("succeeded", nil); err != nil {
		t.Fatal(err)
	}

	runner := &hostedCommandFakeRunner{}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})
	scripted := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/hosted-agent": route(http.StatusOK, `{
			"id":"agent-id",
			"name":"hosted-agent",
			"state":"enabled",
			"versions":{"latest":{"version":"8","status":"active","definition":{"kind":"hosted"}}}
		}`),
	}}
	stubCredentialAndHTTP(t, scripted)

	command, _, err := rootCmd().Find([]string{"hosted-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"workspace":      root,
		"service":        "agent",
		"environment":    "prod",
		"accept-preview": "true",
		"no-guardrail":   "true",
		"if-changed":     "true",
		"output":         "json",
	} {
		if err := flagSet(command, name).Set(name, value); err != nil {
			t.Fatal(err)
		}
	}
	var output bytes.Buffer
	command.SetOut(&output)
	if err := cmdHostedDeploy(command, nil); err != nil {
		t.Fatal(err)
	}
	if runner.deployCalls != 0 {
		t.Fatalf("--if-changed invoked azd deploy %d time(s)", runner.deployCalls)
	}
	var result hostedDeployResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid result: %v\n%s", err, output.String())
	}
	if result.Changed || result.Status != "unchanged" || result.AgentVersion != "8" {
		t.Fatalf("unexpected unchanged result: %#v", result)
	}
}

func TestHostedPromotePinsOneActiveVersionAtOneHundredPercent(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, false)
	runner := &hostedLifecycleFakeRunner{
		projectEndpoint: "https://account.services.ai.azure.com/api/projects/project",
	}
	before := `{
		"id":"agent-id",
		"name":"deployed-hosted-agent",
		"state":"enabled",
		"versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}},
		"agent_endpoint":{"version_selector":{"version_selection_rules":[
			{"agent_version":"2","traffic_percentage":100,"type":"FixedRatio"}
		]}}
	}`
	after := `{
		"id":"agent-id",
		"name":"deployed-hosted-agent",
		"state":"enabled",
		"versions":{"latest":{"version":"3","status":"active","definition":{"kind":"hosted"}}},
		"agent_endpoint":{"version_selector":{"version_selection_rules":[
			{"agent_version":"3","traffic_percentage":100,"type":"FixedRatio"}
		]}}
	}`
	scripted := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/deployed-hosted-agent": routeSequence(
			route(http.StatusOK, before),
			route(http.StatusOK, after),
		),
		"/agents/deployed-hosted-agent/versions/3": route(
			http.StatusOK,
			`{"name":"deployed-hosted-agent","version":"3","status":"active","definition":{"kind":"hosted"}}`,
		),
	}}
	stubCredentialAndHTTP(t, scripted)
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := execute(
		[]string{
			"hosted-promote",
			"--workspace", workspace,
			"--environment", "prod",
			"--accept-preview",
			"--agent-version", "3",
			"--output", "json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("hosted-promote failed with %d: %s", code, stderr.String())
	}
	if len(scripted.requests) != 4 {
		t.Fatalf("expected GET, GET version, PATCH, GET; got %d", len(scripted.requests))
	}
	patch := scripted.requests[2]
	if patch.Method != http.MethodPatch {
		t.Fatalf("expected PATCH, got %s", patch.Method)
	}
	body, err := io.ReadAll(patch.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"agent_version":"3"`) ||
		!strings.Contains(string(body), `"traffic_percentage":100`) {
		t.Fatalf("unexpected routing patch: %s", body)
	}
}

func TestActiveHostedStatusUsesPinnedTerminalStates(t *testing.T) {
	for _, status := range []string{"active", "idle", " ACTIVE "} {
		if !activeHostedStatus(status) {
			t.Fatalf("expected %q to be active", status)
		}
	}
	for _, status := range []string{"deployed", "creating", "failed", ""} {
		if activeHostedStatus(status) {
			t.Fatalf("unexpected active status %q", status)
		}
	}
}

func TestHostedSessionStoppedUsesServiceTerminalStates(t *testing.T) {
	for _, status := range []string{"idle", "stopped", " IDLE "} {
		if !hostedSessionStopped(status) {
			t.Fatalf("expected %q to represent stopped session compute", status)
		}
	}
	for _, status := range []string{"active", "starting", "failed", ""} {
		if hostedSessionStopped(status) {
			t.Fatalf("unexpected stopped status %q", status)
		}
	}
}

func TestRequireHostedResponsesSuccessRejectsFailedBody(t *testing.T) {
	err := requireHostedResponsesSuccess(&foundry.HostedInvocationResult{
		Body: map[string]interface{}{
			"status": "failed",
			"error": map[string]interface{}{
				"code":    "server_error",
				"message": "runtime failed",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "server_error: runtime failed") {
		t.Fatalf("expected Hosted response failure, got %v", err)
	}
	if err := requireHostedResponsesSuccess(&foundry.HostedInvocationResult{
		Body: map[string]interface{}{"status": "completed"},
	}); err != nil {
		t.Fatalf("completed Hosted response was rejected: %v", err)
	}
}
