package main

import (
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/legacyapp"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type legacyCommandRuntime struct {
	lifecycle   *lifecycleRuntime
	client      *legacyapp.Client
	application string
	deployment  string
}

type legacyStatusResult struct {
	Application legacyapp.ApplicationState `json:"application" yaml:"application"`
	Deployment  legacyapp.DeploymentState  `json:"deployment" yaml:"deployment"`
}

type legacyDeployResult struct {
	ApplicationChange legacyapp.Change `json:"applicationChange" yaml:"applicationChange"`
	DeploymentChange  legacyapp.Change `json:"deploymentChange" yaml:"deploymentChange"`
	RoutingChange     legacyapp.Change `json:"routingChange,omitempty" yaml:"routingChange,omitempty"`
	ApplicationID     string           `json:"applicationId" yaml:"applicationId"`
	DeploymentID      string           `json:"deploymentId" yaml:"deploymentId"`
	AgentVersion      string           `json:"agentVersion" yaml:"agentVersion"`
	Receipt           string           `json:"receipt" yaml:"receipt"`
}

type legacyDeleteResult struct {
	Application        string `json:"application" yaml:"application"`
	Deployment         string `json:"deployment" yaml:"deployment"`
	DryRun             bool   `json:"dryRun" yaml:"dryRun"`
	DeletedDeployment  bool   `json:"deletedDeployment" yaml:"deletedDeployment"`
	DeletedApplication bool   `json:"deletedApplication" yaml:"deletedApplication"`
	Receipt            string `json:"receipt" yaml:"receipt"`
}

func cmdLegacyStatus(cmd *cobra.Command, _ []string) error {
	runtime, err := newLegacyCommandRuntime(cmd)
	if err != nil {
		return err
	}
	status, err := runtime.client.Status(commandContext(cmd))
	if err != nil {
		return err
	}
	result := legacyStatusResult{
		Application: status.Application,
		Deployment:  status.Deployment,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"legacy compatibility status: application=%s exists=%t deployment=%s exists=%t",
		runtime.application,
		status.Application.Exists,
		runtime.deployment,
		status.Deployment.Exists,
	))
}

func cmdLegacyDeploy(cmd *cobra.Command, _ []string) error {
	runtime, err := newLegacyCommandRuntime(cmd)
	if err != nil {
		return err
	}
	version := getFlag(cmd, "agent-version")
	agent, err := runtime.lifecycle.client.GetAgentContext(
		commandContext(cmd),
		runtime.lifecycle.cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	if agent == nil {
		return errs.NotFound("agent %q was not found", runtime.lifecycle.cfg.Agent.Name)
	}
	found, err := runtime.lifecycle.client.GetAgentVersionContext(
		commandContext(cmd),
		runtime.lifecycle.cfg.Agent.Name,
		version,
	)
	if err != nil {
		return err
	}
	if found == nil {
		return errs.NotFound(
			"agent %q version %s was not found",
			runtime.lifecycle.cfg.Agent.Name,
			version,
		)
	}
	if getBoolFlag(cmd, "route") {
		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Ensure legacy resources and route all application %q traffic to deployment %q for agent version %s?",
			runtime.application,
			runtime.deployment,
			version,
		)); err != nil {
			return err
		}
	}
	path, err := releaseReceiptPath(cmd, runtime.lifecycle, "legacy-deploy")
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		"legacy-deploy",
		runtime.lifecycle.cfg.Cloud.Name,
		receipt.ManifestReference{
			Path: runtime.lifecycle.resolved.ManifestPath,
			Hash: runtime.lifecycle.resolved.ManifestHash,
		},
		receipt.ResourceReference{
			Name:     runtime.lifecycle.cfg.Project.Name,
			Endpoint: runtime.lifecycle.endpoint,
		},
		runtime.lifecycle.cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	store.Receipt.Agent.ID = agent.ID
	store.Receipt.Agent.CreatedVersion = version

	applicationResult, err := runtime.client.EnsureApplication(
		commandContext(cmd),
		legacyapp.ApplicationMetadata{
			DisplayName: valueOrDefault(getFlag(cmd, "legacy-display-name"), runtime.application),
			Description: valueOrDefault(
				getFlag(cmd, "legacy-description"),
				runtime.lifecycle.cfg.Agent.Description,
			),
		},
		legacyapp.AgentReference{
			AgentID:   agent.ID,
			AgentName: runtime.lifecycle.cfg.Agent.Name,
		},
	)
	if err != nil {
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "Microsoft.CognitiveServices/applications",
		Name:         runtime.application,
		ID:           applicationResult.State.ID,
		Action:       string(applicationResult.Change),
		Status:       "succeeded",
		CreatedByRun: applicationResult.Change == legacyapp.ChangeCreated,
	}); err != nil {
		return err
	}
	deploymentResult, err := runtime.client.EnsureManagedDeployment(
		commandContext(cmd),
		legacyapp.ManagedDeploymentSpec{
			AgentID:      agent.ID,
			AgentName:    runtime.lifecycle.cfg.Agent.Name,
			AgentVersion: version,
			DisplayName:  runtime.deployment,
			Description:  runtime.lifecycle.cfg.Agent.Description,
		},
	)
	if err != nil {
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "Microsoft.CognitiveServices/applications/agentDeployments",
		Name:         runtime.deployment,
		ID:           deploymentResult.State.ID,
		Action:       string(deploymentResult.Change),
		Status:       "succeeded",
		CreatedByRun: deploymentResult.Change == legacyapp.ChangeCreated,
	}); err != nil {
		return err
	}
	routingChange := legacyapp.ChangeNone
	if getBoolFlag(cmd, "route") {
		routing, err := runtime.client.RouteAllTraffic(commandContext(cmd))
		if err != nil {
			_ = store.Complete(operationFailureStatus(err), err)
			return releaseFailure(store.Path, err)
		}
		routingChange = routing.Change
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "legacy-traffic-routing",
			Name:   runtime.application,
			ID:     routing.DeploymentID,
			Action: string(routing.Change),
			Status: "succeeded",
		}); err != nil {
			return err
		}
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result := legacyDeployResult{
		ApplicationChange: applicationResult.Change,
		DeploymentChange:  deploymentResult.Change,
		RoutingChange:     routingChange,
		ApplicationID:     applicationResult.State.ID,
		DeploymentID:      deploymentResult.State.ID,
		AgentVersion:      version,
		Receipt:           store.Path,
	}
	text := fmt.Sprintf(
		"legacy compatibility deployment ensured: application=%s (%s) deployment=%s (%s) version=%s",
		runtime.application,
		result.ApplicationChange,
		runtime.deployment,
		result.DeploymentChange,
		version,
	)
	if getBoolFlag(cmd, "route") {
		text += fmt.Sprintf("\n  routing: %s", routingChange)
	} else {
		text += "\n  routing: unchanged; use --route --yes to activate this legacy deployment"
	}
	text += "\n  receipt: " + result.Receipt
	return printResult(cmd, result, text)
}

