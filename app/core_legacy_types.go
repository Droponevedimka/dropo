// Package main provides legacy type definitions for migration purposes.
// These types are used only to read old configuration files during migration.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

// ConnectionProfile represents a VPN connection profile (legacy format).
// Used for migration from old profiles.json format.
type ConnectionProfile struct {
	ID              int       `json:"id"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"created_at"`
	SubscriptionURL string    `json:"subscription_url,omitempty"`
	ProxyCount      int       `json:"proxy_count,omitempty"`
	WireGuardTags   []string  `json:"wireguard_tags,omitempty"`
}

// AppConfigLegacy stores application preferences and settings (legacy format).
// Used for migration from old app_config.json format.
type AppConfigLegacy struct {
	AutoStart         bool      `json:"auto_start"`
	Notifications     bool      `json:"notifications"`
	CheckUpdates      bool      `json:"check_updates"`
	EnableLogging     bool      `json:"enable_logging"`
	LogLevel          LogLevel  `json:"log_level"`
	Theme             Theme     `json:"theme"`
	Language          Language  `json:"language"`
	AutoUpdateSub     bool      `json:"auto_update_sub"`
	SubUpdateInterval int       `json:"sub_update_interval"`
	LastSubUpdate     time.Time `json:"last_sub_update"`
	LastUpdateCheck   string    `json:"last_update_check"`
	ActiveProfileID   int       `json:"active_profile_id"`
}

// SetAutoStartLegacy enables or disables system startup launch.
func SetAutoStartLegacy(enable bool) error {
	if runtime.GOOS != "windows" {
		return nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return err
	}

	startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	shortcutPath := filepath.Join(startupDir, AppDisplayName+".lnk")

	if enable {
		// Create shortcut using PowerShell
		psScript := `$WshShell = New-Object -ComObject WScript.Shell; $Shortcut = $WshShell.CreateShortcut('` + shortcutPath + `'); $Shortcut.TargetPath = '` + exePath + `'; $Shortcut.WorkingDirectory = '` + filepath.Dir(exePath) + `'; $Shortcut.Save()`
		cmd := exec.Command("powershell", "-Command", psScript)
		configureBackgroundCommand(cmd)
		return cmd.Run()
	} else {
		// Remove shortcut
		return os.Remove(shortcutPath)
	}
}

// IsAutoStartEnabledLegacy checks if app is set to start with Windows.
func IsAutoStartEnabledLegacy() bool {
	if runtime.GOOS != "windows" {
		return false
	}

	startupDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	shortcutPath := filepath.Join(startupDir, AppDisplayName+".lnk")

	_, err := os.Stat(shortcutPath)
	return err == nil
}
