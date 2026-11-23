//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validateStoreDirectoryPath(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("make OSCORE store directory absolute: %w", err)
	}
	current := string(filepath.Separator)
	rest := strings.TrimPrefix(absolute, current)
	for _, component := range strings.Split(rest, current) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		// #nosec G703 -- current is a cleaned prefix of the caller-selected store root.
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lstat OSCORE store directory component %q: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("OSCORE store directory component %q is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("OSCORE store path component %q is not a directory", current)
		}
	}
	return nil
}

func validateStoreDirectory(path string) error {
	// #nosec G703 -- path is the caller-selected persistent-store root.
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cannot determine owner of %q", path)
	}
	// #nosec G115 -- Unix file owners and effective user IDs are both uint32.
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("directory %q is not owned by the current user", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("directory %q is group- or world-writable", path)
	}
	return nil
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
