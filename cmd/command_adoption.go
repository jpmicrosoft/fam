package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"runtime"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/hosted"

	"github.com/spf13/cobra"
)

type quickstartResult struct {
	Type            string                       `json:"type" yaml:"type"`
	Path            string                       `json:"path" yaml:"path"`
	Name            string                       `json:"name" yaml:"name"`
	Environment     *quickstartEnvironmentResult `json:"environment,omitempty" yaml:"environment,omitempty"`
	NextCommands    []string                     `json:"nextCommands" yaml:"nextCommands"`
	OperatorActions []string                     `json:"operatorActions,omitempty" yaml:"operatorActions,omitempty"`
}

type quickstartEnvironmentResult struct {
	Name       string   `json:"name" yaml:"name"`
	Created    bool     `json:"created" yaml:"created"`
	Reconciled bool     `json:"reconciled" yaml:"reconciled"`
	Configured []string `json:"configured,omitempty" yaml:"configured,omitempty"`
}

func cmdQuickstart(cmd *cobra.Command, _ []string) error {
	reader := bufio.NewReader(cmd.InOrStdin())
	interactive := !getBoolFlag(cmd, "non-interactive") &&
		strings.EqualFold(getFlag(cmd, "output"), "text")

	agentType, err := quickstartValue(
		cmd,
		reader,
		"Deployment type (prompt or hosted)",
		getFlag(cmd, "type"),
		"prompt",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	agentType = strings.ToLower(strings.TrimSpace(agentType))
	if agentType != "prompt" && agentType != "hosted" {
		return errs.Config("--type must be prompt or hosted")
	}
	if agentType == "hosted" && interactive && !cmd.Flags().Changed("source") {
		source, sourceErr := quickstartValue(
			cmd,
			reader,
			"Existing Python source folder to adopt (leave blank to generate starter code)",
			"",
			"",
			true,
			false,
		)
		if sourceErr != nil {
			return sourceErr
		}
		if source != "" {
			if err := cmd.Flags().Set("source", source); err != nil {
				return err
			}
		}
	}
	adoptingHostedSource := agentType == "hosted" &&
		(strings.TrimSpace(getFlag(cmd, "source")) != "" || getBoolFlag(cmd, "in-place"))
	if agentType != "hosted" &&
		(strings.TrimSpace(getFlag(cmd, "source")) != "" || getBoolFlag(cmd, "in-place")) {
		return errs.Config("--source and --in-place are supported only with --type hosted")
	}
	if agentType == "prompt" && getBoolFlag(cmd, "no-guardrail") {
		return errs.Config(
			"--no-guardrail is supported only with --type hosted; Prompt Agents inherit the model deployment policy when --guardrail-policy-id is omitted",
		)
	}

	defaultDestination := "agent.yaml"
	destinationLabel := "Manifest file to create"
	if agentType == "hosted" {
		defaultDestination = "hosted-agent"
		destinationLabel = "New local workspace folder to create (relative path)"
	}
	if interactive {
		if err := writeQuickstartIntroduction(cmd, agentType, adoptingHostedSource); err != nil {
			return err
		}
	}
	destination := ""
	if !getBoolFlag(cmd, "in-place") {
		destination, err = quickstartValue(
			cmd,
			reader,
			destinationLabel,
			getFlag(cmd, "destination"),
			defaultDestination,
			interactive,
			true,
		)
		if err != nil {
			return err
		}
	}
	name, err := quickstartValue(
		cmd,
		reader,
		"Agent name to use in Foundry",
		getFlag(cmd, "name"),
		"support-agent",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	if err := cmd.Flags().Set("destination", destination); err != nil {
		return err
	}
	if err := cmd.Flags().Set("name", name); err != nil {
		return err
	}

	switch agentType {
	case "prompt":
		return quickstartPrompt(cmd, reader, interactive, destination, name)
	case "hosted":
		if adoptingHostedSource {
			return runHostedAdoption(cmd, reader, interactive, destination, name)
		}
		return quickstartHosted(cmd, reader, interactive, destination, name)
	default:
		return errs.Config("unsupported quickstart type %q", agentType)
	}
}

func quickstartPrompt(
	cmd *cobra.Command,
	reader *bufio.Reader,
	interactive bool,
	destination string,
	name string,
) error {
	model, err := quickstartValue(
		cmd,
		reader,
		"Model deployment name (must already exist)",
		getFlag(cmd, "model"),
		"",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	projectResourceID, err := quickstartValue(
		cmd,
		reader,
		"Foundry project resource ID (must end with /projects/<project>)",
		getFlag(cmd, "project-resource-id"),
		"",
		interactive,
		true,
	)
	if err != nil {
		return err
	}
	for flag, value := range map[string]string{
		"model":               model,
		"project-resource-id": projectResourceID,
	} {
		if err := cmd.Flags().Set(flag, value); err != nil {
			return err
		}
	}
	scaffold, err := createPromptScaffold(cmd)
	if err != nil {
		return err
	}
	result := quickstartResult{
		Type: "prompt",
		Path: scaffold.Manifest,
		Name: name,
		OperatorActions: []string{
			fmt.Sprintf(
				"Confirm the Foundry project and model deployment %q already exist. If the child project is missing, add project.location to the manifest, then run foundry-agent-manager project-create before foundry-agent-manager preflight.",
				model,
			),
		},
		NextCommands: []string{
			fmt.Sprintf("%s -f %s", canonicalCommandText("validate"), cliPath(scaffold.Manifest)),
			fmt.Sprintf("%s -f %s", canonicalCommandText("plan"), cliPath(scaffold.Manifest)),
			fmt.Sprintf("%s -f %s", canonicalCommandText("preflight"), cliPath(scaffold.Manifest)),
			fmt.Sprintf("%s -f %s --if-changed", canonicalCommandText("deploy"), cliPath(scaffold.Manifest)),
			fmt.Sprintf("%s -f %s", canonicalCommandText("status"), cliPath(scaffold.Manifest)),
		},
	}
	return printResult(cmd, result, quickstartText(result))
}

func quickstartHosted(
	cmd *cobra.Command,
	reader *bufio.Reader,
	interactive bool,
	destination string,
	name string,
) error {
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
		// Validate and derive from the project resource ID.
		if projectID != "" {
			if _, parseErr := foundryid.ParseProjectID(projectID); parseErr != nil {
				return errs.Config("--project-id is not a valid Foundry project resource ID: %v", parseErr)
			}
		}
	}
	if err := cmd.Flags().Set("protocol", protocol); err != nil {
		return err
	}
	scaffold, err := createHostedScaffold(cmd)
	if err != nil {
		return err
	}
	workspace := cliPath(scaffold.Root)
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
			scaffold.Root,
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
					"The Hosted workspace was created at %s; do not rerun quickstart into the same destination.",
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
		"Review the generated source and infrastructure before accepting preview behavior or provisioning.",
		"Before online Hosted commands, verify azd is authenticated to the project tenant and its identity has Foundry Project Manager on the project; AZURE_TENANT_ID configuration does not reauthenticate azd.",
	}
	if projectID == "" {
		operatorActions = append(
			operatorActions,
			"Set the Foundry project resource ID with --project-id before preflight; AZURE_AI_PROJECT_ID is required to verify the azd deployment identity.",
		)
	}
	nextCommands := []string{
		fmt.Sprintf("%s --workspace %s", canonicalCommandText("hosted-validate"), workspace),
		fmt.Sprintf("%s --workspace %s --environment %s", canonicalCommandText("hosted-plan"), workspace, cliPath(environment)),
	}
	guardrailOptOut := ""
	if scaffold.NoGuardrail {
		guardrailOptOut = " --no-guardrail"
		operatorActions = append(
			operatorActions,
			"Agent-level RAI filtering was explicitly omitted; online preflight and deployment require --no-guardrail to acknowledge that opt-out.",
		)
	}
	if environmentResult == nil {
		operatorActions = append([]string{
			fmt.Sprintf(
				"Create and configure the workspace-scoped azd environment %q with the generated hosted environment create command before online checks.",
				environment,
			),
		}, operatorActions...)
		nextCommands = append(nextCommands, environmentCommand)
	} else {
		operatorActions = append([]string{
			fmt.Sprintf(
				"The workspace-scoped azd environment %q was created or reused and configured with the supplied project context.",
				environment,
			),
		}, operatorActions...)
	}
	nextCommands = append(
		nextCommands,
		fmt.Sprintf("%s --workspace %s --environment %s --accept-preview%s", canonicalCommandText("hosted-preflight"), workspace, cliPath(environment), guardrailOptOut),
		fmt.Sprintf("%s --workspace %s --environment %s --accept-preview --if-changed%s", canonicalCommandText("hosted-deploy"), workspace, cliPath(environment), guardrailOptOut),
		fmt.Sprintf("%s --workspace %s --environment %s --accept-preview", canonicalCommandText("hosted-status"), workspace, cliPath(environment)),
	)
	result := quickstartResult{
		Type:            "hosted",
		Path:            scaffold.Root,
		Name:            name,
		Environment:     environmentResult,
		OperatorActions: operatorActions,
		NextCommands:    nextCommands,
	}
	return printResult(cmd, result, quickstartText(result))
}

func bootstrapQuickstartHostedEnvironment(
	cmd *cobra.Command,
	workspaceRoot,
	serviceName,
	environment,
	tenantID,
	location,
	projectID,
	modelDeployment string,
) (*quickstartEnvironmentResult, error) {
	cloudName := selectedCloudName(cmd, "")
	if cloudName == "" {
		cloudName = azcloud.AzureCloud
	}
	profile, err := azcloud.Resolve(cloudName)
	if err != nil {
		return nil, err
	}
	if !profile.Capabilities.HostedAgents {
		return nil, errs.Config(
			"Foundry Hosted Agents are unavailable in %s; no commercial-cloud fallback is allowed",
			profile.Name,
		)
	}
	workspace, err := hosted.LoadWorkspace(workspaceRoot, serviceName)
	if err != nil {
		return nil, err
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return nil, err
	}
	defer cancel()
	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		return nil, hostedCommandError(err)
	}
	// Derive endpoint and subscription from the project resource ID.
	var projectEndpoint, subscriptionID string
	if projectID != "" {
		parsed, parseErr := foundryid.ParseProjectID(projectID)
		if parseErr != nil {
			return nil, errs.Config("--project-id is not a valid Foundry project resource ID: %v", parseErr)
		}
		projectEndpoint = parsed.ProjectEndpoint()
		subscriptionID = parsed.SubscriptionID
	}
	ensured, err := hosted.EnsureEnvironment(ctx, hosted.EnvironmentCreateOptions{
		Workspace:       workspace,
		AZDPath:         azdPath,
		Name:            environment,
		SubscriptionID:  subscriptionID,
		TenantID:        tenantID,
		Location:        location,
		ProjectID:       projectID,
		ProjectEndpoint: projectEndpoint,
		ModelDeployment: modelDeployment,
		Runner:          newHostedRunner(cmd),
	})
	if err != nil {
		return nil, hostedEnvironmentCreateError(err)
	}
	return &quickstartEnvironmentResult{
		Name:       ensured.Name,
		Created:    ensured.Created,
		Reconciled: ensured.Reconciled,
		Configured: append([]string(nil), ensured.Configured...),
	}, nil
}

