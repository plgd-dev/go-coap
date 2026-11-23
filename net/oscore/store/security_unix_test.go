//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package store

import (
	"os"
	"path/filepath"
	"syscall"
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

func TestNewHighWaterFileStoreAllowsConfiguredTrustedSymlink(t *testing.T) {
	root := privateTempDir(t)
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	store, err := NewHighWaterFileStore(
		filepath.Join(link, "nested"), "allowed-symlink", WithAllowedSymlinks(link),
	)
	if err != nil {
		t.Fatalf("NewHighWaterFileStore rejected configured trusted symlink: %v", err)
	}
	if _, err := os.Stat(store.dir); err != nil {
		t.Fatalf("store directory was not created through the allowed symlink: %v", err)
	}
}

func TestNewHighWaterFileStoreRejectsAllowedSymlinkWithWritableParent(t *testing.T) {
	root := privateTempDir(t)
	parent := filepath.Join(root, "writable-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	if _, err := NewHighWaterFileStore(
		filepath.Join(link, "nested"), "writable-symlink-parent", WithAllowedSymlinks(link),
	); err == nil {
		t.Fatal("NewHighWaterFileStore accepted an allowed symlink with a foreign-writable parent")
	}
}

func TestHighWaterFileStoreRejectsReplacedDirectoryBeforeOperation(t *testing.T) {
	root := privateTempDir(t)
	dir := filepath.Join(root, "store")
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := NewHighWaterFileStore(dir, "rooted-directory-replacement")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Errorf("close rooted store: %v", closeErr)
		}
	})
	if _, reserveErr := store.Reserve(); reserveErr != nil {
		t.Fatal(reserveErr)
	}

	moved := filepath.Join(root, "moved")
	if renameErr := os.Rename(dir, moved); renameErr != nil {
		t.Fatal(renameErr)
	}
	err = os.Symlink(outside, dir)
	if err != nil {
		t.Fatal(err)
	}
	err = store.SaveHighWater(1)
	if err == nil {
		t.Fatal("store operation succeeded after its directory was replaced")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("replacement directory received store files: %v", entries)
	}
}

func TestRootedStoreDirectoryCreationIgnoresReplacedParent(t *testing.T) {
	rootDir := privateTempDir(t)
	parent := filepath.Join(rootDir, "parent")
	outside := filepath.Join(rootDir, "outside")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	moved := filepath.Join(rootDir, "moved")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, parent); err != nil {
		t.Fatal(err)
	}
	if err := createStoreDirectoryAt(root, parent, []string{filepath.Join(parent, "child")}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(moved, "child")); err != nil || !info.IsDir() {
		t.Fatalf("child under retained root = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "child")); !os.IsNotExist(err) {
		t.Fatalf("replacement parent received a child directory: %v", err)
	}
}

func TestHighWaterFileStoreCloseReleasesRoot(t *testing.T) {
	store, err := NewHighWaterFileStore(privateTempDir(t), "close-rooted-store")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reserve(); err == nil {
		t.Fatal("closed store allowed another reservation")
	}
	if err := store.SaveHighWater(1); err == nil {
		t.Fatal("closed store allowed a high-water update")
	}
}

func TestNewHighWaterFileStoreRejectsConfiguredSymlinkToForeignOwner(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "foreign")
	if err := os.Mkdir(target, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Chown(target, os.Geteuid()+1, -1); err != nil {
		t.Skipf("cannot change directory owner for test: %v", err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	if _, err := NewHighWaterFileStore(
		filepath.Join(link, "nested"), "foreign-symlink", WithAllowedSymlinks(link),
	); err == nil {
		t.Fatal("NewHighWaterFileStore accepted a configured symlink to a foreign-owned directory")
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

func TestOpenFileAtRootRejectsSymlink(t *testing.T) {
	dir := privateTempDir(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "state")); err != nil {
		t.Skipf("file symlinks are unavailable: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, openErr := openFileAtRoot(root, "state", os.O_RDONLY, 0); openErr == nil {
		t.Fatal("openFileAtRoot followed a symlink")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "unchanged" {
		t.Fatalf("symlink target contents = %q, want unchanged", got)
	}
}
