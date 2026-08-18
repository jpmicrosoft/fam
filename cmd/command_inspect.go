package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"

	"foundry-agent-manager/internal/agentdiff"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"

	"github.com/spf13/cobra"
)

type statusResult struct {
	Cloud           string      `json:"cloud" yaml:"cloud"`
	ProjectEndpoint string      `json:"projectEndpoint" yaml:"projectEndpoint"`
	Agent           agentStatus `json:"agent" yaml:"agent"`
	APIM            *apimStatus `json:"apim,omitempty" yaml:"apim,omitempty"`
}

type agentStatus struct {
	Name            string   `json:"name" yaml:"name"`
	Exists          bool     `json:"exists" yaml:"exists"`
	State           string   `json:"state,omitempty" yaml:"state,omitempty"`
	LatestVersion   string   `json:"latestVersion,omitempty" yaml:"latestVersion,omitempty"`
	ActiveVersions  []string `json:"activeVersions,omitempty" yaml:"activeVersions,omitempty"`
	SelectorMode    string   `json:"selectorMode,omitempty" yaml:"selectorMode,omitempty"`
	SelectorProblem string   `json:"selectorProblem,omitempty" yaml:"selectorProblem,omitempty"`
	VersionStatus   string   `json:"versionStatus,omitempty" yaml:"versionStatus,omitempty"`
}

type apimStatus struct {
	Name     string `json:"name" yaml:"name"`
	Exists   bool   `json:"exists" yaml:"exists"`
	Target   string `json:"target,omitempty" yaml:"target,omitempty"`
	AuthType string `json:"authType,omitempty" yaml:"authType,omitempty"`
}

func cmdStatus(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, cfg, credential, httpClient)
	agent, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return err
	}
	result := statusResult{
		Cloud:           cfg.Cloud.Name,
		ProjectEndpoint: endpoint,
		Agent:           agentStatus{Name: cfg.Agent.Name, Exists: agent != nil},
	}
	if agent != nil {
		result.Agent.State = agent.State
		result.Agent.LatestVersion = agent.Versions.Latest.Version
		result.Agent.VersionStatus = agent.Versions.Latest.Status
		selector := agent.VersionSelectorResolution()
		result.Agent.SelectorMode = string(selector.Mode)
		result.Agent.ActiveVersions = append([]string(nil), selector.ActiveVersions...)
		result.Agent.SelectorProblem = selector.Problem
	}
	if cfg.Apim.Enabled && !getBoolFlag(cmd, "no-apim") {
		if !hasProjectCoordinates(cfg.Project) {
			return errs.Config("APIM connection status requires complete project resource coordinates")
		}
		name := connection.DefaultConnectionName(&cfg.Apim, cfg.Agent.Name)
		state, err := connection.GetAPIMConnectionContext(
			commandContext(cmd), &cfg.Apim, &cfg.Project, name, credential, httpClient,
		)
		if err != nil {
			return err
		}
		result.APIM = safeAPIMStatus(state, name)
	}

	var text strings.Builder
	fmt.Fprintf(&text, "agent status: name=%s exists=%t", result.Agent.Name, result.Agent.Exists)
	if result.Agent.Exists {
		fmt.Fprintf(&text, " state=%s active=%s latest=%s selector=%s status=%s",
			result.Agent.State,
			emptyValue(strings.Join(result.Agent.ActiveVersions, ",")),
			result.Agent.LatestVersion,
			result.Agent.SelectorMode,
			result.Agent.VersionStatus,
		)
		if result.Agent.SelectorProblem != "" {
			fmt.Fprintf(&text, " selector_problem=%q", result.Agent.SelectorProblem)
		}
	}
	if result.APIM != nil {
		fmt.Fprintf(&text, "\n  apim: name=%s exists=%t", result.APIM.Name, result.APIM.Exists)
		if result.APIM.Exists {
			fmt.Fprintf(&text, " target=%s auth=%s", result.APIM.Target, result.APIM.AuthType)
		}
	}
	return printResult(cmd, result, text.String())
}

func safeAPIMStatus(state connection.State, defaultName string) *apimStatus {
	name := state.Name
	if name == "" {
		name = defaultName
	}
	result := &apimStatus{Name: name, Exists: state.Exists}
	if state.Exists {
		result.Target, _ = state.Properties["target"].(string)
		result.AuthType, _ = state.Properties["authType"].(string)
	}
	return result
}

type showResult struct {
	Cloud           string      `json:"cloud" yaml:"cloud"`
	ProjectEndpoint string      `json:"projectEndpoint" yaml:"projectEndpoint"`
	Agent           interface{} `json:"agent" yaml:"agent"`
}