func writeQuickstartIntroduction(
	cmd *cobra.Command,
	agentType string,
	adoptingHostedSource bool,
) error {
	switch agentType {
	case "prompt":
		_, err := fmt.Fprintln(
			cmd.ErrOrStderr(),
			"\nCreating a Prompt Agent manifest. This writes a local configuration file only; it does not contact Azure or deploy an agent.",
		)
		return err
	case "hosted":
		if adoptingHostedSource {
			_, err := fmt.Fprintln(
				cmd.ErrOrStderr(),
				"\nAdopting existing Python agent source as a new Foundry Hosted Agent. Copy mode leaves the source untouched; --in-place explicitly writes the workspace files into the source folder. This can bootstrap local azd environment state but never creates Azure resources or deploys the agent.",
			)
			return err
		}
		_, err := fmt.Fprintln(
			cmd.ErrOrStderr(),
			"\nCreating a Hosted Agent workspace. This writes local starter files and can bootstrap its workspace-scoped azd environment; it never creates Azure resources or deploys an agent.\nThe workspace folder must be a new relative path inside the current directory.",
		)
		return err
	default:
		return nil
	}
}

func quickstartValue(
	cmd *cobra.Command,
	reader *bufio.Reader,
	label string,
	current string,
	defaultValue string,
	interactive bool,
	required bool,
) (string, error) {
	current = strings.TrimSpace(current)
	if current != "" {
		return current, nil
	}
	if !interactive {
		if defaultValue != "" {
			return defaultValue, nil
		}
		if !required {
			return "", nil
		}
		return "", errs.Config(
			"%s is required in non-interactive or structured-output mode",
			strings.ToLower(label),
		)
	}

	prompt := label
	if defaultValue != "" {
		prompt += " [" + defaultValue + "]"
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s: ", prompt); err != nil {
		return "", err
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	if required && value == "" {
		return "", errs.Config("%s is required", strings.ToLower(label))
	}
	return value, nil
}

func cliPathOrPlaceholder(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return placeholder
	}
	return cliPath(value)
}

