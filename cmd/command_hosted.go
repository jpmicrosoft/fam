package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/hosted"
	projectapi "foundry-agent-manager/internal/project"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

var hostedLookPathFn hosted.LookPathFunc = hosted.DefaultLookPath
var newHostedRunnerFn = func() hosted.Runner { return hosted.ExecRunner{} }

type diagnosticHostedRunner struct {
	cmd      *cobra.Command
	delegate hosted.Runner
}

func (r diagnosticHostedRunner) Run(ctx context.Context, command hosted.Command) (hosted.Execution, error) {
	started := time.Now()
	execution, err := r.delegate.Run(ctx, command)
	duration := execution.Duration
	if duration <= 0 {
		duration = time.Since(started)
	}
	debugf(
		r.cmd,
		"hosted phase=%s executable=%s exit=%d duration=%s failed=%t",
		command.Phase,
		filepath.Base(command.Executable),
		execution.ExitCode,
		duration.Round(time.Millisecond),
		err != nil,
	)
	return execution, err
}

func newHostedRunner(cmd *cobra.Command) hosted.Runner {
	runner := newHostedRunnerFn()
	writer := cmd.ErrOrStderr()
	display := resolveHostedProgressDisplay(
		writer,
		getFlag(cmd, "output"),
		getFlag(cmd, "progress"),
		getBoolFlag(cmd, "quiet"),
		getBoolFlag(cmd, "verbose") || getBoolFlag(cmd, "debug"),
	)
	if display != hostedProgressOff {
		runner = hostedProgressRunner{
			delegate: runner,
			writer:   writer,
			display:  display,
		}
	}
	if getBoolFlag(cmd, "debug") {
		return diagnosticHostedRunner{cmd: cmd, delegate: runner}
	}
	return runner
}

type hostedInfoResult struct {
	Preview                  bool     `json:"preview" yaml:"preview"`
	AgentType                string   `json:"agentType" yaml:"agentType"`
	AzureCloudSupported      bool     `json:"azureCloudSupported" yaml:"azureCloudSupported"`
	MinimumAZDVersion        string   `json:"minimumAzdVersion" yaml:"minimumAzdVersion"`
	RequiredExtension        string   `json:"requiredExtension" yaml:"requiredExtension"`
	RequiredExtensionVersion string   `json:"requiredExtensionVersion" yaml:"requiredExtensionVersion"`
	DeploymentModes          []string `json:"deploymentModes" yaml:"deploymentModes"`
	Protocols                []string `json:"protocols" yaml:"protocols"`
	Documentation            []string `json:"documentation" yaml:"documentation"`
}

type hostedValidateResult struct {
	Valid     bool             `json:"valid" yaml:"valid"`
	Preview   bool             `json:"preview" yaml:"preview"`
	Cloud     string           `json:"cloud" yaml:"cloud"`
	Workspace hosted.Workspace `json:"workspace" yaml:"workspace"`
}

type hostedPlanResult struct {
	Preview      bool             `json:"preview" yaml:"preview"`
	Cloud        string           `json:"cloud" yaml:"cloud"`
	Environment  string           `json:"environment,omitempty" yaml:"environment,omitempty"`
	Provision    bool             `json:"provision" yaml:"provision"`
	Workspace    hosted.Workspace `json:"workspace" yaml:"workspace"`
	Actions      []string         `json:"actions" yaml:"actions"`
	Warnings     []string         `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	RemoteInvoke bool             `json:"remoteInvoke" yaml:"remoteInvoke"`
	TrafficSplit bool             `json:"trafficSplit" yaml:"trafficSplit"`
}

type hostedPreflightResult struct {
	Preview     bool                   `json:"preview" yaml:"preview"`
	Cloud       string                 `json:"cloud" yaml:"cloud"`
	Environment string                 `json:"environment,omitempty" yaml:"environment,omitempty"`
	Workspace   hosted.Workspace       `json:"workspace" yaml:"workspace"`
	Tooling     hosted.PreflightResult `json:"tooling" yaml:"tooling"`
}

type hostedEnvironmentCreateResult struct {
	Cloud       string                 `json:"cloud" yaml:"cloud"`
	Workspace   string                 `json:"workspace" yaml:"workspace"`
	Environment string                 `json:"environment" yaml:"environment"`
	Created     bool                   `json:"created" yaml:"created"`
	Reconciled  bool                   `json:"reconciled" yaml:"reconciled"`
	Configured  []string               `json:"configured,omitempty" yaml:"configured,omitempty"`
	Commands    []hosted.CommandRecord `json:"commands" yaml:"commands"`
}

type hostedStatusResult struct {
	Preview        bool                   `json:"preview" yaml:"preview"`
	Cloud          string                 `json:"cloud" yaml:"cloud"`
	Environment    string                 `json:"environment,omitempty" yaml:"environment,omitempty"`
	Service        string                 `json:"service" yaml:"service"`
	Tooling        hosted.PreflightResult `json:"tooling" yaml:"tooling"`
	Agent          hosted.Status          `json:"agent" yaml:"agent"`
	State          string                 `json:"state" yaml:"state"`
	SelectorMode   string                 `json:"selectorMode" yaml:"selectorMode"`
	ActiveVersions []string               `json:"activeVersions,omitempty" yaml:"activeVersions,omitempty"`
}

type hostedDeployResult struct {
	Status           string                          `json:"status" yaml:"status"`
	Changed          bool                            `json:"changed" yaml:"changed"`
	Preview          bool                            `json:"preview" yaml:"preview"`
	Cloud            string                          `json:"cloud" yaml:"cloud"`
	Environment      string                          `json:"environment,omitempty" yaml:"environment,omitempty"`
	Workspace        string                          `json:"workspace" yaml:"workspace"`
	Service          string                          `json:"service" yaml:"service"`
	AgentName        string                          `json:"agentName" yaml:"agentName"`
	AgentVersion     string                          `json:"agentVersion" yaml:"agentVersion"`
	AgentStatus      string                          `json:"agentStatus" yaml:"agentStatus"`
	DeploymentMode   hosted.DeploymentMode           `json:"deploymentMode" yaml:"deploymentMode"`
	Toolbox          *hosted.ToolboxRuntime          `json:"toolbox,omitempty" yaml:"toolbox,omitempty"`
	BingGrounding    *hosted.BingGroundingRuntime    `json:"bingGrounding,omitempty" yaml:"bingGrounding,omitempty"`
	BingCustomSearch *hosted.BingCustomSearchRuntime `json:"bingCustomSearch,omitempty" yaml:"bingCustomSearch,omitempty"`
	Provisioned      bool                            `json:"provisioned" yaml:"provisioned"`
	Reconciled       bool                            `json:"reconciled" yaml:"reconciled"`
	AgentEndpoints   map[string]string               `json:"agentEndpoints,omitempty" yaml:"agentEndpoints,omitempty"`
	PlaygroundURL    string                          `json:"playgroundUrl,omitempty" yaml:"playgroundUrl,omitempty"`
	AgentIdentityID  string                          `json:"agentIdentityPrincipalId,omitempty" yaml:"agentIdentityPrincipalId,omitempty"`
	Warnings         []string                        `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	DesiredHash      string                          `json:"desiredHash" yaml:"desiredHash"`
	Receipt          string                          `json:"receipt" yaml:"receipt"`
}