func cmdShow(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, cfg, credential, httpClient)

	var value interface{}
	if version := getFlag(cmd, "agent-version"); version != "" {
		found, err := client.GetAgentVersionContext(commandContext(cmd), cfg.Agent.Name, version)
		if err != nil {
			return err
		}
		if found == nil {
			return errs.NotFound("agent %q version %s was not found", cfg.Agent.Name, version)
		}
		value = found
	} else {
		found, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
		if err != nil {
			return err
		}
		if found == nil {
			return errs.NotFound("agent %q was not found", cfg.Agent.Name)
		}
		value = found
	}
	result := showResult{Cloud: cfg.Cloud.Name, ProjectEndpoint: endpoint, Agent: value}
	return printResult(cmd, result, fmt.Sprintf("agent details: name=%s", cfg.Agent.Name))
}

type versionsResult struct {
	Agent          string                 `json:"agent" yaml:"agent"`
	LatestVersion  string                 `json:"latestVersion,omitempty" yaml:"latestVersion,omitempty"`
	ActiveVersions []string               `json:"activeVersions,omitempty" yaml:"activeVersions,omitempty"`
	Versions       []foundry.AgentVersion `json:"versions" yaml:"versions"`
}

func cmdVersions(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, cfg, credential, httpClient)
	agent, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return err
	}
	versions, err := client.ListVersionDetailsContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return err
	}
	result := versionsResult{Agent: cfg.Agent.Name, Versions: versions}
	if agent != nil {
		selector, latest := resolveSelectorWithVersionFallback(agent, versions)
		result.LatestVersion = latest
		if len(versions) > 0 && selector.IsMalformed() {
			return errs.Foundry(
				"agent %q has a malformed version selector: %s",
				cfg.Agent.Name,
				selector.Problem,
			)
		}
		if !selector.IsMalformed() {
			result.ActiveVersions = append([]string(nil), selector.ActiveVersions...)
		}
	}
	var text strings.Builder
	if len(versions) == 0 {
		fmt.Fprintf(&text, "no versions found for %s", cfg.Agent.Name)
	} else {
		fmt.Fprintf(
			&text,
			"versions for %s (active=%s latest=%s):",
			cfg.Agent.Name,
			emptyValue(strings.Join(result.ActiveVersions, ",")),
			emptyValue(result.LatestVersion),
		)
		for _, version := range versions {
			fmt.Fprintf(&text, "\n  %s status=%s created=%d", version.Version, version.Status, version.CreatedAt)
		}
	}
	return printResult(cmd, result, text.String())
}

func resolveSelectorWithVersionFallback(
	agent *foundry.Agent,
	versions []foundry.AgentVersion,
) (foundry.SelectorResolution, string) {
	if agent == nil {
		return foundry.SelectorResolution{}, ""
	}
	latest := agent.Versions.Latest.Version
	if latest == "" {
		var newest foundry.AgentVersion
		for index, version := range versions {
			if index == 0 || version.CreatedAt > newest.CreatedAt {
				newest = version
			}
		}
		latest = newest.Version
	}
	var selector *foundry.VersionSelector
	if agent.AgentEndpoint != nil {
		selector = agent.AgentEndpoint.VersionSelector
	}
	return foundry.ResolveVersionSelector(selector, latest), latest
}

type connectionDiff struct {
	Enabled bool        `json:"enabled" yaml:"enabled"`
	Exists  bool        `json:"exists" yaml:"exists"`
	Changed bool        `json:"changed" yaml:"changed"`
	Fields  []string    `json:"fields,omitempty" yaml:"fields,omitempty"`
	Status  *apimStatus `json:"status,omitempty" yaml:"status,omitempty"`
}

type fullDiffResult struct {
	Changed bool             `json:"changed" yaml:"changed"`
	Agent   agentdiff.Result `json:"agent" yaml:"agent"`
	APIM    *connectionDiff  `json:"apim,omitempty" yaml:"apim,omitempty"`
}

func cmdDiff(cmd *cobra.Command, _ []string) error {
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
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, cfg, credential, httpClient)
	if err := resolvePreparedManagedGrounding(
		commandContext(cmd),
		client,
		prepared,
	); err != nil {
		return err
	}
	remote, err := client.GetAgentContext(commandContext(cmd), cfg.Agent.Name)
	if err != nil {
		return err
	}
	agentResult, err := agentdiff.Compare(remote, prepared.Desired)
	if err != nil {
		return err
	}
	result := fullDiffResult{Changed: agentResult.Changed, Agent: agentResult}
	if prepared.APIMEnabled {
		if !hasProjectCoordinates(cfg.Project) {
			return errs.Config("APIM connection diff requires complete project resource coordinates")
		}
		state, err := connection.GetAPIMConnectionContext(
			commandContext(cmd), &cfg.Apim, &cfg.Project, prepared.ConnectionName, credential, httpClient,
		)
		if err != nil {
			return err
		}
		apimDiff, err := compareAPIMConnection(state, &cfg.Apim, prepared.ConnectionName, prepared.APIMModels)
		if err != nil {
			return err
		}
		result.APIM = &apimDiff
		result.Changed = result.Changed || apimDiff.Changed
	}

	var text strings.Builder
	if !result.Changed {
		fmt.Fprintf(&text, "no managed changes: agent=%s version=%s", cfg.Agent.Name, agentResult.CurrentVersion)
	} else {
		fmt.Fprintf(&text, "managed changes detected: agent=%s", cfg.Agent.Name)
		for _, difference := range agentResult.Differences {
			fmt.Fprintf(&text, "\n  %s: %v -> %v", difference.Path, difference.Current, difference.Desired)
		}
		if result.APIM != nil && result.APIM.Changed {
			fmt.Fprintf(&text, "\n  apim: %s", strings.Join(result.APIM.Fields, ", "))
		}
	}
	return printResult(cmd, result, text.String())
}

