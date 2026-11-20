//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"fmt"
	"os"
	"syscall"
)

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
