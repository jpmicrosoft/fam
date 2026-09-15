package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/agent365"
	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	commandBlueprintAppID    = "00001111-aaaa-2222-bbbb-3333cccc4444"
	commandBlueprintObjectID = "08be1f79-37a1-49c0-b444-3075e74d1e8c"
	commandIdentityObjectID  = "11112222-bbbb-3333-cccc-4444dddd5555"
)

func TestAgent365InfoStatesMutationBoundaryWithoutAuthentication(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("agent365 info must not acquire a credential")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("agent365 info must not create an HTTP client")
		return nil
	}

	run := runCLI(t, "", "agent365", "info", "--output", "json")
	if run.code != 0 {
		t.Fatalf("agent365 info failed: %s", run.stderr)
	}
	var result agent365InfoResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.ReadOnly || !result.MutationsRequireApproval || result.BindingMutationSupported {
		t.Fatalf("unsafe Agent 365 capabilities: %+v", result)
	}
	if !strings.Contains(strings.Join(result.Limitations, "\n"), "No documented Foundry API") {
		t.Fatalf("missing binding boundary: %+v", result.Limitations)
	}
	if !strings.Contains(strings.Join(result.Limitations, "\n"), "No binding operation is available.") {
		t.Fatalf("missing comparison-only boundary: %+v", result.Limitations)
	}

	text := runCLI(t, "", "agent365", "info")
	if text.code != 0 {
		t.Fatalf("agent365 info text failed: %s", text.stderr)
	}
	for _, expected := range []string{
		"Blueprint inspection and identity comparison are read-only.",
		"cannot create Foundry agents, attach existing blueprints, or change agent identities",
		"No binding operation is available.",
		"separate confirmed mutation via agent365 integration set",
	} {
		if !strings.Contains(text.stdout, expected) {
			t.Errorf("agent365 info text omitted %q:\n%s", expected, text.stdout)
		}
	}
}

func TestAgent365ComparisonHelpStatesReadOnlyBoundaryWithoutAuthentication(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("comparison help must not acquire a credential")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("comparison help must not create an HTTP client")
		return nil
	}

	for _, tc := range []struct {
		args     []string
		expected []string
	}{
		{[]string{"agent365", "--help"}, []string{"Read-only; no binding operation is available."}},
		{[]string{"agent365", "blueprint", "--help"}, []string{"Read-only; not a deployment source."}},
		{[]string{"agent365", "binding", "--help"}, []string{"Read-only; no binding operation is available."}},
		{[]string{"agent365", "binding", "status", "--help"}, []string{"Show Foundry identity information and optional blueprint correlation", "No binding operation is available."}},
		{[]string{"agent365", "binding", "plan", "--help"}, []string{"Compare an existing blueprint with a deployed Prompt or Hosted Agent", "there is no apply step"}},
		{[]string{"help", "agent365", "binding", "plan"}, []string{"No binding operation is available.", "there is no apply step"}},
		{[]string{"agent365-binding-status", "--help"}, []string{"No binding operation is available."}},
		{[]string{"agent365-binding-plan", "--help"}, []string{"No binding operation is available.", "there is no apply step"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			run := runCLI(t, "", tc.args...)
			if run.code != 0 {
				t.Fatalf("comparison help failed: %s", run.stderr)
			}
			if !strings.Contains(strings.ToLower(run.stdout), "read-only") {
				t.Fatalf("comparison help omitted read-only boundary:\n%s", run.stdout)
			}
			for _, expected := range tc.expected {
				if !strings.Contains(run.stdout, expected) {
					t.Errorf("comparison help omitted %q:\n%s", expected, run.stdout)
				}
			}
			if strings.Contains(run.stdout, "Plan correlation") {
				t.Fatalf("comparison help still implies an executable binding plan:\n%s", run.stdout)
			}
		})
	}
}