func quickstartConfirmation(
	cmd *cobra.Command,
	reader *bufio.Reader,
	label string,
	defaultValue bool,
) (bool, error) {
	suffix := " [y/N]"
	if defaultValue {
		suffix = " [Y/n]"
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "%s%s: ", label, suffix); err != nil {
		return false, err
	}
	value, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return defaultValue, nil
	case "y", "yes", "true", "1":
		return true, nil
	case "n", "no", "false", "0":
		return false, nil
	default:
		return false, errs.Config("%s must be answered yes or no", strings.ToLower(label))
	}
}

func quickstartText(result quickstartResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "%s quickstart created: %s\n", result.Type, result.Path)
	for _, action := range result.OperatorActions {
		fmt.Fprintf(&text, "  action: %s\n", action)
	}
	text.WriteString("  next:\n")
	for _, command := range result.NextCommands {
		fmt.Fprintf(&text, "    %s\n", command)
	}
	return strings.TrimRight(text.String(), "\n")
}

func cliPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return `""`
	}
	if strings.HasSuffix(value, `\`) ||
		strings.ContainsAny(value, "\x00\r\n\"`$%&|;<>!^()") {
		return "<local-value>"
	}
	return `"` + value + `"`
}

type doctorCheck struct {
	Name       string   `json:"name" yaml:"name"`
	Category   string   `json:"category" yaml:"category"`
	Status     string   `json:"status" yaml:"status"`
	Severity   string   `json:"severity" yaml:"severity"`
	Details    string   `json:"details" yaml:"details"`
	Observed   string   `json:"observed,omitempty" yaml:"observed,omitempty"`
	Required   string   `json:"required,omitempty" yaml:"required,omitempty"`
	DurationMS int64    `json:"durationMs,omitempty" yaml:"durationMs,omitempty"`
	NextSteps  []string `json:"nextSteps,omitempty" yaml:"nextSteps,omitempty"`
}

