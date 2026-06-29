//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const windowsRunRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// SetAutoStart enables or disables Windows startup launch via HKCU Run.
func SetAutoStart(enable bool) error {
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		windowsRunRegistryPath,
		registry.SET_VALUE|registry.QUERY_VALUE,
	)
	if err != nil {
		return fmt.Errorf("failed to open registry: %w", err)
	}
	defer key.Close()

	if enable {
		exePath, err := os.Executable()
		if err != nil {
			return fmt.Errorf("failed to get executable path: %w", err)
		}
		exePath, _ = filepath.EvalSymlinks(exePath)
		if err := key.SetStringValue(AppName, windowsAutoStartCommandForExecutable(exePath)); err != nil {
			return fmt.Errorf("failed to add to autostart: %w", err)
		}
		if LegacyAppDataDirName != AppName {
			_ = key.DeleteValue(LegacyAppDataDirName)
		}
		return nil
	}

	if err := key.DeleteValue(AppName); err != nil && err != registry.ErrNotExist {
		return fmt.Errorf("failed to remove from autostart: %w", err)
	}
	if LegacyAppDataDirName != AppName {
		if err := key.DeleteValue(LegacyAppDataDirName); err != nil && err != registry.ErrNotExist {
			return fmt.Errorf("failed to remove legacy autostart: %w", err)
		}
	}
	return nil
}

// IsAutoStartEnabled checks whether Windows startup launch is enabled.
func IsAutoStartEnabled() bool {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		windowsRunRegistryPath,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer key.Close()

	_, _, err = key.GetStringValue(AppName)
	return err == nil
}

func windowsAutoStartCommandForExecutable(exePath string) string {
	return `"` + resolveWindowsAutoStartLauncherPath(exePath) + `"`
}

func resolveWindowsAutoStartLauncherPath(exePath string) string {
	clean := filepath.Clean(exePath)
	base := filepath.Base(clean)
	if strings.EqualFold(base, AppName+".exe") {
		return clean
	}

	dir := filepath.Dir(clean)
	candidates := []string{filepath.Join(dir, AppName+".exe")}
	if strings.EqualFold(base, AppName+"-core.exe") {
		candidates = append(candidates, filepath.Join(filepath.Dir(dir), AppName+".exe"))
		if strings.EqualFold(filepath.Base(dir), ResourcesFolder) {
			candidates = append(candidates, filepath.Join(filepath.Dir(dir), AppName+".exe"))
		}
		if strings.EqualFold(filepath.Base(filepath.Dir(dir)), ResourcesFolder) {
			candidates = append(candidates, filepath.Join(filepath.Dir(filepath.Dir(dir)), AppName+".exe"))
		}
	}

	for _, candidate := range uniqueStrings(candidates) {
		if fileExists(candidate) {
			return candidate
		}
	}
	return clean
}
