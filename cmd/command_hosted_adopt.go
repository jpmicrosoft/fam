package main

import (
	"bufio"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/hosted"

	"github.com/spf13/cobra"
)

var invalidHostedAdoptionName = regexp.MustCompile(`[^a-z0-9-]+`)

type hostedAdoptCommandResult struct {
	Action               string                       `json:"action" yaml:"action"`
	Workspace            string                       `json:"workspace" yaml:"workspace"`
	Source               string                       `json:"source" yaml:"source"`
	Name                 string                       `json:"name" yaml:"name"`
	Mode                 string                       `json:"mode" yaml:"mode"`
	Protocol             string                       `json:"protocol" yaml:"protocol"`
	Runtime              string                       `json:"runtime" yaml:"runtime"`
	EntryPoint           string                       `json:"entryPoint" yaml:"entryPoint"`
	DependencyResolution string                       `json:"dependencyResolution" yaml:"dependencyResolution"`
	DependencyFiles      []string                     `json:"dependencyFiles" yaml:"dependencyFiles"`
	CopiedFiles          int                          `json:"copiedFiles" yaml:"copiedFiles"`
	HostingDetected      bool                         `json:"hostingDetected" yaml:"hostingDetected"`
	Environment          *quickstartEnvironmentResult `json:"environment,omitempty" yaml:"environment,omitempty"`
	OperatorActions      []string                     `json:"operatorActions,omitempty" yaml:"operatorActions,omitempty"`
	NextCommands         []string                     `json:"nextCommands" yaml:"nextCommands"`
}

func cmdHostedAdopt(cmd *cobra.Command, _ []string) error {
	reader := bufio.NewReader(cmd.InOrStdin())
	interactive := !getBoolFlag(cmd, "non-interactive") &&
		strings.EqualFold(getFlag(cmd, "output"), "text")
	return runHostedAdoption(cmd, reader, interactive, "", "")
}

