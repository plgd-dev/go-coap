//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewHighWaterFileStoreRejectsWritableDirectory(t *testing.T) {
	dir := privateTempDir(t)
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHighWaterFileStore(dir, "writable-directory"); err == nil {
		t.Fatal("NewHighWaterFileStore accepted a group/world-writable directory")
	}
}

func TestNewHighWaterFileStoreRejectsSymlinkDirectoryComponent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	storePath := filepath.Join(link, "nested")
	if _, err := NewHighWaterFileStore(storePath, "symlink-component"); err == nil {
		t.Fatal("NewHighWaterFileStore accepted a directory path containing a symlink")
	}
	if _, err := os.Lstat(filepath.Join(target, "nested")); !os.IsNotExist(err) {
		t.Fatalf("symlink target child status = %v, want not exist", err)
	}
}

func TestHighWaterFileStoreRejectsSymlinkLock(t *testing.T) {
	dir := privateTempDir(t)
	store, storeErr := NewHighWaterFileStore(dir, "symlink-lock")
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	target := filepath.Join(dir, "lock-target")
	if writeErr := os.WriteFile(target, []byte("unchanged"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if linkErr := os.Symlink(target, store.sequenceLock); linkErr != nil {
		t.Fatal(linkErr)
	}
	if _, reserveErr := store.Reserve(); reserveErr == nil {
		t.Fatal("Reserve followed a symlink lock")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "unchanged" {
		t.Fatalf("lock target contents = %q, want unchanged", got)
	}
}

func TestHighWaterFileStoreRejectsSymlinkStateFile(t *testing.T) {
	dir := privateTempDir(t)
	store, storeErr := NewHighWaterFileStore(dir, "symlink-state")
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	target := filepath.Join(dir, "state-target")
	if writeErr := os.WriteFile(target, []byte(`{"version":1,"next":0}`), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if linkErr := os.Symlink(target, store.sequencePath); linkErr != nil {
		t.Fatal(linkErr)
	}
	if _, reserveErr := store.Reserve(); reserveErr == nil {
		t.Fatal("Reserve followed a symlink state file")
	}
}
