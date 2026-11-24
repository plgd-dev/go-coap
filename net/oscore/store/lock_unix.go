//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"fmt"
	"os"
	"syscall"
)

const (
	supportsProcessFileLock = true
	supportsDirectorySync   = true
)

func withFileLock(path string, action func() error) (err error) {
	file, err := openFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open OSCORE store lock: %w", err)
	}
	return withLockedFile(file, action)
}

func withFileLockAt(root *os.Root, path string, action func() error) (err error) {
	if root == nil {
		return withFileLock(path, action)
	}
	file, err := openFileAtRoot(root, path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open rooted OSCORE store lock: %w", err)
	}
	return withLockedFile(file, action)
}

func withLockedFile(file *os.File, action func() error) (err error) {
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close OSCORE store lock: %w", closeErr)
		}
	}()
	if chmodErr := file.Chmod(0o600); chmodErr != nil {
		return fmt.Errorf("secure OSCORE store lock: %w", chmodErr)
	}
	if lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); lockErr != nil {
		return fmt.Errorf("lock OSCORE store: %w", lockErr)
	}
	defer func() {
		if unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN); err == nil && unlockErr != nil {
			err = fmt.Errorf("unlock OSCORE store: %w", unlockErr)
		}
	}()
	return action()
}

func syncDirectory(path string) error {
	// #nosec G703 -- path is the configured store directory used for durability sync.
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open OSCORE store directory for sync: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync OSCORE store directory: %w", err)
	}
	return nil
}