type doctorSummary struct {
	Passed  int `json:"passed" yaml:"passed"`
	Failed  int `json:"failed" yaml:"failed"`
	Warning int `json:"warning" yaml:"warning"`
	Skipped int `json:"skipped" yaml:"skipped"`
}

type doctorDebugInfo struct {
	OS             string `json:"os" yaml:"os"`
	Architecture   string `json:"architecture" yaml:"architecture"`
	GoVersion      string `json:"goVersion" yaml:"goVersion"`
	RequestTimeout string `json:"requestTimeout" yaml:"requestTimeout"`
	RetryCount     int    `json:"retryCount" yaml:"retryCount"`
	RetryDelay     string `json:"retryDelay" yaml:"retryDelay"`
}

type doctorResult struct {
	Ready            bool             `json:"ready" yaml:"ready"`
	LocalReady       bool             `json:"localReady" yaml:"localReady"`
	OnlineReady      *bool            `json:"onlineReady,omitempty" yaml:"onlineReady,omitempty"`
	DeploymentReady  *bool            `json:"deploymentReady,omitempty" yaml:"deploymentReady,omitempty"`
	ChecksComplete   bool             `json:"checksComplete" yaml:"checksComplete"`
	CoverageComplete bool             `json:"coverageComplete" yaml:"coverageComplete"`
	Scope            string           `json:"scope" yaml:"scope"`
	Mode             string           `json:"mode" yaml:"mode"`
	Cloud            string           `json:"cloud" yaml:"cloud"`
	Online           bool             `json:"online" yaml:"online"`
	Summary          doctorSummary    `json:"summary" yaml:"summary"`
	Debug            *doctorDebugInfo `json:"debug,omitempty" yaml:"debug,omitempty"`
	Checks           []doctorCheck    `json:"checks" yaml:"checks"`
}

func cmdDoctor(cmd *cobra.Command, _ []string) error {
	manifestPath := strings.TrimSpace(getFlag(cmd, "manifest"))
	workspacePath := strings.TrimSpace(getFlag(cmd, "workspace"))
	if manifestPath != "" && workspacePath != "" {
		return errs.Config("doctor accepts either --manifest or --workspace, not both")
	}
	if getBoolFlag(cmd, "check-provision") && (!getBoolFlag(cmd, "online") || workspacePath == "") {
		return errs.Config("--check-provision requires --workspace and --online")
	}
	if strings.TrimSpace(getFlag(cmd, "environment")) != "" && workspacePath == "" {
		return errs.Config("--environment is only valid with --workspace")
	}

	profile, err := azcloud.Resolve(selectedCloudName(cmd, ""))
	if err != nil {
		return err
	}
	result := doctorResult{
		Ready:            true,
		ChecksComplete:   true,
		CoverageComplete: true,
		Scope:            "environment",
		Mode:             "environment",
		Cloud:            profile.Name,
		Online:           getBoolFlag(cmd, "online"),
	}
	if getBoolFlag(cmd, "debug") {
		result.Debug = &doctorDebugInfo{
			OS:             runtime.GOOS,
			Architecture:   runtime.GOARCH,
			GoVersion:      runtime.Version(),
			RequestTimeout: getDurationFlag(cmd, "request-timeout").String(),
			RetryCount:     getIntFlag(cmd, "retry-count"),
			RetryDelay:     getDurationFlag(cmd, "retry-delay").String(),
		}
	}
	addDoctorCheck(&result, doctorCheck{
		Name:     "binary",
		Category: "environment",
		Status:   "passed",
		Severity: "info",
		Details:  buildMetadata(),
	})
	addDoctorCheck(&result, doctorCheck{
		Name:     "cloud",
		Category: "environment",
		Status:   "passed",
		Severity: "info",
		Details:  "AzureCloud is selected; unsupported clouds are rejected before authentication",
		Observed: profile.Name,
		Required: "AzureCloud",
	})

	switch {
	case manifestPath != "":
		result.Mode = "prompt"
		result.Scope = doctorScope(result.Online)
		runPromptDoctor(cmd, &result)
	case workspacePath != "":
		result.Mode = "hosted"
		result.Scope = doctorScope(result.Online)
		runHostedDoctor(cmd, &result)
	default:
		result.Ready = false
		result.ChecksComplete = false
		result.CoverageComplete = false
		addDoctorCheck(&result, doctorCheck{
			Name:     "target",
			Category: "configuration",
			Status:   "warning",
			Severity: "warning",
			Details:  "no --manifest or --workspace was supplied; deployment readiness was not evaluated",
			NextSteps: []string{
				"Pass -f <manifest> for Prompt diagnostics, or --workspace <path> for Hosted diagnostics.",
			},
		})
	}
	finalizeDoctorResult(&result)
	if err := printResult(cmd, result, doctorText(result)); err != nil {
		return err
	}
	if getBoolFlag(cmd, "fail-on-not-ready") && !result.Ready {
		return errs.ReportedExit(11)
	}
	return nil
}

