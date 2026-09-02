//go:build !windows

package hosted

import (
	"os"
	"path/filepath"
)

func replaceAtomicFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncAtomicDirectory(path string) error {
	directory, err := os.Open(filepath.Clean(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
