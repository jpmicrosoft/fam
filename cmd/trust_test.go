package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/trust"

	"github.com/spf13/cobra"
)

const apimManifest = `apiVersion: foundry-agent-manager/v1
agent:
  name: apim-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
apim:
  target: https://contoso.azure-api.net/agents/chat
  auth: api_key
`

// failingHTTPClient fails the test if any Azure request is attempted.
type failingHTTPClient struct {
	t *testing.T
}

func (c *failingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.t.Fatalf("no Azure request may be made before destination approval: %s", req.URL)
	return nil, nil
}

// commandWithApprovals builds a command with repeatable trust approvals applied.
func commandWithApprovals(
	t *testing.T,
	name, manifest string,
	flags map[string]string,
	approvals map[string][]string,
) *cobra.Command {
	t.Helper()
	command := commandWithFlags(t, name, manifest, flags)
	for flag, values := range approvals {
		for _, value := range values {
			if err := command.Flags().Set(flag, value); err != nil {
				t.Fatalf("failed to set --%s: %v", flag, err)
			}
		}
	}
	return command
}

func prepareForTest(t *testing.T, command *cobra.Command) *preparedAgent {
	t.Helper()
	prepared, err := prepareAgent(command)
	if err != nil {
		t.Fatalf("unexpected preparation error: %v", err)
	}
	return prepared
}

// writeSpecFile writes specs/orders.json beside the manifest.
func writeSpecFile(t *testing.T, manifestPath, contents string) {
	t.Helper()
	directory := filepath.Join(filepath.Dir(manifestPath), "specs")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "orders.json"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTrustFlagsExistOnlyOnEnforcingCommands(t *testing.T) {
	root := rootCmd()
	for _, name := range []string{"preflight", "deploy"} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
		for _, flag := range []string{trust.FlagAPIMHost, trust.FlagToolHost, trust.FlagAudience} {
			if command.Flags().Lookup(flag) == nil {
				t.Errorf("command %q is missing --%s", name, flag)
			}
		}
	}
	for _, name := range []string{"validate", "plan"} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
		if command.Flags().Lookup(trust.FlagAPIMHost) != nil {
			t.Errorf("offline command %q must not imply destination trust", name)
		}
	}
}

func TestUnapprovedAPIMHostFailsBeforeSecretResolutionAndAzureCalls(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "")
	manifest := writeManifest(t, strings.Replace(
		apimManifest,
		"https://contoso.azure-api.net/agents/chat",
		"https://attacker.azure-api.net/agents/chat",
		1,
	))
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	prepared := prepareForTest(t, command)

	_, err := runPreflight(command, prepared, transactionCredential{}, &failingHTTPClient{t: t})
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for an unapproved APIM host, got %v", err)
	}
	if !strings.Contains(err.Error(), "attacker.azure-api.net") {
		t.Fatalf("error should name the rejected destination: %v", err)
	}
	if strings.Contains(err.Error(), "subscription key") {
		t.Fatalf("the APIM key must not be resolved before host approval: %v", err)
	}
}

func TestApprovedAPIMHostPassesDestinationApproval(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	command := commandWithApprovals(t, "preflight", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	approved, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("expected the approved host to pass, got %v", err)
	}
	if len(approved) != 1 || !strings.Contains(approved[0], "contoso.azure-api.net") {
		t.Fatalf("unexpected approval summary: %#v", approved)
	}
}

func TestSuffixSharingAPIMHostIsRejectedWithoutApproval(t *testing.T) {
	manifest := writeManifest(t, strings.Replace(
		apimManifest,
		"https://contoso.azure-api.net/agents/chat",
		"https://attacker.azure-api.net/agents/chat",
		1,
	))
	command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for a sibling APIM host, got %v", err)
	}
}

func TestEnvironmentApprovalsMatchFlagApprovals(t *testing.T) {
	t.Setenv(trust.EnvAPIMHosts, "other.azure-api.net, contoso.azure-api.net")
	manifest := writeManifest(t, apimManifest)
	command := commandWithFlags(t, "deploy", manifest, nil)
	if _, err := approveDestinations(command, prepareForTest(t, command)); err != nil {
		t.Fatalf("expected the environment approval to be honored, got %v", err)
	}
}

func TestAPIMApprovalIsRequiredEvenWithCustomSuffixEnvironment(t *testing.T) {
	t.Setenv("FOUNDRY_AGENT_MANAGER_ALLOWED_APIM_SUFFIXES", "internal.example")
	manifest := writeManifest(t, strings.Replace(
		apimManifest,
		"https://contoso.azure-api.net/agents/chat",
		"https://gateway.internal.example/agents/chat",
		1,
	))
	command := commandWithFlags(t, "deploy", manifest, nil)
	prepared := prepareForTest(t, command)
	if _, err := approveDestinations(command, prepared); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("a custom suffix must still require an exact trusted host, got %v", err)
	}
	approvedCommand := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"gateway.internal.example"},
	})
	if _, err := approveDestinations(approvedCommand, prepareForTest(t, approvedCommand)); err != nil {
		t.Fatalf("expected the exactly approved custom host to pass, got %v", err)
	}
}