type hostedLifecycleResult struct {
	Action        string `json:"action" yaml:"action"`
	Preview       bool   `json:"preview" yaml:"preview"`
	Cloud         string `json:"cloud" yaml:"cloud"`
	Environment   string `json:"environment,omitempty" yaml:"environment,omitempty"`
	Workspace     string `json:"workspace" yaml:"workspace"`
	Service       string `json:"service" yaml:"service"`
	AgentName     string `json:"agentName" yaml:"agentName"`
	PreviousState string `json:"previousState,omitempty" yaml:"previousState,omitempty"`
	State         string `json:"state" yaml:"state"`
	Changed       bool   `json:"changed" yaml:"changed"`
	Reconciled    bool   `json:"reconciled" yaml:"reconciled"`
}

func cmdHostedInfo(cmd *cobra.Command, _ []string) error {
	result := hostedInfoResult{
		Preview:                  true,
		AgentType:                "hosted",
		AzureCloudSupported:      true,
		MinimumAZDVersion:        hosted.MinimumAZDVersion,
		RequiredExtension:        hosted.RequiredExtension,
		RequiredExtensionVersion: hosted.RequiredExtensionVer,
		DeploymentModes:          []string{"code", "container", "image"},
		Protocols:                []string{"responses", "invocations", "invocations_ws", "a2a"},
		Documentation: []string{
			"https://learn.microsoft.com/azure/foundry/agents/how-to/deploy-hosted-agent-code",
			"https://learn.microsoft.com/azure/foundry/agents/how-to/manage-hosted-agent",
			"https://learn.microsoft.com/azure/foundry/agents/concepts/azure-yaml-reference",
			"https://learn.microsoft.com/azure/foundry/agents/concepts/hosted-agent-permissions",
		},
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Foundry Hosted Agent deployment and lifecycle are preview-only and AzureCloud-only\n  azd: >=%s\n  extension: %s %s",
		result.MinimumAZDVersion,
		result.RequiredExtension,
		result.RequiredExtensionVersion,
	))
}

func cmdHostedValidate(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	result := hostedValidateResult{
		Valid:     true,
		Preview:   true,
		Cloud:     profile.Name,
		Workspace: workspace,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent workspace is valid: service=%s mode=%s source=%s",
		workspace.Selected.ServiceName,
		workspace.Selected.Mode,
		workspace.Selected.Source,
	))
}

func cmdHostedPlan(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	provision := getBoolFlag(cmd, "provision")
	if getBoolFlag(cmd, "preview-provision") && !provision {
		return errs.Config("--preview-provision requires --provision")
	}
	actions := []string{
		"validate azure.yaml and contained local references",
		"verify the pinned azd and azure.ai.agents command contract",
		"verify azd authentication without logging in",
	}
	if provision {
		if getBoolFlag(cmd, "preview-provision") {
			actions = append(actions, "preview azd infrastructure changes")
		}
		actions = append(actions, "provision all infrastructure declared by the azd workspace")
	}
	actions = append(
		actions,
		"deploy only service "+workspace.Selected.ServiceName+" with azd --no-prompt",
		"reconcile the immutable Hosted Agent version with azd ai agent show --output json",
		"write an atomic redacted operation receipt",
	)
	warnings := append([]string{
		"Hosted Agents and the required azd extension are preview features without an SLA.",
		"Deployment does not invoke the agent; remote invocation is billed and remains a separate operator action.",
		"Hosted Agent endpoints route 100% to one version; this command does not inspect or alter a manually pinned endpoint selector.",
	}, workspace.ContractWarnings...)
	if !provision && !workspace.ExistingProject {
		warnings = append(
			warnings,
			"--provision is disabled; the selected azd environment must already reference provisioned resources",
		)
	}
	if workspace.Selected.Mode != hosted.DeploymentModeCode {
		warnings = append(
			warnings,
			"container and image modes can require Azure Container Registry access and additional RBAC; direct code mode is preferred when possible",
		)
	}
	if workspace.Selected.Toolbox != nil {
		actions = append(
			actions,
			"require the Hosted application code to resolve and consume the declared Toolbox MCP endpoint",
			"require the Hosted runtime to enforce approval before each approval-gated Toolbox invocation",
		)
		warnings = append(
			warnings,
			"azure.yaml declares Toolbox runtime configuration, but deployment does not inject Toolbox client code or invoke tools",
			"Toolbox authentication uses the Hosted Agent identity with scope https://ai.azure.com/.default; downstream RBAC and audience configuration remain operator responsibilities",
		)
	}
	if workspace.Selected.BingGrounding != nil {
		actions = append(
			actions,
			"resolve the declared Bing project connection by name in the Hosted application",
			"attach Grounding with Bing Search through the Microsoft Agent Framework at runtime",
		)
		warnings = append(
			warnings,
			"the Hosted deployment validates the Bing Grounding declaration but cannot prove that custom application code consumes it",
			"Bing search queries, tool parameters, and the resource key cross the Azure compliance boundary; Bing tools require normal network access without VPN or private endpoints",
		)
	}
	if workspace.Selected.BingCustomSearch != nil {
		actions = append(
			actions,
			"resolve the declared Bing Custom Search project connection by name in the Hosted application",
			"attach Bing Custom Search through the Microsoft Agent Framework at runtime",
		)
		warnings = append(
			warnings,
			"the Hosted deployment validates the Bing Custom Search declaration but cannot prove that custom application code consumes it",
			"Bing search queries, tool parameters, and the resource key cross the Azure compliance boundary; Bing tools require normal network access without VPN or private endpoints",
		)
	}
	result := hostedPlanResult{
		Preview:      true,
		Cloud:        profile.Name,
		Environment:  getFlag(cmd, "environment"),
		Provision:    provision,
		Workspace:    workspace,
		Actions:      actions,
		Warnings:     warnings,
		RemoteInvoke: false,
		TrafficSplit: false,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent plan: service=%s mode=%s provision=%t actions=%d",
		workspace.Selected.ServiceName,
		workspace.Selected.Mode,
		provision,
		len(actions),
	))
}

