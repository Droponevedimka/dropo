//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

func prepareProtectedRuntime(version string) (string, error) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil || strings.TrimSpace(programData) == "" {
		return "", fmt.Errorf("resolve ProgramData known folder: %w", err)
	}
	root := filepath.Join(filepath.Clean(programData), AppDataDirName)
	if err := createAndProtectDirectory(root); err != nil {
		return "", err
	}
	runtimeRoot := filepath.Join(root, "runtime")
	if err := createAndProtectDirectory(runtimeRoot); err != nil {
		return "", err
	}
	path := filepath.Join(runtimeRoot, filepath.Base(version))
	if filepath.Base(version) != version || version == "." || version == ".." {
		return "", fmt.Errorf("invalid protected runtime version")
	}
	if err := createAndProtectDirectory(path); err != nil {
		return "", err
	}
	return path, nil
}

// cleanupStaleProtectedRuntimes removes only versioned dependency caches owned
// by dropo. The current runtime and the protected updater workspace are kept.
// Startup process cleanup runs before this function, so no managed executable
// from an older cache should still be alive.
func cleanupStaleProtectedRuntimes(currentVersion string) (int, error) {
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil || strings.TrimSpace(programData) == "" {
		return 0, fmt.Errorf("resolve ProgramData known folder: %w", err)
	}
	runtimeRoot := filepath.Join(filepath.Clean(programData), AppDataDirName, "runtime")
	entries, err := os.ReadDir(runtimeRoot)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == filepath.Base(currentVersion) || entry.Name() == "updates" {
			continue
		}
		target := filepath.Join(runtimeRoot, entry.Name())
		rel, relErr := filepath.Rel(runtimeRoot, target)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			return removed, fmt.Errorf("refuse stale runtime path %q", target)
		}
		if err := os.RemoveAll(target); err != nil {
			return removed, fmt.Errorf("remove stale runtime %s: %w", entry.Name(), err)
		}
		removed++
	}
	return removed, nil
}

func createAndProtectDirectory(path string) error {
	if err := rejectWindowsReparsePoint(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(path, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("create protected directory %s: %w", path, err)
	}
	if err := rejectWindowsReparsePoint(path); err != nil {
		return err
	}
	systemSID, err := windows.StringToSid("S-1-5-18")
	if err != nil {
		return err
	}
	adminSID, err := windows.StringToSid("S-1-5-32-544")
	if err != nil {
		return err
	}
	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(systemSID),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
			Trustee: windows.TRUSTEE{
				TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(adminSID),
			},
		},
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("build protected runtime ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		adminSID,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("protect runtime ACL: %w", err)
	}
	return rejectWindowsReparsePoint(path)
}

func rejectWindowsReparsePoint(path string) error {
	ptr, err := syscall.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("protected runtime path is a reparse point: %s", path)
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fmt.Errorf("protected runtime path is not a directory: %s", path)
	}
	return nil
}
