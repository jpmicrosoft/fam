package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"foundry-agent-manager/internal/azcloud"
	"foundry-agent-manager/internal/config"
	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/receipt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/spf13/cobra"
)

type connectionRuntime struct {
	Resolved   *resolvedManifest
	Credential azcore.TokenCredential
	HTTPClient connection.HTTPClient
	APIVersion string
}

func newConnectionRuntime(cmd *cobra.Command) (*connectionRuntime, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, err
	}
	apiVersion := strings.TrimSpace(getFlag(cmd, "connection-api-version"))
	if apiVersion == "" {
		apiVersion = config.DefaultConnectionAPIVersion
	}
	return &connectionRuntime{
		Resolved:   resolved,
		Credential: credential,
		HTTPClient: newHTTPClient(cmd),
		APIVersion: apiVersion,
	}, nil
}

func newManagedConnectorRuntime(cmd *cobra.Command) (*connectionRuntime, error) {
	if !getBoolFlag(cmd, "accept-preview") {
		return nil, errs.Config(
			"managed MCP connectors are preview; pass --accept-preview after reviewing the connector, publisher, actions, and data boundary",
		)
	}
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, err
	}
	if resolved.Config.Cloud.Name != azcloud.AzureCloud {
		return nil, errs.Config(
			"managed MCP connectors are unavailable in %s; no commercial-cloud fallback is allowed",
			resolved.Config.Cloud.Name,
		)
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, err
	}
	apiVersion := strings.TrimSpace(getFlag(cmd, "connection-api-version"))
	if apiVersion == "" {
		apiVersion = config.DefaultConnectionAPIVersion
	}
	return &connectionRuntime{
		Resolved: resolved, Credential: credential,
		HTTPClient: newHTTPClient(cmd), APIVersion: apiVersion,
	}, nil
}

func cmdConnectionList(cmd *cobra.Command, _ []string) error {
	runtime, err := newConnectionRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := connection.ListContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	return printResult(cmd, result, "listed project connections")
}

func cmdConnectionShow(cmd *cobra.Command, _ []string) error {
	runtime, err := newConnectionRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "connection")
	result, err := connection.GetContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		name,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !result.Exists {
		return errs.NotFound("connection %q was not found", name)
	}
	return printResult(cmd, result, "showed project connection")
}

func cmdConnectionCreate(cmd *cobra.Command, args []string) error {
	return cmdConnectionUpsert(cmd, args, false)
}

func cmdConnectionUpdate(cmd *cobra.Command, args []string) error {
	return cmdConnectionUpsert(cmd, args, true)
}

func cmdConnectionUpsert(cmd *cobra.Command, _ []string, requireExisting bool) error {
	runtime, err := newConnectionRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "connection")
	current, err := connection.GetContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		name,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if requireExisting && !current.Exists {
		return errs.NotFound("connection %q does not exist; use fam project connection create", name)
	}
	if !requireExisting && current.Exists {
		return errs.Config("connection %q already exists; use fam project connection update", name)
	}
	definition, secrets, err := connectionDefinitionFromFlags(cmd, runtime.Resolved.BaseDir)
	if err != nil {
		return err
	}
	result, err := connection.UpsertContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		definition,
		runtime.Credential,
		runtime.HTTPClient,
		secrets...,
	)
	if err != nil {
		return err
	}
	action := "connection-create"
	if requireExisting {
		action = "connection-update"
	}
	if err := writeConnectionReceipt(runtime, cmd, action, name); err != nil {
		return err
	}
	return printResult(cmd, map[string]interface{}{
		"name": result.Name, "created": !requireExisting, "updated": requireExisting,
	}, action+" succeeded")
}

func cmdConnectionDelete(cmd *cobra.Command, _ []string) error {
	runtime, err := newConnectionRuntime(cmd)
	if err != nil {
		return err
	}
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("connection-delete is destructive; rerun with --yes")
	}
	name := getFlag(cmd, "connection")
	deleted, err := connection.DeleteContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		name,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if err := writeConnectionReceipt(runtime, cmd, "connection-delete", name); err != nil {
		return err
	}
	return printResult(
		cmd,
		map[string]interface{}{"name": name, "deleted": deleted},
		"connection-delete succeeded",
	)
}

