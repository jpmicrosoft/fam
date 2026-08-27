package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/grounding"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/receipt"
	"foundry-agent-manager/internal/tools"

	"github.com/spf13/cobra"
)

type groundingValidationResult struct {
	Cloud        string   `json:"cloud" yaml:"cloud"`
	VectorStores []string `json:"vectorStores" yaml:"vectorStores"`
}

type groundingPlanFile struct {
	Path   string `json:"path" yaml:"path"`
	Bytes  int64  `json:"bytes" yaml:"bytes"`
	SHA256 string `json:"sha256" yaml:"sha256"`
}

type groundingPlanItem struct {
	Name        string              `json:"name" yaml:"name"`
	Description string              `json:"description,omitempty" yaml:"description,omitempty"`
	DesiredHash string              `json:"desiredHash" yaml:"desiredHash"`
	Files       []groundingPlanFile `json:"files" yaml:"files"`
}

type groundingPlanResult struct {
	Cloud        string              `json:"cloud" yaml:"cloud"`
	VectorStores []groundingPlanItem `json:"vectorStores" yaml:"vectorStores"`
}

type groundingFileStatus struct {
	Path         string `json:"path" yaml:"path"`
	FileID       string `json:"fileId,omitempty" yaml:"fileId,omitempty"`
	Status       string `json:"status" yaml:"status"`
	SHA256       string `json:"sha256" yaml:"sha256"`
	RemoteSHA256 string `json:"remoteSha256,omitempty" yaml:"remoteSha256,omitempty"`
	InSync       bool   `json:"inSync" yaml:"inSync"`
}

type groundingStatusResult struct {
	Name              string                `json:"name" yaml:"name"`
	Exists            bool                  `json:"exists" yaml:"exists"`
	ID                string                `json:"id,omitempty" yaml:"id,omitempty"`
	Status            string                `json:"status,omitempty" yaml:"status,omitempty"`
	DesiredHash       string                `json:"desiredHash" yaml:"desiredHash"`
	RemoteDesiredHash string                `json:"remoteDesiredHash,omitempty" yaml:"remoteDesiredHash,omitempty"`
	InSync            bool                  `json:"inSync" yaml:"inSync"`
	Files             []groundingFileStatus `json:"files" yaml:"files"`
	StaleManagedFiles int                   `json:"staleManagedFiles" yaml:"staleManagedFiles"`
	UnmanagedFiles    int                   `json:"unmanagedFiles" yaml:"unmanagedFiles"`
}

type groundingSyncResult struct {
	Name     string                `json:"name" yaml:"name"`
	ID       string                `json:"id" yaml:"id"`
	Created  bool                  `json:"created" yaml:"created"`
	Changed  bool                  `json:"changed" yaml:"changed"`
	Status   string                `json:"status" yaml:"status"`
	Files    []groundingFileStatus `json:"files" yaml:"files"`
	Receipt  string                `json:"receipt" yaml:"receipt"`
	Detached int                   `json:"detached" yaml:"detached"`
	Uploaded int                   `json:"uploaded" yaml:"uploaded"`
	Deleted  int                   `json:"deletedUploads" yaml:"deletedUploads"`
}

type groundingMutationResult struct {
	Action  string `json:"action" yaml:"action"`
	Name    string `json:"name" yaml:"name"`
	ID      string `json:"id,omitempty" yaml:"id,omitempty"`
	File    string `json:"file,omitempty" yaml:"file,omitempty"`
	Changed bool   `json:"changed" yaml:"changed"`
	DryRun  bool   `json:"dryRun" yaml:"dryRun"`
	Receipt string `json:"receipt,omitempty" yaml:"receipt,omitempty"`
}

type groundingCommandRuntime struct {
	resolved   *resolvedManifest
	definition grounding.VectorStore
	endpoint   string
	client     *foundry.Client
}

func cmdGroundingValidate(cmd *cobra.Command, _ []string) error {
	resolved, definitions, err := resolveGroundingDefinitions(cmd, true, true)
	if err != nil {
		return err
	}
	result := groundingValidationResult{Cloud: resolved.Config.Cloud.Name}
	for _, definition := range definitions {
		result.VectorStores = append(
			result.VectorStores,
			fmt.Sprintf(
				"vector_store(name=%q, files=%d, desired_hash=%s)",
				definition.Name,
				len(definition.Files),
				definition.DesiredHash,
			),
		)
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf("validated %d managed vector store(s)", len(definitions)),
	)
}

func cmdGroundingPlan(cmd *cobra.Command, _ []string) error {
	resolved, definitions, err := resolveGroundingDefinitions(cmd, true, true)
	if err != nil {
		return err
	}
	result := groundingPlanResult{Cloud: resolved.Config.Cloud.Name}
	var text strings.Builder
	fmt.Fprint(&text, "Grounding plan:")
	for _, definition := range definitions {
		item := groundingPlanItem{
			Name:        definition.Name,
			Description: definition.Description,
			DesiredHash: definition.DesiredHash,
		}
		for _, file := range definition.Files {
			item.Files = append(item.Files, groundingPlanFile{
				Path:   file.Path,
				Bytes:  file.Size,
				SHA256: file.SHA256,
			})
		}
		result.VectorStores = append(result.VectorStores, item)
		fmt.Fprintf(
			&text,
			"\n  %s: files=%d bytes=%d desired_hash=%s",
			definition.Name,
			len(definition.Files),
			groundingBytes(definition),
			definition.DesiredHash,
		)
	}
	return printResult(cmd, result, text.String())
}

