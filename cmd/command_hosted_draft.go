package main

import (
	"fmt"
	"strings"
	"time"

	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/hosted"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type hostedDraftResult struct {
	Preview     bool                      `json:"preview" yaml:"preview"`
	Cloud       string                    `json:"cloud" yaml:"cloud"`
	Environment string                    `json:"environment,omitempty" yaml:"environment,omitempty"`
	AgentName   string                    `json:"agentName" yaml:"agentName"`
	Version     foundry.AgentVersion      `json:"version" yaml:"version"`
	Snapshot    hosted.DeploymentSnapshot `json:"snapshot" yaml:"snapshot"`
	ArchiveHash string                    `json:"archiveHash,omitempty" yaml:"archiveHash,omitempty"`
	ArchiveSize int64                     `json:"archiveSize,omitempty" yaml:"archiveSize,omitempty"`
	Receipt     string                    `json:"receipt" yaml:"receipt"`
}

func cmdHostedDraftDeploy(cmd *cobra.Command, _ []string) error {
	runtime, cancel, err := resolveHostedRESTRuntime(cmd)
	if err != nil {
		return err
	}
	defer cancel()
	if err := requireHostedGuardrailIntent(cmd, runtime.Workspace); err != nil {
		return err
	}
	if runtime.Workspace.Selected.Mode == hosted.DeploymentModeDocker {
		return errs.Config(
			"Hosted draft deployment cannot build a Docker context; use codeConfiguration or a prebuilt image",
		)
	}
	snapshot, err := hosted.ComputeDeploymentSnapshot(runtime.Workspace, runtime.Environment)
	if err != nil {
		return err
	}
	store, err := newHostedOperationStore(cmd, runtime, "hosted-draft-deploy", snapshot.Hash)
	if err != nil {
		return err
	}
	recorder := func(command hosted.CommandRecord) error {
		store.Receipt.Commands = append(store.Receipt.Commands, receipt.CommandRecord{
			Phase:      command.Phase,
			Executable: command.Executable,
			Args:       append([]string(nil), command.Args...),
			Directory:  command.Directory,
			ExitCode:   command.ExitCode,
			Duration:   command.Duration,
		})
		return store.Save()
	}
	raiPolicyID, err := validateHostedRAIPolicy(
		runtime.Context,
		cmd,
		runtime.Profile,
		runtime.Runner,
		runtime.AZDPath,
		runtime.Workspace,
		runtime.Environment,
		runtime.ProjectEndpoint,
		recorder,
	)
	if err != nil {
		_ = store.AddStep(
			"hosted-rai-policy",
			"failed",
			"the configured Hosted Agent RAI policy could not be verified",
		)
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	policyDetail := "the configured account-level RAI policy exists"
	if raiPolicyID == "" {
		policyDetail = "agent-level RAI policy explicitly omitted with --no-guardrail"
	}
	if err := store.AddStep("hosted-rai-policy", "succeeded", policyDetail); err != nil {
		return err
	}
	environment, err := hosted.ResolveServiceEnvironment(
		runtime.Context,
		runtime.Runner,
		runtime.AZDPath,
		runtime.Workspace,
		runtime.Environment,
		recorder,
	)
	if err != nil {
		classified := hostedCommandError(err)
		_ = store.Complete("failed", classified)
		return releaseFailure(store.Path, classified)
	}
	for _, value := range environment {
		store.RegisterSecret(value)
	}
	definition := hostedDraftDefinition(runtime.Workspace, environment, raiPolicyID)
	description := getFlag(cmd, "description")
	customMetadata := custommetadata.MergeHosted(
		runtime.Workspace.Selected.Metadata,
		commandMetadata(cmd),
	)
	var created *foundry.AgentVersion
	var archive hosted.CodeArchive
	switch runtime.Workspace.Selected.Mode {
	case hosted.DeploymentModeCode:
		archive, err = hosted.BuildCodeArchive(runtime.Workspace)
		if err != nil {
			_ = store.Complete("failed", err)
			return releaseFailure(store.Path, err)
		}
		defer archive.Remove()
		metadata := map[string]interface{}{
			"draft":      true,
			"definition": definition,
		}
		if description != "" {
			metadata["description"] = description
		}
		if len(customMetadata) > 0 {
			metadata["metadata"] = customMetadata
		}
		created, err = runtime.Client.CreateHostedCodeVersionContext(
			runtime.Context,
			runtime.Agent.Name,
			metadata,
			archive.Path,
			archive.SHA256,
		)
	case hosted.DeploymentModeImage:
		created, err = runtime.Client.CreateHostedVersionContext(
			runtime.Context,
			runtime.Agent.Name,
			description,
			definition,
			true,
			customMetadata,
		)
	default:
		err = errs.Config("unsupported Hosted draft deployment mode %q", runtime.Workspace.Selected.Mode)
	}
	if err != nil {
		status := "failed"
		if errs.IsAmbiguousMutation(err) {
			status = "unknown"
		}
		_ = store.Complete(status, err)
		return releaseFailure(store.Path, err)
	}
	store.Receipt.Agent.CreatedVersion = created.Version
	store.Receipt.Agent.Changed = true
	if !created.Draft {
		partial := errs.Foundry(
			"Foundry created regular Hosted Agent version %s instead of a draft; draft creation may not be enabled for this subscription",
			created.Version,
		)
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "hosted-agent-version",
			Name:           created.Version,
			Action:         "create-draft",
			Status:         "created-as-regular-version",
			Reconciliation: "Inspect the created version before routing or deleting it.",
		})
		_ = store.Complete("failed-partial", partial)
		return releaseFailure(store.Path, partial)
	}
	verified, err := waitHostedVersion(
		runtime,
		created.Version,
		5*time.Second,
	)
	if err != nil {
		_ = store.Complete("unknown", err)
		return releaseFailure(store.Path, err)
	}
	if !verified.Draft {
		partial := errs.Foundry(
			"Hosted Agent version %s no longer verifies as a draft",
			verified.Version,
		)
		_ = store.Complete("unknown", partial)
		return releaseFailure(store.Path, partial)
	}
	if actualPolicyID := hostedDraftRAIPolicyID(verified.Definition); !strings.EqualFold(actualPolicyID, raiPolicyID) {
		partial := errs.Foundry(
			"Hosted Agent draft %s RAI policy verification mismatch: expected %q, got %q",
			verified.Version,
			raiPolicyID,
			actualPolicyID,
		)
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "hosted-agent-draft",
			Name:           verified.Version,
			Action:         "create",
			Status:         "rai-policy-mismatch",
			Reconciliation: "Inspect or delete the draft before using it; the requested guardrail was not verified on the created version.",
		})
		_ = store.Complete("failed-partial", partial)
		return releaseFailure(store.Path, partial)
	}
	if !strings.EqualFold(verified.Status, "active") {
		failed := errs.Foundry(
			"Hosted Agent draft %s reached status %s: %v",
			verified.Version,
			verified.Status,
			verified.Error,
		)
		_ = store.Complete("failed", failed)
		return releaseFailure(store.Path, failed)
	}
	_ = store.AddResource(receipt.ResourceChange{
		Kind:   "hosted-agent-draft",
		Name:   verified.Version,
		Action: "create",
		Status: verified.Status,
	})
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	result := hostedDraftResult{
		Preview:     true,
		Cloud:       runtime.Profile.Name,
		Environment: runtime.Environment,
		AgentName:   runtime.Agent.Name,
		Version:     sanitizeHostedVersion(*verified),
		Snapshot:    snapshot,
		ArchiveHash: archive.SHA256,
		ArchiveSize: archive.Size,
		Receipt:     store.Path,
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Hosted Agent draft created: name=%s version=%s status=%s",
		runtime.Agent.Name,
		verified.Version,
		verified.Status,
	))
}

