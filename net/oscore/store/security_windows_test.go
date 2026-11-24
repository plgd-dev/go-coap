//go:build windows

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestNewHighWaterFileStoreAcceptsPrivateWindowsDirectory(t *testing.T) {
	store, err := NewHighWaterFileStore(t.TempDir(), "private-windows-directory")
	if err != nil {
		t.Fatalf("NewHighWaterFileStore rejected a private Windows temp directory: %v", err)
	}
	if _, err := store.Reserve(); err != nil {
		t.Fatalf("reserve sequence in private Windows directory: %v", err)
	}
}

func TestWindowsRootedStateReplacement(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	if err := os.WriteFile(filepath.Join(dir, "temporary"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := replaceFileAtomicallyAt(root, dir, "temporary", "state"); err != nil {
		t.Fatalf("durably replace rooted OSCORE state file: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "new" {
		t.Fatalf("state content = %q, want %q", content, "new")
	}
}

func TestWindowsStoreDirectoryChainAcceptsStandardDataPaths(t *testing.T) {
	for _, path := range []string{
		os.Getenv("ProgramData"),
		os.Getenv("LOCALAPPDATA"),
	} {
		if path == "" {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			handle, err := openWindowsDirectoryGuardHandle(path)
			if err != nil {
				t.Fatalf("open standard Windows data path %q: %v", path, err)
			}
			defer windows.CloseHandle(handle)
			if err := validateWindowsDirectoryHandleACL(handle, path, true, true); err != nil {
				t.Fatalf("standard Windows data path %q was rejected as a store ancestor: %v", path, err)
			}
		})
	}
}

func TestWindowsStoreAncestorAllowsCreateButRejectsDelete(t *testing.T) {
	createOnly, descriptorErr := windows.SecurityDescriptorFromString("D:(A;;0x6;;;BU)")
	if descriptorErr != nil {
		t.Fatal(descriptorErr)
	}
	dacl, _, daclErr := createOnly.DACL()
	if daclErr != nil {
		t.Fatal(daclErr)
	}
	if policyErr := validateStoreDirectoryACLPolicy(dacl, true); policyErr != nil {
		t.Fatalf("ancestor create permissions should be allowed: %v", policyErr)
	}
	if policyErr := validateStoreDirectoryACL(dacl); policyErr == nil {
		t.Fatal("store directory with untrusted create permissions was accepted")
	}

	metadataWrite, descriptorErr := windows.SecurityDescriptorFromString("D:(A;;0x40000010;;;BU)")
	if descriptorErr != nil {
		t.Fatal(descriptorErr)
	}
	dacl, _, daclErr = metadataWrite.DACL()
	if daclErr != nil {
		t.Fatal(daclErr)
	}
	if policyErr := validateStoreDirectoryACLPolicy(dacl, true); policyErr != nil {
		t.Fatalf("ancestor metadata write permissions should be allowed: %v", policyErr)
	}

	for _, sddl := range []string{
		"D:(A;;0x40;;;BU)",
		"D:(A;;0x10006;;;BU)",
		"D:(A;;0x40000;;;BU)",
	} {
		t.Run(sddl, func(t *testing.T) {
			createAndDelete, descriptorErr := windows.SecurityDescriptorFromString(sddl)
			if descriptorErr != nil {
				t.Fatal(descriptorErr)
			}
			dacl, _, daclErr := createAndDelete.DACL()
			if daclErr != nil {
				t.Fatal(daclErr)
			}
			if policyErr := validateStoreDirectoryACLPolicy(dacl, true); policyErr == nil {
				t.Fatal("ancestor ACL granting delete access was accepted")
			}
		})
	}

	inheritOnly, descriptorErr := windows.SecurityDescriptorFromString("D:(A;OICIIO;FA;;;BU)")
	if descriptorErr != nil {
		t.Fatal(descriptorErr)
	}
	dacl, _, daclErr = inheritOnly.DACL()
	if daclErr != nil {
		t.Fatal(daclErr)
	}
	if policyErr := validateStoreDirectoryACLPolicy(dacl, true); policyErr != nil {
		t.Fatalf("ancestor inherit-only permissions should be evaluated on the child: %v", policyErr)
	}
	if policyErr := validateStoreDirectoryACL(dacl); policyErr == nil {
		t.Fatal("store directory with untrusted inherit-only permissions was accepted")
	}
}

func TestValidateStoreDirectoryACLRejectsBroadWriteAccess(t *testing.T) {
	for _, sddl := range []string{
		"D:(A;OICI;FA;;;WD)",
		"D:(A;OICIIO;FA;;;WD)",
	} {
		t.Run(sddl, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(sddl)
			if err != nil {
				t.Fatal(err)
			}
			dacl, _, err := descriptor.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if err := validateStoreDirectoryACL(dacl); err == nil {
				t.Fatal("directory ACL granting write access to Everyone was accepted")
			}
		})
	}
}

func TestValidateStoreDirectoryACLRejectsUnrelatedLocalAccountWriteAccess(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:(A;;FA;;;S-1-5-21-1000-2000-3000-4000)",
	)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStoreDirectoryACL(dacl); err == nil {
		t.Fatal("directory ACL granting write access to an unrelated local account was accepted")
	}
}