func cmdConnectorList(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := connection.ListConnectorCatalogContext(
		commandContext(cmd),
		connection.ConnectorCatalogQuery{
			Search: getFlag(cmd, "search"), PageSize: getIntFlag(cmd, "page-size"),
			Skip: getIntFlag(cmd, "skip"),
		},
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"managed connector catalog: returned=%d total=%d skip=%d",
			len(result.Connectors),
			result.TotalCount,
			result.Skip,
		),
	)
}

func cmdConnectorShow(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := connection.GetConnectorCatalogContext(
		commandContext(cmd),
		getFlag(cmd, "connector-name"),
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"managed connector: name=%s auth=%s actions=%d",
			result.Name,
			result.AuthType,
			len(result.Actions),
		),
	)
}

func cmdConnectorCreate(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	region := runtime.Resolved.Config.Project.Location
	if strings.TrimSpace(region) == "" {
		return errs.Config(
			"connector-create requires project.location so managed connector regional availability can be verified",
		)
	}
	if !connection.SupportsManagedConnectorRegion(region) {
		return errs.Config(
			"managed MCP connectors are not documented for project region %q; supported regions: %s",
			region,
			strings.Join(connection.ManagedConnectorRegions, ", "),
		)
	}
	ctx := commandContext(cmd)
	connectionName := getFlag(cmd, "connection")
	current, err := connection.GetContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if current.Exists {
		return errs.Config("connection %q already exists; use fam connector status or fam connector configure", connectionName)
	}
	catalog, err := connection.GetConnectorCatalogContext(
		ctx,
		getFlag(cmd, "connector-name"),
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !strings.EqualFold(catalog.AuthType, "OAuth2") {
		return errs.Config(
			"connector %q uses %s authentication; the Foundry managed MCP flow currently supports OAuth2 connectors only",
			catalog.Name,
			catalog.AuthType,
		)
	}
	state, err := connection.UpsertManagedConnectorContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connection.ManagedConnectorDefinition{
			Name: connectionName, ConnectorName: catalog.Name, ToolEntityID: catalog.EntityID,
		},
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if err := writeConnectionReceipt(runtime, cmd, "connector-create", connectionName); err != nil {
		return err
	}
	output, err := managedConnectorOutput(state)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		map[string]interface{}{"created": true, "result": output},
		fmt.Sprintf(
			"managed connector created: connection=%s connector=%s status=%s",
			connectionName,
			catalog.Name,
			state.OverallStatus,
		),
	)
}

func cmdConnectorConsent(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	ctx := commandContext(cmd)
	connectionName := getFlag(cmd, "connection")
	state, err := connection.GetManagedConnectorContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !state.Exists {
		return errs.NotFound("managed connector connection %q was not found", connectionName)
	}
	result, err := connection.CreateConnectorConsentLinkContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		connection.ConnectorConsentRequest{
			ObjectID: getFlag(cmd, "object-id"), TenantID: getFlag(cmd, "tenant-id"),
			RedirectURL: getFlag(cmd, "redirect-url"),
		},
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		"Open this short-lived connector consent URL in a browser:\n  "+result.Link,
	)
}

func cmdConnectorActions(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	ctx := commandContext(cmd)
	connectorName := getFlag(cmd, "connector-name")
	if operationName := strings.TrimSpace(getFlag(cmd, "operation")); operationName != "" {
		result, err := connection.GetConnectorOperationContext(
			ctx,
			&runtime.Resolved.Config.Project,
			connectorName,
			operationName,
			runtime.Credential,
			runtime.HTTPClient,
		)
		if err != nil {
			return err
		}
		return printResult(
			cmd,
			result,
			fmt.Sprintf(
				"connector operation: connector=%s operation=%s parameters=%d",
				connectorName,
				result.Name,
				len(result.InputsDefinition.Properties),
			),
		)
	}
	result, err := connection.ListConnectorOperationsContext(
		ctx,
		&runtime.Resolved.Config.Project,
		connectorName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf("connector operations: connector=%s count=%d", connectorName, len(result)),
	)
}

