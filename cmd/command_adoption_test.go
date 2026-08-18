package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/hosted"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

func TestQuickstartCreatesValidatedPromptManifest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "prompt",
		"--destination", path,
		"--name", "support-agent",
		"--model", "model-deployment",
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support-project",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("quickstart failed: %s", run.stderr)
	}
	var result quickstartResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Type != "prompt" || result.Path != path || len(result.NextCommands) != 5 {
		t.Fatalf("unexpected quickstart result: %#v", result)
	}
	if len(result.OperatorActions) == 0 ||
		!strings.Contains(result.OperatorActions[0], "project-create") ||
		!strings.Contains(result.OperatorActions[0], "model-deployment") {
		t.Fatalf("Prompt quickstart omitted external prerequisites: %#v", result)
	}
	if validate := runCLI(t, "", "validate", "-f", path); validate.code != 0 {
		t.Fatalf("generated manifest did not validate: %s", validate.stderr)
	}
}

func TestQuickstartPromptWritesOptionalRAIPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	const policyID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom-policy"
	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "prompt",
		"--destination", path,
		"--name", "support-agent",
		"--model", "model-deployment",
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support-project",
		"--guardrail-policy-id", policyID,
		"--non-interactive",
	)
	if run.code != 0 {
		t.Fatalf("quickstart failed: %s", run.stderr)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "rai_policy_id: "+policyID) {
		t.Fatalf("Prompt quickstart omitted RAI policy:\n%s", content)
	}
}

func TestQuickstartPromptRejectsNoGuardrail(t *testing.T) {
	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "prompt",
		"--destination", filepath.Join(t.TempDir(), "agent.yaml"),
		"--name", "support-agent",
		"--no-guardrail",
		"--non-interactive",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "supported only with --type hosted") {
		t.Fatalf("Prompt --no-guardrail was not rejected: code=%d stderr=%s", run.code, run.stderr)
	}
}

func TestQuickstartCreatesValidatedHostedWorkspace(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-test-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })
	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--environment", "prod",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("quickstart failed: %s", run.stderr)
	}
	var result quickstartResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Type != "hosted" || result.Path != absoluteRoot || len(result.NextCommands) != 6 {
		t.Fatalf("unexpected quickstart result: %#v", result)
	}
	if len(result.OperatorActions) == 0 ||
		!strings.Contains(result.OperatorActions[0], "hosted environment create") {
		t.Fatalf("Hosted quickstart omitted environment bootstrap guidance: %#v", result)
	}
	if !strings.Contains(strings.Join(result.NextCommands, "\n"), "hosted environment create") {
		t.Fatalf("Hosted quickstart omitted the environment creation command: %#v", result)
	}
	if result.Environment != nil {
		t.Fatalf("non-interactive quickstart bootstrapped without explicit opt-in: %#v", result)
	}
	if _, err := os.Stat(filepath.Join(absoluteRoot, "azure.yaml")); err != nil {
		t.Fatalf("hosted quickstart did not create azure.yaml: %v", err)
	}
	azureYAML, err := os.ReadFile(filepath.Join(absoluteRoot, "azure.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(azureYAML), "raiPolicyName: ${RAI_POLICY_ID}") {
		t.Fatalf("Hosted quickstart did not default to Microsoft.DefaultV2 through RAI_POLICY_ID:\n%s", azureYAML)
	}
	if validate := runCLI(t, "", "hosted-validate", "--workspace", absoluteRoot); validate.code != 0 {
		t.Fatalf("generated workspace did not validate: %s", validate.stderr)
	}
	doctor := runCLI(t, "", "doctor", "--workspace", absoluteRoot, "--output", "json")
	if doctor.code != 0 {
		t.Fatalf("hosted doctor failed: %s", doctor.stderr)
	}
	var diagnosis doctorResult
	if err := json.Unmarshal([]byte(doctor.stdout), &diagnosis); err != nil {
		t.Fatal(err)
	}
	if !diagnosis.Ready || diagnosis.Mode != "hosted" {
		t.Fatalf("unexpected hosted doctor result: %#v", diagnosis)
	}
}