func cmdLegacyDelete(cmd *cobra.Command, _ []string) error {
	runtime, err := newLegacyCommandRuntime(cmd)
	if err != nil {
		return err
	}
	removeApplication := getBoolFlag(cmd, "application")
	dryRun := getBoolFlag(cmd, "dry-run")
	path, err := releaseReceiptPath(cmd, runtime.lifecycle, "legacy-delete")
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		"legacy-delete",
		runtime.lifecycle.cfg.Cloud.Name,
		receipt.ManifestReference{
			Path: runtime.lifecycle.resolved.ManifestPath,
			Hash: runtime.lifecycle.resolved.ManifestHash,
		},
		receipt.ResourceReference{
			Name:     runtime.lifecycle.cfg.Project.Name,
			Endpoint: runtime.lifecycle.endpoint,
		},
		runtime.lifecycle.cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	result := legacyDeleteResult{
		Application: runtime.application,
		Deployment:  runtime.deployment,
		DryRun:      dryRun,
		Receipt:     store.Path,
	}
	if dryRun {
		if err := store.Complete("planned", nil); err != nil {
			return err
		}
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete legacy deployment %s%s\n  receipt: %s",
			runtime.deployment,
			conditionalText(removeApplication, " and application "+runtime.application),
			result.Receipt,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete legacy deployment %q%s?",
		runtime.deployment,
		conditionalText(removeApplication, " and application "+runtime.application),
	)); err != nil {
		_ = store.Complete("cancelled", err)
		return err
	}
	deleted, err := runtime.client.Delete(commandContext(cmd), removeApplication)
	if err != nil {
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	result.DeletedDeployment = deleted.DeletedDeployment
	result.DeletedApplication = deleted.DeletedApplication
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"legacy compatibility resources deleted: deployment=%t application=%t\n  receipt: %s",
		result.DeletedDeployment,
		result.DeletedApplication,
		result.Receipt,
	))
}

func newLegacyCommandRuntime(cmd *cobra.Command) (*legacyCommandRuntime, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	cfg := resolved.Config
	if !cfg.Cloud.Capabilities.LegacyApplications {
		return nil, errs.Config(
			"legacy Agent Applications are unavailable in %s; no cross-cloud fallback is allowed",
			cfg.Cloud.Name,
		)
	}
	lifecycle, err := lifecycleClientFromResolved(cmd, resolved)
	if err != nil {
		return nil, err
	}
	if !hasProjectCoordinates(cfg.Project) {
		return nil, errs.Config(
			"legacy Agent Application operations require subscription_id, resource_group, account_name, and project.name",
		)
	}
	application := getFlag(cmd, "application-name")
	deployment := getFlag(cmd, "deployment-name")
	client, err := legacyapp.NewClient(legacyapp.Options{
		SubscriptionID:  cfg.Project.SubscriptionID,
		ResourceGroup:   cfg.Project.ResourceGroup,
		AccountName:     cfg.Project.AccountName,
		ProjectName:     cfg.Project.Name,
		ApplicationName: application,
		DeploymentName:  deployment,
		ARMEndpoint:     cfg.Cloud.ARMEndpoint,
		ARMScope:        cfg.Cloud.ARMScope,
		Credential:      lifecycle.credential,
		HTTPClient:      lifecycle.httpClient,
	})
	if err != nil {
		return nil, err
	}
	return &legacyCommandRuntime{
		lifecycle:   lifecycle,
		client:      client,
		application: application,
		deployment:  deployment,
	}, nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func conditionalText(condition bool, value string) string {
	if condition {
		return value
	}
	return ""
}