func cmdGroundingStatus(cmd *cobra.Command, _ []string) error {
	runtime, err := groundingRuntime(cmd, true)
	if err != nil {
		return err
	}
	remote, err := findManagedVectorStore(
		commandContext(cmd),
		runtime.client,
		runtime.definition,
	)
	if err != nil {
		return err
	}
	if remote == nil {
		result := groundingStatusResult{
			Name:        runtime.definition.Name,
			DesiredHash: runtime.definition.DesiredHash,
		}
		return printResult(
			cmd,
			result,
			fmt.Sprintf("managed vector store %q does not exist", runtime.definition.Name),
		)
	}
	files, err := listGroundingFiles(
		commandContext(cmd),
		runtime.client,
		remote.ID,
	)
	if err != nil {
		return err
	}
	result := evaluateGroundingStatus(runtime.definition, remote, files)
	return printResult(cmd, result, groundingStatusText(result))
}

func cmdGroundingSync(cmd *cobra.Command, _ []string) (returnErr error) {
	timeout, interval, err := groundingWaitDurations(cmd)
	if err != nil {
		return err
	}
	if getBoolFlag(cmd, "delete-pruned-uploads") && !getBoolFlag(cmd, "prune") {
		return errs.Config("--delete-pruned-uploads requires --prune")
	}
	runtime, err := groundingRuntime(cmd, true)
	if err != nil {
		return err
	}
	store, err := newGroundingOperationStore(
		cmd,
		runtime.resolved,
		runtime.definition,
		runtime.endpoint,
		"grounding-sync",
	)
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()

	remote, err := findManagedVectorStore(
		commandContext(cmd),
		runtime.client,
		runtime.definition,
	)
	if err != nil {
		return err
	}
	created := false
	if remote == nil {
		remote, err = runtime.client.CreateVectorStoreContext(
			commandContext(cmd),
			runtime.definition.Name,
			runtime.definition.Description,
			grounding.Metadata(runtime.definition, runtime.definition.DesiredHash),
		)
		if err != nil {
			if errs.IsAmbiguousMutation(err) {
				reconciled, reconcileErr := findManagedVectorStore(
					commandContext(cmd),
					runtime.client,
					runtime.definition,
				)
				if reconcileErr == nil && reconciled != nil {
					remote = reconciled
				} else {
					_ = store.AddResource(receipt.ResourceChange{
						Kind:           "foundry-vector-store",
						Name:           runtime.definition.Name,
						Action:         "create",
						Status:         "uncertain",
						Reconciliation: "List vector stores and identify metadata logical_name=" + runtime.definition.Name + " before retrying.",
					})
					return releaseFailure(store.Path, err)
				}
			} else {
				return err
			}
		} else {
			created = true
		}
		if remote == nil || remote.ID == "" {
			return errs.Foundry(
				"vector store %q create returned no resource ID",
				runtime.definition.Name,
			)
		}
		if err := store.AddResource(receipt.ResourceChange{
			Kind:         "foundry-vector-store",
			Name:         runtime.definition.Name,
			ID:           remote.ID,
			Action:       "create",
			Status:       "succeeded",
			CreatedByRun: created,
		}); err != nil {
			return err
		}
	}
	if strings.EqualFold(remote.Status, "expired") {
		return errs.Conflict(
			"managed vector store %q (%s) is expired; delete it explicitly before recreating",
			runtime.definition.Name,
			remote.ID,
		)
	}

	attachments, err := listGroundingFiles(
		commandContext(cmd),
		runtime.client,
		remote.ID,
	)
	if err != nil {
		return err
	}
	staleRemoved := removedManagedAttachments(runtime.definition, attachments)
	if len(staleRemoved) > 0 && !getBoolFlag(cmd, "prune") {
		return errs.Conflict(
			"managed vector store %q contains %d managed file(s) removed from the manifest; rerun with --prune --yes or use fam grounding file delete",
			runtime.definition.Name,
			len(staleRemoved),
		)
	}
	if len(staleRemoved) > 0 {
		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Detach %d managed file(s) removed from grounding vector store %q?",
			len(staleRemoved),
			runtime.definition.Name,
		)); err != nil {
			return err
		}
	}
	replacedUploads := replacementUploadCount(runtime.definition, attachments)
	if replacedUploads > 0 && getBoolFlag(cmd, "delete-replaced-uploads") {
		if err := confirmDestructive(cmd, fmt.Sprintf(
			"Globally delete %d project upload(s) after replacing their managed vector-store attachments in %q?",
			replacedUploads,
			runtime.definition.Name,
		)); err != nil {
			return err
		}
	}

	result := groundingSyncResult{
		Name:    runtime.definition.Name,
		ID:      remote.ID,
		Created: created,
		Receipt: store.Path,
	}
	for _, desired := range runtime.definition.Files {
		matches := managedAttachmentsForPath(attachments, desired.PathHash)
		ready := desiredAttachment(matches, desired.SHA256)
		if ready != nil && !strings.EqualFold(ready.Status, "completed") {
			ready, err = runtime.client.WaitForVectorStoreFileContext(
				commandContext(cmd),
				remote.ID,
				ready.ID,
				timeout,
				interval,
			)
			if err != nil {
				return err
			}
		}
		if ready == nil {
			uploaded, uploadErr := uploadGroundingFile(
				commandContext(cmd),
				runtime,
				desired,
			)
			if uploadErr != nil {
				_ = store.AddResource(receipt.ResourceChange{
					Kind:           "foundry-file",
					Name:           desired.Filename,
					Action:         "upload",
					Status:         mutationStatus(uploadErr),
					Reconciliation: "Inspect recent project files with filename " + desired.Filename + " before retrying.",
				})
				return releaseFailure(store.Path, uploadErr)
			}
			result.Uploaded++
			if err := store.AddResource(receipt.ResourceChange{
				Kind:         "foundry-file",
				Name:         desired.Filename,
				ID:           uploaded.ID,
				Action:       "upload",
				Status:       "succeeded",
				CreatedByRun: true,
			}); err != nil {
				return err
			}
			ready, err = runtime.client.AttachVectorStoreFileContext(
				commandContext(cmd),
				remote.ID,
				uploaded.ID,
				grounding.FileAttributes(desired),
			)
			if err != nil {
				if errs.IsAmbiguousMutation(err) {
					reconciled, reconcileErr := runtime.client.GetVectorStoreFileContext(
						commandContext(cmd),
						remote.ID,
						uploaded.ID,
					)
					if reconcileErr == nil && reconciled != nil {
						ready = reconciled
					} else {
						_ = store.AddResource(receipt.ResourceChange{
							Kind:           "foundry-vector-store-file",
							Name:           desired.Filename,
							ID:             uploaded.ID,
							Action:         "attach",
							Status:         "uncertain",
							Reconciliation: "Read vector store " + remote.ID + " file " + uploaded.ID + " before retrying.",
						})
						return releaseFailure(store.Path, err)
					}
				} else {
					return err
				}
			}
			ready, err = runtime.client.WaitForVectorStoreFileContext(
				commandContext(cmd),
				remote.ID,
				uploaded.ID,
				timeout,
				interval,
			)
			if err != nil {
				return err
			}
			ready.Attributes = grounding.FileAttributes(desired)
			if err := store.AddResource(receipt.ResourceChange{
				Kind:         "foundry-vector-store-file",
				Name:         desired.Filename,
				ID:           uploaded.ID,
				Action:       "attach",
				Status:       "succeeded",
				CreatedByRun: true,
			}); err != nil {
				return err
			}
			result.Changed = true
			attachments = append(attachments, *ready)
		}
		for _, stale := range matches {
			if stale.ID == ready.ID {
				continue
			}
			removed, detachErr := detachGroundingFile(
				commandContext(cmd),
				runtime.client,
				remote.ID,
				stale.ID,
			)
			if detachErr != nil {
				return releaseFailure(store.Path, detachErr)
			}
			if removed {
				result.Detached++
				result.Changed = true
				if err := store.AddResource(receipt.ResourceChange{
					Kind:   "foundry-vector-store-file",
					Name:   attributeString(stale.Attributes, grounding.AttributeFilename),
					ID:     stale.ID,
					Action: "detach-replaced",
					Status: "succeeded",
				}); err != nil {
					return err
				}
				if getBoolFlag(cmd, "delete-replaced-uploads") {
					if _, deleteErr := deleteUploadedFile(
						commandContext(cmd),
						runtime.client,
						stale.ID,
					); deleteErr != nil {
						return releaseFailure(store.Path, deleteErr)
					}
					result.Deleted++
					if err := store.AddResource(receipt.ResourceChange{
						Kind:   "foundry-file",
						Name:   attributeString(stale.Attributes, grounding.AttributeFilename),
						ID:     stale.ID,
						Action: "delete-replaced-upload",
						Status: "succeeded",
					}); err != nil {
						return err
					}
				}
			}
		}
	}

	for _, stale := range staleRemoved {
		removed, detachErr := detachGroundingFile(
			commandContext(cmd),
			runtime.client,
			remote.ID,
			stale.ID,
		)
		if detachErr != nil {
			return releaseFailure(store.Path, detachErr)
		}
		if !removed {
			continue
		}
		result.Detached++
		result.Changed = true
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry-vector-store-file",
			Name:   attributeString(stale.Attributes, grounding.AttributeFilename),
			ID:     stale.ID,
			Action: "prune",
			Status: "succeeded",
		}); err != nil {
			return err
		}
		if getBoolFlag(cmd, "delete-pruned-uploads") {
			if _, deleteErr := deleteUploadedFile(
				commandContext(cmd),
				runtime.client,
				stale.ID,
			); deleteErr != nil {
				return releaseFailure(store.Path, deleteErr)
			}
			result.Deleted++
			if err := store.AddResource(receipt.ResourceChange{
				Kind:   "foundry-file",
				Name:   attributeString(stale.Attributes, grounding.AttributeFilename),
				ID:     stale.ID,
				Action: "delete",
				Status: "succeeded",
			}); err != nil {
				return err
			}
		}
	}

	if _, err := runtime.client.WaitForVectorStoreContext(
		commandContext(cmd),
		remote.ID,
		timeout,
		interval,
	); err != nil {
		return err
	}
	finalFiles, err := listGroundingFiles(
		commandContext(cmd),
		runtime.client,
		remote.ID,
	)
	if err != nil {
		return err
	}
	if err := verifyGroundingFiles(runtime.definition, finalFiles); err != nil {
		return err
	}
	verified, err := runtime.client.GetVectorStoreContext(
		commandContext(cmd),
		remote.ID,
	)
	if err != nil {
		return err
	}
	if verified == nil || !grounding.ManagedStore(verified.Metadata, runtime.definition.Name) {
		return errs.Conflict(
			"vector store %q synchronization completed but manager ownership could not be verified",
			runtime.definition.Name,
		)
	}
	result.Status = verified.Status
	status := evaluateGroundingStatus(runtime.definition, verified, finalFiles)
	result.Files = status.Files
	if !status.InSync {
		return errs.Conflict(
			"vector store %q did not reconcile to the desired file set",
			runtime.definition.Name,
		)
	}
	if created {
		result.Changed = true
	}
	if err := store.AddResource(receipt.ResourceChange{
		Kind:   "foundry-vector-store",
		Name:   runtime.definition.Name,
		ID:     remote.ID,
		Action: "synchronize",
		Status: "succeeded",
	}); err != nil {
		return err
	}
	completion := "unchanged"
	if result.Changed {
		completion = "succeeded"
	}
	if err := store.Complete(completion, nil); err != nil {
		return err
	}
	return printResult(
		cmd,
		result,
		fmt.Sprintf(
			"grounding synchronized: name=%s id=%s uploaded=%d detached=%d deleted_uploads=%d\n  receipt: %s",
			result.Name,
			result.ID,
			result.Uploaded,
			result.Detached,
			result.Deleted,
			result.Receipt,
		),
	)
}