func TestHostedQuickstartCarriesNoGuardrailAcknowledgement(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-no-guardrail-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--no-guardrail",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("quickstart failed: %s", run.stderr)
	}
	var result quickstartResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	assertHostedNoGuardrailNextCommands(t, result.NextCommands)
}

func TestHostedInitGuardrailSelection(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	const policyID = "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom-policy"
	tests := []struct {
		name       string
		extra      []string
		want       string
		wantAbsent string
	}{
		{name: "custom", extra: []string{"--guardrail-policy-id", policyID}, want: "raiPolicyName: " + policyID},
		{name: "disabled", extra: []string{"--no-guardrail"}, wantAbsent: "policies:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := ".hosted-init-guardrail-" + test.name + "-" + filepath.Base(t.TempDir())
			absoluteRoot := filepath.Join(cwd, root)
			t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })
			args := []string{"hosted-init", "--destination", root, "--name", "support-agent"}
			args = append(args, test.extra...)
			run := runCLI(t, "", args...)
			if run.code != 0 {
				t.Fatalf("hosted init failed: %s", run.stderr)
			}
			content, err := os.ReadFile(filepath.Join(absoluteRoot, "azure.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			if test.want != "" && !strings.Contains(string(content), test.want) {
				t.Fatalf("azure.yaml omitted %q:\n%s", test.want, content)
			}
			if test.wantAbsent != "" && strings.Contains(string(content), test.wantAbsent) {
				t.Fatalf("azure.yaml unexpectedly contains %q:\n%s", test.wantAbsent, content)
			}
		})
	}
}

func TestHostedInitRejectsConflictingGuardrailFlags(t *testing.T) {
	run := runCLI(
		t,
		"",
		"hosted-init",
		"--destination", ".hosted-init-conflict-"+filepath.Base(t.TempDir()),
		"--name", "support-agent",
		"--guardrail-policy-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/custom-policy",
		"--no-guardrail",
	)
	if run.code == 0 || !strings.Contains(run.stderr, "cannot be used together") {
		t.Fatalf("conflicting guardrail flags were not rejected: code=%d stderr=%s", run.code, run.stderr)
	}
}

func TestHostedAdoptCopiesExistingPythonSource(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "existing-agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, "main.py"),
		[]byte("from agent_framework_foundry_hosting import ResponsesHostServer\nResponsesHostServer(agent).run()\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.txt"), []byte("agent-framework-foundry-hosting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := ".hosted-adopt-" + filepath.Base(t.TempDir())
	absoluteDestination := filepath.Join(cwd, destination)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteDestination) })

	run := runCLI(
		t,
		"",
		"hosted", "adopt",
		"--source", source,
		"--destination", destination,
		"--name", "existing-agent",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted adopt failed: %s", run.stderr)
	}
	var result hostedAdoptCommandResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "hosted-adopt" ||
		result.Workspace != absoluteDestination ||
		result.Mode != "copy" ||
		!result.HostingDetected {
		t.Fatalf("unexpected adoption result: %#v", result)
	}
	if !strings.Contains(strings.Join(result.NextCommands, "\n"), "hosted environment create") {
		t.Fatalf("adoption omitted environment bootstrap guidance: %#v", result)
	}
	validate := runCLI(t, "", "hosted", "validate", "--workspace", absoluteDestination)
	if validate.code != 0 {
		t.Fatalf("adopted workspace did not validate: %s", validate.stderr)
	}
}

func TestHostedAdoptCarriesNoGuardrailAcknowledgement(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "existing-agent")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.txt"), []byte("agent-framework-foundry-hosting\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := ".hosted-adopt-no-guardrail-" + filepath.Base(t.TempDir())
	absoluteDestination := filepath.Join(cwd, destination)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteDestination) })

	run := runCLI(
		t,
		"",
		"hosted", "adopt",
		"--source", source,
		"--destination", destination,
		"--name", "existing-agent",
		"--no-guardrail",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted adopt failed: %s", run.stderr)
	}
	var result hostedAdoptCommandResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	assertHostedNoGuardrailNextCommands(t, result.NextCommands)
}

