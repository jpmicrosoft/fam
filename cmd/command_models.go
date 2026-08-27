package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/project"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

type modelDeploymentListResult struct {
	Cloud         string                         `json:"cloud" yaml:"cloud"`
	Subscription  string                         `json:"subscriptionId" yaml:"subscriptionId"`
	ResourceGroup string                         `json:"resourceGroup" yaml:"resourceGroup"`
	AccountName   string                         `json:"accountName" yaml:"accountName"`
	Deployments   []project.ModelDeploymentState `json:"deployments" yaml:"deployments"`
}

type modelDeploymentShowResult struct {
	Cloud         string                       `json:"cloud" yaml:"cloud"`
	Subscription  string                       `json:"subscriptionId" yaml:"subscriptionId"`
	ResourceGroup string                       `json:"resourceGroup" yaml:"resourceGroup"`
	AccountName   string                       `json:"accountName" yaml:"accountName"`
	Deployment    project.ModelDeploymentState `json:"deployment" yaml:"deployment"`
}

type modelDeploymentPlanResult struct {
	Cloud         string                      `json:"cloud" yaml:"cloud"`
	Subscription  string                      `json:"subscriptionId" yaml:"subscriptionId"`
	ResourceGroup string                      `json:"resourceGroup" yaml:"resourceGroup"`
	AccountName   string                      `json:"accountName" yaml:"accountName"`
	Plan          project.ModelDeploymentPlan `json:"plan" yaml:"plan"`
}

type modelDeploymentCreateResult struct {
	Cloud         string                       `json:"cloud" yaml:"cloud"`
	Subscription  string                       `json:"subscriptionId" yaml:"subscriptionId"`
	ResourceGroup string                       `json:"resourceGroup" yaml:"resourceGroup"`
	AccountName   string                       `json:"accountName" yaml:"accountName"`
	Created       bool                         `json:"created" yaml:"created"`
	Deployment    project.ModelDeploymentState `json:"deployment" yaml:"deployment"`
	Receipt       string                       `json:"receipt" yaml:"receipt"`
}

