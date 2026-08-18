package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/hostedautopilot"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type autopilotInfoResult struct {
	Experimental         bool     `json:"experimental" yaml:"experimental"`
	AgentType            string   `json:"agentType" yaml:"agentType"`
	Repository           string   `json:"repository" yaml:"repository"`
	ReviewedCommit       string   `json:"reviewedCommit" yaml:"reviewedCommit"`
	SamplePath           string   `json:"samplePath" yaml:"samplePath"`
	RequiredExecutables  []string `json:"requiredExecutables" yaml:"requiredExecutables"`
	ExternalManualSteps  []string `json:"externalManualSteps" yaml:"externalManualSteps"`
	PromptAgentSupported bool     `json:"promptAgentSupported" yaml:"promptAgentSupported"`
}

type autopilotPreflightResult struct {
	Cloud               string            `json:"cloud" yaml:"cloud"`
	Region              string            `json:"region" yaml:"region"`
	ApprovedCommit      string            `json:"approvedCommit" yaml:"approvedCommit"`
	ResolvedExecutables map[string]string `json:"resolvedExecutables" yaml:"resolvedExecutables"`
}

type autopilotDeployResult struct {
	Status                       string                          `json:"status" yaml:"status"`
	Repository                   string                          `json:"repository" yaml:"repository"`
	Commit                       string                          `json:"commit" yaml:"commit"`
	SamplePath                   string                          `json:"samplePath" yaml:"samplePath"`
	Commands                     []hostedautopilot.CommandRecord `json:"commands" yaml:"commands"`
	AdminApprovalRemainsExternal bool                            `json:"adminApprovalRemainsExternal" yaml:"adminApprovalRemainsExternal"`
	AdminApproval                string                          `json:"adminApproval" yaml:"adminApproval"`
	Receipt                      string                          `json:"receipt" yaml:"receipt"`
}

func cmdAutopilotInfo(cmd *cobra.Command, _ []string) error {
	result := autopilotInfoResult{
		Experimental:         true,
		AgentType:            "hosted",
		Repository:           hostedautopilot.OfficialRepositoryURL,
		ReviewedCommit:       hostedautopilot.ReviewedSampleCommit,
		SamplePath:           hostedautopilot.SamplePath,
		RequiredExecutables:  hostedautopilot.RequiredExecutables(),
		PromptAgentSupported: false,
		ExternalManualSteps: []string{
			"Approve and activate the AgentIdentityBlueprint in the Microsoft 365 admin center.",
			"Configure the approved blueprint and Bot ID in the Teams Developer Portal.",
			"Create agent instances from the approved blueprint in Microsoft Teams.",
		},
	}
	return printResult(cmd, result, fmt.Sprintf(
		"experimental Hosted-agent Autopilot wrapper: commit=%s\n  supported cloud: AzureCloud\n  prompt agents: unsupported\n  admin and Teams configuration remain manual",
		result.ReviewedCommit,
	))
}

func cmdAutopilotPreflight(cmd *cobra.Command, _ []string) error {
	options, err := autopilotPreflightOptions(cmd)
	if err != nil {
		return err
	}
	result, err := hostedautopilot.Preflight(options)
	if err != nil {
		return autopilotCommandError(err)
	}
	output := autopilotPreflightResult{
		Cloud:               string(result.Cloud),
		Region:              result.Region,
		ApprovedCommit:      result.ApprovedCommit,
		ResolvedExecutables: result.ResolvedExecutables,
	}
	return printResult(cmd, output, fmt.Sprintf(
		"experimental Autopilot preflight passed: cloud=%s region=%s commit=%s",
		output.Cloud,
		output.Region,
		output.ApprovedCommit,
	))
}

func cmdAutopilotDeploy(cmd *cobra.Command, _ []string) error {
	workDir, err := absoluteAutopilotPath(getFlag(cmd, "work-dir"))
	if err != nil {
		return err
	}
	options, err := autopilotPreflightOptions(cmd)
	if err != nil {
		return err
	}
	receiptPath, err := autopilotReceiptPath(cmd, workDir)
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		receiptPath,
		"experimental-hosted-autopilot",
		string(options.Cloud),
		receipt.ManifestReference{},
		receipt.ResourceReference{},
		"hosted-autopilot-sample",
	)
	if err != nil {
		return err
	}
	if err := store.AddStep(
		"experimental-boundary",
		"succeeded",
		"running only the pinned Microsoft-owned Hosted-agent sample; prompt-agent Autopilot is not implemented",
	); err != nil {
		return err
	}
	allowedEnv := map[string]string{}
	if environmentName := getFlag(cmd, "environment-name"); environmentName != "" {
		allowedEnv["AZURE_ENV_NAME"] = environmentName
	}
	result, err := hostedautopilot.Run(commandContext(cmd), hostedautopilot.RunRequest{
		Preflight:   options,
		IsolatedDir: workDir,
		AllowedEnv:  allowedEnv,
		Runner:      execAutopilotRunner{},
	})
	if err != nil {
		classified := autopilotCommandError(err)
		_ = store.Complete(operationFailureStatus(classified), classified)
		return releaseFailure(store.Path, classified)
	}
	now := time.Now().UTC()
	if err := store.AddExternalAction(receipt.ExternalAction{
		Kind:           "hosted-autopilot-admin-approval",
		System:         "Microsoft 365 admin center and Teams Developer Portal",
		Status:         "required",
		ResourceID:     result.CommitSHA,
		Irreversible:   false,
		Reconciliation: result.AdminApproval,
		StartedAt:      now,
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded-pending-external-actions", nil); err != nil {
		return err
	}
	output := autopilotDeployResult{
		Status:                       "succeeded-pending-external-actions",
		Repository:                   result.RepositoryURL,
		Commit:                       result.CommitSHA,
		SamplePath:                   result.SamplePath,
		Commands:                     result.Commands,
		AdminApprovalRemainsExternal: result.AdminApprovalRemainsExternal,
		AdminApproval:                result.AdminApproval,
		Receipt:                      store.Path,
	}
	return printResult(cmd, output, fmt.Sprintf(
		"experimental Hosted-agent Autopilot sample provisioned: commit=%s\n  external action: %s\n  receipt: %s",
		output.Commit,
		output.AdminApproval,
		output.Receipt,
	))
}

