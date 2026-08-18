package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/memory"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

type memoryRuntime struct {
	Resolved   *resolvedManifest
	Definition *memory.Definition
	Endpoint   string
	Client     *foundry.Client
}

type memoryMutationOutput struct {
	Action  string      `json:"action" yaml:"action"`
	Changed bool        `json:"changed" yaml:"changed"`
	Result  interface{} `json:"result,omitempty" yaml:"result,omitempty"`
	Receipt string      `json:"receipt" yaml:"receipt"`
}

func cmdMemoryStoreValidate(cmd *cobra.Command, _ []string) error {
	resolved, definitions, err := resolveMemoryDefinitions(cmd)
	if err != nil {
		return err
	}
	result := make([]map[string]interface{}, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, map[string]interface{}{
			"name":           definition.Name,
			"chatModel":      definition.ChatModel,
			"embeddingModel": definition.EmbeddingModel,
			"desiredHash":    definition.DesiredHash,
		})
	}
	return printResult(
		cmd,
		map[string]interface{}{"cloud": resolved.Config.Cloud.Name, "memoryStores": result},
		fmt.Sprintf("validated %d memory store definition(s)", len(result)),
	)
}

func cmdMemoryStorePlan(cmd *cobra.Command, args []string) error {
	return cmdMemoryStoreValidate(cmd, args)
}

func cmdMemoryStoreSync(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	store, err := newMemoryOperationStore(
		cmd,
		runtime,
		"memory-store-sync",
		runtime.Definition.DesiredHash,
	)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()

	desired := runtime.Definition.Store()
	remote, err := runtime.Client.GetMemoryStoreContext(commandContext(cmd), desired.Name)
	created := false
	if err != nil {
		if !isNotFoundError(err) {
			return err
		}
		remote, err = runtime.Client.CreateMemoryStoreContext(commandContext(cmd), desired)
		if err != nil {
			if !errs.IsAmbiguousMutation(err) {
				return err
			}
			reconciled, reconcileErr := runtime.Client.GetMemoryStoreContext(
				commandContext(cmd),
				desired.Name,
			)
			if reconcileErr != nil {
				_ = store.AddResource(receipt.ResourceChange{
					Kind:           "foundry-memory-store",
					Name:           desired.Name,
					Action:         "create",
					Status:         "uncertain",
					Reconciliation: "Get the memory store by name before retrying.",
				})
				return err
			}
			remote = reconciled
		} else {
			created = true
		}
	}
	if !memoryDefinitionsEqual(remote.Definition, desired.Definition) {
		return errs.Config(
			"memory store %q exists with a different immutable definition; delete and recreate it to change chat_model, embedding_model, or options",
			desired.Name,
		)
	}
	changed := created
	action := "unchanged"
	if created {
		action = "created"
	} else if remote.Description != desired.Description ||
		!memoryMetadataEqual(remote.Metadata, desired.Metadata) {
		remote, err = runtime.Client.UpdateMemoryStoreContext(
			commandContext(cmd),
			desired.Name,
			desired.Description,
			desired.Metadata,
		)
		if err != nil {
			return err
		}
		changed = true
		action = "updated"
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:         "foundry-memory-store",
		Name:         desired.Name,
		ID:           remote.ID,
		Action:       action,
		Status:       "succeeded",
		CreatedByRun: created,
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, memoryMutationOutput{
		Action:  action,
		Changed: changed,
		Result:  remote,
		Receipt: store.Path,
	}, fmt.Sprintf("memory store %q %s", desired.Name, action))
}

func cmdMemoryStoreList(cmd *cobra.Command, _ []string) error {
	runtime, err := newMemoryRuntime(cmd, false)
	if err != nil {
		return err
	}
	stores, err := runtime.Client.ListMemoryStoresContext(commandContext(cmd))
	if err != nil {
		return err
	}
	return printResult(cmd, stores, fmt.Sprintf("found %d memory store(s)", len(stores)))
}

func cmdMemoryStoreShow(cmd *cobra.Command, _ []string) error {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	store, err := runtime.Client.GetMemoryStoreContext(
		commandContext(cmd),
		runtime.Definition.Name,
	)
	if err != nil {
		return err
	}
	return printResult(cmd, store, fmt.Sprintf("memory store %q", store.Name))
}

