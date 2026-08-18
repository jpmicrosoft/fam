package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/grounding"
	"foundry-agent-manager/internal/receipt"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type toolboxValidationResult struct {
	Cloud     string   `json:"cloud" yaml:"cloud"`
	Toolboxes []string `json:"toolboxes" yaml:"toolboxes"`
}

type toolboxPlanItem struct {
	Name                string   `json:"name" yaml:"name"`
	Action              string   `json:"action" yaml:"action"`
	Tools               int      `json:"tools" yaml:"tools"`
	Skills              int      `json:"skills" yaml:"skills"`
	RequiresPreview     bool     `json:"requiresPreview" yaml:"requiresPreview"`
	PreviewCapabilities []string `json:"previewCapabilities,omitempty" yaml:"previewCapabilities,omitempty"`
	PreviewFeatures     []string `json:"previewFeatures,omitempty" yaml:"previewFeatures,omitempty"`
}

type toolboxPlanResult struct {
	Cloud     string            `json:"cloud" yaml:"cloud"`
	Toolboxes []toolboxPlanItem `json:"toolboxes" yaml:"toolboxes"`
}

type toolboxDeployResult struct {
	Name           string `json:"name" yaml:"name"`
	Changed        bool   `json:"changed" yaml:"changed"`
	Version        string `json:"version,omitempty" yaml:"version,omitempty"`
	DefaultVersion string `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
	Staged         bool   `json:"staged" yaml:"staged"`
	Receipt        string `json:"receipt" yaml:"receipt"`
}

type toolboxStatusResult struct {
	Name           string `json:"name" yaml:"name"`
	Exists         bool   `json:"exists" yaml:"exists"`
	ID             string `json:"id,omitempty" yaml:"id,omitempty"`
	DefaultVersion string `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
}

