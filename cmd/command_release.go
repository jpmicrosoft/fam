package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type releaseResult struct {
	Action        string `json:"action" yaml:"action"`
	Agent         string `json:"agent" yaml:"agent"`
	Changed       bool   `json:"changed" yaml:"changed"`
	DryRun        bool   `json:"dryRun" yaml:"dryRun"`
	SelectorMode  string `json:"selectorMode" yaml:"selectorMode"`
	ActiveVersion string `json:"activeVersion" yaml:"activeVersion"`
	LatestVersion string `json:"latestVersion" yaml:"latestVersion"`
	Receipt       string `json:"receipt" yaml:"receipt"`
}

func cmdPromote(cmd *cobra.Command, _ []string) error {
	return changeReleaseRouting(cmd, "promote", getBoolFlag(cmd, "latest"))
}

func cmdRollback(cmd *cobra.Command, _ []string) error {
	return changeReleaseRouting(cmd, "rollback", false)
}

func changeReleaseRouting(cmd *cobra.Command, operation string, restoreLatest bool) error {
	version := getFlag(cmd, "agent-version")
	if restoreLatest == (version != "") {
		return errs.Config("set exactly one of --agent-version or --latest")
	}
	if operation == "rollback" && restoreLatest {
		return errs.Config("rollback requires a concrete --agent-version")
	}

	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	agent, err := runtime.client.GetAgentContext(commandContext(cmd), runtime.cfg.Agent.Name)
	if err != nil {
		return err
	}
	if agent == nil {
		return errs.NotFound("agent %q was not found", runtime.cfg.Agent.Name)
	}
	before := agent.VersionSelectorResolution()
	if before.IsMalformed() {
		return errs.Foundry(
			"agent %q has a malformed version selector: %s",
			runtime.cfg.Agent.Name,
			before.Problem,
		)
	}
	if len(before.ActiveVersions) != 1 {
		return errs.Conflict(
			"agent %q routes traffic to %d versions; resolve routing before %s",
			runtime.cfg.Agent.Name,
			len(before.ActiveVersions),
			operation,
		)
	}
	if !restoreLatest {
		found, err := runtime.client.GetAgentVersionContext(
			commandContext(cmd),
			runtime.cfg.Agent.Name,
			version,
		)
		if err != nil {
			return err
		}
		if found == nil {
			return errs.NotFound("agent %q version %s was not found", runtime.cfg.Agent.Name, version)
		}
	}

	target := version
	targetMode := string(foundry.SelectorPinned)
	if restoreLatest {
		target = agent.Versions.Latest.Version
		targetMode = string(foundry.SelectorDefaultLatest)
		if target == "" {
			return errs.Foundry("agent %q response contained no latest version", runtime.cfg.Agent.Name)
		}
	}
	changed := before.ActiveVersions[0] != target ||
		(restoreLatest && !before.IsLatest()) ||
		(!restoreLatest && !before.IsPinned())
	dryRun := getBoolFlag(cmd, "dry-run")
	path, err := releaseReceiptPath(cmd, runtime, operation)
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		operation,
		runtime.cfg.Cloud.Name,
		receipt.ManifestReference{
			Path: runtime.resolved.ManifestPath,
			Hash: runtime.resolved.ManifestHash,
		},
		receipt.ResourceReference{
			Name:     runtime.cfg.Project.Name,
			Endpoint: runtime.endpoint,
		},
		runtime.cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	store.Receipt.Agent.ID = agent.ID
	store.Receipt.Agent.LatestVersionBefore = agent.Versions.Latest.Version
	store.Receipt.Agent.ActiveVersionBefore = before.ActiveVersions[0]
	store.Receipt.Agent.SelectorBefore = selectorReceipt(before)
	store.Receipt.Agent.Changed = changed
	if err := store.AddStep("inspect-routing", "succeeded", fmt.Sprintf(
		"active=%s latest=%s mode=%s",
		before.ActiveVersions[0],
		agent.Versions.Latest.Version,
		before.Mode,
	)); err != nil {
		return err
	}

	result := releaseResult{
		Action:        operation,
		Agent:         runtime.cfg.Agent.Name,
		Changed:       changed,
		DryRun:        dryRun,
		SelectorMode:  targetMode,
		ActiveVersion: target,
		LatestVersion: agent.Versions.Latest.Version,
		Receipt:       store.Path,
	}
	if !changed {
		store.Receipt.Agent.LatestVersionAfter = agent.Versions.Latest.Version
		store.Receipt.Agent.ActiveVersionAfter = before.ActiveVersions[0]
		store.Receipt.Agent.SelectorAfter = selectorReceipt(before)
		if err := store.Complete("unchanged", nil); err != nil {
			return err
		}
		return printResult(cmd, result, fmt.Sprintf(
			"agent routing unchanged: name=%s active=%s mode=%s\n  receipt: %s",
			result.Agent,
			result.ActiveVersion,
			result.SelectorMode,
			result.Receipt,
		))
	}
	if dryRun {
		if err := store.Complete("planned", nil); err != nil {
			return err
		}
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would %s agent %s to version %s\n  receipt: %s",
			operation,
			result.Agent,
			target,
			result.Receipt,
		))
	}
	if operation == "rollback" {
		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Route all traffic for agent %q from version %s back to version %s?",
			runtime.cfg.Agent.Name,
			before.ActiveVersions[0],
			version,
		)); err != nil {
			_ = store.Complete("cancelled", err)
			return err
		}
	}
	if err := store.AddStep("update-routing", "started", "target version "+target); err != nil {
		return err
	}

	var patchErr error
	if restoreLatest {
		patchErr = runtime.client.RestoreDefaultVersionSelectorContext(
			commandContext(cmd),
			runtime.cfg.Agent.Name,
		)
	} else {
		patchErr = runtime.client.PinAgentVersionContext(
			commandContext(cmd),
			runtime.cfg.Agent.Name,
			version,
		)
	}
	reconciled := false
	if patchErr != nil && !errs.IsAmbiguousMutation(patchErr) {
		_ = store.Complete("failed", patchErr)
		return releaseFailure(store.Path, patchErr)
	}

	verified, verifyErr := runtime.client.GetAgentAfterPatchContext(
		commandContext(cmd),
		runtime.cfg.Agent.Name,
	)
	if verifyErr != nil {
		combined := errors.Join(patchErr, verifyErr)
		_ = store.Complete("unknown", combined)
		return releaseFailure(store.Path, combined)
	}
	after := verified.VersionSelectorResolution()
	if after.IsMalformed() || !routingMatches(after, verified.Versions.Latest.Version, version, restoreLatest) {
		mismatch := errs.Conflict(
			"agent routing did not converge to the requested target; active=%s latest=%s mode=%s",
			strings.Join(after.ActiveVersions, ","),
			verified.Versions.Latest.Version,
			after.Mode,
		)
		combined := errors.Join(patchErr, mismatch)
		_ = store.Complete("unknown", combined)
		return releaseFailure(store.Path, combined)
	}
	if patchErr != nil {
		reconciled = true
	}
	store.Receipt.Agent.LatestVersionAfter = verified.Versions.Latest.Version
	store.Receipt.Agent.ActiveVersionAfter = after.ActiveVersions[0]
	store.Receipt.Agent.SelectorAfter = selectorReceipt(after)
	status := "succeeded"
	details := "routing update verified"
	if reconciled {
		status = "succeeded-reconciled"
		details = "ambiguous PATCH reconciled from committed agent state"
	}
	if err := store.AddStep("update-routing", "succeeded", details); err != nil {
		return err
	}
	if err := store.Complete(status, nil); err != nil {
		return err
	}
	result.SelectorMode = string(after.Mode)
	result.ActiveVersion = after.ActiveVersions[0]
	result.LatestVersion = verified.Versions.Latest.Version
	return printResult(cmd, result, fmt.Sprintf(
		"agent routing updated: name=%s active=%s latest=%s mode=%s\n  receipt: %s",
		result.Agent,
		result.ActiveVersion,
		result.LatestVersion,
		result.SelectorMode,
		result.Receipt,
	))
}

