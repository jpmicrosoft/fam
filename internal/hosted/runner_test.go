package hosted

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

type fakeRunner struct {
	commands []Command
	run      func(Command) (Execution, error)
}

func (f *fakeRunner) Run(_ context.Context, command Command) (Execution, error) {
	f.commands = append(f.commands, command)
	if f.run != nil {
		return f.run(command)
	}
	return Execution{ExitCode: 0}, nil
}

func TestResolveAZDGatesUnsupportedCloudBeforeLookPath(t *testing.T) {
	called := false
	_, err := ResolveAZD("UnsupportedCloud", func(string) (string, error) {
		called = true
		return "azd", nil
	})
	if err == nil || !errors.Is(err, ErrHostedUnsupported) {
		t.Fatalf("expected unsupported cloud error, got %v", err)
	}
	if called {
		t.Fatal("PATH lookup occurred before the cloud capability gate")
	}
}

func TestStandardAZDCandidatesUseOfficialWindowsLocations(t *testing.T) {
	values := map[string]string{
		"LOCALAPPDATA": `C:\Users\qa\AppData\Local`,
		"ProgramFiles": `C:\Program Files`,
	}
	candidates := standardAZDCandidates("windows", func(name string) string {
		return values[name]
	})
	if len(candidates) != 2 ||
		candidates[0] != filepath.Join(values["LOCALAPPDATA"], "Programs", "Azure Dev CLI", "azd.exe") ||
		candidates[1] != filepath.Join(values["ProgramFiles"], "Azure Dev CLI", "azd.exe") {
		t.Fatalf("unexpected Windows azd candidates: %#v", candidates)
	}
}

