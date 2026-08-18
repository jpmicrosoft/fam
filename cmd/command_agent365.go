package main

import (
	"context"
	"fmt"
	"strings"

	"foundry-agent-manager/internal/agent365"
	"foundry-agent-manager/internal/azcloud"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"

	"github.com/spf13/cobra"
)

type agent365InfoResult struct {
	Cloud                    string   `json:"cloud" yaml:"cloud"`
	GraphEndpoint            string   `json:"graphEndpoint" yaml:"graphEndpoint"`
	GraphScope               string   `json:"graphScope" yaml:"graphScope"`
	Permissions              []string `json:"permissions" yaml:"permissions"`
	ReadOnly                 bool     `json:"readOnly" yaml:"readOnly"`
	MutationsRequireApproval bool     `json:"mutationsRequireApproval" yaml:"mutationsRequireApproval"`
	BindingMutationSupported bool     `json:"bindingMutationSupported" yaml:"bindingMutationSupported"`
	Capabilities             []string `json:"capabilities" yaml:"capabilities"`
	Limitations              []string `json:"limitations" yaml:"limitations"`
	Documentation            []string `json:"documentation" yaml:"documentation"`
}

type agent365PermissionsResult struct {
	Blueprint           agent365.Blueprint               `json:"blueprint" yaml:"blueprint"`
	Permissions         []agent365.InheritablePermission `json:"inheritablePermissions" yaml:"inheritablePermissions"`
	ResolvedPermissions []agent365.ResolvedPermission    `json:"resolvedPermissions,omitempty" yaml:"resolvedPermissions,omitempty"`
}

type agent365BindingTarget struct {
	Type            string
	Cloud           string
	Name            string
	ProjectEndpoint string
	Agent           *foundry.Agent
}

type agent365BindingStatusResult struct {
	TargetType               string                           `json:"targetType" yaml:"targetType"`
	Cloud                    string                           `json:"cloud" yaml:"cloud"`
	AgentName                string                           `json:"agentName" yaml:"agentName"`
	ProjectEndpoint          string                           `json:"projectEndpoint" yaml:"projectEndpoint"`
	InstanceIdentity         *foundry.AgentIdentity           `json:"instanceIdentity,omitempty" yaml:"instanceIdentity,omitempty"`
	FoundryBlueprint         *foundry.AgentIdentity           `json:"foundryBlueprint,omitempty" yaml:"foundryBlueprint,omitempty"`
	BlueprintReference       *foundry.AgentBlueprintReference `json:"blueprintReference,omitempty" yaml:"blueprintReference,omitempty"`
	RequestedBlueprint       *agent365.Blueprint              `json:"requestedBlueprint,omitempty" yaml:"requestedBlueprint,omitempty"`
	Correlation              string                           `json:"correlation" yaml:"correlation"`
	Identity                 agent365IdentityEvidence         `json:"identity" yaml:"identity"`
	BindingMutationSupported bool                             `json:"bindingMutationSupported" yaml:"bindingMutationSupported"`
}

type agent365BindingPlanResult struct {
	agent365BindingStatusResult `json:",inline" yaml:",inline"`
	ChangeRequired              bool     `json:"changeRequired" yaml:"changeRequired"`
	Executable                  bool     `json:"executable" yaml:"executable"`
	Steps                       []string `json:"steps" yaml:"steps"`
}