type modelDeploymentDeleteResult struct {
	Cloud         string                        `json:"cloud" yaml:"cloud"`
	Subscription  string                        `json:"subscriptionId" yaml:"subscriptionId"`
	ResourceGroup string                        `json:"resourceGroup" yaml:"resourceGroup"`
	AccountName   string                        `json:"accountName" yaml:"accountName"`
	Name          string                        `json:"name" yaml:"name"`
	Deleted       bool                          `json:"deleted" yaml:"deleted"`
	DryRun        bool                          `json:"dryRun" yaml:"dryRun"`
	Existing      *project.ModelDeploymentState `json:"existing,omitempty" yaml:"existing,omitempty"`
	Receipt       string                        `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

func cmdModelDeploymentList(cmd *cobra.Command, _ []string) error {
	resolved, credential, httpClient, err := modelCommandRuntime(cmd)
	if err != nil {
		return err
	}
	deployments, err := project.ListModelDeploymentsContext(
		commandContext(cmd),
		&resolved.Config.Project,
		credential,
		httpClient,
	)
	if err != nil {
		return err
	}
	result := modelDeploymentListResult{
		Cloud:         resolved.Config.Cloud.Name,
		Subscription:  resolved.Config.Project.SubscriptionID,
		ResourceGroup: resolved.Config.Project.ResourceGroup,
		AccountName:   resolved.Config.Project.AccountName,
		Deployments:   deployments,
	}
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"model deployments: account=%s count=%d",
		result.AccountName,
		len(result.Deployments),
	)
	for _, deployment := range result.Deployments {
		fmt.Fprintf(
			&text,
			"\n  %s: model=%s/%s format=%s sku=%s capacity=%d state=%s",
			deployment.Name,
			deployment.ModelName,
			deployment.ModelVersion,
			deployment.ModelFormat,
			deployment.SKUName,
			deployment.Capacity,
			deployment.ProvisioningState,
		)
	}
	return printResult(cmd, result, text.String())
}

func cmdModelDeploymentShow(cmd *cobra.Command, _ []string) error {
	resolved, credential, httpClient, err := modelCommandRuntime(cmd)
	if err != nil {
		return err
	}
	name, err := modelDeploymentName(cmd, resolved.Config)
	if err != nil {
		return err
	}
	state, err := project.InspectModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		name,
		credential,
		httpClient,
	)
	if err != nil {
		return err
	}
	if !state.Exists {
		return errs.NotFound(
			"model deployment %q does not exist on Foundry account %q",
			name,
			resolved.Config.Project.AccountName,
		)
	}
	result := modelDeploymentShowResult{
		Cloud:         resolved.Config.Cloud.Name,
		Subscription:  resolved.Config.Project.SubscriptionID,
		ResourceGroup: resolved.Config.Project.ResourceGroup,
		AccountName:   resolved.Config.Project.AccountName,
		Deployment:    state,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"model deployment: %s\n  model: %s/%s\n  format: %s\n  sku: %s\n  capacity: %d\n  state: %s",
		state.Name,
		state.ModelName,
		state.ModelVersion,
		state.ModelFormat,
		state.SKUName,
		state.Capacity,
		state.ProvisioningState,
	))
}

func cmdModelDeploymentPlan(cmd *cobra.Command, _ []string) error {
	resolved, credential, httpClient, err := modelCommandRuntime(cmd)
	if err != nil {
		return err
	}
	desired, err := modelDeploymentDesired(cmd, resolved.Config)
	if err != nil {
		return err
	}
	plan, err := project.PlanModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		desired,
		credential,
		httpClient,
	)
	if err != nil {
		return errs.WithNextSteps(
			err,
			"Review the exact model version, SKU, capacity, regional capacity, and quota returned by Azure.",
			"Run fam model deployment list -f <manifest> to inspect existing deployment names before retrying.",
		)
	}
	result := modelDeploymentPlanResult{
		Cloud:         resolved.Config.Cloud.Name,
		Subscription:  resolved.Config.Project.SubscriptionID,
		ResourceGroup: resolved.Config.Project.ResourceGroup,
		AccountName:   resolved.Config.Project.AccountName,
		Plan:          plan,
	}
	return printResult(cmd, result, modelPlanText(result))
}

func cmdModelDeploymentCreate(cmd *cobra.Command, _ []string) error {
	resolved, credential, httpClient, err := modelCommandRuntime(cmd)
	if err != nil {
		return err
	}
	desired, err := modelDeploymentDesired(cmd, resolved.Config)
	if err != nil {
		return err
	}
	plan, err := project.PlanModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		desired,
		credential,
		httpClient,
	)
	if err != nil {
		return errs.WithNextSteps(
			err,
			"Correct the live model, SKU, capacity, quota, or regional-capacity failure before retrying create.",
			"Run fam model deployment plan -f <manifest> with the same flags.",
		)
	}
	store, err := newModelDeploymentOperationStore(
		cmd,
		resolved,
		"model-deployment-create",
		desired.Name,
	)
	if err != nil {
		return err
	}
	if err := store.AddStep(
		"plan",
		"succeeded",
		fmt.Sprintf("action=%s location=%s", plan.Action, plan.Location),
	); err != nil {
		return err
	}
	state, created, err := project.CreateModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		desired,
		getDurationFlag(cmd, "wait-timeout"),
		getDurationFlag(cmd, "wait-interval"),
		credential,
		httpClient,
	)
	if err != nil {
		_ = store.AddResource(receipt.ResourceChange{
			Kind:         "foundryModelDeployment",
			Name:         desired.Name,
			Action:       "create",
			Status:       operationFailureStatus(err),
			CreatedByRun: false,
			Reconciliation: reconciliationForMutation(
				err,
				"Inspect the deployment with fam model deployment show before retrying; Azure may have accepted the create request.",
			),
		})
		return completeModelDeploymentFailure(store, err)
	}
	action := "already-existed"
	if created {
		action = "created"
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:           "foundryModelDeployment",
		Name:           desired.Name,
		ID:             state.ID,
		Action:         action,
		Status:         state.ProvisioningState,
		CreatedByRun:   created,
		Reconciliation: "ARM reports the deployment in Succeeded state with the requested managed fields.",
	}); err != nil {
		return completeModelDeploymentFailure(store, err)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return errors.Join(err, fmt.Errorf("operation receipt: %s", store.Path))
	}
	result := modelDeploymentCreateResult{
		Cloud:         resolved.Config.Cloud.Name,
		Subscription:  resolved.Config.Project.SubscriptionID,
		ResourceGroup: resolved.Config.Project.ResourceGroup,
		AccountName:   resolved.Config.Project.AccountName,
		Created:       created,
		Deployment:    state,
		Receipt:       store.Path,
	}
	status := "already exists with the requested configuration"
	if created {
		status = "created"
	}
	return printResult(cmd, result, fmt.Sprintf(
		"model deployment %s: %s\n  model: %s/%s\n  sku: %s capacity=%d\n  receipt: %s",
		status,
		state.Name,
		state.ModelName,
		state.ModelVersion,
		state.SKUName,
		state.Capacity,
		store.Path,
	))
}

func cmdModelDeploymentDelete(cmd *cobra.Command, _ []string) error {
	resolved, credential, httpClient, err := modelCommandRuntime(cmd)
	if err != nil {
		return err
	}
	name, err := modelDeploymentName(cmd, resolved.Config)
	if err != nil {
		return err
	}
	existing, err := project.InspectModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		name,
		credential,
		httpClient,
	)
	if err != nil {
		return err
	}
	result := modelDeploymentDeleteResult{
		Cloud:         resolved.Config.Cloud.Name,
		Subscription:  resolved.Config.Project.SubscriptionID,
		ResourceGroup: resolved.Config.Project.ResourceGroup,
		AccountName:   resolved.Config.Project.AccountName,
		Name:          name,
		DryRun:        getBoolFlag(cmd, "dry-run"),
	}
	if existing.Exists {
		result.Existing = &existing
	}
	if result.DryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete model deployment %s from account %s (exists=%t)",
			name,
			result.AccountName,
			existing.Exists,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete model deployment %q from Foundry account %q?",
		name,
		result.AccountName,
	)); err != nil {
		return err
	}
	store, err := newModelDeploymentOperationStore(
		cmd,
		resolved,
		"model-deployment-delete",
		name,
	)
	if err != nil {
		return err
	}
	deleted, err := project.DeleteModelDeploymentContext(
		commandContext(cmd),
		&resolved.Config.Project,
		name,
		getDurationFlag(cmd, "wait-timeout"),
		getDurationFlag(cmd, "wait-interval"),
		credential,
		httpClient,
	)
	if err != nil {
		_ = store.AddResource(receipt.ResourceChange{
			Kind:         "foundryModelDeployment",
			Name:         name,
			Action:       "delete",
			Status:       operationFailureStatus(err),
			CreatedByRun: false,
			Reconciliation: reconciliationForMutation(
				err,
				"Inspect the deployment with fam model deployment show before retrying; Azure may have accepted the delete request.",
			),
		})
		return completeModelDeploymentFailure(store, err)
	}
	result.Deleted = deleted
	result.Receipt = store.Path
	status := "already-absent"
	if deleted {
		status = "deleted"
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:           "foundryModelDeployment",
		Name:           name,
		Action:         "delete",
		Status:         status,
		CreatedByRun:   false,
		Reconciliation: "ARM confirms the deployment is absent.",
	}); err != nil {
		return completeModelDeploymentFailure(store, err)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return errors.Join(err, fmt.Errorf("operation receipt: %s", store.Path))
	}
	return printResult(cmd, result, fmt.Sprintf(
		"model deployment %s: %s\n  receipt: %s",
		status,
		name,
		store.Path,
	))
}

func modelCommandRuntime(cmd *cobra.Command) (
	*resolvedManifest,
	azcore.TokenCredential,
	project.HTTPClient,
	error,
) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, nil, nil, err
	}
	if !hasModelAccountCoordinates(resolved.Config.Project) {
		return nil, nil, nil, errs.Config(
			"model deployment commands require project.subscription_id, project.resource_group, and project.account_name",
		)
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, nil, nil, err
	}
	return resolved, credential, newHTTPClient(cmd), nil
}

func modelDeploymentName(
	cmd *cobra.Command,
	cfg *config.ResolvedConfig,
) (string, error) {
	name := strings.TrimSpace(getFlag(cmd, "deployment-name"))
	if name == "" && cfg.ModelDeployment.Configured {
		name = strings.TrimSpace(cfg.ModelDeployment.DeploymentName)
	}
	if name == "" {
		name = strings.TrimSpace(cfg.Agent.Model)
	}
	if name == "" {
		return "", errs.Config(
			"model deployment name is required through --deployment-name, model_deployment.deployment_name, or agent.model",
		)
	}
	return name, nil
}

func modelDeploymentDesired(
	cmd *cobra.Command,
	cfg *config.ResolvedConfig,
) (project.ModelDeploymentDesired, error) {
	spec := cfg.ModelDeployment
	desired := project.ModelDeploymentDesired{
		Name:                    spec.DeploymentName,
		ModelName:               spec.ModelName,
		ModelVersion:            spec.ModelVersion,
		ModelFormat:             spec.ModelFormat,
		SKUName:                 spec.SKUName,
		Capacity:                spec.Capacity,
		RAIPolicyName:           spec.RAIPolicyName,
		VersionUpgradeOption:    spec.VersionUpgradeOption,
		SpilloverDeploymentName: spec.SpilloverDeploymentName,
	}
	var err error
	if desired.Name, err = modelDeploymentName(cmd, cfg); err != nil {
		return project.ModelDeploymentDesired{}, err
	}
	for flag, target := range map[string]*string{
		"model-name":                &desired.ModelName,
		"model-version":             &desired.ModelVersion,
		"model-format":              &desired.ModelFormat,
		"sku-name":                  &desired.SKUName,
		"rai-policy-name":           &desired.RAIPolicyName,
		"version-upgrade-option":    &desired.VersionUpgradeOption,
		"spillover-deployment-name": &desired.SpilloverDeploymentName,
	} {
		if value := strings.TrimSpace(getFlag(cmd, flag)); value != "" {
			*target = value
		}
	}
	if cmd.Flags().Changed("capacity") {
		desired.Capacity = getIntFlag(cmd, "capacity")
	}
	for _, required := range []struct {
		field string
		value string
	}{
		{field: "--model-name or model_deployment.model_name", value: desired.ModelName},
		{field: "--model-version or model_deployment.model_version", value: desired.ModelVersion},
		{field: "--model-format or model_deployment.model_format", value: desired.ModelFormat},
		{field: "--sku-name or model_deployment.sku_name", value: desired.SKUName},
	} {
		if required.value == "" {
			return project.ModelDeploymentDesired{}, errs.Config(
				"%s is required",
				required.field,
			)
		}
	}
	if desired.Capacity <= 0 {
		return project.ModelDeploymentDesired{}, errs.Config(
			"--capacity or model_deployment.capacity must be greater than zero",
		)
	}
	return desired, nil
}

func modelPlanText(result modelDeploymentPlanResult) string {
	plan := result.Plan
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"model deployment plan\n  account: %s\n  deployment: %s\n  action: %s\n  ready: %t",
		result.AccountName,
		plan.Desired.Name,
		plan.Action,
		plan.Ready,
	)
	if plan.Location != "" {
		fmt.Fprintf(&text, "\n  location: %s", plan.Location)
	}
	fmt.Fprintf(
		&text,
		"\n  model: %s/%s format=%s\n  sku: %s capacity=%d",
		plan.Desired.ModelName,
		plan.Desired.ModelVersion,
		plan.Desired.ModelFormat,
		plan.Desired.SKUName,
		plan.Desired.Capacity,
	)
	for _, check := range plan.Checks {
		fmt.Fprintf(&text, "\n  %-20s %-14s %s", check.Name+":", check.Status, check.Detail)
	}
	return text.String()
}

func hasModelAccountCoordinates(projectSpec config.ProjectSpec) bool {
	return strings.TrimSpace(projectSpec.SubscriptionID) != "" &&
		strings.TrimSpace(projectSpec.ResourceGroup) != "" &&
		strings.TrimSpace(projectSpec.AccountName) != ""
}

func newModelDeploymentOperationStore(
	cmd *cobra.Command,
	resolved *resolvedManifest,
	operation string,
	name string,
) (*receipt.OperationStore, error) {
	path := strings.TrimSpace(getFlag(cmd, "receipt"))
	if path == "" {
		path = receipt.OperationPath(resolved.ManifestPath, operation, name, time.Now())
	} else if !filepath.IsAbs(path) {
		path = resolveRelativePath(resolved.BaseDir, path)
	}
	accountID := fmt.Sprintf(
		"/subscriptions/%s/resourceGroups/%s/providers/Microsoft.CognitiveServices/accounts/%s",
		resolved.Config.Project.SubscriptionID,
		resolved.Config.Project.ResourceGroup,
		resolved.Config.Project.AccountName,
	)
	return newManagedOperationStore(
		cmd,
		path,
		operation,
		resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path: resolved.ManifestPath,
			Hash: resolved.ManifestHash,
		},
		receipt.ResourceReference{
			Name: resolved.Config.Project.AccountName,
			ID:   accountID,
		},
		"",
	)
}

func completeModelDeploymentFailure(
	store *receipt.OperationStore,
	err error,
) error {
	completeErr := store.Complete(operationFailureStatus(err), err)
	combined := errors.Join(err, completeErr)
	if store.Path == "" {
		return combined
	}
	return errors.Join(combined, fmt.Errorf("operation receipt: %s", store.Path))
}