func cmdHostedPreflight(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, true)
	if err != nil {
		return err
	}
	if err := requireHostedGuardrailIntent(cmd, workspace); err != nil {
		return err
	}
	if getBoolFlag(cmd, "preview-provision") && !getBoolFlag(cmd, "provision") {
		return errs.Config("--preview-provision requires --provision")
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		return hostedCommandError(err)
	}
	runner := newHostedRunner(cmd)
	tooling, err := hosted.CheckPreflight(ctx, hosted.PreflightOptions{
		Workspace:        workspace,
		AZDPath:          azdPath,
		Environment:      getFlag(cmd, "environment"),
		CheckEnvironment: true,
		CheckProvision:   getBoolFlag(cmd, "provision"),
		Runner:           runner,
	})
	if err != nil {
		return hostedCommandError(err)
	}
	if err := requireHostedLocation(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		func(command hosted.CommandRecord) error {
			tooling.Commands = append(tooling.Commands, command)
			return nil
		},
	); err != nil {
		return hostedCommandError(err)
	}
	projectEndpoint, err := hosted.ResolveProjectEndpoint(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		func(command hosted.CommandRecord) error {
			tooling.Commands = append(tooling.Commands, command)
			return nil
		},
	)
	if err != nil {
		return hostedCommandError(err)
	}
	if _, err := validateHostedRAIPolicy(
		ctx,
		cmd,
		profile,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		projectEndpoint,
		func(command hosted.CommandRecord) error {
			tooling.Commands = append(tooling.Commands, command)
			return nil
		},
	); err != nil {
		return err
	}
	if !getBoolFlag(cmd, "provision") {
		doctorRecord, doctorErr := hosted.RunDoctor(
			ctx,
			runner,
			azdPath,
			workspace,
			getFlag(cmd, "environment"),
			nil,
		)
		tooling.Commands = append(tooling.Commands, doctorRecord)
		if doctorErr != nil {
			return hostedCommandError(doctorErr)
		}
	}
	result := hostedPreflightResult{
		Preview:     true,
		Cloud:       profile.Name,
		Environment: getFlag(cmd, "environment"),
		Workspace:   workspace,
		Tooling:     tooling,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent preflight passed: service=%s azd=%s extension=%s",
		workspace.Selected.ServiceName,
		tooling.AZDVersion,
		tooling.AgentExtensionVer,
	))
}

func cmdHostedEnvironmentCreate(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		return hostedCommandError(err)
	}
	environment := getFlag(cmd, "environment")
	projectID := getFlag(cmd, "project-id")
	parsed, parseErr := foundryid.ParseProjectID(projectID)
	if parseErr != nil {
		return hostedCommandError(parseErr)
	}
	projectEndpoint := parsed.ProjectEndpoint()
	subscriptionID := parsed.SubscriptionID
	ensured, err := hosted.EnsureEnvironment(ctx, hosted.EnvironmentCreateOptions{
		Workspace:       workspace,
		AZDPath:         azdPath,
		Name:            environment,
		SubscriptionID:  subscriptionID,
		TenantID:        getFlag(cmd, "tenant-id"),
		Location:        getFlag(cmd, "location"),
		ProjectID:       projectID,
		ProjectEndpoint: projectEndpoint,
		ModelDeployment: getFlag(cmd, "model-deployment"),
		Runner:          newHostedRunner(cmd),
	})
	if err != nil {
		return hostedEnvironmentCreateError(err)
	}
	result := hostedEnvironmentCreateResult{
		Cloud:       profile.Name,
		Workspace:   workspace.Root,
		Environment: environment,
		Created:     ensured.Created,
		Reconciled:  ensured.Reconciled,
		Configured:  ensured.Configured,
		Commands:    ensured.Commands,
	}
	status := "already exists"
	if result.Reconciled {
		status = "created and reconciled"
	} else if result.Created {
		status = "created"
	}
	configured := ""
	if len(result.Configured) > 0 {
		configured = fmt.Sprintf(
			"\n  configured: %s",
			strings.Join(result.Configured, ", "),
		)
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted azd environment %s: name=%s workspace=%s%s",
		status,
		environment,
		workspace.Root,
		configured,
	))
}

func validateHostedRAIPolicy(
	ctx context.Context,
	cmd *cobra.Command,
	profile azcloud.Profile,
	runner hosted.Runner,
	azdPath string,
	workspace hosted.Workspace,
	environment string,
	projectEndpoint string,
	record hosted.Recorder,
) (string, error) {
	configured := workspace.Selected.RAIPolicy
	projectID, err := hosted.ResolveEnvironmentValue(
		ctx,
		runner,
		azdPath,
		workspace,
		environment,
		"AZURE_AI_PROJECT_ID",
		record,
	)
	if err != nil {
		return "", hostedCommandError(err)
	}
	projectID = strings.TrimSpace(projectID)
	projectResource, err := foundryid.ParseProjectID(projectID)
	if err != nil {
		return "", errs.Config("AZURE_AI_PROJECT_ID is not a valid Foundry project resource ID: %v", err)
	}
	if !projectResource.MatchesProjectEndpoint(projectEndpoint) {
		return "", errs.Config(
			"AZURE_AI_PROJECT_ID does not match the resolved Foundry project endpoint",
		)
	}
	if configured == nil {
		return "", nil
	}
	policyID := configured.PolicyID
	if configured.UnresolvedReference {
		resolved, err := hosted.ResolveEnvironmentValue(
			ctx,
			runner,
			azdPath,
			workspace,
			environment,
			hosted.RAIPolicyEnv,
			record,
		)
		if err != nil {
			return "", hostedCommandError(err)
		}
		policyID = resolved
	}
	policy, err := foundryid.ParseRAIPolicyID(policyID)
	if err != nil {
		return "", errs.Config("Hosted Agent RAI policy is invalid: %v", err)
	}
	if !policy.SameAccount(projectResource.Account()) {
		return "", errs.Config(
			"Hosted Agent RAI policy account must match the Foundry project account",
		)
	}
	credential, err := newCredential(cmd, profile)
	if err != nil {
		return "", err
	}
	spec := &config.ProjectSpec{
		SubscriptionID: projectResource.SubscriptionID,
		ResourceGroup:  projectResource.ResourceGroup,
		AccountName:    projectResource.AccountName,
		ARMEndpoint:    profile.ARMEndpoint,
		ARMScope:       profile.ARMScope,
	}
	if err := projectapi.InspectRAIPolicyContext(
		ctx,
		spec,
		policy.PolicyName,
		credential,
		newHTTPClient(cmd),
	); err != nil {
		return "", err
	}
	return policy.String(), nil
}

