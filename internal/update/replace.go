package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	errs "foundry-agent-manager/internal/errors"
)

type fileIdentity struct {
	info os.FileInfo
	hash [32]byte
}

func (f fileIdentity) equal(other fileIdentity) bool {
	return os.SameFile(f.info, other.info) && f.info.Size() == other.info.Size() &&
		f.info.ModTime().Equal(other.info.ModTime()) && f.info.Mode() == other.info.Mode() &&
		f.hash == other.hash
}

func identify(ctx context.Context, path string, limit int64) (fileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, fileError("inspect the installed executable", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return fileIdentity{}, errs.Security("installed executable is not a nonempty bounded regular file")
	}
	file, err := openImage(path)
	if err != nil {
		return fileIdentity{}, fileError("read the installed executable", err)
	}
	digest := sha256.New()
	count, readErr := io.Copy(digest, io.LimitReader(contextReader{ctx, file}, limit+1))
	opened, statErr := file.Stat()
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, closeErr); err != nil {
		return fileIdentity{}, fileError("capture the installed executable identity", err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return fileIdentity{}, fileError("recheck the installed executable", err)
	}
	if !os.SameFile(info, opened) || !os.SameFile(info, after) || !after.Mode().IsRegular() ||
		count != info.Size() || count > limit || info.Size() != after.Size() ||
		!info.ModTime().Equal(after.ModTime()) || info.Mode() != after.Mode() {
		return fileIdentity{}, errs.Conflict("installed executable changed while its identity was captured")
	}
	var hash [32]byte
	copy(hash[:], digest.Sum(nil))
	return fileIdentity{info: after, hash: hash}, nil
}

type commitOps struct {
	windows bool
	move    func(string, string) error
	replace func(string, string) error
	link    func(string, string) error
	syncDir func(string) error
}

type commitResult struct {
	changed bool
	backup  bool
}

// commit is deliberately context-free: cancellation cannot interrupt recovery
// after the first mutation. The caller owns the stable installation lock.
func commit(target, stage, backup string, ops commitOps) (commitResult, error) {
	if ops.windows {
		if err := ops.move(target, backup); err != nil {
			return commitResult{}, fileError("move the running executable to its recovery backup", err)
		}
		if err := ops.move(stage, target); err != nil {
			if rollbackErr := ops.move(backup, target); rollbackErr != nil {
				return commitResult{backup: true}, errs.WithNextSteps(
					errs.Conflict("update replacement and rollback failed; original preserved at %s", backup),
					"Exit FAM and inspect the target and recovery.json before restoring the original executable.",
					"Do not overwrite an unexpected target; preserve the backup until recovery is complete.")
			}
			return commitResult{}, fileError("replace the executable (original restored)", err)
		}
		return commitResult{changed: true, backup: true}, nil
	}
	if err := ops.link(target, backup); err != nil {
		return commitResult{}, fileError("create a recoverable original executable", err)
	}
	if err := ops.syncDir(filepath.Dir(backup)); err != nil {
		return commitResult{backup: true}, fileError("persist the original executable backup", err)
	}
	if err := ops.replace(stage, target); err != nil {
		return commitResult{backup: true}, fileError("atomically replace the executable", err)
	}
	if err := ops.syncDir(filepath.Dir(target)); err != nil {
		return commitResult{changed: true, backup: true}, fileError("persist the executable replacement; backup retained", err)
	}
	return commitResult{changed: true, backup: true}, nil
}

