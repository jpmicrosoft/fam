package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	errs "foundry-agent-manager/internal/errors"
)

func testMoveNoReplace(source, destination string) error {
	if runtime.GOOS == "windows" {
		return nativeCommitOps().move(source, destination)
	}
	if err := os.Link(source, destination); err != nil {
		return err
	}
	return os.Remove(source)
}

func TestWindowsProtocolPersistsRecoveryThenRollsBackDespiteCancellation(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("replacement fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := mustRead(t, u.target)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	moves := 0
	syncDir := u.ops.syncDir
	u.ops = commitOps{
		windows: true, syncDir: syncDir,
		move: func(source, destination string) error {
			moves++
			if moves == 1 {
				marker := mustRead(t, filepath.Join(filepath.Dir(destination), "recovery.json"))
				var record map[string]string
				if err := json.Unmarshal(marker, &record); err != nil {
					t.Fatal(err)
				}
				if record["executable"] != u.target || record["backup"] != destination ||
					record["version"] != "1.1.0" || record["staged"] == "" {
					t.Fatalf("recovery marker did not precede mutation: %v", record)
				}
				if err := testMoveNoReplace(source, destination); err != nil {
					return err
				}
				cancel()
				return nil
			}
			if moves == 2 {
				return errors.New("synthetic stage move failure")
			}
			return testMoveNoReplace(source, destination)
		},
	}
	result, err := u.Apply(ctx, plan)
	if err == nil || result.Changed || result.BackupPath != "" || moves != 3 {
		t.Fatalf("rollback outcome: result=%+v moves=%d err=%v", result, moves, err)
	}
	if !bytes.Equal(original, mustRead(t, u.target)) {
		t.Fatal("rollback did not restore the original")
	}
	if names := directoryNames(t, filepath.Dir(u.target)); len(names) != 2 {
		t.Fatalf("successful rollback retained staging: %v", names)
	}
}

func TestWindowsProtocolNeverOverwritesUnexpectedTargetDuringRollback(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("replacement fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := mustRead(t, u.target)
	unexpected := []byte("another writer's target")
	moves := 0
	u.ops = commitOps{
		windows: true, syncDir: u.ops.syncDir,
		move: func(source, destination string) error {
			moves++
			if moves == 2 {
				mustWrite(t, u.target, unexpected, 0755)
				return errors.New("synthetic collision")
			}
			return testMoveNoReplace(source, destination)
		},
	}
	result, err := u.Apply(context.Background(), plan)
	if !errs.IsKind(err, "conflict") || result.Changed || result.BackupPath == "" || moves != 3 {
		t.Fatalf("failed rollback lost recovery state: %+v %v moves=%d", result, err, moves)
	}
	if !bytes.Equal(unexpected, mustRead(t, u.target)) ||
		!bytes.Equal(original, mustRead(t, result.BackupPath)) {
		t.Fatal("rollback overwrote a collision or lost the original backup")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(result.BackupPath), "recovery.json")); err != nil {
		t.Fatalf("recovery marker was removed: %v", err)
	}
}

func TestReplacementPreparationFailureNeverMutatesOriginal(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("replacement fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := mustRead(t, u.target)
	u.ops.syncDir = func(string) error { return os.ErrPermission }
	result, err := u.Apply(context.Background(), plan)
	if err == nil || result.Changed || result.BackupPath != "" ||
		!bytes.Equal(original, mustRead(t, u.target)) {
		t.Fatalf("preparation failure mutated target: %+v %v", result, err)
	}
	if len(errs.Remediation(err)) == 0 {
		t.Fatal("permission failure lacks actionable remediation")
	}
}

func TestPOSIXRenameFailureKeepsRecoverableOriginal(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("replacement fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	original := mustRead(t, u.target)
	u.ops = commitOps{
		link: os.Link, syncDir: u.ops.syncDir,
		replace: func(string, string) error { return errors.New("synthetic rename failure") },
	}
	result, err := u.Apply(context.Background(), plan)
	if err == nil || result.Changed || result.BackupPath == "" {
		t.Fatalf("rename failure lost original backup: %+v %v", result, err)
	}
	if !bytes.Equal(original, mustRead(t, u.target)) ||
		!bytes.Equal(original, mustRead(t, result.BackupPath)) {
		t.Fatal("failed atomic replacement changed original bytes")
	}
}

func TestTargetIdentityIsRecheckedUnderLockImmediatelyBeforeCommit(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	attachFixture(t, u, "1.1.0", []byte("replacement fixture"))
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	newer := []byte("newer target installed during preparation")
	syncDir := u.ops.syncDir
	changed := false
	u.ops.syncDir = func(path string) error {
		if !changed {
			changed = true
			mustWrite(t, u.target, newer, 0755)
		}
		return syncDir(path)
	}
	result, err := u.Apply(context.Background(), plan)
	if !errs.IsKind(err, "conflict") || result.Changed ||
		!bytes.Equal(newer, mustRead(t, u.target)) {
		t.Fatalf("precommit identity check missed replacement: %+v %v", result, err)
	}
}

func TestKernelLockIsExclusivePermanentAndContextAware(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fam.update.lock")
	first, err := acquireLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	firstInfo, err := first.Stat()
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	locked, lockErr := tryLock(second)
	closeErr := second.Close()
	if lockErr != nil || closeErr != nil || locked {
		t.Fatalf("lock is not exclusive: locked=%v err=%v close=%v", locked, lockErr, closeErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := acquireLock(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("lock wait ignored cancellation: %v", err)
	}
	if err := releaseLock(first); err != nil {
		t.Fatal(err)
	}
	next, err := acquireLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	nextInfo, err := next.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseLock(next); err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(firstInfo, nextInfo) {
		t.Fatal("lock file was removed or replaced between acquisitions")
	}
}

func TestLockRejectsSymlinksWithoutCreatingTheirTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "must-not-exist")
	lock := filepath.Join(dir, "fam.update.lock")
	if err := os.Symlink(target, lock); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("this Windows account cannot create symlinks")
		}
		t.Fatal(err)
	}
	if _, err := acquireLock(context.Background(), lock); !errs.IsKind(err, "security") {
		t.Fatalf("lock symlink accepted: %v", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("opening the lock created the symlink destination")
	}
}

func TestResolvedSymlinkUpdatesTheRealExecutableOnly(t *testing.T) {
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	link := filepath.Join(filepath.Dir(u.target), "alias")
	if err := os.Symlink(u.target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("this Windows account cannot create symlinks")
		}
		t.Fatal(err)
	}
	viaLink := updaterAt(t, link, "1.0.0", "", runtime.GOOS)
	payload := []byte("verified symlink target replacement")
	attachFixture(t, viaLink, "1.1.0", payload)
	plan, err := viaLink.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := viaLink.Apply(context.Background(), plan)
	if err != nil || result.Executable != u.target || !bytes.Equal(payload, mustRead(t, u.target)) {
		t.Fatalf("resolved update failed: %+v %v", result, err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("alias link was replaced: %v", err)
	}
}

func TestReplacementModeComesFromInstallationNotArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not Windows execution permissions")
	}
	u := testUpdater(t, "1.0.0", "", runtime.GOOS)
	if err := os.Chmod(u.target, 0750); err != nil {
		t.Fatal(err)
	}
	u = updaterAt(t, u.target, "1.0.0", "", runtime.GOOS)
	entries := standardEntries(runtime.GOOS, []byte("replacement fixture"))
	entries[0].mode = os.ModeSetuid | os.ModeSetgid | 0777
	f := attachFixture(t, u, "1.1.0", entries[0].data)
	f.archive = makeArchive(t, runtime.GOOS, entries)
	f.checksum = checksumFixture(f.archive, f.release.Assets[0].Name)
	plan, err := u.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(u.target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0750 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("unsafe installed mode: %v", info.Mode())
	}
}