func TestEnsureEnvironmentCreatesAndVerifies(t *testing.T) {
	runner := &fakeRunner{}
	runner.run = func(command Command) (Execution, error) {
		switch command.Phase {
		case "environment-list-before":
			return Execution{ExitCode: 0, Stdout: `[]`}, nil
		case "environment-create":
			return Execution{ExitCode: 0}, nil
		case "environment-list-after":
			return Execution{
				ExitCode: 0,
				Stdout:   `[{"Name":"dev","IsDefault":true}]`,
			}, nil
		case "environment-configure":
			return Execution{ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected command phase %q", command.Phase)
			return Execution{}, nil
		}
	}

	result, err := EnsureEnvironment(context.Background(), EnvironmentCreateOptions{
		Workspace:      Workspace{Root: t.TempDir()},
		AZDPath:        `C:\tools\azd.exe`,
		Name:           "dev",
		SubscriptionID: "11111111-2222-3333-4444-555555555555",
		Location:       "eastus2",
		Runner:         runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.Reconciled || len(result.Commands) != 4 ||
		len(result.Configured) != 2 {
		t.Fatalf("unexpected environment result: %#v", result)
	}
	if got := strings.Join(runner.commands[1].Args, " "); got !=
		"env new dev --no-prompt --subscription 11111111-2222-3333-4444-555555555555 --location eastus2" {
		t.Fatalf("unexpected azd env new arguments: %s", got)
	}
	if got := strings.Join(runner.commands[3].Args, " "); got !=
		"env set AZURE_SUBSCRIPTION_ID=11111111-2222-3333-4444-555555555555 AZURE_LOCATION=eastus2 --environment dev --no-prompt" {
		t.Fatalf("unexpected azd env set arguments: %s", got)
	}
	for _, command := range runner.commands {
		if command.Environment["AZD_NON_INTERACTIVE"] != "true" {
			t.Fatalf("command %s was not forced non-interactive", command.Phase)
		}
	}
}

func TestEnsureEnvironmentIsIdempotent(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout:   `[{"Name":"dev","IsDefault":true}]`,
			}, nil
		},
	}
	result, err := EnsureEnvironment(context.Background(), EnvironmentCreateOptions{
		Workspace: Workspace{Root: t.TempDir()},
		AZDPath:   `C:\tools\azd.exe`,
		Name:      "dev",
		Runner:    runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.Reconciled || len(runner.commands) != 1 {
		t.Fatalf("existing environment was not idempotent: %#v", result)
	}
}

func TestEnsureEnvironmentConfiguresExistingProjectContext(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			switch command.Phase {
			case "environment-list-before":
				return Execution{
					ExitCode: 0,
					Stdout:   `[{"Name":"dev","IsDefault":true}]`,
				}, nil
			case "environment-configure":
				return Execution{ExitCode: 0}, nil
			default:
				t.Fatalf("unexpected command phase %q", command.Phase)
				return Execution{}, nil
			}
		},
	}
	result, err := EnsureEnvironment(context.Background(), EnvironmentCreateOptions{
		Workspace: Workspace{
			Root: t.TempDir(),
			Selected: Service{RAIPolicy: &RAIPolicy{
				PolicyID:            "${RAI_POLICY_ID}",
				UnresolvedReference: true,
			}},
		},
		AZDPath:         `C:\tools\azd.exe`,
		Name:            "dev",
		SubscriptionID:  "11111111-2222-3333-4444-555555555555",
		TenantID:        "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Location:        "eastus2",
		ProjectID:       "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		ProjectEndpoint: "https://account.services.ai.azure.com/api/projects/support",
		ModelDeployment: "support-model",
		Runner:          runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.Reconciled || len(result.Configured) != 8 ||
		len(runner.commands) != 2 {
		t.Fatalf("unexpected configured environment result: %#v", result)
	}
	if got := strings.Join(runner.commands[1].Args, " "); got !=
		"env set AZURE_SUBSCRIPTION_ID=11111111-2222-3333-4444-555555555555 AZURE_TENANT_ID=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee AZURE_LOCATION=eastus2 AZURE_AI_PROJECT_ID=/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support FOUNDRY_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_MODEL_DEPLOYMENT_NAME=support-model RAI_POLICY_ID=/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/Microsoft.DefaultV2 --environment dev --no-prompt" {
		t.Fatalf("unexpected azd env set arguments: %s", got)
	}
}

func TestEnsureEnvironmentRejectsRAIPolicyFromAnotherAccount(t *testing.T) {
	_, err := EnsureEnvironment(context.Background(), EnvironmentCreateOptions{
		Workspace: Workspace{
			Root: t.TempDir(),
			Selected: Service{RAIPolicy: &RAIPolicy{
				PolicyID: "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/other/raiPolicies/custom",
			}},
		},
		AZDPath:         `C:\tools\azd.exe`,
		Name:            "dev",
		SubscriptionID:  "11111111-2222-3333-4444-555555555555",
		Location:        "eastus2",
		ProjectID:       "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		ProjectEndpoint: "https://account.services.ai.azure.com/api/projects/support",
		ModelDeployment: "support-model",
		Runner:          &fakeRunner{},
	})
	if err == nil || !strings.Contains(err.Error(), "must match the Foundry project account") {
		t.Fatalf("cross-account RAI policy was not rejected: %v", err)
	}
}

func TestEnsureEnvironmentReconcilesAmbiguousCreate(t *testing.T) {
	runner := &fakeRunner{}
	runner.run = func(command Command) (Execution, error) {
		switch command.Phase {
		case "environment-list-before":
			return Execution{ExitCode: 0, Stdout: `[]`}, nil
		case "environment-create":
			return Execution{ExitCode: 1}, errors.New("exit status 1")
		case "environment-list-after":
			return Execution{
				ExitCode: 0,
				Stdout:   `[{"Name":"dev","IsDefault":true}]`,
			}, nil
		default:
			t.Fatalf("unexpected command phase %q", command.Phase)
			return Execution{}, nil
		}
	}
	result, err := EnsureEnvironment(context.Background(), EnvironmentCreateOptions{
		Workspace: Workspace{Root: t.TempDir()},
		AZDPath:   `C:\tools\azd.exe`,
		Name:      "dev",
		Runner:    runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || !result.Reconciled {
		t.Fatalf("ambiguous creation was not reconciled: %#v", result)
	}
}

func TestEnsureEnvironmentRejectsUnsafeOptions(t *testing.T) {
	tests := []EnvironmentCreateOptions{
		{Name: "--dev"},
		{Name: "dev", SubscriptionID: "not-a-guid"},
		{Name: "dev", TenantID: "not-a-guid"},
		{Name: "dev", Location: "East US"},
		{Name: "dev", ProjectID: "/subscriptions/not-a-guid/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project"},
		{
			Name:           "dev",
			SubscriptionID: "11111111-2222-3333-4444-555555555555",
			ProjectID:      "/subscriptions/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project",
		},
		{
			Name:            "dev",
			ProjectEndpoint: "https://other.services.ai.azure.com/api/projects/project",
			ProjectID:       "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project",
		},
		{
			Name:            "dev",
			ProjectEndpoint: "https://account.services.ai.azure.com/api/projects/other",
			ProjectID:       "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/project",
		},
		{Name: "dev", ProjectEndpoint: "https://attacker.example/api/projects/project"},
		{Name: "dev", ModelDeployment: "model\nname"},
	}
	for _, options := range tests {
		options.Workspace = Workspace{Root: t.TempDir()}
		options.AZDPath = `C:\tools\azd.exe`
		options.Runner = &fakeRunner{}
		if _, err := EnsureEnvironment(context.Background(), options); err == nil {
			t.Fatalf("unsafe options were accepted: %#v", options)
		}
	}
}

func TestCheckPreflightUsesPinnedReadOnlyContract(t *testing.T) {
	workspace := Workspace{Root: t.TempDir()}
	runner := &fakeRunner{}
	runner.run = func(command Command) (Execution, error) {
		switch command.Phase {
		case "azd-version":
			return Execution{ExitCode: 0, Stdout: "azd version 1.27.1", Duration: time.Millisecond}, nil
		case "azd-extensions":
			return Execution{
				ExitCode: 0,
				Stdout:   `[{"id":"azure.ai.agents","version":"1.0.0-beta.13","installedVersion":"1.0.0-beta.13"}]`,
			}, nil
		case "agent-extension-version":
			return Execution{ExitCode: 0, Stdout: "Version: 1.0.0-beta.13"}, nil
		case "deploy-contract":
			return Execution{ExitCode: 0, Stdout: "azd deploy <service>\n--no-prompt\n--environment"}, nil
		case "status-contract":
			return Execution{ExitCode: 0, Stdout: "azd ai agent show [name]\n--output\n--no-prompt"}, nil
		case "azd-environment":
			return Execution{ExitCode: 0, Stdout: `[{"Name":"prod","IsDefault":true}]`}, nil
		default:
			return Execution{ExitCode: 0}, nil
		}

	}
	result, err := CheckPreflight(context.Background(), PreflightOptions{
		Workspace:        workspace,
		AZDPath:          `C:\tools\azd.exe`,
		Environment:      "prod",
		CheckEnvironment: true,
		Runner:           runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AZDVersion != MinimumAZDVersion ||
		result.AgentExtensionVer != RequiredExtensionVer ||
		!result.Authenticated ||
		!result.EnvironmentChecked {
		t.Fatalf("unexpected preflight: %#v", result)
	}
	if len(runner.commands) != 7 {
		t.Fatalf("unexpected command count: %d", len(runner.commands))
	}
	extensionArgs := strings.Join(runner.commands[1].Args, " ")
	if extensionArgs != "extension list --output json" {
		t.Fatalf("unsafe extension inspection command: %s", extensionArgs)
	}
	envArgs := strings.Join(runner.commands[6].Args, " ")
	if envArgs != "env list --output json --no-prompt" {
		t.Fatalf("unexpected environment command: %s", envArgs)
	}

	for _, command := range runner.commands {
		if command.Environment["AZD_NON_INTERACTIVE"] != "true" {
			t.Fatalf("command %s was not forced non-interactive", command.Phase)
		}
	}
}

func TestDiagnosePreflightCollectsIndependentFailures(t *testing.T) {
	workspace := Workspace{Root: t.TempDir()}
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			switch command.Phase {
			case "azd-version":
				return Execution{ExitCode: 0, Stdout: "azd version 1.27.1"}, nil
			case "azd-extensions":
				return Execution{ExitCode: 0, Stdout: `[]`}, nil
			case "deploy-contract":
				return Execution{ExitCode: 1}, errors.New("unavailable")
			case "azd-auth":
				return Execution{ExitCode: 0, Stdout: "not logged in"}, nil
			case "azd-environment":
				return Execution{ExitCode: 0, Stdout: `[]`}, nil
			default:
				return Execution{ExitCode: 0}, nil
			}
		},
	}
	result := DiagnosePreflight(context.Background(), PreflightOptions{
		Workspace:        workspace,
		AZDPath:          `C:\tools\azd.exe`,
		Environment:      "prod",
		CheckEnvironment: true,
		Runner:           runner,
	})
	failures := 0
	for _, check := range result.Checks {
		if check.Status == "failed" {
			failures++
		}
	}
	if failures < 4 {
		t.Fatalf("expected independent failures, got %d: %#v", failures, result.Checks)
	}
	if len(runner.commands) < 5 {
		t.Fatalf("diagnostics stopped early after %d command(s)", len(runner.commands))
	}
	for _, command := range runner.commands {
		if command.Phase == "agent-extension-version" ||
			command.Phase == "status-contract" {
			t.Fatalf("untrusted Hosted extension command was executed: %#v", command)
		}
	}
	for _, name := range []string{"agent-extension-command", "status-contract"} {
		foundBlocked := false
		for _, check := range result.Checks {
			if check.Name == name && check.Status == "skipped" {
				foundBlocked = true
			}
		}
		if !foundBlocked {
			t.Fatalf("unsafe dependent check %s was not reported as blocked: %#v", name, result.Checks)
		}
	}
}

func TestCheckPreflightStillStopsAfterFirstFailure(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			if command.Phase == "azd-version" {
				return Execution{ExitCode: 0, Stdout: "azd version 1.0.0"}, nil
			}
			return Execution{ExitCode: 0}, nil
		},
	}
	_, err := CheckPreflight(context.Background(), PreflightOptions{
		Workspace: Workspace{Root: t.TempDir()},
		AZDPath:   `C:\tools\azd.exe`,
		Runner:    runner,
	})
	if !errors.Is(err, ErrAZDTooOld) {
		t.Fatalf("expected azd version failure, got %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("normal preflight must remain fail-fast, got %d commands", len(runner.commands))
	}
}

func TestParseExtensionsIgnoresCatalogEntriesThatAreNotInstalled(t *testing.T) {
	extensions, err := parseExtensions(`[
		{"id":"azure.ai.agents","version":"1.0.0-beta.9","installedVersion":""},
		{"id":"azure.ai.toolboxes","version":"1.0.0-beta.5","installedVersion":"1.0.0-beta.5"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := extensions["azure.ai.agents"]; found {
		t.Fatal("uninstalled catalog extension was treated as installed")
	}
	if extensions["azure.ai.toolboxes"] != "1.0.0-beta.5" {
		t.Fatalf("unexpected installed extensions: %#v", extensions)
	}
}

func TestRequireEnvironmentDoesNotReadEnvironmentValues(t *testing.T) {
	if err := requireEnvironment(`[{"Name":"prod","IsDefault":true}]`, "prod"); err != nil {
		t.Fatal(err)
	}

	if err := requireEnvironment(`[{"Name":"prod","IsDefault":true}]`, "missing"); !errors.Is(err, ErrEnvironment) {
		t.Fatalf("expected missing environment error, got %v", err)
	}
	if err := requireEnvironment(`not-json`, "prod"); !errors.Is(err, ErrEnvironment) {
		t.Fatalf("expected malformed environment list error, got %v", err)
	}
}

func TestCheckPreflightRejectsSuccessfulNotLoggedInStatus(t *testing.T) {
	workspace := Workspace{Root: t.TempDir()}
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			switch command.Phase {
			case "azd-version":
				return Execution{ExitCode: 0, Stdout: "azd version 1.30.0"}, nil
			case "azd-extensions":
				return Execution{
					ExitCode: 0,
					Stdout: `[{
						"id":"azure.ai.agents",
						"installedVersion":"1.0.0-beta.13"
					}]`,
				}, nil
			case "agent-extension-version":
				return Execution{ExitCode: 0, Stdout: "Version: 1.0.0-beta.13"}, nil
			case "deploy-contract":
				return Execution{ExitCode: 0, Stdout: "azd deploy <service>\n--no-prompt\n--environment"}, nil
			case "status-contract":
				return Execution{ExitCode: 0, Stdout: "agent show [name]\n--output\n--no-prompt"}, nil
			case "azd-auth":
				return Execution{ExitCode: 0, Stdout: "Not logged in, run `azd auth login` to login to Azure"}, nil
			default:
				return Execution{ExitCode: 0}, nil
			}
		},
	}
	_, err := CheckPreflight(context.Background(), PreflightOptions{
		Workspace: workspace,
		AZDPath:   `C:\tools\azd.exe`,
		Runner:    runner,
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestCheckPreflightRejectsVersionDrift(t *testing.T) {
	tests := []struct {
		name       string
		azdVersion string
		extension  string
		expected   error
	}{
		{
			name:       "old azd",
			azdVersion: "1.27.0",
			extension:  RequiredExtensionVer,
			expected:   ErrAZDTooOld,
		},
		{
			name:       "prerelease azd",
			azdVersion: "1.27.1-beta.1",
			extension:  RequiredExtensionVer,
			expected:   ErrAZDTooOld,
		},
		{
			name:       "new unreviewed extension",
			azdVersion: MinimumAZDVersion,
			extension:  "1.0.0-beta.9",
			expected:   ErrMissingExtension,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{}
			runner.run = func(command Command) (Execution, error) {
				switch command.Phase {
				case "azd-version":
					return Execution{ExitCode: 0, Stdout: "azd version " + tt.azdVersion}, nil
				case "azd-extensions":
					return Execution{
						ExitCode: 0,
						Stdout:   `[{"id":"azure.ai.agents","installedVersion":"` + tt.extension + `"}]`,
					}, nil
				default:
					return Execution{ExitCode: 0, Stdout: "Version: " + tt.extension}, nil
				}
			}

			_, err := CheckPreflight(context.Background(), PreflightOptions{
				Workspace: Workspace{Root: t.TempDir()},
				AZDPath:   "azd",
				Runner:    runner,
			})
			if err == nil || !errors.Is(err, tt.expected) {
				t.Fatalf("expected %v, got %v", tt.expected, err)
			}
		})
	}
}

func TestCompareVersionsUsesSemverPrereleaseOrdering(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  int
	}{
		{left: "1.27.1-beta.2", right: "1.27.1-beta.10", want: -1},
		{left: "1.27.1-beta.10", right: "1.27.1-beta.2", want: 1},
		{left: "1.27.1-beta.1", right: "1.27.1", want: -1},
		{left: "1.27.1", right: "1.27.1-beta.1", want: 1},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.left, tt.right); got != tt.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
		}
	}
}

func TestRunDeployAndShowUseArgumentArrays(t *testing.T) {
	workspace := Workspace{
		Root: t.TempDir(),
		Selected: Service{
			ServiceName: "hosted-agent",
		},
	}
	runner := &fakeRunner{}
	runner.run = func(command Command) (Execution, error) {
		if command.Phase == "status" {
			return Execution{
				ExitCode: 0,
				Stdout:   `{"id":"id","name":"agent","version":"7","status":"active","agent_endpoints":{"Responses":"https://example"}}`,
			}, nil
		}
		return Execution{ExitCode: 0}, nil
	}
	if _, err := RunDeploy(
		context.Background(),
		runner,
		"azd",
		workspace,
		"prod",
		nil,
	); err != nil {
		t.Fatal(err)
	}
	status, _, err := ShowStatus(
		context.Background(),
		runner,
		"azd",
		workspace,
		"prod",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if status.Version != "7" || status.AgentEndpoints["Responses"] == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
	if got := strings.Join(runner.commands[0].Args, " "); got !=
		"deploy hosted-agent --no-prompt --environment prod" {
		t.Fatalf("unexpected deploy args: %s", got)
	}
	if got := strings.Join(runner.commands[1].Args, " "); got !=
		"ai agent show hosted-agent --output json --no-prompt --environment prod" {
		t.Fatalf("unexpected status args: %s", got)
	}
}

func TestShowStatusClassifiesUndeployedEnvironment(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stderr:   "agent version could not be resolved from azd environment for service 'agent'",
			}, errors.New("exit status 1")
		},
	}
	_, _, err := ShowStatus(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir(), Selected: Service{ServiceName: "agent"}},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrAgentNotDeployed) {
		t.Fatalf("expected undeployed classification, got %v", err)
	}
}

func TestShowStatusRejectsMalformedPayload(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout:   `{"name":"agent","version":"7"}`,
			}, nil
		},
	}
	_, _, err := ShowStatus(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir(), Selected: Service{ServiceName: "agent"}},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("expected invalid status classification, got %v", err)
	}
}

func TestResolveProjectEndpointUsesLiteralWithoutReadingEnvironment(t *testing.T) {
	runner := &fakeRunner{}
	endpoint, err := ResolveProjectEndpoint(
		context.Background(),
		runner,
		"azd",
		Workspace{
			Root: t.TempDir(),
			Selected: Service{
				ProjectEndpoint: "https://account.services.ai.azure.com/api/projects/project",
			},
		},
		"prod",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://account.services.ai.azure.com/api/projects/project" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("literal endpoint must not read azd environment values: %#v", runner.commands)
	}
}

func TestResolveProjectEndpointReadsOnlyCanonicalValue(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout:   "https://account.services.ai.azure.com/api/projects/project\n",
			}, nil
		},
	}
	endpoint, err := ResolveProjectEndpoint(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://account.services.ai.azure.com/api/projects/project" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one value lookup, got %#v", runner.commands)
	}
	if got := strings.Join(runner.commands[0].Args, " "); got !=
		"env get-value FOUNDRY_PROJECT_ENDPOINT --no-prompt --environment prod" {
		t.Fatalf("unexpected endpoint command: %s", got)
	}
	if strings.Contains(strings.Join(runner.commands[0].Args, " "), "get-values") {
		t.Fatal("project endpoint resolution requested the complete environment")
	}
}

func TestResolveProjectEndpointFallsBackToLegacyValue(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			if strings.Contains(strings.Join(command.Args, " "), ReservedProjectEnv) {
				return Execution{ExitCode: 1}, errors.New("exit status 1")
			}
			return Execution{
				ExitCode: 0,
				Stdout:   "https://account.services.ai.azure.com/api/projects/project\n",
			}, nil
		},
	}
	endpoint, err := ResolveProjectEndpoint(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://account.services.ai.azure.com/api/projects/project" {
		t.Fatalf("unexpected endpoint: %s", endpoint)
	}
	if len(runner.commands) != 2 {
		t.Fatalf("expected canonical and compatibility lookups, got %#v", runner.commands)
	}
	if got := strings.Join(runner.commands[1].Args, " "); got !=
		"env get-value AZURE_AI_PROJECT_ENDPOINT --no-prompt --environment prod" {
		t.Fatalf("unexpected compatibility endpoint command: %s", got)
	}
}

func TestRunDeployClassifiesMissingCanonicalProjectEndpoint(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stderr:   "FOUNDRY_PROJECT_ENDPOINT is not set in the current azd environment",
			}, errors.New("exit status 1")
		},
	}
	_, err := RunDeploy(
		context.Background(),
		runner,
		"azd",
		Workspace{
			Root:     t.TempDir(),
			Selected: Service{ServiceName: "agent"},
		},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectEndpoint) || !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("expected project endpoint deployment error, got %v", err)
	}
	if !runner.commands[0].CaptureStdout || !runner.commands[0].CaptureStderr {
		t.Fatal("deploy output was not captured for bounded error classification")
	}
}

func TestRunDeployClassifiesProjectAccessFromStdout(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stdout:   "Foundry returned HTTP 403 (wrong tenant or insufficient RBAC).",
			}, errors.New("exit status 1")
		},
	}
	_, err := RunDeploy(
		context.Background(),
		runner,
		"azd",
		Workspace{
			Root:     t.TempDir(),
			Selected: Service{ServiceName: "agent"},
		},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectAccess) || !errors.Is(err, ErrCommandFailed) {
		t.Fatalf("expected project access deployment error, got %v", err)
	}
}

func TestRunDoctorUsesDeploymentIdentityAndClassifiesAccess(t *testing.T) {
	workspace := Workspace{Root: t.TempDir()}
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stdout: "FOUNDRY_PROJECT_ENDPOINT set\n" +
					"Foundry returned HTTP 403 (wrong tenant or insufficient RBAC).\n" +
					"AZURE_AI_PROJECT_ID is not set in the current azd environment.",
			}, errors.New("exit status 1")
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		workspace,
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectAccess) {
		t.Fatalf("expected project access diagnostic, got %v", err)
	}
	if got := strings.Join(runner.commands[0].Args, " "); got !=
		"ai agent doctor --no-prompt --environment prod" {
		t.Fatalf("unexpected doctor args: %s", got)
	}
	if runner.commands[0].Directory != workspace.Root ||
		!runner.commands[0].CaptureStdout ||
		!runner.commands[0].CaptureStderr {
		t.Fatalf("doctor did not preserve bounded deployment context: %#v", runner.commands[0])
	}
}

func TestRunDoctorAcceptsUndeployedOnlyBeforeFirstDeployment(t *testing.T) {
	tests := []struct {
		name    string
		summary string
		wantErr bool
	}{
		{
			name:    "only undeployed",
			summary: "9 passed, 1 failed, 3 skipped",
		},
		{
			name:    "additional failure",
			summary: "8 passed, 2 failed, 3 skipped",
			wantErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &fakeRunner{
				run: func(command Command) (Execution, error) {
					return Execution{
						ExitCode: 1,
						Stdout: "Foundry project endpoint reachable\n" +
							"Developer has required role on Foundry project\n" +
							"1 of 1 agents have not been deployed:\n" +
							test.summary,
					}, errors.New("exit status 1")
				},
			}
			_, err := RunDoctor(
				context.Background(),
				runner,
				"azd",
				Workspace{Root: t.TempDir()},
				"prod",
				nil,
			)
			if test.wantErr && !errors.Is(err, ErrCommandFailed) {
				t.Fatalf("expected additional diagnostics failure, got %v", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("undeployed-only diagnostics should pass preflight: %v", err)
			}
		})
	}
}

func TestRunDoctorRejectsSkippedProjectRoleCheck(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stdout: "Foundry project endpoint reachable\n" +
					"Developer has required role on Foundry project -- skipped " +
					"(AZURE_AI_PROJECT_ID is not set in the current azd environment.)\n" +
					"1 of 1 agents have not been deployed:\n" +
					"9 passed, 1 failed, 3 skipped",
			}, errors.New("exit status 1")
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectID) {
		t.Fatalf("expected missing project ID diagnostic, got %v", err)
	}
}

func TestRunDoctorExitZeroSkippedRoleCheckFailsClosed(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout: "Foundry project endpoint reachable\n" +
					"Developer has required role on Foundry project -- skipped " +
					"(AZURE_AI_PROJECT_ID is not set in the current azd environment.)\n" +
					"10 passed, 0 failed, 3 skipped",
			}, nil
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectID) {
		t.Fatalf("expected ErrProjectID when exit 0 but role check skipped, got %v", err)
	}
}

func TestRunDoctorExitZeroAffirmativeRolePassSucceeds(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout: "Foundry project endpoint reachable\n" +
					"Developer has required role on Foundry project\n" +
					"10 passed, 0 failed, 3 skipped",
			}, nil
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if err != nil {
		t.Fatalf("expected success with affirmative role pass, got %v", err)
	}
}

func TestRunDoctorExitOneUndeployedWithAffirmativeRolePassAccepted(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 1,
				Stdout: "Foundry project endpoint reachable\n" +
					"Developer has required role on Foundry project\n" +
					"1 of 1 agents have not been deployed:\n" +
					"9 passed, 1 failed, 3 skipped",
			}, errors.New("exit status 1")
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if err != nil {
		t.Fatalf("expected undeployed-only acceptance with affirmative role pass, got %v", err)
	}
}

func TestRunDoctorExitZeroNoRoleEvidenceFailsClosed(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{
				ExitCode: 0,
				Stdout:   "All checks passed\n10 passed, 0 failed, 0 skipped",
			}, nil
		},
	}
	_, err := RunDoctor(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrProjectID) {
		t.Fatalf("expected ErrProjectID when no role evidence in output, got %v", err)
	}
}

func TestResolveProjectEndpointRejectsUntrustedValue(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{ExitCode: 0, Stdout: "https://attacker.example/api/projects/project"}, nil
		},
	}
	_, err := ResolveProjectEndpoint(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if err == nil || !errs.IsKind(err, "security") {
		t.Fatalf("expected security rejection, got %v", err)
	}
}

func TestResolveProjectEndpointPreservesOutputLimitClassification(t *testing.T) {
	runner := &fakeRunner{
		run: func(command Command) (Execution, error) {
			return Execution{}, ErrOutputTooLarge
		},
	}
	_, err := ResolveProjectEndpoint(
		context.Background(),
		runner,
		"azd",
		Workspace{Root: t.TempDir()},
		"prod",
		nil,
	)
	if !errors.Is(err, ErrOutputTooLarge) || errors.Is(err, ErrProjectEndpoint) {
		t.Fatalf("expected direct output-limit classification, got %v", err)
	}
}

func TestResolveServiceEnvironmentReadsOnlyReferencedValues(t *testing.T) {
	workspace := Workspace{
		Root: t.TempDir(),
		Selected: Service{
			Environment: map[string]string{
				"LITERAL": "plain",
				"SECRET":  "${SECRET_VALUE}",
			},
		},
	}
	runner := &fakeRunner{run: func(command Command) (Execution, error) {
		if command.Phase != "environment-value" {
			t.Fatalf("unexpected command phase: %s", command.Phase)
		}
		if got := strings.Join(command.Args, " "); got !=
			"env get-value SECRET_VALUE --no-prompt --environment prod" {
			t.Fatalf("unexpected environment lookup: %s", got)
		}
		return Execution{ExitCode: 0, Stdout: "resolved-secret\r\n"}, nil
	}}
	values, err := ResolveServiceEnvironment(
		context.Background(),
		runner,
		`C:\tools\azd.exe`,
		workspace,
		"prod",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if values["LITERAL"] != "plain" || values["SECRET"] != "resolved-secret" {
		t.Fatalf("unexpected resolved values: %#v", values)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("expected one targeted environment lookup, got %d", len(runner.commands))
	}
}

func TestLimitedBufferBoundsOutput(t *testing.T) {
	var buffer limitedBuffer
	payload := strings.Repeat("x", maxCommandOutput+100)
	n, err := buffer.Write([]byte(payload))
	if err != nil || n != len(payload) {
		t.Fatalf("unexpected write result: n=%d err=%v", n, err)
	}
	if !buffer.truncated || len(buffer.String()) != maxCommandOutput {
		t.Fatalf("output was not bounded: truncated=%t len=%d", buffer.truncated, len(buffer.String()))
	}
}

func TestExecRunnerHonorsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := (ExecRunner{}).Run(ctx, Command{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestExecRunnerHelperProcess"},
		Environment: map[string]string{
			"FOUNDRY_AGENT_MANAGER_RUNNER_HELPER": "1",
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestExecRunnerHelperProcess(t *testing.T) {
	if os.Getenv("FOUNDRY_AGENT_MANAGER_RUNNER_HELPER") != "1" {
		return
	}
	time.Sleep(time.Second)
}

func TestMergeEnvironmentUsesPlatformKeySemantics(t *testing.T) {
	merged := mergeEnvironment(
		[]string{"MixedCase=first", "MIXEDCASE=second"},
		map[string]string{"AZD_NON_INTERACTIVE": "true"},
	)
	count := 0
	for _, entry := range merged {
		if strings.HasPrefix(entry, "MixedCase=") || strings.HasPrefix(entry, "MIXEDCASE=") {
			count++
		}

	}
	want := 2
	if runtime.GOOS == "windows" {
		want = 1
	}
	if count != want {
		t.Fatalf("environment key count = %d, want %d: %#v", count, want, merged)
	}
}

func TestCommandEnvironmentPrependsExecutableDirectoryToPath(t *testing.T) {
	executableDirectory := t.TempDir()
	executable := filepath.Join(executableDirectory, "azd")
	merged := commandEnvironment(
		[]string{"PATH=" + filepath.Join("existing", "bin")},
		map[string]string{"AZD_NON_INTERACTIVE": "true"},
		executable,
	)
	pathValue := environmentValueFromList(merged, "PATH")
	expectedPrefix := executableDirectory + string(os.PathListSeparator)
	if !strings.HasPrefix(pathValue, expectedPrefix) {
		t.Fatalf("executable directory was not prepended to PATH: %q", pathValue)
	}
}
