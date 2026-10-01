package hosted

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

const projectionRecoveryIgnore = "# FAM-owned azd projection recovery; do not edit.\n*\n"

// Recovery lives beside, not inside, the workspace: even a service with project
// "." must not upload these files. This requires a writable workspace parent on
// the same filesystem and hard-link support; unsupported filesystems fail closed.
func projectionRecoveryPath(workspace Workspace) string {
	return workspace.Root + ".fam-azd-projection"
}

type projectionIO struct {
	move           func(string, string) error
	publish        func(string, string) error
	syncDirectory  func(string) error
	checkHardLinks func(string) error
}

func defaultProjectionIO() projectionIO {
	return projectionIO{
		move:           os.Rename,
		publish:        os.Link,
		syncDirectory:  syncAtomicDirectory,
		checkHardLinks: checkProjectionHardLinks,
	}
}

// projectConfiguration serializes cooperating FAM projections, not editors or
// standalone azd. The inspected azd contract has no alternate-manifest input.
//
// Each replacement moves the live file into a fresh, private recovery directory,
// then links a fully written replacement into an ABSENT azure.yaml path. It never
// renames over azure.yaml. This is NOT atomic CAS: the path is briefly absent,
// editors can still change it after publication, and open handles can write the
// displaced inode later. Immutable byte snapshots AND all displaced/live inodes
// are therefore retained even on success. The recovery directory is FAM-owned;
// external mutation of that directory is not supported.
// A FAM-owned .gitignore excludes recovery files from ordinary Git additions,
// including in enclosing monorepos. It cannot protect already tracked files,
// force-adds, or uploads by other tools.
// Referenced YAML is revalidated but is not locked against editors. Directory
// durability uses syncAtomicDirectory (a no-op on Windows); this protects against
// process death, not every filesystem/power-loss failure.
//
// Any failure after displacement retains the lease and all recovery evidence.
// After stopping FAM/azd and editors, operators must reconcile azure.yaml against
// original.yaml, projected.yaml and the per-phase displaced/replacement files,
// then remove the empty "active" directory to permit another operation. No
// automatic stale-lock takeover or automatic recovery overwrites user files.
// Old operation directories can be removed manually only after closing editors
// and confirming that their retained bytes are no longer needed.
func projectConfiguration(workspace Workspace, rendered []byte, ops projectionIO) (restore func() error, err error) {
	base := projectionRecoveryPath(workspace)
	release, err := acquireProjection(base)
	if err != nil {
		return nil, err
	}
	retainLease := false
	defer func() {
		if !retainLease {
			err = errors.Join(err, release())
		}
	}()
	original, mode, err := validatedProjectionSource(workspace)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp(base, "operation-")
	if err != nil {
		return nil, fmt.Errorf("create azd projection recovery: %w", err)
	}
	fail := func(err error) error {
		return fmt.Errorf("azd projection requires recovery in %q (lease %q); stop FAM/azd and editors, reconcile retained files before removing the active lease: %w", directory, filepath.Join(base, "active"), err)
	}
	prepareFailure := func(err error) error {
		return fmt.Errorf("prepare azd projection recovery in %q; azure.yaml was not changed: %w", directory, err)
	}
	if err := ops.checkHardLinks(directory); err != nil {
		return nil, prepareFailure(err)
	}
	if err := writeProjectionFile(filepath.Join(directory, "original.yaml"), original, 0o600); err != nil {
		return nil, prepareFailure(err)
	}
	if err := writeProjectionFile(filepath.Join(directory, "projected.yaml"), rendered, 0o600); err != nil {
		return nil, prepareFailure(err)
	}
	if err := errors.Join(ops.syncDirectory(directory), ops.syncDirectory(base)); err != nil {
		return nil, prepareFailure(err)
	}
	retainLease = true
	if err := replaceProjection(workspace.AzureYAML, directory, "materialize", original, rendered, mode, ops); err != nil {
		return nil, fail(err)
	}
	var once sync.Once
	var restoreErr error
	return func() error {
		once.Do(func() {
			displacedOriginal := filepath.Join(directory, "materialize", "displaced.yaml")
			restoreErr = checkProjectionBytes(displacedOriginal, original)
			if restoreErr == nil {
				restoreErr = replaceProjection(workspace.AzureYAML, directory, "restore", rendered, original, mode, ops)
			}
			if restoreErr == nil {
				restoreErr = checkProjectionBytes(displacedOriginal, original)
			}
			if restoreErr == nil {
				restoreErr = release()
			}
			if restoreErr != nil {
				restoreErr = fail(restoreErr)
			}
		})
		return restoreErr
	}, nil
}

func acquireProjection(base string) (func() error, error) {
	if err := os.Mkdir(base, 0o700); err != nil && !os.IsExist(err) {
		return nil, fmt.Errorf("create azd projection recovery directory %q: %w", base, err)
	}
	info, err := os.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errs.Security("azd projection recovery path %q must be a directory without symbolic links", base)
	}
	active := filepath.Join(base, "active")
	if err := os.Mkdir(active, 0o700); err != nil {
		return nil, fmt.Errorf("active or interrupted azd projection; do not remove a live lease; stop FAM/azd and editors and reconcile recovery files in %q before removing %q: %w", base, active, err)
	}
	release := func() error {
		if err := os.Remove(active); err != nil {
			return fmt.Errorf("release azd projection lease %q: %w", active, err)
		}
		return syncAtomicDirectory(base)
	}
	// Establish the ignore guard under the lease, before retaining user bytes.
	if err := ensureProjectionRecoveryIgnore(base); err != nil {
		return nil, errors.Join(err, release())
	}
	if err := errors.Join(syncAtomicDirectory(base), syncAtomicDirectory(filepath.Dir(base))); err != nil {
		return nil, errors.Join(err, release())
	}
	return release, nil
}