type toolboxVersionsResult struct {
	Name           string                   `json:"name" yaml:"name"`
	DefaultVersion string                   `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
	Versions       []foundry.ToolboxVersion `json:"versions" yaml:"versions"`
}

type toolboxMutationResult struct {
	Action         string `json:"action" yaml:"action"`
	Name           string `json:"name" yaml:"name"`
	Version        string `json:"version" yaml:"version"`
	Changed        bool   `json:"changed" yaml:"changed"`
	DryRun         bool   `json:"dryRun" yaml:"dryRun"`
	DefaultVersion string `json:"defaultVersion,omitempty" yaml:"defaultVersion,omitempty"`
	Receipt        string `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

func cmdToolboxValidate(cmd *cobra.Command, _ []string) error {
	resolved, definitions, err := resolveToolboxDefinitions(cmd, true)
	if err != nil {
		return err
	}
	if _, err := tools.ToolboxDestinations(definitions); err != nil {
		return err
	}
	result := toolboxValidationResult{Cloud: resolved.Config.Cloud.Name}
	for _, definition := range definitions {
		result.Toolboxes = append(result.Toolboxes, tools.DescribeToolbox(definition))
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf("validated %d Toolbox definition(s)", len(definitions)),
	)
}

func cmdToolboxPlan(cmd *cobra.Command, _ []string) error {
	resolved, definitions, err := resolveToolboxDefinitions(cmd, true)
	if err != nil {
		return err
	}
	if _, err := tools.ToolboxDestinations(definitions); err != nil {
		return err
	}
	result := toolboxPlanResult{Cloud: resolved.Config.Cloud.Name}
	var text strings.Builder
	fmt.Fprintf(&text, "Toolbox plan:")
	for _, definition := range definitions {
		item := toolboxPlanItem{
			Name:                definition.Name,
			Action:              "create immutable version; first version becomes default, later versions remain staged until promoted",
			Tools:               len(definition.Tools),
			Skills:              len(definition.Skills),
			RequiresPreview:     definition.RequiresPreview,
			PreviewCapabilities: append([]string(nil), definition.PreviewCapabilities...),
			PreviewFeatures:     append([]string(nil), definition.PreviewFeatures...),
		}
		result.Toolboxes = append(result.Toolboxes, item)
		fmt.Fprintf(
			&text,
			"\n  %s: tools=%d skills=%d preview=%t",
			item.Name,
			item.Tools,
			item.Skills,
			item.RequiresPreview,
		)
	}
	return printResult(cmd, result, text.String())
}

func cmdToolboxDeploy(cmd *cobra.Command, _ []string) (returnErr error) {
	resolved, definitions, err := resolveToolboxDefinitions(cmd, false)
	if err != nil {
		return err
	}
	definition := definitions[0]
	if definition.RequiresPreview && !getBoolFlag(cmd, "accept-preview") {
		return errs.Config(
			"Toolbox %q uses preview capabilities; pass --accept-preview after reviewing: %s",
			definition.Name,
			emptyValue(strings.Join(definition.PreviewCapabilities, ", ")),
		)
	}
	destinations, err := tools.ToolboxDestinations(definitions)
	if err != nil {
		return err
	}
	if _, err := approveToolDestinations(cmd, resolved.Config, destinations); err != nil {
		return err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, resolved.Config, credential, httpClient)
	if err != nil {
		return err
	}
	client := newFoundryClient(endpoint, resolved.Config, credential, httpClient)
	store, err := newToolboxOperationStore(cmd, resolved, definition, endpoint, "toolbox-deploy")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete("failed", returnErr)
		}
	}()
	groundingDefinitions, err := grounding.Build(
		resolved.Config.Grounding,
		resolved.BaseDir,
		true,
	)
	if err != nil {
		return err
	}
	definition, err = resolveToolboxManagedGrounding(
		commandContext(cmd),
		client,
		groundingDefinitions,
		definition,
	)
	if err != nil {
		return err
	}
	if err := store.AddStep(
		"preflight",
		"succeeded",
		fmt.Sprintf("%d external destination(s) approved", len(destinations)),
	); err != nil {
		return err
	}

	logical, err := client.GetToolboxContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	versions, err := client.ListToolboxVersionsContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	latest := latestToolboxVersion(versions)
	if getBoolFlag(cmd, "if-changed") && latest != nil {
		equal, compareErr := tools.ToolboxPayloadEqual(toolboxVersionMap(latest), definition)
		if compareErr != nil {
			return compareErr
		}
		if equal {
			defaultVersion := ""
			if logical != nil {
				defaultVersion = logical.DefaultVersion
			}
			result := toolboxDeployResult{
				Name:           definition.Name,
				Version:        latest.Version,
				DefaultVersion: defaultVersion,
				Receipt:        store.Path,
			}
			if err := store.AddResource(receipt.ResourceChange{
				Kind:   "foundry-toolbox-version",
				Name:   definition.Name,
				Action: "unchanged",
				Status: "succeeded",
			}); err != nil {
				return err
			}
			if err := store.Complete("unchanged", nil); err != nil {
				return err
			}
			return printResult(
				cmd,
				result,
				fmt.Sprintf("Toolbox unchanged: name=%s latest=%s", definition.Name, latest.Version),
			)
		}
	}

	created, err := client.CreateToolboxVersionContext(
		commandContext(cmd),
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
			_ = store.Complete("failed-partial", err)
		}
		return err
	}
	updatedLogical, reconcileErr := client.GetToolboxContext(
		commandContext(cmd),
		definition.Name,
	)
	if reconcileErr != nil || updatedLogical == nil || updatedLogical.DefaultVersion == "" {
		if reconcileErr == nil {
			reconcileErr = errs.Foundry(
				"Toolbox %q create succeeded but the logical resource did not report default_version",
				definition.Name,
			)
		}
		ambiguous := errs.AmbiguousMutation(reconcileErr)
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "foundry-toolbox-version",
			Name:           definition.Name,
			ID:             created.ID,
			Action:         "create",
			Status:         "uncertain",
			CreatedByRun:   true,
			Reconciliation: "Read the logical Toolbox and list versions before retrying.",
		})
		_ = store.Complete("failed-partial", ambiguous)
		return ambiguous
	}
	defaultVersion := updatedLogical.DefaultVersion
	result := toolboxDeployResult{
		Name:           definition.Name,
		Changed:        true,
		Version:        created.Version,
		DefaultVersion: defaultVersion,
		Staged:         defaultVersion != "" && defaultVersion != created.Version,
		Receipt:        store.Path,
	}
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
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	status := "default"
	if result.Staged {
		status = "staged"
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"Toolbox version created: name=%s version=%s status=%s",
			definition.Name,
			created.Version,
			status,
		),
	)
}