func doctorScope(online bool) string {
	if online {
		return "online"
	}
	return "local"
}

func runPromptDoctor(cmd *cobra.Command, result *doctorResult) {
	started := time.Now()
	prepared, err := prepareAgent(cmd)
	if err != nil {
		addDoctorFailure(result, "prompt-configuration", "configuration", err, time.Since(started))
		addDoctorBlocked(result, "prompt-online", "access", "online checks require a valid Prompt manifest")
		addPromptCoverageChecks(result, false, true)
		return
	}
	result.LocalReady = true
	addDoctorCheck(result, doctorCheck{
		Name:     "prompt-configuration",
		Category: "configuration",
		Status:   "passed",
		Severity: "info",
		Details: fmt.Sprintf(
			"manifest, contained files, and %d declarative tool(s) are valid",
			len(prepared.WireTools),
		),
		Observed:   fmt.Sprintf("%d tool(s)", len(prepared.WireTools)),
		Required:   "valid manifest and contained dependencies",
		DurationMS: doctorDurationMS(time.Since(started)),
	})
	if !result.Online {
		addDoctorCheck(result, doctorCheck{
			Name:     "prompt-online",
			Category: "access",
			Status:   "skipped",
			Severity: "info",
			Details:  "authentication, project, connectivity, and permission checks require --online",
			NextSteps: []string{
				"Rerun with --online before deployment when Azure readiness must be proven.",
			},
		})
		addPromptCoverageChecks(result, prepared.Resolved.Config.Agent.RAIPolicyID != "", true)
		return
	}

	authStarted := time.Now()
	credential, err := newCredential(cmd, prepared.Resolved.Config.Cloud)
	if err != nil {
		addDoctorFailure(result, "prompt-authentication", "authentication", err, time.Since(authStarted))
		addDoctorBlocked(result, "prompt-project-access", "access", "project access requires a usable Azure credential")
		addPromptCoverageChecks(result, prepared.Resolved.Config.Agent.RAIPolicyID != "", true)
		return
	}
	addDoctorCheck(result, doctorCheck{
		Name:       "prompt-authentication",
		Category:   "authentication",
		Status:     "passed",
		Severity:   "info",
		Details:    "DefaultAzureCredential was initialized for AzureCloud",
		Required:   "usable Azure credential chain",
		DurationMS: doctorDurationMS(time.Since(authStarted)),
	})

	onlineStarted := time.Now()
	state, preflightErr := runPreflight(cmd, prepared, credential, newHTTPClient(cmd))
	if state != nil {
		for _, check := range state.Result.Checks {
			addDoctorCheck(result, doctorCheck{
				Name:     "prompt-" + check.Name,
				Category: promptCheckCategory(check.Name),
				Status:   check.Status,
				Severity: doctorSeverity(check.Status),
				Details:  check.Details,
			})
		}
	}
	if preflightErr != nil {
		addDoctorFailure(
			result,
			promptFailureName(preflightErr),
			promptFailureCategory(preflightErr),
			preflightErr,
			time.Since(onlineStarted),
		)
	} else {
		addDoctorCheck(result, doctorCheck{
			Name:       "prompt-online",
			Category:   "access",
			Status:     "passed",
			Severity:   "info",
			Details:    "all supported Prompt authentication, project, destination, and connectivity checks passed",
			Required:   "readable Foundry project and approved destinations",
			DurationMS: doctorDurationMS(time.Since(onlineStarted)),
		})
	}
	addPromptCoverageChecks(result, prepared.Resolved.Config.Agent.RAIPolicyID != "", false)
}