func requireHostedGuardrailIntent(cmd *cobra.Command, workspace hosted.Workspace) error {
	noGuardrail := getBoolFlag(cmd, "no-guardrail")
	if workspace.Selected.RAIPolicy == nil {
		if noGuardrail {
			return nil
		}
		return errs.Config(
			"Hosted workspace declares no agent-level RAI policy; pass --no-guardrail to explicitly accept this opt-out",
		)
	}
	if noGuardrail {
		return errs.Config(
			"--no-guardrail cannot be used because the Hosted workspace declares an RAI policy",
		)
	}
	return nil
}

func cmdHostedStatus(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	selector := runtime.Agent.VersionSelectorResolution()
	result := hostedStatusResult{
		Preview:        true,
		Cloud:          runtime.Profile.Name,
		Environment:    runtime.Environment,
		Service:        runtime.Workspace.Selected.ServiceName,
		Tooling:        runtime.Tooling,
		Agent:          runtime.Deployment,
		State:          runtime.Agent.State,
		SelectorMode:   string(selector.Mode),
		ActiveVersions: append([]string(nil), selector.ActiveVersions...),
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent status: %s selector=%s active=%s",
		hostedRuntimeSummary(runtime),
		selector.Mode,
		strings.Join(selector.ActiveVersions, ","),
	))
}

func cmdHostedDisable(cmd *cobra.Command, _ []string) error {
	return cmdHostedLifecycle(cmd, "disable", "disabled")
}

func cmdHostedEnable(cmd *cobra.Command, _ []string) error {
	return cmdHostedLifecycle(cmd, "enable", "enabled")
}

func cmdHostedLifecycle(cmd *cobra.Command, action, targetState string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	previousState := strings.ToLower(strings.TrimSpace(runtime.Agent.State))
	if previousState == targetState {
		return printHostedLifecycleResult(
			cmd,
			runtime.Profile,
			runtime.Workspace,
			runtime.Agent.Name,
			action,
			previousState,
			targetState,
			false,
			false,
		)
	}

	var mutationErr error
	switch action {
	case "disable":
		mutationErr = runtime.Client.DisableContext(runtime.Context, runtime.Agent.Name)
	case "enable":
		mutationErr = runtime.Client.EnableContext(runtime.Context, runtime.Agent.Name)
	default:
		return errs.Config("unsupported Hosted Agent lifecycle action %q", action)
	}
	if mutationErr != nil && !errs.IsAmbiguousMutation(mutationErr) {
		return mutationErr
	}
	verified, verifyErr := runtime.Client.GetAgentContext(runtime.Context, runtime.Agent.Name)
	if verifyErr == nil &&
		verified != nil &&
		strings.EqualFold(strings.TrimSpace(verified.State), targetState) {
		return printHostedLifecycleResult(
			cmd,
			runtime.Profile,
			runtime.Workspace,
			runtime.Agent.Name,
			action,
			previousState,
			targetState,
			true,
			mutationErr != nil,
		)
	}
	if mutationErr != nil {
		detail := errs.Foundry(
			"Hosted Agent %q did not verify as %s after an ambiguous %s; rerun the same command to re-read state before any retry",
			runtime.Agent.Name,
			targetState,
			action,
		)
		if verifyErr != nil {
			detail = errs.Foundry(
				"Hosted Agent %q state could not be verified after an ambiguous %s: %v; rerun the same command to re-read state before any retry",
				runtime.Agent.Name,
				action,
				verifyErr,
			)
		}
		return errs.AmbiguousMutation(errors.Join(mutationErr, detail))
	}
	if verifyErr != nil {
		return errs.AmbiguousMutation(
			errs.FoundryWrap(
				verifyErr,
				"Hosted Agent %q accepted the %s request but its state could not be verified; rerun the same command before retrying",
				runtime.Agent.Name,
				action,
			),
		)
	}
	if verified == nil {
		return errs.AmbiguousMutation(
			errs.Foundry(
				"Hosted Agent %q accepted the %s request but disappeared during state verification; rerun the same command before retrying",
				runtime.Agent.Name,
				action,
			),
		)
	}
	return errs.Transient(
		"Hosted Agent %q accepted the %s request but reported state %q instead of %q; rerun the same command to verify convergence",
		runtime.Agent.Name,
		action,
		verified.State,
		targetState,
	)
}

func printHostedLifecycleResult(
	cmd *cobra.Command,
	profile azcloud.Profile,
	workspace hosted.Workspace,
	agentName string,
	action string,
	previousState string,
	state string,
	changed bool,
	reconciled bool,
) error {
	result := hostedLifecycleResult{
		Action:        action,
		Preview:       true,
		Cloud:         profile.Name,
		Environment:   getFlag(cmd, "environment"),
		Workspace:     workspace.Root,
		Service:       workspace.Selected.ServiceName,
		AgentName:     agentName,
		PreviousState: previousState,
		State:         state,
		Changed:       changed,
		Reconciled:    reconciled,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent %s: name=%s state=%s changed=%t",
		state,
		agentName,
		state,
		changed,
	))
}