func TestValidateStoreDirectoryACLAllowsCreatorOwnerInheritance(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString(
		"D:(A;OICIIO;FA;;;CO)(A;;FA;;;SY)",
	)
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStoreDirectoryACL(dacl); err != nil {
		t.Fatalf("creator-owner inheritance ACE rejected: %v", err)
	}
}

func TestValidateStoreDirectoryOwnerAcceptsSystem(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString("O:SYG:SYD:P(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStoreDirectoryOwner(descriptor); err != nil {
		t.Fatalf("trusted SYSTEM owner was rejected: %v", err)
	}
}

func TestValidateStoreDirectoryOwnerRejectsUntrustedOwner(t *testing.T) {
	descriptor, err := windows.SecurityDescriptorFromString("O:WDG:SYD:P(A;;FA;;;SY)")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStoreDirectoryOwner(descriptor); err == nil {
		t.Fatal("untrusted Everyone owner was accepted")
	}
}

func TestNewHighWaterFileStoreRejectsDirectorySymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "store")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlink unavailable: %v", err)
	}
	if _, err := NewHighWaterFileStore(link, "no-reparse"); err == nil {
		t.Fatal("NewHighWaterFileStore accepted a directory symlink")
	}
}

func TestOpenFileNoFollowRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "state")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("file symlink unavailable: %v", err)
	}
	if _, err := openFileNoFollow(link, os.O_RDONLY, 0); err == nil {
		t.Fatal("openFileNoFollow accepted a file symlink")
	}
}

func TestLockFileExSerializesTwoStoreInstances(t *testing.T) {
	dir := t.TempDir()
	first, err := NewHighWaterFileStore(dir, "windows-lock")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewHighWaterFileStore(dir, "windows-lock")
	if err != nil {
		t.Fatal(err)
	}

	held := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withFileLock(first.sequenceLock, func() error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	secondDone := make(chan error, 1)
	go func() {
		_, err := second.Reserve()
		secondDone <- err
	}()

	select {
	case err := <-secondDone:
		close(release)
		<-firstDone
		t.Fatalf("second store instance acquired LockFileEx before the first released it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first LockFileEx holder failed: %v", err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second store instance failed after LockFileEx was released: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second store instance remained blocked after LockFileEx was released")
	}
}

func TestWindowsStoreDirectoryGuardBlocksAncestorReplacement(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "store-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(parent, "store")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	guard, err := newStoreDirectoryGuard(dir)
	if err != nil {
		t.Fatalf("create directory guard: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	guardDone := make(chan error, 1)
	go func() {
		guardDone <- guard.withOperation(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	if err := os.Rename(parent, parent+".replaced"); err == nil {
		close(release)
		<-guardDone
		t.Fatal("renamed an ancestor while the guarded store operation was active")
	}
	close(release)
	if err := <-guardDone; err != nil {
		t.Fatalf("guarded operation: %v", err)
	}
}
