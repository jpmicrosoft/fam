package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/agentdiff"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"
	"foundry-agent-manager/internal/project"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

type deployResult struct {
	Status          string                    `json:"status" yaml:"status"`
	Changed         bool                      `json:"changed" yaml:"changed"`
	Cloud           string                    `json:"cloud" yaml:"cloud"`
	ProjectEndpoint string                    `json:"projectEndpoint" yaml:"projectEndpoint"`
	ProjectCreated  bool                      `json:"projectCreated" yaml:"projectCreated"`
	Agent           *foundry.AgentResult      `json:"agent,omitempty" yaml:"agent,omitempty"`
	CurrentVersion  string                    `json:"currentVersion,omitempty" yaml:"currentVersion,omitempty"`
	ActiveVersion   string                    `json:"activeVersion,omitempty" yaml:"activeVersion,omitempty"`
	LatestVersion   string                    `json:"latestVersion,omitempty" yaml:"latestVersion,omitempty"`
	Staged          bool                      `json:"staged" yaml:"staged"`
	APIMAction      string                    `json:"apimAction,omitempty" yaml:"apimAction,omitempty"`
	Smoke           *foundry.InvocationResult `json:"smoke,omitempty" yaml:"smoke,omitempty"`
	Receipt         string                    `json:"receipt" yaml:"receipt"`
}

type deploymentTransaction struct {
	store                     *receipt.Store
	cfg                       *config.ResolvedConfig
	credential                azcore.TokenCredential
	httpClient                *httpx.RetryClient
	client                    *foundry.Client
	projectCreated            bool
	rollbackProject           bool
	allowSharedRollback       bool
	agentVersionCreated       bool
	agentVersion              string
	connectionCreated         bool
	connectionUpdated         bool
	connectionEnsured         bool
	connectionNeedsLateUpdate bool
	previousConnection        connection.State
	connectionName            string
	connectionModels          []string
	uncertainMutations        []string
	agentExistedBefore        bool
	agentCreateAttempted      bool
	agentCreateAmbiguous      bool
	selectorChanged           bool
	selectorBefore            *foundry.VersionSelector
	activeVersionBefore       string
	latestVersionBefore       string
}