func TestAgent365BlueprintShowOmitsCredentialMaterial(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/v1.0/applications/microsoft.graph.agentIdentityBlueprint": route(
			http.StatusOK,
			`{"value":[{
				"id":"`+commandBlueprintObjectID+`",
				"appId":"`+commandBlueprintAppID+`",
				"displayName":"Support Blueprint",
				"managerApplications":[],
				"requiredResourceAccess":[],
				"passwordCredentials":[{"secretText":"must-not-escape"}],
				"keyCredentials":[{"key":"must-not-escape"}]
			}]}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(
		t,
		"",
		"agent365", "blueprint", "show",
		"--blueprint-id", commandBlueprintAppID,
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("blueprint show failed: %s", run.stderr)
	}
	if strings.Contains(run.stdout, "must-not-escape") ||
		strings.Contains(run.stdout, "passwordCredentials") ||
		strings.Contains(run.stdout, "keyCredentials") {
		t.Fatalf("credential material escaped into output: %s", run.stdout)
	}
	var blueprint struct {
		ObjectID string `json:"objectId"`
		AppID    string `json:"appId"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &blueprint); err != nil {
		t.Fatal(err)
	}
	if blueprint.ObjectID != commandBlueprintObjectID || blueprint.AppID != commandBlueprintAppID {
		t.Fatalf("unexpected blueprint: %+v", blueprint)
	}
}

