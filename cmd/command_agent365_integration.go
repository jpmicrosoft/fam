package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/agent365arm"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundryid"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type agent365IntegrationStatusResult struct {
	Account          agent365arm.AccountStatus `json:"account" yaml:"account"`
	CollectionActive bool                      `json:"collectionActive" yaml:"collectionActive"`
	Scope            string                    `json:"scope" yaml:"scope"`
	Guidance         []string                  `json:"guidance" yaml:"guidance"`
}

type agent365IntegrationPlanResult struct {
	Plan       agent365arm.Plan `json:"plan" yaml:"plan"`
	Scope      string           `json:"scope" yaml:"scope"`
	Executable bool             `json:"executable" yaml:"executable"`
	Guidance   []string         `json:"guidance" yaml:"guidance"`
}

type agent365IntegrationSetResult struct {
	Plan     agent365arm.Plan            `json:"plan" yaml:"plan"`
	Mutation *agent365arm.MutationResult `json:"mutation,omitempty" yaml:"mutation,omitempty"`
	Changed  bool                        `json:"changed" yaml:"changed"`
	Receipt  string                      `json:"receipt" yaml:"receipt"`
}

func newAgent365IntegrationCommands() []*cobra.Command {
	status := &cobra.Command{
		Use:          "agent365-integration-status",
		Short:        "Show Foundry account Agent 365 licensing and activity-data collection status.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365IntegrationStatus,
		SilenceUsage: true,
	}
	addAgent365AccountFlags(status)

	plan := &cobra.Command{
		Use:          "agent365-integration-plan",
		Short:        "Plan an account-wide Agent 365 activity-data collection change.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365IntegrationPlan,
		SilenceUsage: true,
	}
	addAgent365AccountFlags(plan)
	plan.Flags().Bool(
		"enabled",
		false,
		"Requested account-wide a365LoggingEnabled value; this flag must be explicitly supplied.",
	)

	set := &cobra.Command{
		Use:          "agent365-integration-set",
		Short:        "Set and verify account-wide Agent 365 activity-data collection.",
		GroupID:      "agent365",
		Args:         noArgs,
		RunE:         cmdAgent365IntegrationSet,
		SilenceUsage: true,
	}
	addAgent365AccountFlags(set)
	set.Flags().Bool(
		"enabled",
		false,
		"Requested account-wide a365LoggingEnabled value; this flag must be explicitly supplied.",
	)
	set.Flags().String(
		"if-match",
		"",
		"Optional ARM ETag precondition; defaults to the ETag returned by the planning GET.",
	)
	set.Flags().Bool("yes", false, "Confirm the account-wide Agent 365 data-collection mutation.")
	set.Flags().String("receipt", "", "Operation receipt path.")

	return []*cobra.Command{status, plan, set}
}

func cmdAgent365IntegrationStatus(cmd *cobra.Command, _ []string) error {
	client, _, err := newAgent365ARMClient(cmd)
	if err != nil {
		return err
	}
	status, err := client.GetStatus(commandContext(cmd))
	if err != nil {
		return err
	}
	result := agent365IntegrationStatusResult{
		Account:          status,
		CollectionActive: status.CollectionActive(),
		Scope:            "foundry-account",
		Guidance:         agent365IntegrationGuidance(status),
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 integration status: account=%s logging-present=%t logging-enabled=%t a365-status=%s collection-active=%t",
		status.Name,
		status.A365LoggingEnabledPresent,
		status.A365LoggingEnabled,
		status.A365Status,
		result.CollectionActive,
	))
}

func cmdAgent365IntegrationPlan(cmd *cobra.Command, _ []string) error {
	enabled, err := requiredEnabledFlag(cmd)
	if err != nil {
		return err
	}
	client, _, err := newAgent365ARMClient(cmd)
	if err != nil {
		return err
	}
	plan, err := client.Plan(commandContext(cmd), enabled)
	if err != nil {
		return err
	}
	result := agent365IntegrationPlanResult{
		Plan:       plan,
		Scope:      "foundry-account",
		Executable: true,
		Guidance:   agent365IntegrationGuidance(plan.Current),
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 integration plan: account=%s requested=%t change-required=%t action=%s current-status=%s",
		plan.Current.Name,
		enabled,
		plan.ChangeRequired,
		plan.Action,
		plan.Current.A365Status,
	))
}