func cmdDeploy(cmd *cobra.Command, _ []string) error {
	waitTimeout, waitInterval, err := projectWaitDurations(cmd)
	if err != nil {
		return err
	}
	prepared, err := prepareAgent(cmd)
	if err != nil {
		return err
	}
	cfg := prepared.Resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	preflight, err := runPreflight(cmd, prepared, credential, httpClient)
	if err != nil {
		return err
	}

	desiredComparison, err := agentdiff.Compare(nil, prepared.Desired)
	if err != nil {
		return err
	}
	receiptPath := getFlag(cmd, "receipt")
	if receiptPath == "" {
		receiptPath = receipt.DefaultPath(prepared.Resolved.ManifestPath, cfg.Agent.Name, time.Now())
	} else if !filepath.IsAbs(receiptPath) {
		receiptPath, err = filepath.Abs(receiptPath)
		if err != nil {
			return errs.Config("failed to resolve --receipt path: %v", err)
		}
	}
	store, err := newManagedReceiptStore(
		cmd,
		receiptPath,
		cfg.Cloud.Name,
		prepared.Resolved.ManifestPath,
		prepared.Resolved.ManifestHash,
		desiredComparison.DesiredHash,
		cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	// Register the credential before the first write so no receipt step, error,
	// or ARM diagnostic can persist it. Operator trust configuration is never
	// written to receipts.
	store.RegisterSecret(preflight.Secret.Secret)
	store.Receipt.Project.Name = cfg.Project.Name
	store.Receipt.Project.Endpoint = preflight.Endpoint
	store.Receipt.APIM.Name = prepared.ConnectionName
	store.Receipt.APIM.ExistedBefore = preflight.Connection.Exists
	if err := store.AddStep("preflight", "succeeded", fmt.Sprintf(
		"all local and online preflight checks passed; %d approved destination(s)",
		len(preflight.ApprovedDestinations),
	)); err != nil {
		return err
	}

	transaction := &deploymentTransaction{
		store:               store,
		cfg:                 cfg,
		credential:          credential,
		httpClient:          httpClient,
		rollbackProject:     getBoolFlag(cmd, "rollback-created-project"),
		allowSharedRollback: getBoolFlag(cmd, "allow-unconditional-shared-rollback"),
		previousConnection:  preflight.Connection,
		connectionName:      prepared.ConnectionName,
		connectionModels:    append([]string(nil), prepared.APIMModels...),
	}
	result, deployErr := executeDeployment(
		cmd,
		prepared,
		preflight,
		transaction,
		waitTimeout,
		waitInterval,
	)
	if deployErr != nil {
		compensationErr := transaction.compensate()
		return deploymentFailure(store, deployErr, compensationErr)
	}
	if err := store.Complete(result.Status, nil); err != nil {
		return err
	}
	result.Receipt = store.Path

	var text strings.Builder
	if result.Changed && result.Agent != nil {
		if result.Staged {
			fmt.Fprintf(
				&text,
				"agent version staged: id=%s name=%s candidate=%s active=%s latest=%s",
				result.Agent.ID,
				result.Agent.Name,
				result.Agent.Version,
				result.ActiveVersion,
				result.LatestVersion,
			)
		} else {
			fmt.Fprintf(
				&text,
				"agent deployed: id=%s name=%s version=%s active=%s",
				result.Agent.ID,
				result.Agent.Name,
				result.Agent.Version,
				result.ActiveVersion,
			)
		}
	} else {
		fmt.Fprintf(
			&text,
			"agent unchanged: name=%s active=%s latest=%s",
			cfg.Agent.Name,
			emptyValue(result.ActiveVersion),
			emptyValue(result.LatestVersion),
		)
	}
	if result.ProjectCreated {
		fmt.Fprintf(&text, "\n  project created: %s", result.ProjectEndpoint)
	}
	if result.APIMAction != "" {
		fmt.Fprintf(&text, "\n  apim: connection %q %s", prepared.ConnectionName, result.APIMAction)
	}
	if result.Smoke != nil {
		fmt.Fprintf(&text, "\n  smoke: passed response=%q", result.Smoke.OutputText)
	}
	fmt.Fprintf(&text, "\n  receipt: %s", result.Receipt)
	return printResult(cmd, result, text.String())
}

// deploymentFailure records the terminal receipt status and returns the error an
// operator sees. The receipt is the only durable record of what was mutated and
// what still needs manual reconciliation, so its path is always named.
func deploymentFailure(store *receipt.Store, deployErr, compensationErr error) error {
	combined := errors.Join(deployErr, compensationErr)
	status := "failed-compensated"
	if compensationErr != nil {
		status = "failed-partial"
	}
	if receiptErr := store.Complete(status, combined); receiptErr != nil {
		combined = errors.Join(combined, receiptErr)
	}
	if store.Path == "" {
		return combined
	}
	return errors.Join(combined, fmt.Errorf("deployment receipt: %s", store.Path))
}

func promptVersionMetadata(
	desired agentdiff.Desired,
	remote *foundry.Agent,
) (map[string]string, error) {
	if desired.ManageMetadata {
		return custommetadata.Clone(desired.Metadata), nil
	}
	if remote == nil || len(remote.Versions.Latest.Metadata) == 0 {
		return nil, nil
	}
	metadata, err := custommetadata.FromMap(remote.Versions.Latest.Metadata)
	if err != nil {
		return nil, errs.Foundry(
			"latest Prompt Agent metadata cannot be preserved: %v",
			err,
		)
	}
	return metadata, nil
}

func executeDeployment(
	cmd *cobra.Command,
	prepared *preparedAgent,
	preflight *preflightState,
	transaction *deploymentTransaction,
	waitTimeout time.Duration,
	waitInterval time.Duration,
) (*deployResult, error) {
	cfg := prepared.Resolved.Config
	// Fail closed if approval state is missing, and re-verify approvals here so
	// no future call path can reach a mutation without them.
	if !preflight.DestinationsApproved {
		return nil, errs.Security("deployment destinations were not approved; rerun preflight")
	}
	if _, err := approveDestinations(cmd, prepared); err != nil {
		return nil, err
	}
	endpoint := preflight.Endpoint
	if getBoolFlag(cmd, "ensure-project") {
		verbosef(cmd, "ensuring Foundry project %s", cfg.Project.Name)
		if err := transaction.store.AddStep("project", "started", "ensuring project state"); err != nil {
			return nil, err
		}
		resolvedEndpoint, created, err := project.EnsureProjectContext(
			commandContext(cmd), &cfg.Project, transaction.credential, transaction.httpClient,
		)
		if err != nil {
			transaction.recordUncertainMutation("project", err)
			return nil, err
		}
		resolvedEndpoint, err = validateProjectEndpoint(cfg, resolvedEndpoint)
		if err != nil {
			return nil, err
		}
		endpoint = resolvedEndpoint
		cfg.Project.Endpoint = resolvedEndpoint
		transaction.projectCreated = created
		transaction.store.Receipt.Project.Created = created
		transaction.store.Receipt.Project.Endpoint = resolvedEndpoint
		projectDetails := projectAction(created, resolvedEndpoint)
		if !preflight.Project.Exists && !created {
			projectDetails = "ensured " + resolvedEndpoint + "; ARM did not confirm creation ownership"
			transaction.recordUncertain(
				"project-ownership",
				"project ownership is unknown; automatic project deletion is disabled",
			)
		}
		if err := transaction.store.AddStep("project", "succeeded", projectDetails); err != nil {
			return nil, err
		}
	} else {
		var err error
		endpoint, err = validateProjectEndpoint(cfg, preflight.Endpoint)
		if err != nil {
			return nil, err
		}
		cfg.Project.Endpoint = endpoint
	}

	client := newFoundryClient(endpoint, cfg, transaction.credential, transaction.httpClient)
	transaction.client = client
	if transaction.projectCreated {
		verbosef(cmd, "waiting for project data-plane propagation")
		if err := client.WaitUntilReadyContext(commandContext(cmd), waitTimeout, waitInterval); err != nil {
			return nil, err
		}
		if err := transaction.store.AddStep("project-readiness", "succeeded", "project is reachable on the data plane"); err != nil {
			return nil, err
		}
	}
	if !preflight.Project.Exists {
		deployment, err := requireProjectModelDeployment(
			commandContext(cmd),
			client,
			cfg.Agent.Model,
			cfg.Project.Name,
		)
		if err != nil {
			return nil, err
		}
		if err := transaction.store.AddStep(
			"model-reference",
			"succeeded",
			projectModelDeploymentDetails(deployment),
		); err != nil {
			return nil, err
		}
	}

	remote, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return nil, err
	}
	comparison, err := agentdiff.Compare(remote, prepared.Desired)
	if err != nil {
		return nil, err
	}
	willCreateVersion := !(getBoolFlag(cmd, "if-changed") && !comparison.Changed)
	if err := transaction.prepareRoutingForStagedCreate(
		commandContext(cmd),
		remote,
		willCreateVersion,
	); err != nil {
		return nil, err
	}

	if prepared.APIMEnabled {
		connectionState := preflight.Connection
		if transaction.projectCreated {
			connectionState = connection.State{Exists: false, Name: prepared.ConnectionName}
		} else {
			connectionState, err = connection.GetAPIMConnectionContext(
				commandContext(cmd),
				&cfg.Apim,
				&cfg.Project,
				prepared.ConnectionName,
				transaction.credential,
				transaction.httpClient,
			)
			if err != nil {
				return nil, err
			}
		}
		transaction.previousConnection = connectionState
		transaction.store.Receipt.APIM.ExistedBefore = connectionState.Exists
		shouldUpdate := !connectionState.Exists
		if connectionState.Exists {
			apimDiff, err := compareAPIMConnection(
				connectionState,
				&cfg.Apim,
				prepared.ConnectionName,
				prepared.APIMModels,
			)
			if err != nil {
				return nil, err
			}
			shouldUpdate = apimDiff.Changed
		}
		if shouldUpdate && connectionState.Exists && remote != nil &&
			!getBoolFlag(cmd, "allow-active-apim-update") {
			return nil, errs.Conflict(
				"APIM connection %q is shared by the active version; updating it before promotion could change production behavior. "+
					"Use a new apim.connection_name, or rerun with --allow-active-apim-update after reviewing the impact",
				prepared.ConnectionName,
			)
		}
		if connectionState.Exists && !connectionState.Restorable() {
			if !strings.EqualFold(connectionState.AuthType(), "ApiKey") {
				return nil, errs.Conflict(
					"existing APIM connection %q uses unsupported authType %q; choose a different apim.connection_name",
					prepared.ConnectionName,
					connectionState.AuthType(),
				)
			}
			if shouldUpdate && !getBoolFlag(cmd, "allow-nonrestorable-apim-update") {
				return nil, errs.Config(
					"existing APIM connection %q uses an API key that Azure did not return; "+
						"safe rollback is impossible. Rerun with --allow-nonrestorable-apim-update "+
						"to explicitly accept that risk, or use managed_identity authentication",
					prepared.ConnectionName,
				)
			}
		}
		if !connectionState.Exists || connectionState.Restorable() {
			if shouldUpdate {
				action := "updated"
				if !connectionState.Exists {
					action = "created"
				}
				if err := transaction.store.AddStep(
					"apim-connection",
					"started",
					action,
				); err != nil {
					return nil, err
				}
				ensureResult, err := connection.EnsureAPIMConnectionContext(
					commandContext(cmd),
					&cfg.Apim,
					&cfg.Project,
					prepared.ConnectionName,
					prepared.APIMModels,
					preflight.Secret.Secret,
					transaction.credential,
					transaction.httpClient,
				)
				if err != nil {
					transaction.recordUncertainMutation("apim-connection", err)
					return nil, err
				}
				if connectionState.Exists {
					transaction.connectionUpdated = true
				} else if ensureResult.Created {
					transaction.connectionCreated = true
				} else {
					action = "ensured"
					transaction.connectionEnsured = true
					transaction.recordUncertain(
						"apim-connection-ownership",
						"ARM did not confirm connection creation ownership; automatic deletion is disabled",
					)
				}
				transaction.store.Receipt.APIM.Action = action
				if err := transaction.store.AddStep("apim-connection", "succeeded", action); err != nil {
					return nil, err
				}
			} else if err := transaction.store.AddStep(
				"apim-connection",
				"skipped",
				"managed properties are unchanged",
			); err != nil {
				return nil, err
			}
		} else {
			transaction.connectionNeedsLateUpdate = shouldUpdate
			if !shouldUpdate {
				if err := transaction.store.AddStep(
					"apim-connection",
					"skipped",
					"managed properties are unchanged",
				); err != nil {
					return nil, err
				}
			}
		}
	}

	result := &deployResult{
		Status:          "succeeded",
		Cloud:           cfg.Cloud.Name,
		ProjectEndpoint: endpoint,
		ProjectCreated:  transaction.projectCreated,
		CurrentVersion:  comparison.CurrentVersion,
		ActiveVersion:   transaction.activeVersionBefore,
		LatestVersion:   transaction.latestVersionBefore,
	}
	if getBoolFlag(cmd, "if-changed") && !comparison.Changed {
		result.Status = "unchanged"
		result.Changed = false
		transaction.store.Receipt.Agent.Changed = false
		transaction.store.Receipt.Agent.Version = comparison.CurrentVersion
		transaction.store.Receipt.Agent.LatestVersionAfter = transaction.latestVersionBefore
		transaction.store.Receipt.Agent.ActiveVersionAfter = transaction.activeVersionBefore
		transaction.store.Receipt.Agent.SelectorAfter = transaction.store.Receipt.Agent.SelectorBefore
		if err := transaction.store.AddStep("agent-version", "skipped", "managed fields are unchanged"); err != nil {
			return nil, err
		}
	} else {
		if err := transaction.store.AddStep(
			"agent-version",
			"started",
			"creating immutable version",
		); err != nil {
			return nil, err
		}
		transaction.agentCreateAttempted = true
		desiredPayload := agentdiff.DesiredValue(prepared.Desired)
		definition, ok := desiredPayload["definition"].(map[string]interface{})
		if !ok {
			return nil, errs.Config("managed agent definition is invalid")
		}
		versionMetadata, err := promptVersionMetadata(prepared.Desired, remote)
		if err != nil {
			return nil, err
		}
		deployed, err := client.UpsertDefinitionContext(
			commandContext(cmd),
			cfg.Agent.Name,
			prepared.Desired.Description,
			definition,
			versionMetadata,
		)
		if err != nil {
			transaction.agentCreateAmbiguous = errs.IsAmbiguousMutation(err)
			transaction.recordUncertainMutation("agent-version", err)
			return nil, err
		}

		transaction.agentVersionCreated = true
		transaction.agentVersion = deployed.Version
		transaction.store.Receipt.Agent.ID = deployed.ID
		transaction.store.Receipt.Agent.Version = deployed.Version
		transaction.store.Receipt.Agent.CreatedVersion = deployed.Version
		transaction.store.Receipt.Agent.Changed = true
		result.Changed = true
		result.Agent = deployed
		result.CurrentVersion = deployed.Version
		if err := transaction.store.AddStep("agent-version", "succeeded", "created version "+deployed.Version); err != nil {
			return nil, err
		}

		active, latest, staged, err := transaction.finalizeStagedRouting(
			commandContext(cmd),
			deployed.Version,
		)
		if err != nil {
			return nil, err
		}
		result.ActiveVersion = active
		result.LatestVersion = latest
		result.Staged = staged
		if staged {
			result.Status = "staged"
		}
	}

	if prepared.APIMEnabled && transaction.connectionNeedsLateUpdate {
		if err := transaction.store.AddStep(
			"apim-connection",
			"started",
			"updating non-restorable API-key connection",
		); err != nil {
			return nil, err
		}
		if _, err := connection.EnsureAPIMConnectionContext(
			commandContext(cmd),
			&cfg.Apim,
			&cfg.Project,
			prepared.ConnectionName,
			prepared.APIMModels,
			preflight.Secret.Secret,
			transaction.credential,
			transaction.httpClient,
		); err != nil {
			transaction.recordUncertainMutation("apim-connection", err)
			return nil, err
		}
		transaction.connectionUpdated = true
		transaction.store.Receipt.APIM.Action = "updated-nonrestorable"
		if err := transaction.store.AddStep(
			"apim-connection",
			"succeeded",
			"updated after agent creation; prior API-key credential was unavailable for rollback",
		); err != nil {
			return nil, err
		}
	}

	if transaction.connectionCreated {
		result.APIMAction = "created"
	} else if transaction.connectionUpdated {
		result.APIMAction = "updated"
	} else if transaction.connectionEnsured {
		result.APIMAction = "ensured"
	}
	if result.APIMAction != "" && !result.Changed {
		result.Status = "succeeded"
		result.Changed = true
	}

	if getBoolFlag(cmd, "smoke-test") {
		transaction.store.Receipt.Smoke.Attempted = true
		structuredInputs, err := loadStructuredInputValues(cmd, cfg.Agent.StructuredInputs)
		if err != nil {
			return nil, err
		}
		invocation, err := client.InvokePromptVersionWithOptionsContext(
			commandContext(cmd),
			cfg.Agent.Name,
			result.CurrentVersion,
			getFlag(cmd, "smoke-prompt"),
			foundry.InvocationOptions{
				StructuredInputs: structuredInputs,
				MemoryUserID:     getFlag(cmd, "memory-user-id"),
			},
		)
		if err != nil {
			return nil, err
		}
		invocation, err = resolveMCPApprovals(
			cmd,
			invocation,
			func(
				ctx context.Context,
				previousResponseID string,
				decisions []foundry.MCPApprovalDecision,
			) (*foundry.InvocationResult, error) {
				return client.ContinuePromptVersionWithApprovalsContext(
					ctx,
					cfg.Agent.Name,
					result.CurrentVersion,
					previousResponseID,
					decisions,
					foundry.InvocationOptions{MemoryUserID: getFlag(cmd, "memory-user-id")},
				)
			},
		)
		if err != nil {
			return nil, err
		}
		transaction.store.Receipt.Smoke.Succeeded = true
		transaction.store.Receipt.Smoke.ResponseID = invocation.ID
		result.Smoke = invocation
		if err := transaction.store.AddStep("smoke-test", "succeeded", "response id "+invocation.ID); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (t *deploymentTransaction) prepareRoutingForStagedCreate(
	ctx context.Context,
	remote *foundry.Agent,
	willCreateVersion bool,
) error {
	t.agentExistedBefore = remote != nil
	if remote == nil {
		return t.store.AddStep(
			"stage-routing",
			"skipped",
			"first deployment has no prior active version",
		)
	}
	resolution := remote.VersionSelectorResolution()
	if resolution.IsMalformed() {
		return errs.Foundry(
			"agent %q has a malformed version selector: %s",
			t.cfg.Agent.Name,
			resolution.Problem,
		)
	}
	if len(resolution.ActiveVersions) != 1 {
		return errs.Conflict(
			"agent %q routes traffic to %d versions; resolve routing before deploying",
			t.cfg.Agent.Name,
			len(resolution.ActiveVersions),
		)
	}
	t.activeVersionBefore = resolution.ActiveVersions[0]
	t.latestVersionBefore = remote.Versions.Latest.Version
	if remote.AgentEndpoint != nil {
		t.selectorBefore = remote.AgentEndpoint.VersionSelector
	}
	t.store.Receipt.Agent.ID = remote.ID
	t.store.Receipt.Agent.LatestVersionBefore = t.latestVersionBefore
	t.store.Receipt.Agent.ActiveVersionBefore = t.activeVersionBefore
	t.store.Receipt.Agent.SelectorBefore = selectorReceipt(resolution)

	if !willCreateVersion {
		return t.store.AddStep(
			"stage-routing",
			"skipped",
			"managed fields are unchanged, so no candidate version will be created",
		)
	}
	if !resolution.IsLatest() {
		return t.store.AddStep(
			"stage-routing",
			"skipped",
			fmt.Sprintf("active version %s is already pinned", t.activeVersionBefore),
		)
	}
	if t.latestVersionBefore == "" {
		return errs.Foundry(
			"agent %q response contained no latest version to pin before deployment",
			t.cfg.Agent.Name,
		)
	}
	if err := t.store.AddStep(
		"stage-routing",
		"started",
		"pinning current latest version "+t.latestVersionBefore,
	); err != nil {
		return err
	}
	_, reconciled, err := t.pinAndVerify(ctx, t.latestVersionBefore)
	if err != nil {
		t.recordUncertainMutation("version-selector", err)
		return err
	}
	t.selectorChanged = true
	details := "current latest version pinned before candidate creation"
	if reconciled {
		details = "ambiguous selector PATCH reconciled before candidate creation"
	}
	return t.store.AddStep("stage-routing", "succeeded", details)
}

func (t *deploymentTransaction) finalizeStagedRouting(
	ctx context.Context,
	candidateVersion string,
) (string, string, bool, error) {
	agent, err := t.client.GetAgentContext(ctx, t.cfg.Agent.Name)
	if err != nil {
		return "", "", false, err
	}
	if agent == nil {
		return "", "", false, errs.NotFound(
			"agent %q was not found after creating version %s",
			t.cfg.Agent.Name,
			candidateVersion,
		)
	}
	if agent.Versions.Latest.Version != candidateVersion {
		return "", "", false, errs.Conflict(
			"agent %q latest version is %s after creating candidate %s; concurrent version creation may have occurred",
			t.cfg.Agent.Name,
			emptyValue(agent.Versions.Latest.Version),
			candidateVersion,
		)
	}
	if !t.agentExistedBefore {
		if err := t.store.AddStep(
			"stage-routing",
			"started",
			"pinning the first deployed version",
		); err != nil {
			return "", "", false, err
		}
		verified, reconciled, err := t.pinAndVerify(ctx, candidateVersion)
		if err != nil {
			t.recordUncertainMutation("version-selector", err)
			return "", "", false, err
		}
		t.selectorChanged = true
		agent = verified
		details := "first deployed version pinned"
		if reconciled {
			details = "ambiguous first-version selector PATCH reconciled"
		}
		if err := t.store.AddStep("stage-routing", "succeeded", details); err != nil {
			return "", "", false, err
		}
	}
	resolution := agent.VersionSelectorResolution()
	if resolution.IsMalformed() || len(resolution.ActiveVersions) != 1 {
		return "", "", false, errs.Conflict(
			"agent %q routing could not be verified after creating version %s",
			t.cfg.Agent.Name,
			candidateVersion,
		)
	}
	active := resolution.ActiveVersions[0]
	if t.agentExistedBefore && active != t.activeVersionBefore {
		return "", "", false, errs.Conflict(
			"agent %q active version changed from %s to %s while staging candidate %s",
			t.cfg.Agent.Name,
			t.activeVersionBefore,
			active,
			candidateVersion,
		)
	}
	t.store.Receipt.Agent.LatestVersionAfter = agent.Versions.Latest.Version
	t.store.Receipt.Agent.ActiveVersionAfter = active
	t.store.Receipt.Agent.SelectorAfter = selectorReceipt(resolution)
	if err := t.store.Save(); err != nil {
		return "", "", false, err
	}
	return active, agent.Versions.Latest.Version, active != candidateVersion, nil
}

func (t *deploymentTransaction) pinAndVerify(
	ctx context.Context,
	version string,
) (*foundry.Agent, bool, error) {
	patchErr := t.client.PinAgentVersionContext(ctx, t.cfg.Agent.Name, version)
	if patchErr != nil && !errs.IsAmbiguousMutation(patchErr) {
		return nil, false, patchErr
	}
	verified, verifyErr := t.client.GetAgentAfterPatchContext(ctx, t.cfg.Agent.Name)
	if verifyErr != nil {
		return nil, false, errors.Join(patchErr, verifyErr)
	}
	resolution := verified.VersionSelectorResolution()
	if resolution.IsMalformed() ||
		!resolution.IsPinned() ||
		len(resolution.ActiveVersions) != 1 ||
		resolution.ActiveVersions[0] != version {
		return nil, false, errors.Join(
			patchErr,
			errs.Conflict(
				"agent %q selector did not converge to pinned version %s",
				t.cfg.Agent.Name,
				version,
			),
		)
	}
	return verified, patchErr != nil, nil
}

func (t *deploymentTransaction) compensate() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var compensationErrors []error
	candidateRemoved := !t.agentVersionCreated

	if t.client != nil && t.agentVersionCreated {
		err := t.client.DeleteVersionContext(ctx, t.cfg.Agent.Name, t.agentVersion, true)
		if err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("agent compensation failed: %w", err))
			_ = t.store.AddStep("compensate-agent", "failed", err.Error())
		} else {
			candidateRemoved = true
			t.store.Receipt.Agent.Compensated = true
			if receiptErr := t.store.AddStep("compensate-agent", "succeeded", "removed version created by the failed deployment"); receiptErr != nil {
				compensationErrors = append(compensationErrors, receiptErr)
			}
		}
	}

	if t.selectorChanged && t.agentExistedBefore {
		if t.agentCreateAmbiguous || !candidateRemoved {
			err := fmt.Errorf(
				"agent %q version selector remains pinned to %s because candidate creation or cleanup is uncertain; restoring latest could activate an unknown version",
				t.cfg.Agent.Name,
				t.activeVersionBefore,
			)
			compensationErrors = append(compensationErrors, err)
			_ = t.store.AddStep("reconcile-version-selector", "required", err.Error())
		} else if err := t.restoreSelectorAfterCompensation(ctx); err != nil {
			compensationErrors = append(compensationErrors, err)
			_ = t.store.AddStep("compensate-version-selector", "failed", err.Error())
		} else if receiptErr := t.store.AddStep(
			"compensate-version-selector",
			"succeeded",
			"restored the routing mode that existed before candidate creation",
		); receiptErr != nil {
			compensationErrors = append(compensationErrors, receiptErr)
		}
	}

	if (t.connectionUpdated || t.connectionCreated) && !t.allowSharedRollback {
		err := fmt.Errorf(
			"APIM connection %q requires manual reconciliation; automatic shared-resource rollback is disabled without --allow-unconditional-shared-rollback",
			t.connectionName,
		)
		compensationErrors = append(compensationErrors, err)
		_ = t.store.AddStep("reconcile-apim", "required", err.Error())
	} else if t.connectionUpdated {
		current, err := t.currentConnectionForCompensation(ctx)
		if err != nil {
			compensationErrors = append(compensationErrors, err)
			_ = t.store.AddStep("compensate-apim", "failed", err.Error())
		} else if !current.Exists {
			err := fmt.Errorf("APIM connection restoration refused because the connection was deleted after deployment")
			compensationErrors = append(compensationErrors, err)
			_ = t.store.AddStep("compensate-apim", "failed", err.Error())
		} else {
			err = connection.RestoreAPIMConnectionContext(
				ctx,
				&t.cfg.Apim,
				&t.cfg.Project,
				t.previousConnection,
				t.credential,
				t.httpClient,
			)
			if err != nil {
				compensationErrors = append(compensationErrors, fmt.Errorf("APIM connection restoration failed: %w", err))
				_ = t.store.AddStep("compensate-apim", "failed", err.Error())
			} else {
				t.store.Receipt.APIM.Compensated = true
				if receiptErr := t.store.AddStep("compensate-apim", "succeeded", "restored the previous connection properties"); receiptErr != nil {
					compensationErrors = append(compensationErrors, receiptErr)
				}
			}
		}
	} else if t.connectionCreated {
		current, err := t.currentConnectionForCompensation(ctx)
		if err != nil {
			compensationErrors = append(compensationErrors, fmt.Errorf("APIM connection cleanup failed: %w", err))
			_ = t.store.AddStep("compensate-apim", "failed", err.Error())
		} else if !current.Exists {
			t.store.Receipt.APIM.Compensated = true
			if receiptErr := t.store.AddStep("compensate-apim", "succeeded", "connection was already absent"); receiptErr != nil {
				compensationErrors = append(compensationErrors, receiptErr)
			}
		} else {
			_, err = connection.DeleteAPIMConnectionContext(
				ctx,
				&t.cfg.Apim,
				&t.cfg.Project,
				t.connectionName,
				t.credential,
				t.httpClient,
			)
			if err != nil {
				compensationErrors = append(compensationErrors, fmt.Errorf("APIM connection cleanup failed: %w", err))
				_ = t.store.AddStep("compensate-apim", "failed", err.Error())
			} else {
				t.store.Receipt.APIM.Compensated = true
				if receiptErr := t.store.AddStep("compensate-apim", "succeeded", "deleted the connection created by the failed deployment"); receiptErr != nil {
					compensationErrors = append(compensationErrors, receiptErr)
				}
			}
		}
	}

	if t.projectCreated && t.rollbackProject {
		if !t.allowSharedRollback {
			err := fmt.Errorf(
				"project %q requires manual reconciliation; automatic project deletion is disabled without --allow-unconditional-shared-rollback",
				t.cfg.Project.Name,
			)
			compensationErrors = append(compensationErrors, err)
			_ = t.store.AddStep("reconcile-project", "required", err.Error())
		} else {
			_, err := project.DeleteProjectContext(ctx, &t.cfg.Project, t.credential, t.httpClient)
			if err != nil {
				compensationErrors = append(compensationErrors, fmt.Errorf("project cleanup failed: %w", err))
				_ = t.store.AddStep("compensate-project", "failed", err.Error())
			} else {
				t.store.Receipt.Project.Compensated = true
				if receiptErr := t.store.AddStep("compensate-project", "succeeded", "deleted the project created by the failed deployment"); receiptErr != nil {
					compensationErrors = append(compensationErrors, receiptErr)
				}
			}
		}
	}
	for _, mutation := range t.uncertainMutations {
		err := fmt.Errorf(
			"%s outcome is unknown; inspect Azure and the deployment receipt before retrying",
			mutation,
		)
		compensationErrors = append(compensationErrors, err)
		_ = t.store.AddStep("reconcile-"+mutation, "required", err.Error())
	}
	return errors.Join(compensationErrors...)
}

