//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package store

import (
	"errors"
	"fmt"
	"os"
)

func validateStoreDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	return nil
}

func validateStoreDirectoryPath(_ string, allowedSymlinks []string) error {
	if len(allowedSymlinks) > 0 {
		return errors.New("allowed OSCORE store symlinks are unsupported on this platform")
	}
	return nil
}

type otherStoreDirectoryGuard struct{}

func newStoreDirectoryGuard(string) (storeDirectoryGuard, error) {
	return otherStoreDirectoryGuard{}, nil
}

func (otherStoreDirectoryGuard) withOperation(action func() error) error {
	return action()
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}

func replaceFileAtomically(source, destination string) error {
	return os.Rename(source, destination)
}