func cmdAgent365IntegrationSet(cmd *cobra.Command, _ []string) error {
	enabled, err := requiredEnabledFlag(cmd)
	if err != nil {
		return err
	}
	client, cloudName, err := newAgent365ARMClient(cmd)
	if err != nil {
		return err
	}
	plan, err := client.Plan(commandContext(cmd), enabled)
	if err != nil {
		return err
	}
	path, err := agent365IntegrationReceiptPath(cmd, plan.Current.Name)
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		"agent365-integration-set",
		cloudName,
		receipt.ManifestReference{},
		receipt.ResourceReference{
			Name: plan.Current.Name,
			ID:   plan.Current.ID,
		},
		"",
	)
	if err != nil {
		return err
	}
	if err := store.AddStep(
		"inspect-agent365-integration",
		"succeeded",
		fmt.Sprintf(
			"logging-present=%t logging-enabled=%t a365-status=%s requested=%t",
			plan.Current.A365LoggingEnabledPresent,
			plan.Current.A365LoggingEnabled,
			plan.Current.A365Status,
			enabled,
		),
	); err != nil {
		return err
	}
	result := agent365IntegrationSetResult{
		Plan:    plan,
		Changed: plan.ChangeRequired,
		Receipt: store.Path,
	}
	if !plan.ChangeRequired {
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry_account_agent365_logging",
			Name:   plan.Current.Name,
			ID:     plan.Current.ID,
			Action: "none",
			Status: "unchanged",
		}); err != nil {
			return err
		}
		if err := store.Complete("unchanged", nil); err != nil {
			return err
		}
		return printResult(cmd, result, fmt.Sprintf(
			"Agent 365 integration unchanged: account=%s enabled=%t status=%s\n  receipt: %s",
			plan.Current.Name,
			enabled,
			plan.Current.A365Status,
			store.Path,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Set account-wide Agent 365 activity-data collection to %t for Foundry account %q?",
		enabled,
		plan.Current.Name,
	)); err != nil {
		_ = store.Complete("cancelled", err)
		return err
	}
	if err := store.AddStep(
		"set-agent365-logging",
		"started",
		fmt.Sprintf("a365LoggingEnabled=%t", enabled),
	); err != nil {
		return err
	}
	ifMatch := strings.TrimSpace(getFlag(cmd, "if-match"))
	if ifMatch == "" {
		ifMatch = plan.Current.ETag
	}
	mutation, mutationErr := client.SetLogging(
		commandContext(cmd),
		enabled,
		ifMatch,
	)
	result.Mutation = &mutation
	resourceStatus := "succeeded"
	receiptStatus := "succeeded"
	if mutationErr != nil {
		resourceStatus = string(mutation.Outcome)
		receiptStatus = "failed"
		if errs.IsAmbiguousMutation(mutationErr) {
			receiptStatus = "unknown"
		}
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "foundry_account_agent365_logging",
		Name:         plan.Current.Name,
		ID:           plan.Current.ID,
		Action:       string(plan.Action),
		Status:       resourceStatus,
		CreatedByRun: false,
		Reconciliation: fmt.Sprintf(
			"requested=%t outcome=%s patch-status=%d request-id=%s",
			enabled,
			mutation.Outcome,
			mutation.Patch.StatusCode,
			mutation.Patch.RequestID,
		),
	}); err != nil {
		return err
	}
	if mutationErr != nil {
		_ = store.Complete(receiptStatus, mutationErr)
		return errs.WithNextSteps(
			mutationErr,
			"Inspect the operation receipt at "+store.Path+".",
			"Run agent365 integration status to reconcile the account state before retrying.",
		)
	}
	if err := store.AddStep(
		"verify-agent365-logging",
		"succeeded",
		fmt.Sprintf("a365LoggingEnabled=%t", enabled),
	); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"Agent 365 integration updated: account=%s enabled=%t a365-status=%s outcome=%s\n  receipt: %s",
		plan.Current.Name,
		enabled,
		mutation.Verified.A365Status,
		mutation.Outcome,
		store.Path,
	))
}