func cmdConnectorConfigure(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	selectedRaw, err := cmd.Flags().GetStringSlice("operation")
	if err != nil {
		return errs.Config("failed to read --operation: %v", err)
	}
	selected := nonEmptyUnique(selectedRaw)
	if len(selected) == 0 {
		return errs.Config("connector-configure requires at least one --operation")
	}
	ctx := commandContext(cmd)
	connectionName := getFlag(cmd, "connection")
	current, err := connection.GetManagedConnectorContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !current.Exists {
		return errs.NotFound("managed connector connection %q was not found", connectionName)
	}
	operations := make([]connection.ConnectorOperation, 0, len(selected))
	for _, operationName := range selected {
		operation, err := connection.GetConnectorOperationContext(
			ctx,
			&runtime.Resolved.Config.Project,
			current.ConnectorName,
			operationName,
			runtime.Credential,
			runtime.HTTPClient,
		)
		if err != nil {
			return err
		}
		operations = append(operations, operation)
	}
	desired, err := connection.BuildManagedConnectorMCPConfig(
		connectionName,
		current.ConnectorName,
		getFlag(cmd, "connector-description"),
		operations,
	)
	if err != nil {
		return err
	}
	if sameJSONObject(current.MCPServerConfig, desired) {
		output, outputErr := managedConnectorOutput(current)
		if outputErr != nil {
			return outputErr
		}
		return printResult(
			cmd,
			map[string]interface{}{
				"changed": false, "result": output, "operations": selected,
			},
			"managed connector actions are unchanged",
		)
	}
	if current.ActionsConfigured && !getBoolFlag(cmd, "yes") {
		return errs.Config(
			"connector-configure replaces the complete registered action set; rerun with every intended --operation and --yes",
		)
	}
	updated, err := connection.UpsertManagedConnectorContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connection.ManagedConnectorDefinition{
			Name: connectionName, ConnectorName: current.ConnectorName,
			ToolEntityID: current.ToolEntityID, MCPServerConfig: desired,
		},
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if err := writeConnectionReceipt(runtime, cmd, "connector-configure", connectionName); err != nil {
		return err
	}
	output, err := managedConnectorOutput(updated)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		map[string]interface{}{
			"changed": true, "result": output, "operations": selected,
		},
		fmt.Sprintf(
			"managed connector actions registered: connection=%s operations=%d status=%s",
			connectionName,
			len(selected),
			updated.OverallStatus,
		),
	)
}

