package main

import (
	"fmt"
	"strings"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hosted"

	"github.com/spf13/cobra"
)

type hostedShowResult struct {
	Preview       bool                  `json:"preview" yaml:"preview"`
	Cloud         string                `json:"cloud" yaml:"cloud"`
	Environment   string                `json:"environment,omitempty" yaml:"environment,omitempty"`
	Workspace     string                `json:"workspace" yaml:"workspace"`
	Service       string                `json:"service" yaml:"service"`
	Deployment    hosted.Status         `json:"deployment" yaml:"deployment"`
	Agent         *foundry.Agent        `json:"agent,omitempty" yaml:"agent,omitempty"`
	Version       *foundry.AgentVersion `json:"version,omitempty" yaml:"version,omitempty"`
	ProjectTarget string                `json:"projectEndpoint" yaml:"projectEndpoint"`
}

type hostedVersionsResult struct {
	Preview       bool                   `json:"preview" yaml:"preview"`
	Cloud         string                 `json:"cloud" yaml:"cloud"`
	Environment   string                 `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName     string                 `json:"agentName" yaml:"agentName"`
	IncludeDrafts bool                   `json:"includeDrafts" yaml:"includeDrafts"`
	Versions      []foundry.AgentVersion `json:"versions" yaml:"versions"`
}

type hostedDiffResult struct {
	Preview         bool                      `json:"preview" yaml:"preview"`
	Cloud           string                    `json:"cloud" yaml:"cloud"`
	Environment     string                    `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName       string                    `json:"agentName" yaml:"agentName"`
	Changed         bool                      `json:"changed" yaml:"changed"`
	Reasons         []string                  `json:"reasons,omitempty" yaml:"reasons,omitempty"`
	Snapshot        hosted.DeploymentSnapshot `json:"snapshot" yaml:"snapshot"`
	RemoteVersion   string                    `json:"remoteVersion,omitempty" yaml:"remoteVersion,omitempty"`
	ExpectedVersion string                    `json:"expectedVersion,omitempty" yaml:"expectedVersion,omitempty"`
	Receipt         string                    `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

type hostedDiagnosticVersion struct {
	Version string      `json:"version" yaml:"version"`
	Status  string      `json:"status" yaml:"status"`
	Draft   bool        `json:"draft" yaml:"draft"`
	Error   interface{} `json:"error,omitempty" yaml:"error,omitempty"`
}

type hostedDiagnoseResult struct {
	Preview       bool                       `json:"preview" yaml:"preview"`
	Cloud         string                     `json:"cloud" yaml:"cloud"`
	Environment   string                     `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName     string                     `json:"agentName" yaml:"agentName"`
	State         string                     `json:"state" yaml:"state"`
	LatestVersion string                     `json:"latestVersion,omitempty" yaml:"latestVersion,omitempty"`
	LatestStatus  string                     `json:"latestStatus,omitempty" yaml:"latestStatus,omitempty"`
	Selector      foundry.SelectorResolution `json:"selector" yaml:"selector"`
	Tooling       hosted.PreflightResult     `json:"tooling" yaml:"tooling"`
	Issues        []string                   `json:"issues,omitempty" yaml:"issues,omitempty"`
	Warnings      []string                   `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Failed        []hostedDiagnosticVersion  `json:"failedVersions,omitempty" yaml:"failedVersions,omitempty"`
}

func cmdHostedShow(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	result := hostedShowResult{
		Preview:       true,
		Cloud:         runtime.Profile.Name,
		Environment:   runtime.Environment,
		Workspace:     runtime.Workspace.Root,
		Service:       runtime.Workspace.Selected.ServiceName,
		Deployment:    runtime.Deployment,
		ProjectTarget: runtime.ProjectEndpoint,
	}
	version := getFlag(cmd, "agent-version")
	if version == "" {
		sanitized := sanitizeHostedAgent(runtime.Agent)
		result.Agent = &sanitized
		return printResult(cmd, result, fmt.Sprintf(
			"Hosted Agent: %s",
			hostedRuntimeSummary(runtime),
		))
	}
	found, err := requireHostedVersion(runtime.Context, runtime, version)
	if err != nil {
		return err
	}
	sanitized := sanitizeHostedVersion(*found)
	result.Version = &sanitized
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent version: name=%s version=%s status=%s draft=%t",
		runtime.Agent.Name,
		found.Version,
		found.Status,
		found.Draft,
	))
}

func cmdHostedVersions(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	includeDrafts := getBoolFlag(cmd, "include-drafts")
	versions, err := runtime.Client.ListVersionDetailsWithDraftsContext(
		runtime.Context,
		runtime.Agent.Name,
		includeDrafts,
	)
	if err != nil {
		return err
	}
	sanitized := make([]foundry.AgentVersion, len(versions))
	for i := range versions {
		sanitized[i] = sanitizeHostedVersion(versions[i])
		kind, _ := versions[i].Definition["kind"].(string)
		if !strings.EqualFold(kind, "hosted") {
			return errs.Config(
				"agent %q version %s is kind %q, not hosted",
				runtime.Agent.Name,
				versions[i].Version,
				kind,
			)
		}
	}
	result := hostedVersionsResult{
		Preview:       true,
		Cloud:         runtime.Profile.Name,
		Environment:   runtime.Environment,
		AgentName:     runtime.Agent.Name,
		IncludeDrafts: includeDrafts,
		Versions:      sanitized,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent versions: name=%s count=%d include-drafts=%t",
		runtime.Agent.Name,
		len(versions),
		includeDrafts,
	))
}

func cmdHostedDiff(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	snapshot, err := hosted.ComputeDeploymentSnapshot(runtime.Workspace, runtime.Environment)
	if err != nil {
		return err
	}
	comparison, err := compareHostedDeployment(runtime, snapshot)
	if err != nil {
		return err
	}
	result := hostedDiffResult{
		Preview:         true,
		Cloud:           runtime.Profile.Name,
		Environment:     runtime.Environment,
		AgentName:       runtime.Agent.Name,
		Changed:         comparison.Changed,
		Reasons:         comparison.Reasons,
		Snapshot:        snapshot,
		RemoteVersion:   comparison.RemoteVersion,
		ExpectedVersion: comparison.ExpectedVersion,
		Receipt:         comparison.ReceiptPath,
	}
	text := fmt.Sprintf(
		"Hosted deployment unchanged: name=%s version=%s hash=%s",
		runtime.Agent.Name,
		comparison.RemoteVersion,
		snapshot.Hash,
	)
	if comparison.Changed {
		text = fmt.Sprintf(
			"Hosted deployment changed: name=%s reasons=%s",
			runtime.Agent.Name,
			strings.Join(comparison.Reasons, "; "),
		)
	}
	return printResult(cmd, result, text)
}

func cmdHostedDiagnose(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	versions, err := runtime.Client.ListVersionDetailsWithDraftsContext(
		runtime.Context,
		runtime.Agent.Name,
		true,
	)
	if err != nil {
		return err
	}
	selector := runtime.Agent.VersionSelectorResolution()
	result := hostedDiagnoseResult{
		Preview:       true,
		Cloud:         runtime.Profile.Name,
		Environment:   runtime.Environment,
		AgentName:     runtime.Agent.Name,
		State:         runtime.Agent.State,
		LatestVersion: runtime.Agent.Versions.Latest.Version,
		LatestStatus:  runtime.Agent.Versions.Latest.Status,
		Selector:      selector,
		Tooling:       runtime.Tooling,
	}
	if strings.EqualFold(runtime.Agent.State, "disabled") {
		result.Warnings = append(result.Warnings, "the Hosted Agent endpoint is disabled")
	}
	if selector.IsMalformed() {
		result.Issues = append(result.Issues, "endpoint routing is malformed: "+selector.Problem)
	}
	byVersion := make(map[string]foundry.AgentVersion, len(versions))
	for _, version := range versions {
		byVersion[version.Version] = version
		if strings.EqualFold(version.Status, "failed") {
			result.Failed = append(result.Failed, hostedDiagnosticVersion{
				Version: version.Version,
				Status:  version.Status,
				Draft:   version.Draft,
				Error:   version.Error,
			})
		}
	}
	for _, active := range selector.ActiveVersions {
		version, ok := byVersion[active]
		if !ok {
			result.Issues = append(result.Issues, fmt.Sprintf(
				"endpoint routing references missing version %s",
				active,
			))
			continue
		}
		if version.Draft {
			result.Issues = append(result.Issues, fmt.Sprintf(
				"endpoint routing references draft version %s",
				active,
			))
		}
		if !strings.EqualFold(version.Status, "active") {
			result.Issues = append(result.Issues, fmt.Sprintf(
				"routed version %s is %s",
				active,
				version.Status,
			))
		}
	}
	if len(result.Failed) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf(
			"%d Hosted Agent version(s) are failed",
			len(result.Failed),
		))
	}
	text := fmt.Sprintf(
		"Hosted diagnostics: name=%s issues=%d warnings=%d failed-versions=%d",
		runtime.Agent.Name,
		len(result.Issues),
		len(result.Warnings),
		len(result.Failed),
	)
	return printResult(cmd, result, text)
}
