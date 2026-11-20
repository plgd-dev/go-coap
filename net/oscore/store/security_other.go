//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package store

import (
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

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}