func releaseReceiptPath(cmd *cobra.Command, runtime *lifecycleRuntime, operation string) (string, error) {
	path := getFlag(cmd, "receipt")
	if path == "" {
		return receipt.OperationPath(
			runtime.resolved.ManifestPath,
			operation,
			runtime.cfg.Agent.Name,
			time.Now(),
		), nil
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errs.Config("failed to resolve --receipt path: %v", err)
	}
	return absolute, nil
}

func selectorReceipt(resolution foundry.SelectorResolution) receipt.SelectorState {
	return receipt.SelectorState{
		Mode:          string(resolution.Mode),
		ActiveVersion: strings.Join(resolution.ActiveVersions, ","),
		Raw:           append(json.RawMessage(nil), resolution.RawSelector...),
	}
}

func routingMatches(
	resolution foundry.SelectorResolution,
	latest string,
	version string,
	restoreLatest bool,
) bool {
	if resolution.IsMalformed() || len(resolution.ActiveVersions) != 1 {
		return false
	}
	if restoreLatest {
		return resolution.IsLatest() &&
			latest != "" &&
			resolution.ActiveVersions[0] == latest
	}
	return resolution.IsPinned() && resolution.ActiveVersions[0] == version
}

func releaseFailure(receiptPath string, err error) error {
	if receiptPath == "" {
		return err
	}
	return errors.Join(err, fmt.Errorf("operation receipt: %s", receiptPath))
}