func hostedDraftDefinition(
	workspace hosted.Workspace,
	environment map[string]string,
	raiPolicyID string,
) map[string]interface{} {
	protocols := make([]interface{}, len(workspace.Selected.Protocols))
	for i, protocol := range workspace.Selected.Protocols {
		protocols[i] = map[string]interface{}{
			"protocol": protocol.Name,
			"version":  protocol.Version,
		}
	}
	definition := map[string]interface{}{
		"kind":              "hosted",
		"protocol_versions": protocols,
		"cpu":               workspace.Selected.Resources.CPU,
		"memory":            workspace.Selected.Resources.Memory,
	}
	if len(environment) > 0 {
		definition["environment_variables"] = environment
	}
	if raiPolicyID != "" {
		definition["rai_config"] = map[string]interface{}{
			"rai_policy_name": raiPolicyID,
		}
	}
	switch workspace.Selected.Mode {
	case hosted.DeploymentModeCode:
		definition["code_configuration"] = map[string]interface{}{
			"runtime":               workspace.Selected.Code.Runtime,
			"entry_point":           append([]string(nil), workspace.Selected.Code.EntryPoint...),
			"dependency_resolution": workspace.Selected.Code.DependencyResolution,
		}
	case hosted.DeploymentModeImage:
		definition["container_configuration"] = map[string]interface{}{
			"image": workspace.Selected.Image,
		}
	}
	return definition
}