func cmdGroundingDeleteFile(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, err := groundingRuntime(cmd, false)
	if err != nil {
		return err
	}
	relative := filepath.Clean(strings.TrimSpace(getFlag(cmd, "file")))
	if err := netcheck.ValidateRelativeFileReference(
		relative,
		"--file",
	); err != nil {
		return err
	}
	pathSum := sha256.Sum256([]byte(filepath.ToSlash(relative)))
	pathHash := hex.EncodeToString(pathSum[:])
	remote, err := findManagedVectorStore(
		commandContext(cmd),
		runtime.client,
		runtime.definition,
	)
	if err != nil {
		return err
	}
	result := groundingMutationResult{
		Action: "delete-file",
		Name:   runtime.definition.Name,
		File:   relative,
		DryRun: getBoolFlag(cmd, "dry-run"),
	}
	if remote == nil {
		return printResult(cmd, result, fmt.Sprintf(
			"managed vector store %q not found",
			runtime.definition.Name,
		))
	}
	result.ID = remote.ID
	files, err := listGroundingFiles(
		commandContext(cmd),
		runtime.client,
		remote.ID,
	)
	if err != nil {
		return err
	}
	matches := managedAttachmentsForPath(files, pathHash)
	if len(matches) == 0 {
		return printResult(cmd, result, fmt.Sprintf(
			"managed file %q is not attached to vector store %q",
			relative,
			runtime.definition.Name,
		))
	}
	if result.DryRun {
		result.Changed = true
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would detach %d managed attachment(s) for %q from vector store %q",
			len(matches),
			relative,
			runtime.definition.Name,
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Detach managed file %q from vector store %q?",
		relative,
		runtime.definition.Name,
	)); err != nil {
		return err
	}
	store, err := newGroundingOperationStore(
		cmd,
		runtime.resolved,
		runtime.definition,
		runtime.endpoint,
		"grounding-delete-file",
	)
	if err != nil {
		return err
	}
	result.Receipt = store.Path
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	var detached []foundry.VectorStoreFile
	for _, found := range matches {
		removed, detachErr := detachGroundingFile(
			commandContext(cmd),
			runtime.client,
			remote.ID,
			found.ID,
		)
		if detachErr != nil {
			return releaseFailure(store.Path, detachErr)
		}
		result.Changed = result.Changed || removed
		if removed {
			detached = append(detached, found)
			if err := store.AddResource(receipt.ResourceChange{
				Kind:   "foundry-vector-store-file",
				Name:   relative,
				ID:     found.ID,
				Action: "detach",
				Status: "succeeded",
			}); err != nil {
				return err
			}
		}
	}
	if result.Changed {
		_, updateErr := runtime.client.UpdateVectorStoreContext(
			commandContext(cmd),
			remote.ID,
			runtime.definition.Name,
			runtime.definition.Description,
			grounding.InProgressMetadata(runtime.definition),
		)
		if updateErr != nil {
			if errs.IsAmbiguousMutation(updateErr) {
				reconciled, reconcileErr := runtime.client.GetVectorStoreContext(
					commandContext(cmd),
					remote.ID,
				)
				if reconcileErr != nil || reconciled == nil ||
					attributeString(
						reconciled.Metadata,
						grounding.MetadataDesiredHash,
					) != "" {
					return releaseFailure(store.Path, updateErr)
				}
			} else {
				return updateErr
			}
		}
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry-vector-store",
			Name:   runtime.definition.Name,
			ID:     remote.ID,
			Action: "invalidate",
			Status: "succeeded",
		}); err != nil {
			return err
		}
	}
	if getBoolFlag(cmd, "delete-upload") {
		for _, found := range detached {
			if _, deleteErr := deleteUploadedFile(
				commandContext(cmd),
				runtime.client,
				found.ID,
			); deleteErr != nil {
				return releaseFailure(store.Path, deleteErr)
			}
			if err := store.AddResource(receipt.ResourceChange{
				Kind:   "foundry-file",
				Name:   relative,
				ID:     found.ID,
				Action: "delete",
				Status: "succeeded",
			}); err != nil {
				return err
			}
		}
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"managed file detached: store=%s file=%s\n  receipt: %s",
		runtime.definition.Name,
		relative,
		result.Receipt,
	))
}