func cmdHostedDeploy(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, true)
	if err != nil {
		return err
	}
	if err := requireHostedGuardrailIntent(cmd, workspace); err != nil {
		return err
	}
	snapshot, err := hosted.ComputeDeploymentSnapshot(workspace, getFlag(cmd, "environment"))
	if err != nil {
		return err
	}
	provision := getBoolFlag(cmd, "provision")
	if getBoolFlag(cmd, "preview-provision") && !provision {
		return errs.Config("--preview-provision requires --provision")
	}
	ctx, cancel, err := hostedExecutionContext(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	receiptPath, err := hostedReceiptPath(cmd, workspace)
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		receiptPath,
		"hosted-deploy",
		profile.Name,
		receipt.ManifestReference{
			Path:        workspace.AzureYAML,
			Hash:        workspace.Hash,
			DesiredHash: snapshot.Hash,
		},
		receipt.ResourceReference{Name: workspace.Name, Endpoint: workspace.Selected.ProjectEndpoint},
		workspace.Selected.AgentName,
	)
	if err != nil {
		return err
	}
	store.Receipt.Metadata = custommetadata.MergeHosted(
		workspace.Selected.Metadata,
		commandMetadata(cmd),
	)
	if err := store.AddStep(
		"workspace-validation",
		"succeeded",
		fmt.Sprintf(
			"service=%s mode=%s source=%s; provisioning=%t",
			workspace.Selected.ServiceName,
			workspace.Selected.Mode,
			workspace.Selected.Source,
			provision,
		),
	); err != nil {
		return err
	}
	if workspace.Selected.Toolbox != nil {
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "hosted-toolbox-runtime-reference",
			Name:   hostedToolboxReference(workspace.Selected.Toolbox),
			Action: "declare",
			Status: "application-code-required",
			Reconciliation: "Verify the Hosted source loads the Toolbox MCP endpoint, authenticates as the agent identity, " +
				"and pauses for explicit approval before approval-gated calls.",
		}); err != nil {
			return err
		}
	}
	recorder := func(command hosted.CommandRecord) error {
		store.Receipt.Commands = append(store.Receipt.Commands, receipt.CommandRecord{
			Phase:      command.Phase,
			Executable: command.Executable,
			Args:       append([]string(nil), command.Args...),
			Directory:  command.Directory,
			ExitCode:   command.ExitCode,
			Duration:   command.Duration,
		})
		return store.Save()
	}

	azdPath, err := hosted.ResolveAZD(profile.Name, hostedLookPathFn)
	if err != nil {
		classified := hostedCommandError(err)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	runner := newHostedRunner(cmd)
	tooling, err := hosted.CheckPreflight(ctx, hosted.PreflightOptions{
		Workspace:        workspace,
		AZDPath:          azdPath,
		Environment:      getFlag(cmd, "environment"),
		CheckEnvironment: true,
		CheckProvision:   provision,
		Runner:           runner,
		Record:           recorder,
	})
	if err != nil {
		classified := hostedCommandError(err)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	if err := requireHostedLocation(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	); err != nil {
		classified := hostedCommandError(err)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	projectEndpoint, err := hosted.ResolveProjectEndpoint(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	)
	if err != nil {
		classified := hostedCommandError(err)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	if _, err := validateHostedRAIPolicy(
		ctx,
		cmd,
		profile,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		projectEndpoint,
		recorder,
	); err != nil {
		_ = store.AddStep(
			"hosted-rai-policy",
			"failed",
			"the configured Hosted Agent RAI policy could not be verified",
		)
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	if err := store.AddStep(
		"hosted-preflight",
		"succeeded",
		fmt.Sprintf("azd=%s extension=%s", tooling.AZDVersion, tooling.AgentExtensionVer),
	); err != nil {
		return err
	}
	if provision {
		if err := store.AddStep(
			"provision",
			"started",
			"explicitly provisioning all infrastructure declared by the azd workspace",
		); err != nil {
			return err
		}
		_, provisionErr := hosted.RunProvision(
			ctx,
			runner,
			azdPath,
			workspace,
			getFlag(cmd, "environment"),
			getBoolFlag(cmd, "preview-provision"),
			recorder,
		)
		if provisionErr != nil {
			classified := errs.AmbiguousMutation(hostedCommandError(provisionErr))
			_ = store.AddResource(receipt.ResourceChange{
				Kind:           "hosted-workspace-infrastructure",
				Name:           workspace.Name,
				Action:         "provision",
				Status:         "unknown",
				Reconciliation: "Inspect the azd environment and Azure deployment history before retrying provisioning.",
			})
			_ = store.Complete("unknown", classified)
			return releaseFailure(store.Path, classified)
		}
		if err := store.AddStep("provision", "succeeded", "azd provision completed"); err != nil {
			return err
		}
		projectEndpoint, err = hosted.ResolveProjectEndpoint(
			ctx,
			runner,
			azdPath,
			workspace,
			getFlag(cmd, "environment"),
			recorder,
		)
		if err != nil {
			classified := hostedCommandError(err)
			_ = store.AddResource(receipt.ResourceChange{
				Kind:           "hosted-workspace-infrastructure",
				Name:           workspace.Name,
				Action:         "provision",
				Status:         "succeeded-policy-revalidation-failed",
				Reconciliation: "Inspect the provisioned environment and rerun Hosted preflight before deployment.",
			})
			_ = store.Complete("failed-partial", classified)
			return releaseFailure(store.Path, classified)
		}
		if _, err := validateHostedRAIPolicy(
			ctx,
			cmd,
			profile,
			runner,
			azdPath,
			workspace,
			getFlag(cmd, "environment"),
			projectEndpoint,
			recorder,
		); err != nil {
			_ = store.AddStep(
				"hosted-rai-policy",
				"failed",
				"post-provision project and RAI policy validation failed",
			)
			_ = store.AddResource(receipt.ResourceChange{
				Kind:           "hosted-workspace-infrastructure",
				Name:           workspace.Name,
				Action:         "provision",
				Status:         "succeeded-policy-revalidation-failed",
				Reconciliation: "Inspect the provisioned project identity and RAI policy, then rerun Hosted preflight before deployment.",
			})
			_ = store.Complete("failed-partial", err)
			return releaseFailure(store.Path, err)
		}
	}
	policyDetail := "the configured account-level RAI policy exists and matches the final deployment target"
	if workspace.Selected.RAIPolicy == nil {
		policyDetail = "agent-level RAI policy explicitly omitted with --no-guardrail for the final deployment target"
	}
	if err := store.AddStep("hosted-rai-policy", "succeeded", policyDetail); err != nil {
		return err
	}
	store.Receipt.Project.Endpoint = projectEndpoint
	if err := store.Save(); err != nil {
		return err
	}
	if _, doctorErr := hosted.RunDoctor(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	); doctorErr != nil {
		classified := hostedCommandError(doctorErr)
		_ = store.AddStep(
			"hosted-project-access",
			"failed",
			"azd diagnostics did not prove project access for the deployment identity",
		)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	if err := store.AddStep(
		"hosted-project-access",
		"succeeded",
		"azd diagnostics verified project reachability for the deployment identity",
	); err != nil {
		return err
	}
	baselineStatus, _, baselineErr := hosted.ShowStatus(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	)
	switch {
	case baselineErr == nil:
		if err := store.AddStep(
			"hosted-agent-baseline",
			"succeeded",
			fmt.Sprintf("version=%s status=%s", baselineStatus.Version, baselineStatus.Status),
		); err != nil {
			return err
		}
	case errors.Is(baselineErr, hosted.ErrAgentNotDeployed):
		if err := store.AddStep(
			"hosted-agent-baseline",
			"not-deployed",
			"the selected azd environment has no deployed version for this service",
		); err != nil {
			return err
		}
	default:
		if err := store.AddStep(
			"hosted-agent-baseline",
			"unavailable",
			"the pre-deployment version could not be established; ambiguous deployment failures will fail closed",
		); err != nil {
			return err
		}
	}
	if getBoolFlag(cmd, "if-changed") && baselineErr == nil && activeHostedStatus(baselineStatus.Status) {
		credential, credentialErr := newCredential(cmd, profile)
		if credentialErr != nil {
			_ = store.Complete("failed", credentialErr)
			return releaseFailure(store.Path, credentialErr)
		}
		client := foundry.NewClientWithOptions(
			projectEndpoint,
			credential,
			newHTTPClient(cmd),
			foundry.ClientOptions{Scope: profile.FoundryScope},
		)
		agent, agentErr := client.GetAgentContext(ctx, baselineStatus.Name)
		if agentErr != nil {
			_ = store.Complete("failed", agentErr)
			return releaseFailure(store.Path, agentErr)
		}
		if agent == nil {
			notFound := errs.NotFound(
				"deployed Hosted Agent %q was not found while evaluating --if-changed",
				baselineStatus.Name,
			)
			_ = store.Complete("failed", notFound)
			return releaseFailure(store.Path, notFound)
		}
		if kindErr := requireHostedAgentContext(ctx, client, agent, baselineStatus.Version); kindErr != nil {
			_ = store.Complete("failed", kindErr)
			return releaseFailure(store.Path, kindErr)
		}
		comparison, compareErr := compareHostedDeployment(&hostedRESTRuntime{
			Context:         ctx,
			Profile:         profile,
			Workspace:       workspace,
			Environment:     getFlag(cmd, "environment"),
			AZDPath:         azdPath,
			Runner:          runner,
			Tooling:         tooling,
			Deployment:      baselineStatus,
			ProjectEndpoint: projectEndpoint,
			Client:          client,
			Agent:           agent,
		}, snapshot)
		if compareErr != nil {
			_ = store.Complete("failed", compareErr)
			return releaseFailure(store.Path, compareErr)
		}
		if !comparison.Changed {
			store.Receipt.Agent.ID = agent.ID
			store.Receipt.Agent.LatestVersionBefore = baselineStatus.Version
			store.Receipt.Agent.LatestVersionAfter = baselineStatus.Version
			store.Receipt.Agent.ActiveVersionBefore = firstVersion(
				agent.VersionSelectorResolution().ActiveVersions,
			)
			store.Receipt.Agent.ActiveVersionAfter = store.Receipt.Agent.ActiveVersionBefore
			store.Receipt.Agent.SelectorBefore = hostedSelectorState(agent)
			store.Receipt.Agent.SelectorAfter = store.Receipt.Agent.SelectorBefore
			store.Receipt.Agent.Changed = false
			if err := store.AddStep(
				"if-changed",
				"skipped",
				fmt.Sprintf(
					"deployable snapshot and remote latest version match receipt %s",
					comparison.ReceiptPath,
				),
			); err != nil {
				return err
			}
			if err := store.Complete("succeeded", nil); err != nil {
				return err
			}
			return printHostedDeployResult(
				cmd,
				profile,
				workspace,
				baselineStatus,
				provision,
				false,
				false,
				snapshot.Hash,
				store.Path,
			)
		}
		if err := store.AddStep(
			"if-changed",
			"changed",
			strings.Join(comparison.Reasons, "; "),
		); err != nil {
			return err
		}
	}

	if err := store.AddStep(
		"hosted-agent-version",
		"started",
		"creating an immutable Hosted Agent version; no remote invocation will be run",
	); err != nil {
		return err
	}
	_, deployErr := hosted.RunDeploy(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	)
	if deployErr != nil {
		status, _, statusErr := hosted.ShowStatus(
			ctx,
			runner,
			azdPath,
			workspace,
			getFlag(cmd, "environment"),
			recorder,
		)
		deploymentAdvanced := errors.Is(baselineErr, hosted.ErrAgentNotDeployed) ||
			(baselineErr == nil && baselineStatus.Version != status.Version)
		if statusErr == nil && activeHostedStatus(status.Status) && deploymentAdvanced {
			populateHostedReceipt(store, status)
			if err := store.AddStep(
				"hosted-agent-version",
				"succeeded-reconciled",
				"azd deploy returned an error, but azd ai agent show verified an active immutable version",
			); err != nil {
				return err
			}
			if err := recordHostedRuntimeObligations(store, workspace, status); err != nil {
				return err
			}
			if err := store.Complete("succeeded-reconciled", nil); err != nil {
				return err
			}
			return printHostedDeployResult(
				cmd,
				profile,
				workspace,
				status,
				provision,
				true,
				true,
				snapshot.Hash,
				store.Path,
			)
		}
		classified := hostedCommandError(deployErr)
		if statusErr != nil {
			classified = errors.Join(classified, hostedCommandError(statusErr))
		}
		ambiguous := errs.AmbiguousMutation(classified)
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "hosted-agent-version",
			Name:           workspace.Selected.AgentName,
			Action:         "create-version",
			Status:         "unknown",
			Reconciliation: "Run fam hosted status for the same workspace, service, and environment before retrying.",
		})
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}

	status, _, err := hosted.ShowStatus(
		ctx,
		runner,
		azdPath,
		workspace,
		getFlag(cmd, "environment"),
		recorder,
	)
	if err != nil {
		classified := errs.AmbiguousMutation(hostedCommandError(err))
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "hosted-agent-version",
			Name:           workspace.Selected.AgentName,
			Action:         "reconcile",
			Status:         "unknown",
			Reconciliation: "The deploy command succeeded, but status could not be verified. Run fam hosted status before another deployment.",
		})
		_ = store.Complete("unknown", classified)
		return releaseFailure(store.Path, classified)
	}
	if !activeHostedStatus(status.Status) {
		classified := errs.Transient(
			"Hosted Agent %s version %s is %s after azd deploy; reconcile with fam hosted status before retrying",
			status.Name,
			status.Version,
			status.Status,
		)
		_ = store.Complete("unknown", classified)
		return releaseFailure(store.Path, classified)
	}
	populateHostedReceipt(store, status)
	if err := store.AddStep(
		"hosted-agent-version",
		"succeeded",
		fmt.Sprintf("verified version=%s status=%s", status.Version, status.Status),
	); err != nil {
		return err
	}
	if err := recordHostedRuntimeObligations(store, workspace, status); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printHostedDeployResult(
		cmd,
		profile,
		workspace,
		status,
		provision,
		false,
		true,
		snapshot.Hash,
		store.Path,
	)
}

func recordHostedRuntimeObligations(
	store *receipt.OperationStore,
	workspace hosted.Workspace,
	status hosted.Status,
) error {
	if status.InstanceIdentity != nil && status.InstanceIdentity.PrincipalID != "" {
		if err := store.AddStep(
			"agent-identity",
			"succeeded",
			"downstream Azure RBAC must target the Hosted Agent identity principal when the agent accesses external resources",
		); err != nil {
			return err
		}
	}
	if workspace.Selected.Toolbox != nil {
		if err := store.AddStep(
			"toolbox-runtime",
			"operator-action-required",
			"deployment preserved the Toolbox declaration; application code must consume it and enforce per-invocation approval",
		); err != nil {
			return err
		}
	}
	if workspace.Selected.BingGrounding != nil {
		if err := store.AddStep(
			"bing-grounding-runtime",
			"operator-action-required",
			"deployment preserved the Bing Grounding connection declaration; application code must resolve the project connection and attach the runtime tool",
		); err != nil {
			return err
		}
	}
	if workspace.Selected.BingCustomSearch != nil {
		if err := store.AddStep(
			"bing-custom-search-runtime",
			"operator-action-required",
			"deployment preserved the Bing Custom Search declaration; application code must resolve the project connection and attach the runtime tool",
		); err != nil {
			return err
		}
	}
	return nil
}

func resolveHostedWorkspace(
	cmd *cobra.Command,
	requirePreviewAcceptance bool,
) (azcloud.Profile, hosted.Workspace, error) {
	cloudName := selectedCloudName(cmd, "")
	if cloudName == "" {
		cloudName = azcloud.AzureCloud
	}
	profile, err := azcloud.Resolve(cloudName)
	if err != nil {
		return azcloud.Profile{}, hosted.Workspace{}, err
	}
	if !profile.Capabilities.HostedAgents {
		return azcloud.Profile{}, hosted.Workspace{}, errs.Config(
			"Foundry Hosted Agents are unavailable in %s; no commercial-cloud fallback is allowed",
			profile.Name,
		)
	}
	if requirePreviewAcceptance && !getBoolFlag(cmd, "accept-preview") {
		return azcloud.Profile{}, hosted.Workspace{}, errs.Config(
			"Hosted Agent preview was not explicitly accepted; pass --accept-preview after reviewing the preview limitations",
		)
	}
	if err := hosted.ValidateEnvironmentName(getFlag(cmd, "environment")); err != nil {
		return azcloud.Profile{}, hosted.Workspace{}, err
	}
	workspace, err := hosted.LoadWorkspace(getFlag(cmd, "workspace"), getFlag(cmd, "service"))
	if err != nil {
		return azcloud.Profile{}, hosted.Workspace{}, err
	}
	return profile, workspace, nil
}

func hostedCommandError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return err
	case errors.Is(err, hosted.ErrAuthentication):
		return errs.WithNextSteps(
			errs.Auth("%v", err),
			"Run azd auth login outside fam.",
			"Then rerun fam hosted preflight with the same workspace and environment.",
		)
	case errors.Is(err, hosted.ErrEnvironment):
		return errs.WithNextSteps(
			errs.Config("%v", err),
			"Create a missing environment with fam hosted environment create --workspace <workspace> --environment <environment> --project-id <project-resource-id> --model-deployment <deployment> --location <azure-location>.",
			"Set required values, including AZURE_LOCATION, with hosted environment create or azd env set, then rerun the Hosted command.",
		)
	case errors.Is(err, hosted.ErrProjectEndpoint):
		return errs.WithNextSteps(
			errs.Config("%v", err),
			"Provision the workspace when it owns the project resources, or run fam hosted environment create --workspace <workspace> --environment <environment> --project-id <project-resource-id> --model-deployment <deployment> --location <azure-location>.",
			"Then rerun the Hosted command with the same workspace and environment.",
		)
	case errors.Is(err, hosted.ErrProjectID):
		return errs.WithNextSteps(
			errs.Config("%v", err),
			"Configure the full Foundry project resource ID with fam hosted environment create --workspace <workspace> --environment <environment> --project-id <project-resource-id> --model-deployment <deployment> --location <azure-location>.",
			"Then rerun fam hosted preflight; it must verify the azd deployment identity before deploy.",
		)
	case errors.Is(err, hosted.ErrProjectAccess):
		return errs.WithNextSteps(
			errs.Authorization("%v", err),
			"Confirm azd is authenticated to the tenant that owns the Foundry project; when needed, run azd auth logout followed by azd auth login --tenant-id <tenant-id>.",
			"Assign Foundry Project Manager on the target project to the azd deployment identity.",
			"Then rerun fam hosted preflight with the same workspace and environment.",
		)
	case errors.Is(err, hosted.ErrAgentNotDeployed):
		return errs.WithNextSteps(
			errs.Config("%v", err),
			"Verify the selected service and azd environment, then deploy it with fam hosted deploy.",
			"Run fam hosted plan first when you need to review whether provisioning is required.",
		)
	case errors.Is(err, hosted.ErrHostedUnsupported),
		errors.Is(err, hosted.ErrMissingAZD),
		errors.Is(err, hosted.ErrAZDTooOld),
		errors.Is(err, hosted.ErrMissingExtension):
		return errs.Config("%v", err)
	case errors.Is(err, hosted.ErrOutputTooLarge):
		return errs.Security("%v", err)
	case errors.Is(err, hosted.ErrCommandFailed),
		errors.Is(err, hosted.ErrInvalidStatus):
		return errs.ToolBuild("%v", err)
	default:
		return err
	}
}