func TestManagedIdentityAudienceApprovals(t *testing.T) {
	managedIdentityManifest := strings.Replace(apimManifest, "  auth: api_key\n", "  auth: managed_identity\n", 1)
	tests := []struct {
		name      string
		audience  string
		approvals []string
		wantErr   bool
	}{
		{name: "built-in default", audience: "", wantErr: false},
		{name: "dangerous audience without approval", audience: "https://management.azure.com", wantErr: true},
		{
			name:      "approved custom audience",
			audience:  "api://orders-api",
			approvals: []string{"api://orders-api"},
			wantErr:   false,
		},
		{name: "oauth scope form", audience: "https://cognitiveservices.azure.com/.default", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			contents := managedIdentityManifest
			if tt.audience != "" {
				contents += "  audience: " + tt.audience + "\n"
			}
			manifest := writeManifest(t, contents)
			command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
				trust.FlagAPIMHost: {"contoso.azure-api.net"},
				trust.FlagAudience: tt.approvals,
			})
			prepared, err := prepareAgent(command)
			if err != nil {
				if !tt.wantErr {
					t.Fatalf("unexpected preparation error: %v", err)
				}
				return
			}
			_, err = approveDestinations(command, prepared)
			if tt.wantErr && err == nil {
				t.Fatal("expected the audience to be rejected")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected the audience to be accepted, got %v", err)
			}
		})
	}
}

func toolManifest(tool string) string {
	return `apiVersion: foundry-agent-manager/v1
agent:
  name: tool-agent
  model: model
  instructions: help
project:
  resource_id: /subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project
tools:
` + tool
}

func TestOpenAPIDestinationApprovals(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		approvals []string
		wantErr   bool
	}{
		{
			name: "approved root server",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths: {}
`,
			approvals: []string{"api.contoso.com"},
		},
		{
			name: "unapproved root server",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://attacker.example
      paths: {}
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "unapproved operation-level server",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths:
        /items:
          get:
            servers:
              - url: https://attacker.example
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "unapproved path-item server",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths:
        /items:
          servers:
            - url: https://attacker.example
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "templated server url",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://{tenant}.contoso.com
      paths: {}
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "relative server url",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: /v1
      paths: {}
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "missing servers list",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      paths: {}
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
		{
			name: "anonymous auth still needs host approval",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths: {}
    auth:
      type: anonymous
`,
			wantErr: true,
		},
		{
			name: "managed identity audience needs approval",
			tool: `  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths: {}
    auth:
      type: managed_identity
      audience: api://orders-api
`,
			approvals: []string{"api.contoso.com"},
			wantErr:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := writeManifest(t, toolManifest(tt.tool))
			command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
				trust.FlagToolHost: tt.approvals,
			})
			_, err := approveDestinations(command, prepareForTest(t, command))
			if tt.wantErr && err == nil {
				t.Fatal("expected the destination to be rejected")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected the destination to be accepted, got %v", err)
			}
		})
	}
}

func TestConnectionToolBaseURLRequiresExactApproval(t *testing.T) {
	manifest := writeManifest(t, toolManifest(`  - type: a2a_preview
    project_connection_id: a2a-connection
    base_url: https://a2a.contoso.com
    agent_card_path: https://cards.contoso.com/.well-known/agent-card.json
    send_credentials_for_agent_card: true
`))
	unapproved := commandWithApprovals(t, "deploy", manifest, nil, nil)
	if _, err := approveDestinations(
		unapproved,
		prepareForTest(t, unapproved),
	); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected the A2A base URL to require approval, got %v", err)
	}

	baseOnly := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagToolHost: {"a2a.contoso.com"},
	})
	if _, err := approveDestinations(
		baseOnly,
		prepareForTest(t, baseOnly),
	); err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected the external agent-card host to require approval, got %v", err)
	}

	approved := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagToolHost: {"a2a.contoso.com", "cards.contoso.com"},
	})
	if _, err := approveDestinations(
		approved,
		prepareForTest(t, approved),
	); err != nil {
		t.Fatalf("expected exact A2A and agent-card host approvals to pass, got %v", err)
	}
}

func TestOpenAPIManagedIdentityAudienceCanBeApproved(t *testing.T) {
	manifest := writeManifest(t, toolManifest(`  - type: openapi
    name: orders
    spec:
      openapi: "3.0.0"
      servers:
        - url: https://api.contoso.com
      paths: {}
    auth:
      type: managed_identity
      audience: api://orders-api
`))
	command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagToolHost: {"api.contoso.com"},
		trust.FlagAudience: {"api://orders-api"},
	})
	if _, err := approveDestinations(command, prepareForTest(t, command)); err != nil {
		t.Fatalf("expected the approved tool host and audience to pass, got %v", err)
	}
}