func TestAgent365BlueprintListTextShowsFriendlyNamesAndIDs(t *testing.T) {
	secondObjectID := "11112222-3333-4444-5555-666677778888"
	secondAppID := "99990000-aaaa-bbbb-cccc-ddddeeeeffff"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/v1.0/applications/microsoft.graph.agentIdentityBlueprint": route(
			http.StatusOK,
			`{"value":[
				{
					"id":"`+commandBlueprintObjectID+`",
					"appId":"`+commandBlueprintAppID+`",
					"displayName":"Support Blueprint",
					"managerApplications":[],
					"requiredResourceAccess":[]
				},
				{
					"id":"`+secondObjectID+`",
					"appId":"`+secondAppID+`",
					"displayName":"Operations Blueprint",
					"managerApplications":[],
					"requiredResourceAccess":[]
				}
			]}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(t, "", "agent365", "blueprint", "list")
	if run.code != 0 {
		t.Fatalf("blueprint list failed: %s", run.stderr)
	}
	for _, expected := range []string{
		"Agent 365 blueprints: count=2 truncated=false",
		`name="Support Blueprint" app-id=` + commandBlueprintAppID +
			" object-id=" + commandBlueprintObjectID,
		`name="Operations Blueprint" app-id=` + secondAppID +
			" object-id=" + secondObjectID,
	} {
		if !strings.Contains(run.stdout, expected) {
			t.Fatalf("blueprint list output is missing %q:\n%s", expected, run.stdout)
		}
	}
	if strings.Count(run.stdout, "\n  name=") != 2 {
		t.Fatalf("blueprint list must print one row per result:\n%s", run.stdout)
	}
}

func TestAgent365BindingStatusReportsPromptIdentityWithoutGraphLookup(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1",
			"name":"base-agent",
			"state":"active",
			"instance_identity":{"principal_id":"runtime-principal","client_id":"runtime-client","status":"ready"},
			"blueprint":{"principal_id":"blueprint-principal","client_id":"`+commandBlueprintAppID+`","status":"ready"},
			"blueprint_reference":{"type":"agent_identity_blueprint","blueprint_id":"`+commandBlueprintObjectID+`"},
			"versions":{"latest":{"version":"2","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, httpClient)
	manifest := writeManifest(t, baseManifest)

	run := runCLI(
		t,
		"",
		"agent365", "binding", "status",
		"-f", manifest,
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("binding status failed: %s", run.stderr)
	}
	var result agent365BindingStatusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.TargetType != "prompt" || result.Correlation != "not-requested" {
		t.Fatalf("unexpected binding status: %+v", result)
	}
	if result.InstanceIdentity == nil || result.FoundryBlueprint == nil ||
		result.BlueprintReference == nil {
		t.Fatalf("identity fields were not preserved: %+v", result)
	}
	for _, args := range [][]string{
		{"agent365", "binding", "status", "-f", manifest},
		{"agent365-binding-status", "-f", manifest},
	} {
		text := runCLI(t, "", args...)
		if text.code != 0 {
			t.Fatalf("identity status text failed: %s", text.stderr)
		}
		for _, expected := range []string{
			"Agent 365 identity status (read-only):",
			"correlation=not-requested mutation-supported=false",
			"cannot create Foundry agents, attach existing blueprints, or change agent identities",
			"No binding operation is available.",
		} {
			if !strings.Contains(text.stdout, expected) {
				t.Errorf("identity status text omitted %q:\n%s", expected, text.stdout)
			}
		}
	}
	for _, request := range httpClient.requests {
		if request.URL.Host == "graph.microsoft.com" {
			t.Fatal("binding status without a blueprint selector must not call Microsoft Graph")
		}
		if request.Method != http.MethodGet {
			t.Fatalf("identity status issued mutation %s %s", request.Method, request.URL)
		}
	}
}

func TestAgent365BindingPlanCorrelatesButDoesNotMutate(t *testing.T) {
	for _, tc := range []struct {
		correlation     string
		blueprintFields string
	}{
		{"matched", `"blueprint":{"client_id":"` + commandBlueprintAppID + `"},`},
		{"not-matched", `"blueprint":{"client_id":"99990000-aaaa-bbbb-cccc-ddddeeeeffff"},`},
		{"insufficient-data", ""},
	} {
		t.Run(tc.correlation, func(t *testing.T) {
			httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
				"/agents/base-agent": route(http.StatusOK, `{
					"id":"agent-1",
					"name":"base-agent",
					"state":"active",
					`+tc.blueprintFields+`
					"versions":{"latest":{"version":"2","status":"ready"}}
				}`),
				"/v1.0/applications/microsoft.graph.agentIdentityBlueprint": route(
					http.StatusOK,
					`{"value":[{
						"id":"`+commandBlueprintObjectID+`",
						"appId":"`+commandBlueprintAppID+`",
						"displayName":"Support Blueprint",
						"managerApplications":[],
						"requiredResourceAccess":[]
					}]}`,
				),
			}}
			stubCredentialAndHTTP(t, httpClient)
			manifest := writeManifest(t, baseManifest)

			for _, format := range []string{"text", "json", "yaml"} {
				t.Run(format, func(t *testing.T) {
					flags := []string{"--blueprint-id", commandBlueprintAppID, "-f", manifest, "--output", format}
					run := runCLI(t, "", append([]string{"agent365", "binding", "plan"}, flags...)...)
					if run.code != 0 {
						t.Fatalf("identity comparison failed: %s", run.stderr)
					}
					legacy := runCLI(t, "", append([]string{"agent365-binding-plan"}, flags...)...)
					if legacy.code != run.code || legacy.stdout != run.stdout || legacy.stderr != run.stderr {
						t.Fatalf("canonical and compatibility comparison differ:\ncanonical=%#v\nlegacy=%#v", run, legacy)
					}
					if format == "text" {
						for _, expected := range []string{
							"Agent 365 identity comparison (read-only):",
							"correlation=" + tc.correlation + " executable=false",
							"cannot create Foundry agents, attach existing blueprints, or change agent identities",
							"No binding operation is available.",
						} {
							if !strings.Contains(run.stdout, expected) {
								t.Errorf("comparison text omitted %q:\n%s", expected, run.stdout)
							}
						}
						if strings.Contains(run.stdout, "change-required=") {
							t.Fatalf("comparison text implies an actionable change:\n%s", run.stdout)
						}
						return
					}

					unmarshal := json.Unmarshal
					if format == "yaml" {
						unmarshal = yaml.Unmarshal
					}
					var result agent365BindingPlanResult
					if err := unmarshal([]byte(run.stdout), &result); err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(strings.Join(result.Steps, "\n"), "No binding operation is available.") {
						t.Fatalf("comparison guidance omitted the non-executable boundary: %+v", result.Steps)
					}
					if strings.Contains(strings.Join(result.Steps, "\n"), "when Microsoft exposes") {
						t.Fatalf("comparison guidance implies a future binding capability: %+v", result.Steps)
					}

					var fields map[string]interface{}
					if err := unmarshal([]byte(run.stdout), &fields); err != nil {
						t.Fatal(err)
					}
					for name, expected := range map[string]interface{}{
						"correlation":              tc.correlation,
						"changeRequired":           tc.correlation != "matched",
						"executable":               false,
						"bindingMutationSupported": false,
					} {
						if value, exists := fields[name]; !exists || value != expected {
							t.Errorf("comparison field %q = %#v, present=%t; want %#v", name, value, exists, expected)
						}
					}
				})
			}
			for _, request := range httpClient.requests {
				if request.Method != http.MethodGet {
					t.Fatalf("identity comparison issued mutation %s %s", request.Method, request.URL)
				}
			}
		})
	}
}

func TestAgent365InvalidSelectorFailsBeforeAuthentication(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("invalid selector must fail before authentication")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("invalid selector must fail before HTTP client creation")
		return nil
	}

	run := runCLI(
		t,
		"",
		"agent365", "blueprint", "show",
		"--blueprint-id", "not-a-guid",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "valid GUID") {
		t.Fatalf("expected selector validation failure: %#v", run)
	}
}

func TestAgent365InvalidListLimitFailsBeforeAuthentication(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("invalid list limit must fail before authentication")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("invalid list limit must fail before HTTP client creation")
		return nil
	}

	run := runCLI(t, "", "agent365", "blueprint", "list", "--limit", "101")
	if run.code == 0 || !strings.Contains(run.stderr, "between 1 and 100") {
		t.Fatalf("expected list limit validation failure: %#v", run)
	}
}

func TestAgent365AllAndLimitFailBeforeAuthentication(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("conflicting pagination flags must fail before authentication")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("conflicting pagination flags must fail before HTTP client creation")
		return nil
	}

	run := runCLI(
		t,
		"",
		"agent365", "identity", "list",
		"--all",
		"--limit", "10",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "mutually exclusive") {
		t.Fatalf("expected pagination flag conflict: %#v", run)
	}
}

func TestAgent365BindingMutationCommandsDoNotExist(t *testing.T) {
	help := runCLI(t, "", "agent365", "binding", "--help")
	if help.code != 0 {
		t.Fatalf("binding help failed: %s", help.stderr)
	}
	for _, unsupported := range []string{"create", "delete", "bind", "unbind"} {
		if strings.Contains(help.stdout, "\n  "+unsupported+" ") {
			t.Fatalf("binding help exposed unsupported mutation %q:\n%s", unsupported, help.stdout)
		}
	}
}

func TestAgent365IdentityListFollowsBoundedContinuation(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/v1.0/servicePrincipals/microsoft.graph.agentIdentity": routeSequence(
			route(http.StatusOK, `{
					"value":[{"id":"`+commandIdentityObjectID+`","displayName":"Agent One","appId":"33334444-dddd-5555-eeee-6666ffff7777"}],
					"@odata.nextLink":"https://graph.microsoft.com/v1.0/servicePrincipals/microsoft.graph.agentIdentity?$skiptoken=next"
				}`),
			route(http.StatusOK, `{
					"value":[{"id":"44445555-eeee-6666-ffff-7777aaaa8888","displayName":"Agent Two","appId":"55556666-ffff-7777-aaaa-8888bbbb9999"}]
				}`),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(t, "", "agent365", "identity", "list", "--all", "--output", "json")
	if run.code != 0 {
		t.Fatalf("identity list failed: %s", run.stderr)
	}
	var result struct {
		Count     int  `json:"count"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 || result.Truncated || len(httpClient.requests) != 2 {
		t.Fatalf("unexpected paginated identity result: %+v requests=%d", result, len(httpClient.requests))
	}
	for _, request := range httpClient.requests {
		if request.Method != http.MethodGet || request.URL.Host != "graph.microsoft.com" {
			t.Fatalf("unsafe identity request: %s %s", request.Method, request.URL)
		}
	}
}

