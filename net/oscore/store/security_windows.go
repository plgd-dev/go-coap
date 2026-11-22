//go:build windows

package store

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateStoreDirectory(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q is not a directory", path)
	}
	descriptor, err := windows.GetNamedSecurityInfo(
		path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read OSCORE store directory ACL: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read OSCORE store directory DACL: %w", err)
	}
	return validateStoreDirectoryACL(dacl)
}

func validateStoreDirectoryACL(dacl *windows.ACL) error {
	if dacl == nil {
		return fmt.Errorf("directory has a null DACL")
	}
	broadSIDs := make([]*windows.SID, 0, 7)
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinWorldSid,
		windows.WinAuthenticatedUserSid,
		windows.WinBuiltinUsersSid,
		windows.WinBuiltinGuestsSid,
		windows.WinInteractiveSid,
		windows.WinNetworkSid,
		windows.WinAnonymousSid,
	} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			return fmt.Errorf("create well-known SID for OSCORE directory ACL validation: %w", err)
		}
		broadSIDs = append(broadSIDs, sid)
	}

	const directoryWriteRights = windows.FILE_WRITE_DATA |
		windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_EA |
		windows.FILE_WRITE_ATTRIBUTES |
		windows.ACCESS_MASK(0x40) |
		windows.DELETE |
		windows.WRITE_DAC |
		windows.WRITE_OWNER |
		windows.GENERIC_WRITE |
		windows.GENERIC_ALL

	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return fmt.Errorf("read OSCORE store directory ACL entry %d: %w", i, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
				continue
			}
			return fmt.Errorf("unsupported ACE type %d in OSCORE store directory ACL", ace.Header.AceType)
		}
		if windows.ACCESS_MASK(ace.Mask)&directoryWriteRights == 0 {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() {
			return fmt.Errorf("invalid SID in OSCORE store directory ACL")
		}
		for _, broadSID := range broadSIDs {
			if sid.Equals(broadSID) {
				return fmt.Errorf("directory ACL grants write access to a broad principal")
			}
		}
	}
	return nil
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, flags, perm)
}