func newAgent365Commands() []*cobra.Command {
	info := &cobra.Command{
		Use:          "agent365-info",
		Short:        "Explain Agent 365 blueprint inspection and Foundry integration boundaries.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365Info,
		SilenceUsage: true,
	}
	list := &cobra.Command{
		Use:          "agent365-blueprint-list",
		Short:        "List Agent 365 identity blueprints from Microsoft Graph.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintList,
		SilenceUsage: true,
	}
	list.Flags().Int(
		"limit",
		100,
		"Page size and maximum blueprints without --all (1-100).",
	)
	list.Flags().Bool(
		"all",
		false,
		"Follow bounded Microsoft Graph continuation links (maximum 5000 results).",
	)

	show := &cobra.Command{
		Use:          "agent365-blueprint-show",
		Short:        "Show non-secret metadata for one Agent 365 identity blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintShow,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(show)

	permissions := &cobra.Command{
		Use:          "agent365-blueprint-permissions",
		Short:        "Show requested and inheritable permissions for an Agent 365 blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintPermissions,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(permissions)
	permissions.Flags().Bool(
		"resolve-names",
		false,
		"Resolve requested permission IDs to resource and permission names; requires Application.Read.All.",
	)

	validate := &cobra.Command{
		Use:          "agent365-blueprint-validate",
		Short:        "Validate documented readiness properties for an Agent 365 blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintValidate,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(validate)
	validate.Flags().Bool(
		"fail-on-invalid",
		false,
		"Exit with code 11 after printing the result when a blocking validation check fails.",
	)

	plan := &cobra.Command{
		Use:          "agent365-binding-plan",
		Short:        "Plan correlation of a Foundry Agent with an existing Agent 365 blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BindingPlan,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(plan)
	addAgent365BindingTargetFlags(plan)
	plan.Flags().Bool(
		"resolve-identity",
		false,
		"Resolve the Foundry instance identity through Microsoft Graph; requires AgentIdentity.Read.All.",
	)

	status := &cobra.Command{
		Use:          "agent365-binding-status",
		Short:        "Show Foundry-managed identity and blueprint correlation for an agent.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BindingStatus,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(status)
	addAgent365BindingTargetFlags(status)
	status.Flags().Bool(
		"resolve-identity",
		false,
		"Resolve the Foundry instance identity through Microsoft Graph; requires AgentIdentity.Read.All.",
	)

	commands := []*cobra.Command{info, list, show, permissions, validate, plan, status}
	return append(commands, newAgent365ExpansionCommands()...)
}

func cmdAgent365Info(cmd *cobra.Command, _ []string) error {
	profile, err := resolveAgent365Profile(cmd)
	if err != nil {
		return err
	}
	result := agent365InfoResult{
		Cloud:         profile.Name,
		GraphEndpoint: profile.GraphEndpoint,
		GraphScope:    profile.GraphScope,
		Permissions: []string{
			"AgentIdentityBlueprint.Read.All",
			"AgentIdentity.Read.All",
			"AgentIdentityBlueprintPrincipal.Read.All",
			"Application.Read.All for sponsors, friendly permission names, and observability assignments",
		},
		ReadOnly:                 false,
		MutationsRequireApproval: true,
		BindingMutationSupported: false,
		Capabilities: []string{
			"List and inspect Agent ID blueprints, identities, and blueprint principals through Microsoft Graph v1.0.",
			"Validate Microsoft disablement, manager applications, requested access, and all documented inheritance modes.",
			"Inspect blueprint owners, sponsors, associated identities, and optional friendly permission names.",
			"Correlate Foundry Agent identity fields with Microsoft Entra identities for Prompt and Hosted targets.",
			"Inspect and explicitly set Foundry account Agent 365 activity-data collection through the documented ARM preview property.",
			"Validate Hosted Agent local observability integration and the required Agent365.Observability.OtelWrite app role.",
			"Plan publication, identity migration, registry verification, and administrator handoff without inventing unsupported mutations.",
		},
		Limitations: []string{
			"A blueprint is an identity template, not deployable agent source or configuration.",
			"No documented Foundry API binds an arbitrary existing Agent 365 blueprint to an existing Prompt or Hosted Agent.",
			"The manager never treats local metadata, generated Agent 365 configuration, or matching IDs alone as a successful binding.",
			"Blueprint credentials are never requested, read, logged, or emitted.",
			"Shared-versus-distinct lifecycle identity cannot be classified authoritatively from project-agent and directory identity fields alone.",
			"Registry and generic Autopilot publication mutations are not exposed without a stable documented API.",
		},
		Documentation: []string{
			"https://learn.microsoft.com/graph/api/agentidentityblueprint-list?view=graph-rest-1.0",
			"https://learn.microsoft.com/graph/api/agentidentity-list?view=graph-rest-1.0",
			"https://learn.microsoft.com/graph/api/agentidentityblueprintprincipal-list?view=graph-rest-1.0",
			"https://learn.microsoft.com/azure/foundry/agents/concepts/agent-identity",
			"https://learn.microsoft.com/azure/foundry/agents/how-to/agent-365-integration",
		},
	}
	return printResult(
		cmd,
		result,
		"Agent 365 support includes read-only inventory, planning, and one confirmed account-level logging mutation; arbitrary existing-blueprint binding and registry publication are not supported",
	)
}

func cmdAgent365BlueprintList(cmd *cobra.Command, _ []string) error {
	limit := getIntFlag(cmd, "limit")
	if err := agent365.ValidateListLimit(limit); err != nil {
		return err
	}
	if getBoolFlag(cmd, "all") && cmd.Flags().Changed("limit") {
		return errs.Config("--all and --limit are mutually exclusive")
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	result, err := client.ListBlueprintsPaginated(
		commandContext(cmd),
		limit,
		agent365.PaginationOptions{All: getBoolFlag(cmd, "all")},
	)
	if err != nil {
		return err
	}
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"Agent 365 blueprints: count=%d truncated=%t",
		result.Count,
		result.Truncated,
	)
	for _, blueprint := range result.Blueprints {
		fmt.Fprintf(
			&text,
			"\n  name=%q app-id=%s object-id=%s",
			blueprint.DisplayName,
			blueprint.AppID,
			blueprint.ObjectID,
		)
	}
	return printResult(cmd, result, text.String())
}

func cmdAgent365BlueprintShow(cmd *cobra.Command, _ []string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
	if err != nil {
		return err
	}
	return printResult(cmd, blueprint, fmt.Sprintf(
		"Agent 365 blueprint: name=%s app-id=%s object-id=%s",
		blueprint.DisplayName,
		blueprint.AppID,
		blueprint.ObjectID,
	))
}

func cmdAgent365BlueprintPermissions(cmd *cobra.Command, _ []string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
	if err != nil {
		return err
	}
	permissions, err := client.ListInheritablePermissions(commandContext(cmd), blueprint.ObjectID)
	if err != nil {
		return err
	}
	result := agent365PermissionsResult{
		Blueprint: *blueprint, Permissions: permissions,
	}
	if getBoolFlag(cmd, "resolve-names") {
		resolved, err := client.ResolvePermissions(commandContext(cmd), *blueprint)
		if err != nil {
			return err
		}
		result.ResolvedPermissions = resolved
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 blueprint permissions: requested-resources=%d inheritable-resources=%d",
		len(blueprint.RequiredResourceAccess),
		len(permissions),
	))
}

func cmdAgent365BlueprintValidate(cmd *cobra.Command, _ []string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
	if err != nil {
		return err
	}
	permissions, err := client.ListInheritablePermissions(commandContext(cmd), blueprint.ObjectID)
	if err != nil {
		return err
	}
	result := agent365.Validate(*blueprint, permissions)
	if err := printResult(cmd, result, fmt.Sprintf(
		"Agent 365 blueprint validation: name=%s valid=%t checks=%d",
		blueprint.DisplayName,
		result.Valid,
		len(result.Checks),
	)); err != nil {
		return err
	}
	if getBoolFlag(cmd, "fail-on-invalid") && !result.Valid {
		return errs.ReportedExit(11)
	}
	return nil
}

func cmdAgent365BindingStatus(cmd *cobra.Command, _ []string) error {
	selector, err := optionalAgent365Selector(cmd)
	if err != nil {
		return err
	}
	target, cancel, err := resolveAgent365BindingTarget(cmd)
	if err != nil {
		return err
	}
	if cancel != nil {
		defer cancel()
	}
	result := bindingStatusFromTarget(target)
	if getBoolFlag(cmd, "resolve-identity") {
		if err := resolveBindingIdentity(
			commandContext(cmd),
			cmd,
			target,
			&result,
		); err != nil {
			return err
		}
	}
	if selector.AppID != "" || selector.ObjectID != "" {
		client, err := newAgent365Client(cmd)
		if err != nil {
			return err
		}
		blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
		if err != nil {
			return err
		}
		result.RequestedBlueprint = blueprint
		result.Correlation = blueprintCorrelation(target.Agent, *blueprint)
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 binding status: target=%s agent=%s correlation=%s mutation-supported=false",
		result.TargetType,
		result.AgentName,
		result.Correlation,
	))
}

func cmdAgent365BindingPlan(cmd *cobra.Command, _ []string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	target, cancel, err := resolveAgent365BindingTarget(cmd)
	if err != nil {
		return err
	}
	if cancel != nil {
		defer cancel()
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
	if err != nil {
		return err
	}
	status := bindingStatusFromTarget(target)
	status.RequestedBlueprint = blueprint
	status.Correlation = blueprintCorrelation(target.Agent, *blueprint)
	if getBoolFlag(cmd, "resolve-identity") {
		identity, err := resolveTargetAgentIdentity(
			commandContext(cmd),
			cmd,
			target,
		)
		if err != nil {
			return err
		}
		status.Identity = classifyAgent365Identity(target.Agent, identity)
	}
	result := agent365BindingPlanResult{
		agent365BindingStatusResult: status,
		ChangeRequired:              status.Correlation != "matched",
		Executable:                  false,
		Steps: []string{
			"Treat the Foundry identity response as correlation evidence only; it is not proof of an operator-created binding.",
			"Do not write blueprint IDs into agent metadata or generated Agent 365 files and report that as a successful binding.",
			"Use a documented Foundry publishing flow when Microsoft exposes one that creates or assigns the Agent 365 identity.",
			"Continue to grant Azure RBAC separately to the runtime identity that actually accesses downstream resources.",
		},
	}
	if status.Correlation == "matched" {
		result.Steps = append([]string{
			"The current Foundry blueprint client ID or blueprint reference matches the requested blueprint; no mutation is planned.",
		}, result.Steps...)
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 binding plan: target=%s agent=%s correlation=%s change-required=%t executable=%t",
		result.TargetType,
		result.AgentName,
		result.Correlation,
		result.ChangeRequired,
		result.Executable,
	))
}

func newAgent365Client(cmd *cobra.Command) (*agent365.Client, error) {
	profile, err := resolveAgent365Profile(cmd)
	if err != nil {
		return nil, err
	}
	if profile.GraphEndpoint != agent365.Endpoint || profile.GraphScope != agent365.Scope {
		return nil, errs.Security(
			"Agent 365 Microsoft Graph profile does not match the trusted public endpoint and scope",
		)
	}
	credential, err := newCredential(cmd, profile)
	if err != nil {
		return nil, err
	}
	return agent365.NewClient(credential, newHTTPClient(cmd))
}

func resolveAgent365Profile(cmd *cobra.Command) (azcloud.Profile, error) {
	profile, err := azcloud.Resolve(selectedCloudName(cmd, ""))
	if err != nil {
		return azcloud.Profile{}, err
	}
	if !profile.Capabilities.Agent365 {
		return azcloud.Profile{}, errs.Config(
			"Agent 365 blueprint inspection is unavailable in %s; no commercial-cloud fallback is allowed",
			profile.Name,
		)
	}
	return profile, nil
}

func resolveAgent365BindingTarget(
	cmd *cobra.Command,
) (agent365BindingTarget, func(), error) {
	manifestPath := strings.TrimSpace(getFlag(cmd, "manifest"))
	workspacePath := strings.TrimSpace(getFlag(cmd, "workspace"))
	if manifestPath == "" && workspacePath == "" {
		return agent365BindingTarget{}, nil, errs.Config(
			"exactly one target is required: -f/--manifest for a Prompt Agent or --workspace for a Hosted Agent",
		)
	}
	if manifestPath != "" && workspacePath != "" {
		return agent365BindingTarget{}, nil, errs.Config(
			"-f/--manifest and --workspace are mutually exclusive",
		)
	}
	if manifestPath != "" {
		if strings.TrimSpace(getFlag(cmd, "environment")) != "" ||
			strings.TrimSpace(getFlag(cmd, "service")) != "" ||
			getBoolFlag(cmd, "accept-preview") {
			return agent365BindingTarget{}, nil, errs.Config(
				"--service, --environment, and --accept-preview are only valid with --workspace",
			)
		}
		resolved, err := resolveManifest(cmd)
		if err != nil {
			return agent365BindingTarget{}, nil, err
		}
		cfg := resolved.Config
		credential, err := newCredential(cmd, cfg.Cloud)
		if err != nil {
			return agent365BindingTarget{}, nil, err
		}
		httpClient := newHTTPClient(cmd)
		endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
		if err != nil {
			return agent365BindingTarget{}, nil, err
		}
		client := newFoundryClient(endpoint, cfg, credential, httpClient)
		remote, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
		if err != nil {
			return agent365BindingTarget{}, nil, err
		}
		if remote == nil {
			return agent365BindingTarget{}, nil, errs.NotFound(
				"Prompt Agent %q was not found in the selected Foundry project",
				cfg.Agent.Name,
			)
		}
		return agent365BindingTarget{
			Type:            "prompt",
			Cloud:           cfg.Cloud.Name,
			Name:            cfg.Agent.Name,
			ProjectEndpoint: endpoint,
			Agent:           remote,
		}, nil, nil
	}

	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return agent365BindingTarget{}, nil, err
	}
	return agent365BindingTarget{
		Type:            "hosted",
		Cloud:           runtime.Profile.Name,
		Name:            runtime.Agent.Name,
		ProjectEndpoint: runtime.ProjectEndpoint,
		Agent:           runtime.Agent,
	}, cancel, nil
}

func bindingStatusFromTarget(target agent365BindingTarget) agent365BindingStatusResult {
	result := agent365BindingStatusResult{
		TargetType:               target.Type,
		Cloud:                    target.Cloud,
		AgentName:                target.Name,
		ProjectEndpoint:          target.ProjectEndpoint,
		Correlation:              "not-requested",
		Identity:                 classifyAgent365Identity(target.Agent, nil),
		BindingMutationSupported: false,
	}
	if target.Agent != nil {
		result.InstanceIdentity = target.Agent.InstanceIdentity
		result.FoundryBlueprint = target.Agent.Blueprint
		result.BlueprintReference = target.Agent.BlueprintReference
	}
	return result
}

func resolveBindingIdentity(
	ctx context.Context,
	cmd *cobra.Command,
	target agent365BindingTarget,
	result *agent365BindingStatusResult,
) error {
	identity, err := resolveTargetAgentIdentity(ctx, cmd, target)
	if err != nil {
		return err
	}
	result.Identity = classifyAgent365Identity(target.Agent, identity)
	return nil
}

func resolveTargetAgentIdentity(
	ctx context.Context,
	cmd *cobra.Command,
	target agent365BindingTarget,
) (*agent365.AgentIdentity, error) {
	if target.Agent == nil || target.Agent.InstanceIdentity == nil {
		return nil, errs.NotFound(
			"%s Agent %q did not expose an instance identity to resolve",
			target.Type,
			target.Name,
		)
	}
	objectID, err := agent365.ValidateGUID(
		target.Agent.InstanceIdentity.PrincipalID,
		"Foundry instance identity principal ID",
	)
	if err != nil {
		return nil, err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return nil, err
	}
	return client.GetAgentIdentity(ctx, objectID)
}

func blueprintCorrelation(agent *foundry.Agent, blueprint agent365.Blueprint) string {
	if agent == nil {
		return "insufficient-data"
	}
	candidates := []string{blueprint.AppID, blueprint.ObjectID}
	if agent.Blueprint != nil {
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(agent.Blueprint.ClientID), candidate) {
				return "matched"
			}
		}
	}
	if agent.BlueprintReference != nil {
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(agent.BlueprintReference.BlueprintID), candidate) {
				return "matched"
			}
		}
	}
	if agent.Blueprint == nil && agent.BlueprintReference == nil {
		return "insufficient-data"
	}
	return "not-matched"
}