func requireHostedLocation(
	ctx context.Context,
	runner hosted.Runner,
	azdPath string,
	workspace hosted.Workspace,
	environment string,
	record hosted.Recorder,
) error {
	location, err := hosted.ResolveEnvironmentValue(
		ctx,
		runner,
		azdPath,
		workspace,
		environment,
		"AZURE_LOCATION",
		record,
	)
	if err != nil {
		return fmt.Errorf(
			"%w: AZURE_LOCATION is required for Hosted deployment; configure it with hosted environment create --location <azure-location>: %v",
			hosted.ErrEnvironment,
			err,
		)
	}
	if strings.TrimSpace(location) == "" {
		return fmt.Errorf(
			"%w: AZURE_LOCATION is required for Hosted deployment; configure it with hosted environment create --location <azure-location>",
			hosted.ErrEnvironment,
		)
	}
	return nil
}

func hostedEnvironmentCreateError(err error) error {
	if errors.Is(err, hosted.ErrEnvironment) {
		return errs.WithNextSteps(
			errs.Config("%v", err),
			"Run hosted environment create with --project-id <project-resource-id>, --model-deployment <deployment>, and --location <azure-location>; add --tenant-id <tenant-id> for cross-tenant context.",
			"Inspect the local result with azd env list --cwd <workspace>, then rerun hosted preflight with the same environment.",
		)
	}
	return hostedCommandError(err)
}

