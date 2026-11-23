//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

func validateStoreDirectoryPath(path string, allowedSymlinks []string) error {
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
			if isAllowedStoreSymlink(current, allowedSymlinks) {
				if err := validateStoreSymlinkTarget(current); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("OSCORE store directory component %q is a symlink", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("OSCORE store path component %q is not a directory", current)
		}
	}
	return nil
}

func isAllowedStoreSymlink(path string, allowedSymlinks []string) bool {
	cleaned := filepath.Clean(path)
	for _, allowed := range allowedSymlinks {
		if cleaned == allowed {
			return true
		}
	}
	return isAllowedUnixSystemSymlink(cleaned)
}

func isAllowedUnixSystemSymlink(path string) bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	switch filepath.Clean(path) {
	case "/var", "/tmp":
		return true
	default:
		return false
	}
}

func validateStoreSymlinkTarget(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve allowed OSCORE store symlink %q: %w", path, err)
	}
	// #nosec G703 -- EvalSymlinks resolves the caller-approved symlink before stat.
	info, err := os.Stat(resolved)
	if err != nil {
		return fmt.Errorf("stat allowed OSCORE store symlink target %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("allowed OSCORE store symlink target %q is not a directory", resolved)
	}
	current := string(filepath.Separator)
	for _, component := range strings.Split(strings.TrimPrefix(resolved, current), current) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		// #nosec G703 -- current is a cleaned ancestor of the resolved target.
		if err := validateTrustedStoreDirectory(current); err != nil {
			return fmt.Errorf("validate allowed OSCORE store symlink ancestor %q: %w", current, err)
		}
	}
	return nil
}

func validateTrustedStoreDirectory(path string) error {
	// #nosec G703 -- path is a canonical ancestor resolved from an approved symlink.
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
	if stat.Uid != uint32(os.Geteuid()) && stat.Uid != 0 {
		return fmt.Errorf("directory %q is not owned by the current user or root", path)
	}
	if info.Mode().Perm()&0o022 != 0 &&
		(info.Mode()&os.ModeSticky == 0 || filepath.Clean(path) != "/tmp") {
		return fmt.Errorf("directory %q is group- or world-writable", path)
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

type unixStoreDirectoryGuard struct {
	path string
	root *os.Root
}

func newStoreDirectoryGuard(path string) (storeDirectoryGuard, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open rooted OSCORE store directory %q: %w", path, err)
	}
	return &unixStoreDirectoryGuard{path: path, root: root}, nil
}

func (g *unixStoreDirectoryGuard) withOperation(action func() error) error {
	rootInfo, err := g.root.Stat(".")
	if err != nil {
		return fmt.Errorf("stat rooted OSCORE store directory: %w", err)
	}
	pathInfo, err := os.Stat(g.path)
	if err != nil {
		return fmt.Errorf("stat configured OSCORE store directory: %w", err)
	}
	if !os.SameFile(rootInfo, pathInfo) {
		return errors.New("configured OSCORE store directory no longer matches its rooted directory")
	}
	return action()
}

func (g *unixStoreDirectoryGuard) fileRoot() *os.Root {
	return g.root
}

func (g *unixStoreDirectoryGuard) Close() error {
	return g.root.Close()
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW, uint32(perm.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openFileAtRoot(root *os.Root, path string, flags int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(path, flags|syscall.O_NOFOLLOW, perm)
}

func replaceFileAtomically(source, destination string) error {
	// #nosec G703 -- both paths are generated inside the validated store directory.
	return os.Rename(source, destination)
}