func cmdMemoryStoreDelete(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("memory-store-delete requires --yes")
	}
	store, err := newMemoryOperationStore(
		cmd,
		runtime,
		"memory-store-delete",
		runtime.Definition.DesiredHash,
	)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	result, err := runtime.Client.DeleteMemoryStoreContext(
		commandContext(cmd),
		runtime.Definition.Name,
	)
	if err != nil {
		return err
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-memory-store",
		Name:   runtime.Definition.Name,
		ID:     result.ID,
		Action: "deleted",
		Status: "succeeded",
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, memoryMutationOutput{
		Action:  "deleted",
		Changed: result.Deleted,
		Result:  result,
		Receipt: store.Path,
	}, fmt.Sprintf("deleted memory store %q", runtime.Definition.Name))
}

func cmdMemorySearch(cmd *cobra.Command, _ []string) error {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	body, err := memoryConversationBody(cmd, true)
	if err != nil {
		return err
	}
	if previous := strings.TrimSpace(getFlag(cmd, "previous-search-id")); previous != "" {
		body["previous_search_id"] = previous
	}
	if max := getIntFlag(cmd, "max-memories"); max > 0 {
		body["options"] = map[string]interface{}{"max_memories": max}
	}
	result, err := runtime.Client.SearchMemoriesContext(
		commandContext(cmd),
		runtime.Definition.Name,
		body,
	)
	if err != nil {
		return err
	}
	return printResult(cmd, result, "memory search completed")
}

func cmdMemoryUpdate(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	body, err := memoryConversationBody(cmd, false)
	if err != nil {
		return err
	}
	if previous := strings.TrimSpace(getFlag(cmd, "previous-update-id")); previous != "" {
		body["previous_update_id"] = previous
	}
	body["update_delay"] = getIntFlag(cmd, "update-delay")
	store, err := newMemoryOperationStore(cmd, runtime, "memory-update", "")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	result, err := runtime.Client.UpdateMemoriesContext(
		commandContext(cmd),
		runtime.Definition.Name,
		body,
		getDurationFlag(cmd, "memory-timeout"),
		getDurationFlag(cmd, "memory-interval"),
	)
	if err != nil {
		return err
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-memory-scope",
		Name:   fmt.Sprint(body["scope"]),
		Action: "updated",
		Status: "succeeded",
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, memoryMutationOutput{
		Action:  "updated",
		Changed: true,
		Result:  result,
		Receipt: store.Path,
	}, "memory update completed")
}

func cmdMemoryItemCreate(cmd *cobra.Command, _ []string) error {
	return mutateMemoryItem(cmd, "memory-item-create", func(
		ctx context.Context,
		client *foundry.Client,
		storeName string,
	) (interface{}, error) {
		kind, err := memoryKind(cmd)
		if err != nil {
			return nil, err
		}
		return client.CreateMemoryItemContext(
			ctx,
			storeName,
			getFlag(cmd, "scope"),
			getFlag(cmd, "content"),
			kind,
		)
	})
}

func cmdMemoryItemUpdate(cmd *cobra.Command, _ []string) error {
	return mutateMemoryItem(cmd, "memory-item-update", func(
		ctx context.Context,
		client *foundry.Client,
		storeName string,
	) (interface{}, error) {
		return client.UpdateMemoryItemContext(
			ctx,
			storeName,
			getFlag(cmd, "memory-id"),
			getFlag(cmd, "content"),
		)
	})
}

func cmdMemoryItemDelete(cmd *cobra.Command, _ []string) error {
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("memory-item-delete requires --yes")
	}
	return mutateMemoryItem(cmd, "memory-item-delete", func(
		ctx context.Context,
		client *foundry.Client,
		storeName string,
	) (interface{}, error) {
		return client.DeleteMemoryItemContext(
			ctx,
			storeName,
			getFlag(cmd, "memory-id"),
		)
	})
}

func cmdMemoryItemShow(cmd *cobra.Command, _ []string) error {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	item, err := runtime.Client.GetMemoryItemContext(
		commandContext(cmd),
		runtime.Definition.Name,
		getFlag(cmd, "memory-id"),
	)
	if err != nil {
		return err
	}
	return printResult(cmd, item, fmt.Sprintf("memory item %q", item.MemoryID))
}