func cmdGroundingDeleteStore(cmd *cobra.Command, _ []string) (returnErr error) {
	runtime, err := groundingRuntime(cmd, false)
	if err != nil {
		return err
	}
	remote, err := findManagedVectorStore(
		commandContext(cmd),
		runtime.client,
		runtime.definition,
	)
	if err != nil {
		return err
	}
	result := groundingMutationResult{
		Action: "delete-store",
		Name:   runtime.definition.Name,
		DryRun: getBoolFlag(cmd, "dry-run"),
	}
	if remote == nil {
		return printResult(cmd, result, fmt.Sprintf(
			"managed vector store %q not found",
			runtime.definition.Name,
		))
	}
	result.ID = remote.ID
	files, err := listGroundingFiles(
		commandContext(cmd),
		runtime.client,
		remote.ID,
	)
	if err != nil {
		return err
	}
	if result.DryRun {
		result.Changed = true
		return printResult(cmd, result, fmt.Sprintf(
			"dry run: would delete vector store %q (%s); managed uploads=%d",
			runtime.definition.Name,
			remote.ID,
			len(files),
		))
	}
	if err := confirmDestructive(cmd, fmt.Sprintf(
		"Delete managed vector store %q (%s)?",
		runtime.definition.Name,
		remote.ID,
	)); err != nil {
		return err
	}
	store, err := newGroundingOperationStore(
		cmd,
		runtime.resolved,
		runtime.definition,
		runtime.endpoint,
		"grounding-delete-store",
	)
	if err != nil {
		return err
	}
	result.Receipt = store.Path
	defer func() {
		if returnErr != nil && store.Receipt.CompletedAt == nil {
			_ = store.Complete(operationFailureStatus(returnErr), returnErr)
		}
	}()
	removed, deleteErr := runtime.client.DeleteVectorStoreContext(
		commandContext(cmd),
		remote.ID,
	)
	if deleteErr != nil {
		if errs.IsAmbiguousMutation(deleteErr) {
			reconciled, reconcileErr := runtime.client.GetVectorStoreContext(
				commandContext(cmd),
				remote.ID,
			)
			if reconcileErr == nil && reconciled == nil {
				removed = true
			} else {
				_ = store.AddResource(receipt.ResourceChange{
					Kind:           "foundry-vector-store",
					Name:           runtime.definition.Name,
					ID:             remote.ID,
					Action:         "delete",
					Status:         "uncertain",
					Reconciliation: "Read vector store " + remote.ID + " before retrying deletion.",
				})
				return releaseFailure(store.Path, deleteErr)
			}
		} else {
			return deleteErr
		}
	}
	result.Changed = removed
	if removed {
		if err := store.AddResource(receipt.ResourceChange{
			Kind:   "foundry-vector-store",
			Name:   runtime.definition.Name,
			ID:     remote.ID,
			Action: "delete",
			Status: "succeeded",
		}); err != nil {
			return err
		}
	}
	if getBoolFlag(cmd, "delete-uploads") {
		for _, file := range files {
			if !grounding.ManagedFile(file.Attributes) {
				continue
			}
			if _, deleteErr := deleteUploadedFile(
				commandContext(cmd),
				runtime.client,
				file.ID,
			); deleteErr != nil {
				return releaseFailure(store.Path, deleteErr)
			}
			if err := store.AddResource(receipt.ResourceChange{
				Kind:   "foundry-file",
				Name:   attributeString(file.Attributes, grounding.AttributeFilename),
				ID:     file.ID,
				Action: "delete",
				Status: "succeeded",
			}); err != nil {
				return err
			}
		}
	}
	if err := store.Complete("succeeded", nil); err != nil {
		return err
	}
	return printResult(cmd, result, fmt.Sprintf(
		"managed vector store deleted: name=%s id=%s\n  receipt: %s",
		result.Name,
		result.ID,
		result.Receipt,
	))
}