func assertHostedNoGuardrailNextCommands(t *testing.T, commands []string) {
	t.Helper()
	foundPreflight := false
	foundDeploy := false
	for _, command := range commands {
		if strings.Contains(command, "hosted preflight") {
			foundPreflight = true
			if !strings.Contains(command, "--no-guardrail") {
				t.Fatalf("preflight next command omitted --no-guardrail: %q", command)
			}
		}
		if strings.Contains(command, "hosted deploy") {
			foundDeploy = true
			if !strings.Contains(command, "--no-guardrail") {
				t.Fatalf("deploy next command omitted --no-guardrail: %q", command)
			}
		}
	}
	if !foundPreflight || !foundDeploy {
		t.Fatalf("missing Hosted online next commands: %#v", commands)
	}
}

func TestQuickstartSourceUsesHostedAdoption(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "quickstart-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "app.py"), []byte("print('ready')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "pyproject.toml"), []byte("[project]\nname = \"quickstart-source\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := ".quickstart-adopt-" + filepath.Base(t.TempDir())
	absoluteDestination := filepath.Join(cwd, destination)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteDestination) })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--source", source,
		"--destination", destination,
		"--name", "quickstart-agent",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("quickstart source adoption failed: %s", run.stderr)
	}
	var result hostedAdoptCommandResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Action != "hosted-adopt" || result.Workspace != absoluteDestination {
		t.Fatalf("quickstart did not route to Hosted adoption: %#v", result)
	}
	if result.HostingDetected ||
		!strings.Contains(strings.Join(result.OperatorActions, "\n"), "ResponsesHostServer") {
		t.Fatalf("quickstart adoption omitted hosting compatibility warning: %#v", result)
	}
}

func TestInteractiveQuickstartCanSelectExistingSource(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "interactive-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.txt"), []byte("example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := ".interactive-adopt-" + filepath.Base(t.TempDir())
	absoluteDestination := filepath.Join(cwd, destination)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteDestination) })

	stdin := strings.Join([]string{
		"hosted",
		source,
		destination,
		"interactive-agent",
		"n",
	}, "\n")
	run := runCLI(t, stdin, "quickstart")
	if run.code != 0 {
		t.Fatalf("interactive source adoption failed: %s", run.stderr)
	}
	if !strings.Contains(run.stderr, "Existing Python source folder to adopt") ||
		!strings.Contains(run.stderr, "Adopting existing Python agent source") ||
		!strings.Contains(run.stdout, "Hosted Python source adopted") {
		t.Fatalf("interactive quickstart did not expose source adoption:\nstdout=%s\nstderr=%s", run.stdout, run.stderr)
	}
	if _, err := os.Stat(filepath.Join(absoluteDestination, "azure.yaml")); err != nil {
		t.Fatalf("interactive adoption did not create a workspace: %v", err)
	}
}

func TestHostedAdoptBootstrapsEnvironment(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "bootstrap-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "main.py"), []byte("print('ready')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "requirements.txt"), []byte("example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := ".hosted-adopt-bootstrap-" + filepath.Base(t.TempDir())
	absoluteDestination := filepath.Join(cwd, destination)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteDestination) })

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
		"hosted-adopt",
		"--source", source,
		"--destination", destination,
		"--name", "bootstrap-agent",
		"--environment", "dev",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--model", "support-model",
		"--location", "eastus2",
		"--bootstrap-environment",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted adoption bootstrap failed: %s", run.stderr)
	}
	var result hostedAdoptCommandResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Environment == nil || !result.Environment.Created {
		t.Fatalf("adoption did not bootstrap the environment: %#v", result)
	}
	if strings.Contains(strings.Join(result.NextCommands, "\n"), "hosted environment create") {
		t.Fatalf("bootstrapped adoption retained redundant environment step: %#v", result)
	}
}

