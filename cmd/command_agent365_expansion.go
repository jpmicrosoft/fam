package main

import (
	"fmt"
	"strings"

	"foundry-agent-manager/internal/agent365"
	"foundry-agent-manager/internal/agent365hosted"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"

	"github.com/spf13/cobra"
)

type agent365GovernanceResult struct {
	Blueprint  agent365.Blueprint         `json:"blueprint" yaml:"blueprint"`
	Objects    []agent365.DirectoryObject `json:"objects" yaml:"objects"`
	Count      int                        `json:"count" yaml:"count"`
	Truncated  bool                       `json:"truncated" yaml:"truncated"`
	Permission string                     `json:"requiredPermission" yaml:"requiredPermission"`
}

type agent365ObservabilityResult struct {
	Cloud                       string                          `json:"cloud" yaml:"cloud"`
	AgentName                   string                          `json:"agentName" yaml:"agentName"`
	AgentIdentityObjectID       string                          `json:"agentIdentityObjectId,omitempty" yaml:"agentIdentityObjectId,omitempty"`
	ExpectedResourceDisplayName string                          `json:"expectedResourceDisplayName" yaml:"expectedResourceDisplayName"`
	ExpectedAppRoleID           string                          `json:"expectedAppRoleId" yaml:"expectedAppRoleId"`
	Local                       agent365hosted.ValidationResult `json:"local" yaml:"local"`
	AssignmentPresent           *bool                           `json:"assignmentPresent,omitempty" yaml:"assignmentPresent,omitempty"`
	Assignment                  *agent365.AppRoleAssignment     `json:"assignment,omitempty" yaml:"assignment,omitempty"`
	Ready                       bool                            `json:"ready" yaml:"ready"`
	Executable                  bool                            `json:"executable" yaml:"executable"`
	Steps                       []string                        `json:"steps" yaml:"steps"`
}

type agent365PublicationInfoResult struct {
	ReadOnly                  bool     `json:"readOnly" yaml:"readOnly"`
	RegistryMutationSupported bool     `json:"registryMutationSupported" yaml:"registryMutationSupported"`
	ExistingBindingSupported  bool     `json:"existingBindingSupported" yaml:"existingBindingSupported"`
	HostedExecutionBoundary   string   `json:"hostedExecutionBoundary" yaml:"hostedExecutionBoundary"`
	PromptExecutionBoundary   string   `json:"promptExecutionBoundary" yaml:"promptExecutionBoundary"`
	IdentityLifecycle         []string `json:"identityLifecycle" yaml:"identityLifecycle"`
	Documentation             []string `json:"documentation" yaml:"documentation"`
}

type agent365IdentityEvidence struct {
	Classification     string                           `json:"classification" yaml:"classification"`
	Authoritative      bool                             `json:"authoritative" yaml:"authoritative"`
	InstanceIdentity   *foundry.AgentIdentity           `json:"instanceIdentity,omitempty" yaml:"instanceIdentity,omitempty"`
	FoundryBlueprint   *foundry.AgentIdentity           `json:"foundryBlueprint,omitempty" yaml:"foundryBlueprint,omitempty"`
	BlueprintReference *foundry.AgentBlueprintReference `json:"blueprintReference,omitempty" yaml:"blueprintReference,omitempty"`
	DirectoryIdentity  *agent365.AgentIdentity          `json:"directoryIdentity,omitempty" yaml:"directoryIdentity,omitempty"`
	Correlation        string                           `json:"correlation" yaml:"correlation"`
	RBACGuidance       []string                         `json:"rbacGuidance" yaml:"rbacGuidance"`
}

