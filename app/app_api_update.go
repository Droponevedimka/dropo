package main

// Update methods for dropo.
// This file contains auto-update functionality

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		"assetName":      updateInfo.AssetName,
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
	execDir := filepath.Dir(execPath)

	updateScript, scriptContent, err := makeUpdateScript(tempFile, execPath, execDir)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to prepare update script: " + err.Error(),
		}
	}

	if err := os.WriteFile(updateScript, []byte(scriptContent), 0755); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Failed to create update script: " + err.Error(),
		}
	}

	// Run the update script
	var cmd *exec.Cmd
	if strings.EqualFold(filepath.Ext(updateScript), ".ps1") {
		cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", updateScript)
	} else {
		cmd = exec.Command("cmd", "/C", "start", "/b", updateScript)
	}
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

func makeUpdateScript(tempFile, execPath, execDir string) (string, string, error) {
	switch strings.ToLower(filepath.Ext(tempFile)) {
	case ".zip":
		updateScript := filepath.Join(os.TempDir(), "dropo_update.ps1")
		extractDir := filepath.Join(os.TempDir(), fmt.Sprintf("dropo_update_%d", time.Now().UnixNano()))
		scriptContent := fmt.Sprintf(`$ErrorActionPreference = "Stop"
Start-Sleep -Seconds 2
$zip = %s
$dest = %s
$extract = %s
$exe = %s
if (Test-Path -LiteralPath $extract) { Remove-Item -LiteralPath $extract -Recurse -Force }
New-Item -ItemType Directory -Path $extract | Out-Null
Expand-Archive -LiteralPath $zip -DestinationPath $extract -Force
Get-ChildItem -LiteralPath $extract | Copy-Item -Destination $dest -Recurse -Force
Remove-Item -LiteralPath $zip -Force
Remove-Item -LiteralPath $extract -Recurse -Force
Start-Process -FilePath $exe -WorkingDirectory $dest
Remove-Item -LiteralPath $PSCommandPath -Force
`, psQuote(tempFile), psQuote(execDir), psQuote(extractDir), psQuote(execPath))
		return updateScript, scriptContent, nil
	case ".exe":
		updateScript := filepath.Join(os.TempDir(), "dropo_update.bat")
		scriptContent := fmt.Sprintf(`@echo off
timeout /t 2 /nobreak > nul
copy /y "%s" "%s"
del "%s"
start "" "%s"
del "%%~f0"
`, tempFile, execPath, tempFile, execPath)
		return updateScript, scriptContent, nil
	default:
		return "", "", fmt.Errorf("unsupported update asset type: %s", filepath.Ext(tempFile))
	}
}

func psQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// GetAppVersion возвращает текущую версию приложения
func (a *App) GetAppVersion() map[string]interface{} {
	return GetVersionInfo()
}
