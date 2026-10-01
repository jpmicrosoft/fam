package hostedskills

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	errs "foundry-agent-manager/internal/errors"
)

const transactionPrefix = ".fam-skills-"
const lockDirectory = transactionPrefix + "sync.lock"

// A same-directory rename transaction never exposes a partially written bundle.
// A crash between renames leaves an explicit recovery directory, not successful
// output. Cleanup removes only inventoried files whose bytes still match.
func replaceManaged(ctx context.Context, source *os.Root, service string, previous *managedFiles, files map[string][]byte) (resultErr error) {
	if err := checkTransactions(source); err != nil {
		return err
	}
	if err := source.Mkdir(lockDirectory, 0o700); err != nil {
		return errs.Config("cannot acquire Hosted Skill sync lock; resolve an interrupted or concurrent sync: %v", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, source.Remove(lockDirectory))
	}()
	old, err := readManaged(source, DirectoryName, service)
	if err != nil {
		return err
	}
	if !sameManaged(previous, old) {
		return errs.Config("Hosted Skill output changed during synchronization; no output was replaced")
	}
	inventory := ownership{FormatVersion: 1, Owner: ownerName, Service: service, Files: make(map[string]string)}
	for name, data := range files {
		if !validManagedPath(name) {
			return errs.Security("refusing unsafe generated Hosted Skill path %q", name)
		}
		inventory.Files[name] = digest(data)
	}
	if err := validateManagedPaths(inventory.Files); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return errs.Config("cannot encode Hosted Skill ownership: %v", err)
	}
	files[ownershipFile] = append(encoded, '\n')
	stage := transactionPrefix + "stage-" + rand.Text()
	if err := source.Mkdir(stage, 0o755); err != nil {
		return errs.Config("cannot stage Hosted Skill output: %v", err)
	}
	staged := true
	defer func() {
		if staged {
			resultErr = errors.Join(resultErr, removeVerified(source, stage, files))
		}
	}()
	if err := writeStage(ctx, source, stage, files); err != nil {
		return err
	}
	prepared, err := readManaged(source, stage, service)
	if err != nil {
		return err
	}
	if prepared == nil {
		return errs.Config("staged Hosted Skill output disappeared")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := readManaged(source, DirectoryName, service)
	if err != nil {
		return err
	}
	if !sameManaged(old, current) {
		return errs.Config("Hosted Skill output changed during synchronization; no output was replaced")
	}
	backup := transactionPrefix + "backup-" + rand.Text()
	if old != nil {
		if err := source.Rename(DirectoryName, backup); err != nil {
			return errs.Config("cannot preserve previous Hosted Skill output: %v", err)
		}
		preserved, err := readManaged(source, backup, service)
		if err != nil || !sameManaged(old, preserved) {
			restoreErr := source.Rename(backup, DirectoryName)
			return errors.Join(errs.Config("previous Hosted Skill output changed during replacement"), err, restoreErr)
		}
	}
	if err := source.Rename(stage, DirectoryName); err != nil {
		var restoreErr error
		if old != nil {
			restoreErr = source.Rename(backup, DirectoryName)
		}
		return errors.Join(errs.Config("cannot install Hosted Skill output: %v", err), restoreErr)
	}
	staged = false
	if old != nil {
		if err := removeVerified(source, backup, old.data); err != nil {
			return errs.Config("Hosted Skill output installed, but previous output at %q requires manual recovery: %v", backup, err)
		}
	}
	return nil
}

func sameManaged(first, second *managedFiles) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return first.identity == second.identity
}

func checkTransactions(source *os.Root) error {
	directory, err := source.Open(".")
	if err != nil {
		return errs.Config("cannot inspect Hosted Skill synchronization state: %v", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errs.Config("cannot inspect Hosted Skill synchronization state: %v", errors.Join(readErr, closeErr))
	}
	for _, entry := range entries {
		if strings.HasPrefix(strings.ToLower(entry.Name()), transactionPrefix) {
			return errs.Config("Hosted Skill sync is active or interrupted at %q; preserve and resolve that transaction before continuing", entry.Name())
		}
		if strings.EqualFold(entry.Name(), DirectoryName) && entry.Name() != DirectoryName {
			return errs.Security("Hosted Skill output has a case-colliding unowned directory %q", entry.Name())
		}
	}
	return nil
}

func writeStage(ctx context.Context, source *os.Root, stage string, files map[string][]byte) error {
	names := sortedPaths(files)
	createdDirectories := make(map[string]bool)
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := createStageParents(source, stage, name, createdDirectories); err != nil {
			return err
		}
		file, err := source.OpenFile(filepath.Join(stage, filepath.FromSlash(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return errs.Config("cannot stage Hosted Skill file %q: %v", name, err)
		}
		_, writeErr := file.Write(files[name])
		syncErr := file.Sync()
		closeErr := file.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			return errs.Config("cannot flush staged Hosted Skill file %q: %v", name, err)
		}
	}
	return nil
}

func createStageParents(source *os.Root, stage, name string, created map[string]bool) error {
	parts := strings.Split(name, "/")
	directory := ""
	for _, part := range parts[:len(parts)-1] {
		directory = filepath.Join(directory, part)
		if !created[directory] {
			if err := source.Mkdir(filepath.Join(stage, directory), 0o755); err != nil {
				return errs.Config("cannot stage Hosted Skill directory: %v", err)
			}
			created[directory] = true
		}
	}
	return nil
}

func sortedPaths(files map[string][]byte) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func removeVerified(source *os.Root, name string, expected map[string][]byte) error {
	directory, err := openDirectory(source, name)
	if err != nil {
		return err
	}
	allowedDirectories := map[string]bool{".": true}
	for file := range expected {
		addParentDirectories(allowedDirectories, file)
	}
	var files, directories []string
	err = fs.WalkDir(directory.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errs.Security("refusing to clean changed Hosted Skill transaction link %q", path)
		}
		if entry.IsDir() {
			if !allowedDirectories[path] {
				return errs.Security("refusing to clean unowned Hosted Skill transaction directory %q", path)
			}
			if path != "." {
				directories = append(directories, path)
			}
			return nil
		}
		want, found := expected[path]
		if !found {
			return errs.Security("refusing to clean unowned Hosted Skill transaction file %q", path)
		}
		data, err := readRegular(directory, path, maxArtifactFileBytes)
		if err != nil {
			return err
		}
		if !bytes.Equal(data, want) {
			return errs.Security("refusing to clean modified Hosted Skill transaction file %q", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		directory.Close()
		return err
	}
	for _, path := range files {
		if err := directory.Remove(filepath.FromSlash(path)); err != nil {
			directory.Close()
			return err
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(directories)))
	for _, path := range directories {
		if err := directory.Remove(filepath.FromSlash(path)); err != nil {
			directory.Close()
			return err
		}
	}
	if err := directory.Close(); err != nil {
		return err
	}
	return source.Remove(name)
}