func TestQuickstartBootstrapsHostedEnvironment(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-bootstrap-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

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
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--environment", "dev",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--model", "support-model",
		"--location", "eastus2",
		"--tenant-id", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		"--bootstrap-environment",
		"--non-interactive",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("quickstart bootstrap failed: %s", run.stderr)
	}
	var result quickstartResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Environment == nil || result.Environment.Name != "dev" ||
		!result.Environment.Created || len(result.Environment.Configured) < 4 {
		t.Fatalf("unexpected quickstart environment result: %#v", result)
	}
	if len(result.NextCommands) != 5 ||
		strings.Contains(strings.Join(result.NextCommands, "\n"), "hosted environment create") {
		t.Fatalf("bootstrapped quickstart retained redundant setup step: %#v", result)
	}
	if got := strings.Join(runner.commands[3].Args, " "); got !=
		"env set AZURE_SUBSCRIPTION_ID=11111111-2222-3333-4444-555555555555 AZURE_TENANT_ID=aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee AZURE_LOCATION=eastus2 AZURE_AI_PROJECT_ID=/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support FOUNDRY_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_PROJECT_ENDPOINT=https://account.services.ai.azure.com/api/projects/support AZURE_AI_MODEL_DEPLOYMENT_NAME=support-model RAI_POLICY_ID=/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/raiPolicies/Microsoft.DefaultV2 --environment dev --no-prompt" {
		t.Fatalf("unexpected quickstart environment configuration: %s", got)
	}
}

func TestInteractiveHostedQuickstartDefaultsToBootstrap(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-interactive-bootstrap-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	runner := &hostedEnvironmentCommandRunner{}
	oldLookPath := hostedLookPathFn
	oldRunner := newHostedRunnerFn
	hostedLookPathFn = func(string) (string, error) { return `C:\tools\azd.exe`, nil }
	newHostedRunnerFn = func() hosted.Runner { return runner }
	t.Cleanup(func() {
		hostedLookPathFn = oldLookPath
		newHostedRunnerFn = oldRunner
	})

	stdin := strings.Join([]string{
		"hosted",
		"",
		root,
		"support-agent",
		"",
		"/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"support-model",
		"eastus2",
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
	}, "\n")
	run := runCLI(t, stdin, "quickstart")
	if run.code != 0 {
		t.Fatalf("interactive quickstart bootstrap failed: %s", run.stderr)
	}
	for _, expected := range []string{
		"Create and configure the workspace azd environment now [Y/n]",
		"Existing Foundry project resource ID",
		"Existing model deployment name",
		"Azure location for Hosted deployment",
		"Azure tenant ID",
	} {
		if !strings.Contains(run.stderr, expected) {
			t.Fatalf("interactive bootstrap prompt missing %q:\n%s", expected, run.stderr)
		}
	}
	if len(runner.commands) != 4 {
		t.Fatalf("interactive quickstart did not bootstrap the environment: %#v", runner.commands)
	}
	if strings.Contains(run.stdout, "hosted environment create") ||
		!strings.Contains(run.stdout, "hosted preflight") {
		t.Fatalf("interactive quickstart printed incorrect next steps:\n%s", run.stdout)
	}
}

func TestNonInteractiveHostedBootstrapRequiresProjectAndModel(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	root := ".quickstart-missing-bootstrap-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--bootstrap-environment",
		"--non-interactive",
	)
	if run.code != 3 ||
		!strings.Contains(run.stderr, "existing foundry project resource id") {
		t.Fatalf("missing bootstrap context was not rejected: %#v", run)
	}
	if _, err := os.Stat(absoluteRoot); !os.IsNotExist(err) {
		t.Fatalf("quickstart wrote the workspace before validating bootstrap input: %v", err)
	}
}

func TestNonInteractiveHostedBootstrapRequiresModel(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-missing-model-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--bootstrap-environment",
		"--non-interactive",
	)
	if run.code != 3 ||
		!strings.Contains(run.stderr, "existing model deployment name is required") {
		t.Fatalf("missing model was not rejected: %#v", run)
	}
	if _, err := os.Stat(absoluteRoot); !os.IsNotExist(err) {
		t.Fatalf("quickstart wrote the workspace before validating model: %v", err)
	}
}