func cmdToolboxStatus(cmd *cobra.Command, _ []string) error {
	runtime, definition, err := toolboxRuntime(cmd)
	if err != nil {
		return err
	}
	found, err := runtime.client.GetToolboxContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	result := toolboxStatusResult{Name: definition.Name, Exists: found != nil}
	if found != nil {
		result.ID = found.ID
		result.DefaultVersion = found.DefaultVersion
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"Toolbox status: name=%s exists=%t default=%s",
			definition.Name,
			result.Exists,
			emptyValue(result.DefaultVersion),
		),
	)
}

func cmdToolboxVersions(cmd *cobra.Command, _ []string) error {
	runtime, definition, err := toolboxRuntime(cmd)
	if err != nil {
		return err
	}
	logical, err := runtime.client.GetToolboxContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	versions, err := runtime.client.ListToolboxVersionsContext(
		commandContext(cmd),
		definition.Name,
	)
	if err != nil {
		return err
	}
	result := toolboxVersionsResult{Name: definition.Name, Versions: versions}
	if logical != nil {
		result.DefaultVersion = logical.DefaultVersion
	}
	var text strings.Builder
	fmt.Fprintf(
		&text,
		"Toolbox versions: name=%s default=%s",
		definition.Name,
		emptyValue(result.DefaultVersion),
	)
	for _, version := range versions {
		fmt.Fprintf(&text, "\n  %s created=%d", version.Version, version.CreatedAt)
	}
	return printResult(cmd, result, text.String())
}

func cmdToolboxPromote(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, definition, err := toolboxRuntime(cmd)
	if err != nil {
		return err
	}
	version := getFlag(cmd, "toolbox-version")
	logical, err := runtime.client.GetToolboxContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	if logical == nil {
		return errs.NotFound("Toolbox %q was not found", definition.Name)
	}
	found, err := runtime.client.GetToolboxVersionContext(
		commandContext(cmd),
		definition.Name,
		version,
	)
	if err != nil {
		return err
	}
	if found == nil {
		return errs.NotFound("Toolbox %q version %s was not found", definition.Name, version)
	}
	result := toolboxMutationResult{
		Action:         "promote",
		Name:           definition.Name,
		Version:        version,
		Changed:        logical.DefaultVersion != version,
		DryRun:         getBoolFlag(cmd, "dry-run"),
		DefaultVersion: logical.DefaultVersion,
	}
	if !result.Changed {
		return printResult(
			cmd,
			result,
			fmt.Sprintf("Toolbox %s version %s is already default", definition.Name, version),
		)
	}
	if result.DryRun {
		return printResult(
			cmd,
			result,
			fmt.Sprintf(
				"dry run: would promote Toolbox %s from %s to %s",
				definition.Name,
				emptyValue(logical.DefaultVersion),
				version,
			),
		)
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Promote Toolbox %q version %s as the default used by consumers?",
		definition.Name,
		version,
	)); err != nil {
		return err
	}
	store, err := newToolboxOperationStore(
		cmd,
		runtime.resolved,
		definition,
		runtime.endpoint,
		"toolbox-promote",
	)
	if err != nil {
		return err
	}
	result.Receipt = store.Path
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete("failed", returnErr)
		}
	}()
	promoteErr := runtime.client.PromoteToolboxVersionContext(
		commandContext(cmd),
		definition.Name,
		version,
	)
	observed, reconcileErr := runtime.client.GetToolboxContext(
		commandContext(cmd),
		definition.Name,
	)
	converged := reconcileErr == nil &&
		observed != nil &&
		observed.DefaultVersion == version
	if promoteErr != nil && !converged {
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "foundry-toolbox",
			Name:           definition.Name,
			Action:         "promote",
			Status:         "uncertain",
			Reconciliation: "Read the logical Toolbox default_version before retrying promotion.",
		})
		_ = store.Complete("failed-partial", promoteErr)
		return promoteErr
	}
	if !converged {
		var convergenceErr error
		if reconcileErr != nil {
			convergenceErr = errs.AmbiguousMutation(
				errs.FoundryWrap(
					reconcileErr,
					"Toolbox %q promotion could not be verified",
					definition.Name,
				),
			)
		} else {
			current := ""
			if observed != nil {
				current = observed.DefaultVersion
			}
			convergenceErr = errs.AmbiguousMutation(
				errs.Conflict(
					"Toolbox %q promotion returned success but default_version is %s instead of %s",
					definition.Name,
					emptyValue(current),
					version,
				),
			)
		}
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "foundry-toolbox",
			Name:           definition.Name,
			Action:         "promote",
			Status:         "uncertain",
			Reconciliation: "Read the logical Toolbox default_version before retrying promotion.",
		})
		_ = store.Complete("failed-partial", convergenceErr)
		return convergenceErr
	}
	result.DefaultVersion = version
	resourceStatus := "succeeded"
	receiptStatus := "succeeded"
	if promoteErr != nil {
		resourceStatus = "succeeded-reconciled"
		receiptStatus = "succeeded-reconciled"
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-toolbox",
		Name:   definition.Name,
		Action: "promote",
		Status: resourceStatus,
	}); err != nil {
		return err
	}
	if err := store.Complete(receiptStatus, nil); err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf("Toolbox promoted: name=%s default=%s", definition.Name, version),
	)
}