func runHostedDoctor(cmd *cobra.Command, result *doctorResult) {
	started := time.Now()
	profile, workspace, err := resolveHostedWorkspace(cmd, result.Online)
	if err != nil {
		addDoctorFailure(result, "hosted-workspace", "configuration", err, time.Since(started))
		addDoctorBlocked(result, "hosted-online", "tooling", "online checks require a valid Hosted workspace")
		addHostedCoverageChecks(result)
		return
	}
	result.LocalReady = true
	addDoctorCheck(result, doctorCheck{
		Name:     "hosted-workspace",
		Category: "configuration",
		Status:   "passed",
		Severity: "info",
		Details: fmt.Sprintf(
			"workspace is valid: service=%s mode=%s source=%s",
			workspace.Selected.ServiceName,
			workspace.Selected.Mode,
			workspace.Selected.Source,
		),
		Observed:   string(workspace.Selected.Mode),
		Required:   "valid hosted azure.ai.agent workspace",
		DurationMS: doctorDurationMS(time.Since(started)),
	})
	if !result.Online {
		addDoctorCheck(result, doctorCheck{
			Name:     "hosted-online",
			Category: "tooling",
			Status:   "skipped",
			Severity: "info",
			Details:  "azd, extension, authentication, environment, project, and permission checks require --online --accept-preview",
			NextSteps: []string{
				"Create or select an azd environment with azd env new <environment> --cwd <workspace>, then rerun with --online --accept-preview and the same --environment value.",
			},
		})
		addHostedCoverageChecks(result)
		return
	}

	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		addDoctorFailure(result, "hosted-timeout", "environment", err, 0)
		addHostedCoverageChecks(result)
		return
	}
	defer cancel()
	azdStarted := time.Now()
	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		addDoctorFailure(result, "hosted-azd", "tooling", hostedCommandError(err), time.Since(azdStarted))
		for _, name := range []string{
			"hosted-agent-extension-installed",
			"hosted-agent-extension-command",
			"hosted-deploy-contract",
			"hosted-status-contract",
			"hosted-azd-authentication",
			"hosted-azd-environment",
			"hosted-project-endpoint",
			"hosted-project-access",
		} {
			addDoctorBlocked(result, name, "tooling", "the Azure Developer CLI executable is required")
		}
		addHostedCoverageChecks(result)
		return
	}
	addDoctorCheck(result, doctorCheck{
		Name:       "hosted-azd",
		Category:   "tooling",
		Status:     "passed",
		Severity:   "info",
		Details:    "Azure Developer CLI executable was found",
		Observed:   "azd executable found",
		Required:   "azd executable",
		DurationMS: doctorDurationMS(time.Since(azdStarted)),
	})

	runner := newHostedRunner(cmd)
	diagnostics := hosted.DiagnosePreflight(ctx, hosted.PreflightOptions{
		Workspace:        workspace,
		AZDPath:          azdPath,
		Environment:      getFlag(cmd, "environment"),
		CheckEnvironment: true,
		CheckProvision:   getBoolFlag(cmd, "check-provision"),
		Runner:           runner,
	})
	for _, check := range diagnostics.Checks {
		nextSteps := []string(nil)
		details := check.Details
		if check.Error != nil {
			classified := hostedCommandError(check.Error)
			details = fmt.Sprintf("%s: %v", errs.KindOf(classified), classified)
			nextSteps = errs.Remediation(classified)
		}
		addDoctorCheck(result, doctorCheck{
			Name:       "hosted-" + check.Name,
			Category:   hostedCheckCategory(check.Name),
			Status:     check.Status,
			Severity:   doctorSeverity(check.Status),
			Details:    details,
			Observed:   check.Observed,
			Required:   check.Required,
			DurationMS: doctorDurationMS(check.Duration),
			NextSteps:  nextSteps,
		})
	}
	if !hostedDiagnosticsTrusted(diagnostics, getBoolFlag(cmd, "check-provision")) {
		addDoctorBlocked(
			result,
			"hosted-project-endpoint",
			"security",
			"project endpoint resolution was blocked until the azd binary and Hosted Agent extension contracts are trusted",
		)
		addDoctorBlocked(
			result,
			"hosted-project-access",
			"security",
			"credential and project access checks were blocked until the Hosted tooling trust boundary passes",
		)
		addHostedCoverageChecks(result)
		return
	}

	endpointStarted := time.Now()
	endpoint, endpointErr := hosted.ResolveProjectEndpoint(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		nil,
	)
	if endpointErr != nil {
		addDoctorFailure(
			result,
			"hosted-project-endpoint",
			"configuration",
			hostedCommandError(endpointErr),
			time.Since(endpointStarted),
		)
		addDoctorBlocked(result, "hosted-project-access", "access", "project access requires a valid project endpoint")
	} else {
		addDoctorCheck(result, doctorCheck{
			Name:       "hosted-project-endpoint",
			Category:   "configuration",
			Status:     "passed",
			Severity:   "info",
			Details:    "the selected environment resolves a valid Foundry project endpoint",
			Observed:   safeEndpointLabel(endpoint),
			Required:   "AzureCloud Foundry project endpoint",
			DurationMS: doctorDurationMS(time.Since(endpointStarted)),
		})
		accessStarted := time.Now()
		credential, credentialErr := newCredential(cmd, profile)
		if credentialErr != nil {
			addDoctorFailure(
				result,
				"hosted-project-credential",
				"authentication",
				credentialErr,
				time.Since(accessStarted),
			)
			addDoctorBlocked(result, "hosted-project-access", "access", "project access requires a usable Azure credential")
		} else {
			client := foundry.NewClientWithOptions(
				endpoint,
				credential,
				newHTTPClient(cmd),
				foundry.ClientOptions{Scope: profile.FoundryScope},
			)
			if accessErr := client.ProbeContext(ctx); accessErr != nil {
				addDoctorFailure(
					result,
					"hosted-project-access",
					"access",
					accessErr,
					time.Since(accessStarted),
				)
			} else {
				addDoctorCheck(result, doctorCheck{
					Name:       "hosted-project-access",
					Category:   "access",
					Status:     "passed",
					Severity:   "info",
					Details:    "DefaultAzureCredential can read the selected Foundry project data plane",
					Observed:   "project data-plane read succeeded",
					Required:   "Foundry project read access",
					DurationMS: doctorDurationMS(time.Since(accessStarted)),
				})
			}
		}
	}
	addHostedCoverageChecks(result)
}