func TestHostedQuickstartBootstrapFailurePreservesWorkspaceAndRecovery(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-bootstrap-recovery-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	oldLookPath := hostedLookPathFn
	hostedLookPathFn = func(string) (string, error) { return "", os.ErrNotExist }
	t.Cleanup(func() { hostedLookPathFn = oldLookPath })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--environment", "dev",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/agents-rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--model", "support-model",
		"--location", "eastus2",
		"--bootstrap-environment",
		"--non-interactive",
	)
	if run.code != 3 ||
		!strings.Contains(run.stderr, "workspace was created") ||
		!strings.Contains(run.stderr, "hosted environment create") {
		t.Fatalf("bootstrap failure omitted workspace recovery: %#v", run)
	}
	if _, err := os.Stat(filepath.Join(absoluteRoot, "azure.yaml")); err != nil {
		t.Fatalf("recoverable workspace was not preserved: %v", err)
	}
}

func TestNonInteractiveHostedBootstrapRequiresLocation(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-missing-location-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })

	run := runCLI(
		t,
		"",
		"quickstart",
		"--type", "hosted",
		"--destination", root,
		"--name", "support-agent",
		"--project-id", "/subscriptions/11111111-2222-3333-4444-555555555555/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support",
		"--model", "support-model",
		"--bootstrap-environment",
		"--non-interactive",
	)
	if run.code != 3 ||
		!strings.Contains(run.stderr, "azure location for hosted deployment is required") {
		t.Fatalf("missing location was not rejected: %#v", run)
	}
	if _, err := os.Stat(absoluteRoot); !os.IsNotExist(err) {
		t.Fatalf("quickstart wrote the workspace before validating location: %v", err)
	}
}

func TestQuickstartPromptsForMissingPromptValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prompted.yaml")
	stdin := strings.Join([]string{
		"prompt",
		path,
		"prompted-agent",
		"model-deployment",
		"/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/prompted-project",
		"",
	}, "\n")
	run := runCLI(t, stdin, "quickstart")
	if run.code != 0 {
		t.Fatalf("interactive quickstart failed: %s", run.stderr)
	}
	if !strings.Contains(run.stdout, "prompt quickstart created") {
		t.Fatalf("unexpected quickstart output: %s", run.stdout)
	}
	if !strings.Contains(run.stderr, "Deployment type") ||
		!strings.Contains(run.stderr, "Model deployment name") {
		t.Fatalf("interactive prompts were not written to stderr: %s", run.stderr)
	}
	if !strings.Contains(run.stderr, "writes a local configuration file only") ||
		!strings.Contains(run.stderr, "Manifest file to create") {
		t.Fatalf("interactive Prompt guidance is unclear: %s", run.stderr)
	}
}

func TestQuickstartExplainsHostedWorkspacePrompt(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := ".quickstart-prompt-" + filepath.Base(t.TempDir())
	absoluteRoot := filepath.Join(cwd, root)
	t.Cleanup(func() { _ = os.RemoveAll(absoluteRoot) })
	stdin := strings.Join([]string{
		"hosted",
		"",
		root,
		"hosted-agent",
		"n",
	}, "\n")
	run := runCLI(t, stdin, "quickstart")
	if run.code != 0 {
		t.Fatalf("interactive Hosted quickstart failed: %s", run.stderr)
	}
	for _, expected := range []string{
		"Existing Python source folder to adopt",
		"Creating a Hosted Agent workspace",
		"writes local starter files",
		"never creates Azure resources",
		"new relative path inside the current directory",
		"New local workspace folder to create",
		"Create and configure the workspace azd environment now",
	} {
		if !strings.Contains(run.stderr, expected) {
			t.Fatalf("Hosted guidance is missing %q:\n%s", expected, run.stderr)
		}
	}
}

func TestDoctorReportsLocalPromptReadiness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	create := runCLI(
		t,
		"",
		"quickstart",
		"--type", "prompt",
		"--destination", path,
		"--name", "support-agent",
		"--model", "model-deployment",
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/account/projects/support-project",
		"--non-interactive",
	)
	if create.code != 0 {
		t.Fatalf("quickstart failed: %s", create.stderr)
	}
	run := runCLI(t, "", "doctor", "-f", path, "--output", "json")
	if run.code != 0 {
		t.Fatalf("doctor failed: %s", run.stderr)
	}
	var result doctorResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Mode != "prompt" {
		t.Fatalf("unexpected doctor result: %#v", result)
	}
	if !result.LocalReady || result.Scope != "local" ||
		result.OnlineReady != nil || result.DeploymentReady != nil ||
		result.CoverageComplete {
		t.Fatalf("local readiness scope is unclear: %#v", result)
	}
	if len(result.Checks) < 4 || result.Checks[len(result.Checks)-1].Status != "skipped" {
		t.Fatalf("doctor did not report the skipped online boundary: %#v", result.Checks)
	}
	if result.Summary.Passed == 0 || result.Summary.Skipped == 0 {
		t.Fatalf("doctor summary is incomplete: %#v", result.Summary)
	}
}

func TestDoctorReturnsActionableFailedReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runCLI(t, "", "doctor", "-f", path, "--output", "json")
	if run.code != 0 {
		t.Fatalf("doctor diagnostics should return a report: %s", run.stderr)
	}
	var result doctorResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ready || len(result.Checks) == 0 {
		t.Fatalf("invalid manifest was not reported: %#v", result)
	}
	var failure *doctorCheck
	for index := range result.Checks {
		if result.Checks[index].Status == "failed" {
			failure = &result.Checks[index]
			break
		}
	}
	if failure == nil || len(failure.NextSteps) == 0 ||
		failure.Category == "" || failure.Severity != "error" {
		t.Fatalf("doctor failure is not actionable: %#v", result.Checks)
	}
}

func TestDoctorFailOnNotReadyReturnsReportWithoutErrorEnvelope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(path, []byte("apiVersion: wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run := runCLI(
		t,
		"",
		"doctor",
		"-f", path,
		"--fail-on-not-ready",
		"--output", "json",
	)
	if run.code != 11 {
		t.Fatalf("expected readiness exit 11, got %d: %s", run.code, run.stderr)
	}
	if strings.TrimSpace(run.stderr) != "" {
		t.Fatalf("reported readiness must not emit a second error envelope: %q", run.stderr)
	}
	var result doctorResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatalf("readiness report is not valid JSON: %v\n%s", err, run.stdout)
	}
	if result.Ready {
		t.Fatalf("invalid manifest was reported ready: %#v", result)
	}
}

func TestDoctorHostedOnlineChecksToolingAndProjectAccess(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedLifecycleFakeRunner{}
	stubCredentialAndHTTP(t, &scriptedHTTP{})
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
		"doctor",
		"--workspace", workspace,
		"--environment", "prod",
		"--online",
		"--accept-preview",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted doctor failed: %s", run.stderr)
	}
	var result doctorResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.OnlineReady == nil || !*result.OnlineReady ||
		result.DeploymentReady == nil || !*result.DeploymentReady {
		t.Fatalf("hosted online readiness was not proven: %#v", result)
	}
	foundAccess := false
	for _, check := range result.Checks {
		if check.Name == "hosted-project-access" && check.Status == "passed" {
			foundAccess = true
		}
	}
	if !foundAccess {
		t.Fatalf("hosted project access check is missing: %#v", result.Checks)
	}
}

func TestDoctorBlocksUntrustedHostedExtensionAndCredentialUse(t *testing.T) {
	workspace := writeHostedLifecycleWorkspace(t, true)
	runner := &hostedLifecycleFakeRunner{extensionVersion: "9.9.9"}
	scripted := &scriptedHTTP{}
	stubCredentialAndHTTP(t, scripted)
	credentialFactory := newCredentialFn
	credentialCalled := false
	newCredentialFn = func(
		cmd *cobra.Command,
		profile azcloud.Profile,
	) (azcore.TokenCredential, error) {
		credentialCalled = true
		return credentialFactory(cmd, profile)
	}
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
		"doctor",
		"--workspace", workspace,
		"--environment", "prod",
		"--online",
		"--accept-preview",
		"--output", "json",
	)
	if run.code != 0 {
		t.Fatalf("hosted doctor report failed: %s", run.stderr)
	}
	var result doctorResult
	if err := json.Unmarshal([]byte(run.stdout), &result); err != nil {
		t.Fatal(err)
	}
	if result.Ready || credentialCalled || len(scripted.requests) != 0 {
		t.Fatalf(
			"untrusted extension crossed the credential boundary: ready=%t credential=%t requests=%d",
			result.Ready,
			credentialCalled,
			len(scripted.requests),
		)
	}
	for _, command := range runner.commands {
		if command.Phase == "agent-extension-version" ||
			command.Phase == "status-contract" ||
			command.Phase == "project-endpoint" {
			t.Fatalf("unsafe dependent command was executed: %#v", command)
		}
	}
	blocked := map[string]bool{}
	for _, check := range result.Checks {
		if check.Status == "skipped" {
			blocked[check.Name] = true
		}
	}
	for _, name := range []string{
		"hosted-agent-extension-command",
		"hosted-status-contract",
		"hosted-project-endpoint",
		"hosted-project-access",
	} {
		if !blocked[name] {
			t.Fatalf("unsafe dependent check %s was not blocked: %#v", name, result.Checks)
		}
	}
}