type agent365PublicationResult struct {
	TargetType            string                   `json:"targetType" yaml:"targetType"`
	Cloud                 string                   `json:"cloud" yaml:"cloud"`
	AgentName             string                   `json:"agentName" yaml:"agentName"`
	ProjectEndpoint       string                   `json:"projectEndpoint" yaml:"projectEndpoint"`
	Identity              agent365IdentityEvidence `json:"identity" yaml:"identity"`
	RegistryStatus        string                   `json:"registryStatus" yaml:"registryStatus"`
	PublicationStatus     string                   `json:"publicationStatus" yaml:"publicationStatus"`
	Executable            bool                     `json:"executable" yaml:"executable"`
	ExecutionBoundary     string                   `json:"executionBoundary" yaml:"executionBoundary"`
	Steps                 []string                 `json:"steps" yaml:"steps"`
	AdminHandoff          []string                 `json:"adminHandoff" yaml:"adminHandoff"`
	DocumentationConflict string                   `json:"documentationConflict,omitempty" yaml:"documentationConflict,omitempty"`
}

func newAgent365ExpansionCommands() []*cobra.Command {
	identityList := &cobra.Command{
		Use:          "agent365-identity-list",
		Short:        "List Microsoft Entra Agent ID identities.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365IdentityList,
		SilenceUsage: true,
	}
	addAgent365ListFlags(identityList)

	identityShow := &cobra.Command{
		Use:          "agent365-identity-show",
		Short:        "Show non-secret metadata for one Microsoft Entra Agent ID identity.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365IdentityShow,
		SilenceUsage: true,
	}
	identityShow.Flags().String("identity-object-id", "", "Agent identity Microsoft Entra object ID.")
	requireFlags(identityShow, "identity-object-id")

	principalList := &cobra.Command{
		Use:          "agent365-blueprint-principal-list",
		Short:        "List Agent ID blueprint principals in the selected tenant.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintPrincipalList,
		SilenceUsage: true,
	}
	addAgent365ListFlags(principalList)

	principalShow := &cobra.Command{
		Use:          "agent365-blueprint-principal-show",
		Short:        "Show non-secret metadata for one Agent ID blueprint principal.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintPrincipalShow,
		SilenceUsage: true,
	}
	principalShow.Flags().String(
		"principal-object-id",
		"",
		"Blueprint principal Microsoft Entra object ID.",
	)
	requireFlags(principalShow, "principal-object-id")

	owners := &cobra.Command{
		Use:          "agent365-blueprint-owners",
		Short:        "List owners of one Agent ID blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintOwners,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(owners)
	addAgent365AllFlag(owners)

	sponsors := &cobra.Command{
		Use:          "agent365-blueprint-sponsors",
		Short:        "List sponsors of one Agent ID blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintSponsors,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(sponsors)
	addAgent365AllFlag(sponsors)

	identities := &cobra.Command{
		Use:          "agent365-blueprint-identities",
		Short:        "List Agent ID identities associated with one blueprint.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365BlueprintIdentities,
		SilenceUsage: true,
	}
	addAgent365BlueprintSelectorFlags(identities)
	addAgent365ListFlags(identities)

	observabilityPlan := &cobra.Command{
		Use:          "agent365-observability-plan",
		Short:        "Inspect local Hosted Agent observability evidence and print the Agent 365 enablement plan.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365ObservabilityPlan,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(observabilityPlan)

	observabilityStatus := &cobra.Command{
		Use:          "agent365-observability-status",
		Short:        "Validate Hosted source and the Agent 365 observability app-role assignment.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365ObservabilityStatus,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(observabilityStatus)
	addHostedPreviewFlag(observabilityStatus)
	observabilityStatus.Flags().String(
		"identity-object-id",
		"",
		"Optional deployed Agent ID identity object ID; otherwise resolve the Hosted runtime identity.",
	)
	observabilityStatus.Flags().Bool(
		"fail-on-not-ready",
		false,
		"Exit with code 11 after printing the result when observability is not ready.",
	)

	publicationInfo := &cobra.Command{
		Use:          "agent365-publication-info",
		Short:        "Explain Agent 365 publication, registry, and identity boundaries.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365PublicationInfo,
		SilenceUsage: true,
	}

	publicationPlan := &cobra.Command{
		Use:          "agent365-publication-plan",
		Short:        "Plan Agent 365 publication and post-publication identity migration.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365PublicationPlan,
		SilenceUsage: true,
	}
	addAgent365PublicationFlags(publicationPlan)

	publicationStatus := &cobra.Command{
		Use:          "agent365-publication-status",
		Short:        "Show publication and identity evidence without claiming unsupported registry state.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365PublicationStatus,
		SilenceUsage: true,
	}
	addAgent365PublicationFlags(publicationStatus)

	adminHandoff := &cobra.Command{
		Use:          "agent365-publication-admin-handoff",
		Short:        "Generate the Agent 365 tenant-admin and RBAC handoff for a Foundry Agent.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365PublicationAdminHandoff,
		SilenceUsage: true,
	}
	addAgent365PublicationFlags(adminHandoff)

	commands := []*cobra.Command{
		identityList,
		identityShow,
		principalList,
		principalShow,
		owners,
		sponsors,
		identities,
		observabilityPlan,
		observabilityStatus,
		publicationInfo,
		publicationPlan,
		publicationStatus,
		adminHandoff,
	}
	return append(commands, newAgent365IntegrationCommands()...)
}

func cmdAgent365IdentityList(cmd *cobra.Command, _ []string) error {
	pageSize, opts, err := agent365ListOptions(cmd)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	result, err := client.ListAgentIdentities(commandContext(cmd), pageSize, opts)
	if err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 identities: count=%d truncated=%t",
		result.Count,
		result.Truncated,
	))
}

func cmdAgent365IdentityShow(cmd *cobra.Command, _ []string) error {
	objectID, err := agent365.ValidateGUID(
		getFlag(cmd, "identity-object-id"),
		"agent identity object ID",
	)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	identity, err := client.GetAgentIdentity(commandContext(cmd), objectID)
	if err != nil {
		return err
	}
	return printResult(cmd, identity, fmt.Sprintf(
		"Agent 365 identity: name=%s object-id=%s blueprint-id=%s enabled=%s",
		identity.DisplayName,
		identity.ID,
		identity.AgentIdentityBlueprintID,
		optionalBool(identity.AccountEnabled),
	))
}

func cmdAgent365BlueprintPrincipalList(cmd *cobra.Command, _ []string) error {
	pageSize, opts, err := agent365ListOptions(cmd)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	result, err := client.ListBlueprintPrincipals(commandContext(cmd), pageSize, opts)
	if err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 blueprint principals: count=%d truncated=%t",
		result.Count,
		result.Truncated,
	))
}

