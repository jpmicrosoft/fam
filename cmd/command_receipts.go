package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/custommetadata"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/monitorlogs"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

const (
	defaultReceiptLogStream = "Custom-FoundryAgentReceipts"

	envReceiptLogEndpoint = "FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_ENDPOINT"
	envReceiptLogDCRID    = "FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_DCR_ID"
	envReceiptLogStream   = "FOUNDRY_AGENT_MANAGER_RECEIPT_LOG_STREAM"
)

type receiptPublisherSetter interface {
	SetPublisher(context.Context, receipt.Publisher)
}

type configuredReceiptLog struct {
	options monitorlogs.Options
	enabled bool
}

type receiptLogPublisher struct {
	command     *cobra.Command
	profile     azcloud.Profile
	receiptPath string
	options     monitorlogs.Options
}

type commandMetadataContextKey struct{}

func setCommandMetadata(command *cobra.Command, metadata map[string]string) {
	ctx := command.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	command.SetContext(context.WithValue(
		ctx,
		commandMetadataContextKey{},
		custommetadata.Clone(metadata),
	))
}

func commandMetadata(command *cobra.Command) map[string]string {
	if command == nil || command.Context() == nil {
		return nil
	}
	metadata, _ := command.Context().Value(commandMetadataContextKey{}).(map[string]string)
	return custommetadata.Clone(metadata)
}

func (p *receiptLogPublisher) Publish(ctx context.Context, payload []byte) error {
	client, err := newReceiptLogClient(p.command, p.profile, p.options)
	if err != nil {
		return receiptLogRetryError(err, p.receiptPath, p.options)
	}
	if err := client.Publish(ctx, payload); err != nil {
		return receiptLogRetryError(err, p.receiptPath, p.options)
	}
	return nil
}

func cmdReceiptUpload(cmd *cobra.Command, _ []string) error {
	profile, err := azcloud.Resolve(selectedCloudName(cmd, azcloud.AzureCloud))
	if err != nil {
		return err
	}
	configured, err := resolveReceiptLog(cmd, profile)
	if err != nil {
		return err
	}
	if !configured.enabled {
		return errs.WithNextSteps(
			errs.Config(
				"receipt Log Analytics publishing requires --receipt-log-endpoint and --receipt-log-dcr-id",
			),
			fmt.Sprintf(
				"Set the flags or the %s and %s environment variables.",
				envReceiptLogEndpoint,
				envReceiptLogDCRID,
			),
		)
	}
	path, payload, err := readReceiptFile(getFlag(cmd, "file"))
	if err != nil {
		return err
	}
	client, err := newReceiptLogClient(cmd, profile, configured.options)
	if err != nil {
		return err
	}
	result, err := client.Upload(commandContext(cmd), payload)
	if err != nil {
		return receiptLogRetryError(err, path, configured.options)
	}
	return printResult(cmd, struct {
		File                     string `json:"file" yaml:"file"`
		monitorlogs.UploadResult `yaml:",inline"`
	}{
		File:         path,
		UploadResult: result,
	}, fmt.Sprintf(
		"receipt uploaded to Log Analytics: id=%s operation=%s status=%s\n  file: %s\n  dcr: %s\n  stream: %s",
		result.ReceiptID,
		result.Operation,
		result.Status,
		path,
		result.DCRImmutableID,
		result.StreamName,
	))
}

func newManagedReceiptStore(
	cmd *cobra.Command,
	path string,
	cloudName string,
	manifestPath string,
	manifestHash string,
	desiredHash string,
	agentName string,
) (*receipt.Store, error) {
	store := receipt.New(
		path,
		cloudName,
		manifestPath,
		manifestHash,
		desiredHash,
		agentName,
	)
	store.Receipt.Metadata = custommetadata.InterfaceMap(commandMetadata(cmd))
	if err := configureReceiptPublishing(cmd, cloudName, path, store); err != nil {
		return nil, err
	}
	return store, nil
}

func newManagedOperationStore(
	cmd *cobra.Command,
	path string,
	operation string,
	cloudName string,
	manifest receipt.ManifestReference,
	project receipt.ResourceReference,
	agentName string,
) (*receipt.OperationStore, error) {
	store := receipt.NewOperation(
		path,
		operation,
		cloudName,
		manifest,
		project,
		agentName,
	)
	store.Receipt.Metadata = custommetadata.InterfaceMap(commandMetadata(cmd))
	if err := configureReceiptPublishing(cmd, cloudName, path, store); err != nil {
		return nil, err
	}
	return store, nil
}

