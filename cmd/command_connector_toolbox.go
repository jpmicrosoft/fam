package main

import (
	"fmt"
	"net/url"
	"strings"

	"foundry-agent-manager/internal/connection"
	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/receipt"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type connectorToolboxDeployResult struct {
	Connection            string                 `json:"connection" yaml:"connection"`
	ToolboxName           string                 `json:"toolboxName" yaml:"toolboxName"`
	Changed               bool                   `json:"changed" yaml:"changed"`
	Version               string                 `json:"version,omitempty" yaml:"version,omitempty"`
	DefaultVersion        string                 `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
	Promoted              bool                   `json:"promoted" yaml:"promoted"`
	ToolboxEndpoint       string                 `json:"toolboxEndpoint" yaml:"toolboxEndpoint"`
	PromptAttachmentReady bool                   `json:"promptAttachmentReady" yaml:"promptAttachmentReady"`
	PromptAttachment      map[string]interface{} `json:"promptAttachment" yaml:"promptAttachment"`
	HostedEnvironment     map[string]string      `json:"hostedEnvironment" yaml:"hostedEnvironment"`
	Receipt               string                 `json:"receipt" yaml:"receipt"`
}

func cmdConnectorToolboxDeploy(cmd *cobra.Command, _ []string) (returnErr error) {
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
	mcpTool, err := connection.ManagedConnectorMCPTool(state)
	if err != nil {
		return err
	}
	if mcpTool == nil {
		return errs.Conflict(
			"managed connector %q must be Connected with configured actions before it can be added to a Toolbox",
			connectionName,
		)
	}
	definition, err := connectorToolboxDefinition(
		getFlag(cmd, "toolbox-name"),
		getFlag(cmd, "toolbox-description"),
		connectionName,
		mcpTool,
		runtime.Resolved.BaseDir,
	)
	if err != nil {
		return err
	}
	destinations, err := tools.ToolboxDestinations([]tools.ToolboxDefinition{definition})
	if err != nil {
		return err
	}
	if _, err := approveToolDestinations(cmd, runtime.Resolved.Config, destinations); err != nil {
		return err
	}
	projectEndpoint, err := resolveProjectEndpoint(
		cmd,
		runtime.Resolved.Config,
		runtime.Credential,
		runtime.HTTPClient,
	)
	if err != nil {
		return err
	}
	client := newFoundryClient(
		projectEndpoint,
		runtime.Resolved.Config,
		runtime.Credential,
		runtime.HTTPClient,
	)
	toolboxEndpoint, err := tools.ToolboxEndpoint(projectEndpoint, definition.Name, "")
	if err != nil {
		return err
	}
	promptConnection := strings.TrimSpace(getFlag(cmd, "toolbox-project-connection"))
	promptReady := promptConnection != ""
	if promptReady {
		found, err := connection.GetContext(
			ctx,
			&runtime.Resolved.Config.Project,
			runtime.APIVersion,
			promptConnection,
			runtime.Credential,
			runtime.HTTPClient,
		)
		if err != nil {
			return err
		}
		if !found.Exists {
			return errs.NotFound(
				"Prompt Toolbox project connection %q was not found",
				promptConnection,
			)
		}
		connectionTarget, _ := found.Properties["target"].(string)
		if !sameToolboxConnectionTarget(connectionTarget, toolboxEndpoint) {
			return errs.Config(
				"Prompt Toolbox project connection %q target %q does not match %q",
				promptConnection,
				connectionTarget,
				toolboxEndpoint,
			)
		}
	} else {
		promptConnection = "<PROJECT_CONNECTION_ID>"
	}
	store, err := newToolboxOperationStore(
		cmd,
		runtime.Resolved,
		definition,
		projectEndpoint,
		"connector-toolbox-deploy",
	)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete("failed", returnErr)
		}
	}()

	logical, err := client.GetToolboxContext(ctx, definition.Name)
	if err != nil {
		return err
	}
	versions, err := client.ListToolboxVersionsContext(ctx, definition.Name)
	if err != nil {
		return err
	}
	latest := latestToolboxVersion(versions)
	var (
		version        string
		changed        bool
		defaultVersion string
	)
	if logical != nil {
		defaultVersion = logical.DefaultVersion
	}
	if getBoolFlag(cmd, "if-changed") && latest != nil {
		equal, err := tools.ToolboxPayloadEqual(toolboxVersionMap(latest), definition)
		if err != nil {
			return err
		}
		if equal {
			version = latest.Version
		}
	}
	if version == "" {
		created, err := client.CreateToolboxVersionContext(
			ctx,
			definition.Name,
			definition.Payload(),
			definition.PreviewHeader(),
		)
		if err != nil {
			if errs.IsKind(err, "ambiguous-mutation") {
				_ = store.AddResource(receipt.ResourceChange{
					Kind:           "foundry-toolbox-version",
					Name:           definition.Name,
					Action:         "create",
					Status:         "uncertain",
					Reconciliation: "List Toolbox versions and compare the newest managed payload before retrying.",
				})
			}
			return err
		}
		version = created.Version
		changed = true
		if err := store.AddResource(receipt.ResourceChange{
			Kind:         "foundry-toolbox-version",
			Name:         definition.Name,
			ID:           created.ID,
			Action:       "create",
			Status:       "succeeded",
			CreatedByRun: true,
		}); err != nil {
			return err
		}
		refreshed, err := client.GetToolboxContext(ctx, definition.Name)
		if err != nil {
			return errs.AmbiguousMutation(err)
		}
		if refreshed == nil || refreshed.DefaultVersion == "" {
			return errs.AmbiguousMutation(
				errs.Foundry(
					"Toolbox %q version creation succeeded but default_version could not be reconciled",
					definition.Name,
				),
			)
		}
		defaultVersion = refreshed.DefaultVersion
	}

	promoted := false
	if getBoolFlag(cmd, "promote") && defaultVersion != version {
		if !getBoolFlag(cmd, "yes") {
			return errs.Config(
				"connector-toolbox-deploy --promote changes the consumer default; rerun with --yes",
			)
		}
		if err := client.PromoteToolboxVersionContext(ctx, definition.Name, version); err != nil {
			return err
		}
		observed, err := client.GetToolboxContext(ctx, definition.Name)
		if err != nil {
			return errs.AmbiguousMutation(err)
		}
		if observed == nil || observed.DefaultVersion != version {
			return errs.AmbiguousMutation(
				errs.Conflict(
					"Toolbox %q promotion to version %s could not be reconciled",
					definition.Name,
					version,
				),
			)
		}
		defaultVersion = version
		promoted = true
		changed = true
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry-toolbox",
			Name:   definition.Name,
			Action: "promote",
			Status: "succeeded",
		}); err != nil {
			return err
		}
	}
	if !changed {
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry-toolbox-version",
			Name:   definition.Name,
			Action: "unchanged",
			Status: "succeeded",
		}); err != nil {
			return err
		}
	}
	result := connectorToolboxDeployResult{
		Connection:            connectionName,
		ToolboxName:           definition.Name,
		Changed:               changed,
		Version:               version,
		DefaultVersion:        defaultVersion,
		Promoted:              promoted,
		ToolboxEndpoint:       toolboxEndpoint,
		PromptAttachmentReady: promptReady,
		PromptAttachment: map[string]interface{}{
			"type":                  "toolbox",
			"name":                  definition.Name,
			"project_connection_id": promptConnection,
			"require_approval":      "always",
		},
		HostedEnvironment: map[string]string{
			"TOOLBOX_NAME":          definition.Name,
			"TOOLBOX_APPROVAL_MODE": "always_require",
		},
		Receipt: store.Path,
	}
	status := "succeeded"
	if !changed {
		status = "unchanged"
	}
	if err := store.Complete(status, nil); err != nil {
		return err
	}
	text := fmt.Sprintf(
		"managed connector Toolbox ready: connection=%s toolbox=%s version=%s default=%s",
		connectionName,
		definition.Name,
		version,
		defaultVersion,
	)
	if !promptReady {
		text += "\n  Prompt attachment requires an existing project connection; replace <PROJECT_CONNECTION_ID> or rerun with --toolbox-project-connection."
	}
	return printResult(cmd, result, text)
}

func sameToolboxConnectionTarget(actual, expected string) bool {
	normalize := func(raw string) (string, bool) {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil ||
			!strings.EqualFold(parsed.Scheme, "https") ||
			parsed.Hostname() == "" ||
			parsed.User != nil ||
			parsed.RawQuery != "" ||
			parsed.Fragment != "" {
			return "", false
		}
		host := strings.ToLower(parsed.Hostname())
		if port := parsed.Port(); port != "" && port != "443" {
			host += ":" + port
		}
		path := strings.TrimSuffix(parsed.EscapedPath(), "/")
		return "https://" + host + path, true
	}
	actualNormalized, actualOK := normalize(actual)
	expectedNormalized, expectedOK := normalize(expected)
	return actualOK && expectedOK && actualNormalized == expectedNormalized
}

func connectorToolboxDefinition(
	name string,
	description string,
	connectionName string,
	mcpTool map[string]interface{},
	baseDir string,
) (tools.ToolboxDefinition, error) {
	if strings.TrimSpace(description) == "" {
		description = "Managed connector " + connectionName
	}
	definitions, err := tools.BuildToolboxes(
		[]map[string]interface{}{{
			"name":        name,
			"description": description,
			"tools":       []interface{}{mcpTool},
		}},
		baseDir,
	)
	if err != nil {
		return tools.ToolboxDefinition{}, err
	}
	return definitions[0], nil
}