func cmdConnectorStatus(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	state, err := connection.GetManagedConnectorContext(
		commandContext(cmd),
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		getFlag(cmd, "connection"),
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !state.Exists {
		return errs.NotFound("managed connector connection %q was not found", getFlag(cmd, "connection"))
	}
	output, err := managedConnectorOutput(state)
	if err != nil {
		return err
	}
	return printResult(
		cmd,
		output,
		fmt.Sprintf(
			"managed connector status: connection=%s connector=%s status=%s actions=%t",
			state.Name,
			state.ConnectorName,
			state.OverallStatus,
			state.ActionsConfigured,
		),
	)
}

func cmdConnectorWait(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	timeout := getDurationFlag(cmd, "connector-timeout")
	interval := getDurationFlag(cmd, "connector-interval")
	if timeout <= 0 || interval <= 0 {
		return errs.Config("--connector-timeout and --connector-interval must be positive")
	}
	ctx, cancel := context.WithTimeout(commandContext(cmd), timeout)
	defer cancel()
	connectionName := getFlag(cmd, "connection")
	var state connection.ManagedConnectorState
	for {
		state, err = connection.GetManagedConnectorContext(
			ctx,
			&runtime.Resolved.Config.Project,
			runtime.APIVersion,
			connectionName,
			runtime.Credential,
			runtime.HTTPClient,
		)
		if err != nil {
			return err
		}
		if !state.Exists {
			return errs.NotFound("managed connector connection %q was not found", connectionName)
		}
		if strings.EqualFold(state.OverallStatus, "Connected") {
			output, outputErr := managedConnectorOutput(state)
			if outputErr != nil {
				return outputErr
			}
			return printResult(
				cmd,
				output,
				fmt.Sprintf("managed connector connected: connection=%s target=%s", state.Name, state.Target),
			)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errs.Transient(
				"managed connector %q did not reach Connected within %s; current status is %s",
				connectionName,
				timeout,
				state.OverallStatus,
			)
		case <-timer.C:
		}
	}
}

func cmdConnectorDelete(cmd *cobra.Command, _ []string) error {
	runtime, err := newManagedConnectorRuntime(cmd)
	if err != nil {
		return err
	}
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("connector-delete is destructive; rerun with --yes")
	}
	ctx := commandContext(cmd)
	connectionName := getFlag(cmd, "connection")
	state, err := connection.GetManagedConnectorContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if !state.Exists {
		return printResult(
			cmd,
			map[string]interface{}{"name": connectionName, "deleted": false},
			"managed connector was already absent",
		)
	}
	deleted, err := connection.DeleteContext(
		ctx,
		&runtime.Resolved.Config.Project,
		runtime.APIVersion,
		connectionName,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	if err := writeConnectionReceipt(runtime, cmd, "connector-delete", connectionName); err != nil {
		return err
	}
	return printResult(
		cmd,
		map[string]interface{}{"name": connectionName, "deleted": deleted},
		"managed connector deleted",
	)
}

func sameJSONObject(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return strings.TrimSpace(left) == strings.TrimSpace(right)
	}
	var leftValue interface{}
	var rightValue interface{}
	if json.Unmarshal([]byte(left), &leftValue) != nil ||
		json.Unmarshal([]byte(right), &rightValue) != nil {
		return left == right
	}
	leftData, _ := json.Marshal(leftValue)
	rightData, _ := json.Marshal(rightValue)
	return string(leftData) == string(rightData)
}

func nonEmptyUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func managedConnectorOutput(
	state connection.ManagedConnectorState,
) (map[string]interface{}, error) {
	result := map[string]interface{}{"connector": state}
	tool, err := connection.ManagedConnectorMCPTool(state)
	if err != nil {
		return nil, err
	}
	if tool != nil {
		result["mcpTool"] = tool
	}
	return result, nil
}

func connectionDefinitionFromFlags(
	cmd *cobra.Command,
	baseDir string,
) (connection.Definition, []string, error) {
	credentials, secrets, err := connectionCredentialsFromFlags(cmd, baseDir)
	if err != nil {
		return connection.Definition{}, nil, err
	}
	metadata, err := readStringMapFile(baseDir, getFlag(cmd, "metadata-file"), "connection metadata")
	if err != nil {
		return connection.Definition{}, nil, err
	}
	return connection.Definition{
		Name:          getFlag(cmd, "connection"),
		Category:      getFlag(cmd, "connection-type"),
		Target:        getFlag(cmd, "target"),
		AuthType:      getFlag(cmd, "auth-type"),
		Audience:      getFlag(cmd, "audience"),
		IsSharedToAll: getBoolFlag(cmd, "shared"),
		Metadata:      metadata,
		Credentials:   credentials,
	}, secrets, nil
}

