package main

import (
	"fmt"
	"sort"
	"strings"

	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type validateResult struct {
	Valid     bool   `json:"valid" yaml:"valid"`
	Manifest  string `json:"manifest" yaml:"manifest"`
	Cloud     string `json:"cloud" yaml:"cloud"`
	Agent     string `json:"agent" yaml:"agent"`
	Tools     int    `json:"tools" yaml:"tools"`
	Toolboxes int    `json:"toolboxes" yaml:"toolboxes"`
	Grounding int    `json:"grounding" yaml:"grounding"`
	Metadata  int    `json:"metadata" yaml:"metadata"`
	// DestinationTrust states explicitly that an offline command did not evaluate
	// operator approval of APIM, OpenAPI, or MCP destinations.
	DestinationTrust string `json:"destinationTrust" yaml:"destinationTrust"`
}

// destinationTrustNotEvaluated is reported by the offline commands so their
// success is never read as "these destinations are trusted".
const destinationTrustNotEvaluated = "not-evaluated (structure only; preflight/deploy enforce destination approval)"

type versionResult struct {
	Version string `json:"version" yaml:"version"`
	Commit  string `json:"commit,omitempty" yaml:"commit,omitempty"`
	BuiltAt string `json:"builtAt,omitempty" yaml:"builtAt,omitempty"`
}

func cmdVersion(cmd *cobra.Command, _ []string) error {
	result := versionResult{
		Version: config.Version,
		Commit:  config.BuildCommit,
		BuiltAt: config.BuildDate,
	}
	return printResult(cmd, result, buildMetadata())
}

func cmdValidate(cmd *cobra.Command, _ []string) error {
	prepared, err := prepareAgent(cmd)
	if err != nil {
		return err
	}
	result := validateResult{
		Valid:            true,
		Manifest:         prepared.Resolved.ManifestPath,
		Cloud:            prepared.Resolved.Config.Cloud.Name,
		Agent:            prepared.Resolved.Config.Agent.Name,
		Tools:            len(prepared.WireTools),
		Toolboxes:        len(prepared.Toolboxes),
		Grounding:        len(prepared.Grounding),
		Metadata:         len(prepared.Resolved.Config.Agent.Metadata),
		DestinationTrust: destinationTrustNotEvaluated,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"manifest OK (structure only): %s\n  metadata: %d field(s)\n  destination trust: %s",
		result.Manifest,
		result.Metadata,
		result.DestinationTrust,
	))
}