func cmdToolboxDeleteVersion(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, definition, err := toolboxRuntime(cmd)
	if err != nil {
		return err
	}
	version := getFlag(cmd, "toolbox-version")
	logical, err := runtime.client.GetToolboxContext(commandContext(cmd), definition.Name)
	if err != nil {
		return err
	}
	found, err := runtime.client.GetToolboxVersionContext(
		commandContext(cmd),
		definition.Name,
		version,
	)
	if err != nil {
		return err
	}
	result := toolboxMutationResult{
		Action:  "delete-version",
		Name:    definition.Name,
		Version: version,
		Changed: found != nil,
		DryRun:  getBoolFlag(cmd, "dry-run"),
	}
	if logical != nil {
		result.DefaultVersion = logical.DefaultVersion
	}
	if found == nil {
		return printResult(
			cmd,
			result,
			fmt.Sprintf("Toolbox %q version %s not found", definition.Name, version),
		)
	}
	if result.DefaultVersion == version {
		return errs.Conflict(
			"Toolbox %q version %s is the default and cannot be deleted; promote another version first",
			definition.Name,
			version,
		)
	}
	if result.DryRun {
		return printResult(
			cmd,
			result,
			fmt.Sprintf("dry run: would delete Toolbox %s version %s", definition.Name, version),
		)
	}
	if err := confirmDestructive(
		cmd,
		fmt.Sprintf("Delete Toolbox %q immutable version %s?", definition.Name, version),
	); err != nil {
		return err
	}
	store, err := newToolboxOperationStore(
		cmd,
		runtime.resolved,
		definition,
		runtime.endpoint,
		"toolbox-delete-version",
	)
	if err != nil {
		return err
	}
	result.Receipt = store.Path
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete("failed", returnErr)
		}
	}()
	removed, err := runtime.client.DeleteToolboxVersionContext(
		commandContext(cmd),
		definition.Name,
		version,
	)
	if err != nil {
		status := "failed"
		if errs.IsKind(err, "ambiguous-mutation") {
			status = "failed-partial"
			_ = store.AddResource(receipt.ResourceChange{
				Kind:           "foundry-toolbox-version",
				Name:           definition.Name,
				Action:         "delete",
				Status:         "uncertain",
				Reconciliation: "Read the Toolbox version before retrying deletion.",
			})
		}
		_ = store.Complete(status, err)
		return err
	}
	observed, reconcileErr := runtime.client.GetToolboxVersionContext(
		commandContext(cmd),
		definition.Name,
		version,
	)
	if reconcileErr != nil || observed != nil {
		var convergenceErr error
		if reconcileErr != nil {
			convergenceErr = errs.AmbiguousMutation(
				errs.FoundryWrap(
					reconcileErr,
					"Toolbox %q version %s deletion could not be verified",
					definition.Name,
					version,
				),
			)
		} else {
			convergenceErr = errs.AmbiguousMutation(
				errs.Conflict(
					"Toolbox %q version %s still exists after deletion returned success",
					definition.Name,
					version,
				),
			)
		}
		_ = store.AddResource(receipt.ResourceChange{
			Kind:           "foundry-toolbox-version",
			Name:           definition.Name,
			Action:         "delete",
			Status:         "uncertain",
			Reconciliation: "Read the Toolbox version before retrying deletion.",
		})
		_ = store.Complete("failed-partial", convergenceErr)
		return convergenceErr
	}
	result.Changed = removed
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-toolbox-version",
		Name:   definition.Name,
		Action: "delete",
		Status: "succeeded",
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf("Toolbox version deleted: name=%s version=%s", definition.Name, version),
	)
}