func groundingRuntime(
	cmd *cobra.Command,
	inspectFiles bool,
) (*groundingCommandRuntime, error) {
	resolved, definitions, err := resolveGroundingDefinitions(cmd, inspectFiles, false)
	if err != nil {
		return nil, err
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
	return &groundingCommandRuntime{
		resolved:   resolved,
		definition: definitions[0],
		endpoint:   endpoint,
		client:     newFoundryClient(endpoint, resolved.Config, credential, httpClient),
	}, nil
}

func resolveGroundingDefinitions(
	cmd *cobra.Command,
	inspectFiles bool,
	allowMultiple bool,
) (*resolvedManifest, []grounding.VectorStore, error) {
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, nil, err
	}
	definitions, err := grounding.Build(
		resolved.Config.Grounding,
		resolved.BaseDir,
		inspectFiles,
	)
	if err != nil {
		return nil, nil, err
	}
	if len(definitions) == 0 {
		return nil, nil, errs.Config(
			"manifest defines no grounding.vector_stores",
		)
	}
	selected := strings.TrimSpace(getFlag(cmd, "grounding"))
	if selected != "" {
		for _, definition := range definitions {
			if strings.EqualFold(definition.Name, selected) {
				return resolved, []grounding.VectorStore{definition}, nil
			}
		}
		return nil, nil, errs.NotFound(
			"managed vector store %q is not defined in the manifest",
			selected,
		)
	}
	if allowMultiple || len(definitions) == 1 {
		return resolved, definitions, nil
	}
	return nil, nil, errs.Config(
		"manifest defines %d managed vector stores; select one with --grounding",
		len(definitions),
	)
}