func addAgent365BlueprintSelectorFlags(command *cobra.Command) {
	command.Flags().String(
		"blueprint-id",
		"",
		"Agent 365 blueprint application/client ID.",
	)
	command.Flags().String(
		"blueprint-object-id",
		"",
		"Agent 365 blueprint Microsoft Entra object ID.",
	)
}

func addAgent365BindingTargetFlags(command *cobra.Command) {
	command.Flags().StringP(
		"manifest",
		"f",
		"",
		"Path to a Prompt Agent manifest.",
	)
	command.Flags().String(
		"workspace",
		"",
		"Path to a Hosted Agent azd workspace containing azure.yaml.",
	)
	command.Flags().String(
		"service",
		"",
		"Hosted azure.ai.agent service name when azure.yaml defines more than one.",
	)
	command.Flags().String(
		"environment",
		"",
		"Optional existing Hosted Agent azd environment name.",
	)
	command.Flags().Bool(
		"accept-preview",
		false,
		"Explicitly accept Hosted Agent preview limitations when --workspace is used.",
	)
}

func rawAgent365Selector(cmd *cobra.Command) agent365.BlueprintSelector {
	selector := agent365.BlueprintSelector{
		AppID:    getFlag(cmd, "blueprint-id"),
		ObjectID: getFlag(cmd, "blueprint-object-id"),
	}
	return selector
}

func requiredAgent365Selector(cmd *cobra.Command) (agent365.BlueprintSelector, error) {
	return agent365.ValidateSelector(rawAgent365Selector(cmd))
}

func optionalAgent365Selector(cmd *cobra.Command) (agent365.BlueprintSelector, error) {
	selector := rawAgent365Selector(cmd)
	if strings.TrimSpace(selector.AppID) == "" && strings.TrimSpace(selector.ObjectID) == "" {
		return agent365.BlueprintSelector{}, nil
	}
	return agent365.ValidateSelector(selector)
}
