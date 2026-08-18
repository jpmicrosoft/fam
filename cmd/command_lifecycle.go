package main

import (
	"fmt"
	"strings"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/httpx"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

type lifecycleRuntime struct {
	resolved   *resolvedManifest
	cfg        *config.ResolvedConfig
	endpoint   string
	credential azcore.TokenCredential
	httpClient *httpx.RetryClient
	client     *foundry.Client
}

func lifecycleClient(cmd *cobra.Command) (*lifecycleRuntime, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	return lifecycleClientFromResolved(cmd, resolved)
}

func lifecycleClientFromResolved(
	cmd *cobra.Command,
	resolved *resolvedManifest,
) (*lifecycleRuntime, error) {
	cfg := resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return nil, err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return nil, err
	}
	return &lifecycleRuntime{
		resolved:   resolved,
		cfg:        cfg,
		endpoint:   endpoint,
		credential: credential,
		httpClient: httpClient,
		client:     newFoundryClient(endpoint, cfg, credential, httpClient),
	}, nil
}

type lifecycleResult struct {
	Action   string   `json:"action" yaml:"action"`
	Agent    string   `json:"agent" yaml:"agent"`
	Changed  bool     `json:"changed" yaml:"changed"`
	DryRun   bool     `json:"dryRun" yaml:"dryRun"`
	Versions []string `json:"versions,omitempty" yaml:"versions,omitempty"`
	Latest   string   `json:"latest,omitempty" yaml:"latest,omitempty"`
	APIM     string   `json:"apim,omitempty" yaml:"apim,omitempty"`
}

func cmdDisable(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	if err := runtime.client.DisableContext(commandContext(cmd), runtime.cfg.Agent.Name); err != nil {
		return err
	}
	result := lifecycleResult{Action: "disable", Agent: runtime.cfg.Agent.Name, Changed: true}
	return printResult(cmd, result, fmt.Sprintf("agent suspended: name=%s", runtime.cfg.Agent.Name))
}

func cmdEnable(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	if err := runtime.client.EnableContext(commandContext(cmd), runtime.cfg.Agent.Name); err != nil {
		return err
	}
	result := lifecycleResult{Action: "enable", Agent: runtime.cfg.Agent.Name, Changed: true}
	return printResult(cmd, result, fmt.Sprintf("agent resumed: name=%s", runtime.cfg.Agent.Name))
}

func cmdPrune(cmd *cobra.Command, _ []string) error {
	keep := getIntFlag(cmd, "keep")
	// Validate retention locally so an invalid value cannot cost an Azure
	// round-trip before it is rejected.
	if keep < 1 {
		return errs.Config("--keep must be at least 1")
	}
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	latest, planned, err := runtime.client.PlanPruneContext(commandContext(cmd), runtime.cfg.Agent.Name, keep)
	if err != nil {
		return err
	}
	dryRun := getBoolFlag(cmd, "dry-run")
	result := lifecycleResult{
		Action:   "prune",
		Agent:    runtime.cfg.Agent.Name,
		Changed:  len(planned) > 0,
		DryRun:   dryRun,
		Versions: planned,
		Latest:   latest,
	}
	if len(planned) == 0 {
		return printResult(cmd, result, fmt.Sprintf("nothing to prune for %s (latest=%s)", runtime.cfg.Agent.Name, emptyValue(latest)))
	}
	if dryRun {
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would prune %d version(s) of %s: %s",
			len(planned), runtime.cfg.Agent.Name, strings.Join(planned, ", "),
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete %d old version(s) of %q and retain the newest %d?",
		len(planned), runtime.cfg.Agent.Name, keep,
	)); err != nil {
		return err
	}
	force := !getBoolFlag(cmd, "no-force")
	for _, version := range planned {
		if err := runtime.client.DeleteVersionContext(commandContext(cmd), runtime.cfg.Agent.Name, version, force); err != nil {
			return err
		}
	}
	return printResult(cmd, result, fmt.Sprintf(
		"pruned %d version(s) of %s: %s (kept newest=%d)",
		len(planned), runtime.cfg.Agent.Name, strings.Join(planned, ", "), keep,
	))
}

func cmdDeleteVersion(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	version := getFlag(cmd, "agent-version")
	found, err := runtime.client.GetAgentVersionContext(commandContext(cmd), runtime.cfg.Agent.Name, version)
	if err != nil {
		return err
	}
	dryRun := getBoolFlag(cmd, "dry-run")
	result := lifecycleResult{
		Action:   "delete-version",
		Agent:    runtime.cfg.Agent.Name,
		Changed:  found != nil,
		DryRun:   dryRun,
		Versions: []string{version},
	}
	if found == nil {
		return printResult(cmd, result, fmt.Sprintf("agent %q version %s not found", runtime.cfg.Agent.Name, version))
	}
	agent, err := runtime.client.GetAgentContext(commandContext(cmd), runtime.cfg.Agent.Name)
	if err != nil {
		return err
	}
	if agent != nil {
		versions, err := runtime.client.ListVersionDetailsContext(
			commandContext(cmd),
			runtime.cfg.Agent.Name,
		)
		if err != nil {
			return err
		}
		selector, _ := resolveSelectorWithVersionFallback(agent, versions)
		if selector.IsMalformed() {
			return errs.FoundryWrap(
				fmt.Errorf("%s", selector.Problem),
				"cannot safely delete agent %q version %s",
				runtime.cfg.Agent.Name,
				version,
			)
		}
		for _, activeVersion := range selector.ActiveVersions {
			if activeVersion == version {
				return errs.Conflict(
					"agent %q version %s is active and cannot be deleted; promote or rollback to another version first",
					runtime.cfg.Agent.Name,
					version,
				)
			}
		}
	}
	if dryRun {
		return printResult(cmd, result, fmt.Sprintf("dry run: would delete %s version %s", runtime.cfg.Agent.Name, version))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf("Delete agent %q version %s?", runtime.cfg.Agent.Name, version)); err != nil {
		return err
	}
	if err := runtime.client.DeleteVersionContext(
		commandContext(cmd), runtime.cfg.Agent.Name, version, !getBoolFlag(cmd, "no-force"),
	); err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf("agent version deleted: name=%s version=%s", runtime.cfg.Agent.Name, version))
}