func cmdAgent365BlueprintPrincipalShow(cmd *cobra.Command, _ []string) error {
	objectID, err := agent365.ValidateGUID(
		getFlag(cmd, "principal-object-id"),
		"blueprint principal object ID",
	)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	principal, err := client.GetBlueprintPrincipal(commandContext(cmd), objectID)
	if err != nil {
		return err
	}
	return printResult(cmd, principal, fmt.Sprintf(
		"Agent 365 blueprint principal: name=%s object-id=%s blueprint-app-id=%s",
		principal.DisplayName,
		principal.ID,
		principal.AppID,
	))
}

func cmdAgent365BlueprintOwners(cmd *cobra.Command, _ []string) error {
	return cmdAgent365BlueprintGovernance(cmd, "owners")
}

func cmdAgent365BlueprintSponsors(cmd *cobra.Command, _ []string) error {
	return cmdAgent365BlueprintGovernance(cmd, "sponsors")
}

func cmdAgent365BlueprintGovernance(cmd *cobra.Command, relationship string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	opts := agent365.PaginationOptions{All: getBoolFlag(cmd, "all")}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	blueprint, err := client.GetBlueprint(commandContext(cmd), selector)
	if err != nil {
		return err
	}
	var (
		objects    []agent365.DirectoryObject
		truncated  bool
		permission string
	)
	switch relationship {
	case "owners":
		objects, truncated, err = client.ListBlueprintOwnersPaginated(
			commandContext(cmd),
			blueprint.ObjectID,
			opts,
		)
		permission = "AgentIdentityBlueprint.Read.All"
	case "sponsors":
		objects, truncated, err = client.ListBlueprintSponsorsPaginated(
			commandContext(cmd),
			blueprint.ObjectID,
			opts,
		)
		permission = "Application.Read.All"
	default:
		return errs.Config("unsupported Agent 365 governance relationship %q", relationship)
	}
	if err != nil {
		return err
	}
	result := agent365GovernanceResult{
		Blueprint:  *blueprint,
		Objects:    objects,
		Count:      len(objects),
		Truncated:  truncated,
		Permission: permission,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 blueprint %s: blueprint=%s count=%d truncated=%t permission=%s",
		relationship,
		blueprint.DisplayName,
		len(objects),
		truncated,
		permission,
	))
}