func findManagedVectorStore(
	ctx context.Context,
	client *foundry.Client,
	definition grounding.VectorStore,
) (*foundry.VectorStore, error) {
	stores, err := client.ListVectorStoresContext(ctx)
	if err != nil {
		return nil, err
	}
	var matches []foundry.VectorStore
	for _, store := range stores {
		if grounding.ManagedStore(store.Metadata, definition.Name) {
			matches = append(matches, store)
		}
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		var ids []string
		for _, store := range matches {
			ids = append(ids, store.ID)
		}
		sort.Strings(ids)
		return nil, errs.Conflict(
			"managed vector store %q is ambiguous; matching IDs: %s. Reconcile duplicate stores before retrying",
			definition.Name,
			strings.Join(ids, ", "),
		)
	}
	return &matches[0], nil
}

func resolvePreparedManagedGrounding(
	ctx context.Context,
	client *foundry.Client,
	prepared *preparedAgent,
) error {
	names := tools.ManagedVectorStoreNames(prepared.WireTools)
	if len(names) == 0 {
		return nil
	}
	ids, err := resolveManagedVectorStoreIDs(ctx, client, prepared.Grounding, names)
	if err != nil {
		return err
	}
	resolved, err := tools.ResolveManagedVectorStores(prepared.WireTools, ids)
	if err != nil {
		return err
	}
	prepared.WireTools = resolved
	prepared.Desired.Tools = resolved
	return nil
}

func resolveToolboxManagedGrounding(
	ctx context.Context,
	client *foundry.Client,
	definitions []grounding.VectorStore,
	definition tools.ToolboxDefinition,
) (tools.ToolboxDefinition, error) {
	names := tools.ManagedVectorStoreNames(definition.Tools)
	if len(names) == 0 {
		return definition, nil
	}
	ids, err := resolveManagedVectorStoreIDs(ctx, client, definitions, names)
	if err != nil {
		return tools.ToolboxDefinition{}, err
	}
	return tools.ResolveToolboxManagedVectorStores(definition, ids)
}

func resolveManagedVectorStoreIDs(
	ctx context.Context,
	client *foundry.Client,
	definitions []grounding.VectorStore,
	names []string,
) (map[string]string, error) {
	definitionByName := make(map[string]grounding.VectorStore, len(definitions))
	for _, definition := range definitions {
		definitionByName[strings.ToLower(definition.Name)] = definition
	}
	result := make(map[string]string, len(names))
	for _, name := range names {
		definition, exists := definitionByName[strings.ToLower(name)]
		if !exists {
			return nil, errs.Config(
				"managed vector store %q is not defined under grounding.vector_stores",
				name,
			)
		}
		remote, err := findManagedVectorStore(ctx, client, definition)
		if err != nil {
			return nil, err
		}
		if remote == nil {
			return nil, errs.NotFound(
				"managed vector store %q does not exist; run fam grounding sync first",
				definition.Name,
			)
		}
		files, err := listGroundingFiles(ctx, client, remote.ID)
		if err != nil {
			return nil, err
		}
		status := evaluateGroundingStatus(definition, remote, files)
		if !status.InSync {
			return nil, errs.Conflict(
				"managed vector store %q is not synchronized to desired hash %s; run fam grounding sync before deploying",
				definition.Name,
				definition.DesiredHash,
			)
		}
		result[name] = remote.ID
	}
	return result, nil
}

