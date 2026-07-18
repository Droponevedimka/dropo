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
		"platform":       CurrentPlatformTarget().ReleaseOS,
		"selfUpdate":     CurrentPlatformTarget().SelfUpdate,
	}
}

// DownloadAndInstallUpdate загружает и устанавливает обновление
func (a *App) DownloadAndInstallUpdate() map[string]interface{} {
	if !CurrentPlatformTarget().SelfUpdate {
		return map[string]interface{}{
			"success": false,
			"error":   "Self-update is not implemented for " + CurrentPlatformTarget().ReleaseOS,
		}
	}
	updateInfo, err := CheckForUpdates()
	if err != nil {
		return map[string]interface{}{"success": false, "error": "Failed to resolve update: " + err.Error()}
	}
	if !updateInfo.Available {
		return map[string]interface{}{"success": false, "error": "No update is available"}
	}
	if err := validateTrustedUpdateURL(updateInfo.DownloadURL); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}

	// Остановить VPN если запущен
	if a.isVPNRunning() {
		a.Stop()
	}

	a.AddToLogBuffer("Downloading update...")

	// Download the update
	tempFile, err := DownloadUpdate(updateInfo.DownloadURL, updateInfo.FileSize, updateInfo.SHA256, func(downloaded, total int64) {
		// Progress callback - can emit events if needed
		if total > 0 {
			progress := float64(downloaded) / float64(total) * 100
			a.emitEvent("update-progress", progress)
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
	installDir, launchExe := resolvePortableInstallRoot(execDir)

	updateScript, scriptContent, err := makeProtectedUpdateScript(tempFile, launchExe, installDir, updateInfo.SHA256)
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
		a.FinalizeQuit()
	}()

	return map[string]interface{}{
		"success": true,
		"message": "Update downloaded, app will restart",
	}
}

func resolvePortableInstallRoot(execDir string) (string, string) {
	clean := filepath.Clean(execDir)
	if strings.EqualFold(filepath.Base(clean), "resources") {
		root := filepath.Dir(clean)
		return root, filepath.Join(root, "dropo.exe")
	}
	if strings.EqualFold(filepath.Base(clean), "app") &&
		strings.EqualFold(filepath.Base(filepath.Dir(clean)), "resources") {
		root := filepath.Dir(filepath.Dir(clean))
		return root, filepath.Join(root, "dropo.exe")
	}
	return clean, filepath.Join(clean, "dropo.exe")
}

func makeUpdateScript(tempFile, execPath, execDir string) (string, string, error) {
	// Place the script in a fresh private directory (0700) with a random name so
	// another local process cannot pre-create or swap the script between our write
	// and the elevated launch (TOCTOU → arbitrary code execution). See review.md §4.
	scriptDir, err := os.MkdirTemp("", "dropo-update-*")
	if err != nil {
		return "", "", fmt.Errorf("failed to create private update dir: %w", err)
	}
	_ = os.Chmod(scriptDir, 0700)
	return renderUpdateScript(scriptDir, tempFile, execPath, execDir, "")
}

func makeProtectedUpdateScript(tempFile, execPath, execDir, expectedSHA256 string) (string, string, error) {
	if normalizeGitHubSHA256(expectedSHA256) == "" {
		return "", "", fmt.Errorf("release asset has no valid SHA-256 digest")
	}
	root, err := prepareProtectedRuntime("updates")
	if err != nil {
		return "", "", err
	}
	scriptDir, err := os.MkdirTemp(root, "update-*")
	if err != nil {
		return "", "", err
	}
	return renderUpdateScript(scriptDir, tempFile, execPath, execDir, expectedSHA256)
}

func renderUpdateScript(scriptDir, tempFile, execPath, execDir, expectedSHA256 string) (string, string, error) {
	expectedSHA256 = normalizeGitHubSHA256(expectedSHA256)
	hashCheck := ""
	if expectedSHA256 != "" {
		hashCheck = fmt.Sprintf("$actualHash = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant()\nif ($actualHash -ne %s) { throw 'Update SHA-256 mismatch' }\n", psQuote(expectedSHA256))
	}

	switch strings.ToLower(filepath.Ext(tempFile)) {
	case ".exe":
		updateScript := filepath.Join(scriptDir, "dropo_update.ps1")
		scriptContent := fmt.Sprintf(`$ErrorActionPreference = "Stop"
Start-Sleep -Seconds 2
$package = %s
%s
$process = Start-Process -FilePath $package -ArgumentList "--from-update" -PassThru
$process.WaitForExit()
Remove-Item -LiteralPath $package -Force -ErrorAction SilentlyContinue
Remove-Item -LiteralPath $PSCommandPath -Force
`, psQuote(tempFile), hashCheck)
		return updateScript, scriptContent, nil
	case ".zip":
		updateScript := filepath.Join(scriptDir, "dropo_update.ps1")
		extractDir := filepath.Join(scriptDir, "extract")
		scriptContent := fmt.Sprintf(`$ErrorActionPreference = "Stop"
Start-Sleep -Seconds 2
$package = %s
$dest = %s
$extract = %s
$exe = %s
%s
if (Test-Path -LiteralPath $extract) { Remove-Item -LiteralPath $extract -Recurse -Force }
New-Item -ItemType Directory -Path $extract | Out-Null
Expand-Archive -LiteralPath $package -DestinationPath $extract -Force
Get-ChildItem -LiteralPath $extract | Copy-Item -Destination $dest -Recurse -Force
Remove-Item -LiteralPath $package -Force
Remove-Item -LiteralPath $extract -Recurse -Force
Start-Process -FilePath $exe -WorkingDirectory $dest
Remove-Item -LiteralPath $PSCommandPath -Force
`, psQuote(tempFile), psQuote(execDir), psQuote(extractDir), psQuote(execPath), hashCheck)
		return updateScript, scriptContent, nil
	default:
		_ = os.RemoveAll(scriptDir)
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
