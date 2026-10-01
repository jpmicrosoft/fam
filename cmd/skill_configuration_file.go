package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	errs "foundry-agent-manager/internal/errors"
	"foundry-agent-manager/internal/netcheck"
)

func replaceSkillConfiguration(path string, original, data []byte) error {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return errs.Config("failed to open configuration directory: %v", err)
	}
	defer root.Close()
	name := filepath.Base(path)
	lockName := "." + name + ".fam-edit-lock"
	lock, err := root.OpenFile(lockName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return errs.Conflict("cannot lock %s for editing; another edit may be active: %v", path, err)
	}
	if err := lock.Close(); err != nil {
		_ = root.Remove(lockName)
		return errs.Config("failed to close configuration edit lock: %v", err)
	}
	defer root.Remove(lockName)

	mode := os.FileMode(0o600)
	check := func() error {
		info, err := root.Lstat(name)
		if os.IsNotExist(err) && original == nil {
			return nil
		}
		if err != nil {
			return errs.Conflict("configuration changed before saving %s: %v", path, err)
		}
		if !info.Mode().IsRegular() || original == nil {
			return errs.Conflict("configuration changed before saving %s", path)
		}
		file, err := root.Open(name)
		if err != nil {
			return errs.Config("failed to reopen configuration: %v", err)
		}
		current, readErr := io.ReadAll(io.LimitReader(file, netcheck.MaxContainedFileBytes+1))
		closeErr := file.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return errs.Config("failed to reread configuration: %v", err)
		}
		if !bytes.Equal(current, original) {
			return errs.Conflict("configuration changed while editing %s; retry with the updated file", path)
		}
		mode = info.Mode().Perm()
		return nil
	}
	if err := check(); err != nil {
		return err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errs.Config("failed to generate temporary file name: %v", err)
	}
	tempName := "." + name + ".fam-" + hex.EncodeToString(random[:])
	file, err := root.OpenFile(tempName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errs.Config("failed to create configuration replacement: %v", err)
	}
	defer root.Remove(tempName)
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return errs.Config("failed to save configuration replacement: %v", err)
	}
	if err := check(); err != nil {
		return err
	}
	if err := root.Rename(tempName, name); err != nil {
		return errs.Config("failed to replace Skill configuration %s: %v", path, err)
	}
	return nil
}