func uploadGroundingFile(
	ctx context.Context,
	runtime *groundingCommandRuntime,
	file grounding.File,
) (*foundry.OpenAIFile, error) {
	source, info, err := netcheck.OpenContainedFile(
		runtime.resolved.BaseDir,
		file.Path,
		fmt.Sprintf(
			"grounding vector store %q file %q",
			runtime.definition.Name,
			file.Path,
		),
		netcheck.MaxGroundingFileBytes,
	)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	if info.Size() != file.Size {
		return nil, errs.Security(
			"grounding file %q changed size after validation; retry with a stable file",
			file.Path,
		)
	}
	hasher := sha256.New()
	uploaded, err := runtime.client.UploadFileContext(
		ctx,
		grounding.ManagedUploadFilename(file),
		io.TeeReader(source, hasher),
	)
	if err != nil {
		return nil, err
	}
	actualHash := hex.EncodeToString(hasher.Sum(nil))
	if actualHash != file.SHA256 {
		cleanupErr := errors.New("uploaded file content changed during synchronization")
		if _, err := runtime.client.DeleteFileContext(ctx, uploaded.ID); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
		return nil, errs.SecurityWrap(
			cleanupErr,
			"grounding file %q changed after validation",
			file.Path,
		)
	}
	if uploaded.Bytes != 0 && uploaded.Bytes != file.Size {
		return nil, errs.Conflict(
			"uploaded file %q reported %d bytes; expected %d",
			file.Path,
			uploaded.Bytes,
			file.Size,
		)
	}
	return uploaded, nil
}

func detachGroundingFile(
	ctx context.Context,
	client *foundry.Client,
	vectorStoreID string,
	fileID string,
) (bool, error) {
	removed, err := client.DeleteVectorStoreFileContext(ctx, vectorStoreID, fileID)
	if err == nil {
		return removed, nil
	}
	if !errs.IsAmbiguousMutation(err) {
		return false, err
	}
	found, reconcileErr := client.GetVectorStoreFileContext(ctx, vectorStoreID, fileID)
	if reconcileErr == nil && found == nil {
		return true, nil
	}
	return false, err
}

func deleteUploadedFile(
	ctx context.Context,
	client *foundry.Client,
	fileID string,
) (bool, error) {
	removed, err := client.DeleteFileContext(ctx, fileID)
	if err == nil {
		return removed, nil
	}
	if !errs.IsAmbiguousMutation(err) {
		return false, err
	}
	found, reconcileErr := client.GetFileContext(ctx, fileID)
	if reconcileErr == nil && found == nil {
		return true, nil
	}
	return false, err
}

func listGroundingFiles(
	ctx context.Context,
	client *foundry.Client,
	vectorStoreID string,
) ([]foundry.VectorStoreFile, error) {
	files, err := client.ListVectorStoreFilesContext(ctx, vectorStoreID)
	if err != nil {
		return nil, err
	}
	for index := range files {
		if grounding.ManagedFile(files[index].Attributes) {
			continue
		}
		uploaded, err := client.GetFileContext(ctx, files[index].ID)
		if err != nil {
			return nil, err
		}
		if uploaded == nil {
			continue
		}
		attributes, managed := grounding.FileAttributesFromUploadFilename(uploaded.Filename)
		if managed {
			files[index].Attributes = attributes
		}
	}
	return files, nil
}

func managedAttachmentsForPath(
	files []foundry.VectorStoreFile,
	pathHash string,
) []foundry.VectorStoreFile {
	var result []foundry.VectorStoreFile
	for _, file := range files {
		if grounding.ManagedFile(file.Attributes) &&
			attributeString(file.Attributes, grounding.AttributePathHash) == pathHash {
			result = append(result, file)
		}
	}
	return result
}

func desiredAttachment(
	files []foundry.VectorStoreFile,
	sha string,
) *foundry.VectorStoreFile {
	for index := range files {
		if attributeString(files[index].Attributes, grounding.AttributeSHA256) == sha &&
			strings.EqualFold(files[index].Status, "completed") {
			return &files[index]
		}
	}
	for index := range files {
		if attributeString(files[index].Attributes, grounding.AttributeSHA256) == sha &&
			!strings.EqualFold(files[index].Status, "failed") &&
			!strings.EqualFold(files[index].Status, "cancelled") {
			return &files[index]
		}
	}
	return nil
}

func removedManagedAttachments(
	definition grounding.VectorStore,
	files []foundry.VectorStoreFile,
) []foundry.VectorStoreFile {
	expected := make(map[string]struct{}, len(definition.Files))
	for _, file := range definition.Files {
		expected[file.PathHash] = struct{}{}
	}
	var result []foundry.VectorStoreFile
	for _, file := range files {
		if !grounding.ManagedFile(file.Attributes) {
			continue
		}
		if _, exists := expected[attributeString(file.Attributes, grounding.AttributePathHash)]; !exists {
			result = append(result, file)
		}
	}
	return result
}

func replacementUploadCount(
	definition grounding.VectorStore,
	files []foundry.VectorStoreFile,
) int {
	count := 0
	for _, desired := range definition.Files {
		matches := managedAttachmentsForPath(files, desired.PathHash)
		ready := desiredAttachment(matches, desired.SHA256)
		for _, found := range matches {
			if ready == nil || found.ID != ready.ID {
				count++
			}
		}
	}
	return count
}

