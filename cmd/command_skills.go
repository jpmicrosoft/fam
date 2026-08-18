package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/foundry"
	"foundry-agent-manager/internal/netcheck"
	"foundry-agent-manager/internal/receipt"

	"github.com/spf13/cobra"
)

func newSkillRuntime(
	cmd *cobra.Command,
) (*resolvedManifest, *foundry.Client, error) {
	if !getBoolFlag(cmd, "accept-preview") {
		return nil, nil, errs.Config(
			"Skills are preview; rerun with --accept-preview after reviewing the preview limitations.",
		)
	}
	resolved, err := resolveManifest(cmd)
	if err != nil {
		return nil, nil, err
	}
	credential, err := newCredential(cmd, resolved.Config.Cloud)
	if err != nil {
		return nil, nil, err
	}
	httpClient := newHTTPClient(cmd)
	endpoint, err := resolveProjectEndpoint(cmd, resolved.Config, credential, httpClient)
	if err != nil {
		return nil, nil, err
	}
	return resolved, newFoundryClient(endpoint, resolved.Config, credential, httpClient), nil
}

func cmdSkillCreate(cmd *cobra.Command, _ []string) error {
	resolved, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	ctx := commandContext(cmd)
	name := getFlag(cmd, "skill")
	setDefault := getBoolFlag(cmd, "default")
	var result *foundry.SkillVersion
	source := getFlag(cmd, "path")
	if source != "" {
		files, loadErr := loadSkillFiles(resolved.BaseDir, source)
		if loadErr != nil {
			return loadErr
		}
		result, err = client.CreateSkillFromFilesContext(ctx, name, files, setDefault)
	} else {
		instructionsFile := getFlag(cmd, "skill-instructions-file")
		if instructionsFile == "" {
			return errs.Config("skill-create requires --path or --skill-instructions-file")
		}
		instructions, loadErr := readRequiredTextFile(
			resolved.BaseDir,
			instructionsFile,
			"skill instructions",
		)
		if loadErr != nil {
			return loadErr
		}
		description := getFlag(cmd, "skill-description")
		if description == "" {
			return errs.Config("inline skill creation requires --skill-description")
		}
		result, err = client.CreateSkillInlineContext(
			ctx,
			name,
			foundry.SkillInlineContent{
				Description:   description,
				Instructions:  instructions,
				License:       getFlag(cmd, "license"),
				Compatibility: getFlag(cmd, "compatibility"),
				AllowedTools:  stringSliceFlag(cmd, "allowed-tools"),
			},
			setDefault,
		)
	}
	if err != nil {
		return err
	}
	if err := writeSkillReceipt(
		resolved, cmd, "skill-create", "foundry_skill", name, result.ID, result.Version,
	); err != nil {
		return err
	}
	return skillOutput(cmd, result, "created skill version")
}

func cmdSkillList(cmd *cobra.Command, _ []string) error {
	_, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := client.ListSkillsContext(commandContext(cmd))
	if err != nil {
		return err
	}
	return skillOutput(cmd, result, "listed skills")
}

func cmdSkillShow(cmd *cobra.Command, _ []string) error {
	_, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := client.GetSkillContext(commandContext(cmd), getFlag(cmd, "skill"))
	if err != nil {
		return err
	}
	if result == nil {
		return errs.NotFound("skill %q was not found", getFlag(cmd, "skill"))
	}
	return skillOutput(cmd, result, "showed skill")
}

func cmdSkillVersionList(cmd *cobra.Command, _ []string) error {
	_, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := client.ListSkillVersionsContext(commandContext(cmd), getFlag(cmd, "skill"))
	if err != nil {
		return err
	}
	return skillOutput(cmd, result, "listed skill versions")
}

func cmdSkillVersionShow(cmd *cobra.Command, _ []string) error {
	_, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	result, err := client.GetSkillVersionContext(
		commandContext(cmd),
		getFlag(cmd, "skill"),
		getFlag(cmd, "version"),
	)
	if err != nil {
		return err
	}
	if result == nil {
		return errs.NotFound(
			"skill %q version %s was not found",
			getFlag(cmd, "skill"),
			getFlag(cmd, "version"),
		)
	}
	return skillOutput(cmd, result, "showed skill version")
}

func cmdSkillSetDefault(cmd *cobra.Command, _ []string) error {
	resolved, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "skill")
	version := getFlag(cmd, "version")
	result, err := client.SetSkillDefaultContext(commandContext(cmd), name, version)
	if err != nil {
		return err
	}
	if err := writeSkillReceipt(
		resolved, cmd, "skill-set-default", "foundry_skill", name, result.ID, result.DefaultVersion,
	); err != nil {
		return err
	}
	return skillOutput(cmd, result, "updated skill default version")
}

func cmdSkillDelete(cmd *cobra.Command, _ []string) error {
	resolved, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "skill")
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("skill-delete is destructive; rerun with --yes")
	}
	deleted, err := client.DeleteSkillContext(commandContext(cmd), name)
	if err != nil {
		return err
	}
	if err := writeSkillReceipt(
		resolved, cmd, "skill-delete", "foundry_skill", name, "", "",
	); err != nil {
		return err
	}
	return skillOutput(cmd, map[string]interface{}{"name": name, "deleted": deleted}, "deleted skill")
}

