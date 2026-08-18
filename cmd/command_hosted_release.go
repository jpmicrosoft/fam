package main

import (
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type hostedRoutingResult struct {
	Preview         bool                    `json:"preview" yaml:"preview"`
	Cloud           string                  `json:"cloud" yaml:"cloud"`
	Environment     string                  `json:"environment,omitempty" yaml:"environment,omitempty"`
	Action          string                  `json:"action" yaml:"action"`
	AgentName       string                  `json:"agentName" yaml:"agentName"`
	PreviousVersion string                  `json:"previousVersion,omitempty" yaml:"previousVersion,omitempty"`
	ActiveVersion   string                  `json:"activeVersion" yaml:"activeVersion"`
	Latest          bool                    `json:"latest" yaml:"latest"`
	Changed         bool                    `json:"changed" yaml:"changed"`
	Reconciled      bool                    `json:"reconciled" yaml:"reconciled"`
	Selector        foundry.VersionSelector `json:"selector,omitempty" yaml:"selector,omitempty"`
	Receipt         string                  `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

type hostedDeleteResult struct {
	Preview   bool     `json:"preview" yaml:"preview"`
	Cloud     string   `json:"cloud" yaml:"cloud"`
	Action    string   `json:"action" yaml:"action"`
	AgentName string   `json:"agentName" yaml:"agentName"`
	Versions  []string `json:"versions,omitempty" yaml:"versions,omitempty"`
	Keep      int      `json:"keep,omitempty" yaml:"keep,omitempty"`
	Changed   bool     `json:"changed" yaml:"changed"`
	DryRun    bool     `json:"dryRun" yaml:"dryRun"`
	Receipt   string   `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

func cmdHostedPromote(cmd *cobra.Command, _ []string) error {
	return hostedRoute(cmd, "promote", true)
}

func cmdHostedRollback(cmd *cobra.Command, _ []string) error {
	return hostedRoute(cmd, "rollback", false)
}

func hostedRoute(cmd *cobra.Command, action string, allowLatest bool) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	version := getFlag(cmd, "agent-version")
	latest := allowLatest && getBoolFlag(cmd, "latest")
	if allowLatest {
		if (version == "") == !latest {
			return errs.Config("specify exactly one of --agent-version or --latest")
		}
	} else if version == "" {
		return errs.Config("--agent-version is required")
	}
	target := runtime.Agent.Versions.Latest.Version
	if !latest {
		target = version
	}
	found, err := requireHostedVersion(runtime.Context, runtime, target)
	if err != nil {
		return err
	}
	if found.Draft {
		return errs.Config(
			"Hosted Agent draft version %s cannot receive endpoint traffic; deploy a regular version first",
			target,
		)
	}
	if !strings.EqualFold(found.Status, "active") {
		return errs.Config(
			"Hosted Agent %q version %s is %s, not active",
			runtime.Agent.Name,
			target,
			found.Status,
		)
	}
	before := runtime.Agent.VersionSelectorResolution()
	if before.IsMalformed() {
		return errs.Config("cannot safely change malformed Hosted endpoint routing: %s", before.Problem)
	}
	already := (latest && before.IsLatest()) ||
		(!latest && before.IsPinned() && len(before.ActiveVersions) == 1 && before.ActiveVersions[0] == target)
	result := hostedRoutingResult{
		Preview:         true,
		Cloud:           runtime.Profile.Name,
		Environment:     runtime.Environment,
		Action:          action,
		AgentName:       runtime.Agent.Name,
		PreviousVersion: firstVersion(before.ActiveVersions),
		ActiveVersion:   target,
		Latest:          latest,
		Changed:         !already,
	}
	if already {
		return printResult(cmd, result, fmt.Sprintf(
			"Hosted Agent routing already matches: name=%s version=%s latest=%t",
			runtime.Agent.Name,
			target,
			latest,
		))
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-"+action, "")
	if err != nil {
		return err
	}
	var mutationErr error
	if latest {
		mutationErr = runtime.Client.RestoreDefaultVersionSelectorContext(runtime.Context, runtime.Agent.Name)
	} else {
		mutationErr = runtime.Client.PinAgentVersionContext(runtime.Context, runtime.Agent.Name, target)
	}
	if mutationErr != nil && !errs.IsAmbiguousMutation(mutationErr) {
		_ = store.Complete("failed", mutationErr)
		return releaseFailure(store.Path, mutationErr)
	}
	verified, verifyErr := runtime.Client.GetAgentAfterPatchContext(runtime.Context, runtime.Agent.Name)
	if verifyErr != nil {
		ambiguous := mutationErr
		if ambiguous == nil {
			ambiguous = errs.AmbiguousMutation(verifyErr)
		}
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}
	if err := requireHostedAgentContext(
		runtime.Context,
		runtime.Client,
		verified,
		verified.Versions.Latest.Version,
	); err != nil {
		_ = store.Complete("unknown", err)
		return releaseFailure(store.Path, err)
	}
	after := verified.VersionSelectorResolution()
	matches := (latest && after.IsLatest() && firstVersion(after.ActiveVersions) == target) ||
		(!latest && after.IsPinned() && firstVersion(after.ActiveVersions) == target)
	if !matches {
		ambiguous := errs.AmbiguousMutation(errs.Foundry(
			"Hosted Agent %q routing did not verify at version %s after %s",
			runtime.Agent.Name,
			target,
			action,
		))
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}
	store.Receipt.Agent.ActiveVersionBefore = firstVersion(before.ActiveVersions)
	store.Receipt.Agent.ActiveVersionAfter = target
	store.Receipt.Agent.SelectorAfter = hostedSelectorState(verified)
	store.Receipt.Agent.Changed = true
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	if verified.AgentEndpoint != nil && verified.AgentEndpoint.VersionSelector != nil {
		result.Selector = *verified.AgentEndpoint.VersionSelector
	}
	result.Reconciled = mutationErr != nil
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent %s: name=%s version=%s latest=%t reconciled=%t",
		action,
		runtime.Agent.Name,
		target,
		latest,
		result.Reconciled,
	))
}

