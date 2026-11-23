//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func validateStoreDirectory(path string) error {
	handle, err := openWindowsPathNoFollow(path, true, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	descriptor, err := windows.GetSecurityInfo(
		handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read OSCORE store directory ACL: %w", err)
	}
	if err := validateStoreDirectoryOwner(descriptor); err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read OSCORE store directory DACL: %w", err)
	}
	return validateStoreDirectoryACL(dacl)
}

// validateStoreDirectoryPath rejects directory symlinks and reparse points in
// every component. os.Lstat alone is useful for the final component; opening
// the directory by handle below also verifies that no reparse point was raced
// in while the path was being resolved.
func validateStoreDirectoryPath(path string) error {
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("make OSCORE store directory absolute: %w", err)
	}
	return validateWindowsStoreDirectoryComponents(absolute)
}

func validateWindowsStoreDirectoryComponents(absolute string) error {
	volume := filepath.VolumeName(absolute)
	rest := strings.TrimPrefix(absolute, volume)
	current := volume
	if strings.HasPrefix(rest, `\`) {
		current = volume + `\`
		rest = strings.TrimPrefix(rest, `\`)
	}
	for _, component := range strings.Split(rest, `\`) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		if err := validateWindowsStoreDirectoryComponent(current); err != nil {
			return err
		}
	}
	return nil
}

func validateWindowsStoreDirectoryComponent(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("lstat OSCORE store directory component %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("OSCORE store directory component %q is a reparse point or symlink", path)
	}
	return validateWindowsHandleFileAttributes(path, true)
}

func validateWindowsHandleFileAttributes(path string, directory bool) error {
	handle, err := openWindowsPathNoFollow(path, directory, windows.FILE_READ_ATTRIBUTES)
	if err != nil {
		return err
	}
	return windows.CloseHandle(handle)
}

func openWindowsPathNoFollow(path string, directory bool, access uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return 0, fmt.Errorf("open OSCORE store path %q without following reparse points: %w", path, err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("inspect OSCORE store path %q: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("OSCORE store path %q is a reparse point", path)
	}
	if directory && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("OSCORE store path %q is not a directory", path)
	}
	if !directory && info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		windows.CloseHandle(handle)
		return 0, fmt.Errorf("OSCORE store path %q is a directory", path)
	}
	return handle, nil
}

func validateStoreDirectoryACL(dacl *windows.ACL) error {
	if dacl == nil {
		return fmt.Errorf("directory has a null DACL")
	}
	trustedSIDs, err := storeDirectoryTrustedSIDs()
	if err != nil {
		return err
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		if err := validateStoreDirectoryACE(dacl, i, trustedSIDs); err != nil {
			return err
		}
	}
	return nil
}

func validateStoreDirectoryOwner(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read OSCORE store directory owner: %w", err)
	}
	if owner == nil || !owner.IsValid() {
		return fmt.Errorf("directory has an invalid or missing owner")
	}
	trusted, err := storeDirectoryTrustedSIDs()
	if err != nil {
		return err
	}
	for _, trustedSID := range trusted {
		if owner.Equals(trustedSID) {
			return nil
		}
	}
	return fmt.Errorf("directory owner is not a trusted OSCORE store account")
}

func validateStoreFileOwner(descriptor *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("read OSCORE store file owner: %w", err)
	}
	if owner == nil || !owner.IsValid() {
		return fmt.Errorf("store file has an invalid or missing owner")
	}
	trusted, err := storeDirectoryTrustedSIDs()
	if err != nil {
		return err
	}
	for _, trustedSID := range trusted {
		if owner.Equals(trustedSID) {
			return nil
		}
	}
	return fmt.Errorf("store file owner is not a trusted OSCORE store account")
}

func storeDirectoryTrustedSIDs() ([]*windows.SID, error) {
	sids := make([]*windows.SID, 0, 3)
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err == nil && user != nil && user.User.Sid != nil {
		sids = append(sids, user.User.Sid)
	}
	for _, sidType := range []windows.WELL_KNOWN_SID_TYPE{
		windows.WinBuiltinAdministratorsSid,
		windows.WinLocalSystemSid,
	} {
		sid, err := windows.CreateWellKnownSid(sidType)
		if err != nil {
			return nil, fmt.Errorf("create trusted SID for OSCORE directory ownership: %w", err)
		}
		sids = append(sids, sid)
	}
	return sids, nil
}

func validateStoreDirectoryACE(dacl *windows.ACL, index uint32, trustedSIDs []*windows.SID) error {
	const directoryWriteRights = windows.FILE_WRITE_DATA |
		windows.FILE_APPEND_DATA |
		windows.FILE_WRITE_EA |
		windows.FILE_WRITE_ATTRIBUTES |
		windows.ACCESS_MASK(0x40) |
		windows.ACCESS_MASK(0x10) | // FILE_DELETE_CHILD
		windows.DELETE |
		windows.WRITE_DAC |
		windows.WRITE_OWNER |
		windows.GENERIC_WRITE |
		windows.GENERIC_ALL

	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, index, &ace); err != nil {
		return fmt.Errorf("read OSCORE store directory ACL entry %d: %w", index, err)
	}
	if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
		return nil
	}
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
		return fmt.Errorf("unsupported ACE type %d in OSCORE store directory ACL", ace.Header.AceType)
	}
	if windows.ACCESS_MASK(ace.Mask)&directoryWriteRights == 0 {
		return nil
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if !sid.IsValid() {
		return fmt.Errorf("invalid SID in OSCORE store directory ACL")
	}
	for _, trustedSID := range trustedSIDs {
		if sid.Equals(trustedSID) {
			return nil
		}
	}
	return fmt.Errorf("directory ACL grants write access to an untrusted principal")
}

func openFileNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var access uint32
	switch {
	case flags&(os.O_WRONLY|os.O_RDWR) == os.O_RDWR:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	case flags&os.O_WRONLY != 0:
		access = windows.GENERIC_WRITE
	default:
		access = windows.GENERIC_READ
	}
	var disposition uint32
	switch {
	case flags&os.O_CREATE != 0 && flags&os.O_EXCL != 0:
		disposition = windows.CREATE_NEW
	case flags&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	default:
		disposition = windows.OPEN_EXISTING
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(name, access, share, nil, disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("inspect OSCORE store file %q: %w", path, err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("OSCORE store file %q is a reparse point", path)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("OSCORE store file %q is a directory", path)
	}
	descriptor, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("read OSCORE store file security %q: %w", path, err)
	}
	if err := validateStoreFileOwner(descriptor); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("read OSCORE store file DACL %q: %w", path, err)
	}
	if err := validateStoreDirectoryACL(dacl); err != nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("validate OSCORE store file ACL %q: %w", path, err)
	}
	return os.NewFile(uintptr(handle), path), nil
}