func hostedReceiptPath(cmd *cobra.Command, workspace hosted.Workspace) (string, error) {
	if path := getFlag(cmd, "receipt"); path != "" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", errs.Config("failed to resolve --receipt path: %v", err)
		}
		return filepath.Clean(absolute), nil
	}
	return receipt.OperationPath(
		workspace.AzureYAML,
		"hosted-deploy",
		workspace.Selected.AgentName,
		time.Now(),
	), nil
}

func populateHostedReceipt(store *receipt.OperationStore, status hosted.Status) {
	store.Receipt.Agent.ID = status.ID
	store.Receipt.Agent.CreatedVersion = status.Version
	store.Receipt.Agent.LatestVersionAfter = status.Version
	store.Receipt.Agent.Changed = true
}

func activeHostedStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active", "idle":
		return true
	default:
		return false
	}
}

func printHostedDeployResult(
	cmd *cobra.Command,
	profile azcloud.Profile,
	workspace hosted.Workspace,
	status hosted.Status,
	provisioned bool,
	reconciled bool,
	changed bool,
	desiredHash string,
	receiptPath string,
) error {
	operationStatus := "succeeded"
	if reconciled {
		operationStatus = "succeeded-reconciled"
	} else if !changed {
		operationStatus = "unchanged"
	}
	warnings := append([]string(nil), workspace.ContractWarnings...)
	if status.InstanceIdentity == nil || status.InstanceIdentity.PrincipalID == "" {
		warnings = append(
			warnings,
			"azd status did not report an agent identity principal; verify identity before assigning downstream RBAC",
		)
	}
	identityID := ""
	if status.InstanceIdentity != nil {
		identityID = status.InstanceIdentity.PrincipalID
	}
	result := hostedDeployResult{
		Status:           operationStatus,
		Changed:          changed,
		Preview:          true,
		Cloud:            profile.Name,
		Environment:      getFlag(cmd, "environment"),
		Workspace:        workspace.Root,
		Service:          workspace.Selected.ServiceName,
		AgentName:        status.Name,
		AgentVersion:     status.Version,
		AgentStatus:      status.Status,
		DeploymentMode:   workspace.Selected.Mode,
		Toolbox:          workspace.Selected.Toolbox,
		BingGrounding:    workspace.Selected.BingGrounding,
		BingCustomSearch: workspace.Selected.BingCustomSearch,
		Provisioned:      provisioned,
		Reconciled:       reconciled,
		AgentEndpoints:   status.AgentEndpoints,
		PlaygroundURL:    status.PlaygroundURL,
		AgentIdentityID:  identityID,
		Warnings:         warnings,
		DesiredHash:      desiredHash,
		Receipt:          receiptPath,
	}
	text := fmt.Sprintf(
		"Hosted Agent deployed: name=%s version=%s status=%s mode=%s\n  receipt: %s",
		status.Name,
		status.Version,
		status.Status,
		workspace.Selected.Mode,
		receiptPath,
	)
	if !changed {
		text = fmt.Sprintf(
			"Hosted Agent deploy skipped: name=%s version=%s snapshot=%s\n  receipt: %s",
			status.Name,
			status.Version,
			desiredHash,
			receiptPath,
		)
	}
	return printResult(cmd, result, text)
}

