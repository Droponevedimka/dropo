package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

// NetworkModeStatus describes the requested network engine and the engine that
// is actually active in this build.
type NetworkModeStatus struct {
	Requested      NetworkMode `json:"requested"`
	Active         NetworkMode `json:"active"`
	Fallback       bool        `json:"fallback"`
	FallbackReason string      `json:"fallbackReason,omitempty"`
	Label          string      `json:"label"`
	Description    string      `json:"description"`
	DriverReady    bool        `json:"driverReady"`
	HelperPath     string      `json:"helperPath,omitempty"`
}

func networkModeLabel(mode NetworkMode) string {
	return "Windows Unified"
}

func networkModeDescription(mode NetworkMode) string {
	return "A single Windows runtime: sing-box TUN routes traffic and one composed zapret2/winws2 process keeps an independent first-working strategy for each blocked service."
}

func networkModeStatusPayload(status NetworkModeStatus) map[string]interface{} {
	return map[string]interface{}{
		"requested":      string(status.Requested),
		"active":         string(status.Active),
		"fallback":       status.Fallback,
		"fallbackReason": status.FallbackReason,
		"label":          status.Label,
		"description":    status.Description,
		"driverReady":    status.DriverReady,
		"helperPath":     status.HelperPath,
	}
}

func (a *App) currentNetworkModeStatus() NetworkModeStatus {
	requested := DefaultNetworkMode
	if a.storage != nil {
		settings := a.storage.GetAppSettings()
		requested = settings.NetworkMode
	}
	return a.resolveNetworkMode(requested)
}

func (a *App) resolveNetworkMode(requested NetworkMode) NetworkModeStatus {
	requested = NormalizeNetworkMode(requested)
	status := NetworkModeStatus{
		Requested:   requested,
		Active:      NetworkModeWindowsUnified,
		Label:       networkModeLabel(NetworkModeWindowsUnified),
		Description: networkModeDescription(NetworkModeWindowsUnified),
	}

	ready, helperPath, reason := a.deepWindowsEngineReady()
	status.HelperPath = helperPath
	status.DriverReady = ready
	if !ready {
		status.Fallback = true
		status.FallbackReason = reason
	}
	return status
}

func (a *App) deepWindowsEngineReady() (bool, string, string) {
	helperPath := ""
	if a != nil && a.basePath != "" {
		helperPath = filepath.Join(a.binDir(), ZapretProcessName)
	}
	if !interceptionEngineSupported() {
		return false, helperPath, fmt.Sprintf("transparent interception engine (%s) is unavailable on this platform", interceptionEngineKind())
	}

	if a != nil && a.zapret != nil {
		strategies := a.zapret.AvailableStrategies()
		if len(strategies) > 0 {
			return true, a.zapret.strategyPath(strategies[0]), ""
		}
	}

	missing := a.missingDeepWindowsFiles()
	if len(missing) == 0 {
		return true, helperPath, ""
	}
	return false, helperPath, fmt.Sprintf("Windows Unified runtime is incomplete: missing %s", strings.Join(missing, ", "))
}

func (a *App) missingDeepWindowsFiles() []string {
	if a == nil || a.basePath == "" {
		return []string{"app base path"}
	}
	required := []string{
		filepath.Join(a.binDir(), ZapretProcessName),
		filepath.Join(a.binDir(), "WinDivert.dll"),
		filepath.Join(a.binDir(), "WinDivert64.sys"),
		filepath.Join(a.binDir(), "cygwin1.dll"),
	}
	if len(DefaultZapretTransparentStrategies) > 0 {
		for _, file := range DefaultZapretTransparentStrategies[0].RequiredFiles {
			required = append(required, filepath.Join(a.binDir(), file))
		}
	}
	missing := make([]string, 0)
	for _, path := range required {
		if !fileExists(path) {
			missing = append(missing, filepath.Base(path))
		}
	}
	return missing
}

func (a *App) shouldUseDeepWindowsPrimary(configPath string, status NetworkModeStatus) (bool, string) {
	return false, "Windows Unified always uses sing-box TUN routing with one composed winws2 engine"
}

// GetNetworkMode returns the current network engine state and supported modes.
func (a *App) GetNetworkMode() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	status := a.currentNetworkModeStatus()
	return map[string]interface{}{
		"success": true,
		"status":  networkModeStatusPayload(status),
		"modes": []map[string]string{
			{"value": string(NetworkModeWindowsUnified), "label": networkModeLabel(NetworkModeWindowsUnified), "description": networkModeDescription(NetworkModeWindowsUnified)},
		},
	}
}

// SetNetworkMode stores the requested network engine mode. It is intentionally
// blocked while the VPN is active because changing the engine requires a restart.
func (a *App) SetNetworkMode(mode string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	networkMode := NetworkMode(mode)
	if networkMode != NetworkModeWindowsUnified && networkMode != NetworkModeAuto && networkMode != NetworkModeDeepWindows && networkMode != NetworkModeCompatTun {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Неизвестный сетевой режим: %s", mode),
		}
	}

	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()
	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить сетевой режим пока VPN активен. Сначала отключите VPN.",
		}
	}

	settings := a.storage.GetAppSettings()
	settings.NetworkMode = NetworkModeWindowsUnified
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	status := a.resolveNetworkMode(networkMode)
	a.writeLog(fmt.Sprintf("[NetworkMode] requested=%s active=%s fallback=%t reason=%s", status.Requested, status.Active, status.Fallback, status.FallbackReason))
	return map[string]interface{}{
		"success": true,
		"message": "Сетевой режим изменен",
		"status":  networkModeStatusPayload(status),
	}
}