func TestGlobalDebugWritesSafeDiagnosticsToStderr(t *testing.T) {
	run := runCLI(t, "", "version", "--debug")
	if run.code != 0 {
		t.Fatalf("version --debug failed: %s", run.stderr)
	}
	if !strings.Contains(run.stderr, "debug: command=foundry-agent-manager version") ||
		!strings.Contains(run.stderr, "request-timeout=") {
		t.Fatalf("debug diagnostics are missing: %q", run.stderr)
	}
}

func TestCommonCommandHelpIncludesExamples(t *testing.T) {
	for _, command := range [][]string{
		{"quickstart"},
		{"doctor"},
		{"prompt", "init"},
		{"prompt", "deploy"},
		{"hosted", "init"},
		{"hosted", "deploy"},
	} {
		t.Run(strings.Join(command, "/"), func(t *testing.T) {
			run := runCLI(t, "", append(command, "--help")...)
			if run.code != 0 {
				t.Fatalf("%s --help failed: %s", strings.Join(command, " "), run.stderr)
			}
			if !strings.Contains(run.stdout, "Examples:") ||
				!strings.Contains(run.stdout, "foundry-agent-manager "+strings.Join(command, " ")) {
				t.Fatalf("%s help is missing copyable examples:\n%s", strings.Join(command, " "), run.stdout)
			}
		})
	}
}

func TestHelpCommandTargetsSelectedCommand(t *testing.T) {
	for _, args := range [][]string{
		{"help", "quickstart"},
		{"quickstart", "--help"},
	} {
		run := runCLI(t, "", args...)
		if run.code != 0 {
			t.Fatalf("%v failed: %s", args, run.stderr)
		}
		for _, expected := range []string{
			"Usage:",
			"foundry-agent-manager quickstart",
			"Examples:",
			"--type string",
			"Related workflow:",
			"foundry-agent-manager help doctor",
			"foundry-agent-manager help hosted validate",
		} {
			if !strings.Contains(run.stdout, expected) {
				t.Errorf("%v help missing %q:\n%s", args, expected, run.stdout)
			}
		}
		for _, unrelated := range []string{
			"Prompt Agent - Deploy and Operate:",
			"Tools, Data, and Integrations - Online:",
			"Hosted Agent:",
			"Autopilot (experimental):",
			"hosted-delete",
		} {
			if strings.Contains(run.stdout, unrelated) {
				t.Errorf("%v help includes unrelated catalog entry %q:\n%s", args, unrelated, run.stdout)
			}
		}
	}
}

func TestUnknownHelpTopicIsActionable(t *testing.T) {
	run := runCLI(t, "", "help", "does-not-exist")
	if run.code != 3 {
		t.Fatalf("expected config exit 3, got %d: %s", run.code, run.stderr)
	}
	if run.stdout != "" {
		t.Fatalf("unknown help topic printed the root catalog:\n%s", run.stdout)
	}
	if !strings.Contains(run.stderr, `unknown help topic "does-not-exist"`) ||
		!strings.Contains(run.stderr, "foundry-agent-manager help") {
		t.Fatalf("unknown help topic is not actionable:\n%s", run.stderr)
	}
}