func (u *Updater) install(ctx context.Context, plan *Plan, executable []byte) (result Result, err error) {
	result = plan.result
	lock, err := acquireLock(ctx, u.target+".update.lock")
	if err != nil {
		return result, err
	}
	defer func() {
		if closeErr := releaseLock(lock); closeErr != nil {
			err = errors.Join(err, fileError("release the update lock", closeErr))
		}
	}()
	if err := u.recheck(ctx, plan.identity); err != nil {
		return result, err
	}
	directory, err := os.MkdirTemp(filepath.Dir(u.target), ".fam-update-")
	if err != nil {
		return result, fileError("create private staging beside the executable", err)
	}
	stage := filepath.Join(directory, "replacement")
	backup := filepath.Join(directory, "original")
	marker := filepath.Join(directory, "recovery.json")
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, cleanStage(directory, stage, marker))
		}
	}()
	mode := (plan.identity.info.Mode().Perm() & 0755) | 0500
	if err := writeSynced(stage, executable, mode); err != nil {
		return result, err
	}
	record, err := json.Marshal(struct {
		Executable string `json:"executable"`
		Backup     string `json:"backup"`
		Staged     string `json:"staged"`
		Version    string `json:"version"`
	}{u.target, backup, stage, plan.result.TargetVersion})
	if err != nil {
		return result, fileError("encode the recovery marker", err)
	}
	if err := writeSynced(marker, record, 0600); err != nil {
		return result, err
	}
	if err := u.ops.syncDir(directory); err != nil {
		return result, fileError("persist the recovery directory", err)
	}
	if err := u.ops.syncDir(filepath.Dir(directory)); err != nil {
		return result, fileError("persist the staging directory entry", err)
	}
	if err := u.recheck(ctx, plan.identity); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	outcome, err := commit(u.target, stage, backup, u.ops)
	if outcome.changed {
		result.Status, result.Changed = "updated", true
	}
	if outcome.backup {
		keep = true
		result.BackupPath = backup
	}
	if err != nil {
		return result, err
	}
	if err := os.Remove(backup); err != nil {
		// Windows may keep the running image locked until this process exits.
		// BackupPath makes retained recovery state explicit to the caller.
		if errors.Is(err, os.ErrNotExist) {
			result.BackupPath = ""
			return result, errs.Conflict("recovery backup disappeared during update cleanup")
		}
		return result, nil
	}
	result.BackupPath, keep = "", false
	return result, nil
}

func writeSynced(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fileError("create an exclusive staging file", err)
	}
	_, writeErr := file.Write(data)
	modeErr := file.Chmod(mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, modeErr, syncErr, closeErr); err != nil {
		return fileError("write and persist a staging file", err)
	}
	return nil
}

func cleanStage(directory, stage, marker string) error {
	var failures []error
	for _, path := range []string{stage, marker, directory} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, fileError("clean the update staging path", err))
		}
	}
	return errors.Join(failures...)
}

// The lock file is permanent. Deleting it would let waiters and a new updater
// acquire locks on different inodes. Kernel locks are released on process exit.
func acquireLock(ctx context.Context, path string) (*os.File, error) {
	file, err := openLock(ctx, path)
	if err != nil {
		return nil, fileError("open the installation update lock", err)
	}
	fail := func(err error) (*os.File, error) {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fileError("close the installation update lock", closeErr))
		}
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return fail(fileError("inspect the update lock", err))
	}
	named, err := os.Lstat(path)
	if err != nil {
		return fail(fileError("inspect the update lock path", err))
	}
	if !named.Mode().IsRegular() || !os.SameFile(opened, named) {
		return fail(errs.Security("installation update lock is not a stable regular file"))
	}
	for {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		locked, err := tryLock(file)
		if err != nil {
			return fail(fileError("lock the installation for update", err))
		}
		if locked {
			return file, nil
		}
		if err := wait(ctx, 25*time.Millisecond); err != nil {
			return fail(err)
		}
	}
}

func openLock(ctx context.Context, path string) (*os.File, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
			if errors.Is(createErr, os.ErrExist) {
				continue
			}
			return file, createErr
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errs.Security("installation update lock must not be a link or special file")
		}
		return os.OpenFile(path, os.O_RDWR, 0600)
	}
}

func releaseLock(file *os.File) error {
	return errors.Join(unlock(file), file.Close())
}