func TestAgent365BlueprintPermissionsResolvesFriendlyNamesOnlyWhenRequested(t *testing.T) {
	resourceAppID := "66667777-aaaa-8888-bbbb-9999cccc0000"
	permissionID := "77778888-bbbb-9999-cccc-0000dddd1111"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/v1.0/applications/microsoft.graph.agentIdentityBlueprint": route(
			http.StatusOK,
			`{"value":[{
					"id":"`+commandBlueprintObjectID+`",
					"appId":"`+commandBlueprintAppID+`",
					"displayName":"Support Blueprint",
					"managerApplications":[],
					"requiredResourceAccess":[{"resourceAppId":"`+resourceAppID+`","resourceAccess":[{"id":"`+permissionID+`","type":"Scope"}]}]
				}]}`,
		),
		"/v1.0/applications/" + commandBlueprintObjectID + "/microsoft.graph.agentIdentityBlueprint/inheritablePermissions": route(
			http.StatusOK,
			`{"value":[]}`,
		),
		"/v1.0/servicePrincipals": route(
			http.StatusOK,
			`{"value":[{
					"id":"88889999-cccc-0000-dddd-1111eeee2222",
					"displayName":"Microsoft Graph",
					"appId":"`+resourceAppID+`",
					"oauth2PermissionScopes":[{"id":"`+permissionID+`","value":"User.Read","adminConsentDisplayName":"Read users"}],
					"appRoles":[]
				}]}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(
		t,
		"",
		"agent365", "blueprint", "permissions",
		"--blueprint-id", commandBlueprintAppID,
		"--resolve-names",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("permission resolution failed: %s", run.stderr)
	}
	var result agent365PermissionsResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.ResolvedPermissions) != 1 ||
		result.ResolvedPermissions[0].PermissionValue != "User.Read" ||
		result.ResolvedPermissions[0].ResourceDisplayName != "Microsoft Graph" {
		t.Fatalf("unexpected friendly permissions: %+v", result.ResolvedPermissions)
	}
}

func TestAgent365BindingStatusCanCorrelateDirectoryIdentity(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
				"id":"agent-1",
				"name":"base-agent",
				"state":"active",
				"instance_identity":{"principal_id":"`+commandIdentityObjectID+`","client_id":"runtime-client","status":"ready"},
				"blueprint":{"principal_id":"blueprint-principal","client_id":"`+commandBlueprintAppID+`","status":"ready"},
				"versions":{"latest":{"version":"2","status":"ready"}}
			}`),
		"/v1.0/servicePrincipals/" + commandIdentityObjectID + "/microsoft.graph.agentIdentity": route(
			http.StatusOK,
			`{"id":"`+commandIdentityObjectID+`","displayName":"Support Agent Identity","appId":"99990000-dddd-1111-eeee-2222ffff3333","agentIdentityBlueprintId":"`+commandBlueprintAppID+`"}`,
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	manifest := writeManifest(t, baseManifest)

	run := runCLI(
		t,
		"",
		"agent365", "binding", "status",
		"-f", manifest,
		"--resolve-identity",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("binding identity resolution failed: %s", run.stderr)
	}
	var result agent365BindingStatusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Identity.Correlation != "identity-and-blueprint-matched" ||
		result.Identity.Classification != "modern-unique-agent-identity" ||
		!result.Identity.Authoritative {
		t.Fatalf("unexpected identity classification: %+v", result.Identity)
	}
}