func cmdMemoryItemList(cmd *cobra.Command, _ []string) error {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	kind := strings.TrimSpace(getFlag(cmd, "kind"))
	if kind != "" {
		if _, err := validateMemoryKind(kind); err != nil {
			return err
		}
	}
	items, err := runtime.Client.ListMemoryItemsContext(
		commandContext(cmd),
		runtime.Definition.Name,
		getFlag(cmd, "scope"),
		kind,
	)
	if err != nil {
		return err
	}
	return printResult(cmd, items, fmt.Sprintf("found %d memory item(s)", len(items)))
}

func cmdMemoryScopeDelete(cmd *cobra.Command, _ []string) (returnErr error) {
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("memory-scope-delete requires --yes")
	}
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	store, err := newMemoryOperationStore(cmd, runtime, "memory-scope-delete", "")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	result, err := runtime.Client.DeleteMemoryScopeContext(
		commandContext(cmd),
		runtime.Definition.Name,
		getFlag(cmd, "scope"),
	)
	if err != nil {
		return err
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-memory-scope",
		Name:   result.Scope,
		Action: "deleted",
		Status: "succeeded",
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, memoryMutationOutput{
		Action:  "deleted",
		Changed: result.Deleted,
		Result:  result,
		Receipt: store.Path,
	}, fmt.Sprintf("deleted memory scope %q", result.Scope))
}

func mutateMemoryItem(
	cmd *cobra.Command,
	operation string,
	action func(context.Context, *foundry.Client, string) (interface{}, error),
) (returnErr error) {
	runtime, err := newMemoryRuntime(cmd, true)
	if err != nil {
		return err
	}
	store, err := newMemoryOperationStore(cmd, runtime, operation, "")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	result, err := action(commandContext(cmd), runtime.Client, runtime.Definition.Name)
	if err != nil {
		return err
	}
	resourceName := strings.TrimSpace(getFlag(cmd, "memory-id"))
	if item, ok := result.(*foundry.MemoryItem); ok {
		resourceName = item.MemoryID
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-memory-item",
		Name:   resourceName,
		Action: strings.TrimPrefix(operation, "memory-item-"),
		Status: "succeeded",
	}); err != nil {
		return err
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, memoryMutationOutput{
		Action:  strings.TrimPrefix(operation, "memory-item-"),
		Changed: true,
		Result:  result,
		Receipt: store.Path,
	}, strings.ReplaceAll(operation, "-", " ")+" completed")
}

func resolveMemoryDefinitions(
	cmd *cobra.Command,
) (*resolvedManifest, []memory.Definition, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, nil, err
	}
	definitions, err := memory.Build(resolved.Config.MemoryStores)
	if err != nil {
		return nil, nil, err
	}
	if len(definitions) == 0 {
		return nil, nil, errs.Config("manifest defines no memory_stores")
	}
	return resolved, definitions, nil
}

func newMemoryRuntime(cmd *cobra.Command, selectStore bool) (*memoryRuntime, error) {
	if !getBoolFlag(cmd, "accept-preview") {
		return nil, errs.Config("memory commands require --accept-preview")
	}
	resolved, definitions, err := resolveMemoryDefinitions(cmd)
	if err != nil {
		return nil, err
	}
	if reason := resolved.Config.Cloud.UnsupportedTools["memory_search_preview"]; reason != "" {
		return nil, errs.Config("memory is unavailable in %s: %s", resolved.Config.Cloud.Name, reason)
	}
	var definition *memory.Definition
	if selectStore {
		definition, err = selectMemoryDefinition(cmd, definitions)
		if err != nil {
			return nil, err
		}
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, resolved.Config, credential, httpClient)
	if err != nil {
		return nil, err
	}
	return &memoryRuntime{
		Resolved:   resolved,
		Definition: definition,
		Endpoint:   endpoint,
		Client:     newFoundryClient(endpoint, resolved.Config, credential, httpClient),
	}, nil
}

func selectMemoryDefinition(
	cmd *cobra.Command,
	definitions []memory.Definition,
) (*memory.Definition, error) {
	selected := strings.TrimSpace(getFlag(cmd, "memory-store"))
	if selected != "" {
		for index := range definitions {
			if strings.EqualFold(definitions[index].Name, selected) {
				return &definitions[index], nil
			}
		}
		return nil, errs.NotFound("memory store %q is not defined in the manifest", selected)
	}
	if len(definitions) == 1 {
		return &definitions[0], nil
	}
	return nil, errs.Config(
		"manifest defines %d memory stores; select one with --memory-store",
		len(definitions),
	)
}