func (t *deploymentTransaction) restoreSelectorAfterCompensation(ctx context.Context) error {
	patchErr := t.client.PatchVersionSelectorContext(
		ctx,
		t.cfg.Agent.Name,
		t.selectorBefore,
	)
	if patchErr != nil && !errs.IsAmbiguousMutation(patchErr) {
		return fmt.Errorf("version selector restoration failed: %w", patchErr)
	}
	verified, verifyErr := t.client.GetAgentAfterPatchContext(ctx, t.cfg.Agent.Name)
	if verifyErr != nil {
		return errors.Join(patchErr, verifyErr)
	}
	resolution := verified.VersionSelectorResolution()
	expected := foundry.ResolveVersionSelector(t.selectorBefore, verified.Versions.Latest.Version)
	if resolution.IsMalformed() ||
		expected.IsMalformed() ||
		resolution.Mode != expected.Mode ||
		strings.Join(resolution.ActiveVersions, ",") != strings.Join(expected.ActiveVersions, ",") {
		return errors.Join(
			patchErr,
			errs.Conflict("version selector restoration did not converge"),
		)
	}
	return nil
}

func (t *deploymentTransaction) recordUncertainMutation(action string, err error) {
	if !errs.IsAmbiguousMutation(err) {
		return
	}
	t.recordUncertain(
		action,
		"Azure may have committed the request; manual reconciliation is required",
	)
}