func hostedToolboxReference(toolbox *hosted.ToolboxRuntime) string {
	if toolbox == nil {
		return ""
	}
	if toolbox.Name != "" {
		return toolbox.Name
	}
	return toolbox.Endpoint
}

func addHostedWorkspaceFlags(command *cobra.Command) {
	command.Flags().String("workspace", "", "Path to an existing azd workspace containing azure.yaml.")
	command.Flags().String("service", "", "Hosted azure.ai.agent service name when azure.yaml defines more than one.")
	command.Flags().String("environment", "", "Optional existing azd environment name.")
	requireFlags(command, "workspace")
}

func addHostedProvisionFlags(command *cobra.Command) {
	command.Flags().Bool(
		"provision",
		false,
		"Explicitly run azd provision before deployment; otherwise infrastructure is never provisioned.",
	)
	command.Flags().Bool(
		"preview-provision",
		false,
		"Run azd provision --preview before an explicitly requested --provision.",
	)
}

func addHostedPreviewFlag(command *cobra.Command) {
	command.Flags().Bool(
		"accept-preview",
		false,
		"Explicitly accept the Foundry Hosted Agent and azure.ai.agents preview limitations.",
	)
	command.Flags().Duration(
		"azd-timeout",
		time.Hour,
		"Maximum total time for Hosted Agent azd and lifecycle operations.",
	)
}

func addHostedSessionIDFlags(command *cobra.Command) {
	command.Flags().String("session-id", "", "Hosted Agent session id.")
	command.Flags().String(
		"isolation-key",
		"",
		"Isolation key for endpoints configured with Header isolation.",
	)
	requireFlags(command, "session-id")
}

func addHostedConfirmationFlags(command *cobra.Command) {
	command.Flags().Bool("dry-run", false, "Print the destructive action without applying it.")
	command.Flags().Bool("yes", false, "Confirm the destructive operation without an interactive prompt.")
}

func addHostedReceiptFlag(command *cobra.Command) {
	command.Flags().String("receipt", "", "Operation receipt path (defaults beside azure.yaml).")
}

func hostedExecutionContext(cmd *cobra.Command) (context.Context, context.CancelFunc, error) {
	timeout := getDurationFlag(cmd, "azd-timeout")
	if timeout <= 0 {
		return nil, nil, errs.Config("--azd-timeout must be greater than zero")
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), timeout)
	return ctx, cancel, nil
}
