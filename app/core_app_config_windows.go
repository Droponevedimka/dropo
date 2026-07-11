//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const (
	windowsRunRegistryPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	windowsAutoStartTaskName = "dropo-autostart"
)

// SetAutoStart registers the unelevated launcher in the current user's Run key.
// Older builds created a HighestAvailable Scheduled Task in a user-writable
// portable directory; remove it before changing the safe entry.
func SetAutoStart(enable bool) error {
	if err := removeAutoStartTask(); err != nil {
		return err
	}
	if enable {
		return createLauncherRunAutoStart()
	}
	removeLauncherRunAutoStart()
	return nil
}

// IsAutoStartEnabled reports whether only the safe per-user launcher entry exists.
func IsAutoStartEnabled() bool {
	return launcherRunAutoStartExists() && !autoStartTaskExists()
}

func autoStartTaskExists() bool {
	_, err := runSchtasks("/query", "/tn", windowsAutoStartTaskName)
	return err == nil
}

func removeLauncherRunAutoStart() {
	key, err := registry.OpenKey(
		registry.CURRENT_USER,
		windowsRunRegistryPath,
		registry.SET_VALUE,
	)
	if err != nil {
		return
	}
	defer key.Close()

	_ = key.DeleteValue(AppName)
	if LegacyAppDataDirName != AppName {
		_ = key.DeleteValue(LegacyAppDataDirName)
	}
}

func launcherRunAutoStartExists() bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, windowsRunRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	value, _, err := key.GetStringValue(AppName)
	return err == nil && strings.TrimSpace(value) != ""
}

func createLauncherRunAutoStart() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	launcher := resolveWindowsAutoStartLauncherPath(exePath)
	key, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunRegistryPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to open HKCU Run: %w", err)
	}
	defer key.Close()
	if err := key.SetStringValue(AppName, fmt.Sprintf("%q --autostart", launcher)); err != nil {
		return fmt.Errorf("failed to register UI autostart: %w", err)
	}
	return nil
}

func removeAutoStartTask() error {
	// Nothing to delete: keep disabling idempotent so the settings toggle never
	// surfaces a spurious "task not found" error.
	if !autoStartTaskExists() {
		return nil
	}
	out, err := runSchtasks("/delete", "/tn", windowsAutoStartTaskName, "/f")
	if err != nil {
		return fmt.Errorf("failed to remove autostart task: %v: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// runSchtasks invokes the system schtasks.exe with a hidden window and returns
// its combined output. The absolute System32 path avoids PATH hijacking.
func runSchtasks(args ...string) (string, error) {
	cmd := exec.Command(schtasksPath(), args...)
	configureBackgroundCommand(cmd)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func schtasksPath() string {
	if root := os.Getenv("SystemRoot"); root != "" {
		candidate := filepath.Join(root, "System32", "schtasks.exe")
		if fileExists(candidate) {
			return candidate
		}
	}
	return "schtasks.exe"
}

// resolveWindowsAutoStartLauncherPath maps whatever executable is running to the
// user-facing dropo.exe launcher that should be started at logon (never the
// dropo-core.exe helper, which must inherit elevation from the launcher).
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