func cmdAgent365BlueprintIdentities(cmd *cobra.Command, _ []string) error {
	selector, err := requiredAgent365Selector(cmd)
	if err != nil {
		return err
	}
	pageSize, opts, err := agent365ListOptions(cmd)
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
	result, err := client.ListAgentIdentitiesByBlueprint(
		commandContext(cmd),
		blueprint.AppID,
		pageSize,
		opts,
	)
	if err != nil {
		return err
	}
	return printResult(cmd, struct {
		Blueprint  agent365.Blueprint         `json:"blueprint" yaml:"blueprint"`
		Identities agent365.AgentIdentityList `json:"identityInventory" yaml:"identityInventory"`
	}{
		Blueprint:  *blueprint,
		Identities: result,
	}, fmt.Sprintf(
		"Agent 365 blueprint identities: blueprint=%s count=%d truncated=%t",
		blueprint.DisplayName,
		result.Count,
		result.Truncated,
	))
}

func cmdAgent365ObservabilityPlan(cmd *cobra.Command, _ []string) error {
	profile, workspace, err := resolveHostedWorkspace(cmd, false)
	if err != nil {
		return err
	}
	local, err := agent365hosted.ValidateSource(workspace.Selected.SourceDirectory)
	if err != nil {
		return err
	}
	result := agent365ObservabilityResult{
		Cloud:                       profile.Name,
		AgentName:                   workspace.Selected.AgentName,
		ExpectedResourceDisplayName: agent365.ObservabilityDisplayName,
		ExpectedAppRoleID:           agent365.ObservabilityAppRoleID,
		Local:                       local,
		Ready:                       false,
		Executable:                  false,
		Steps:                       observabilitySteps(),
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 observability plan: agent=%s local-ready=%t assignment=not-checked executable=false",
		result.AgentName,
		local.Ready,
	))
}

func cmdAgent365ObservabilityStatus(cmd *cobra.Command, _ []string) error {
	var (
		profileName      string
		agentName        string
		sourceDirectory  string
		identityObjectID string
	)
	override := strings.TrimSpace(getFlag(cmd, "identity-object-id"))
	if override != "" {
		normalized, err := agent365.ValidateGUID(override, "agent identity object ID")
		if err != nil {
			return err
		}
		profile, workspace, err := resolveHostedWorkspace(cmd, true)
		if err != nil {
			return err
		}
		profileName = profile.Name
		agentName = workspace.Selected.AgentName
		sourceDirectory = workspace.Selected.SourceDirectory
		identityObjectID = normalized
	} else {
		runtime, cancel, err := resolveHostedRESTRuntime(cmd)
		if err != nil {
			return err
		}
		defer cancel()
		profileName = runtime.Profile.Name
		agentName = runtime.Agent.Name
		sourceDirectory = runtime.Workspace.Selected.SourceDirectory
		if runtime.Agent.InstanceIdentity == nil {
			return errs.NotFound(
				"Hosted Agent %q did not expose an instance identity for observability validation",
				runtime.Agent.Name,
			)
		}
		identityObjectID, err = agent365.ValidateGUID(
			runtime.Agent.InstanceIdentity.PrincipalID,
			"Hosted Agent instance identity principal ID",
		)
		if err != nil {
			return err
		}
	}
	local, err := agent365hosted.ValidateSource(sourceDirectory)
	if err != nil {
		return err
	}
	client, err := newAgent365Client(cmd)
	if err != nil {
		return err
	}
	resource, err := client.ResolveObservabilityServicePrincipal(commandContext(cmd))
	if err != nil {
		return err
	}
	present, assignment, err := client.HasObservabilityAssignment(
		commandContext(cmd),
		identityObjectID,
		resource.ID,
	)
	if err != nil {
		return err
	}
	result := agent365ObservabilityResult{
		Cloud:                       profileName,
		AgentName:                   agentName,
		AgentIdentityObjectID:       identityObjectID,
		ExpectedResourceDisplayName: agent365.ObservabilityDisplayName,
		ExpectedAppRoleID:           agent365.ObservabilityAppRoleID,
		Local:                       local,
		AssignmentPresent:           &present,
		Assignment:                  assignment,
		Ready:                       local.Ready && present,
		Executable:                  false,
		Steps:                       observabilitySteps(),
	}
	if err := printResult(cmd, result, fmt.Sprintf(
		"Agent 365 observability status: agent=%s local-ready=%t role-assigned=%t ready=%t",
		result.AgentName,
		local.Ready,
		present,
		result.Ready,
	)); err != nil {
		return err
	}
	if getBoolFlag(cmd, "fail-on-not-ready") && !result.Ready {
		return errs.ReportedExit(11)
	}
	return nil
}