func cmdSkillVersionDelete(cmd *cobra.Command, _ []string) error {
	resolved, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "skill")
	version := getFlag(cmd, "version")
	if !getBoolFlag(cmd, "yes") {
		return errs.Config("skill-version-delete is destructive; rerun with --yes")
	}
	deleted, err := client.DeleteSkillVersionContext(commandContext(cmd), name, version)
	if err != nil {
		return err
	}
	if err := writeSkillReceipt(
		resolved, cmd, "skill-version-delete", "foundry_skill_version", name, "", version,
	); err != nil {
		return err
	}
	return skillOutput(cmd, map[string]interface{}{
		"name": name, "version": version, "deleted": deleted,
	}, "deleted skill version")
}

func cmdSkillDownload(cmd *cobra.Command, _ []string) error {
	resolved, client, err := newSkillRuntime(cmd)
	if err != nil {
		return err
	}
	name := getFlag(cmd, "skill")
	version := getFlag(cmd, "version")
	data, err := client.DownloadSkillContext(commandContext(cmd), name, version)
	if err != nil {
		return err
	}
	output := getFlag(cmd, "destination")
	if !filepath.IsAbs(output) {
		output = filepath.Join(resolved.BaseDir, output)
	}
	output = filepath.Clean(output)
	if !getBoolFlag(cmd, "force") {
		if _, statErr := os.Stat(output); statErr == nil {
			return errs.Config("destination already exists: %s (use --force to replace it)", output)
		} else if !os.IsNotExist(statErr) {
			return errs.Config("failed to inspect destination %s: %v", output, statErr)
		}
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
		return errs.Config("failed to create skill download directory: %v", err)
	}
	if err := os.WriteFile(output, data, 0o600); err != nil {
		return errs.Config("failed to write skill download %s: %v", output, err)
	}
	return skillOutput(cmd, map[string]interface{}{
		"name": name, "version": version, "destination": output, "bytes": len(data),
	}, "downloaded skill")
}

func loadSkillFiles(baseDir, source string) ([]foundry.SkillFile, error) {
	path := source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errs.Config("failed to inspect skill path %s: %v", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errs.Security("skill path must not be a symbolic link: %s", path)
	}
	if !info.IsDir() {
		data, err := readSkillFile(path)
		if err != nil {
			return nil, err
		}
		return []foundry.SkillFile{{Name: filepath.Base(path), Data: data}}, nil
	}

	var files []foundry.SkillFile
	var total int64
	hasSkillManifest := false
	err = filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security("skill directories must not contain symbolic links: %s", current)
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(path, current)
		if err != nil || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
			return errs.Security("skill file escaped the selected directory: %s", current)
		}
		data, err := readSkillFile(current)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > 128*1024*1024 {
			return errs.Config("skill directory exceeds the 128 MiB manager upload limit")
		}
		name := filepath.ToSlash(relative)
		if strings.EqualFold(name, "SKILL.md") {
			hasSkillManifest = true
		}
		files = append(files, foundry.SkillFile{Name: name, Data: data})
		return nil
	})
	if err != nil {
		return nil, errs.Config("failed to load skill directory %s: %v", path, err)
	}
	if !hasSkillManifest {
		return nil, errs.Config("skill directory must contain SKILL.md at its root")
	}
	return files, nil
}

func readSkillFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, errs.Config("failed to inspect skill file %s: %v", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Config("skill upload supports regular files only: %s", path)
	}
	if info.Size() > 64*1024*1024 {
		return nil, errs.Config("skill file exceeds the 64 MiB manager upload limit: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.Config("failed to read skill file %s: %v", path, err)
	}
	return data, nil
}

func readRequiredTextFile(baseDir, name, label string) (string, error) {
	data, err := netcheck.ReadContainedFile(baseDir, name, "--skill-instructions-file")
	if err != nil {
		return "", errs.Config("failed to read %s file %s: %v", label, name, err)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errs.Config("%s file is empty", label)
	}
	return value, nil
}

func stringSliceFlag(cmd *cobra.Command, name string) []string {
	value, _ := cmd.Flags().GetStringSlice(name)
	return value
}

func writeSkillReceipt(
	resolved *resolvedManifest,
	cmd *cobra.Command,
	operation string,
	kind string,
	name string,
	id string,
	version string,
) error {
	path := getFlag(cmd, "receipt")
	if path == "" {
		path = receipt.OperationPath(resolved.ManifestPath, operation, name, time.Now())
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(resolved.BaseDir, path)
	}
	store, err := newManagedOperationStore(
		cmd,
		path,
		operation,
		resolved.Config.Cloud.Name,
		receipt.ManifestReference{Path: resolved.ManifestPath, Hash: resolved.ManifestHash},
		receipt.ResourceReference{
			Name:     resolved.Config.Project.Name,
			Endpoint: resolved.Config.Project.Endpoint,
		},
		"",
	)
	if err != nil {
		return err
	}
	store.Receipt.Resources = append(store.Receipt.Resources, receipt.ResourceChange{
		Kind: kind, Name: name, ID: id, Action: operation, Status: "succeeded",
	})
	if version != "" {
		store.Receipt.Resources[0].Reconciliation = "version=" + version
	}
	if err := store.AddStep(operation, "succeeded", name); err != nil {
		return err
	}
	return store.Complete("succeeded", nil)
}

func skillOutput(cmd *cobra.Command, value interface{}, text string) error {
	return printResult(cmd, value, text)
}
