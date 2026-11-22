//go:build windows

package store

import (
	"testing"

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