func addPromptCoverageChecks(result *doctorResult, hasRAIPolicy bool, includeModelGap bool) {
	if includeModelGap {
		addCoverageGap(
			result,
			"prompt-model-deployment",
			"model deployment existence requires an online read against the selected Foundry project",
			"Rerun doctor with --online or run preflight before deployment.",
		)
	}
	if hasRAIPolicy {
		addCoverageGap(
			result,
			"prompt-rai-policy",
			"the configured RAI policy reference is schema-valid but no stable read-only validation is available in this workflow",
			"Verify the policy assignment in Foundry before production deployment.",
		)
	}
	addCoverageGap(
		result,
		"prompt-quota-capacity",
		"quota and model capacity are not queried by doctor",
		"Confirm quota and deployment capacity in the target Foundry account.",
	)
	addCoverageGap(
		result,
		"prompt-downstream-rbac",
		"permissions required by external tools and downstream resources remain operator-owned",
		"Verify each external resource grants the deployed identity only the required roles.",
	)
	addCoverageGap(
		result,
		"prompt-smoke-test",
		"agent invocation is intentionally skipped because it can create billable usage",
		"Run smoke explicitly after deployment when billable invocation is approved.",
	)
}

func addHostedCoverageChecks(result *doctorResult) {
	addCoverageGap(
		result,
		"hosted-model-runtime",
		"Hosted application code can choose model deployments at runtime, so doctor cannot infer or validate every model reference",
		"Validate model references used by the Hosted application before production rollout.",
	)
	addCoverageGap(
		result,
		"hosted-rai-policy",
		"Hosted application code can select model and RAI policy behavior at runtime, so doctor cannot infer every policy reference",
		"Verify the model deployment and RAI policy used by the Hosted application.",
	)
	addCoverageGap(
		result,
		"hosted-quota-capacity",
		"Hosted compute and model quota or capacity are not queried by doctor",
		"Confirm quota and regional capacity for the selected environment.",
	)
	addCoverageGap(
		result,
		"hosted-provision-permissions",
		"subscription and resource-group mutation permissions are not exercised because doctor never runs provision, including preview",
		"Review the deployment identity's Azure roles before using --provision.",
	)
	addCoverageGap(
		result,
		"hosted-identity-alignment",
		"azd authentication and DefaultAzureCredential project access are checked separately and may resolve to different identities",
		"Ensure the azd deployment identity has the same required target access before deployment.",
	)
	addCoverageGap(
		result,
		"hosted-downstream-rbac",
		"downstream permissions for the Hosted Agent identity are not assigned or inferred",
		"After deployment, grant the reported Hosted Agent principal only the required downstream roles.",
	)
	addCoverageGap(
		result,
		"hosted-smoke-test",
		"agent invocation is intentionally skipped because it can create billable usage",
		"Run foundry-agent-manager hosted smoke explicitly after deployment when billable invocation is approved.",
	)
}

func addCoverageGap(result *doctorResult, name, details, nextStep string) {
	result.CoverageComplete = false
	addDoctorCheck(result, doctorCheck{
		Name:      name,
		Category:  "coverage",
		Status:    "skipped",
		Severity:  "info",
		Details:   details,
		NextSteps: []string{nextStep},
	})
}

func addDoctorBlocked(result *doctorResult, name, category, details string) {
	result.ChecksComplete = false
	addDoctorCheck(result, doctorCheck{
		Name:     name,
		Category: category,
		Status:   "skipped",
		Severity: "warning",
		Details:  details,
	})
}

func addDoctorFailure(result *doctorResult, name, category string, err error, duration time.Duration) {
	addDoctorCheck(result, doctorCheck{
		Name:       name,
		Category:   category,
		Status:     "failed",
		Severity:   "error",
		Details:    fmt.Sprintf("%s: %v", errs.KindOf(err), err),
		DurationMS: doctorDurationMS(duration),
		NextSteps:  errs.Remediation(err),
	})
}

func addDoctorCheck(result *doctorResult, check doctorCheck) {
	if check.Category == "" {
		check.Category = "general"
	}
	if check.Severity == "" {
		check.Severity = doctorSeverity(check.Status)
	}
	if check.Status == "failed" {
		result.Ready = false
	}
	result.Checks = append(result.Checks, check)
}