func compareAPIMConnection(
	state connection.State,
	apim *config.ApimSpec,
	name string,
	models []string,
) (connectionDiff, error) {
	result := connectionDiff{
		Enabled: true,
		Exists:  state.Exists,
		Status:  safeAPIMStatus(state, name),
	}
	if !state.Exists {
		result.Changed = true
		result.Fields = []string{"connection is missing"}
		return result, nil
	}
	// A rejected or unresolved target must surface as an error: discarding it
	// previously produced a nil body and a panic on the type assertion below.
	desiredBody, err := connection.BuildConnectionBody(apim, models, "<redacted>")
	if err != nil {
		return connectionDiff{}, err
	}
	desired, ok := desiredBody["properties"].(map[string]interface{})
	if !ok {
		return connectionDiff{}, errs.Config("APIM connection request could not be constructed for comparison")
	}
	delete(desired, "credentials")
	current := map[string]interface{}{}
	for key := range desired {
		if value, ok := state.Properties[key]; ok {
			current[key] = value
		}
	}
	desired = normalizeJSONMap(desired)
	current = normalizeJSONMap(current)
	for key, desiredValue := range desired {
		if !reflect.DeepEqual(current[key], desiredValue) {
			result.Fields = append(result.Fields, key)
		}
	}
	result.Changed = len(result.Fields) > 0
	return result, nil
}

func normalizeJSONMap(value map[string]interface{}) map[string]interface{} {
	data, _ := json.Marshal(value)
	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)
	return result
}

type smokeResult struct {
	Agent      string `json:"agent" yaml:"agent"`
	ResponseID string `json:"responseId" yaml:"responseId"`
	OutputText string `json:"outputText" yaml:"outputText"`
}

func cmdSmoke(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	credential, err := newCredential(cmd, cfg.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, cfg, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, cfg, credential, httpClient)
	structuredInputs, err := loadStructuredInputValues(cmd, cfg.Agent.StructuredInputs)
	if err != nil {
		return err
	}
	invocation, err := client.InvokeEndpointWithOptionsContext(
		commandContext(cmd),
		cfg.Agent.Name,
		getFlag(cmd, "prompt"),
		foundry.InvocationOptions{
			StructuredInputs: structuredInputs,
			MemoryUserID:     getFlag(cmd, "memory-user-id"),
		},
	)
	if err != nil {
		return err
	}
	invocation, err = resolveMCPApprovals(
		cmd,
		invocation,
		func(
			ctx context.Context,
			previousResponseID string,
			decisions []foundry.MCPApprovalDecision,
		) (*foundry.InvocationResult, error) {
			return client.ContinueEndpointWithApprovalsContext(
				ctx,
				cfg.Agent.Name,
				previousResponseID,
				decisions,
				foundry.InvocationOptions{MemoryUserID: getFlag(cmd, "memory-user-id")},
			)
		},
	)
	if err != nil {
		return err
	}
	result := smokeResult{
		Agent:      cfg.Agent.Name,
		ResponseID: invocation.ID,
		OutputText: invocation.OutputText,
	}
	return printResult(cmd, result, fmt.Sprintf("smoke test passed: agent=%s response=%q", result.Agent, result.OutputText))
}

func loadStructuredInputValues(
	cmd *cobra.Command,
	definitions map[string]interface{},
) (map[string]interface{}, error) {
	path := strings.TrimSpace(getFlag(cmd, "structured-inputs-file"))
	if path == "" {
		if err := config.ValidateStructuredInputValues(definitions, nil); err != nil {
			return nil, err
		}
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, errs.Config("failed to inspect structured inputs file %q: %v", path, err)
	}
	const maxStructuredInputsBytes = 1 << 20
	if !info.Mode().IsRegular() || info.Size() > maxStructuredInputsBytes {
		return nil, errs.Config(
			"structured inputs file %q must be a regular file no larger than %d bytes",
			path,
			maxStructuredInputsBytes,
		)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Config("failed to read structured inputs file %q: %v", path, err)
	}
	var values map[string]interface{}
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, errs.Config("structured inputs file %q must contain one JSON object: %v", path, err)
	}
	if values == nil {
		return nil, errs.Config("structured inputs file %q must contain one JSON object", path)
	}
	if err := config.ValidateStructuredInputValues(definitions, values); err != nil {
		return nil, err
	}
	return values, nil
}