type toolboxCommandRuntime struct {
	resolved *resolvedManifest
	endpoint string
	client   *foundry.Client
}

func toolboxRuntime(
	cmd *cobra.Command,
) (*toolboxCommandRuntime, tools.ToolboxDefinition, error) {
	resolved, definitions, err := resolveToolboxDefinitions(cmd, false)
	if err != nil {
		return nil, tools.ToolboxDefinition{}, err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, tools.ToolboxDefinition{}, err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, resolved.Config, credential, httpClient)
	if err != nil {
		return nil, tools.ToolboxDefinition{}, err
	}
	return &toolboxCommandRuntime{
		resolved: resolved,
		endpoint: endpoint,
		client:   newFoundryClient(endpoint, resolved.Config, credential, httpClient),
	}, definitions[0], nil
}

func resolveToolboxDefinitions(
	cmd *cobra.Command,
	allowMultiple bool,
) (*resolvedManifest, []tools.ToolboxDefinition, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, nil, err
	}
	definitions, err := tools.BuildToolboxes(
		resolved.Config.Toolboxes,
		resolved.BaseDir,
	)
	if err != nil {
		return nil, nil, err
	}
	if len(definitions) == 0 {
		return nil, nil, errs.Config("manifest defines no top-level toolboxes")
	}
	selected := strings.TrimSpace(getFlag(cmd, "toolbox"))
	if selected != "" {
		for _, definition := range definitions {
			if strings.EqualFold(definition.Name, selected) {
				return resolved, []tools.ToolboxDefinition{definition}, nil
			}
		}
		return nil, nil, errs.NotFound("Toolbox %q is not defined in the manifest", selected)
	}
	if allowMultiple || len(definitions) == 1 {
		return resolved, definitions, nil
	}
	return nil, nil, errs.Config(
		"manifest defines %d Toolboxes; select one with --toolbox",
		len(definitions),
	)
}

func newToolboxOperationStore(
	cmd *cobra.Command,
	resolved *resolvedManifest,
	definition tools.ToolboxDefinition,
	endpoint string,
	operation string,
) (*receipt.OperationStore, error) {
	path := getFlag(cmd, "receipt")
	if path == "" {
		path = receipt.OperationPath(
			resolved.ManifestPath,
			operation,
			definition.Name,
			time.Now(),
		)
	} else if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, errs.Config("failed to resolve --receipt path: %v", err)
		}
		path = absolute
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		operation,
		resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path:        resolved.ManifestPath,
			Hash:        resolved.ManifestHash,
			DesiredHash: toolboxDesiredHash(definition),
		},
		receipt.ResourceReference{
			Name:     resolved.Config.Project.Name,
			Endpoint: endpoint,
		},
		resolved.Config.Agent.Name,
	)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func toolboxDesiredHash(definition tools.ToolboxDefinition) string {
	data, err := json.Marshal(definition.Payload())
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func toolboxVersionMap(version *foundry.ToolboxVersion) map[string]interface{} {
	data, err := json.Marshal(version)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil
	}
	return result
}

func latestToolboxVersion(
	versions []foundry.ToolboxVersion,
) *foundry.ToolboxVersion {
	if len(versions) == 0 {
		return nil
	}
	latest := &versions[0]
	for index := 1; index < len(versions); index++ {
		if versions[index].CreatedAt > latest.CreatedAt {
			latest = &versions[index]
		}
	}
	return latest
}
