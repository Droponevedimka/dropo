package main

// Update methods for dropo.
// This file contains auto-update functionality

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// CheckForUpdates проверяет наличие обновлений (API для фронтенда)
func (a *App) CheckForUpdates() map[string]interface{} {
	updateInfo, err := CheckForUpdates()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success":        true,
		"hasUpdate":      updateInfo.Available,
		"currentVersion": updateInfo.CurrentVersion,
		"latestVersion":  updateInfo.Version,
		"downloadURL":    updateInfo.DownloadURL,
		"releaseNotes":   updateInfo.Description,
		"publishedAt":    updateInfo.PublishedAt,
		"releaseURL":     updateInfo.ReleaseURL,
		"fileSize":       updateInfo.FileSize,
	}
}

// DownloadAndInstallUpdate загружает и устанавливает обновление
func (a *App) DownloadAndInstallUpdate(downloadURL string) map[string]interface{} {
	// Остановить VPN если запущен
	if a.isRunning {
		a.Stop()
	}

	a.AddToLogBuffer("Downloading update...")

	// Download the update
	tempFile, err := DownloadUpdate(downloadURL, func(downloaded, total int64) {
		// Progress callback - can emit events if needed
		if total > 0 {
			progress := float64(downloaded) / float64(total) * 100
			wailsRuntime.EventsEmit(a.ctx, "update-progress", progress)
		}
	})

	if err != nil {
		a.AddToLogBuffer("Update download failed: " + err.Error())
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to download update: " + err.Error(),
		}
	}

	a.AddToLogBuffer("Update downloaded to: " + tempFile)

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to get executable path: " + err.Error(),
		}
	}

	// Create update script that will replace the executable after app closes
	updateScript := filepath.Join(os.TempDir(), "dropo_update.bat")
	scriptContent := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak > nul
copy /y "%s" "%s"
del "%s"
start "" "%s"
del "%%~f0"
`, tempFile, execPath, tempFile, execPath)

	if err := os.WriteFile(updateScript, []byte(scriptContent), 0755); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to create update script: " + err.Error(),
		}
	}

	// Run the update script
	cmd := exec.Command("cmd", "/C", "start", "/b", updateScript)
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to start update script: " + err.Error(),
		}
	}

	a.AddToLogBuffer("Update script started, restarting app...")

	// Quit the app
	go func() {
		time.Sleep(500 * time.Millisecond)
		wailsRuntime.Quit(a.ctx)
	}()

	return map[string]interface{}{
		"success": true,
		"message": "Update downloaded, app will restart",
	}
}

// GetAppVersion возвращает текущую версию приложения
func (a *App) GetAppVersion() map[string]interface{} {
	return GetVersionInfo()
}