func TestAgent365ObservabilityPlanValidatesModernDistroLocally(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	source := filepath.Join(workspace, "src", "agent")
	if err := os.WriteFile(
		filepath.Join(source, "requirements.txt"),
		[]byte("microsoft-opentelemetry\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, "main.py"),
		[]byte("from microsoft.opentelemetry import use_microsoft_opentelemetry\nuse_microsoft_opentelemetry(enable_a365=True)\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	run := runCLI(
		t,
		"",
		"agent365", "observability", "plan",
		"--workspace", workspace,
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("observability plan failed: %s", run.stderr)
	}
	var result agent365ObservabilityResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Local.Ready || !result.Local.ModernDistro ||
		result.ExpectedAppRoleID == "" || result.Executable {
		t.Fatalf("unexpected observability plan: %+v", result)
	}
}

func TestAgent365IntegrationStatusSeparatesLoggingFromLicensing(t *testing.T) {
	resourcePath := "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		resourcePath: route(http.StatusOK, `{
				"id":"`+resourcePath+`",
				"name":"foundry-account",
				"location":"eastus",
				"etag":"W/\"one\"",
				"properties":{"a365LoggingEnabled":true,"a365Status":"NotLicensed"}
			}`),
	}}
	stubCredentialAndHTTP(t, httpClient)

	run := runCLI(
		t,
		"",
		"agent365", "integration", "status",
		"--account-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("integration status failed: %s", run.stderr)
	}
	var result agent365IntegrationStatusResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Account.A365LoggingEnabled || result.CollectionActive ||
		result.Account.A365Status != "NotLicensed" {
		t.Fatalf("logging and licensing were conflated: %+v", result)
	}
}

func TestAgent365InvalidAccountCoordinatesFailBeforeCredentialCreation(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("invalid account coordinates must fail before credential creation")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("invalid account coordinates must fail before HTTP client creation")
		return nil
	}

	run := runCLI(
		t,
		"",
		"agent365", "integration", "status",
		"--account-id", "/subscriptions/not-a-guid/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "not a valid UUID") {
		t.Fatalf("expected account coordinate validation failure: %#v", run)
	}
}

func TestAgent365IntegrationSetRequiresConfirmationBeforePatch(t *testing.T) {
	resourcePath := "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		resourcePath: route(http.StatusOK, `{
				"id":"`+resourcePath+`",
				"name":"foundry-account",
				"location":"eastus",
				"etag":"W/\"one\"",
				"properties":{"a365LoggingEnabled":false,"a365Status":"Enabled"}
			}`),
	}}
	stubCredentialAndHTTP(t, httpClient)
	receiptPath := filepath.Join(t.TempDir(), "agent365-integration-cancelled.json")

	run := runCLI(
		t,
		"",
		"agent365", "integration", "set",
		"--account-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account",
		"--enabled=true",
		"--receipt", receiptPath,
		"--output", "json",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "--yes") {
		t.Fatalf("integration set did not require confirmation: %#v", run)
	}
	for _, request := range httpClient.requests {
		if request.Method == http.MethodPatch {
			t.Fatal("integration set patched before confirmation")
		}
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var recorded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Status != "cancelled" {
		t.Fatalf("confirmation failure must retain a cancelled audit receipt, got %q", recorded.Status)
	}
}

func TestAgent365IntegrationSetWritesVerifiedReceipt(t *testing.T) {
	resourcePath := "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account"
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		resourcePath: routeSequence(
			route(http.StatusOK, `{
					"id":"`+resourcePath+`",
					"name":"foundry-account",
					"location":"eastus",
					"etag":"W/\"one\"",
					"properties":{"a365LoggingEnabled":false,"a365Status":"Enabled"}
				}`),
			route(http.StatusOK, `{}`),
			route(http.StatusOK, `{
					"id":"`+resourcePath+`",
					"name":"foundry-account",
					"location":"eastus",
					"etag":"W/\"two\"",
					"properties":{"a365LoggingEnabled":true,"a365Status":"Enabled"}
				}`),
		),
	}}
	stubCredentialAndHTTP(t, httpClient)
	receiptPath := filepath.Join(t.TempDir(), "agent365-integration.json")

	run := runCLI(
		t,
		"",
		"agent365", "integration", "set",
		"--account-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/foundry-rg/providers/Microsoft.CognitiveServices/accounts/foundry-account",
		"--enabled=true",
		"--yes",
		"--receipt", receiptPath,
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("integration set failed: %s", run.stderr)
	}
	var result agent365IntegrationSetResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Mutation == nil || result.Mutation.Outcome != "verified" ||
		result.Mutation.Verified == nil || !result.Mutation.Verified.CollectionActive() {
		t.Fatalf("unexpected integration mutation: %+v", result)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"status": "succeeded"`) ||
		!strings.Contains(string(data), `"foundry_account_agent365_logging"`) {
		t.Fatalf("receipt omitted verified mutation: %s", data)
	}
	methods := make([]string, 0, len(httpClient.requests))
	for _, request := range httpClient.requests {
		methods = append(methods, request.Method)
	}
	if strings.Join(methods, ",") != "GET,PATCH,GET" {
		t.Fatalf("unexpected mutation sequence: %v", methods)
	}
}