func cmdAgent365PublicationInfo(cmd *cobra.Command, _ []string) error {
	result := agent365PublicationInfoResult{
		ReadOnly:                  true,
		RegistryMutationSupported: false,
		ExistingBindingSupported:  false,
		HostedExecutionBoundary:   "Only the separately pinned and reviewed Hosted Autopilot sample workflow is executable; it does not publish an arbitrary existing Hosted Agent.",
		PromptExecutionBoundary:   "Prompt Agents support Agent 365 registry synchronization after standard Microsoft 365 publication; Prompt Autopilot publishing is not supported.",
		IdentityLifecycle: []string{
			"New-model agents receive a unique blueprint and instance identity when they are created.",
			"Standard Microsoft 365 publication and Agent 365 registry synchronization do not replace a new-model agent's instance identity or invalidate its existing Azure RBAC assignments.",
			"Use instance_identity.client_id as the Azure Bot Service msaAppId; use instance_identity.principal_id for Azure RBAC and directory-object correlation.",
			"Legacy agents can use the shared project identity, while legacy Agent Applications have separate identities; migrating either to a new-model agent requires reassessing and reassigning downstream RBAC to the new identity.",
		},
		Documentation: []string{
			"https://learn.microsoft.com/azure/foundry/agents/concepts/agent-identity",
			"https://learn.microsoft.com/azure/foundry/agents/concepts/agent-365-integration",
			"https://learn.microsoft.com/azure/foundry/agents/how-to/migrate",
			"https://learn.microsoft.com/microsoft-agent-365/developer/choose-integration-option",
		},
	}
	return printResult(
		cmd,
		result,
		"Agent 365 publication support is evidence, planning, and admin handoff only; no generic registry mutation is exposed",
	)
}

func cmdAgent365PublicationPlan(cmd *cobra.Command, _ []string) error {
	result, cancel, err := resolveAgent365PublicationResult(cmd)
	if err != nil {
		return err
	}
	if cancel != nil {
		defer cancel()
	}
	result.PublicationStatus = "planned-not-executed"
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 publication plan: target=%s agent=%s executable=%t identity=%s",
		result.TargetType,
		result.AgentName,
		result.Executable,
		result.Identity.Classification,
	))
}

func cmdAgent365PublicationStatus(cmd *cobra.Command, _ []string) error {
	result, cancel, err := resolveAgent365PublicationResult(cmd)
	if err != nil {
		return err
	}
	if cancel != nil {
		defer cancel()
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 publication status: target=%s agent=%s publication=%s registry=%s identity=%s",
		result.TargetType,
		result.AgentName,
		result.PublicationStatus,
		result.RegistryStatus,
		result.Identity.Classification,
	))
}

func cmdAgent365PublicationAdminHandoff(cmd *cobra.Command, _ []string) error {
	result, cancel, err := resolveAgent365PublicationResult(cmd)
	if err != nil {
		return err
	}
	if cancel != nil {
		defer cancel()
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 admin handoff: target=%s agent=%s tasks=%d",
		result.TargetType,
		result.AgentName,
		len(result.AdminHandoff),
	))
}

