//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

const supportsProcessFileLock = false
const supportsDirectorySync = false

type fallbackFileLock struct {
	directoryInfo os.FileInfo
	directoryPath string
	name          string
	lock          *sync.Mutex
}

var (
	fileLocksMu sync.Mutex
	fileLocks   []fallbackFileLock
)

func withFileLock(path string, action func() error) error {
	lock, err := fallbackLockFor(path)
	if err != nil {
		return err
	}
	lock.Lock()
	defer lock.Unlock()
	return action()
}

func fallbackLockFor(path string) (*sync.Mutex, error) {
	directory := filepath.Dir(path)
	info, err := os.Stat(directory)
	if err != nil {
		return nil, fmt.Errorf("stat OSCORE store lock directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve OSCORE store lock directory: %w", err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, fmt.Errorf("make OSCORE store lock directory absolute: %w", err)
	}
	name := filepath.Base(path)
	if runtime.GOOS == "windows" {
		resolved = strings.ToLower(resolved)
		name = strings.ToLower(name)
	}

	fileLocksMu.Lock()
	defer fileLocksMu.Unlock()
	for _, entry := range fileLocks {
		if entry.name == name &&
			(os.SameFile(entry.directoryInfo, info) || entry.directoryPath == resolved) {
			return entry.lock, nil
		}
	}
	lock := new(sync.Mutex)
	fileLocks = append(fileLocks, fallbackFileLock{
		directoryInfo: info,
		directoryPath: resolved,
		name:          name,
		lock:          lock,
	})
	return lock, nil
}

func syncDirectory(string) error {
	return nil
}