func TestAgent365PublicationInfoDoesNotAuthenticate(t *testing.T) {
	originalCredential, originalHTTP := newCredentialFn, newHTTPClientFn
	t.Cleanup(func() {
		newCredentialFn, newHTTPClientFn = originalCredential, originalHTTP
	})
	newCredentialFn = func(_ *cobra.Command, _ azcloud.Profile) (azcore.TokenCredential, error) {
		t.Fatal("publication info must not acquire a credential")
		return nil, nil
	}
	newHTTPClientFn = func(_ *cobra.Command) *httpx.RetryClient {
		t.Fatal("publication info must not create an HTTP client")
		return nil
	}

	run := runCLI(t, "", "agent365", "publication", "info", "--output", "json")
	if run.code != 0 {
		t.Fatalf("publication info failed: %s", run.stderr)
	}
	var result agent365PublicationInfoResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.ReadOnly || result.RegistryMutationSupported ||
		result.ExistingBindingSupported {
		t.Fatalf("unsafe publication capabilities: %+v", result)
	}
	if !strings.Contains(result.PromptExecutionBoundary, "Autopilot publishing is not supported") ||
		!strings.Contains(strings.Join(result.IdentityLifecycle, " "), "instance_identity.client_id") ||
		strings.Contains(strings.Join(result.IdentityLifecycle, " "), "Publishing creates a distinct") {
		t.Fatalf("publication guidance is stale: %+v", result)
	}
}