func (t *deploymentTransaction) recordUncertain(action, details string) {
	for _, existing := range t.uncertainMutations {
		if existing == action {
			return
		}
	}
	t.uncertainMutations = append(t.uncertainMutations, action)
	_ = t.store.AddStep(
		action,
		"unknown",
		details,
	)
}

func (t *deploymentTransaction) currentConnectionForCompensation(ctx context.Context) (connection.State, error) {
	current, err := connection.GetAPIMConnectionContext(
		ctx,
		&t.cfg.Apim,
		&t.cfg.Project,
		t.connectionName,
		t.credential,
		t.httpClient,
	)
	if err != nil {
		return connection.State{}, err
	}
	if !current.Exists {
		return current, nil
	}
	comparison, err := compareAPIMConnection(
		current,
		&t.cfg.Apim,
		t.connectionName,
		t.connectionModels,
	)
	if err != nil {
		return connection.State{}, err
	}
	if comparison.Changed {
		return connection.State{}, errs.Conflict(
			"APIM connection %q changed after deployment; refusing compensation to avoid overwriting concurrent work",
			t.connectionName,
		)
	}
	return current, nil
}

func projectWaitDurations(cmd *cobra.Command) (time.Duration, time.Duration, error) {
	waitTimeout := getFloatFlag(cmd, "project-wait-timeout")
	waitInterval := getFloatFlag(cmd, "project-wait-interval")
	maxDurationSeconds := float64(math.MaxInt64) / float64(time.Second)
	if math.IsNaN(waitTimeout) || math.IsInf(waitTimeout, 0) || waitTimeout <= 0 || waitTimeout > maxDurationSeconds {
		return 0, 0, errs.Config("--project-wait-timeout must be a finite number of seconds greater than zero")
	}
	if math.IsNaN(waitInterval) || math.IsInf(waitInterval, 0) || waitInterval < 0 || waitInterval > maxDurationSeconds {
		return 0, 0, errs.Config("--project-wait-interval must be a finite, non-negative number of seconds")
	}
	return time.Duration(waitTimeout * float64(time.Second)), time.Duration(waitInterval * float64(time.Second)), nil
}

func projectAction(created bool, endpoint string) string {
	if created {
		return "created " + endpoint
	}
	return "already existed at " + endpoint
}
