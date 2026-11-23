//go:build windows

package store

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const (
	supportsProcessFileLock = true
	supportsDirectorySync   = false
)

func withFileLock(path string, action func() error) (err error) {
	file, err := openFileNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open OSCORE store lock: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close OSCORE store lock: %w", closeErr)
		}
	}()
	var overlapped windows.Overlapped
	handle := windows.Handle(file.Fd())
	if lockErr := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlapped); lockErr != nil {
		return fmt.Errorf("lock OSCORE store: %w", lockErr)
	}
	defer func() {
		if unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, &overlapped); err == nil && unlockErr != nil {
			err = fmt.Errorf("unlock OSCORE store: %w", unlockErr)
		}
	}()
	return action()
}

func syncDirectory(string) error {
	return nil
}