func verifyGroundingFiles(
	definition grounding.VectorStore,
	files []foundry.VectorStoreFile,
) error {
	status := evaluateGroundingStatus(definition, &foundry.VectorStore{
		ID:       "verification",
		Status:   "completed",
		Metadata: grounding.Metadata(definition, definition.DesiredHash),
	}, files)
	if status.StaleManagedFiles != 0 {
		return errs.Conflict(
			"managed vector store %q still contains %d stale managed file(s)",
			definition.Name,
			status.StaleManagedFiles,
		)
	}
	for _, file := range status.Files {
		if !file.InSync {
			return errs.Conflict(
				"managed vector store %q file %q is not ready",
				definition.Name,
				file.Path,
			)
		}
	}
	return nil
}

func evaluateGroundingStatus(
	definition grounding.VectorStore,
	remote *foundry.VectorStore,
	files []foundry.VectorStoreFile,
) groundingStatusResult {
	result := groundingStatusResult{
		Name:        definition.Name,
		DesiredHash: definition.DesiredHash,
		Exists:      remote != nil,
	}
	if remote == nil {
		for _, desired := range definition.Files {
			result.Files = append(result.Files, groundingFileStatus{
				Path:   desired.Path,
				Status: "missing",
				SHA256: desired.SHA256,
			})
		}
		return result
	}
	result.ID = remote.ID
	result.Status = remote.Status
	result.RemoteDesiredHash = attributeString(
		remote.Metadata,
		grounding.MetadataDesiredHash,
	)
	desiredByPath := make(map[string]grounding.File, len(definition.Files))
	selectedByPath := make(map[string]string, len(definition.Files))
	for _, desired := range definition.Files {
		desiredByPath[desired.PathHash] = desired
		matches := managedAttachmentsForPath(files, desired.PathHash)
		found := desiredAttachment(matches, desired.SHA256)
		status := groundingFileStatus{
			Path:   desired.Path,
			Status: "missing",
			SHA256: desired.SHA256,
		}
		if found != nil {
			selectedByPath[desired.PathHash] = found.ID
			status.FileID = found.ID
			status.Status = found.Status
			status.RemoteSHA256 = attributeString(
				found.Attributes,
				grounding.AttributeSHA256,
			)
			status.InSync = strings.EqualFold(found.Status, "completed") &&
				status.RemoteSHA256 == desired.SHA256
		}
		result.Files = append(result.Files, status)
	}
	for _, file := range files {
		if !grounding.ManagedFile(file.Attributes) {
			result.UnmanagedFiles++
			continue
		}
		pathHash := attributeString(file.Attributes, grounding.AttributePathHash)
		desired, exists := desiredByPath[pathHash]
		if !exists ||
			attributeString(file.Attributes, grounding.AttributeSHA256) != desired.SHA256 ||
			selectedByPath[pathHash] != file.ID {
			result.StaleManagedFiles++
		}
	}
	result.InSync = strings.EqualFold(remote.Status, "completed") &&
		grounding.ManagedStore(remote.Metadata, definition.Name) &&
		result.StaleManagedFiles == 0
	for _, file := range result.Files {
		result.InSync = result.InSync && file.InSync
	}
	return result
}

func groundingStatusText(result groundingStatusResult) string {
	if !result.Exists {
		return fmt.Sprintf("managed vector store %q does not exist", result.Name)
	}
	return fmt.Sprintf(
		"grounding status: name=%s id=%s status=%s in_sync=%t files=%d stale=%d unmanaged=%d",
		result.Name,
		result.ID,
		result.Status,
		result.InSync,
		len(result.Files),
		result.StaleManagedFiles,
		result.UnmanagedFiles,
	)
}

func groundingWaitDurations(cmd *cobra.Command) (time.Duration, time.Duration, error) {
	timeout := getDurationFlag(cmd, "index-timeout")
	interval := getDurationFlag(cmd, "index-interval")
	if timeout <= 0 {
		return 0, 0, errs.Config("--index-timeout must be greater than zero")
	}
	if interval <= 0 {
		return 0, 0, errs.Config("--index-interval must be greater than zero")
	}
	if interval > timeout {
		return 0, 0, errs.Config("--index-interval must not exceed --index-timeout")
	}
	return timeout, interval, nil
}

func newGroundingOperationStore(
	cmd *cobra.Command,
	resolved *resolvedManifest,
	definition grounding.VectorStore,
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
	return newManagedOperationStore(
		cmd,
		path,
		operation,
		resolved.Config.Cloud.Name,
		receipt.ManifestReference{
			Path:        resolved.ManifestPath,
			Hash:        resolved.ManifestHash,
			DesiredHash: definition.DesiredHash,
		},
		receipt.ResourceReference{
			Name:     resolved.Config.Project.Name,
			Endpoint: endpoint,
		},
		resolved.Config.Agent.Name,
	)
}

func groundingBytes(definition grounding.VectorStore) int64 {
	var total int64
	for _, file := range definition.Files {
		total += file.Size
	}
	return total
}

func attributeString(values map[string]interface{}, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