func TestOpenAPISpecFileServersAreInspected(t *testing.T) {
	manifest := writeManifest(t, toolManifest(`  - type: openapi
    name: orders
    spec_file: specs/orders.json
`))
	writeSpecFile(t, manifest, `{"openapi":"3.0.0","servers":[{"url":"https://attacker.example"}],"paths":{}}`)
	command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagToolHost: {"api.contoso.com"},
	})
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection for a spec-file destination, got %v", err)
	}
	if !strings.Contains(err.Error(), "attacker.example") {
		t.Fatalf("error should name the rejected destination: %v", err)
	}
}

func TestMCPDestinationApprovals(t *testing.T) {
	manifest := writeManifest(t, toolManifest(`  - type: mcp
    server_label: docs
    server_url: https://mcp.contoso.com/sse
    require_approval: always
`))
	unapproved := commandWithFlags(t, "deploy", manifest, nil)
	if _, err := approveDestinations(unapproved, prepareForTest(t, unapproved)); err == nil ||
		!errs.IsKind(err, "security") {
		t.Fatalf("expected an unapproved MCP host to fail closed, got %v", err)
	}
	approved := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagToolHost: {"mcp.contoso.com"},
	})
	if _, err := approveDestinations(approved, prepareForTest(t, approved)); err != nil {
		t.Fatalf("expected the approved MCP host to pass, got %v", err)
	}
}

func TestMCPRequireApprovalRejectsUnsupportedValues(t *testing.T) {
	for _, value := range []string{"sometimes", "NEVER", "auto"} {
		manifest := writeManifest(t, toolManifest(`  - type: mcp
    server_label: docs
    server_url: https://mcp.contoso.com/sse
    require_approval: `+value+"\n"))
		command := commandWithFlags(t, "validate", manifest, nil)
		if _, err := prepareAgent(command); err == nil {
			t.Fatalf("expected require_approval %q to be rejected", value)
		}
	}
}

func TestInvalidResolvedTargetFailsDiffWithoutPanic(t *testing.T) {
	state := connection.State{
		Exists:     true,
		Name:       "apim-agent",
		Properties: map[string]interface{}{"target": "https://contoso.azure-api.net/agents/chat"},
	}
	apim := &config.ApimSpec{
		Enabled:              true,
		GatewayURL:           "https://contoso.azure-api.net",
		Auth:                 "api_key",
		ConnectionAPIVersion: config.DefaultConnectionAPIVersion,
		AllowedSuffixes:      []string{"azure-api.net"},
	}
	if _, err := compareAPIMConnection(state, apim, "apim-agent", []string{"model"}); err == nil {
		t.Fatal("expected an unresolved APIM target to fail the comparison")
	}

	apim.GatewayURL = "https://attacker.example"
	apim.APIPath = "agents/chat"
	if _, err := compareAPIMConnection(state, apim, "apim-agent", []string{"model"}); err == nil ||
		!errs.IsKind(err, "security") {
		t.Fatal("expected a foreign APIM target to fail the comparison with a security error")
	}
}

func TestDeploymentRefusesUnapprovedPreflightState(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	command := commandWithApprovals(t, "deploy", manifest, nil, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	prepared := prepareForTest(t, command)
	transaction := &deploymentTransaction{cfg: prepared.Resolved.Config}
	_, err := executeDeployment(
		command,
		prepared,
		&preflightState{Endpoint: "https://account.services.ai.azure.com/api/projects/project"},
		transaction,
		time.Minute,
		time.Second,
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("deploy must not mutate Azure without approved destinations, got %v", err)
	}
}

func TestDeploymentRevalidatesApprovalsBeforeMutating(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	// The approval state says "approved" but no approval flag is supplied, so the
	// independent re-check inside executeDeployment must still fail closed.
	command := commandWithFlags(t, "deploy", manifest, nil)
	prepared := prepareForTest(t, command)
	transaction := &deploymentTransaction{cfg: prepared.Resolved.Config}
	_, err := executeDeployment(
		command,
		prepared,
		&preflightState{
			DestinationsApproved: true,
			Endpoint:             "https://account.services.ai.azure.com/api/projects/project",
		},
		transaction,
		time.Minute,
		time.Second,
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("deploy must re-verify destination approvals, got %v", err)
	}
}

func TestUnapprovedAPIMHostRejectsBeforeReadingASecretFile(t *testing.T) {
	manifest := writeManifest(t, strings.Replace(
		apimManifest,
		"https://contoso.azure-api.net/agents/chat",
		"https://attacker.azure-api.net/agents/chat",
		1,
	))
	keyFile := filepath.Join(filepath.Dir(manifest), "apim.key")
	if err := os.WriteFile(keyFile, []byte("super-secret-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	command := commandWithApprovals(t, "preflight", manifest, map[string]string{
		"apim-subscription-key-file": keyFile,
	}, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, &failingHTTPClient{t: t})
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected the unapproved host to fail before the key file is read, got %v", err)
	}
	if strings.Contains(err.Error(), "super-secret-key") {
		t.Fatalf("the secret must never appear in an error: %v", err)
	}
}

