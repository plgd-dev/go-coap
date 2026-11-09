//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd

package store

import (
	"sync"
)

var fileLocks sync.Map

func withFileLock(path string, action func() error) error {
	value, _ := fileLocks.LoadOrStore(path, new(sync.Mutex))
	lock := value.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	return action()
}

func syncDirectory(string) error {
	return nil
}