func hostedDraftRAIPolicyID(definition map[string]interface{}) string {
	raiConfig, ok := definition["rai_config"].(map[string]interface{})
	if !ok {
		return ""
	}
	policyID, _ := raiConfig["rai_policy_name"].(string)
	return strings.TrimSpace(policyID)
}

func waitHostedVersion(
	runtime *hostedRESTRuntime,
	version string,
	interval time.Duration,
) (*foundry.AgentVersion, error) {
	for {
		found, err := runtime.Client.GetAgentVersionContext(
			runtime.Context,
			runtime.Agent.Name,
			version,
		)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return nil, errs.NotFound(
				"Hosted Agent %q version %s disappeared during provisioning",
				runtime.Agent.Name,
				version,
			)
		}
		switch strings.ToLower(strings.TrimSpace(found.Status)) {
		case "active", "failed":
			return found, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-runtime.Context.Done():
			timer.Stop()
			return nil, runtime.Context.Err()
		case <-timer.C:
		}
	}
}

func cmdHostedInit(cmd *cobra.Command, _ []string) error {
	result, err := createHostedScaffold(cmd)
	if err != nil {
		return err
	}
	text := fmt.Sprintf(
		"Hosted Agent workspace scaffolded: root=%s name=%s protocol=%s",
		result.Root,
		result.AgentName,
		result.Protocol,
	)
	if result.BingGroundingConnection != "" {
		text += "\n  Bing Grounding connection: " + result.BingGroundingConnection
	}
	if result.BingCustomSearchConnection != "" {
		text += "\n  Bing Custom Search connection: " + result.BingCustomSearchConnection
		text += "\n  Bing Custom Search instance: " + result.BingCustomSearchInstance
	}
	if result.ToolboxName != "" {
		text += "\n  Toolbox: " + result.ToolboxName
	}
	return printResult(cmd, result, text)
}

func createHostedScaffold(cmd *cobra.Command) (hosted.ScaffoldResult, error) {
	return hosted.Scaffold(hosted.ScaffoldOptions{
		Destination:                getFlag(cmd, "destination"),
		AgentName:                  getFlag(cmd, "name"),
		Protocol:                   getFlag(cmd, "protocol"),
		GuardrailPolicyID:          getFlag(cmd, "guardrail-policy-id"),
		NoGuardrail:                getBoolFlag(cmd, "no-guardrail"),
		BingGroundingConnection:    getFlag(cmd, "bing-grounding-connection"),
		BingCustomSearchConnection: getFlag(cmd, "bing-custom-search-connection"),
		BingCustomSearchInstance:   getFlag(cmd, "bing-custom-search-instance"),
		ToolboxName:                getFlag(cmd, "toolbox-name"),
		Metadata:                   commandMetadata(cmd),
	})
}
