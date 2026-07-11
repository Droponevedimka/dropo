package main

// UI and window methods for dropo.
// This file contains window management and UI operations

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
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

// RequestFrontendQuit asks the Flutter shell to run the same two-step quit flow
// as the in-app Exit button. If the frontend does not acknowledge the request,
// the core falls back to a backend-only shutdown and closes the stale UI window.
func (a *App) RequestFrontendQuit(source string) {
	if a == nil {
		os.Exit(0)
	}
	if source == "" {
		source = "external"
	}
	if a.isShuttingDown() {
		a.FinalizeQuit()
		return
	}
	a.writeLog("Frontend quit requested from " + source)
	requestPlatformFrontendQuit()
	a.emitEvent("request-app-quit", map[string]interface{}{
		"source": source,
	})
	if !a.frontendQuitRequested.CompareAndSwap(false, true) {
		return
	}
	go func() {
		time.Sleep(3500 * time.Millisecond)
		if a.isShuttingDown() {
			return
		}
		a.writeLog("Frontend did not acknowledge quit request; forcing shutdown")
		a.PrepareQuit()
		forcePlatformFrontendExit()
		a.FinalizeQuit()
	}()
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
	telegramStatus := a.TelegramProxyStatus()
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
	// Show the cleanup notice when this VPN session started the Telegram sidecar
	// and dropo either opened the tg://proxy link before or Telegram is actively
	// connected to the local port. ActiveConnection can be false by the time the
	// app is closing, so the persisted injected flag is intentionally enough.
	telegramStatus.ShowNotice = a.tgProxyStartedSession.Load() && (telegramStatus.ActiveConnection || telegramStatus.Injected)
	if telegramStatus.ShowNotice {
		telegramStatus.RecommendRemove = true
	}
	return telegramStatus
}

// QuitWithTelegramNotice is the tray "Выход" path: stop all background processes,
// then — only if the Telegram sidecar ran this session — bring the window up and
// show the cleanup notice. Otherwise exit immediately.
func (a *App) QuitWithTelegramNotice() {
	if a.isShuttingDown() {
		os.Exit(0)
	}
	status := a.PrepareQuit()
	if status.ShowNotice {
		a.ShowWindow()
		// Give the (possibly hidden) window a moment to come up before the event,
		// otherwise the notice can be missed.
		time.Sleep(350 * time.Millisecond)
		a.emitEvent("show-telegram-exit-notice", status)
		return
	}
	a.FinalizeQuit()
}

// FinalizeQuit performs the actual process exit. Call after PrepareQuit (and
// after any closing notice has been dismissed).
func (a *App) FinalizeQuit() {
	removeBridgeToken(a.dataPath)
	a.closeLogFile()
	go func() {
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}()
	os.Exit(0)
}

// ShowWindow shows the application window
func (a *App) ShowWindow() {
	showPlatformWindow()
	a.SetWindowVisible(true)
}

// ShowAbout shows about dialog
func (a *App) ShowAbout() {
	a.emitEvent("show-about", GetVersionInfo())
}

// OpenExternalLink opens a trusted http(s)/tg link with the OS default handler.
func (a *App) OpenExternalLink(link string) map[string]interface{} {
	if link == "" {
		return map[string]interface{}{"success": false, "error": "empty link"}
	}
	if !(strings.HasPrefix(link, "https://") || strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "tg://")) {
		return map[string]interface{}{"success": false, "error": "unsupported link scheme"}
	}
	if err := openExternalURL(link); err != nil {
		return map[string]interface{}{"success": false, "error": err.Error()}
	}
	return map[string]interface{}{"success": true}
}

// HideWindow hides the application window
func (a *App) HideWindow() {
	if !isSystrayReady() {
		a.writeLog("HideWindow skipped: systray is unavailable")
		return
	}
	hidePlatformWindow()
	a.SetWindowVisible(false)
	a.refreshTrayIconForCurrentState()
}

// EnsureTray starts tray integration when the Flutter shell attaches to an
// already-running core that was started without the tray (for example by a
// smoke test or a previous broken UI run).
func (a *App) EnsureTray() map[string]interface{} {
	if !isSystrayReady() {
		ensurePlatformTray()
	}
	a.refreshTrayIconForCurrentState()
	return map[string]interface{}{
		"success": true,
		"ready":   isSystrayReady(),
	}
}

func (a *App) refreshTrayIconForCurrentState() {
	if a == nil {
		return
	}
	a.mu.Lock()
	running := a.isRunning
	hasError := a.hasError.Load()
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

// OpenLogs opens the logs folder in file explorer and selects the active log
// file on Windows.
func (a *App) OpenLogs() map[string]interface{} {
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

	selected := a.logPath
	if selected == "" || !fileExists(selected) {
		selected = latestLogFile(logDir)
	}
	if runtime.GOOS == "windows" && selected != "" && fileExists(selected) {
		cmd := exec.Command("explorer", "/select,"+selected)
		if err := cmd.Start(); err != nil {
			a.writeLog("[Logs] could not open selected log: " + err.Error())
			openFolder(logDir)
			return map[string]interface{}{"success": false, "error": err.Error(), "path": logDir}
		}
		return map[string]interface{}{"success": true, "path": selected}
	}

	openFolder(logDir)
	return map[string]interface{}{"success": true, "path": logDir}
}

func latestLogFile(logDir string) string {
	entries, err := os.ReadDir(logDir)
	if err != nil {
		return ""
	}
	var latest string
	var latestTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if latest == "" || info.ModTime().After(latestTime) {
			latest = filepath.Join(logDir, entry.Name())
			latestTime = info.ModTime()
		}
	}
	return latest
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

	singboxPath := a.singBoxPathSnapshot()
	if singboxPath != "" && fileExists(singboxPath) {
		result["found"] = true
		result["path"] = singboxPath
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
