//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !windows

package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFallbackFileLockUsesDirectoryIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sender.sequence.lock")
	first, err := fallbackLockFor(path)
	if err != nil {
		t.Fatal(err)
	}

	alias := filepath.Join(dir, ".", "sender.sequence.lock")
	if runtime.GOOS == "windows" {
		alias = strings.ToUpper(alias)
	}
	second, err := fallbackLockFor(alias)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("path aliases to the same lock file received different mutexes")
	}
}

func TestHighWaterFileStoreRequiresExplicitSingleProcessMode(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewHighWaterFileStore(dir, "requires-process-lock"); err == nil {
		t.Fatal("NewHighWaterFileStore accepted a platform without process locking")
	}
	if _, err := NewHighWaterFileStore(dir, "single-process", WithSingleProcessOnly()); err != nil {
		t.Fatalf("NewHighWaterFileStore rejected explicit single-process mode: %v", err)
	}
}

func TestFallbackFileLockResolvesDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "store-alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	first, err := fallbackLockFor(filepath.Join(dir, "sender.sequence.lock"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := fallbackLockFor(filepath.Join(alias, "sender.sequence.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("symlink aliases to the same store received different mutexes")
	}
}
