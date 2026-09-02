package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/botservice"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/m365publish"
	"foundry-agent-manager/internal/publication"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

const activityProtocolAPIVersion = "2025-05-15-preview"

type publishM365Result struct {
	Status                string `json:"status" yaml:"status"`
	Agent                 string `json:"agent" yaml:"agent"`
	ActiveVersion         string `json:"activeVersion" yaml:"activeVersion"`
	BotServiceARMID       string `json:"botServiceArmId" yaml:"botServiceArmId"`
	BotServiceAction      string `json:"botServiceAction" yaml:"botServiceAction"`
	TeamsChannelAction    string `json:"teamsChannelAction" yaml:"teamsChannelAction"`
	TitleID               string `json:"titleId" yaml:"titleId"`
	PublishScope          string `json:"publishScope" yaml:"publishScope"`
	AdminApprovalRequired bool   `json:"adminApprovalRequired" yaml:"adminApprovalRequired"`
	Receipt               string `json:"receipt" yaml:"receipt"`
}

func cmdPublishM365(cmd *cobra.Command, _ []string) error {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return err
	}
	cfg := resolved.Config
	if !cfg.Cloud.Capabilities.M365Publishing {
		return errs.Config(
			"Microsoft 365 publishing is unavailable in %s; no commercial-cloud fallback is allowed",
			cfg.Cloud.Name,
		)
	}
	publicationPath, err := absoluteInputPath(getFlag(cmd, "publication"))
	if err != nil {
		return err
	}
	pub, err := publication.Load(publicationPath)
	if err != nil {
		return err
	}
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
	if agent == nil {
		return errs.NotFound("agent %q was not found", cfg.Agent.Name)
	}
	if agent.InstanceIdentity == nil || strings.TrimSpace(agent.InstanceIdentity.ClientID) == "" {
		return errs.Conflict(
			"agent %q has no instance_identity.client_id and cannot be published to Microsoft 365",
			cfg.Agent.Name,
		)
	}
	selector := agent.VersionSelectorResolution()
	if selector.IsMalformed() {
		return errs.Foundry(
			"agent %q has a malformed version selector: %s",
			cfg.Agent.Name,
			selector.Problem,
		)
	}
	if len(selector.ActiveVersions) != 1 || !selector.IsPinned() {
		return errs.Conflict(
			"agent %q must pin all traffic to one concrete version before Microsoft 365 publishing",
			cfg.Agent.Name,
		)
	}
	activityEndpoint, err := activityProtocolEndpoint(endpoint, cfg.Agent.Name)
	if err != nil {
		return err
	}

	botSubscription := pub.Microsoft365.BotService.SubscriptionID
	if botSubscription == "" {
		botSubscription = cfg.Project.SubscriptionID
	}
	botResourceGroup := pub.Microsoft365.BotService.ResourceGroup
	if botResourceGroup == "" {
		botResourceGroup = cfg.Project.ResourceGroup
	}
	botClient, err := botservice.NewClient(botservice.ARMOptions{
		ARMEndpoint:    cfg.Cloud.ARMEndpoint,
		ARMScope:       cfg.Cloud.ARMScope,
		SubscriptionID: botSubscription,
		ResourceGroup:  botResourceGroup,
		Credential:     credential,
		HTTPClient:     httpClient,
	})
	if err != nil {
		return err
	}

	receiptPath, err := publicationReceiptPath(cmd, resolved, cfg.Agent.Name)
	if err != nil {
		return err
	}
	store, err := newManagedOperationStore(
		cmd,
		receiptPath,
		"publish-m365",
		cfg.Cloud.Name,
		receipt.ManifestReference{
			Path: resolved.ManifestPath,
			Hash: resolved.ManifestHash,
		},
		receipt.ResourceReference{Name: cfg.Project.Name, Endpoint: endpoint},
		cfg.Agent.Name,
	)
	if err != nil {
		return err
	}
	store.Receipt.Agent.ID = agent.ID
	store.Receipt.Agent.LatestVersionBefore = agent.Versions.Latest.Version
	store.Receipt.Agent.LatestVersionAfter = agent.Versions.Latest.Version
	store.Receipt.Agent.ActiveVersionBefore = selector.ActiveVersions[0]
	store.Receipt.Agent.ActiveVersionAfter = selector.ActiveVersions[0]
	store.Receipt.Agent.SelectorBefore = selectorReceipt(selector)
	store.Receipt.Agent.SelectorAfter = selectorReceipt(selector)
	if err := store.AddStep(
		"publication-preflight",
		"succeeded",
		"agent identity, active version, cloud capability, and publication configuration verified",
	); err != nil {
		return err
	}

	icons, err := pub.LoadIcons()
	if err != nil {
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	allowBotUpdate := pub.Microsoft365.BotService.AllowUpdate || getBoolFlag(cmd, "allow-bot-update")
	botResult, err := botClient.EnsureBotContext(commandContext(cmd), botservice.BotSpec{
		Name:           pub.Microsoft365.BotService.Name,
		DisplayName:    pub.Microsoft365.BotService.DisplayName,
		Endpoint:       activityEndpoint,
		MSAAppID:       agent.InstanceIdentity.ClientID,
		MSAAppTenantID: pub.Microsoft365.BotService.TenantID,
		AllowUpdate:    allowBotUpdate,
	})
	if err != nil {
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "Microsoft.BotService/botServices",
			Name:           pub.Microsoft365.BotService.Name,
			Status:         mutationStatus(err),
			Reconciliation: reconciliationForMutation(err, "inspect the Azure Bot Service resource before retrying"),
		})
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "Microsoft.BotService/botServices",
		Name:         pub.Microsoft365.BotService.Name,
		ID:           botResult.ResourceID,
		Action:       strings.ToLower(string(botResult.Status)),
		Status:       "succeeded",
		CreatedByRun: botResult.Status == botservice.StatusCreated,
	}); err != nil {
		return err
	}

	channelResult, err := botClient.EnsureTeamsChannelContext(
		commandContext(cmd),
		pub.Microsoft365.BotService.Name,
	)
	if err != nil {
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "Microsoft.BotService/botServices/channels",
			Name:           "MsTeamsChannel",
			Status:         mutationStatus(err),
			Reconciliation: reconciliationForMutation(err, "inspect the Teams channel before retrying"),
		})
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "Microsoft.BotService/botServices/channels",
		Name:         "MsTeamsChannel",
		ID:           channelResult.ResourceID,
		Action:       strings.ToLower(string(channelResult.Status)),
		Status:       "succeeded",
		CreatedByRun: channelResult.Status == botservice.StatusCreated,
	}); err != nil {
		return err
	}

	publishRequest := m365publish.Request{
		AgentName:                cfg.Agent.Name,
		AgentDisplayName:         pub.Microsoft365.AgentDisplayName,
		BotServiceARMID:          botResult.ResourceID,
		PublishScope:             pub.Microsoft365.PublishScope,
		AppVersion:               pub.Microsoft365.AppVersion,
		ShortDescription:         pub.Microsoft365.ShortDescription,
		FullDescription:          pub.Microsoft365.FullDescription,
		DeveloperName:            pub.Microsoft365.DeveloperName,
		DeveloperWebsiteURL:      pub.Microsoft365.DeveloperWebsiteURL,
		PrivacyURL:               pub.Microsoft365.PrivacyURL,
		TermsOfUseURL:            pub.Microsoft365.TermsOfUseURL,
		CanRespondWithoutMention: pub.Microsoft365.CanRespondWithoutMention,
		ColorIconBase64:          icons.ColorBase64,
		OutlineIconBase64:        icons.OutlineBase64,
	}
	requestHash, err := publicationRequestHash(publishRequest)
	if err != nil {
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	actionIndex := len(store.Receipt.ExternalActions)
	if err := store.AddExternalAction(receipt.ExternalAction{
		Kind:           "microsoft365-publication",
		System:         "Microsoft 365 and Teams",
		Status:         "started",
		RequestHash:    requestHash,
		Irreversible:   true,
		Compensation:   "No automatic compensation; update or remove the publication through Microsoft 365 administration.",
		Reconciliation: "Do not retry after an ambiguous response. Inspect Foundry publication state and Microsoft 365 catalogs first.",
		StartedAt:      time.Now().UTC(),
	}); err != nil {
		return err
	}
	publisher, err := m365publish.NewClient(m365publish.Options{
		ProjectEndpoint: endpoint,
		Scope:           cfg.Cloud.FoundryScope,
		Credential:      credential,
		HTTPClient:      httpClient,
	})
	if err != nil {
		_ = store.Complete("failed", err)
		return releaseFailure(store.Path, err)
	}
	published, err := publisher.PublishContext(commandContext(cmd), publishRequest)
	if err != nil {
		action := &store.Receipt.ExternalActions[actionIndex]
		action.Status = mutationStatus(err)
		action.Reconciliation = reconciliationForMutation(
			err,
			"inspect the Foundry publication state and Microsoft 365 catalogs before retrying",
		)
		_ = store.Save()
		_ = store.Complete(operationFailureStatus(err), err)
		return releaseFailure(store.Path, err)
	}
	now := time.Now().UTC()
	action := &store.Receipt.ExternalActions[actionIndex]
	action.ResourceID = published.TitleID
	action.CompletedAt = &now
	action.Status = "succeeded"
	operationStatus := "succeeded"
	if published.AdminApprovalRequired {
		action.Status = "pending-admin-approval"
		action.Reconciliation = "A Microsoft 365 administrator must approve the agent in the Microsoft 365 admin center."
		operationStatus = "succeeded-pending-external-actions"
	}
	if err := store.Save(); err != nil {
		return err
	}
	if err := store.Complete(operationStatus, nil); err != nil {
		return err
	}

	result := publishM365Result{
		Status:                operationStatus,
		Agent:                 cfg.Agent.Name,
		ActiveVersion:         selector.ActiveVersions[0],
		BotServiceARMID:       botResult.ResourceID,
		BotServiceAction:      strings.ToLower(string(botResult.Status)),
		TeamsChannelAction:    strings.ToLower(string(channelResult.Status)),
		TitleID:               published.TitleID,
		PublishScope:          published.PublishScope,
		AdminApprovalRequired: published.AdminApprovalRequired,
		Receipt:               store.Path,
	}
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"agent published to Microsoft 365: name=%s active=%s title=%s scope=%s",
		result.Agent,
		result.ActiveVersion,
		result.TitleID,
		result.PublishScope,
	)
	fmt.Fprintf(
		&text,
		"\n  bot service: %s (%s)\n  Teams channel: %s",
		result.BotServiceARMID,
		result.BotServiceAction,
		result.TeamsChannelAction,
	)
	if result.AdminApprovalRequired {
		fmt.Fprint(&text, "\n  external action: Microsoft 365 tenant administrator approval is required")
	}
	fmt.Fprintf(&text, "\n  receipt: %s", result.Receipt)
	return printResult(cmd, result, text.String())
}