func connectionCredentialsFromFlags(
	cmd *cobra.Command,
	baseDir string,
) (map[string]interface{}, []string, error) {
	credentialsFile := strings.TrimSpace(getFlag(cmd, "credentials-file"))
	secretFile := strings.TrimSpace(getFlag(cmd, "secret-file"))
	secretEnvironment := strings.TrimSpace(getFlag(cmd, "secret-env"))
	selected := 0
	for _, value := range []string{credentialsFile, secretFile, secretEnvironment} {
		if value != "" {
			selected++
		}
	}
	if selected > 1 {
		return nil, nil, errs.Config(
			"--credentials-file, --secret-file, and --secret-env are mutually exclusive",
		)
	}
	if credentialsFile != "" {
		path := resolveRelativePath(baseDir, credentialsFile)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, errs.Config("failed to read connection credentials file %s: %v", path, err)
		}
		var credentials map[string]interface{}
		if err := json.Unmarshal(data, &credentials); err != nil {
			return nil, nil, errs.Config("connection credentials file must contain a JSON object: %v", err)
		}
		if len(credentials) == 0 {
			return nil, nil, errs.Config("connection credentials file must not be empty")
		}
		return credentials, collectStringSecrets(credentials), nil
	}
	if secretFile != "" || secretEnvironment != "" {
		if !strings.EqualFold(getFlag(cmd, "auth-type"), "ApiKey") {
			return nil, nil, errs.Config("--secret-file and --secret-env require --auth-type ApiKey")
		}
		var value string
		var source string
		if secretFile != "" {
			source = resolveRelativePath(baseDir, secretFile)
			data, err := os.ReadFile(source)
			if err != nil {
				return nil, nil, errs.Config("failed to read connection secret file %s: %v", source, err)
			}
			value = strings.TrimRight(string(data), "\r\n")
		} else {
			source = "environment variable " + secretEnvironment
			value = os.Getenv(secretEnvironment)
		}
		if value == "" {
			return nil, nil, errs.Config("connection secret from %s is empty", source)
		}
		return map[string]interface{}{"key": value}, []string{value}, nil
	}
	if strings.EqualFold(getFlag(cmd, "auth-type"), "ApiKey") {
		return nil, nil, errs.Config("ApiKey connections require --secret-file or --secret-env")
	}
	return map[string]interface{}{}, nil, nil
}

func readStringMapFile(baseDir, name, label string) (map[string]string, error) {
	if strings.TrimSpace(name) == "" {
		return nil, nil
	}
	path := resolveRelativePath(baseDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Config("failed to read %s file %s: %v", label, path, err)
	}
	var result map[string]string
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, errs.Config("%s file must contain a JSON object with string values: %v", label, err)
	}
	return result, nil
}

func collectStringSecrets(value interface{}) []string {
	var result []string
	switch typed := value.(type) {
	case string:
		if typed != "" {
			result = append(result, typed)
		}
	case map[string]interface{}:
		for _, nested := range typed {
			result = append(result, collectStringSecrets(nested)...)
		}
	case []interface{}:
		for _, nested := range typed {
			result = append(result, collectStringSecrets(nested)...)
		}
	}
	return result
}

func resolveRelativePath(baseDir, name string) string {
	if filepath.IsAbs(name) {
		return filepath.Clean(name)
	}
	return filepath.Clean(filepath.Join(baseDir, name))
}

func writeConnectionReceipt(
	runtime *connectionRuntime,
	cmd *cobra.Command,
	operation string,
	name string,
) error {
	path := strings.TrimSpace(getFlag(cmd, "receipt"))
	if path == "" {
		path = receipt.OperationPath(runtime.Resolved.ManifestPath, operation, name, time.Now())
	} else if !filepath.IsAbs(path) {
		path = resolveRelativePath(runtime.Resolved.BaseDir, path)
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		operation,
		runtime.Resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path: runtime.Resolved.ManifestPath,
			Hash: runtime.Resolved.ManifestHash,
		},
		receipt.ResourceReference{
			Name:     runtime.Resolved.Config.Project.Name,
			Endpoint: runtime.Resolved.Config.Project.Endpoint,
		},
		"",
	)
	if err != nil {
		return err
	}
	store.Receipt.Resources = append(store.Receipt.Resources, receipt.ResourceChange{
		Kind: "foundry_project_connection", Name: name, Action: operation, Status: "succeeded",
	})
	if err := store.AddStep(operation, "succeeded", name); err != nil {
		return err
	}
	return store.Complete("succeeded", nil)
}
