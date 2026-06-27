package main

// UI and window methods for dropo.
// This file contains window management and UI operations

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Quit closes the application (called from UI)
func (a *App) Quit() {
	a.QuitApp()
}

// QuitApp closes the application (alias). Immediate path used by the tray and
// other callers: stop everything, then exit. The in-app exit button instead
// uses the two-step PrepareQuit + FinalizeQuit so a closing notice can be shown.
func (a *App) QuitApp() {
	if a.isShuttingDown() {
		os.Exit(0)
	}
	a.PrepareQuit()
	a.FinalizeQuit()
}

// PrepareQuit stops the VPN, WinDivert and all background processes WITHOUT
// exiting, then reports whether a Telegram-proxy cleanup notice should be shown
// before the portable app finally closes. The frontend calls FinalizeQuit once
// the notice is dismissed (or its timer elapses).
func (a *App) PrepareQuit() TelegramProxyStatusInfo {
	if a.isShuttingDown() {
		return TelegramProxyStatusInfo{}
	}
	quitBusyID := "app-exit"
	a.emitBusy(quitBusyID, true, "Завершаем сеанс: останавливаем VPN, WinDivert и фоновые процессы...")
	if a.logPath == "" && a.tempLogPath == "" {
		a.setupLogPath()
	}
	a.writeLog("Application quit requested")
	a.emitBusy(quitBusyID, true, "Останавливаем VPN и дочерние процессы dropo...")
	injected := false
	if a.storage != nil {
		injected = a.storage.GetAppSettings().TelegramProxyInjected
	}
	a.requestShutdown()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if a.isInitialized() {
			a.Stop()
		}
		if a.nativeWG != nil {
			a.writeLog("Stopping WireGuard health check...")
			a.nativeWG.StopHealthCheck()
			a.writeLog("Stopping all Native WireGuard tunnels...")
			a.nativeWG.StopAllTunnels()
		}
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.cleanupDropoRuntimeResidue("quit")
		closeManagedProcessJob(a.writeLog)
		if a.trafficStats != nil {
			a.trafficStats.Save()
		}
	}()

	select {
	case <-done:
	case <-time.After(4 * time.Second):
		a.writeLog("Quit cleanup timed out; forcing process exit")
	}

	a.emitBusy(quitBusyID, false, "")
	// The sidecar is stopped now, so the local proxy saved in Telegram points at
	// a dead port — this is exactly when the user must be told to remove it.
	return TelegramProxyStatusInfo{Injected: injected, RecommendRemove: injected}
}

// QuitWithTelegramNotice is the tray "Выход" path: stop all background processes,
// then ALWAYS bring the window up and show the Telegram-proxy cleanup notice
// (a safety reminder — the local proxy cannot be removed programmatically). The
// frontend calls FinalizeQuit when the user dismisses it.
func (a *App) QuitWithTelegramNotice() {
	if a.isShuttingDown() {
		os.Exit(0)
	}
	status := a.PrepareQuit()
	if a.ctx != nil {
		a.ShowWindow()
		// Give the (possibly hidden) webview a moment to come up before the event,
		// otherwise the notice can be missed.
		time.Sleep(350 * time.Millisecond)
		wailsRuntime.EventsEmit(a.ctx, "show-telegram-exit-notice", status)
		return
	}
	a.FinalizeQuit()
}

// FinalizeQuit performs the actual process exit. Call after PrepareQuit (and
// after any closing notice has been dismissed).
func (a *App) FinalizeQuit() {
	a.closeLogFile()
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
	if a.ctx != nil {
		wailsRuntime.Quit(a.ctx)
	}
	os.Exit(0)
}

// ShowWindow shows the application window
func (a *App) ShowWindow() {
	if a.ctx != nil {
		wailsRuntime.WindowShow(a.ctx)
		a.SetWindowVisible(true)
	}
}

// ShowAbout shows about dialog
func (a *App) ShowAbout() {
	if a.ctx != nil {
		info := GetVersionInfo()
		wailsRuntime.MessageDialog(a.ctx, wailsRuntime.MessageDialogOptions{
			Type:  wailsRuntime.InfoDialog,
			Title: "О программе dropo",
			Message: fmt.Sprintf(
				"Версия: %s\nsing-box: %s\nGitHub: %s\nTelegram: %s",
				info["fullVersion"],
				info["singboxVersion"],
				info["githubURL"],
				info["telegramName"],
			),
		})
	}
}

// HideWindow hides the application window
func (a *App) HideWindow() {
	if a.ctx != nil {
		if !isSystrayReady() {
			a.writeLog("HideWindow skipped: systray is unavailable")
			return
		}
		wailsRuntime.WindowHide(a.ctx)
		a.SetWindowVisible(false)
		a.refreshTrayIconForCurrentState()
	}
}

func (a *App) refreshTrayIconForCurrentState() {
	if a == nil {
		return
	}
	a.mu.Lock()
	running := a.isRunning
	hasError := a.hasError
	a.mu.Unlock()
	switch {
	case hasError:
		UpdateTrayIcon("error")
	case running:
		UpdateTrayIcon("connected")
	default:
		UpdateTrayIcon("disconnected")
	}
}

// OpenConfigFolder opens the config folder in file explorer
func (a *App) OpenConfigFolder() {
	var configDir string
	switch runtime.GOOS {
	case "windows":
		configDir = a.basePath
		if configDir == "" {
			configDir = filepath.Join(os.Getenv("LOCALAPPDATA"), AppDataDirName)
		}
	case "darwin":
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, "Library", "Application Support", AppDataDirName)
	default:
		home, _ := os.UserHomeDir()
		configDir = filepath.Join(home, ".config", AppDataDirName)
	}

	openFolder(configDir)
}

// OpenLogs opens the logs folder in file explorer
func (a *App) OpenLogs() {
	var logDir string
	switch runtime.GOOS {
	case "windows":
		logDir = filepath.Join(os.Getenv("LOCALAPPDATA"), AppDataDirName, "logs")
	case "darwin":
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, "Library", "Logs", AppDataDirName)
	default:
		home, _ := os.UserHomeDir()
		logDir = filepath.Join(home, ".local", "share", AppDataDirName, "logs")
	}

	// Create logs folder if it doesn't exist
	os.MkdirAll(logDir, 0755)

	openFolder(logDir)
}

// openFolder opens a folder in the system file manager
func openFolder(path string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("explorer", path)
	case "darwin":
		cmd = exec.Command("open", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}

	cmd.Start()
}

// GetVersion returns application version
func (a *App) GetVersion() string {
	return Version
}

// GetSingBoxVersion returns bundled sing-box version.
func (a *App) GetSingBoxVersion() string {
	return SingBoxVersion
}

// GetSingBoxInfo returns sing-box information
func (a *App) GetSingBoxInfo() map[string]interface{} {
	result := map[string]interface{}{
		"found":   false,
		"path":    "",
		"version": "",
	}

	if a.singboxPath != "" && fileExists(a.singboxPath) {
		result["found"] = true
		result["path"] = a.singboxPath
	}

	return result
}

// SetWindowVisible sets window visibility flag (for ping optimization)
func (a *App) SetWindowVisible(visible bool) {
	a.windowVisibleFlag.Store(visible)
	a.windowVisible = visible
}

// IsWindowVisible returns window visibility flag
func (a *App) IsWindowVisible() bool {
	return a.windowVisibleFlag.Load()
}