func newAgent365ARMClient(
	cmd *cobra.Command,
) (*agent365arm.Client, string, error) {
	profile, err := resolveAgent365Profile(cmd)
	if err != nil {
		return nil, "", err
	}
	if profile.ARMEndpoint != agent365arm.AzureCloudARMEndpoint ||
		profile.ARMScope != agent365arm.AzureCloudARMScope {
		return nil, "", errs.Security(
			"Agent 365 ARM profile does not match the trusted AzureCloud endpoint and scope",
		)
	}
	accountIDRaw := getFlag(cmd, "account-id")
	acct, parseErr := foundryid.ParseAccountID(accountIDRaw)
	if parseErr != nil {
		return nil, "", errs.Config("--account-id is not a valid Foundry account resource ID: %v", parseErr)
	}
	if err := agent365arm.ValidateCoordinates(
		acct.SubscriptionID,
		acct.ResourceGroup,
		acct.AccountName,
	); err != nil {
		return nil, "", err
	}
	credential, err := newCredential(cmd, profile)
	if err != nil {
		return nil, "", err
	}
	client, err := agent365arm.NewClient(agent365arm.Options{
		SubscriptionID: acct.SubscriptionID,
		ResourceGroup:  acct.ResourceGroup,
		AccountName:    acct.AccountName,
		ARMEndpoint:    profile.ARMEndpoint,
		ARMScope:       profile.ARMScope,
		APIVersion:     agent365arm.DefaultAPIVersion,
		Credential:     credential,
		HTTPClient:     newHTTPClient(cmd),
	})
	if err != nil {
		return nil, "", err
	}
	return client, profile.Name, nil
}

func addAgent365AccountFlags(command *cobra.Command) {
	command.Flags().String("account-id", "", "Foundry account resource ID (/subscriptions/<uuid>/resourceGroups/<rg>/providers/Microsoft.CognitiveServices/accounts/<account>).")
	requireFlags(command, "account-id")
}

func requiredEnabledFlag(cmd *cobra.Command) (bool, error) {
	if !cmd.Flags().Changed("enabled") {
		return false, errs.Config("--enabled must be explicitly supplied as true or false")
	}
	return getBoolFlag(cmd, "enabled"), nil
}

func agent365IntegrationGuidance(status agent365arm.AccountStatus) []string {
	guidance := []string{
		"a365LoggingEnabled controls Agent 365 activity-data collection for the entire Foundry account; there is no per-project or per-agent override.",
		"a365Status is read-only and reflects Agent 365 licensing and tenant consent; changing the logging flag cannot change that status.",
		"Agent 365 storage follows Microsoft Entra tenant geography rather than the Foundry account region.",
	}
	if status.A365LoggingEnabled && status.A365Status != agent365arm.A365StatusEnabled {
		guidance = append(
			guidance,
			"Logging is enabled but collection is not active; resolve licensing or tenant consent before expecting data flow.",
		)
	}
	if !status.A365Status.Known() && status.A365Status != "" {
		guidance = append(
			guidance,
			"ARM returned an unknown a365Status value; preserve it and consult the current service documentation before mutation.",
		)
	}
	return guidance
}

func agent365IntegrationReceiptPath(cmd *cobra.Command, accountName string) (string, error) {
	if path := strings.TrimSpace(getFlag(cmd, "receipt")); path != "" {
		if filepath.IsAbs(path) {
			return filepath.Clean(path), nil
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return "", errs.Config("failed to resolve Agent 365 receipt path: %v", err)
		}
		return filepath.Clean(absolute), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", errs.Config("failed to resolve current directory for Agent 365 receipt: %v", err)
	}
	return receipt.OperationPath(
		filepath.Join(cwd, "agent365-account-"+accountName+".json"),
		"agent365-integration-set",
		accountName,
		time.Now(),
	), nil
}
