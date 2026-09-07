//go:build !windows && !linux && !darwin

package update

import (
	"os"

	errs "foundry-agent-manager/internal/errors"
)

func nativeCommitOps() commitOps {
	return commitOps{}
}

func openImage(path string) (*os.File, error) {
	return os.Open(path)
}

func tryLock(*os.File) (bool, error) {
	return false, errs.Config("self-update is unsupported on this operating system")
}

func unlock(*os.File) error {
	return errs.Config("self-update is unsupported on this operating system")
}