func cmdDelete(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	found, err := runtime.client.GetAgentContext(commandContext(cmd), runtime.cfg.Agent.Name)
	if err != nil {
		return err
	}
	dryRun := getBoolFlag(cmd, "dry-run")
	result := lifecycleResult{
		Action:  "delete",
		Agent:   runtime.cfg.Agent.Name,
		Changed: found != nil,
		DryRun:  dryRun,
	}
	if found == nil {
		return printResult(
			cmd,
			result,
			fmt.Sprintf("agent %q not found (nothing to delete)", runtime.cfg.Agent.Name),
		)
	}
	if dryRun {
		return printResult(cmd, result, fmt.Sprintf("dry run: would delete agent %s and all versions", runtime.cfg.Agent.Name))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf("Delete agent %q and all immutable versions?", runtime.cfg.Agent.Name)); err != nil {
		return err
	}
	removed, err := runtime.client.DeleteAgentContext(
		commandContext(cmd), runtime.cfg.Agent.Name, !getBoolFlag(cmd, "no-force"),
	)
	if err != nil {
		return err
	}
	result.Changed = removed
	text := fmt.Sprintf("agent %q not found (nothing to delete)", runtime.cfg.Agent.Name)
	if removed {
		text = fmt.Sprintf("agent deleted: name=%s (all versions)", runtime.cfg.Agent.Name)
	}
	return printResult(cmd, result, text)
}

func cmdDecommission(cmd *cobra.Command, _ []string) error {
	runtime, err := lifecycleClient(cmd)
	if err != nil {
		return err
	}
	cfg := runtime.cfg
	removeAPIM := cfg.Apim.Enabled && !getBoolFlag(cmd, "no-apim")
	if removeAPIM && !hasProjectCoordinates(cfg.Project) {
		return errs.Config("APIM connection teardown requires complete project resource coordinates")
	}
	dryRun := getBoolFlag(cmd, "dry-run")
	result := lifecycleResult{
		Action: "decommission",
		Agent:  cfg.Agent.Name,
		DryRun: dryRun,
	}
	agent, err := runtime.client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return err
	}
	result.Changed = agent != nil
	var apimState connection.State
	if removeAPIM {
		result.APIM = connection.DefaultConnectionName(&cfg.Apim, cfg.Agent.Name)
		apimState, err = connection.GetAPIMConnectionContext(
			commandContext(cmd),
			&cfg.Apim,
			&cfg.Project,
			result.APIM,
			runtime.credential,
			runtime.httpClient,
		)
		if err != nil {
			return err
		}
		result.Changed = result.Changed || apimState.Exists
	}
	if dryRun {
		var text strings.Builder
		if agent != nil {
			fmt.Fprintf(&text, "dry run: would delete agent %s and all versions", cfg.Agent.Name)
		} else {
			fmt.Fprintf(&text, "dry run: agent %q not found", cfg.Agent.Name)
		}
		if result.APIM != "" {
			if apimState.Exists {
				fmt.Fprintf(&text, "; would delete APIM connection %s", result.APIM)
			} else {
				fmt.Fprintf(&text, "; APIM connection %q not found", result.APIM)
			}
		}
		return printResult(cmd, result, text.String())
	}
	if !result.Changed {
		text := fmt.Sprintf("agent %q not found", cfg.Agent.Name)
		if result.APIM != "" {
			text += fmt.Sprintf(" and APIM connection %q not found", result.APIM)
		}
		text += " (nothing to decommission)"
		return printResult(
			cmd,
			result,
			text,
		)
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Decommission agent %q%s?",
		cfg.Agent.Name,
		apimConfirmationSuffix(existingConnectionName(apimState, result.APIM)),
	)); err != nil {
		return err
	}
	removed := false
	if agent != nil {
		removed, err = runtime.client.DeleteAgentContext(
			commandContext(cmd), cfg.Agent.Name, !getBoolFlag(cmd, "no-force"),
		)
		if err != nil {
			return err
		}
	}
	result.Changed = removed
	var text strings.Builder
	if removed {
		fmt.Fprintf(&text, "agent decommissioned: name=%s", cfg.Agent.Name)
	} else {
		fmt.Fprintf(&text, "agent %q not found", cfg.Agent.Name)
	}
	if removeAPIM && apimState.Exists {
		existed, err := connection.DeleteAPIMConnectionContext(
			commandContext(cmd), &cfg.Apim, &cfg.Project, result.APIM, runtime.credential, runtime.httpClient,
		)
		if err != nil {
			return err
		}
		result.Changed = result.Changed || existed
		state := "not found"
		if existed {
			state = "deleted"
		}
		fmt.Fprintf(&text, "\n  apim: connection %q %s", result.APIM, state)
	} else if removeAPIM {
		fmt.Fprintf(&text, "\n  apim: connection %q not found", result.APIM)
	}
	return printResult(cmd, result, text.String())
}

func emptyValue(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

func apimConfirmationSuffix(name string) string {
	if name == "" {
		return ""
	}
	return fmt.Sprintf(" and APIM project connection %q", name)
}

func existingConnectionName(state connection.State, name string) string {
	if state.Exists {
		return name
	}
	return ""
}
