package main

import (
	"fmt"
	"path/filepath"
	"runtime"
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
	switch NormalizeNetworkMode(mode) {
	case NetworkModeDeepWindows:
		return "Deep Windows"
	case NetworkModeCompatTun:
		return "Compatibility TUN"
	default:
		return "Auto"
	}
}

func networkModeDescription(mode NetworkMode) string {
	switch NormalizeNetworkMode(mode) {
	case NetworkModeDeepWindows:
		return "Deep Windows uses the bundled zapret/winws + WinDivert engine as the primary Windows traffic layer."
	case NetworkModeCompatTun:
		return "Compatibility TUN is a fallback path used only when the Deep Windows engine cannot be activated."
	default:
		return "Auto selects Deep Windows when winws/WinDivert are available and falls back to Compatibility TUN only on error."
	}
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
		Active:      NetworkModeCompatTun,
		Label:       networkModeLabel(NetworkModeCompatTun),
		Description: networkModeDescription(NetworkModeCompatTun),
	}

	ready, helperPath, reason := a.deepWindowsEngineReady()
	status.HelperPath = helperPath
	status.DriverReady = ready
	if ready {
		status.Active = NetworkModeDeepWindows
		status.Label = networkModeLabel(NetworkModeDeepWindows)
		status.Description = networkModeDescription(NetworkModeDeepWindows)
		if requested == NetworkModeCompatTun {
			status.FallbackReason = "Compatibility TUN is fallback-only while Deep Windows is available"
		}
		return status
	}

	if requested != NetworkModeCompatTun {
		status.Fallback = true
		status.FallbackReason = reason
	}
	return status
}

func (a *App) deepWindowsEngineReady() (bool, string, string) {
	helperPath := ""
	if a != nil && a.basePath != "" {
		helperPath = filepath.Join(a.basePath, "bin", ZapretProcessName)
	}
	if runtime.GOOS != "windows" {
		return false, helperPath, fmt.Sprintf("Deep Windows is Windows-only; active mode is %s", networkModeLabel(NetworkModeCompatTun))
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
	return false, helperPath, fmt.Sprintf("Deep Windows engine is unavailable: missing %s; active mode is %s",
		strings.Join(missing, ", "), networkModeLabel(NetworkModeCompatTun))
}

func (a *App) missingDeepWindowsFiles() []string {
	if a == nil || a.basePath == "" {
		return []string{"app base path"}
	}
	required := []string{
		filepath.Join(a.basePath, "bin", ZapretProcessName),
		filepath.Join(a.basePath, "bin", "WinDivert.dll"),
		filepath.Join(a.basePath, "bin", "WinDivert64.sys"),
		filepath.Join(a.basePath, "bin", "cygwin1.dll"),
	}
	if len(DefaultZapretTransparentStrategies) > 0 {
		for _, file := range DefaultZapretTransparentStrategies[0].RequiredFiles {
			required = append(required, filepath.Join(a.basePath, "bin", file))
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

func (a *App) logDeepWindowsError(message string) {
	if a == nil {
		return
	}
	status := a.currentNetworkModeStatus()
	if status.Requested == NetworkModeCompatTun {
		return
	}
	a.writeLog(fmt.Sprintf("[NetworkMode] ERROR: Deep Windows engine failed: %s; fallback/compat routes will be used where available", message))
}

func (a *App) shouldUseDeepWindowsPrimary(configPath string, status NetworkModeStatus) (bool, string) {
	if runtime.GOOS != "windows" {
		return false, "Deep Windows transparent engine is Windows-only"
	}
	if status.Active != NetworkModeDeepWindows {
		return false, fmt.Sprintf("active network mode is %s", status.Active)
	}
	if a == nil || a.zapret == nil || !a.zapret.IsInstalled() {
		return false, "zapret/winws transparent engine is not available"
	}
	if a.storage == nil {
		return false, "storage is not initialized"
	}

	if _, err := readJSONConfig(configPath); err != nil {
		return false, fmt.Sprintf("failed to read active config: %v", err)
	}
	plan := a.buildDeepWindowsRoutePlan(configPath)
	if plan.RequiresRedirector {
		return false, "Deep Windows plan requires proxy redirector; using Compatibility TUN so subscription/proxy routes are enforced"
	}

	return true, "Deep Windows is primary; transparent routes do not require sing-box TUN"
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
			{"value": string(NetworkModeAuto), "label": "Auto", "description": networkModeDescription(NetworkModeAuto)},
			{"value": string(NetworkModeDeepWindows), "label": networkModeLabel(NetworkModeDeepWindows), "description": networkModeDescription(NetworkModeDeepWindows)},
			{"value": string(NetworkModeCompatTun), "label": networkModeLabel(NetworkModeCompatTun), "description": networkModeDescription(NetworkModeCompatTun)},
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
	if NormalizeNetworkMode(networkMode) != networkMode {
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
	settings.NetworkMode = networkMode
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
