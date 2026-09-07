//go:build linux || darwin

package update

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func nativeCommitOps() commitOps {
	return commitOps{replace: os.Rename, link: os.Link, syncDir: syncDirectory}
}

func openImage(path string) (*os.File, error) {
	return os.Open(path)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	// Some filesystems do not implement directory fsync. File contents and the
	// recoverable hard link still precede the atomic rename.
	if errors.Is(syncErr, unix.EINVAL) || errors.Is(syncErr, unix.ENOTSUP) {
		syncErr = nil
	}
	return errors.Join(syncErr, closeErr)
}

func tryLock(file *os.File) (bool, error) {
	err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return false, nil
	}
	return err == nil, err
}

func unlock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
