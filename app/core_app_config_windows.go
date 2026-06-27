//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// SetAutoStart enables or disables Windows startup launch via HKCU Run.
func SetAutoStart(enable bool) error {
	key, _, err := registry.CreateKey(
		registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Run`,
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
		if err := key.SetStringValue(AppName, exePath); err != nil {
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
		`Software\Microsoft\Windows\CurrentVersion\Run`,
		registry.QUERY_VALUE,
	)
	if err != nil {
		return false
	}
	defer key.Close()

	_, _, err = key.GetStringValue(AppName)
	return err == nil
}