func finalizeDoctorResult(result *doctorResult) {
	result.Summary = doctorSummary{}
	for _, check := range result.Checks {
		switch check.Status {
		case "passed":
			result.Summary.Passed++
		case "failed":
			result.Summary.Failed++
		case "warning":
			result.Summary.Warning++
		case "skipped":
			result.Summary.Skipped++
		}
	}
	if result.Mode == "environment" {
		result.Ready = false
	}
	if result.Online {
		onlineReady := result.Ready && result.LocalReady
		result.OnlineReady = &onlineReady
		deploymentReady := onlineReady
		result.DeploymentReady = &deploymentReady
	}
}

func doctorDurationMS(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	milliseconds := duration.Milliseconds()
	if milliseconds == 0 {
		return 1
	}
	return milliseconds
}

func doctorSeverity(status string) string {
	switch status {
	case "failed":
		return "error"
	case "warning":
		return "warning"
	default:
		return "info"
	}
}

func promptCheckCategory(name string) string {
	switch {
	case strings.Contains(name, "destination"), strings.Contains(name, "secret"):
		return "security"
	case strings.Contains(name, "project"), strings.Contains(name, "foundry"), strings.Contains(name, "connection"):
		return "access"
	default:
		return "configuration"
	}
}

func promptFailureName(err error) string {
	switch errs.KindOf(err) {
	case "auth":
		return "prompt-authentication"
	case "authorization":
		return "prompt-authorization"
	case "not_found":
		return "prompt-project-access"
	case "security":
		return "prompt-destination-approval"
	case "foundry", "transient":
		return "prompt-connectivity"
	default:
		return "prompt-online"
	}
}

func promptFailureCategory(err error) string {
	switch errs.KindOf(err) {
	case "auth":
		return "authentication"
	case "authorization":
		return "authorization"
	case "security":
		return "security"
	case "foundry", "transient":
		return "connectivity"
	case "not_found":
		return "access"
	default:
		return "configuration"
	}
}

func hostedCheckCategory(name string) string {
	switch {
	case strings.Contains(name, "authorization"):
		return "authorization"
	case strings.Contains(name, "auth"):
		return "authentication"
	case strings.Contains(name, "environment"):
		return "environment"
	case strings.Contains(name, "contract"), strings.Contains(name, "extension"), strings.Contains(name, "version"):
		return "tooling"
	default:
		return "configuration"
	}
}

func hostedDiagnosticsTrusted(diagnostics hosted.PreflightDiagnostics, checkProvision bool) bool {
	required := map[string]bool{
		"azd-version":               false,
		"agent-extension-installed": false,
		"agent-extension-command":   false,
		"deploy-contract":           false,
		"status-contract":           false,
	}
	if checkProvision {
		required["provision-contract"] = false
	}
	for _, check := range diagnostics.Checks {
		if _, exists := required[check.Name]; exists && check.Status == "passed" {
			required[check.Name] = true
		}
	}
	for _, passed := range required {
		if !passed {
			return false
		}
	}
	return true
}

func safeEndpointLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<validated Foundry project endpoint>"
	}
	return parsed.Scheme + "://" + parsed.Host + "/api/projects/<redacted>"
}

func doctorText(result doctorResult) string {
	var text strings.Builder
	readiness := "NOT READY"
	if result.Ready {
		if result.Scope == "online" {
			readiness = "DEPLOYMENT READY"
		} else {
			readiness = "LOCAL READY"
		}
	}
	fmt.Fprintf(
		&text,
		"doctor: %s  mode=%s scope=%s cloud=%s checks-complete=%t coverage-complete=%t\n",
		readiness,
		result.Mode,
		result.Scope,
		result.Cloud,
		result.ChecksComplete,
		result.CoverageComplete,
	)
	fmt.Fprintf(
		&text,
		"  summary: pass=%d fail=%d warn=%d skip=%d\n",
		result.Summary.Passed,
		result.Summary.Failed,
		result.Summary.Warning,
		result.Summary.Skipped,
	)
	for _, check := range result.Checks {
		symbol := doctorStatusSymbol(check.Status)
		fmt.Fprintf(&text, "  %s %s/%s: %s\n", symbol, check.Category, check.Name, check.Details)
		if check.Observed != "" {
			fmt.Fprintf(&text, "    observed: %s\n", check.Observed)
		}
		if check.Required != "" {
			fmt.Fprintf(&text, "    required: %s\n", check.Required)
		}
		if check.DurationMS > 0 {
			fmt.Fprintf(&text, "    duration: %dms\n", check.DurationMS)
		}
		for _, step := range check.NextSteps {
			fmt.Fprintf(&text, "    next: %s\n", step)
		}
	}
	return strings.TrimRight(text.String(), "\n")
}

func doctorStatusSymbol(status string) string {
	switch status {
	case "passed":
		return "[PASS]"
	case "failed":
		return "[FAIL]"
	case "warning":
		return "[WARN]"
	case "skipped":
		return "[SKIP]"
	default:
		return "[" + status + "]"
	}
}