func cmdHostedPrune(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	keep := getIntFlag(cmd, "keep")
	if keep < 1 {
		return errs.Config("--keep must be at least 1")
	}
	versions, err := runtime.Client.ListVersionDetailsWithDraftsContext(
		runtime.Context,
		runtime.Agent.Name,
		getBoolFlag(cmd, "include-drafts"),
	)
	if err != nil {
		return err
	}
	selector := runtime.Agent.VersionSelectorResolution()
	if selector.IsMalformed() {
		return errs.Config("cannot safely prune malformed Hosted endpoint routing: %s", selector.Problem)
	}
	protected := append([]string{runtime.Agent.Versions.Latest.Version}, selector.ActiveVersions...)
	planned, err := foundry.PlanVersionRetention(versions, keep, protected...)
	if err != nil {
		return err
	}
	result := hostedDeleteResult{
		Preview:   true,
		Cloud:     runtime.Profile.Name,
		Action:    "prune",
		AgentName: runtime.Agent.Name,
		Versions:  planned,
		Keep:      keep,
		Changed:   len(planned) > 0,
		DryRun:    getBoolFlag(cmd, "dry-run"),
	}
	if len(planned) == 0 {
		return printResult(cmd, result, fmt.Sprintf(
			"nothing to prune for Hosted Agent %s",
			runtime.Agent.Name,
		))
	}
	if result.DryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would prune %d Hosted Agent version(s): %s",
			len(planned),
			strings.Join(planned, ", "),
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete %d old Hosted Agent version(s) of %q?",
		len(planned),
		runtime.Agent.Name,
	)); err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-prune", "")
	if err != nil {
		return err
	}
	for _, version := range planned {
		if err := runtime.Client.DeleteVersionContext(
			runtime.Context,
			runtime.Agent.Name,
			version,
			!getBoolFlag(cmd, "no-force"),
		); err != nil {
			_ = store.Complete("failed", err)
			return releaseFailure(store.Path, err)
		}
		found, verifyErr := runtime.Client.GetAgentVersionContext(
			runtime.Context,
			runtime.Agent.Name,
			version,
		)
		if verifyErr != nil || found != nil {
			ambiguous := verifyErr
			if ambiguous == nil {
				ambiguous = errs.Foundry("Hosted Agent version %s still exists after delete", version)
			}
			ambiguous = errs.AmbiguousMutation(ambiguous)
			_ = store.Complete("unknown", ambiguous)
			return releaseFailure(store.Path, ambiguous)
		}
		_ = store.AddResource(receipt.ResourceChange{
			Kind:   "hosted-agent-version",
			Name:   version,
			Action: "delete",
			Status: "deleted",
		})
	}
	store.Receipt.Agent.Changed = true
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"pruned %d Hosted Agent version(s): %s",
		len(planned),
		strings.Join(planned, ", "),
	))
}