func newMemoryOperationStore(
	cmd *cobra.Command,
	runtime *memoryRuntime,
	operation string,
	desiredHash string,
) (*receipt.OperationStore, error) {
	path := strings.TrimSpace(getFlag(cmd, "receipt"))
	if path == "" {
		path = receipt.OperationPath(
			runtime.Resolved.ManifestPath,
			operation,
			runtime.Definition.Name,
			time.Now(),
		)
	} else if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, errs.Config("failed to resolve --receipt path: %v", err)
		}
		path = absolute
	}
	return newManagedOperationStore(
		cmd,
		path,
		operation,
		runtime.Resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path:        runtime.Resolved.ManifestPath,
			Hash:        runtime.Resolved.ManifestHash,
			DesiredHash: desiredHash,
		},
		receipt.ResourceReference{
			Name:     runtime.Resolved.Config.Project.Name,
			Endpoint: runtime.Endpoint,
		},
		runtime.Resolved.Config.Agent.Name,
	)
}

func memoryConversationBody(
	cmd *cobra.Command,
	allowEmptyItems bool,
) (map[string]interface{}, error) {
	scope := strings.TrimSpace(getFlag(cmd, "scope"))
	if scope == "" {
		return nil, errs.Config("--scope is required")
	}
	body := map[string]interface{}{"scope": scope}
	itemsFile := strings.TrimSpace(getFlag(cmd, "items-file"))
	input := strings.TrimSpace(getFlag(cmd, "input"))
	if itemsFile != "" && input != "" {
		return nil, errs.Config("--items-file and --input are mutually exclusive")
	}
	if itemsFile != "" {
		items, err := readMemoryItemsFile(itemsFile)
		if err != nil {
			return nil, err
		}
		body["items"] = items
	} else if input != "" {
		body["items"] = []interface{}{map[string]interface{}{
			"type": "message",
			"role": "user",
			"content": []interface{}{map[string]interface{}{
				"type": "input_text",
				"text": input,
			}},
		}}
	} else if !allowEmptyItems {
		return nil, errs.Config("set --input or --items-file")
	}
	return body, nil
}

func readMemoryItemsFile(path string) ([]interface{}, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errs.Config("failed to inspect memory items file %q: %v", path, err)
	}
	const maxMemoryItemsBytes = 4 << 20
	if !info.Mode().IsRegular() || info.Size() > maxMemoryItemsBytes {
		return nil, errs.Config(
			"memory items file %q must be a regular file no larger than %d bytes",
			path,
			maxMemoryItemsBytes,
		)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Config("failed to read memory items file %q: %v", path, err)
	}
	var items []interface{}
	if err := json.Unmarshal(data, &items); err != nil || items == nil {
		return nil, errs.Config("memory items file %q must contain one JSON array", path)
	}
	return items, nil
}

func memoryKind(cmd *cobra.Command) (string, error) {
	return validateMemoryKind(getFlag(cmd, "kind"))
}

func validateMemoryKind(kind string) (string, error) {
	kind = strings.TrimSpace(kind)
	switch kind {
	case "user_profile", "chat_summary", "procedural":
		return kind, nil
	default:
		return "", errs.Config(
			"--kind must be user_profile, chat_summary, or procedural",
		)
	}
}

func memoryDefinitionsEqual(
	current foundry.MemoryStoreDefinition,
	desired foundry.MemoryStoreDefinition,
) bool {
	if current.Options != nil && desired.Options != nil &&
		desired.Options.DefaultTTLSeconds == nil &&
		current.Options.DefaultTTLSeconds != nil &&
		*current.Options.DefaultTTLSeconds == 0 {
		current.Options.DefaultTTLSeconds = nil
	}
	currentData, _ := json.Marshal(current)
	desiredData, _ := json.Marshal(desired)
	var currentValue interface{}
	var desiredValue interface{}
	_ = json.Unmarshal(currentData, &currentValue)
	_ = json.Unmarshal(desiredData, &desiredValue)
	return reflect.DeepEqual(currentValue, desiredValue)
}

func memoryMetadataEqual(current, desired map[string]string) bool {
	if len(current) != len(desired) {
		return false
	}
	for key, value := range current {
		if desired[key] != value {
			return false
		}
	}
	return true
}

func isNotFoundError(err error) bool {
	return errs.IsKind(err, "not_found") ||
		strings.Contains(strings.ToLower(err.Error()), "404")
}
