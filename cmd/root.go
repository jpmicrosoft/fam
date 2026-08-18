package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"

	"github.com/spf13/cobra"
)

func rootCmd() *cobra.Command {
	return rootCmdFor("foundry-agent-manager")
}

func rootCmdFor(executableName string) *cobra.Command {
	root := &cobra.Command{
		Use:   executableName,
		Short: "Deploy and manage Microsoft Foundry Prompt and Hosted Agents.",
		Long: `Deploy and manage Microsoft Foundry Prompt and Hosted Agents.

Getting started:
  quickstart   Create starter files and optionally bootstrap a Hosted azd environment
  doctor       Check your setup and diagnose problems
  version      Show installed version

Commands are grouped into resource namespaces. Use --help on any namespace or command for details.
All commands default to text output; use --output json or --output yaml for automation.`,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := outputFormat(cmd); err != nil {
				return errs.Config("%v", err)
			}
			if err := validateProgressSetting(getFlag(cmd, "progress")); err != nil {
				return errs.Config("%v", err)
			}
			metadata, err := metadataFromFlags(cmd)
			if err != nil {
				return err
			}
			setCommandMetadata(cmd, metadata)
			if cloudName := getFlag(cmd, "cloud"); cloudName != "" {
				if _, err := azcloud.Resolve(cloudName); err != nil {
					return err
				}
			}
			profile, err := azcloud.Resolve(selectedCloudName(cmd, azcloud.AzureCloud))
			if err != nil {
				return err
			}
			if _, err := resolveReceiptLog(cmd, profile); err != nil {
				return err
			}
			if getDurationFlag(cmd, "request-timeout") <= 0 {
				return errs.Config("--request-timeout must be greater than zero")
			}
			if getIntFlag(cmd, "retry-count") < 0 {
				return errs.Config("--retry-count must be non-negative")
			}
			if getDurationFlag(cmd, "retry-delay") <= 0 {
				return errs.Config("--retry-delay must be greater than zero")
			}
			debugf(
				cmd,
				"command=%s build=%q output=%s progress=%s cloud=%s request-timeout=%s retries=%d retry-delay=%s",
				cmd.CommandPath(),
				buildMetadata(),
				getFlag(cmd, "output"),
				getFlag(cmd, "progress"),
				selectedCloudName(cmd, "AzureCloud"),
				getDurationFlag(cmd, "request-timeout"),
				getIntFlag(cmd, "retry-count"),
				getDurationFlag(cmd, "retry-delay"),
			)
			return nil
		},
	}
	root.Version = config.Version
	root.SetVersionTemplate(executableName + " {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return errs.Config("%v", err)
	})

	root.PersistentFlags().StringP("output", "o", "text", "Output format: text, json, or yaml.")
	root.PersistentFlags().Bool("quiet", false, "Suppress successful text output (errors are always shown).")
	root.PersistentFlags().BoolP("verbose", "v", false, "Show diagnostic progress on stderr.")
	root.PersistentFlags().Bool("debug", false, "Show detailed redacted diagnostics on stderr; implies --verbose.")
	root.PersistentFlags().String("progress", "auto", "Progress display for long-running operations: auto, plain, or off.")
	root.PersistentFlags().String("cloud", "", "Azure cloud environment (only AzureCloud is currently supported).")
	root.PersistentFlags().String("tenant-id", "", "Microsoft Entra tenant ID when the target project is in a different tenant.")
	root.PersistentFlags().Duration("request-timeout", 120*time.Second, "Maximum time for each Azure HTTP request.")
	root.PersistentFlags().Int("retry-count", 3, "Automatic retries for safe Azure requests that fail transiently.")
	root.PersistentFlags().Duration("retry-delay", time.Second, "Wait time before the first retry (increases with each attempt).")
	root.PersistentFlags().StringArray(
		"metadata",
		nil,
		"Custom non-secret metadata as key=value; repeat for multiple fields. Values override agent.metadata.",
	)
	root.PersistentFlags().String(
		"receipt-log-endpoint",
		"",
		"Azure Monitor Logs ingestion endpoint used to publish completed receipts.",
	)
	root.PersistentFlags().String(
		"receipt-log-dcr-id",
		"",
		"Immutable data collection rule ID used to publish completed receipts.",
	)
	root.PersistentFlags().String(
		"receipt-log-stream",
		"",
		"Data collection rule stream for receipts (default Custom-FoundryAgentReceipts).",
	)

	root.AddGroup(
		&cobra.Group{ID: "getting-started", Title: "Getting Started:"},
		&cobra.Group{ID: "prompt-lifecycle", Title: "Prompt Agent:"},
		&cobra.Group{ID: "models", Title: "Models:"},
		&cobra.Group{ID: "agent365", Title: "Agent 365:"},
		&cobra.Group{ID: "tools-integrations", Title: "Projects, Tools, Data, and Integrations:"},
		&cobra.Group{ID: "hosted", Title: "Hosted Agent:"},
		&cobra.Group{ID: "autopilot", Title: "Autopilot (experimental):"},
	)
	root.AddCommand(&cobra.Command{
		Use:          "version",
		Short:        "Show version and build metadata.",
		GroupID:      "getting-started",
		Args:         noArgs,
		RunE:         cmdVersion,
		SilenceUsage: true,
	})
	quickstart := &cobra.Command{
		Use:          "quickstart",
		Short:        "Create starter files and optionally bootstrap a Hosted azd environment.",
		GroupID:      "getting-started",
		Args:         noArgs,
		RunE:         cmdQuickstart,
		SilenceUsage: true,
	}
	quickstart.Flags().String("type", "", "Deployment type: prompt or hosted.")
	quickstart.Flags().String(
		"destination",
		"",
		"Prompt manifest file, or new relative Hosted workspace directory.",
	)
	quickstart.Flags().Bool("non-interactive", false, "Disable prompts and require non-default values through flags.")
	quickstart.Flags().Bool("force", false, "Overwrite an existing Prompt manifest; Hosted destinations must remain new.")
	quickstart.Flags().Bool("no-tools", false, "Omit the default Prompt code_interpreter tool.")
	quickstart.Flags().String("protocol", "responses", "Hosted protocol: responses or invocations.")
	quickstart.Flags().String("environment", "dev", "Workspace-scoped azd environment for Hosted bootstrap or generated next steps.")
	quickstart.Flags().Bool(
		"bootstrap-environment",
		false,
		"Create/configure the Hosted workspace azd environment; interactive Hosted quickstart defaults to yes.",
	)
	quickstart.Flags().String(
		"project-id",
		"",
		"Foundry project resource ID; derives endpoint and subscription for Hosted environment bootstrap.",
	)
	quickstart.Flags().String(
		"tenant-id",
		"",
		"Azure tenant UUID stored in the Hosted azd environment; does not authenticate azd.",
	)
	quickstart.Flags().Duration(
		"azd-timeout",
		time.Hour,
		"Maximum total time for Hosted environment bootstrap.",
	)
	quickstart.Flags().String(
		"bing-grounding-connection",
		"",
		"Existing Foundry project connection name for the generated Hosted workspace.",
	)
	quickstart.Flags().String(
		"bing-custom-search-connection",
		"",
		"Existing Bing Custom Search project connection for the generated Hosted workspace.",
	)
	quickstart.Flags().String(
		"bing-custom-search-instance",
		"",
		"Bing Custom Search instance paired with --bing-custom-search-connection.",
	)
	quickstart.Flags().String("toolbox-name", "", "Existing Foundry Toolbox for the generated Hosted workspace.")
	quickstart.Flags().String(
		"guardrail-policy-id",
		"",
		"Optional full RAI policy resource ID; Prompt otherwise inherits its model policy and Hosted defaults to Microsoft.DefaultV2.",
	)
	quickstart.Flags().Bool(
		"no-guardrail",
		false,
		"Hosted only: explicitly omit the agent-level RAI policy instead of using Microsoft.DefaultV2.",
	)
	addHostedAdoptionSourceFlags(quickstart)
	addOverrideFlags(quickstart)
	quickstart.Flags().Lookup("model").Usage = "Existing model deployment name for the Prompt manifest or Hosted environment bootstrap."
	quickstart.Flags().Lookup("location").Usage = "Azure location required for Hosted environment bootstrap; seeds project.location for Prompt."
	root.AddCommand(quickstart)

	doctor := &cobra.Command{
		Use:          "doctor",
		Short:        "Diagnose local setup and optional online readiness.",
		GroupID:      "getting-started",
		Args:         noArgs,
		RunE:         cmdDoctor,
		SilenceUsage: true,
	}
	doctor.Flags().StringP("manifest", "f", "", "Optional Prompt-agent manifest to diagnose.")
	doctor.Flags().String("workspace", "", "Optional Hosted Agent azure.yaml workspace to diagnose.")
	doctor.Flags().String("service", "", "Hosted service name when azure.yaml defines more than one.")
	doctor.Flags().String("environment", "", "Existing azd environment for Hosted online diagnostics.")
	doctor.Flags().Bool("online", false, "Also run read-only authentication, project, connectivity, and Hosted tooling checks.")
	doctor.Flags().Bool("check-provision", false, "Verify the Hosted azd provision command contract without provisioning.")
	doctor.Flags().Bool("fail-on-not-ready", false, "Return exit code 11 after writing the report when requested checks are not ready.")
	doctor.Flags().Duration("azd-timeout", time.Hour, "Maximum total time for Hosted online diagnostics.")
	addOverrideFlags(doctor)
	addDeploymentDependencyFlags(doctor)
	addPromptPreviewFlag(doctor)
	root.AddCommand(doctor)

	root.AddCommand(&cobra.Command{
		Use:          "tool-catalog",
		Short:        "List supported tool contracts and cloud availability (offline).",
		GroupID:      "getting-started",
		Args:         noArgs,
		RunE:         cmdToolCatalog,
		SilenceUsage: true,
	})
	receiptUpload := &cobra.Command{
		Use:          "receipt-upload",
		Short:        "Upload a preserved operation receipt to Azure Monitor Logs.",
		GroupID:      "getting-started",
		Args:         noArgs,
		RunE:         cmdReceiptUpload,
		SilenceUsage: true,
	}
	receiptUpload.Flags().String("file", "", "Manager-generated v1 or v2 receipt JSON file.")
	requireFlags(receiptUpload, "file")
	root.AddCommand(receiptUpload)
	autopilotInfo := &cobra.Command{
		Use:          "autopilot-info",
		Short:        "Show the pinned experimental Hosted-agent Autopilot boundary (offline).",
		GroupID:      "autopilot",
		Args:         noArgs,
		RunE:         cmdAutopilotInfo,
		SilenceUsage: true,
	}
	root.AddCommand(autopilotInfo)
	autopilotPreflight := &cobra.Command{
		Use:          "autopilot-preflight",
		Short:        "Validate tools, cloud, region, preview acceptance, and the reviewed sample commit.",
		GroupID:      "autopilot",
		Args:         noArgs,
		RunE:         cmdAutopilotPreflight,
		SilenceUsage: true,
	}
	addAutopilotFlags(autopilotPreflight)
	root.AddCommand(autopilotPreflight)
	autopilotDeploy := &cobra.Command{
		Use:          "autopilot-deploy",
		Short:        "Provision the pinned Hosted-agent Autopilot sample (experimental, mutating).",
		GroupID:      "autopilot",
		Args:         noArgs,
		RunE:         cmdAutopilotDeploy,
		SilenceUsage: true,
	}
	addAutopilotFlags(autopilotDeploy)
	autopilotDeploy.Flags().String("work-dir", "", "Existing empty non-symlink directory for the pinned sample checkout.")
	autopilotDeploy.Flags().String("environment-name", "", "Optional AZURE_ENV_NAME passed only to the sample azd workflow.")
	autopilotDeploy.Flags().String("receipt", "", "Operation receipt path (defaults beside the work directory).")
	requireFlags(autopilotDeploy, "work-dir")
	root.AddCommand(autopilotDeploy)

	hostedInfo := &cobra.Command{
		Use:          "hosted-info",
		Short:        "Show the verified Hosted Agent deployment boundary (offline).",
		GroupID:      "hosted",
		Args:         noArgs,
		RunE:         cmdHostedInfo,
		SilenceUsage: true,
	}
	root.AddCommand(hostedInfo)
	hostedValidate := &cobra.Command{
		Use:          "hosted-validate",
		Short:        "Validate one Hosted Agent service in an existing azure.yaml workspace.",
		Args:         noArgs,
		RunE:         cmdHostedValidate,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedValidate)
	root.AddCommand(hostedValidate)
	hostedPlan := &cobra.Command{
		Use:          "hosted-plan",
		Short:        "Plan Hosted Agent deployment without running azd.",
		Args:         noArgs,
		RunE:         cmdHostedPlan,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedPlan)
	addHostedProvisionFlags(hostedPlan)
	root.AddCommand(hostedPlan)
	hostedEnvironmentCreate := &cobra.Command{
		Use:          "hosted-environment-create",
		Short:        "Create or reuse and configure a Hosted workspace azd environment.",
		Args:         noArgs,
		RunE:         cmdHostedEnvironmentCreate,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedEnvironmentCreate)
	hostedEnvironmentCreate.Flags().String(
		"tenant-id",
		"",
		"Optional Azure tenant UUID stored as project context; does not authenticate azd.",
	)
	hostedEnvironmentCreate.Flags().String(
		"project-id",
		"",
		"Foundry project resource ID; derives endpoint and subscription.",
	)
	hostedEnvironmentCreate.Flags().String(
		"model-deployment",
		"",
		"Existing model deployment name to configure.",
	)
	hostedEnvironmentCreate.Flags().String(
		"location",
		"",
		"Azure location required by azd when deploying the Hosted Agent.",
	)
	hostedEnvironmentCreate.Flags().Duration(
		"azd-timeout",
		time.Hour,
		"Maximum total time for local azd environment creation and verification.",
	)
	requireFlags(hostedEnvironmentCreate, "environment", "project-id", "model-deployment", "location")
	root.AddCommand(hostedEnvironmentCreate)
	hostedPreflight := &cobra.Command{
		Use:          "hosted-preflight",
		Short:        "Verify the pinned azd contract, authentication, environment, and project access.",
		Args:         noArgs,
		RunE:         cmdHostedPreflight,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedPreflight)
	addHostedProvisionFlags(hostedPreflight)
	addHostedPreviewFlag(hostedPreflight)
	addHostedGuardrailOptOutFlag(hostedPreflight)
	root.AddCommand(hostedPreflight)
	hostedStatus := &cobra.Command{
		Use:          "hosted-status",
		Short:        "Show machine-readable status for a deployed Hosted Agent.",
		Args:         noArgs,
		RunE:         cmdHostedStatus,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedStatus)
	addHostedPreviewFlag(hostedStatus)
	root.AddCommand(hostedStatus)
	hostedShow := &cobra.Command{
		Use:          "hosted-show",
		Short:        "Show the deployed Hosted Agent or one immutable version.",
		Args:         noArgs,
		RunE:         cmdHostedShow,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedShow)
	addHostedPreviewFlag(hostedShow)
	hostedShow.Flags().String("agent-version", "", "Optional immutable Hosted Agent version.")
	root.AddCommand(hostedShow)
	hostedVersions := &cobra.Command{
		Use:          "hosted-versions",
		Short:        "List immutable Hosted Agent versions.",
		Args:         noArgs,
		RunE:         cmdHostedVersions,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedVersions)
	addHostedPreviewFlag(hostedVersions)
	hostedVersions.Flags().Bool("include-drafts", false, "Include preview draft versions.")
	root.AddCommand(hostedVersions)
	hostedDiff := &cobra.Command{
		Use:          "hosted-diff",
		Short:        "Compare deployable Hosted workspace state with the last verified deployment.",
		Args:         noArgs,
		RunE:         cmdHostedDiff,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDiff)
	addHostedPreviewFlag(hostedDiff)
	root.AddCommand(hostedDiff)
	hostedDiagnose := &cobra.Command{
		Use:          "hosted-diagnose",
		Short:        "Inspect Hosted tooling, versions, endpoint routing, and failures.",
		Args:         noArgs,
		RunE:         cmdHostedDiagnose,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDiagnose)
	addHostedPreviewFlag(hostedDiagnose)
	root.AddCommand(hostedDiagnose)
	hostedSmoke := &cobra.Command{
		Use:          "hosted-smoke",
		Short:        "Invoke a Hosted Agent once through responses or invocations.",
		Args:         noArgs,
		RunE:         cmdHostedSmoke,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSmoke)
	addHostedPreviewFlag(hostedSmoke)
	hostedSmoke.Flags().String("protocol", "", "Hosted protocol: responses or invocations.")
	hostedSmoke.Flags().String("prompt", "", "Natural-language input for the responses protocol.")
	hostedSmoke.Flags().String("input", "", "Raw JSON request body for the invocations protocol.")
	hostedSmoke.Flags().String("input-file", "", "Workspace-relative file containing raw invocations JSON.")
	hostedSmoke.Flags().String("session-id", "", "Optional Hosted session id to reuse.")
	hostedSmoke.Flags().String("previous-response-id", "", "Optional Responses API previous response id.")
	hostedSmoke.Flags().String("conversation-id", "", "Optional Responses API conversation id.")
	hostedSmoke.Flags().String("isolation-key", "", "Isolation key for endpoints configured with Header isolation.")
	addMCPApprovalFlags(hostedSmoke)
	root.AddCommand(hostedSmoke)
	hostedSessionCreate := &cobra.Command{
		Use:          "hosted-session-create",
		Short:        "Create a Hosted Agent session, optionally pinned to one version.",
		Args:         noArgs,
		RunE:         cmdHostedSessionCreate,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionCreate)
	addHostedPreviewFlag(hostedSessionCreate)
	hostedSessionCreate.Flags().String("agent-version", "", "Optional concrete Hosted Agent version for the session.")
	hostedSessionCreate.Flags().String("isolation-key", "", "Isolation key for endpoints configured with Header isolation.")
	addHostedReceiptFlag(hostedSessionCreate)
	root.AddCommand(hostedSessionCreate)
	hostedSessionList := &cobra.Command{
		Use:          "hosted-session-list",
		Short:        "List Hosted Agent sessions visible to the current identity.",
		Args:         noArgs,
		RunE:         cmdHostedSessionList,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionList)
	addHostedPreviewFlag(hostedSessionList)
	hostedSessionList.Flags().String("isolation-key", "", "Isolation key for endpoints configured with Header isolation.")
	root.AddCommand(hostedSessionList)
	hostedSessionShow := &cobra.Command{
		Use:          "hosted-session-show",
		Short:        "Show one Hosted Agent session.",
		Args:         noArgs,
		RunE:         cmdHostedSessionShow,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionShow)
	addHostedPreviewFlag(hostedSessionShow)
	addHostedSessionIDFlags(hostedSessionShow)
	root.AddCommand(hostedSessionShow)
	hostedSessionStop := &cobra.Command{
		Use:          "hosted-session-stop",
		Short:        "Stop Hosted session compute while preserving persisted state.",
		Args:         noArgs,
		RunE:         cmdHostedSessionStop,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionStop)
	addHostedPreviewFlag(hostedSessionStop)
	addHostedSessionIDFlags(hostedSessionStop)
	addHostedReceiptFlag(hostedSessionStop)
	root.AddCommand(hostedSessionStop)
	hostedSessionDelete := &cobra.Command{
		Use:          "hosted-session-delete",
		Short:        "Delete a Hosted Agent session and its persisted sandbox state.",
		Args:         noArgs,
		RunE:         cmdHostedSessionDelete,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionDelete)
	addHostedPreviewFlag(hostedSessionDelete)
	addHostedSessionIDFlags(hostedSessionDelete)
	addHostedConfirmationFlags(hostedSessionDelete)
	addHostedReceiptFlag(hostedSessionDelete)
	root.AddCommand(hostedSessionDelete)
	hostedSessionFileUpload := &cobra.Command{
		Use:          "hosted-session-file-upload",
		Short:        "Upload a contained local file to a Hosted session sandbox.",
		Args:         noArgs,
		RunE:         cmdHostedSessionFileUpload,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionFileUpload)
	addHostedPreviewFlag(hostedSessionFileUpload)
	addHostedSessionIDFlags(hostedSessionFileUpload)
	hostedSessionFileUpload.Flags().String("file", "", "Workspace-relative local file to upload.")
	hostedSessionFileUpload.Flags().String("remote-path", "", "Relative path in the Hosted session sandbox.")
	requireFlags(hostedSessionFileUpload, "file")
	addHostedReceiptFlag(hostedSessionFileUpload)
	root.AddCommand(hostedSessionFileUpload)
	hostedSessionFileList := &cobra.Command{
		Use:          "hosted-session-file-list",
		Short:        "List files in a Hosted session sandbox.",
		Args:         noArgs,
		RunE:         cmdHostedSessionFileList,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionFileList)
	addHostedPreviewFlag(hostedSessionFileList)
	addHostedSessionIDFlags(hostedSessionFileList)
	hostedSessionFileList.Flags().String("remote-path", ".", "Relative session directory to list.")
	root.AddCommand(hostedSessionFileList)
	hostedSessionFileDownload := &cobra.Command{
		Use:          "hosted-session-file-download",
		Short:        "Download a Hosted session file to a new contained local file.",
		Args:         noArgs,
		RunE:         cmdHostedSessionFileDownload,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionFileDownload)
	addHostedPreviewFlag(hostedSessionFileDownload)
	addHostedSessionIDFlags(hostedSessionFileDownload)
	hostedSessionFileDownload.Flags().String("remote-path", "", "Relative Hosted session file path.")
	hostedSessionFileDownload.Flags().String("output-file", "", "New workspace-relative local output file.")
	hostedSessionFileDownload.Flags().Int64("max-bytes", 50<<20, "Maximum bytes to download.")
	requireFlags(hostedSessionFileDownload, "remote-path", "output-file")
	root.AddCommand(hostedSessionFileDownload)
	hostedSessionFileDelete := &cobra.Command{
		Use:          "hosted-session-file-delete",
		Short:        "Delete one file from a Hosted session sandbox.",
		Args:         noArgs,
		RunE:         cmdHostedSessionFileDelete,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedSessionFileDelete)
	addHostedPreviewFlag(hostedSessionFileDelete)
	addHostedSessionIDFlags(hostedSessionFileDelete)
	hostedSessionFileDelete.Flags().String("remote-path", "", "Relative Hosted session file path.")
	requireFlags(hostedSessionFileDelete, "remote-path")
	addHostedConfirmationFlags(hostedSessionFileDelete)
	addHostedReceiptFlag(hostedSessionFileDelete)
	root.AddCommand(hostedSessionFileDelete)
	hostedPromote := &cobra.Command{
		Use:          "hosted-promote",
		Short:        "Route 100 percent of Hosted endpoint traffic to one version or latest.",
		Args:         noArgs,
		RunE:         cmdHostedPromote,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedPromote)
	addHostedPreviewFlag(hostedPromote)
	hostedPromote.Flags().String("agent-version", "", "Concrete active Hosted Agent version.")
	hostedPromote.Flags().Bool("latest", false, "Restore implicit routing to the latest regular version.")
	addHostedReceiptFlag(hostedPromote)
	root.AddCommand(hostedPromote)
	hostedRollback := &cobra.Command{
		Use:          "hosted-rollback",
		Short:        "Route 100 percent of Hosted endpoint traffic to a prior active version.",
		Args:         noArgs,
		RunE:         cmdHostedRollback,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedRollback)
	addHostedPreviewFlag(hostedRollback)
	hostedRollback.Flags().String("agent-version", "", "Concrete prior Hosted Agent version.")
	requireFlags(hostedRollback, "agent-version")
	addHostedReceiptFlag(hostedRollback)
	root.AddCommand(hostedRollback)
	hostedPrune := &cobra.Command{
		Use:          "hosted-prune",
		Short:        "Delete old Hosted Agent versions while protecting latest and routed versions.",
		Args:         noArgs,
		RunE:         cmdHostedPrune,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedPrune)
	addHostedPreviewFlag(hostedPrune)
	addDestructiveFlags(hostedPrune)
	hostedPrune.Flags().Int("keep", 1, "Number of newest versions to retain.")
	hostedPrune.Flags().Bool("include-drafts", false, "Include draft versions in retention planning.")
	addHostedReceiptFlag(hostedPrune)
	root.AddCommand(hostedPrune)
	hostedDeleteVersion := &cobra.Command{
		Use:          "hosted-delete-version",
		Short:        "Delete one non-latest, non-routed Hosted Agent version.",
		Args:         noArgs,
		RunE:         cmdHostedDeleteVersion,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDeleteVersion)
	addHostedPreviewFlag(hostedDeleteVersion)
	addDestructiveFlags(hostedDeleteVersion)
	hostedDeleteVersion.Flags().String("agent-version", "", "Immutable Hosted Agent version to delete.")
	requireFlags(hostedDeleteVersion, "agent-version")
	addHostedReceiptFlag(hostedDeleteVersion)
	root.AddCommand(hostedDeleteVersion)
	hostedDelete := &cobra.Command{
		Use:          "hosted-delete",
		Short:        "Permanently delete a Hosted Agent, all versions, and active sessions (destructive, irreversible).",
		Args:         noArgs,
		RunE:         cmdHostedDelete,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDelete)
	addHostedPreviewFlag(hostedDelete)
	addDestructiveFlags(hostedDelete)
	addHostedReceiptFlag(hostedDelete)
	root.AddCommand(hostedDelete)
	hostedLogs := &cobra.Command{
		Use:          "hosted-logs",
		Short:        "Read a bounded Hosted Agent session log stream.",
		Args:         noArgs,
		RunE:         cmdHostedLogs,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedLogs)
	addHostedPreviewFlag(hostedLogs)
	hostedLogs.Flags().String("agent-version", "", "Hosted Agent version that served the session.")
	hostedLogs.Flags().String("session-id", "", "Hosted Agent session id.")
	hostedLogs.Flags().Int("max-lines", 200, "Maximum SSE events to return.")
	hostedLogs.Flags().Int64("max-bytes", 1<<20, "Maximum SSE bytes to read.")
	hostedLogs.Flags().Duration("duration", 30*time.Second, "Maximum log-stream duration.")
	requireFlags(hostedLogs, "agent-version", "session-id")
	root.AddCommand(hostedLogs)
	hostedDraftDeploy := &cobra.Command{
		Use:          "hosted-draft-deploy",
		Short:        "Create and verify a preview Hosted Agent draft version.",
		Args:         noArgs,
		RunE:         cmdHostedDraftDeploy,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDraftDeploy)
	addHostedPreviewFlag(hostedDraftDeploy)
	addHostedGuardrailOptOutFlag(hostedDraftDeploy)
	hostedDraftDeploy.Flags().String("description", "", "Optional draft version description.")
	addHostedReceiptFlag(hostedDraftDeploy)
	root.AddCommand(hostedDraftDeploy)
	hostedAdopt := &cobra.Command{
		Use:          "hosted-adopt",
		Short:        "Adopt existing Python source as a new Foundry Hosted Agent workspace.",
		Args:         noArgs,
		RunE:         cmdHostedAdopt,
		SilenceUsage: true,
	}
	addHostedAdoptionSourceFlags(hostedAdopt)
	hostedAdopt.Flags().String("destination", "", "New relative Hosted workspace directory; omitted with --in-place.")
	hostedAdopt.Flags().String("name", "", "New Foundry Hosted Agent and service name; defaults from --source.")
	hostedAdopt.Flags().String("protocol", "responses", "Hosted protocol: responses or invocations.")
	hostedAdopt.Flags().String("environment", "dev", "Workspace-scoped azd environment for bootstrap or generated next steps.")
	hostedAdopt.Flags().Bool(
		"bootstrap-environment",
		false,
		"Create/configure the workspace azd environment; interactive adoption defaults to yes.",
	)
	hostedAdopt.Flags().String(
		"project-id",
		"",
		"Foundry project resource ID; derives endpoint and subscription for environment bootstrap.",
	)
	hostedAdopt.Flags().String(
		"model",
		"",
		"Existing model deployment name for environment bootstrap.",
	)
	hostedAdopt.Flags().String(
		"location",
		"",
		"Azure location required for Hosted environment bootstrap.",
	)
	hostedAdopt.Flags().String(
		"tenant-id",
		"",
		"Optional Azure tenant UUID stored as project context; does not authenticate azd.",
	)
	hostedAdopt.Flags().Duration(
		"azd-timeout",
		time.Hour,
		"Maximum total time for Hosted environment bootstrap.",
	)
	hostedAdopt.Flags().Bool("non-interactive", false, "Disable prompts and use flags or defaults.")
	hostedAdopt.Flags().String(
		"guardrail-policy-id",
		"",
		"Optional full RAI policy resource ID; defaults to Microsoft.DefaultV2.",
	)
	hostedAdopt.Flags().Bool(
		"no-guardrail",
		false,
		"Explicitly omit the agent-level RAI policy instead of using Microsoft.DefaultV2.",
	)
	root.AddCommand(hostedAdopt)
	hostedInit := &cobra.Command{
		Use:          "hosted-init",
		Short:        "Create a validated Python Hosted Agent workspace scaffold.",
		Args:         noArgs,
		RunE:         cmdHostedInit,
		SilenceUsage: true,
	}
	hostedInit.Flags().String("destination", "", "New relative directory for the Hosted Agent workspace.")
	hostedInit.Flags().String("name", "", "Hosted Agent and service name.")
	hostedInit.Flags().String("protocol", "responses", "Hosted protocol: responses or invocations.")
	hostedInit.Flags().String(
		"bing-grounding-connection",
		"",
		"Existing Foundry project connection name to wire into the Python Hosted Agent scaffold.",
	)
	hostedInit.Flags().String(
		"bing-custom-search-connection",
		"",
		"Existing Foundry project connection name for Bing Custom Search.",
	)
	hostedInit.Flags().String(
		"bing-custom-search-instance",
		"",
		"Bing Custom Search instance name paired with --bing-custom-search-connection.",
	)
	hostedInit.Flags().String(
		"toolbox-name",
		"",
		"Existing Foundry Toolbox to consume through the generated Hosted runtime.",
	)
	hostedInit.Flags().String(
		"guardrail-policy-id",
		"",
		"Optional full RAI policy resource ID; defaults to Microsoft.DefaultV2.",
	)
	hostedInit.Flags().Bool(
		"no-guardrail",
		false,
		"Explicitly omit the agent-level RAI policy instead of using Microsoft.DefaultV2.",
	)
	requireFlags(hostedInit, "destination", "name")
	root.AddCommand(hostedInit)
	hostedDisable := &cobra.Command{
		Use:          "hosted-disable",
		Short:        "Take a deployed Hosted Agent endpoint offline without deleting versions.",
		Args:         noArgs,
		RunE:         cmdHostedDisable,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDisable)
	addHostedPreviewFlag(hostedDisable)
	root.AddCommand(hostedDisable)
	hostedEnable := &cobra.Command{
		Use:          "hosted-enable",
		Short:        "Restore service for a disabled Hosted Agent endpoint.",
		Args:         noArgs,
		RunE:         cmdHostedEnable,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedEnable)
	addHostedPreviewFlag(hostedEnable)
	root.AddCommand(hostedEnable)
	hostedDeploy := &cobra.Command{
		Use:          "hosted-deploy",
		Short:        "Deploy one Hosted Agent service (mutating; provisioning is optional).",
		Args:         noArgs,
		RunE:         cmdHostedDeploy,
		SilenceUsage: true,
	}
	addHostedWorkspaceFlags(hostedDeploy)
	addHostedProvisionFlags(hostedDeploy)
	addHostedPreviewFlag(hostedDeploy)
	addHostedGuardrailOptOutFlag(hostedDeploy)
	hostedDeploy.Flags().String("receipt", "", "Operation receipt path (defaults beside azure.yaml).")
	hostedDeploy.Flags().Bool(
		"if-changed",
		false,
		"Skip azd deploy only when the deployable snapshot and verified remote latest version match a successful receipt.",
	)
	root.AddCommand(hostedDeploy)

	commands := []*cobra.Command{
		newManifestCommand("init", "Write a starter manifest to a new file (offline).", cmdInit),
		newManifestCommand("validate", "Validate a manifest and all local references (offline).", cmdValidate),
		newManifestCommand("plan", "Resolve configuration and print intended actions (offline).", cmdPlan),
		newManifestCommand("project-create", "Create or reconcile the manifest's Foundry project.", cmdProjectCreate),
		newManifestCommand("model-deployment-list", "List Foundry model deployments.", cmdModelDeploymentList),
		newManifestCommand("model-deployment-show", "Show one Foundry model deployment.", cmdModelDeploymentShow),
		newManifestCommand("model-deployment-plan", "Validate and plan a Foundry model deployment.", cmdModelDeploymentPlan),
		newManifestCommand("model-deployment-create", "Create a validated Foundry model deployment.", cmdModelDeploymentCreate),
		newManifestCommand("model-deployment-delete", "Delete a Foundry model deployment.", cmdModelDeploymentDelete),
		newManifestCommand("connection-list", "List project connections without credential values.", cmdConnectionList),
		newManifestCommand("connection-show", "Show one project connection without credential values.", cmdConnectionShow),
		newManifestCommand("connection-create", "Create one project connection through ARM.", cmdConnectionCreate),
		newManifestCommand("connection-update", "Update one existing project connection through ARM.", cmdConnectionUpdate),
		newManifestCommand("connection-delete", "Delete one project connection through ARM.", cmdConnectionDelete),
		newManifestCommand("api-center-list", "List or search documented Azure API Center MCP registry metadata.", cmdAPICenterList),
		newManifestCommand("api-center-show", "Show one exact Azure API Center MCP registry record.", cmdAPICenterShow),
		newManifestCommand("logicapps-registration-plan", "Validate and plan the portal-only non-OAuth2 Logic Apps connector registration.", cmdLogicAppsRegistrationPlan),
		newManifestCommand("connector-list", "Browse the preview Foundry managed connector catalog.", cmdConnectorList),
		newManifestCommand("connector-show", "Show one preview managed connector catalog entry.", cmdConnectorShow),
		newManifestCommand("connector-create", "Create an OAuth2 managed MCP connector connection.", cmdConnectorCreate),
		newManifestCommand("connector-consent", "Create a short-lived per-user OAuth consent link.", cmdConnectorConsent),
		newManifestCommand("connector-actions", "List or inspect agent-callable connector operations.", cmdConnectorActions),
		newManifestCommand("connector-configure", "Register the complete managed connector action allowlist.", cmdConnectorConfigure),
		newManifestCommand("connector-status", "Show managed MCP connector status and target.", cmdConnectorStatus),
		newManifestCommand("connector-wait", "Wait for a managed MCP connector to become Connected.", cmdConnectorWait),
		newManifestCommand("connector-toolbox-deploy", "Create or update a Toolbox version from a connected managed connector.", cmdConnectorToolboxDeploy),
		newManifestCommand("connector-delete", "Delete a managed MCP connector connection.", cmdConnectorDelete),
		newManifestCommand("preflight", "Verify local inputs, credentials, and reachable Azure dependencies.", cmdPreflight),
		newManifestCommand("deploy", "Preflight, ensure optional dependencies, and deploy the agent (mutating).", cmdDeploy),
		newManifestCommand("status", "Show concise remote agent and APIM connection status.", cmdStatus),
		newManifestCommand("show", "Show the remote agent or one immutable version.", cmdShow),
		newManifestCommand("endpoint-show", "Show stable endpoint routing, identity, protocols, authorization, and agent card.", cmdEndpointShow),
		newManifestCommand("endpoint-configure", "Apply manifest endpoint protocols, authorization, and agent card without changing routing.", cmdEndpointConfigure),
		newManifestCommand("versions", "List immutable agent versions.", cmdVersions),
		newManifestCommand("diff", "Compare the desired manifest with the latest remote version.", cmdDiff),
		newManifestCommand("compatibility", "Check documented model, region, and tool compatibility.", cmdCompatibility),
		newManifestCommand("toolbox-validate", "Validate managed Toolbox definitions and local references (offline).", cmdToolboxValidate),
		newManifestCommand("toolbox-plan", "Plan immutable Toolbox version creation without calling Azure.", cmdToolboxPlan),
		newManifestCommand("toolbox-deploy", "Create an immutable Toolbox version without promoting later versions.", cmdToolboxDeploy),
		newManifestCommand("toolbox-status", "Show a logical Toolbox and its promoted default version.", cmdToolboxStatus),
		newManifestCommand("toolbox-versions", "List immutable Toolbox versions.", cmdToolboxVersions),
		newManifestCommand("toolbox-promote", "Promote one immutable Toolbox version as the consumer default.", cmdToolboxPromote),
		newManifestCommand("toolbox-delete-version", "Delete one non-default immutable Toolbox version.", cmdToolboxDeleteVersion),
		newManifestCommand("skill-create", "Create a preview skill or immutable skill version.", cmdSkillCreate),
		newManifestCommand("skill-list", "List preview skills in the Foundry project.", cmdSkillList),
		newManifestCommand("skill-show", "Show one preview skill.", cmdSkillShow),
		newManifestCommand("skill-version-list", "List immutable versions of one preview skill.", cmdSkillVersionList),
		newManifestCommand("skill-version-show", "Show one immutable preview skill version.", cmdSkillVersionShow),
		newManifestCommand("skill-set-default", "Set the default version of one preview skill.", cmdSkillSetDefault),
		newManifestCommand("skill-delete", "Delete one preview skill and all versions.", cmdSkillDelete),
		newManifestCommand("skill-version-delete", "Delete one immutable preview skill version.", cmdSkillVersionDelete),
		newManifestCommand("skill-download", "Download a preview skill version as a zip archive.", cmdSkillDownload),
		newManifestCommand("grounding-validate", "Validate managed document-grounding definitions and files (offline).", cmdGroundingValidate),
		newManifestCommand("grounding-plan", "Plan managed vector-store synchronization without calling Azure.", cmdGroundingPlan),
		newManifestCommand("grounding-sync", "Upload and index manifest documents in a managed vector store.", cmdGroundingSync),
		newManifestCommand("grounding-status", "Show managed vector-store and file-indexing status.", cmdGroundingStatus),
		newManifestCommand("grounding-delete-file", "Detach one managed document from a vector store.", cmdGroundingDeleteFile),
		newManifestCommand("grounding-delete-store", "Delete one managed vector store.", cmdGroundingDeleteStore),
		newManifestCommand("memory-store-validate", "Validate preview memory-store definitions (offline).", cmdMemoryStoreValidate),
		newManifestCommand("memory-store-plan", "Plan preview memory-store reconciliation (offline).", cmdMemoryStorePlan),
		newManifestCommand("memory-store-sync", "Create or reconcile one preview memory store.", cmdMemoryStoreSync),
		newManifestCommand("memory-store-list", "List preview memory stores in the Foundry project.", cmdMemoryStoreList),
		newManifestCommand("memory-store-show", "Show one preview memory store.", cmdMemoryStoreShow),
		newManifestCommand("memory-store-delete", "Delete one preview memory store.", cmdMemoryStoreDelete),
		newManifestCommand("memory-search", "Search one preview memory-store scope.", cmdMemorySearch),
		newManifestCommand("memory-update", "Extract and consolidate memories from conversation items.", cmdMemoryUpdate),
		newManifestCommand("memory-item-create", "Create one explicit preview memory item.", cmdMemoryItemCreate),
		newManifestCommand("memory-item-list", "List preview memory items in a scope.", cmdMemoryItemList),
		newManifestCommand("memory-item-show", "Show one preview memory item.", cmdMemoryItemShow),
		newManifestCommand("memory-item-update", "Update one preview memory item.", cmdMemoryItemUpdate),
		newManifestCommand("memory-item-delete", "Delete one preview memory item.", cmdMemoryItemDelete),
		newManifestCommand("memory-scope-delete", "Delete every preview memory item in one scope.", cmdMemoryScopeDelete),
		newManifestCommand("smoke", "Invoke the agent through its stable endpoint once.", cmdSmoke),
		newManifestCommand("disable", "Suspend the agent.", cmdDisable),
		newManifestCommand("enable", "Resume a suspended agent.", cmdEnable),
		newManifestCommand("promote", "Route the stable endpoint to a version or explicitly restore latest.", cmdPromote),
		newManifestCommand("rollback", "Route the stable endpoint back to a verified immutable version.", cmdRollback),
		newManifestCommand("publish-m365", "Ensure Bot Service and publish the stable endpoint to Microsoft 365 and Teams.", cmdPublishM365),
		newManifestCommand("legacy-status", "Inspect explicit legacy Agent Application compatibility resources.", cmdLegacyStatus),
		newManifestCommand("legacy-deploy", "Ensure an explicit legacy Agent Application and Managed Responses deployment.", cmdLegacyDeploy),
		newManifestCommand("legacy-delete", "Delete explicit legacy compatibility resources.", cmdLegacyDelete),
		newManifestCommand("prune", "Delete older agent versions while retaining the newest versions.", cmdPrune),
		newManifestCommand("delete-version", "Delete one immutable agent version (destructive).", cmdDeleteVersion),
		newManifestCommand("delete", "Delete the whole agent (destructive, irreversible).", cmdDelete),
		newManifestCommand("decommission", "Delete the agent and its optional APIM connection (destructive, irreversible).", cmdDecommission),
	}

	for _, command := range commands {
		addOverrideFlags(command)
		switch command.Name() {
		case "init":
			command.Flags().Lookup("manifest").Usage = "Path to write the new agent manifest (YAML)."
			command.Flags().Bool("force", false, "Overwrite an existing file at --manifest.")
			command.Flags().Bool("no-tools", false, "Omit the default code_interpreter tool.")
			command.Flags().String(
				"guardrail-policy-id",
				"",
				"Optional full RAI policy resource ID; omitted to inherit the model deployment policy.",
			)
			for flagName, usage := range map[string]string{
				"name":                "Seed agent.name (default: new-agent).",
				"model":               "Seed agent.model (default: a placeholder).",
				"description":         "Seed agent.description.",
				"instructions-file":   "Seed agent.instructions from this file's current content (one time; not a runtime override).",
				"project-resource-id": "Seed project.resource_id (Foundry project resource ID).",
				"location":            "Seed project.location.",
			} {
				command.Flags().Lookup(flagName).Usage = usage
			}
		case "preflight":
			addDeploymentDependencyFlags(command)
			addPromptPreviewFlag(command)
		case "project-create":
			command.Flags().Float64("project-wait-timeout", 180, "Seconds to wait for the project data plane.")
			command.Flags().Float64("project-wait-interval", 5, "Seconds between project readiness checks.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "model-deployment-list":
		case "model-deployment-show":
			command.Flags().String(
				"deployment-name",
				"",
				"Deployment name (defaults to model_deployment.deployment_name or agent.model).",
			)
		case "model-deployment-plan":
			addModelDeploymentDesiredFlags(command)
		case "model-deployment-create":
			addModelDeploymentDesiredFlags(command)
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			command.Flags().Duration("wait-timeout", 30*time.Minute, "Maximum time to wait for model deployment creation.")
			command.Flags().Duration("wait-interval", 10*time.Second, "Delay between model deployment status checks.")
		case "model-deployment-delete":
			command.Flags().String(
				"deployment-name",
				"",
				"Deployment name (defaults to model_deployment.deployment_name or agent.model).",
			)
			command.Flags().Bool("dry-run", false, "Show the deletion without applying it.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			command.Flags().Duration("wait-timeout", 30*time.Minute, "Maximum time to wait for model deployment deletion.")
			command.Flags().Duration("wait-interval", 10*time.Second, "Delay between model deployment status checks.")
		case "connection-list":
			addConnectionAPIVersionFlag(command)
		case "connection-show":
			addConnectionFlags(command)
		case "connection-create", "connection-update":
			addConnectionFlags(command)
			command.Flags().String("connection-type", "", "Foundry connection category, such as ApiKey or RemoteTool_Preview.")
			command.Flags().String("target", "", "Absolute HTTPS service endpoint stored in the connection.")
			command.Flags().String("auth-type", "", "ARM connection authType.")
			command.Flags().String("audience", "", "Optional managed-identity token audience.")
			command.Flags().Bool("shared", false, "Share the connection with every user of the project.")
			command.Flags().String("metadata-file", "", "JSON object with non-secret string metadata.")
			command.Flags().String("credentials-file", "", "JSON object containing credentials; never emitted in output or receipts.")
			command.Flags().String("secret-file", "", "Plain-text API key file used only with --auth-type ApiKey.")
			command.Flags().String("secret-env", "", "Environment variable containing an API key used only with --auth-type ApiKey.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "connection-type", "target", "auth-type")
		case "connection-delete":
			addConnectionFlags(command)
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "api-center-list":
			addAPICenterFlags(command)
			command.Flags().String("search", "", "Client-side case-insensitive search over returned registry metadata.")
		case "api-center-show":
			addAPICenterFlags(command)
			command.Flags().String("server", "", "Exact server name, id, title, or displayName.")
			requireFlags(command, "server")
		case "logicapps-registration-plan":
			addManagedConnectorPreviewFlag(command)
			command.Flags().String("connector-name", "", "Exact non-OAuth2 connector catalog name.")
			command.Flags().String("mcp-server-name", "", "MCP server name to enter in the Azure portal wizard.")
			command.Flags().String("mcp-server-description", "", "MCP server description to enter in the Azure portal wizard.")
			command.Flags().String("logic-app-resource-id", "", "Optional existing Standard logic app Microsoft.Web/sites resource ID.")
			command.Flags().StringSlice("operation", nil, "Connector action to register; repeat or provide CSV.")
			command.Flags().StringSlice("user-parameter", nil, "Parameter sourced from a portal-provided static User value as <operation>/<parameter>.")
			command.Flags().StringSlice("model-parameter", nil, "Optional parameter sourced from the model as <operation>/<parameter>.")
			requireFlags(command, "connector-name", "mcp-server-name", "mcp-server-description")
		case "connector-list":
			addManagedConnectorPreviewFlag(command)
			command.Flags().String("search", "*", "Free-text connector catalog search.")
			command.Flags().Int("page-size", 100, "Connector catalog page size (1-100).")
			command.Flags().Int("skip", 0, "Connector catalog records to skip.")
		case "connector-show":
			addManagedConnectorPreviewFlag(command)
			command.Flags().String("connector-name", "", "Exact Foundry connector catalog name.")
			requireFlags(command, "connector-name")
		case "connector-create":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().String("connector-name", "", "Exact OAuth2 connector catalog name.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "connector-name")
		case "connector-consent":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().String("object-id", "", "End-user or service-principal object ID receiving connector consent.")
			command.Flags().String("tenant-id", "", "Home tenant ID for the consenting principal.")
			command.Flags().String(
				"redirect-url",
				connection.DefaultConnectorRedirectURL,
				"HTTPS OAuth redirect URL registered for the Foundry consent experience.",
			)
			requireFlags(command, "object-id", "tenant-id")
		case "connector-actions":
			addManagedConnectorPreviewFlag(command)
			command.Flags().String("connector-name", "", "Exact Foundry connector catalog name.")
			command.Flags().String("operation", "", "Optional exact operation name to inspect with its input schema.")
			requireFlags(command, "connector-name")
		case "connector-configure":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().StringSlice("operation", nil, "Complete agent-callable operation allowlist; repeat or provide CSV.")
			command.Flags().String("connector-description", "", "Description applied to the managed MCP server and connector.")
			command.Flags().Bool("yes", false, "Confirm replacement of an existing complete action allowlist.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "connector-status":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
		case "connector-wait":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().Duration("connector-timeout", 10*time.Minute, "Maximum time to wait for Connected status.")
			command.Flags().Duration("connector-interval", 5*time.Second, "Delay between connector status checks.")
		case "connector-toolbox-deploy":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().String("toolbox-name", "", "Foundry Toolbox name to create or update.")
			command.Flags().String("toolbox-description", "", "Optional immutable Toolbox version description.")
			command.Flags().String("toolbox-project-connection", "", "Existing project connection used by the emitted Prompt Agent Toolbox attachment.")
			command.Flags().Bool("if-changed", true, "Skip immutable version creation when the newest managed payload is unchanged.")
			command.Flags().Bool("promote", false, "Promote the resulting immutable version as the Toolbox consumer default.")
			command.Flags().Bool("yes", false, "Confirm --promote without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			addTrustFlags(command)
			requireFlags(command, "toolbox-name")
		case "connector-delete":
			addManagedConnectorPreviewFlag(command)
			addConnectionFlags(command)
			command.Flags().Bool("yes", false, "Confirm managed connector deletion.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "deploy":
			addDeploymentDependencyFlags(command)
			addPromptPreviewFlag(command)
			command.Flags().Float64("project-wait-timeout", 180, "Seconds to wait for a newly created project.")
			command.Flags().Float64("project-wait-interval", 5, "Seconds between project readiness checks.")
			command.Flags().Bool("if-changed", false, "Skip creating an immutable version when managed fields are unchanged.")
			command.Flags().Bool("smoke-test", false, "Invoke the agent once after deployment.")
			command.Flags().String("smoke-prompt", "Reply with a short readiness confirmation.", "Prompt used by --smoke-test.")
			command.Flags().String(
				"structured-inputs-file",
				"",
				"JSON object containing runtime values declared by agent.structured_inputs.",
			)
			command.Flags().String("memory-user-id", "", "Runtime x-memory-user-id value for {{$userId}} Memory scopes.")
			addMCPApprovalFlags(command)
			command.Flags().String("receipt", "", "Deployment receipt path (defaults beside the manifest).")
			command.Flags().Bool("rollback-created-project", false, "Delete a project created by this run if deployment fails.")
			command.Flags().Bool(
				"allow-active-apim-update",
				false,
				"Allow a shared APIM connection update that can affect the active version before promotion.",
			)
			command.Flags().Bool(
				"allow-unconditional-shared-rollback",
				false,
				"Allow APIM/project rollback without service concurrency tokens; unsafe with concurrent deployments.",
			)
		case "status", "diff":
			command.Flags().Bool("no-apim", false, "Skip APIM connection inspection.")
		case "compatibility":
			command.Flags().String("model-name", "", "Base model name from the Microsoft compatibility table; defaults to agent.model.")
			command.Flags().String("region", "", "Azure region; defaults to project.location.")
		case "toolbox-validate", "toolbox-plan", "toolbox-status", "toolbox-versions":
			command.Flags().String("toolbox", "", "Toolbox name; required when the manifest defines multiple Toolboxes.")
		case "toolbox-deploy":
			command.Flags().String("toolbox", "", "Toolbox name; required when the manifest defines multiple Toolboxes.")
			command.Flags().Bool("if-changed", false, "Skip creating a version when the newest managed payload is unchanged.")
			command.Flags().Bool("accept-preview", false, "Explicitly accept preview limitations used by the Toolbox.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			addTrustFlags(command)
		case "toolbox-promote":
			command.Flags().String("toolbox", "", "Toolbox name; required when the manifest defines multiple Toolboxes.")
			command.Flags().String("toolbox-version", "", "Immutable Toolbox version to make the consumer default.")
			command.Flags().Bool("dry-run", false, "Show the default-version change without applying it.")
			command.Flags().Bool("yes", false, "Confirm promotion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "toolbox-version")
		case "toolbox-delete-version":
			command.Flags().String("toolbox", "", "Toolbox name; required when the manifest defines multiple Toolboxes.")
			command.Flags().String("toolbox-version", "", "Non-default immutable Toolbox version to delete.")
			command.Flags().Bool("dry-run", false, "Show the deletion without applying it.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "toolbox-version")
		case "skill-create":
			addSkillFlags(command)
			command.Flags().String("path", "", "Skill zip file or directory containing a root SKILL.md.")
			command.Flags().String("skill-instructions-file", "", "Markdown instructions file for inline creation.")
			command.Flags().String("skill-description", "", "Description for inline skill creation.")
			command.Flags().String("license", "", "Optional inline skill license.")
			command.Flags().String("compatibility", "", "Optional inline skill compatibility notes.")
			command.Flags().StringSlice("allowed-tools", nil, "Experimental pre-approved tool names for an inline skill.")
			command.Flags().Bool("default", false, "Set the created version as the skill default.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "skill-list":
			command.Flags().Bool("accept-preview", false, "Explicitly accept Skills preview limitations.")
		case "skill-show", "skill-version-list":
			addSkillFlags(command)
		case "skill-version-show":
			addSkillVersionFlags(command)
		case "skill-set-default":
			addSkillVersionFlags(command)
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "skill-delete":
			addSkillFlags(command)
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "skill-version-delete":
			addSkillVersionFlags(command)
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "skill-download":
			addSkillFlags(command)
			command.Flags().String("version", "", "Optional immutable version; omit to download the default.")
			command.Flags().String("destination", "", "Destination zip path.")
			command.Flags().Bool("force", false, "Replace an existing destination.")
			requireFlags(command, "destination")
		case "grounding-validate", "grounding-plan", "grounding-status":
			command.Flags().String("grounding", "", "Managed vector-store name; required when the manifest defines multiple stores.")
		case "grounding-sync":
			command.Flags().String("grounding", "", "Managed vector-store name; required when the manifest defines multiple stores.")
			command.Flags().Duration("index-timeout", 10*time.Minute, "Maximum time to wait for file indexing and vector-store readiness.")
			command.Flags().Duration("index-interval", 2*time.Second, "Delay between indexing status checks.")
			command.Flags().Bool("prune", false, "Detach manager-owned files removed from the manifest.")
			command.Flags().Bool("delete-pruned-uploads", false, "Also delete pruned project files globally; requires --prune and confirmation.")
			command.Flags().Bool("delete-replaced-uploads", false, "Also delete replaced project files globally after detaching them; requires confirmation.")
			command.Flags().Bool("yes", false, "Confirm pruning without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "grounding-delete-file":
			command.Flags().String("grounding", "", "Managed vector-store name; required when the manifest defines multiple stores.")
			command.Flags().String("file", "", "Manifest-relative document path to detach.")
			command.Flags().Bool("delete-upload", false, "Also delete the project file globally after detaching it.")
			command.Flags().Bool("dry-run", false, "Show the detach without applying it.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "file")
		case "grounding-delete-store":
			command.Flags().String("grounding", "", "Managed vector-store name; required when the manifest defines multiple stores.")
			command.Flags().Bool("delete-uploads", false, "Also delete manager-owned project files globally after deleting the store.")
			command.Flags().Bool("dry-run", false, "Show the deletion without applying it.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "memory-store-validate", "memory-store-plan":
			command.Flags().String("memory-store", "", "Memory store name; required when the manifest defines multiple stores.")
		case "memory-store-list":
			command.Flags().Bool("accept-preview", false, "Explicitly accept Memory preview limitations.")
		case "memory-store-sync", "memory-store-show":
			addMemoryStoreFlags(command)
			if command.Name() == "memory-store-sync" {
				command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			}
		case "memory-store-delete":
			addMemoryStoreFlags(command)
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "memory-search":
			addMemoryStoreFlags(command)
			addMemoryConversationFlags(command)
			command.Flags().String("previous-search-id", "", "Previous search id for incremental retrieval.")
			command.Flags().Int("max-memories", 0, "Maximum memories to return; zero uses the service default.")
		case "memory-update":
			addMemoryStoreFlags(command)
			addMemoryConversationFlags(command)
			command.Flags().String("previous-update-id", "", "Previous update id for incremental extraction.")
			command.Flags().Int("update-delay", 0, "Debounce delay in seconds; zero processes immediately.")
			command.Flags().Duration("memory-timeout", 2*time.Minute, "Maximum time to wait for memory extraction.")
			command.Flags().Duration("memory-interval", 2*time.Second, "Delay between memory update status checks.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "memory-item-create":
			addMemoryStoreFlags(command)
			command.Flags().String("scope", "", "Required memory isolation scope.")
			command.Flags().String("content", "", "Required memory content.")
			command.Flags().String("kind", "user_profile", "Memory kind: user_profile, chat_summary, or procedural.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "scope", "content")
		case "memory-item-list":
			addMemoryStoreFlags(command)
			command.Flags().String("scope", "", "Required memory isolation scope.")
			command.Flags().String("kind", "", "Optional memory kind filter.")
			requireFlags(command, "scope")
		case "memory-item-show":
			addMemoryStoreFlags(command)
			command.Flags().String("memory-id", "", "Memory item id.")
			requireFlags(command, "memory-id")
		case "memory-item-update":
			addMemoryStoreFlags(command)
			command.Flags().String("memory-id", "", "Memory item id.")
			command.Flags().String("content", "", "Updated memory content.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "memory-id", "content")
		case "memory-item-delete":
			addMemoryStoreFlags(command)
			command.Flags().String("memory-id", "", "Memory item id.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "memory-id")
		case "memory-scope-delete":
			addMemoryStoreFlags(command)
			command.Flags().String("scope", "", "Memory isolation scope to delete.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "scope")
		case "show":
			command.Flags().String("agent-version", "", "Show this immutable agent version instead of the logical agent.")
		case "endpoint-configure":
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "smoke":
			command.Flags().String("prompt", "Reply with a short readiness confirmation.", "Prompt sent to the agent.")
			command.Flags().String(
				"structured-inputs-file",
				"",
				"JSON object containing runtime values declared by agent.structured_inputs.",
			)
			command.Flags().String("memory-user-id", "", "Runtime x-memory-user-id value for {{$userId}} Memory scopes.")
			addMCPApprovalFlags(command)
		case "promote":
			command.Flags().String("agent-version", "", "Immutable version to receive all stable-endpoint traffic.")
			command.Flags().Bool("latest", false, "Explicitly restore automatic routing to the latest version.")
			command.Flags().Bool("dry-run", false, "Show the routing change without applying it.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "rollback":
			command.Flags().String("agent-version", "", "Earlier immutable version to receive all stable-endpoint traffic.")
			command.Flags().Bool("latest", false, "Reserved for parity; rollback rejects latest routing.")
			command.Flags().Bool("dry-run", false, "Show the rollback without applying it.")
			command.Flags().Bool("yes", false, "Confirm the rollback without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "agent-version")
		case "publish-m365":
			command.Flags().String("publication", "", "Path to foundry-agent-manager/publication/v1 YAML or JSON.")
			command.Flags().Bool("allow-bot-update", false, "Allow a confirmed Bot Service identity, tenant, or endpoint update.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "publication")
		case "legacy-status":
			addLegacyFlags(command)
		case "legacy-deploy":
			addLegacyFlags(command)
			command.Flags().String("agent-version", "", "Immutable prompt-agent version to expose through the legacy deployment.")
			command.Flags().String("legacy-display-name", "", "Legacy application display name.")
			command.Flags().String("legacy-description", "", "Legacy application description.")
			command.Flags().Bool("route", false, "Route all legacy application traffic to the ensured deployment.")
			command.Flags().Bool("yes", false, "Confirm a legacy traffic-routing change without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
			requireFlags(command, "agent-version")
		case "legacy-delete":
			addLegacyFlags(command)
			command.Flags().Bool("application", false, "Also delete the parent legacy Agent Application.")
			command.Flags().Bool("dry-run", false, "Show compatibility resources that would be deleted.")
			command.Flags().Bool("yes", false, "Confirm deletion without an interactive prompt.")
			command.Flags().String("receipt", "", "Operation receipt path (defaults beside the manifest).")
		case "prune":
			addDestructiveFlags(command)
			command.Flags().Int("keep", 1, "Number of newest versions to retain.")
		case "delete-version":
			addDestructiveFlags(command)
			command.Flags().String("agent-version", "", "Immutable agent version to delete.")
			requireFlags(command, "agent-version")
		case "delete":
			addDestructiveFlags(command)
		case "decommission":
			addDestructiveFlags(command)
			command.Flags().Bool("no-apim", false, "Skip tearing down the APIM connection.")
		}
		root.AddCommand(command)
	}
	root.AddCommand(newAgent365Commands()...)

	applyCommandExamples(root)
	registerCommandHierarchy(root)
	configureFocusedHelp(root)
	configureCommandCompletions(root)
	return root
}

func configureFocusedHelp(root *cobra.Command) {
	defaultHelp := root.HelpFunc()
	root.SetHelpFunc(func(command *cobra.Command, args []string) {
		defaultHelp(command, args)
		writeRelatedCommandHelp(command)
	})

	root.SetHelpCommand(&cobra.Command{
		Use:               "help [command path]",
		Short:             "Show focused help for one namespace or command.",
		ValidArgsFunction: completeHelpTopics,
		Long: `Show focused help for one namespace or command.

With no command path, help prints the top-level resource catalog. With a
namespace or command path, it prints only that target's description, usage,
subcommands or examples, flags, and related workflow commands.`,
		Example: strings.Join([]string{
			"  foundry-agent-manager help quickstart",
			"  foundry-agent-manager help prompt deploy",
			"  foundry-agent-manager help hosted session file",
		}, "\n"),
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return root.Help()
			}

			target, remaining, err := root.Find(args)
			if err != nil || target == root || len(remaining) != 0 {
				topic := strings.Join(args, " ")
				return errs.WithNextSteps(
					errs.Config("unknown help topic %q", topic),
					"Run foundry-agent-manager help to see all available commands.",
					"Use foundry-agent-manager help <command path> with an exact namespace or command path.",
				)
			}
			return target.Help()
		},
	})
}

var relatedHelpCommands = map[string][]string{
	"quickstart": {
		"doctor", "init", "validate", "hosted-adopt", "hosted-init", "hosted-validate",
	},
	"doctor": {
		"quickstart", "preflight", "hosted-preflight",
	},
	"init": {
		"validate", "plan", "preflight", "deploy",
	},
	"validate": {
		"plan", "preflight", "deploy",
	},
	"plan": {
		"validate", "preflight", "deploy",
	},
	"preflight": {
		"validate", "model-deployment-plan", "deploy", "status",
	},
	"deploy": {
		"status", "diff", "promote", "rollback",
	},
	"hosted-init": {
		"hosted-validate", "hosted-plan", "hosted-environment-create", "hosted-preflight", "hosted-deploy",
	},
	"hosted-adopt": {
		"hosted-validate", "hosted-plan", "hosted-environment-create", "hosted-preflight", "hosted-deploy",
	},
	"hosted-validate": {
		"hosted-plan", "hosted-environment-create", "hosted-preflight", "hosted-deploy",
	},
	"hosted-plan": {
		"hosted-validate", "hosted-environment-create", "hosted-preflight", "hosted-deploy",
	},
	"hosted-environment-create": {
		"hosted-preflight", "hosted-plan", "hosted-deploy",
	},
	"hosted-preflight": {
		"hosted-environment-create", "hosted-plan", "hosted-deploy", "hosted-diagnose",
	},
	"hosted-deploy": {
		"hosted-status", "hosted-diff", "hosted-promote", "hosted-rollback",
	},
	"hosted-status": {
		"hosted-diagnose", "hosted-versions", "hosted-logs",
	},
	"model-deployment-list": {
		"model-deployment-show", "model-deployment-plan", "model-deployment-create",
	},
	"model-deployment-show": {
		"model-deployment-plan", "model-deployment-delete",
	},
	"model-deployment-plan": {
		"model-deployment-list", "model-deployment-create", "preflight",
	},
	"model-deployment-create": {
		"model-deployment-show", "preflight", "deploy",
	},
	"model-deployment-delete": {
		"model-deployment-list", "model-deployment-plan",
	},
	"agent365-info": {
		"agent365-blueprint-list", "agent365-blueprint-validate", "agent365-binding-status",
	},
	"agent365-blueprint-list": {
		"agent365-blueprint-show", "agent365-blueprint-validate",
	},
	"agent365-blueprint-show": {
		"agent365-blueprint-permissions", "agent365-blueprint-validate", "agent365-binding-plan",
	},
	"agent365-blueprint-permissions": {
		"agent365-blueprint-validate", "agent365-binding-plan",
	},
	"agent365-blueprint-validate": {
		"agent365-binding-plan", "agent365-binding-status",
	},
	"agent365-binding-plan": {
		"agent365-binding-status", "agent365-blueprint-validate",
	},
	"agent365-binding-status": {
		"agent365-binding-plan", "agent365-blueprint-show",
	},
}

func writeRelatedCommandHelp(command *cobra.Command) {
	names := relatedHelpCommands[legacyCommandName(command)]
	if len(names) == 0 {
		return
	}

	out := command.OutOrStdout()
	_, _ = fmt.Fprintln(out, "\nRelated workflow:")
	for _, name := range names {
		path := canonicalCommandArgs(name)
		related, remaining, err := command.Root().Find(path)
		if err != nil || related == nil || related == command || len(remaining) != 0 {
			continue
		}
		_, _ = fmt.Fprintf(
			out,
			"  %-43s %s\n",
			"foundry-agent-manager help "+strings.Join(path, " "),
			related.Short,
		)
	}
}

func addMemoryStoreFlags(command *cobra.Command) {
	command.Flags().String(
		"memory-store",
		"",
		"Memory store name; required when the manifest defines multiple stores.",
	)
	command.Flags().Bool("accept-preview", false, "Explicitly accept Memory preview limitations.")
}

func addMemoryConversationFlags(command *cobra.Command) {
	command.Flags().String("scope", "", "Required memory isolation scope.")
	command.Flags().String("input", "", "One user message converted to a Responses conversation item.")
	command.Flags().String("items-file", "", "JSON array of Responses conversation items.")
	requireFlags(command, "scope")
}

func addSkillFlags(command *cobra.Command) {
	command.Flags().String("skill", "", "Skill name.")
	command.Flags().Bool("accept-preview", false, "Explicitly accept Skills preview limitations.")
	requireFlags(command, "skill")
}

func addSkillVersionFlags(command *cobra.Command) {
	addSkillFlags(command)
	command.Flags().String("version", "", "Immutable skill version.")
	requireFlags(command, "version")
}

func addModelDeploymentDesiredFlags(command *cobra.Command) {
	command.Flags().String(
		"deployment-name",
		"",
		"Deployment name (defaults to model_deployment.deployment_name or agent.model).",
	)
	command.Flags().String("model-name", "", "Exact model catalog name.")
	command.Flags().String("model-version", "", "Exact model catalog version.")
	command.Flags().String("model-format", "", "Model format, such as OpenAI.")
	command.Flags().String("sku-name", "", "Azure model deployment SKU.")
	command.Flags().Int("capacity", 0, "Requested deployment capacity.")
	command.Flags().String("rai-policy-name", "", "Optional existing RAI policy name.")
	command.Flags().String("version-upgrade-option", "", "Optional model version upgrade policy.")
	command.Flags().String("spillover-deployment-name", "", "Optional existing spillover deployment name.")
}

func addConnectionAPIVersionFlag(command *cobra.Command) {
	command.Flags().String(
		"connection-api-version",
		config.DefaultConnectionAPIVersion,
		"ARM API version for Foundry project connections.",
	)
}

func addConnectionFlags(command *cobra.Command) {
	command.Flags().String("connection", "", "Project connection name.")
	addConnectionAPIVersionFlag(command)
	requireFlags(command, "connection")
}

func addManagedConnectorPreviewFlag(command *cobra.Command) {
	command.Flags().Bool(
		"accept-preview",
		false,
		"Explicitly accept managed MCP connector preview limitations and publisher data boundaries.",
	)
}

func addAPICenterFlags(command *cobra.Command) {
	command.Flags().String(
		"api-center-endpoint",
		"",
		"Azure API Center data-plane origin ending in .azure-apicenter.ms.",
	)
	command.Flags().String(
		"api-center-token-scope",
		"",
		"Explicit Microsoft Entra scope for authenticated API Center access; omit for anonymous access.",
	)
	requireFlags(command, "api-center-endpoint")
}

func addMCPApprovalFlags(command *cobra.Command) {
	command.Flags().StringArray(
		"approve-mcp-tool",
		nil,
		"Approve an exact MCP call by <server_label>/<tool_name>; repeat as needed.",
	)
	command.Flags().Bool(
		"reject-unapproved-mcp",
		false,
		"Reject unmatched MCP approval requests and continue instead of stopping for review.",
	)
	command.Flags().Int(
		"max-mcp-approval-rounds",
		5,
		"Maximum MCP approval continuation rounds (1-20).",
	)
}

func newManifestCommand(name, short string, run func(*cobra.Command, []string) error) *cobra.Command {
	command := &cobra.Command{
		Use:          name,
		Short:        short,
		Args:         noArgs,
		RunE:         run,
		SilenceUsage: true,
	}
	command.Flags().StringP("manifest", "f", "", "Path to the agent manifest (YAML/JSON).")
	requireFlags(command, "manifest")
	return command
}

func noArgs(_ *cobra.Command, args []string) error {
	if len(args) != 0 {
		return errs.Config("accepts 0 arg(s), received %d", len(args))
	}
	return nil
}

func requireFlags(command *cobra.Command, names ...string) {
	previous := command.PreRunE
	command.PreRunE = func(cmd *cobra.Command, args []string) error {
		if previous != nil {
			if err := previous(cmd, args); err != nil {
				return err
			}
		}
		for _, name := range names {
			if getFlag(cmd, name) == "" {
				return errs.Config("--%s is required", name)
			}
		}
		return nil
	}
}

func addOverrideFlags(command *cobra.Command) {
	command.Flags().String("location", "", "Azure region for --ensure-project.")
	command.Flags().String("name", "", "Override agent.name.")
	command.Flags().String("model", "", "Override agent.model (model deployment name).")
	command.Flags().String("description", "", "Override agent.description.")
	command.Flags().String("instructions-file", "", "Read agent.instructions from this contained file.")
	command.Flags().String("project-resource-id", "", "Override project.resource_id (Foundry project resource ID).")
}

func metadataFromFlags(command *cobra.Command) (map[string]string, error) {
	flags := command.Flags()
	if flags.Lookup("metadata") == nil {
		flags = command.InheritedFlags()
	}
	if flags.Lookup("metadata") == nil && command.Root() != nil {
		flags = command.Root().PersistentFlags()
	}
	if flags.Lookup("metadata") == nil {
		return nil, nil
	}
	values, err := flags.GetStringArray("metadata")
	if err != nil {
		return nil, errs.Config("failed to read --metadata: %v", err)
	}
	return custommetadata.ParseAssignments(values)
}

func addDeploymentDependencyFlags(command *cobra.Command) {
	command.Flags().Bool("ensure-project", false, "Create the Foundry project if it is missing.")
	command.Flags().Bool("no-apim", false, "Skip the APIM connection.")
	command.Flags().String("apim-subscription-key", "", "APIM key value; prefer another source because process arguments can be visible.")
	command.Flags().String("apim-subscription-key-file", "", "Read the APIM key from a file.")
	command.Flags().Bool("apim-subscription-key-stdin", false, "Read the APIM key from stdin.")
	command.Flags().String("apim-subscription-key-key-vault", "", "Azure Key Vault secret URL containing the APIM key.")
	command.Flags().String("apim-subscription-key-env", "FOUNDRY_AGENT_MANAGER_APIM_SUBSCRIPTION_KEY", "Environment variable containing the APIM key.")
	command.Flags().Bool("allow-nonrestorable-apim-update", false, "Allow updating an existing API-key connection when Azure does not return its prior secret for rollback.")
	addTrustFlags(command)
}

func addDestructiveFlags(command *cobra.Command) {
	command.Flags().Bool("no-force", false, "Do not cascade-delete active sessions (force=false).")
	command.Flags().Bool("dry-run", false, "Print the destructive actions without applying them.")
	command.Flags().Bool("yes", false, "Confirm the destructive operation without an interactive prompt.")
}

func addPromptPreviewFlag(command *cobra.Command) {
	command.Flags().Bool(
		"accept-preview",
		false,
		"Explicitly accept preview limitations for any preview prompt-agent tool in the manifest.",
	)
}

func addHostedGuardrailOptOutFlag(command *cobra.Command) {
	command.Flags().Bool(
		"no-guardrail",
		false,
		"Explicitly accept that this workspace declares no agent-level RAI policy.",
	)
}

func addLegacyFlags(command *cobra.Command) {
	command.Flags().String("application-name", "", "Legacy Agent Application resource name.")
	command.Flags().String("deployment-name", "", "Legacy agentDeployment child resource name.")
	requireFlags(command, "application-name", "deployment-name")
}

func selectedCloudName(cmd *cobra.Command, manifestValue string) string {
	if value := getFlag(cmd, "cloud"); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("FOUNDRY_AGENT_MANAGER_CLOUD")); value != "" {
		return value
	}
	return manifestValue
}

func buildMetadata() string {
	parts := []string{config.Version}
	if config.BuildCommit != "" {
		parts = append(parts, "commit="+config.BuildCommit)
	}
	if config.BuildDate != "" {
		parts = append(parts, "built="+config.BuildDate)
	}
	return fmt.Sprintf("foundry-agent-manager %s", strings.Join(parts, " "))
}