type planResult struct {
	Cloud           string            `json:"cloud" yaml:"cloud"`
	Agent           string            `json:"agent" yaml:"agent"`
	Model           string            `json:"model" yaml:"model"`
	ProjectEndpoint string            `json:"projectEndpoint,omitempty" yaml:"projectEndpoint,omitempty"`
	Tools           []string          `json:"tools" yaml:"tools"`
	Toolboxes       []string          `json:"toolboxes,omitempty" yaml:"toolboxes,omitempty"`
	Grounding       []string          `json:"grounding,omitempty" yaml:"grounding,omitempty"`
	RuntimeActions  []string          `json:"runtimeActions,omitempty" yaml:"runtimeActions,omitempty"`
	RAIPolicy       string            `json:"raiPolicy,omitempty" yaml:"raiPolicy,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	APIM            *planAPIM         `json:"apim,omitempty" yaml:"apim,omitempty"`
	// DestinationTrust states explicitly that plan did not evaluate operator
	// approval of the destinations it prints.
	DestinationTrust string `json:"destinationTrust" yaml:"destinationTrust"`
}

type planAPIM struct {
	Connection string `json:"connection" yaml:"connection"`
	Target     string `json:"target" yaml:"target"`
	Auth       string `json:"auth" yaml:"auth"`
}

func cmdPlan(cmd *cobra.Command, _ []string) error {
	prepared, err := prepareAgent(cmd)
	if err != nil {
		return err
	}
	cfg := prepared.Resolved.Config
	toolDescriptions, err := tools.DescribeTools(cfg.Tools)
	if err != nil {
		return err
	}
	result := planResult{
		Cloud:            cfg.Cloud.Name,
		Agent:            cfg.Agent.Name,
		Model:            prepared.Desired.Model,
		ProjectEndpoint:  cfg.Project.Endpoint,
		Tools:            toolDescriptions,
		RAIPolicy:        cfg.Agent.RAIPolicyID,
		Metadata:         cfg.Agent.Metadata,
		DestinationTrust: destinationTrustNotEvaluated,
	}
	for _, toolbox := range prepared.Toolboxes {
		result.Toolboxes = append(result.Toolboxes, tools.DescribeToolbox(toolbox))
	}
	for _, store := range prepared.Grounding {
		result.Grounding = append(
			result.Grounding,
			fmt.Sprintf(
				"vector_store(name=%q, files=%d, desired_hash=%s)",
				store.Name,
				len(store.Files),
				store.DesiredHash,
			),
		)
	}
	for _, tool := range cfg.Tools {
		if fmt.Sprint(tool["type"]) == "function" {
			result.RuntimeActions = append(
				result.RuntimeActions,
				"the caller or Hosted runtime must execute function calls and submit function_call_output; Foundry does not execute local functions",
			)
		}
	}
	if prepared.APIMEnabled {
		result.APIM = &planAPIM{
			Connection: prepared.ConnectionName,
			Target:     cfg.Apim.ResolvedTarget(),
			Auth:       cfg.Apim.Auth,
		}
	}

	var text strings.Builder
	fmt.Fprintln(&text, canonicalCommandText("plan"))
	fmt.Fprintf(&text, "  cloud:    %s\n", result.Cloud)
	fmt.Fprintf(&text, "  agent:    %s (model=%s)\n", result.Agent, result.Model)
	endpoint := result.ProjectEndpoint
	if endpoint == "" {
		endpoint = "<will be resolved by --ensure-project>"
	}
	fmt.Fprintf(&text, "  project:  %s\n", endpoint)
	toolText := "(none / instructions-only)"
	if len(result.Tools) > 0 {
		toolText = strings.Join(result.Tools, ", ")
	}
	fmt.Fprintf(&text, "  tools:    %s\n", toolText)
	if len(result.Metadata) > 0 {
		keys := make([]string, 0, len(result.Metadata))
		for key := range result.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			values = append(values, fmt.Sprintf("%s=%q", key, result.Metadata[key]))
		}
		fmt.Fprintf(&text, "  metadata: %s\n", strings.Join(values, ", "))
	}
	if len(result.Toolboxes) > 0 {
		fmt.Fprintf(&text, "  toolboxes: %s\n", strings.Join(result.Toolboxes, ", "))
	}
	if len(result.Grounding) > 0 {
		fmt.Fprintf(&text, "  grounding: %s\n", strings.Join(result.Grounding, ", "))
	}
	for _, action := range result.RuntimeActions {
		fmt.Fprintf(&text, "  runtime:  %s\n", action)
	}
	if result.RAIPolicy != "" {
		fmt.Fprintf(&text, "  guardrail: %s\n", result.RAIPolicy)
	}
	if result.APIM != nil {
		fmt.Fprintf(&text, "  apim:     connection=%s target=%s auth=%s\n",
			result.APIM.Connection,
			result.APIM.Target,
			result.APIM.Auth,
		)
	}
	fmt.Fprintf(&text, "  trust:    %s\n", result.DestinationTrust)
	return printResult(cmd, result, strings.TrimRight(text.String(), "\n"))
}

func cmdPreflight(cmd *cobra.Command, _ []string) error {
	prepared, err := prepareAgent(cmd)
	if err != nil {
		return err
	}
	credential, err := newCredential(cmd, prepared.Resolved.Config.Cloud)
	if err != nil {
		return err
	}
	state, err := runPreflight(cmd, prepared, credential, newHTTPClient(cmd))
	if err != nil {
		return err
	}
	return printResult(cmd, state.Result, preflightText(state.Result))
}