func runHostedAdoption(
	cmd *cobra.Command,
	reader *bufio.Reader,
	interactive bool,
	destination,
	name string,
) error {
	source, err := quickstartValue(
		cmd,
		reader,
		"Existing Python agent source folder",
		getFlag(cmd, "source"),
		"",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	if name == "" {
		name, err = quickstartValue(
			cmd,
			reader,
			"New Hosted Agent name",
			getFlag(cmd, "name"),
			suggestHostedAdoptionName(source),
			interactive,
			true,
		)
		if err != nil {
			return err
		}
	}
	inPlace := getBoolFlag(cmd, "in-place")
	if inPlace {
		if cmd.Flags().Changed("destination") || strings.TrimSpace(destination) != "" {
			return errs.Config("--destination cannot be used with --in-place")
		}
		destination = ""
	} else if destination == "" {
		destination, err = quickstartValue(
			cmd,
			reader,
			"New Hosted workspace folder to create (relative path)",
			getFlag(cmd, "destination"),
			name+"-hosted",
			interactive,
			true,
		)
		if err != nil {
			return err
		}
	}
	protocol, err := quickstartValue(
		cmd,
		reader,
		"Hosted API protocol (responses or invocations)",
		getFlag(cmd, "protocol"),
		"responses",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	runtimeName, err := quickstartValue(
		cmd,
		reader,
		"Hosted Python runtime",
		getFlag(cmd, "runtime"),
		"python_3_13",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	dependencyResolution, err := quickstartValue(
		cmd,
		reader,
		"Dependency resolution (remote_build or bundled)",
		getFlag(cmd, "dependency-resolution"),
		"remote_build",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	environment, err := quickstartValue(
		cmd,
		reader,
		"azd environment name to create or select before online checks",
		getFlag(cmd, "environment"),
		"dev",
		interactive,
		true,
	)
	if err != nil {
		return err
	}

	bootstrapEnvironment := getBoolFlag(cmd, "bootstrap-environment")
	if interactive && !cmd.Flags().Changed("bootstrap-environment") {
		bootstrapEnvironment, err = quickstartConfirmation(
			cmd,
			reader,
			"Create and configure the workspace azd environment now",
			true,
		)
		if err != nil {
			return err
		}
	}
	projectID := strings.TrimSpace(getFlag(cmd, "project-id"))
	modelDeployment := strings.TrimSpace(getFlag(cmd, "model"))
	location := strings.TrimSpace(getFlag(cmd, "location"))
	tenantID := strings.TrimSpace(getFlag(cmd, "tenant-id"))
	if bootstrapEnvironment {
		projectID, err = quickstartValue(
			cmd,
			reader,
			"Existing Foundry project resource ID (must end with /projects/<project>)",
			projectID,
			"",
			interactive,
			true,
		)
		if err != nil {
			return err
		}
		modelDeployment, err = quickstartValue(
			cmd,
			reader,
			"Existing model deployment name",
			modelDeployment,
			"",
			interactive,
			true,
		)
		if err != nil {
			return err
		}
		location, err = quickstartValue(
			cmd,
			reader,
			"Azure location for Hosted deployment",
			location,
			"",
			interactive,
			true,
		)
		if err != nil {
			return err
		}
		tenantID, err = quickstartValue(
			cmd,
			reader,
			"Azure tenant ID (optional; recommended for cross-tenant accounts)",
			tenantID,
			"",
			interactive,
			false,
		)
		if err != nil {
			return err
		}
		if _, parseErr := foundryid.ParseProjectID(projectID); parseErr != nil {
			return errs.Config("--project-id is not a valid Foundry project resource ID: %v", parseErr)
		}
	}

	adopted, err := hosted.AdoptPythonSource(hosted.AdoptOptions{
		Source:               source,
		Destination:          destination,
		InPlace:              inPlace,
		AgentName:            name,
		Protocol:             protocol,
		Runtime:              runtimeName,
		EntryPoint:           getFlag(cmd, "entry-point"),
		DependencyResolution: dependencyResolution,
		GuardrailPolicyID:    getFlag(cmd, "guardrail-policy-id"),
		NoGuardrail:          getBoolFlag(cmd, "no-guardrail"),
		Metadata:             commandMetadata(cmd),
	})
	if err != nil {
		return err
	}

	workspace := cliPath(adopted.Root)
	environmentCommand := fmt.Sprintf(
		"%s --workspace %s --environment %s --project-id %s --model-deployment %s --location %s",
		canonicalCommandText("hosted-environment-create"),
		workspace,
		cliPath(environment),
		cliPathOrPlaceholder(projectID, "<project-resource-id>"),
		cliPathOrPlaceholder(modelDeployment, "<model-deployment>"),
		cliPathOrPlaceholder(location, "<azure-location>"),
	)
	if tenantID != "" {
		environmentCommand += " --tenant-id " + cliPath(tenantID)
	}

	var environmentResult *quickstartEnvironmentResult
	if bootstrapEnvironment {
		environmentResult, err = bootstrapQuickstartHostedEnvironment(
			cmd,
			adopted.Root,
			name,
			environment,
			tenantID,
			location,
			projectID,
			modelDeployment,
		)
		if err != nil {
			steps := append([]string{
				fmt.Sprintf(
					"The existing source was adopted at %s; do not rerun adoption into the same workspace.",
					workspace,
				),
			}, errs.Remediation(err)...)
			steps = append(
				steps,
				"Complete or retry the local bootstrap with: "+environmentCommand,
			)
			return errs.WithNextSteps(err, steps...)
		}
	}

	operatorActions := []string{
		"Review the adopted source, dependency metadata, and azure.yaml before accepting preview behavior or provisioning.",
		"Before online Hosted commands, verify azd is authenticated to the project tenant and its identity has Foundry Project Manager on the project; AZURE_TENANT_ID configuration does not reauthenticate azd.",
	}
	if !adopted.HostingDetected {
		server := "ResponsesHostServer"
		if protocol == "invocations" {
			server = "InvocationsHostServer"
		}
		operatorActions = append([]string{
			fmt.Sprintf(
				"The selected entry point does not contain the recognized %s marker. Confirm it starts a compatible %s protocol server before deployment.",
				server,
				protocol,
			),
		}, operatorActions...)
	}
	if environmentResult != nil {
		operatorActions = append([]string{
			fmt.Sprintf(
				"The workspace-scoped azd environment %q was created or reused and configured with the supplied project context.",
				environment,
			),
		}, operatorActions...)
	}
	nextCommands := []string{
		fmt.Sprintf("%s --workspace %s", canonicalCommandText("hosted-validate"), workspace),
		fmt.Sprintf(
			"%s --workspace %s --environment %s",
			canonicalCommandText("hosted-plan"),
			workspace,
			cliPath(environment),
		),
	}
	guardrailOptOut := ""
	if adopted.NoGuardrail {
		guardrailOptOut = " --no-guardrail"
		operatorActions = append(
			operatorActions,
			"Agent-level RAI filtering was explicitly omitted; online preflight and deployment require --no-guardrail to acknowledge that opt-out.",
		)
	}
	if environmentResult == nil {
		operatorActions = append([]string{
			fmt.Sprintf(
				"Create and configure the workspace-scoped azd environment %q before online checks.",
				environment,
			),
		}, operatorActions...)
		nextCommands = append(nextCommands, environmentCommand)
	}
	nextCommands = append(
		nextCommands,
		fmt.Sprintf(
			"%s --workspace %s --environment %s --accept-preview%s",
			canonicalCommandText("hosted-preflight"),
			workspace,
			cliPath(environment),
			guardrailOptOut,
		),
		fmt.Sprintf(
			"%s --workspace %s --environment %s --accept-preview --if-changed%s",
			canonicalCommandText("hosted-deploy"),
			workspace,
			cliPath(environment),
			guardrailOptOut,
		),
		fmt.Sprintf(
			"%s --workspace %s --environment %s --accept-preview",
			canonicalCommandText("hosted-status"),
			workspace,
			cliPath(environment),
		),
	)
	mode := "copy"
	if adopted.InPlace {
		mode = "in-place"
	}
	result := hostedAdoptCommandResult{
		Action:               "hosted-adopt",
		Workspace:            adopted.Root,
		Source:               adopted.Source,
		Name:                 adopted.AgentName,
		Mode:                 mode,
		Protocol:             adopted.Protocol,
		Runtime:              adopted.Runtime,
		EntryPoint:           adopted.EntryPoint,
		DependencyResolution: adopted.DependencyResolution,
		DependencyFiles:      adopted.DependencyFiles,
		CopiedFiles:          adopted.CopiedFiles,
		HostingDetected:      adopted.HostingDetected,
		Environment:          environmentResult,
		OperatorActions:      operatorActions,
		NextCommands:         nextCommands,
	}
	return printResult(cmd, result, hostedAdoptionText(result))
}

func suggestHostedAdoptionName(source string) string {
	base := strings.ToLower(filepath.Base(filepath.Clean(source)))
	base = invalidHostedAdoptionName.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if len(base) > 63 {
		base = strings.Trim(base[:63], "-")
	}
	if base == "" {
		return "hosted-agent"
	}
	return base
}

func hostedAdoptionText(result hostedAdoptCommandResult) string {
	var output strings.Builder
	fmt.Fprintf(
		&output,
		"Hosted Python source adopted: %s\n  source: %s\n  mode: %s\n  agent: %s\n  entry point: %s\n",
		result.Workspace,
		result.Source,
		result.Mode,
		result.Name,
		result.EntryPoint,
	)
	for _, action := range result.OperatorActions {
		fmt.Fprintf(&output, "  action: %s\n", action)
	}
	for index, command := range result.NextCommands {
		prefix := "  next:"
		if index > 0 {
			prefix = "       "
		}
		fmt.Fprintf(&output, "%s %s\n", prefix, command)
	}
	return strings.TrimRight(output.String(), "\n")
}

func addHostedAdoptionSourceFlags(command *cobra.Command) {
	command.Flags().String(
		"source",
		"",
		"Existing Python agent source directory to adopt into a new Foundry Hosted Agent workspace.",
	)
	command.Flags().Bool(
		"in-place",
		false,
		"Write azure.yaml and adoption support files directly into --source instead of copying it.",
	)
	command.Flags().String(
		"entry-point",
		"",
		"Python entry point relative to --source; auto-detects main.py, app.py, agent.py, or one top-level .py file.",
	)
	command.Flags().String(
		"runtime",
		"python_3_13",
		"Hosted direct-code Python runtime: python_3_13 or python_3_14.",
	)
	command.Flags().String(
		"dependency-resolution",
		"remote_build",
		"Hosted dependency resolution: remote_build or bundled.",
	)
}
