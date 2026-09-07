package update

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const helperEnvironment = "FAM_UPDATE_NATIVE_TEST_HELPER"

var nativePayload = []byte("offline replacement fixture; deliberately not a runnable program")

func nativeHelper(t *testing.T) string {
	t.Helper()
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "fam-update-native-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	mustWrite(t, path, mustRead(t, current), 0755)
	t.Cleanup(func() { removeNativeImage(t, path) })
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func removeNativeImage(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := os.Remove(path)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		// Windows image scanners can retain a delete-blocking handle briefly
		// after Wait has observed the helper's process exit.
		if runtime.GOOS != "windows" || !errors.Is(err, os.ErrPermission) || time.Now().After(deadline) {
			t.Errorf("remove exited native helper image: %v", err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func validateNativeHelper(t *testing.T) string {
	t.Helper()
	expected := os.Getenv(helperEnvironment)
	current, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "fam-update-native-helper"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if expected == "" || current != expected || filepath.Base(current) != name {
		t.Fatal("native update test must run only from its disposable helper copy")
	}
	return current
}

// The helper is a copy of the already-built native test executable, not a
// downloaded program. It replaces only itself with inert synthetic bytes.
func TestRunningExecutableReplacement(t *testing.T) {
	if os.Getenv(helperEnvironment) != "" {
		target := validateNativeHelper(t)
		u, err := New(Options{CurrentVersion: "1.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		if u.target != target {
			t.Fatal("updater resolved outside the disposable helper")
		}
		attachFixture(t, u, "1.1.0", nativePayload)
		plan, err := u.Check(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := u.Apply(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != "updated" || !result.Changed || !bytes.Equal(nativePayload, mustRead(t, target)) {
			t.Fatalf("running image replacement failed: %+v", result)
		}
		if result.BackupPath != "" {
			if _, err := os.Stat(result.BackupPath); err != nil {
				t.Fatalf("reported backup is not present: %v", err)
			}
			if filepath.Dir(filepath.Dir(result.BackupPath)) != filepath.Dir(target) {
				t.Fatal("backup escaped the disposable installation")
			}
		}
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(filepath.Dir(target), "native-result.json"), data, 0600)
		return
	}
	helper := nativeHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "-test.run=^TestRunningExecutableReplacement$", "-test.count=1")
	command.Env = append(os.Environ(), helperEnvironment+"="+helper)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("disposable running-image helper failed: %v\n%s", err, output)
	}
	var result Result
	if err := json.Unmarshal(mustRead(t, filepath.Join(filepath.Dir(helper), "native-result.json")), &result); err != nil {
		t.Fatal(err)
	}
	if result.BackupPath != "" {
		t.Cleanup(func() { removeNativeImage(t, result.BackupPath) })
	}
	if result.Executable != helper || result.Status != "updated" || !result.Changed ||
		!bytes.Equal(nativePayload, mustRead(t, helper)) {
		t.Fatalf("native helper did not persist the replacement: %+v", result)
	}
	// Do not invoke helper again: its path now contains the inert fixture.
}

func TestCrossProcessKernelLockIsReleasedOnExit(t *testing.T) {
	if os.Getenv(helperEnvironment) != "" {
		helper := validateNativeHelper(t)
		lock, err := acquireLock(context.Background(), filepath.Join(filepath.Dir(helper), "native.update.lock"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
			t.Fatal(err)
		}
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		runtime.KeepAlive(lock)
		// Simulate termination without an explicit unlock or lock-file removal.
		os.Exit(0)
	}
	helper := nativeHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, helper, "-test.run=^TestCrossProcessKernelLockIsReleasedOnExit$", "-test.count=1")
	command.Env = append(os.Environ(), helperEnvironment+"="+helper)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			cancel()
			if err := command.Wait(); err != nil && ctx.Err() == nil {
				t.Errorf("helper cleanup: %v", err)
			}
		}
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("lock helper did not become ready: %q %v", line, err)
	}
	path := filepath.Join(filepath.Dir(helper), "native.update.lock")
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	before, statErr := file.Stat()
	locked, lockErr := tryLock(file)
	closeErr := file.Close()
	if statErr != nil || lockErr != nil || closeErr != nil || locked {
		t.Fatalf("cross-process lock failed: locked=%v stat=%v lock=%v close=%v", locked, statErr, lockErr, closeErr)
	}
	if _, err := fmt.Fprintln(stdin, "exit"); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	err = command.Wait()
	waited = true
	if err != nil {
		t.Fatalf("lock helper exit: %v %s", err, &stderr)
	}
	next, err := acquireLock(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	after, statErr := next.Stat()
	if err := releaseLock(next); err != nil {
		t.Fatal(err)
	}
	if statErr != nil || !os.SameFile(before, after) {
		t.Fatalf("process exit replaced or removed the permanent lock: %v", statErr)
	}
}
