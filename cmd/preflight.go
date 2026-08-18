package main

import (
	"context"
	"fmt"
	"strings"

	"foundry-agent-manager/internal/compatibility"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/project"
	"foundry-agent-manager/internal/secret"
	"foundry-agent-manager/internal/tools"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

type preflightCheck struct {
	Name    string `json:"name" yaml:"name"`
	Status  string `json:"status" yaml:"status"`
	Details string `json:"details" yaml:"details"`
}

type preflightResult struct {
	Ready           bool             `json:"ready" yaml:"ready"`
	Cloud           string           `json:"cloud" yaml:"cloud"`
	Agent           string           `json:"agent" yaml:"agent"`
	ProjectEndpoint string           `json:"projectEndpoint,omitempty" yaml:"projectEndpoint,omitempty"`
	Checks          []preflightCheck `json:"checks" yaml:"checks"`
}

type preflightState struct {
	Result preflightResult
	// DestinationsApproved records that every credential-bearing and data-egress
	// destination passed operator approval before any Azure mutation.
	DestinationsApproved bool
	ApprovedDestinations []string
	Project              project.State
	Connection           connection.State
	Secret               secret.Value
	Endpoint             string
}

func runPreflight(cmd *cobra.Command, prepared *preparedAgent, credential azcore.TokenCredential, httpClient foundry.HTTPClient) (*preflightState, error) {
	cfg := prepared.Resolved.Config
	state := &preflightState{
		Result: preflightResult{
			Ready: true,
			Cloud: cfg.Cloud.Name,
			Agent: cfg.Agent.Name,
		},
	}
	previewTools := tools.PreviewToolTypes(cfg.Tools)
	if len(previewTools) > 0 && !getBoolFlag(cmd, "accept-preview") {
		return state, errs.Config(
			"preview prompt-agent tools require explicit acceptance; pass --accept-preview after reviewing: %s",
			strings.Join(previewTools, ", "),
		)
	}
	add := func(name, status, details string) {
		state.Result.Checks = append(state.Result.Checks, preflightCheck{Name: name, Status: status, Details: details})
	}
	add("manifest", "passed", "schema, cloud profile, endpoints, and local file containment are valid")
	add("tools", "passed", fmt.Sprintf("%d declarative tool(s) built successfully", len(prepared.WireTools)))
	if len(previewTools) > 0 {
		add(
			"preview-tools",
			"warning",
			"preview limitations explicitly accepted for "+strings.Join(previewTools, ", "),
		)
	}

	// Destination approval runs before any credential is resolved so a hostile
	// manifest cannot cause a secret to be loaded for an unapproved host.
	approvedDestinations, err := approveDestinations(cmd, prepared)
	if err != nil {
		return state, err
	}
	state.DestinationsApproved = true
	state.ApprovedDestinations = approvedDestinations
	if len(approvedDestinations) == 0 {
		add("destination-approval", "passed", "no credential-bearing or external destinations were requested")
	} else {
		add("destination-approval", "passed", fmt.Sprintf(
			"%d operator-approved destination(s): %s",
			len(approvedDestinations),
			strings.Join(approvedDestinations, "; "),
		))
	}

	if prepared.APIMEnabled && cfg.Apim.Auth == "api_key" {
		resolvedSecret, err := secret.Resolve(commandContext(cmd), secret.Options{
			Direct:            getFlag(cmd, "apim-subscription-key"),
			File:              getFlag(cmd, "apim-subscription-key-file"),
			Stdin:             getBoolFlag(cmd, "apim-subscription-key-stdin"),
			KeyVaultSecretURL: getFlag(cmd, "apim-subscription-key-key-vault"),
			EnvironmentName:   getFlag(cmd, "apim-subscription-key-env"),
			Input:             cmd.InOrStdin(),
			Credential:        credential,
			HTTPClient:        httpClient,
			KeyVaultScope:     cfg.Cloud.KeyVaultScope,
			KeyVaultSuffixes:  cfg.Cloud.KeyVaultSuffixes,
		})
		if err != nil {
			return state, err
		}
		state.Secret = resolvedSecret
		add("apim-secret", "passed", resolvedSecret.RedactedDescription())
		if getFlag(cmd, "apim-subscription-key") != "" {
			add("apim-secret-safety", "warning", "the key was supplied in a process argument; prefer environment, file, stdin, or Key Vault")
		}
	}

	if prepared.APIMEnabled {
		if _, err := connection.BuildConnectionBody(&cfg.Apim, prepared.APIMModels, state.Secret.Secret); err != nil {
			return state, err
		}
		add("apim-configuration", "passed", "connection request can be constructed without exposing its credential")
	}

	ensureProject := getBoolFlag(cmd, "ensure-project")
	if hasProjectCoordinates(cfg.Project) {
		projectState, err := project.InspectProjectContext(commandContext(cmd), &cfg.Project, credential, httpClient)
		if err != nil {
			return state, err
		}
		state.Project = projectState
		if !projectState.Exists {
			if !ensureProject {
				return state, errs.NotFound(
					"Foundry project %q does not exist; pass --ensure-project to create it",
					cfg.Project.Name,
				)
			}
			add("project", "passed", "project is missing and will be created because --ensure-project is set")
			state.Endpoint = cfg.Project.Endpoint
		} else {
			if err := config.ValidateProjectLocation(
				projectState.Location,
				cfg.Project.AllowedRegions,
			); err != nil {
				return state, err
			}
			state.Endpoint = projectState.Endpoint
			add("project", "passed", "project exists and is readable through ARM")
		}
	} else {
		if ensureProject {
			return state, errs.Config(
				"--ensure-project requires project.subscription_id, project.resource_group, project.account_name, and project.name",
			)
		}
		endpoint, err := cfg.RequireProjectEndpoint()
		if err != nil {
			return state, err
		}
		state.Endpoint = endpoint
		add("project-resource", "skipped", "project resource coordinates were not supplied; using the explicit data-plane endpoint")
	}

	if state.Endpoint != "" {
		validatedEndpoint, err := validateProjectEndpoint(cfg, state.Endpoint)
		if err != nil {
			return state, err
		}
		state.Endpoint = validatedEndpoint
		cfg.Project.Endpoint = validatedEndpoint
	}

	projectLocation := state.Project.Location
	if projectLocation == "" {
		projectLocation = cfg.Project.Location
	}
	for _, tool := range cfg.Tools {
		if fmt.Sprint(tool["type"]) != "memory_search_preview" {
			continue
		}
		check := compatibility.Check(cfg.Agent.Model, projectLocation, "memory_search_preview")
		if check.RegionStatus == compatibility.StatusUnsupported {
			return state, errs.Config(
				"Memory is not supported in project region %q; choose a documented Memory region: %s",
				projectLocation,
				compatibility.MemorySourceURL,
			)
		}
		if check.RegionStatus == compatibility.StatusSupported {
			add("memory-region", "passed", "project region is documented for Foundry Memory")
		}
	}

	managedGroundingNames := tools.ManagedVectorStoreNames(prepared.WireTools)
	if state.Project.Exists || !hasProjectCoordinates(cfg.Project) {
		client := newFoundryClient(state.Endpoint, cfg, credential, httpClient)
		if err := client.ProbeContext(commandContext(cmd)); err != nil {
			return state, err
		}
		add("foundry-data-plane", "passed", "credential can access the project agent endpoint")
		deployment, err := requireProjectModelDeployment(
			commandContext(cmd),
			client,
			cfg.Agent.Model,
			cfg.Project.Name,
		)
		if err != nil {
			return state, err
		}
		add("model-reference", "passed", projectModelDeploymentDetails(deployment))
		if err := resolvePreparedManagedGrounding(
			commandContext(cmd),
			client,
			prepared,
		); err != nil {
			return state, err
		}
		if len(managedGroundingNames) > 0 {
			add("grounding", "passed", "managed vector-store references are synchronized and resolved")
		}
	} else {
		if len(managedGroundingNames) > 0 {
			return state, errs.Config(
				"managed grounding requires an existing project; create the project, run foundry-agent-manager grounding sync, then deploy the agent",
			)
		}
		add("foundry-data-plane", "skipped", "the project must be created before its data-plane endpoint can be probed")
		deployment, err := project.InspectModelDeploymentContext(
			commandContext(cmd),
			&cfg.Project,
			cfg.Agent.Model,
			credential,
			httpClient,
		)
		if err != nil {
			return state, err
		}
		if !deployment.Exists {
			return state, modelDeploymentNotFound(cfg.Agent.Model, "parent Foundry account")
		}
		if !strings.EqualFold(deployment.ProvisioningState, "Succeeded") {
			return state, errs.Conflict(
				"model deployment %q is not ready; ARM provisioningState is %q",
				cfg.Agent.Model,
				deployment.ProvisioningState,
			)
		}
		add(
			"model-reference",
			"passed",
			accountModelDeploymentDetails(deployment),
		)
	}

	if prepared.APIMEnabled {
		if !hasProjectCoordinates(cfg.Project) {
			return state, errs.Config("APIM connection management requires complete project resource coordinates")
		}
		if state.Project.Exists {
			connectionState, err := connection.GetAPIMConnectionContext(
				commandContext(cmd),
				&cfg.Apim,
				&cfg.Project,
				prepared.ConnectionName,
				credential,
				httpClient,
			)
			if err != nil {
				return state, err
			}
			state.Connection = connectionState
			details := "connection does not exist and will be created"
			if connectionState.Exists {
				details = "connection exists and can be inspected"
			}
			add("apim-connection", "passed", details)
			if connectionState.Exists && !connectionState.Restorable() {
				if !strings.EqualFold(connectionState.AuthType(), "ApiKey") {
					return state, errs.Conflict(
						"existing APIM connection %q uses unsupported authType %q; choose a different apim.connection_name",
						prepared.ConnectionName,
						connectionState.AuthType(),
					)
				}
				add(
					"apim-rollback",
					"warning",
					"Azure did not return the existing API-key credential; updating it requires --allow-nonrestorable-apim-update",
				)
			}
		} else {
			add("apim-connection", "skipped", "the project must be created before the connection can be inspected")
		}
	}

	if cfg.Agent.RAIPolicyID != "" {
		policy, err := foundryid.ParseRAIPolicyID(cfg.Agent.RAIPolicyID)
		if err != nil {
			return state, errs.Config("agent.rai_policy_id is invalid: %v", err)
		}
		policyProject := &config.ProjectSpec{
			SubscriptionID: policy.SubscriptionID,
			ResourceGroup:  policy.ResourceGroup,
			AccountName:    policy.AccountName,
			ARMEndpoint:    cfg.Cloud.ARMEndpoint,
			ARMScope:       cfg.Cloud.ARMScope,
		}
		if err := project.InspectRAIPolicyContext(
			commandContext(cmd),
			policyProject,
			policy.PolicyName,
			credential,
			httpClient,
		); err != nil {
			return state, err
		}
		add("rai-policy-reference", "passed", "the configured account-level RAI policy exists")
	}
	if getBoolFlag(cmd, "allow-unconditional-shared-rollback") {
		add(
			"shared-rollback",
			"warning",
			"APIM/project rollback is unconditional because the service contract exposes no concurrency token; do not run concurrent deployments",
		)
	}
	state.Result.ProjectEndpoint = state.Endpoint
	return state, nil
}

func requireProjectModelDeployment(
	ctx context.Context,
	client *foundry.Client,
	modelName string,
	projectName string,
) (*foundry.ModelDeployment, error) {
	deployment, err := client.GetModelDeploymentContext(ctx, modelName)
	if err != nil {
		return nil, err
	}
	if deployment == nil {
		target := "selected Foundry project"
		if strings.TrimSpace(projectName) != "" {
			target = fmt.Sprintf("Foundry project %q", projectName)
		}
		return nil, modelDeploymentNotFound(modelName, target)
	}
	return deployment, nil
}

func modelDeploymentNotFound(modelName string, target string) error {
	return errs.WithNextSteps(
		errs.NotFound("model deployment %q does not exist in the %s", modelName, target),
		"Verify agent.model exactly matches a deployment name available to the selected Foundry project.",
		"Define model_deployment, run foundry-agent-manager model deployment plan -f <manifest>, create it with foundry-agent-manager model deployment create -f <manifest>, then rerun foundry-agent-manager prompt preflight -f <manifest>.",
	)
}

func projectModelDeploymentDetails(deployment *foundry.ModelDeployment) string {
	details := fmt.Sprintf("deployment %q exists and is accessible from the project", deployment.Name)
	metadata := make([]string, 0, 3)
	if deployment.ModelPublisher != "" {
		metadata = append(metadata, "publisher="+deployment.ModelPublisher)
	}
	if deployment.ModelName != "" {
		metadata = append(metadata, "model="+deployment.ModelName)
	}
	if deployment.ModelVersion != "" {
		metadata = append(metadata, "version="+deployment.ModelVersion)
	}
	if len(metadata) > 0 {
		details += " (" + strings.Join(metadata, ", ") + ")"
	}
	return details
}

func accountModelDeploymentDetails(deployment project.ModelDeploymentState) string {
	details := fmt.Sprintf(
		"account deployment %q exists with provisioningState=%s",
		deployment.Name,
		deployment.ProvisioningState,
	)
	metadata := make([]string, 0, 3)
	if deployment.ModelFormat != "" {
		metadata = append(metadata, "format="+deployment.ModelFormat)
	}
	if deployment.ModelName != "" {
		metadata = append(metadata, "model="+deployment.ModelName)
	}
	if deployment.ModelVersion != "" {
		metadata = append(metadata, "version="+deployment.ModelVersion)
	}
	if len(metadata) > 0 {
		details += " (" + strings.Join(metadata, ", ") + ")"
	}
	return details
}

func preflightText(result preflightResult) string {
	var text strings.Builder
	fmt.Fprintf(&text, "preflight ready: agent=%s cloud=%s\n", result.Agent, result.Cloud)
	if result.ProjectEndpoint != "" {
		fmt.Fprintf(&text, "  project: %s\n", result.ProjectEndpoint)
	}
	for _, check := range result.Checks {
		fmt.Fprintf(&text, "  %-20s %-7s %s\n", check.Name+":", check.Status, check.Details)
	}
	return strings.TrimRight(text.String(), "\n")
}