func resolveAgent365PublicationResult(
	cmd *cobra.Command,
) (agent365PublicationResult, func(), error) {
	target, cancel, err := resolveAgent365BindingTarget(cmd)
	if err != nil {
		return agent365PublicationResult{}, nil, err
	}
	var directoryIdentity *agent365.AgentIdentity
	if getBoolFlag(cmd, "resolve-identity") {
		directoryIdentity, err = resolveTargetAgentIdentity(
			commandContext(cmd),
			cmd,
			target,
		)
		if err != nil {
			if cancel != nil {
				cancel()
			}
			return agent365PublicationResult{}, nil, err
		}
	}
	evidence := classifyAgent365Identity(target.Agent, directoryIdentity)
	modernIdentity := target.Agent != nil && target.Agent.InstanceIdentity != nil
	result := agent365PublicationResult{
		TargetType:        target.Type,
		Cloud:             target.Cloud,
		AgentName:         target.Name,
		ProjectEndpoint:   target.ProjectEndpoint,
		Identity:          evidence,
		RegistryStatus:    "unverified-no-documented-manager-registry-status-api",
		PublicationStatus: "unverified",
		Executable:        false,
		AdminHandoff: []string{
			"Confirm the tenant has the required Microsoft Agent 365 license and administrator consent.",
			"Confirm the Foundry account Agent 365 status and account-wide activity-data setting.",
			"Review blueprint owners, sponsors, requested permissions, inheritable permissions, and Conditional Access policy.",
			"Approve the Agent365.Observability.OtelWrite app role only for identities that send Agent 365 telemetry.",
		},
	}
	if modernIdentity {
		result.Steps = []string{
			"Record the agent's existing blueprint, instance_identity.client_id, and instance_identity.principal_id before publication.",
			"Use only a documented publication flow; standard Microsoft 365 publication does not replace the existing new-model agent identity.",
			"Keep downstream Azure RBAC assignments on instance_identity.principal_id and use instance_identity.client_id only where an application/client ID is required.",
			"Verify registry appearance and governance state in Microsoft Agent 365 admin experiences.",
		}
		result.AdminHandoff = append(result.AdminHandoff,
			"Record the existing new-model agent identity as the continuing post-publication principal.",
			"Do not recreate Azure RBAC solely because standard Microsoft 365 publication or registry synchronization occurred.",
		)
	} else {
		result.Steps = []string{
			"Inventory permissions granted to the legacy shared project identity or separate legacy Agent Application identity.",
			"Use the documented migration flow to create or identify the target new-model agent; do not write blueprint IDs into local metadata as a substitute.",
			"Retrieve the new agent's instance identity and reassign only the required Azure RBAC roles to its principal_id.",
			"Validate downstream token audiences, registry appearance, and governance state after migration.",
		}
		result.AdminHandoff = append(result.AdminHandoff,
			"Record the legacy identity and new-model agent identity as separate principals.",
			"Reassign required Azure RBAC roles during migration because legacy identity assignments do not transfer to the new agent identity.",
		)
	}
	if target.Type == "hosted" {
		result.ExecutionBoundary = "The pinned `autopilot deploy` sample is the only executable Hosted Autopilot path and creates its own reviewed sample deployment."
	} else {
		result.ExecutionBoundary = "Prompt Agents support standard Microsoft 365 publication and Agent 365 registry synchronization; Prompt Autopilot publishing is unsupported."
	}
	return result, cancel, nil
}