func cmdHostedDeleteVersion(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	version := getFlag(cmd, "agent-version")
	found, err := runtime.Client.GetAgentVersionContext(runtime.Context, runtime.Agent.Name, version)
	if err != nil {
		return err
	}
	result := hostedDeleteResult{
		Preview:   true,
		Cloud:     runtime.Profile.Name,
		Action:    "delete-version",
		AgentName: runtime.Agent.Name,
		Versions:  []string{version},
		Changed:   found != nil,
		DryRun:    getBoolFlag(cmd, "dry-run"),
	}
	if found == nil {
		return printResult(cmd, result, fmt.Sprintf(
			"Hosted Agent version %s not found (nothing to delete)",
			version,
		))
	}
	kind, _ := found.Definition["kind"].(string)
	if !strings.EqualFold(kind, "hosted") {
		return errs.Config("agent version %s is kind %q, not hosted", version, kind)
	}
	selector := runtime.Agent.VersionSelectorResolution()
	if selector.IsMalformed() {
		return errs.Config("cannot safely delete with malformed Hosted endpoint routing: %s", selector.Problem)
	}
	if version == runtime.Agent.Versions.Latest.Version {
		return errs.Config(
			"Hosted Agent version %s is the latest version; deploy a newer version before deleting it",
			version,
		)
	}
	for _, active := range selector.ActiveVersions {
		if active == version {
			return errs.Config(
				"Hosted Agent version %s receives endpoint traffic; promote or rollback to another version first",
				version,
			)
		}
	}
	if result.DryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete Hosted Agent %s version %s",
			runtime.Agent.Name,
			version,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete Hosted Agent %q version %s?",
		runtime.Agent.Name,
		version,
	)); err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-delete-version", "")
	if err != nil {
		return err
	}
	if err := runtime.Client.DeleteVersionContext(
		runtime.Context,
		runtime.Agent.Name,
		version,
		!getBoolFlag(cmd, "no-force"),
	); err != nil {
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	verified, verifyErr := runtime.Client.GetAgentVersionContext(
		runtime.Context,
		runtime.Agent.Name,
		version,
	)
	if verifyErr != nil || verified != nil {
		ambiguous := verifyErr
		if ambiguous == nil {
			ambiguous = errs.Foundry("Hosted Agent version %s still exists after delete", version)
		}
		ambiguous = errs.AmbiguousMutation(ambiguous)
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-agent-version",
		Name:   version,
		Action: "delete",
		Status: "deleted",
	})
	store.Receipt.Agent.Changed = true
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent version deleted: name=%s version=%s",
		runtime.Agent.Name,
		version,
	))
}

func cmdHostedDelete(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	result := hostedDeleteResult{
		Preview:   true,
		Cloud:     runtime.Profile.Name,
		Action:    "delete",
		AgentName: runtime.Agent.Name,
		Changed:   true,
		DryRun:    getBoolFlag(cmd, "dry-run"),
	}
	if result.DryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would permanently delete Hosted Agent %s, all versions, and active sessions",
			runtime.Agent.Name,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Permanently delete Hosted Agent %q, all versions, and terminate active sessions?",
		runtime.Agent.Name,
	)); err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-delete", "")
	if err != nil {
		return err
	}
	removed, err := runtime.Client.DeleteAgentContext(
		runtime.Context,
		runtime.Agent.Name,
		!getBoolFlag(cmd, "no-force"),
	)
	if err != nil {
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	verified, verifyErr := runtime.Client.GetAgentContext(runtime.Context, runtime.Agent.Name)
	if verifyErr != nil || verified != nil {
		ambiguous := verifyErr
		if ambiguous == nil {
			ambiguous = errs.Foundry("Hosted Agent %q still exists after delete", runtime.Agent.Name)
		}
		ambiguous = errs.AmbiguousMutation(ambiguous)
		_ = store.Complete("unknown", ambiguous)
		return releaseFailure(store.Path, ambiguous)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-agent",
		Name:   runtime.Agent.Name,
		Action: "delete",
		Status: "deleted",
	})
	store.Receipt.Agent.Changed = removed
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result.Changed = removed
	result.Receipt = store.Path
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent deleted: name=%s",
		runtime.Agent.Name,
	))
}

func firstVersion(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return versions[0]
}