func configureReceiptPublishing(
	cmd *cobra.Command,
	cloudName string,
	receiptPath string,
	store receiptPublisherSetter,
) error {
	profile, err := azcloud.Resolve(cloudName)
	if err != nil {
		return err
	}
	configured, err := resolveReceiptLog(cmd, profile)
	if err != nil {
		return err
	}
	if !configured.enabled {
		return nil
	}
	store.SetPublisher(commandContext(cmd), &receiptLogPublisher{
		command:     cmd,
		profile:     profile,
		receiptPath: receiptPath,
		options:     configured.options,
	})
	return nil
}

func resolveReceiptLog(
	cmd *cobra.Command,
	profile azcloud.Profile,
) (configuredReceiptLog, error) {
	endpoint := flagOrEnvironment(cmd, "receipt-log-endpoint", envReceiptLogEndpoint)
	dcrID := flagOrEnvironment(cmd, "receipt-log-dcr-id", envReceiptLogDCRID)
	explicitStream := flagOrEnvironment(cmd, "receipt-log-stream", envReceiptLogStream)
	enabled := endpoint != "" || dcrID != "" || explicitStream != ""
	if !enabled {
		return configuredReceiptLog{}, nil
	}
	if endpoint == "" || dcrID == "" {
		return configuredReceiptLog{}, errs.Config(
			"receipt Log Analytics publishing requires both --receipt-log-endpoint and --receipt-log-dcr-id",
		)
	}
	stream := explicitStream
	if stream == "" {
		stream = defaultReceiptLogStream
	}
	options, err := monitorlogs.ValidateOptions(monitorlogs.Options{
		Endpoint:        endpoint,
		DCRImmutableID:  dcrID,
		StreamName:      stream,
		Scope:           profile.MonitorIngestionScope,
		AllowedSuffixes: profile.MonitorIngestionSuffixes,
	})
	if err != nil {
		return configuredReceiptLog{}, err
	}
	return configuredReceiptLog{options: options, enabled: true}, nil
}

func newReceiptLogClient(
	cmd *cobra.Command,
	profile azcloud.Profile,
	options monitorlogs.Options,
) (*monitorlogs.Client, error) {
	credential, err := newCredential(cmd, profile)
	if err != nil {
		return nil, err
	}
	return monitorlogs.NewClient(options, credential, newHTTPClient(cmd))
}

func flagOrEnvironment(cmd *cobra.Command, flagName, environmentName string) string {
	if value := strings.TrimSpace(getFlag(cmd, flagName)); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(environmentName))
}

func readReceiptFile(rawPath string) (string, []byte, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", nil, errs.Config("--file is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", nil, errs.Config("failed to resolve receipt file path: %v", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		return "", nil, errs.Config("failed to open receipt file %q: %v", absolute, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", nil, errs.Config("failed to inspect receipt file %q: %v", absolute, err)
	}
	if !info.Mode().IsRegular() {
		return "", nil, errs.Config("receipt file %q must be a regular file", absolute)
	}
	payload, err := io.ReadAll(io.LimitReader(file, monitorlogs.MaxPayloadBytes+1))
	if err != nil {
		return "", nil, errs.Config("failed to read receipt file %q: %v", absolute, err)
	}
	if len(payload) > monitorlogs.MaxPayloadBytes {
		return "", nil, errs.Config(
			"receipt file %q exceeds the %d-byte Logs ingestion request limit",
			absolute,
			monitorlogs.MaxPayloadBytes,
		)
	}
	return filepath.Clean(absolute), payload, nil
}

func receiptLogRetryError(
	err error,
	receiptPath string,
	options monitorlogs.Options,
) error {
	steps := append([]string(nil), errs.Remediation(err)...)
	steps = append(
		steps,
		fmt.Sprintf("The local receipt remains available at %s.", receiptPath),
		fmt.Sprintf(
			"Retry command: fam receipt upload --file <receipt-path> --receipt-log-endpoint %s --receipt-log-dcr-id %s --receipt-log-stream %s",
			options.Endpoint,
			options.DCRImmutableID,
			options.StreamName,
		),
		"Use the receipt ID to de-duplicate records if the prior POST reached Azure but its response was lost.",
	)
	return errs.WithNextSteps(err, steps...)
}