func classifyAgent365Identity(
	agent *foundry.Agent,
	directory *agent365.AgentIdentity,
) agent365IdentityEvidence {
	result := agent365IdentityEvidence{
		Classification:    "identity-unavailable",
		Authoritative:     false,
		Correlation:       "not-requested",
		DirectoryIdentity: directory,
	}
	if agent == nil {
		result.RBACGuidance = []string{
			"Resolve the Foundry agent before making identity or RBAC decisions.",
		}
		return result
	}
	result.InstanceIdentity = agent.InstanceIdentity
	result.FoundryBlueprint = agent.Blueprint
	result.BlueprintReference = agent.BlueprintReference
	if agent.InstanceIdentity == nil {
		result.Classification = "legacy-shared-project-identity"
		result.Authoritative = true
		result.RBACGuidance = []string{
			"The missing instance_identity identifies a legacy agent that uses the shared project identity.",
			"Migrating to a new-model agent creates a unique identity; enumerate existing role assignments and recreate only the required roles on the new principal_id.",
			"Legacy Agent Applications are separate resources with separate identities and require the same explicit RBAC reassessment when replaced.",
			"Validate the downstream token audience after every identity migration.",
		}
		if directory != nil {
			result.Correlation = "insufficient-data"
		}
		return result
	}
	result.Classification = "modern-unique-agent-identity"
	result.Authoritative = true
	result.RBACGuidance = []string{
		"The agent already has a unique new-model instance identity.",
		"Standard Microsoft 365 publication and Agent 365 registry synchronization retain this identity; do not recreate RBAC solely because publication occurred.",
		"Use instance_identity.principal_id for Azure RBAC and directory-object correlation.",
		"Use instance_identity.client_id where an application/client ID is required, including Azure Bot Service msaAppId.",
	}
	if directory == nil {
		return result
	}
	principalID := strings.TrimSpace(agent.InstanceIdentity.PrincipalID)
	directoryID := strings.TrimSpace(directory.ID)
	if principalID == "" || directoryID == "" {
		result.Correlation = "insufficient-data"
		return result
	}
	if !strings.EqualFold(
		principalID,
		directoryID,
	) {
		result.Correlation = "not-matched"
		return result
	}
	result.Correlation = "identity-object-matched"
	blueprintClientID := ""
	if agent.Blueprint != nil {
		blueprintClientID = strings.TrimSpace(agent.Blueprint.ClientID)
	}
	directoryBlueprintID := strings.TrimSpace(directory.AgentIdentityBlueprintID)
	if blueprintClientID != "" &&
		directoryBlueprintID != "" &&
		strings.EqualFold(
			blueprintClientID,
			directoryBlueprintID,
		) {
		result.Correlation = "identity-and-blueprint-matched"
	}
	return result
}

func addAgent365ListFlags(command *cobra.Command) {
	command.Flags().Int("limit", 100, "Page size and maximum results without --all (1-100).")
	addAgent365AllFlag(command)
}

func addAgent365AllFlag(command *cobra.Command) {
	command.Flags().Bool(
		"all",
		false,
		"Follow bounded Microsoft Graph continuation links (maximum 5000 results).",
	)
}

func agent365ListOptions(
	cmd *cobra.Command,
) (int, agent365.PaginationOptions, error) {
	pageSize := getIntFlag(cmd, "limit")
	if err := agent365.ValidateListLimit(pageSize); err != nil {
		return 0, agent365.PaginationOptions{}, err
	}
	if getBoolFlag(cmd, "all") && cmd.Flags().Changed("limit") {
		return 0, agent365.PaginationOptions{}, errs.Config(
			"--all and --limit are mutually exclusive",
		)
	}
	opts := agent365.PaginationOptions{All: getBoolFlag(cmd, "all")}
	if err := agent365.ValidatePaginationOptions(opts); err != nil {
		return 0, agent365.PaginationOptions{}, err
	}
	return pageSize, opts, nil
}

func addAgent365PublicationFlags(command *cobra.Command) {
	addAgent365BindingTargetFlags(command)
	command.Flags().Bool(
		"resolve-identity",
		false,
		"Resolve the Foundry instance identity through Microsoft Graph; requires AgentIdentity.Read.All.",
	)
}

func observabilitySteps() []string {
	return []string{
		"Use Microsoft OpenTelemetry Distro and enable the Agent 365 exporter with a token resolver; legacy Agent 365 observability SDK packages remain detectable but are no longer preferred.",
		"Have a Global Administrator or Application Administrator assign Agent365.Observability.OtelWrite to the deployed Agent ID identity.",
		"Deploy a new immutable Hosted Agent version after changing source or environment configuration.",
		"Run observability status again and validate telemetry in the Agent 365 admin experience.",
	}
}

func optionalBool(value *bool) string {
	if value == nil {
		return "unknown"
	}
	return fmt.Sprintf("%t", *value)
}