func TestStructuredErrorsIncludeOptionalRemediation(t *testing.T) {
	run := runCLI(t, "", "validate", "--output", "json")
	if run.code != 3 {
		t.Fatalf("expected config failure, got %d: %s", run.code, run.stderr)
	}
	var envelope struct {
		Error struct {
			NextSteps []string `json:"nextSteps"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(run.stderr), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Error.NextSteps) == 0 ||
		!strings.Contains(envelope.Error.NextSteps[0], "quickstart") {
		t.Fatalf("missing remediation: %#v", envelope)
	}
}

func TestRootHelpShowsCommandGroups(t *testing.T) {
	run := runCLI(t, "", "--help")
	if run.code != 0 {
		t.Fatalf("--help failed: %s", run.stderr)
	}
	for _, group := range []string{
		"Getting Started:",
		"Prompt Agent:",
		"Projects, Tools, Data, and Integrations:",
		"Hosted Agent:",
	} {
		if !strings.Contains(run.stdout, group) {
			t.Errorf("root help missing group %q:\n%s", group, run.stdout)
		}
	}
}

func TestRootHelpShowsOrientationForNewUsers(t *testing.T) {
	run := runCLI(t, "", "--help")
	if run.code != 0 {
		t.Fatalf("--help failed: %s", run.stderr)
	}
	if !strings.Contains(run.stdout, "Getting started") {
		t.Fatalf("root help missing getting-started orientation:\n%s", run.stdout)
	}
}

func TestDoctorTextUsesPortableStatusLabels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	create := runCLI(
		t, "",
		"quickstart", "--type", "prompt", "--destination", path,
		"--name", "ag", "--model", "md",
		"--project-resource-id", "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/rg/providers/Microsoft.CognitiveServices/accounts/a/projects/project",
		"--non-interactive",
	)
	if create.code != 0 {
		t.Fatalf("quickstart failed: %s", create.stderr)
	}
	run := runCLI(t, "", "doctor", "-f", path)
	if run.code != 0 {
		t.Fatalf("doctor failed: %s", run.stderr)
	}
	if !strings.Contains(run.stdout, "[PASS]") {
		t.Fatalf("doctor text output missing PASS label:\n%s", run.stdout)
	}
	if !strings.Contains(run.stdout, "[SKIP]") {
		t.Fatalf("doctor text output missing SKIP label:\n%s", run.stdout)
	}
	if !strings.Contains(run.stdout, "READY") {
		t.Fatalf("doctor text output missing READY label:\n%s", run.stdout)
	}
}

func TestTextErrorShowsNextSteps(t *testing.T) {
	run := runCLI(t, "", "validate")
	if run.code != 3 {
		t.Fatalf("expected exit 3, got %d", run.code)
	}
	if !strings.Contains(run.stderr, "next:") {
		t.Fatalf("text error missing next-step label:\n%s", run.stderr)
	}
	if !strings.Contains(run.stderr, "quickstart") {
		t.Fatalf("text error missing quickstart remediation:\n%s", run.stderr)
	}
}

func TestUnknownCommandShowsRemediation(t *testing.T) {
	run := runCLI(t, "", "not-a-command", "--output", "json")
	if run.code != 3 {
		t.Fatalf("expected exit 3, got %d", run.code)
	}
	var envelope struct {
		Error struct {
			NextSteps []string `json:"nextSteps"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(run.stderr), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Error.NextSteps) == 0 ||
		!strings.Contains(envelope.Error.NextSteps[0], "--help") {
		t.Fatalf("unknown command missing --help remediation: %#v", envelope)
	}
}

func TestCLIPathOmitsShellExpandableValues(t *testing.T) {
	for _, value := range []string{
		`C:\agent\$(Invoke-WebRequest example.invalid)`,
		`C:\agent\%COMSPEC%`,
		"C:\\agent\\`whoami`",
		"C:\\agent\\line\r\nnext",
	} {
		if actual := cliPath(value); actual != "<local-value>" {
			t.Fatalf("cliPath(%q) = %q, want inert placeholder", value, actual)
		}
	}
	if actual := cliPath(`C:\Users\Example Agent\workspace`); actual != `"C:\Users\Example Agent\workspace"` {
		t.Fatalf("safe workspace path was not quoted: %q", actual)
	}
}