func ensureProjectionRecoveryIgnore(base string) error {
	path := filepath.Join(base, ".gitignore")
	expected := []byte(projectionRecoveryIgnore)
	if err := writeProjectionFile(path, expected, 0o600); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create FAM-owned recovery ignore %q: %w", path, err)
	}
	current, err := readProjectionFile(path, int64(len(expected)))
	if err != nil {
		return fmt.Errorf("conflicting or unowned recovery ignore %q; refusing to retain configuration: %w", path, err)
	}
	if !bytes.Equal(current, expected) {
		return errs.Config("conflicting or unowned recovery ignore %q; refusing to overwrite it or retain configuration", path)
	}
	return nil
}

// Probe only inside the owned recovery directory, before moving azure.yaml.
// Success does not guarantee subsequent cross-directory operations will succeed.
func checkProjectionHardLinks(directory string) (err error) {
	source, err := os.CreateTemp(directory, ".hardlink-check-")
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, os.Remove(source.Name()))
	}()
	if err := source.Close(); err != nil {
		return err
	}
	link := source.Name() + ".link"
	if err := os.Link(source.Name(), link); err != nil {
		return fmt.Errorf("azd projection requires hard-link support in its recovery filesystem: %w", err)
	}
	return os.Remove(link)
}

func validatedProjectionSource(workspace Workspace) ([]byte, os.FileMode, error) {
	if workspace.AzureYAML != filepath.Join(workspace.Root, AzureYAMLFile) {
		return nil, 0, errs.Config("Hosted projection requires azure.yaml in the validated workspace root")
	}
	original, found := workspace.configurationFiles[AzureYAMLFile]
	if !found {
		return nil, 0, errs.Config("Hosted projection requires a freshly loaded workspace with exact configuration inputs")
	}
	for _, name := range sortedFileNames(workspace.configurationFiles) {
		current, err := netcheck.ReadContainedFile(workspace.Root, filepath.FromSlash(name), "Hosted projection source")
		if err != nil {
			return nil, 0, err
		}
		if !bytes.Equal(current, workspace.configurationFiles[name]) {
			return nil, 0, errs.Config("%s changed since validation; reload the workspace before azd projection", name)
		}
	}
	info, err := os.Lstat(workspace.AzureYAML)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, errs.Security("azure.yaml must remain a regular file during azd projection")
	}
	return original, info.Mode().Perm(), nil
}

func replaceProjection(path, directory, phase string, expected, replacement []byte, mode os.FileMode, ops projectionIO) error {
	if err := checkProjectionBytes(path, expected); err != nil {
		return err
	}
	step := filepath.Join(directory, phase)
	// Never reuse a recovery slot or overwrite any existing recovery file.
	if err := os.Mkdir(step, 0o700); err != nil {
		return err
	}
	staged := filepath.Join(step, "replacement.yaml")
	if err := writeProjectionFile(staged, replacement, mode); err != nil {
		return err
	}
	if err := errors.Join(ops.syncDirectory(step), ops.syncDirectory(directory)); err != nil {
		return err
	}
	displaced := filepath.Join(step, "displaced.yaml")
	if err := ops.move(path, displaced); err != nil {
		return err
	}
	if err := errors.Join(ops.syncDirectory(step), ops.syncDirectory(filepath.Dir(path))); err != nil {
		return err
	}
	// Check AFTER displacement, not just before a destructive rename. A file
	// changed in that interval is retained and re-published only if path is absent.
	if err := checkProjectionBytes(displaced, expected); err != nil {
		return errors.Join(err, ops.publish(displaced, path), ops.syncDirectory(filepath.Dir(path)))
	}
	if err := ops.publish(staged, path); err != nil {
		return fmt.Errorf("refusing to overwrite concurrent edits at %q; displaced file retained: %w", path, err)
	}
	return errors.Join(
		ops.syncDirectory(filepath.Dir(path)),
		checkProjectionBytes(displaced, expected),
		checkProjectionBytes(path, replacement),
	)
}

func checkProjectionBytes(path string, expected []byte) error {
	current, err := readProjectionFile(path, int64(len(expected)))
	if err != nil {
		return err
	}
	if !bytes.Equal(current, expected) {
		return errs.Config("azure.yaml changed during the azd operation; refusing to overwrite concurrent edits (retained file %q)", path)
	}
	return nil
}

func readProjectionFile(path string, expectedSize int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errs.Security("projection file %q is no longer a regular file", path)
	}
	// Rooted opening prevents a link swap from escaping the containing directory.
	// Bound the stream as well as the opened size: the file may grow after Stat.
	// Use the expected document size, not a new fixed cap on expanded YAML.
	file, opened, err := netcheck.OpenContainedFile(filepath.Dir(path), filepath.Base(path), "Hosted projection file", expectedSize+1)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errors.Join(errs.Security("projection file %q changed before its contained read", path), file.Close())
	}
	current, readErr := io.ReadAll(io.LimitReader(file, expectedSize+1))
	return current, errors.Join(readErr, file.Close())
}

func writeProjectionFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return errors.Join(err, file.Close())
	}
	_, writeErr := file.Write(data)
	return errors.Join(writeErr, file.Sync(), file.Close())
}
