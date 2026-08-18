package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"foundry-agent-manager/internal/config"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/project"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type projectCreateResult struct {
	Cloud         string `json:"cloud" yaml:"cloud"`
	Name          string `json:"name" yaml:"name"`
	AccountName   string `json:"accountName" yaml:"accountName"`
	ResourceGroup string `json:"resourceGroup" yaml:"resourceGroup"`
	Location      string `json:"location" yaml:"location"`
	Endpoint      string `json:"endpoint" yaml:"endpoint"`
	Created       bool   `json:"created" yaml:"created"`
	Ready         bool   `json:"ready" yaml:"ready"`
	Receipt       string `json:"receipt" yaml:"receipt"`
}

func cmdProjectCreate(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	if !hasProjectCoordinates(cfg.Project) {
		return errs.Config(
			"project-create requires project.subscription_id, project.resource_group, project.account_name, and project.name",
		)
	}
	waitTimeout, waitInterval, err := projectWaitDurations(cmd)
	if err != nil {
		return err
	}
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	store, err := newProjectOperationStore(cmd, resolved)
	if err != nil {
		return err
	}
	if err := store.AddStep(
		"project",
		"started",
		fmt.Sprintf(
			"ensuring project %s under Foundry account %s in resource group %s",
			cfg.Project.Name,
			cfg.Project.AccountName,
			cfg.Project.ResourceGroup,
		),
	); err != nil {
		return err
	}

	endpoint, created, err := project.EnsureProjectContext(
		commandContext(cmd),
		&cfg.Project,
		credential,
		httpClient,
	)
	if err != nil {
		status := "failed"
		if errs.IsAmbiguousMutation(err) {
			status = "failed-partial"
			_ = store.AddResource(receipt.ResourceChange{
				Kind:         "foundryProject",
				Name:         cfg.Project.Name,
				Action:       "ensure",
				Status:       "unknown",
				CreatedByRun: false,
				Reconciliation: "Rerun foundry-agent-manager project create with the same manifest; " +
					"the operation is idempotent and will reconcile the current ARM state.",
			})
		}
		return completeProjectCreateFailure(store, status, err)
	}

	endpoint, err = validateProjectEndpoint(cfg, endpoint)
	if err != nil {
		return recordProjectCreateFailure(store, cfg, endpoint, created, err)
	}
	cfg.Project.Endpoint = endpoint
	store.Receipt.Project.Endpoint = endpoint

	state, err := project.InspectProjectContext(
		commandContext(cmd),
		&cfg.Project,
		credential,
		httpClient,
	)
	if err != nil {
		return recordProjectCreateFailure(store, cfg, endpoint, created, err)
	}
	if !state.Exists {
		return recordProjectCreateFailure(
			store,
			cfg,
			endpoint,
			created,
			errs.Conflict(
				"Foundry project %q was not visible through ARM after the ensure operation; rerun foundry-agent-manager project create to reconcile",
				cfg.Project.Name,
			),
		)
	}
	if state.Endpoint != "" {
		state.Endpoint, err = validateProjectEndpoint(cfg, state.Endpoint)
		if err != nil {
			return recordProjectCreateFailure(store, cfg, endpoint, created, err)
		}
		endpoint = state.Endpoint
		cfg.Project.Endpoint = endpoint
		store.Receipt.Project.Endpoint = endpoint
	}

	client := newFoundryClient(endpoint, cfg, credential, httpClient)
	if err := client.WaitUntilReadyContext(
		commandContext(cmd),
		waitTimeout,
		waitInterval,
	); err != nil {
		return recordProjectCreateFailure(store, cfg, endpoint, created, err)
	}

	action := "already-existed"
	if created {
		action = "created"
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:           "foundryProject",
		Name:           cfg.Project.Name,
		Action:         action,
		Status:         "ready",
		CreatedByRun:   created,
		Reconciliation: "ARM and the Foundry data plane both report the project as reachable.",
	}); err != nil {
		return completeProjectCreateFailure(store, "failed-partial", err)
	}
	if err := store.AddStep(
		"project",
		"succeeded",
		projectAction(created, endpoint),
	); err != nil {
		return completeProjectCreateFailure(store, "failed-partial", err)
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return errors.Join(err, fmt.Errorf("operation receipt: %s", store.Path))
	}

	result := projectCreateResult{
		Cloud:         cfg.Cloud.Name,
		Name:          cfg.Project.Name,
		AccountName:   cfg.Project.AccountName,
		ResourceGroup: cfg.Project.ResourceGroup,
		Location:      state.Location,
		Endpoint:      endpoint,
		Created:       created,
		Ready:         true,
		Receipt:       store.Path,
	}
	status := "already exists"
	if created {
		status = "created"
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Foundry project %s: %s\n  location: %s\n  endpoint: %s\n  receipt: %s",
		status,
		cfg.Project.Name,
		state.Location,
		endpoint,
		store.Path,
	))
}

func newProjectOperationStore(
	cmd *cobra.Command,
	resolved *resolvedManifest,
) (*receipt.OperationStore, error) {
	path := getFlag(cmd, "receipt")
	if path == "" {
		path = receipt.OperationPath(
			resolved.ManifestPath,
			"project-create",
			resolved.Config.Project.Name,
			time.Now(),
		)
	} else if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, errs.Config("failed to resolve --receipt path: %v", err)
		}
		path = absolute
	}
	return newManagedOperationStore(
		cmd,
		path,
		"project-create",
		resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path: resolved.ManifestPath,
			Hash: resolved.ManifestHash,
		},
		receipt.ResourceReference{Name: resolved.Config.Project.Name},
		resolved.Config.Agent.Name,
	)
}

func recordProjectCreateFailure(
	store *receipt.OperationStore,
	cfg *config.ResolvedConfig,
	endpoint string,
	created bool,
	err error,
) error {
	status := "failed"
	resourceStatus := "unavailable"
	reconciliation := "Correct the reported error and rerun foundry-agent-manager project create."
	if created {
		status = "failed-partial"
		resourceStatus = "created-reconciliation-required"
		reconciliation = "The project was created through ARM but readiness was not fully verified. " +
			"Rerun foundry-agent-manager project create with the same manifest to reconcile without creating a duplicate."
	}
	resourceErr := store.AddResource(receipt.ResourceChange{
		Kind:           "foundryProject",
		Name:           cfg.Project.Name,
		Action:         "ensure",
		Status:         resourceStatus,
		CreatedByRun:   created,
		Reconciliation: reconciliation,
	})
	return completeProjectCreateFailure(store, status, errors.Join(err, resourceErr))
}

func completeProjectCreateFailure(
	store *receipt.OperationStore,
	status string,
	err error,
) error {
	completeErr := store.Complete(status, err)
	combined := errors.Join(err, completeErr)
	if store.Path == "" {
		return combined
	}
	return errors.Join(combined, fmt.Errorf("operation receipt: %s", store.Path))
}