func activityProtocolEndpoint(projectEndpoint, agentName string) (string, error) {
	parsed, err := url.Parse(projectEndpoint)
	if err != nil {
		return "", errs.Config("failed to parse project endpoint: %v", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") +
		"/agents/" + url.PathEscape(agentName) + "/endpoint/protocols/activityProtocol"
	query := parsed.Query()
	query.Set("api-version", activityProtocolAPIVersion)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func absoluteInputPath(path string) (string, error) {
	if path == "" {
		return "", errs.Config("--publication is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", errs.Config("failed to resolve --publication path: %v", err)
	}
	return absolute, nil
}

func publicationReceiptPath(
	cmd *cobra.Command,
	resolved *resolvedManifest,
	agentName string,
) (string, error) {
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
		resolved.ManifestPath,
		"publish-m365",
		agentName,
		time.Now(),
	), nil
}

func publicationRequestHash(request m365publish.Request) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", errors.New("failed to hash Microsoft 365 publish request")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func mutationStatus(err error) string {
	if errs.IsAmbiguousMutation(err) {
		return "unknown"
	}
	return "failed"
}

func operationFailureStatus(err error) string {
	if errs.IsAmbiguousMutation(err) {
		return "unknown"
	}
	return "failed-partial"
}

func reconciliationForMutation(err error, ambiguousGuidance string) string {
	if errs.IsAmbiguousMutation(err) {
		return ambiguousGuidance
	}
	return "The preceding successful resource changes were intentionally retained; inspect the receipt before retrying."
}