func autopilotPreflightOptions(cmd *cobra.Command) (hostedautopilot.PreflightOptions, error) {
	cloudName := selectedCloudName(cmd, "")
	if cloudName == "" {
		cloudName = azcloud.AzureCloud
	}
	profile, err := azcloud.Resolve(cloudName)
	if err != nil {
		return hostedautopilot.PreflightOptions{}, err
	}
	if !profile.Capabilities.HostedAutopilot {
		return hostedautopilot.PreflightOptions{}, errs.Config(
			"Hosted-agent Autopilot is unavailable in %s; no commercial-cloud fallback is allowed",
			profile.Name,
		)
	}
	return hostedautopilot.PreflightOptions{
		Cloud:               hostedautopilot.Cloud(profile.Name),
		AcceptPreview:       getBoolFlag(cmd, "accept-preview"),
		ApproveSampleCommit: getFlag(cmd, "approve-sample-commit"),
		Region:              getFlag(cmd, "region"),
		AllowedRegions:      getStringArrayFlag(cmd, "allowed-region"),
	}, nil
}

func autopilotCommandError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, hostedautopilot.ErrCommitNotApproved),
		errors.Is(err, hostedautopilot.ErrCheckedOutCommit),
		errors.Is(err, hostedautopilot.ErrSamplePathMissing),
		errors.Is(err, hostedautopilot.ErrIsolatedDirectory),
		errors.Is(err, hostedautopilot.ErrInvalidEnvironment):
		return errs.Security("%v", err)
	case errors.Is(err, hostedautopilot.ErrPreviewNotAccepted),
		errors.Is(err, hostedautopilot.ErrUnsupportedCloud),
		errors.Is(err, hostedautopilot.ErrRegionNotAllowed),
		errors.Is(err, hostedautopilot.ErrAllowedRegionsEmpty),
		errors.Is(err, hostedautopilot.ErrMissingExecutable),
		errors.Is(err, hostedautopilot.ErrLookPathUnavailable):
		return errs.Config("%v", err)
	case errors.Is(err, hostedautopilot.ErrCommandFailed):
		return errs.ToolBuild("%v", err)
	default:
		return err
	}
}

func addAutopilotFlags(command *cobra.Command) {
	command.Flags().Bool("accept-preview", false, "Explicitly accept the unsupported Hosted-agent Autopilot preview.")
	command.Flags().String(
		"approve-sample-commit",
		"",
		"Exact reviewed Microsoft sample commit SHA approved by the operator.",
	)
	command.Flags().String("region", "", "Azure region selected for the Hosted-agent sample.")
	command.Flags().StringArray(
		"allowed-region",
		nil,
		"Caller-approved Hosted-agent region; repeat for multiple approved regions.",
	)
	requireFlags(command, "approve-sample-commit", "region")
}

func absoluteAutopilotPath(path string) (string, error) {
	if path == "" {
		return "", errs.Config("--work-dir is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errs.Config("failed to resolve --work-dir: %v", err)
	}
	return filepath.Clean(absolute), nil
}

func autopilotReceiptPath(cmd *cobra.Command, workDir string) (string, error) {
	if path := getFlag(cmd, "receipt"); path != "" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", errs.Config("failed to resolve --receipt path: %v", err)
		}
		return absolute, nil
	}
	return receipt.OperationPath(
		filepath.Join(filepath.Dir(workDir), "autopilot.yaml"),
		"experimental-hosted-autopilot",
		"sample",
		time.Now(),
	), nil
}

type execAutopilotRunner struct{}

func (execAutopilotRunner) Run(
	ctx context.Context,
	command hostedautopilot.Command,
) (hostedautopilot.CommandExecution, error) {
	process := exec.CommandContext(ctx, command.Executable, command.Args...)
	process.Dir = command.Dir
	if len(command.Environment) > 0 {
		process.Env = mergeEnvironment(os.Environ(), command.Environment)
	}
	var stdout bytes.Buffer
	if command.OutputPolicy == hostedautopilot.CaptureStdout {
		process.Stdout = &stdout
	} else {
		process.Stdout = io.Discard
	}
	process.Stderr = io.Discard
	err := process.Run()
	exitCode := -1
	if process.ProcessState != nil {
		exitCode = process.ProcessState.ExitCode()
	}
	execution := hostedautopilot.CommandExecution{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
	}
	if err != nil {
		return execution, fmt.Errorf("%s failed with exit code %d", filepath.Base(command.Executable), execution.ExitCode)
	}
	return execution, nil
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	for _, entry := range base {
		key, value, found := strings.Cut(entry, "=")
		if found {
			values[strings.ToUpper(key)] = key + "=" + value
		}
	}
	for key, value := range overrides {
		values[strings.ToUpper(key)] = key + "=" + value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}