// writeTrustFile writes a trust policy file beside t.TempDir() and returns its path.
func writeTrustFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "trust.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTrustFileFlagExistsOnlyOnEnforcingCommands(t *testing.T) {
	root := rootCmd()
	for _, name := range []string{"preflight", "deploy"} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
		if command.Flags().Lookup(trust.FlagPolicyFile) == nil {
			t.Errorf("command %q is missing --%s", name, trust.FlagPolicyFile)
		}
	}
	for _, name := range []string{"validate", "plan"} {
		command, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("command %q is missing: %v", name, err)
		}
		if command.Flags().Lookup(trust.FlagPolicyFile) != nil {
			t.Errorf("offline command %q must not imply destination trust", name)
		}
	}
}

func TestTrustFileApprovesHostWithoutRepeatableFlag(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	trustFile := writeTrustFile(t, "apimHosts:\n  - contoso.azure-api.net\n")
	command := commandWithFlags(t, "preflight", manifest, map[string]string{
		"trust-file": trustFile,
	})
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("expected the trust-file approval to pass, got %v", err)
	}
}

func TestTrustFileRejectsUnapprovedHost(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	trustFile := writeTrustFile(t, "apimHosts:\n  - other-host.azure-api.net\n")
	command := commandWithFlags(t, "preflight", manifest, map[string]string{
		"trust-file": trustFile,
	})
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected a security rejection when the trust file does not approve the host, got %v", err)
	}
}

func TestTrustFileMergesWithRepeatableApprovalFlags(t *testing.T) {
	manifest := writeManifest(t, strings.Join([]string{
		apimManifest,
		"tools:",
		"  - type: openapi",
		"    name: orders",
		"    spec_file: specs/orders.json",
	}, "\n"))
	writeSpecFile(t, manifest, `{
		"openapi": "3.0.0",
		"info": {"title": "orders", "version": "1"},
		"servers": [{"url": "https://api.contoso.com"}],
		"paths": {}
	}`)
	trustFile := writeTrustFile(t, "toolHosts:\n  - api.contoso.com\n")
	command := commandWithApprovals(t, "preflight", manifest, map[string]string{
		"trust-file": trustFile,
	}, map[string][]string{
		trust.FlagAPIMHost: {"contoso.azure-api.net"},
	})
	approved, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("expected the flag-approved APIM host and file-approved tool host to both pass: %v", err)
	}
	if len(approved) != 2 {
		t.Fatalf("expected both the APIM host and the tool host to be recorded as approved: %#v", approved)
	}
}

func TestTrustFileEnvironmentVariableFallback(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	trustFile := writeTrustFile(t, "apimHosts:\n  - contoso.azure-api.net\n")
	t.Setenv(trust.EnvPolicyFile, trustFile)
	command := commandWithFlags(t, "preflight", manifest, nil)
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("expected FOUNDRY_AGENT_MANAGER_TRUST_FILE to be honored, got %v", err)
	}
}

func TestTrustFileFlagTakesPrecedenceOverEnvironmentVariable(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	flagFile := writeTrustFile(t, "apimHosts:\n  - contoso.azure-api.net\n")
	envFile := writeTrustFile(t, "apimHosts:\n  - other-host.azure-api.net\n")
	t.Setenv(trust.EnvPolicyFile, envFile)
	command := commandWithFlags(t, "preflight", manifest, map[string]string{
		"trust-file": flagFile,
	})
	_, err := approveDestinations(command, prepareForTest(t, command))
	if err != nil {
		t.Fatalf("expected --trust-file to take precedence over %s: %v", trust.EnvPolicyFile, err)
	}
}

func TestTrustFileMissingPathFailsBeforeAzureCalls(t *testing.T) {
	manifest := writeManifest(t, apimManifest)
	command := commandWithFlags(t, "preflight", manifest, map[string]string{
		"trust-file": filepath.Join(t.TempDir(), "does-not-exist.yaml"),
	})
	_, err := runPreflight(command, prepareForTest(t, command), transactionCredential{}, &failingHTTPClient{t: t})
	if err == nil || !errs.IsKind(err, "config") {
		t.Fatalf("expected a config error for a missing trust file, got %v", err)
	}
}