func TestAgent365PublicationPlanPreservesModernIdentity(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1",
			"name":"base-agent",
			"instance_identity":{"principal_id":"identity-object","client_id":"identity-client","status":"ready"},
			"blueprint":{"principal_id":"blueprint-object","client_id":"blueprint-client","status":"ready"},
			"versions":{"latest":{"version":"2","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, httpClient)
	manifest := writeManifest(t, baseManifest)

	run := runCLI(t, "", "agent365", "publication", "plan", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("publication plan failed: %s", run.stderr)
	}
	var result agent365PublicationResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	guidance := strings.Join(append(result.Steps, result.AdminHandoff...), " ")
	normalizedGuidance := strings.ToLower(guidance)
	if result.Identity.Classification != "modern-unique-agent-identity" ||
		!result.Identity.Authoritative ||
		result.DocumentationConflict != "" ||
		!strings.Contains(result.ExecutionBoundary, "Prompt Autopilot publishing is unsupported") ||
		!strings.Contains(normalizedGuidance, "do not recreate azure rbac") ||
		strings.Contains(normalizedGuidance, "roles do not transfer") {
		t.Fatalf("modern publication guidance is unsafe: %+v", result)
	}
}

func TestAgent365PublicationPlanSeparatesLegacyIdentityMigration(t *testing.T) {
	httpClient := &scriptedHTTP{routes: map[string]scriptedRoute{
		"/agents/base-agent": route(http.StatusOK, `{
			"id":"agent-1",
			"name":"base-agent",
			"versions":{"latest":{"version":"2","status":"ready"}}
		}`),
	}}
	stubCredentialAndHTTP(t, httpClient)
	manifest := writeManifest(t, baseManifest)

	run := runCLI(t, "", "agent365", "publication", "plan", "-f", manifest, "--output", "json")
	if run.code != 0 {
		t.Fatalf("publication plan failed: %s", run.stderr)
	}
	var result agent365PublicationResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	guidance := strings.Join(append(result.Steps, result.AdminHandoff...), " ")
	if result.Identity.Classification != "legacy-shared-project-identity" ||
		!result.Identity.Authoritative ||
		!strings.Contains(guidance, "roles during migration") ||
		!strings.Contains(strings.Join(result.Identity.RBACGuidance, " "), "shared project identity") {
		t.Fatalf("legacy publication guidance is incomplete: %+v", result)
	}
}

func TestClassifyAgent365IdentityDoesNotMatchEmptyIdentifiers(t *testing.T) {
	result := classifyAgent365Identity(
		&foundry.Agent{InstanceIdentity: &foundry.AgentIdentity{}},
		&agent365.AgentIdentity{},
	)
	if result.Correlation != "insufficient-data" {
		t.Fatalf("empty identity identifiers were correlated: %+v", result)
	}
}
