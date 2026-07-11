package main

// VPN control methods for dropo.
// This file contains VPN start/stop/toggle operations

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// getActiveConfigPath writes active config to file and returns the path.
// This is needed because sing-box requires a file path, but we store configs in settings.json.
func (a *App) getActiveConfigPath() (string, error) {
	if a.storage == nil {
		return "", fmt.Errorf("storage not initialized")
	}
	return a.storage.WriteActiveConfigToFile()
}

// GetStatus returns current VPN status
func (a *App) GetStatus() map[string]interface{} {
	// Wait for initialization if not completed
	a.waitForInit()

	a.mu.Lock()
	defer a.mu.Unlock()

	configPath := ""
	hasConfig := false
	if a.storage != nil {
		configPath = a.storage.ActiveConfigFilePath()
		hasConfig = a.storage.ActiveProfileHasConfig()
		if a.isRunning && fileExists(configPath) {
			hasConfig = true
		}
	}
	networkMode := a.currentNetworkModeStatus()

	return map[string]interface{}{
		"running":                   a.isRunning,
		"connected":                 a.isRunning,
		"connecting":                a.isStarting,
		"hasError":                  a.hasError.Load(),
		"configPath":                configPath,
		"singboxPath":               a.singboxPath,
		"configExists":              hasConfig,
		"singboxExists":             a.singboxPath != "" && fileExists(a.singboxPath),
		"logPath":                   a.logPath,
		"tempLogPath":               a.tempLogPath,
		"networkMode":               string(networkMode.Active),
		"requestedNetworkMode":      string(networkMode.Requested),
		"networkModeFallback":       networkMode.Fallback,
		"networkModeFallbackReason": networkMode.FallbackReason,
		"networkModeLabel":          networkMode.Label,
		"networkModeDescription":    networkMode.Description,
	}
}

// Start starts VPN
func (a *App) Start() map[string]interface{} {
	// Wait for initialization
	a.waitForInit()

	busyID := a.beginBusyTagged("vpn-connect", "Подготовка подключения...")
	defer a.endBusy(busyID)

	a.updateBusy(busyID, "Проверяем состояние приложения...")
	// Heavy binaries (bin/) ship as a separate release asset and are fetched on
	// first run. Refuse to start until they are present (see docs/UPDATE.md).
	if st := a.DependenciesStatus(); st.Managed {
		if !st.Ready {
			return map[string]interface{}{
				"success": false,
				"error":   "Компоненты ещё не загружены. Подключитесь к интернету и дождитесь первичной загрузки компонентов.",
			}
		}
		a.refreshSingBoxPath()
	}
	singboxPath := a.singBoxPathSnapshot()
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "VPN уже запущен",
		}
	}
	if a.isStarting {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Подключение уже выполняется",
		}
	}
	a.isStarting = true

	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.isStarting = false
		a.mu.Unlock()
	}()

	// Fresh VPN session: allow exactly one background strategy search per
	// service again (see requestRouteStrategyMaintenance / the maintenance loop).
	a.resetRouteStrategySession()
	a.tgProxyPromptedSession.Store(false)

	startupSucceeded := false
	defer func() {
		if startupSucceeded {
			return
		}
		a.stopNativeWireGuardTunnels()
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.cleanupDropoRuntimeResidue("failed start")
	}()

	if startupRouteProbeDiscoveryEnabled && a.isRouteProbeDiscoveryRunning() {
		a.updateBusy(busyID, "Завершаем фоновый подбор методов...")
		a.writeLog("[RouteProbe] VPN start is waiting for background discovery to finish")
		if !a.waitForRouteProbeDiscovery(30 * time.Second) {
			a.hasError.Store(true)
			UpdateTrayIcon("error")
			return map[string]interface{}{
				"success": false,
				"error":   "Идет фоновый подбор бесплатных методов. Повторите запуск через несколько секунд.",
			}
		}
	}

	a.cleanupDropoRuntimeResidue("before start")

	a.updateBusy(busyID, "Готовим активный конфиг...")
	if err := a.autoUpdateSubscriptionBeforeStart(busyID); err != nil {
		a.writeLog(fmt.Sprintf("[Subscription] auto-update before start failed: %v", err))
		a.AddToLogBuffer(fmt.Sprintf("Автообновление подписки не удалось: %v", err))
	}
	if err := a.ensureActiveConfigForStart(); err != nil {
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Не удалось подготовить конфиг: %v", err),
		}
	}

	a.updateBusy(busyID, "Сохраняем рабочий конфиг...")
	configPath, err := a.getActiveConfigPath()
	if err != nil || configPath == "" {
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		return map[string]interface{}{
			"success": false,
			"error":   "Конфиг не найден. Проверьте настройки бесплатного доступа или подписки.",
		}
	}
	if err := a.requireRouteSourceWhenFreeMethodsDisabled(configPath); err != nil {
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}
	networkMode := a.currentNetworkModeStatus()
	a.writeLog(fmt.Sprintf("[NetworkMode] requested=%s active=%s fallback=%t reason=%s", networkMode.Requested, networkMode.Active, networkMode.Fallback, networkMode.FallbackReason))
	if networkMode.Fallback && a.ctx != nil {
		a.emitEvent("network-mode-fallback", networkModeStatusPayload(networkMode))
	}

	// Open log file
	if err := a.openLogFile(); err != nil {
		a.writeLog(fmt.Sprintf("Warning: could not open log file: %v", err))
	}

	// Get log level from settings and update config file. Default to info: trace
	// multiplies sing-box log I/O and logOutput work for no benefit unless the
	// user is actively diagnosing. See review.md §2.3.
	logLevel := string(LogLevelInfo)
	if a.storage != nil {
		settings := a.storage.GetAppSettings()
		if settings.LogLevel != "" {
			logLevel = string(settings.LogLevel)
		}
	}

	// Update log level in config file
	if err := a.updateConfigLogLevel(configPath, logLevel); err != nil {
		a.writeLog(fmt.Sprintf("Warning: could not update log level in config: %v", err))
	}

	a.writeLog(fmt.Sprintf("Network config: %s", configPath))
	a.writeLog(fmt.Sprintf("Log level: %s", logLevel))

	// Start local helpers before sing-box so local outbounds are already
	// reachable when sing-box performs its first group health checks.
	a.updateBusy(busyID, "Запускаем бесплатные методы обхода...")
	activeFreeAccessTags := a.startFreeAccessForConfig(configPath)
	a.updateBusy(busyID, "Оставляем в конфиге только активные методы...")
	if err := a.filterActiveFreeAccessOutbounds(configPath, activeFreeAccessTags); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to filter active free-access outbounds: %v", err))
	}
	a.updateBusy(busyID, "Применяем сохранённые стратегии бесплатного доступа...")
	if changed, err := a.applyStoredFreeAccessStrategiesToConfig(configPath, activeFreeAccessTags); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to apply stored strategies: %v", err))
	} else if changed {
		a.writeLog("[FreeAccess] stored/default strategies applied before start")
	} else {
		a.writeLog("[FreeAccess] stored/default strategies did not require config changes")
	}
	a.updateBusy(busyID, "Сравниваем бесплатные методы с VPN/VLESS подпиской...")
	cacheApplied, cacheFresh := false, false
	if hasVPNCandidates, err := configHasVPNProbeCandidates(configPath); err != nil {
		a.writeLog(fmt.Sprintf("[RouteProbe] failed to inspect VPN candidates before start: %v", err))
	} else if startupVPNRouteComparisonEnabled && hasVPNCandidates {
		a.writeLog("[RouteProbe] VPN/VLESS candidates found; comparing them with free strategies before start")
		report, err := a.runRouteProbeAndApply(configPath, activeFreeAccessTags, "startup vpn comparison")
		if err != nil {
			a.writeLog(fmt.Sprintf("[RouteProbe] startup VPN comparison failed: %v", err))
		} else {
			cacheApplied = true
			cacheFresh = true
			if err := a.saveRouteProbeCache(report); err != nil {
				a.writeLog(fmt.Sprintf("[RouteProbe] failed to save startup comparison cache: %v", err))
			}
			a.writeLog(fmt.Sprintf("[RouteProbe] startup VPN comparison applied: %d/%d service(s) have a route",
				routeProbeSuccessCount(report.Services), len(report.Services)))
		}
	} else {
		a.writeLog("[RouteProbe] startup VPN comparison skipped: no VPN/VLESS subscription candidates")
	}
	if startupRouteProbeCacheApplyEnabled && !cacheApplied {
		a.updateBusy(busyID, "Применяем сохраненный подбор маршрутов...")
		var cacheErr error
		cacheApplied, cacheFresh, cacheErr = a.applyCachedRouteProbeToConfig(configPath, true, activeFreeAccessTags)
		if cacheErr != nil {
			a.writeLog(fmt.Sprintf("[RouteProbe] failed to apply discovery cache: %v", cacheErr))
		} else if cacheApplied {
			if cacheFresh {
				a.writeLog("[RouteProbe] fresh discovery cache applied before start")
			} else {
				a.writeLog("[RouteProbe] stale discovery cache applied before start")
			}
		} else {
			a.writeLog("[RouteProbe] no discovery cache available; starting with resilient outbound groups")
		}
	}
	a.updateBusy(busyID, "Проверяем, что выбранные бесплатные методы всё ещё активны...")
	liveFreeAccessTags := liveFreeAccessProxyTags(activeFreeAccessTags)
	if err := a.filterActiveFreeAccessOutbounds(configPath, liveFreeAccessTags); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to re-check live free-access outbounds: %v", err))
	}
	if cacheApplied {
		if err := a.requireWorkingRouteAfterProbe(configPath); err != nil {
			a.hasError.Store(true)
			UpdateTrayIcon("error")
			a.writeLog(fmt.Sprintf("[RouteProbe] startup blocked: %v", err))
			return map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}
		}
	} else {
		a.writeLog("[RouteProbe] startup route gate skipped because discovery cache is not available yet")
	}
	a.logActiveConfigDiagnostics(configPath)

	if useDeepWindows, reason := a.shouldUseDeepWindowsPrimary(configPath, networkMode); useDeepWindows {
		a.writeLog(fmt.Sprintf("[NetworkMode] Deep Windows primary mode: %s", reason))
		deepPlan := a.buildDeepWindowsRoutePlan(configPath)
		a.logDeepWindowsRoutePlan(deepPlan)
		a.updateBusy(busyID, "Starting Deep Windows transparent engine...")
		if err := a.ensureDefaultZapretForDeepWindowsStart(deepPlan, busyID); err != nil {
			a.hasError.Store(true)
			UpdateTrayIcon("error")
			a.writeLog(fmt.Sprintf("[NetworkMode] Deep Windows startup failed: %v", err))
			return map[string]interface{}{
				"success": false,
				"error":   err.Error(),
			}
		}
		if a.zapret != nil {
			activeTransparent := a.zapret.ActiveTag()
			if activeTransparent == "" {
				activeTransparent = "not selected"
			}
			a.writeLog(fmt.Sprintf("[NetworkMode] Deep Windows transparent method ready: %s", activeTransparent))
			a.zapret.logWinDivertStatus("transparent-only ready")
			a.writeLog("[NetworkMode] Deep Windows packet debug is disabled by default; set DROPO_ZAPRET_PACKET_DEBUG=1 only for short local diagnostics")
		}

		proxyFallbackStarted := false
		var proxyFallbackErr error
		if deepPlan.RequiresSingBoxProxy {
			a.updateBusy(busyID, "Запускаем VPN fallback как локальный proxy без TUN...")
			a.writeLog(fmt.Sprintf("[NetworkMode] Deep Windows route plan requires local sing-box proxy: %s", strings.Join(deepPlan.ProxyReasons, "; ")))
			if singboxPath == "" || !fileExists(singboxPath) {
				proxyFallbackErr = fmt.Errorf("sing-box is not available")
				a.writeLog(fmt.Sprintf("[NetworkMode] proxy fallback skipped: %v", proxyFallbackErr))
			} else if err := a.startXrayBridge(); err != nil {
				proxyFallbackErr = fmt.Errorf("failed to start Xray bridge: %w", err)
				a.writeLog(fmt.Sprintf("[NetworkMode] proxy fallback skipped: %v", proxyFallbackErr))
			} else if proxyConfigPath, err := a.writeDeepWindowsProxyFallbackConfig(configPath); err != nil {
				proxyFallbackErr = fmt.Errorf("failed to write proxy-only config: %w", err)
				a.writeLog(fmt.Sprintf("[NetworkMode] proxy fallback skipped: %v", proxyFallbackErr))
			} else if err := a.startSingBoxProxyFallback(proxyConfigPath, logLevel); err != nil {
				proxyFallbackErr = fmt.Errorf("failed to start sing-box proxy-only mode: %w", err)
				a.writeLog(fmt.Sprintf("[NetworkMode] proxy fallback skipped: %v", proxyFallbackErr))
				a.stopXrayBridge()
			} else {
				proxyFallbackStarted = true
			}
		}
		if deepPlan.RequiresSingBoxProxy && !proxyFallbackStarted {
			message := "Deep Windows requires local VPN proxy endpoint, but it did not start"
			if proxyFallbackErr != nil {
				message = fmt.Sprintf("%s: %v", message, proxyFallbackErr)
			}
			a.mu.Lock()
			a.hasError.Store(true)
			a.mu.Unlock()
			UpdateTrayIcon("error")
			a.writeLog(fmt.Sprintf("[NetworkMode] Deep Windows startup failed: %s", message))
			return map[string]interface{}{
				"success": false,
				"error":   message,
			}
		}

		a.mu.Lock()
		if !proxyFallbackStarted {
			a.cmd = nil
			a.cmdDone = nil
		}
		a.isRunning = true
		a.hasError.Store(false)
		a.stoppedManually = false
		a.mu.Unlock()
		UpdateTrayIcon("connected")
		a.setRestoreVPNOnStartup(true)
		if proxyFallbackStarted {
			a.writeLog("VPN started successfully in Deep Windows mode with local proxy endpoint")
			a.AddToLogBuffer("VPN запущен: Deep Windows + proxy endpoint")
		} else {
			a.writeLog("VPN started successfully in Deep Windows mode")
			a.AddToLogBuffer("VPN запущен: Deep Windows")
		}

		if a.nativeWG != nil && a.nativeWG.IsInstalled() {
			a.updateBusy(busyID, "Запускаем рабочие WireGuard-сети...")
			a.startNativeWireGuardTunnels()
		}
		if a.trafficStats != nil {
			a.trafficStats.StartSession()
			a.startTrafficStatsPolling()
		}

		startupSucceeded = true
		return map[string]interface{}{
			"success": true,
			"running": true,
		}
	} else {
		a.writeLog(fmt.Sprintf("[NetworkMode] sing-box/TUN path required: %s", reason))
	}

	a.updateBusy(busyID, "Проверяем sing-box...")
	if singboxPath == "" || !fileExists(singboxPath) {
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		return map[string]interface{}{
			"success": false,
			"error":   "sing-box не найден. Он нужен для подписок/VPN fallback или Compatibility TUN.",
		}
	}

	a.updateBusy(busyID, "Запускаем Xray bridge...")
	if err := a.startXrayBridge(); err != nil {
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		a.writeLog(fmt.Sprintf("ERROR: Failed to start Xray bridge: %v", err))
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка запуска Xray bridge для xhttp: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Starting sing-box: %s", singboxPath))
	a.writeLog(fmt.Sprintf("Config: %s", configPath))
	a.writeLog(fmt.Sprintf("Log level: %s", logLevel))

	// Start sing-box with config for current profile
	a.updateBusy(busyID, "Запускаем sing-box...")
	cmd := exec.Command(singboxPath, "run", "-c", configPath)

	// WireGuard is now handled by Native WireGuard Manager, not sing-box
	// No need for ENABLE_DEPRECATED_WIREGUARD_OUTBOUND

	// Get stdout and stderr for logging
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	configureBackgroundCommand(cmd)

	// Set working directory to resources folder
	if a.storage != nil {
		cmd.Dir = a.storage.GetResourcesPath()
	} else {
		cmd.Dir = a.basePath
	}

	if err := cmd.Start(); err != nil {
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.hasError.Store(true)
		UpdateTrayIcon("error")
		a.writeLog(fmt.Sprintf("ERROR: Failed to start: %v", err))
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка запуска: %v", err),
		}
	}

	attachManagedCmdToJob(cmd, "sing-box", a.writeLog)

	cmdDone := make(chan error, 1)
	a.mu.Lock()
	a.cmd = cmd
	a.cmdDone = cmdDone
	a.isRunning = true
	a.hasError.Store(false)
	a.mu.Unlock()
	UpdateTrayIcon("connected")
	a.setRestoreVPNOnStartup(true)
	a.writeLog("VPN started successfully")
	a.AddToLogBuffer("VPN запущен")

	// Start Native WireGuard tunnels (internal/corporate VPNs)
	if a.nativeWG != nil && a.nativeWG.IsInstalled() {
		a.updateBusy(busyID, "Запускаем рабочие WireGuard-сети...")
		a.startNativeWireGuardTunnels()
	}

	// Start tracking traffic statistics
	if a.trafficStats != nil {
		a.trafficStats.StartSession()
		a.startTrafficStatsPolling()
	}

	// Hybrid: run the winws desync engine ALONGSIDE sing-box TUN. sing-box
	// routes per-service (urltest direct↔VPN, everything else direct); winws
	// desyncs the 'direct' path so blocked desync-solvable services work for
	// free and only the rest fall to the VPN. winws.exe traffic is process-routed
	// to direct in the config, so it does not loop through the TUN.
	if err := a.startComposedTransparentEngine(); err != nil {
		a.writeLog(fmt.Sprintf("[NetworkMode] hybrid winws engine failed to start: %v", err))
	}

	// Log output in goroutines
	go a.logOutput(stdout, "sing-box/out")
	go a.logOutput(stderr, "sing-box/log")

	// Monitor process in goroutine
	go func(cmd *exec.Cmd, done chan error) {
		err := cmd.Wait()
		done <- err
		close(done)
		a.mu.Lock()
		wasStoppedManually := a.stoppedManually
		isCurrentProcess := a.cmd == cmd
		if isCurrentProcess {
			a.isRunning = false
			a.stoppedManually = false
			a.cmd = nil
			a.cmdDone = nil
		}

		// End traffic session
		if a.trafficStats != nil {
			a.trafficStats.EndSession()
			a.trafficStats.Save()
		}

		// ALWAYS stop WireGuard tunnels when VPN process exits
		// This prevents orphaned tunnels that block user's native WireGuard
		a.mu.Unlock() // Unlock before calling stopNativeWireGuardTunnels to avoid deadlock
		a.stopNativeWireGuardTunnels()
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.cleanupDropoRuntimeResidue("process exit")
		a.mu.Lock()

		if !isCurrentProcess {
			a.closeLogFile()
			a.mu.Unlock()
			return
		}

		if wasStoppedManually {
			// Manual stop - not an error
			a.writeLog("VPN stopped by user")
			a.AddToLogBuffer("VPN остановлен пользователем")
			UpdateTrayIcon("disconnected")
		} else if err != nil {
			a.hasError.Store(true)
			a.writeLog(fmt.Sprintf("VPN process exited with error: %v", err))
			a.AddToLogBuffer(fmt.Sprintf("VPN завершился с ошибкой: %v", err))
			UpdateTrayIcon("error")
		} else {
			a.writeLog("VPN process exited normally")
			a.AddToLogBuffer("VPN завершил работу")
			UpdateTrayIcon("disconnected")
		}
		a.closeLogFile()
		a.mu.Unlock()
		// Notify frontend about status change
		if a.ctx != nil {
			a.emitEvent("vpn-status-changed", false)
		}
	}(cmd, cmdDone)
	startupSucceeded = true

	a.updateBusy(busyID, "Подключение запущено")
	return map[string]interface{}{
		"success": true,
		"running": true,
	}
}

func (a *App) writeDeepWindowsProxyFallbackConfig(activeConfigPath string) (string, error) {
	config, err := readJSONConfig(activeConfigPath)
	if err != nil {
		return "", err
	}

	removedTun := removeTunInbounds(config)
	if removedTun == 0 {
		a.writeLog("[NetworkMode] proxy-only fallback config had no TUN inbound to remove")
	} else {
		a.writeLog(fmt.Sprintf("[NetworkMode] proxy-only fallback config removed %d TUN inbound(s)", removedTun))
	}
	if !enableMixedInboundLocalProxy(config, true) {
		return "", fmt.Errorf("mixed inbound is not available for proxy fallback")
	}
	if a.pruneProxyFallbackVPNCandidates(config) {
		a.writeLog("[NetworkMode] proxy-only fallback pruned inactive VPN candidates using route probe results")
	}

	resourcesPath := a.basePath
	if a.storage != nil {
		resourcesPath = a.storage.GetResourcesPath()
	}
	if resourcesPath == "" {
		return "", fmt.Errorf("resources path is not available")
	}
	if err := os.MkdirAll(resourcesPath, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(resourcesPath, "deep_windows_proxy_config.json")
	if err := writeJSONConfig(path, config); err != nil {
		return "", err
	}
	a.writeLog(fmt.Sprintf("[NetworkMode] proxy-only fallback config written: %s", path))
	return path, nil
}

func removeTunInbounds(config map[string]interface{}) int {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return 0
	}
	filtered := make([]interface{}, 0, len(inbounds))
	removed := 0
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if ok && inboundMap["type"] == "tun" {
			removed++
			continue
		}
		filtered = append(filtered, inbound)
	}
	config["inbounds"] = filtered
	return removed
}

func enableMixedInboundLocalProxy(config map[string]interface{}, setSystemProxy bool) bool {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return false
	}
	enabled := false
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}
		inboundMap["listen"] = "127.0.0.1"
		inboundMap["listen_port"] = float64(defaultDropoMixedProxyPort)
		inboundMap["set_system_proxy"] = setSystemProxy
		enabled = true
	}
	return enabled
}

func (a *App) pruneProxyFallbackVPNCandidates(config map[string]interface{}) bool {
	workingTags := a.workingVPNTagsFromRouteProbe()
	if len(workingTags) == 0 {
		return false
	}

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return false
	}
	available := map[string]bool{}
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		if tag != "" {
			available[tag] = true
		}
	}

	keep := make([]string, 0, len(workingTags))
	for _, tag := range workingTags {
		if available[tag] {
			keep = append(keep, tag)
		}
	}
	keep = uniqueStrings(keep)
	if len(keep) == 0 {
		return false
	}

	changed := false
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		switch tag {
		case "auto-select":
			if setVPNFallbackAutoSelectCandidates(outboundMap, keep) {
				changed = true
			}
		case "proxy":
			selectorCandidates := append([]string{"auto-select"}, keep...)
			selectorCandidates = append(selectorCandidates, "direct")
			if !stringSetEqual(interfaceStringSlice(outboundMap["outbounds"]), selectorCandidates) {
				outboundMap["outbounds"] = selectorCandidates
				changed = true
			}
			if outboundMap["default"] != "auto-select" {
				outboundMap["default"] = "auto-select"
				changed = true
			}
		}
	}
	if changed {
		a.writeLog(fmt.Sprintf("[NetworkMode] proxy-only fallback active VPN candidates: %s", strings.Join(keep, ",")))
	}
	return changed
}

func (a *App) workingVPNTagsFromRouteProbe() []string {
	results := a.routeProbeResultsSnapshot()
	if len(results) == 0 {
		cache, err := a.loadRouteProbeCache()
		if err == nil && routeProbeCacheFresh(cache) {
			results = cache.Services
		}
	}
	tags := make([]string, 0)
	for _, result := range results {
		if result.Success && result.MethodKind == "vpn" && result.MethodTag != "" {
			tags = append(tags, result.MethodTag)
		}
	}
	return uniqueStrings(tags)
}

func setVPNFallbackAutoSelectCandidates(outboundMap map[string]interface{}, candidates []string) bool {
	if len(candidates) == 0 {
		return false
	}
	changed := false
	current := interfaceStringSlice(outboundMap["outbounds"])
	if !stringSetEqual(current, candidates) {
		outboundMap["outbounds"] = candidates
		changed = true
	}
	if len(candidates) == 1 {
		if outboundMap["type"] != "selector" {
			outboundMap["type"] = "selector"
			changed = true
		}
		if outboundMap["default"] != candidates[0] {
			outboundMap["default"] = candidates[0]
			changed = true
		}
		for _, key := range []string{"url", "interval", "tolerance", "interrupt_exist_connections"} {
			if _, ok := outboundMap[key]; ok {
				delete(outboundMap, key)
				changed = true
			}
		}
		return changed
	}

	if outboundMap["type"] != "urltest" {
		outboundMap["type"] = "urltest"
		changed = true
	}
	if _, ok := outboundMap["default"]; ok {
		delete(outboundMap, "default")
		changed = true
	}
	defaults := map[string]interface{}{
		"url":                         resilientGroupTestURL,
		"interval":                    "5m",
		"tolerance":                   50,
		"interrupt_exist_connections": false,
	}
	for key, value := range defaults {
		if outboundMap[key] != value {
			outboundMap[key] = value
			changed = true
		}
	}
	return changed
}

func stringSetEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, item := range a {
		counts[item]++
	}
	for _, item := range b {
		if counts[item] == 0 {
			return false
		}
		counts[item]--
	}
	return true
}

func (a *App) startSingBoxProxyFallback(configPath, logLevel string) error {
	singboxPath := a.singBoxPathSnapshot()
	a.writeLog(fmt.Sprintf("Starting sing-box proxy fallback: %s", singboxPath))
	a.writeLog(fmt.Sprintf("Proxy fallback config: %s", configPath))
	a.writeLog(fmt.Sprintf("Log level: %s", logLevel))

	cmd := exec.Command(singboxPath, "run", "-c", configPath)
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	configureBackgroundCommand(cmd)
	if a.storage != nil {
		cmd.Dir = a.storage.GetResourcesPath()
	} else {
		cmd.Dir = a.basePath
	}

	if err := cmd.Start(); err != nil {
		return err
	}
	attachManagedCmdToJob(cmd, "sing-box proxy fallback", a.writeLog)

	cmdDone := make(chan error, 1)
	a.mu.Lock()
	a.cmd = cmd
	a.cmdDone = cmdDone
	a.mu.Unlock()

	go a.logOutput(stdout, "sing-box/out")
	go a.logOutput(stderr, "sing-box/log")
	go a.monitorSingBoxProcess(cmd, cmdDone)
	return nil
}

func (a *App) monitorSingBoxProcess(cmd *exec.Cmd, done chan error) {
	err := cmd.Wait()
	done <- err
	close(done)
	a.mu.Lock()
	wasStoppedManually := a.stoppedManually
	isCurrentProcess := a.cmd == cmd
	if isCurrentProcess {
		a.isRunning = false
		a.stoppedManually = false
		a.cmd = nil
		a.cmdDone = nil
	}

	if a.trafficStats != nil {
		a.trafficStats.EndSession()
		a.trafficStats.Save()
	}

	a.mu.Unlock()
	a.stopNativeWireGuardTunnels()
	a.stopFreeAccess()
	a.stopXrayBridge()
	a.cleanupDropoRuntimeResidue("process exit")
	a.mu.Lock()

	if !isCurrentProcess {
		a.closeLogFile()
		a.mu.Unlock()
		return
	}

	if wasStoppedManually {
		a.writeLog("VPN stopped by user")
		a.AddToLogBuffer("VPN остановлен пользователем")
		UpdateTrayIcon("disconnected")
	} else if err != nil {
		a.hasError.Store(true)
		a.writeLog(fmt.Sprintf("VPN process exited with error: %v", err))
		a.AddToLogBuffer(fmt.Sprintf("VPN завершился с ошибкой: %v", err))
		UpdateTrayIcon("error")
	} else {
		a.writeLog("VPN process exited normally")
		a.AddToLogBuffer("VPN завершил работу")
		UpdateTrayIcon("disconnected")
	}
	a.closeLogFile()
	a.mu.Unlock()
	if a.ctx != nil {
		a.emitEvent("vpn-status-changed", false)
	}
}

// ensureActiveConfigForStart builds a usable sing-box config for a fresh
// profile before the first connection attempt. A subscription is optional:
// with Free Access enabled the generated config routes supported services
// through the local ByeDPI outbound and leaves everything else direct.
func (a *App) ensureActiveConfigForStart() error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}
	if a.configBuilder == nil {
		return fmt.Errorf("config builder not initialized")
	}

	profile, err := a.storage.GetActiveProfile()
	if err != nil || profile == nil {
		return fmt.Errorf("active profile not found")
	}

	// The subscription URL can live on the profile OR in global settings
	// (SetVPNSubscription / GenerateAndSaveConfig write settings.SubscriptionURL).
	// If the active profile has none, fall back to the global one so the VLESS
	// VPN is still built into the config and usable as the fallback outbound —
	// otherwise vpn_candidates=false and VPN-needing services are routed direct.
	subscriptionURL := profile.SubscriptionURL
	if subscriptionURL == "" {
		if settings, serr := a.storage.GetUserSettings(); serr == nil && settings.SubscriptionURL != "" {
			subscriptionURL = settings.SubscriptionURL
			a.writeLog("Active profile has no subscription URL; using the global subscription for the VPN fallback")
		}
	}

	config, err := a.storage.GetProfileConfig(profile.ID)
	if err == nil && len(config) > 0 {
		// Rebuild if the cached config predates Xray support, or if a subscription
		// is configured but the cached config has no VPN candidates (e.g. it was
		// built before the subscription was added → VPN would never be used).
		needsRebuild := subscriptionURL != "" && !profile.XrayConfigReady
		if !needsRebuild && subscriptionURL != "" {
			if path := a.storage.ActiveConfigFilePath(); path != "" {
				if hasVPN, verr := configHasVPNProbeCandidates(path); verr == nil && !hasVPN {
					a.writeLog("Active config has no VPN candidates but a subscription is configured; rebuilding so the VPN fallback works")
					needsRebuild = true
				}
			}
		}
		if needsRebuild {
			return a.configBuilder.BuildConfigForProfile(profile.ID, subscriptionURL, profile.WireGuardConfigs)
		}
		return nil
	}

	a.writeLog("Active profile has no generated config, building it before start")
	return a.configBuilder.BuildConfigForProfile(profile.ID, subscriptionURL, profile.WireGuardConfigs)
}

func (a *App) requireRouteSourceWhenFreeMethodsDisabled(configPath string) error {
	if a.storage == nil {
		return nil
	}

	settings := a.storage.GetAppSettings()
	if FreeMethodsAllowed(settings) {
		return nil
	}

	config, err := readJSONConfig(configPath)
	if err != nil {
		return err
	}
	if len(collectVPNProbeCandidateSpecs(config)) > 0 {
		a.writeLog("[FreeAccess] free methods disabled; VPN/subscription candidate is available")
		return nil
	}

	userSettings, err := a.storage.GetUserSettings()
	if err == nil && userSettings != nil && len(userSettings.WireGuardConfigs) > 0 {
		a.writeLog(fmt.Sprintf("[FreeAccess] free methods disabled; WireGuard-only mode allowed with %d config(s)", len(userSettings.WireGuardConfigs)))
		return nil
	}

	return fmt.Errorf("включено 'Не использовать бесплатные методы', но нет VPN/VLESS/другой подписки или WireGuard-сети. Добавьте рабочую подписку, WireGuard для внутренних сетей или выключите этот тумблер")
}

func (a *App) autoUpdateSubscriptionBeforeStart(busyID string) error {
	if a.storage == nil || a.configBuilder == nil {
		return nil
	}

	settings := a.storage.GetAppSettings()
	if !settings.AutoUpdateSub {
		return nil
	}

	interval := time.Duration(settings.SubUpdateInterval) * time.Hour
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if !settings.LastSubUpdate.IsZero() && time.Since(settings.LastSubUpdate) < interval {
		a.writeLog("[Subscription] auto-update skipped: interval has not elapsed")
		return nil
	}

	profile, err := a.storage.GetActiveProfile()
	if err != nil || profile == nil || profile.SubscriptionURL == "" {
		return nil
	}

	a.updateBusy(busyID, "Обновляем подписку перед подключением...")
	a.writeLog("[Subscription] auto-update before start: refreshing active profile")
	if err := a.configBuilder.BuildConfigForProfile(profile.ID, profile.SubscriptionURL, profile.WireGuardConfigs); err != nil {
		return err
	}

	settings.LastSubUpdate = time.Now()
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return err
	}
	a.writeLog("[Subscription] auto-update before start completed")
	return nil
}

// logOutput reads and logs process output
func (a *App) logOutput(reader io.Reader, prefix string) {
	a.writeLog(fmt.Sprintf("[%s] Log reader started", prefix))

	// Read the logging preference once up front instead of per line: sing-box at
	// trace level emits thousands of lines/sec, and GetAppSettings takes an RLock
	// and copies the whole settings struct (incl. maps) each call. A toggle mid
	// session takes effect on the next connect. See review.md §1.2.
	loggingEnabled := true
	if a.storage != nil {
		loggingEnabled = a.storage.GetAppSettings().EnableLogging
	}

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()

		if loggingEnabled {
			a.writeLog(fmt.Sprintf("[%s] %s", prefix, line))
		} else {
			a.AddToLogBuffer(fmt.Sprintf("[%s] %s", prefix, line))
		}

		// Check for critical errors only (not normal network errors)
		lineLower := strings.ToLower(line)

		// Определяем действительно критические ошибки
		isCriticalError := strings.Contains(lineLower, "fatal") &&
			// Но не ошибки rule-set (можно продолжить без них)
			!strings.Contains(lineLower, "rule-set")

		// Игнорируем обычные сетевые ошибки (не критичны):
		// - IPv6 unreachable (нет IPv6 - норма)
		// - DNS resolution failures
		// - Connection refused/timeout
		// - Network unreachable для отдельных соединений
		isIgnorableError := strings.Contains(lineLower, "unreachable network") ||
			strings.Contains(lineLower, "dns: exchange failed") ||
			strings.Contains(lineLower, "context deadline exceeded") ||
			strings.Contains(lineLower, "connection refused") ||
			strings.Contains(lineLower, "i/o timeout") ||
			strings.Contains(lineLower, "network is unreachable") ||
			strings.Contains(lineLower, "no route to host") ||
			strings.Contains(lineLower, "connectex:")

		if isCriticalError && !isIgnorableError {
			a.mu.Lock()
			a.hasError.Store(true)
			a.mu.Unlock()
			UpdateTrayIcon("error")
		}
	}
	if err := scanner.Err(); err != nil {
		a.writeLog(fmt.Sprintf("[%s] Log reader error: %v", prefix, err))
	} else {
		a.writeLog(fmt.Sprintf("[%s] Log reader finished", prefix))
	}
}

// Stop stops VPN
func (a *App) Stop() map[string]interface{} {
	busyID := a.beginBusyTagged("vpn-disconnect", "Отключаем VPN...")
	defer a.endBusy(busyID)

	a.vpnStopping.Store(true)
	defer a.vpnStopping.Store(false)

	a.writeLog("VPN stop requested")
	manualStop := !a.isShuttingDown()
	if manualStop {
		a.setRestoreVPNOnStartup(false)
	}

	a.mu.Lock()
	cmd := a.cmd
	done := a.cmdDone

	if !a.isRunning || a.cmd == nil || a.cmd.Process == nil {
		a.updateBusy(busyID, "Останавливаем фоновые методы...")
		a.isRunning = false
		a.stoppedManually = false
		a.cmd = nil
		a.cmdDone = nil
		a.hasError.Store(false)
		a.mu.Unlock()
		// Also stop Native WireGuard tunnels and free access process
		a.stopNativeWireGuardTunnels()
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.updateBusy(busyID, "Проверяем и закрываем фоновые процессы dropo...")
		a.cleanupDropoRuntimeResidue("stop without main process")
		if a.trafficStats != nil {
			a.trafficStats.EndSession()
			a.trafficStats.Save()
		}
		a.writeLog("VPN stopped successfully")
		a.closeLogFile()
		UpdateTrayIcon("disconnected")
		if a.ctx != nil {
			a.emitEvent("vpn-status-changed", false)
		}
		return map[string]interface{}{
			"success": true,
		}
	}

	a.stoppedManually = true
	a.hasError.Store(false)
	a.mu.Unlock()

	a.writeLog("Stopping VPN...")
	a.updateBusy(busyID, "Восстанавливаем системный proxy...")
	a.resetDropoSystemProxy("pre-stop")

	// Terminate sing-box first so the TUN interface releases traffic quickly.
	if runtime.GOOS == "windows" {
		a.updateBusy(busyID, "Завершаем sing-box и освобождаем TUN-интерфейс...")
		if err := runBackgroundCommandWithTimeout(4*time.Second, "taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", cmd.Process.Pid)); err != nil {
			a.writeLog(fmt.Sprintf("taskkill sing-box failed or timed out: %v", err))
		}
	} else {
		a.updateBusy(busyID, "Завершаем sing-box...")
		cmd.Process.Signal(syscall.SIGTERM)
	}

	a.updateBusy(busyID, "Ждем завершения сетевого процесса...")
	processExited := waitForProcessDone(done, 3*time.Second)
	if !processExited {
		a.writeLog("Timed out waiting for sing-box to exit; continuing shutdown cleanup")
	}

	a.updateBusy(busyID, "Останавливаем WireGuard-сети...")
	a.stopNativeWireGuardTunnels()
	a.updateBusy(busyID, "Останавливаем бесплатные методы обхода...")
	a.stopFreeAccess()
	a.updateBusy(busyID, "Останавливаем Xray bridge...")
	a.stopXrayBridge()

	if !processExited {
		a.updateBusy(busyID, "Закрываем зависшие фоновые процессы dropo...")
		a.cleanupDropoRuntimeResidue("stop timeout")
	} else {
		a.updateBusy(busyID, "Проверяем и закрываем фоновые процессы dropo...")
		a.cleanupDropoRuntimeResidue("post-stop")
	}
	a.writeLog("VPN stopped successfully")

	a.mu.Lock()
	if a.cmd == cmd {
		a.cmd = nil
		a.cmdDone = nil
	}
	a.isRunning = false
	a.stoppedManually = false
	a.hasError.Store(false)
	a.mu.Unlock()
	UpdateTrayIcon("disconnected")
	if !processExited && a.ctx != nil {
		a.emitEvent("vpn-status-changed", false)
	}

	return map[string]interface{}{
		"success": true,
		"running": false,
	}
}

// Toggle toggles VPN state
func (a *App) Toggle() map[string]interface{} {
	if a.isVPNRunning() {
		return a.Stop()
	}
	return a.Start()
}

// CanModifyVPN checks if VPN settings can be modified
func (a *App) CanModifyVPN() map[string]interface{} {
	a.mu.Lock()
	defer a.mu.Unlock()

	return map[string]interface{}{
		"canModify": !a.isRunning,
		"message":   "Сначала отключите VPN для изменения настроек",
	}
}

// updateConfigLogLevel updates the log level in the config file
func (a *App) updateConfigLogLevel(configPath, logLevel string) error {
	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// Parse JSON
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	// Update log level
	if logSection, ok := config["log"].(map[string]interface{}); ok {
		logSection["level"] = logLevel
	} else {
		// Create log section if not exists
		config["log"] = map[string]interface{}{
			"level": logLevel,
		}
	}

	// Write back
	newData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

func (a *App) filterActiveFreeAccessOutbounds(configPath string, activeFreeAccessTags []string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}

	freeTags := make(map[string]bool, len(FreeAccessMethodTags()))
	for _, tag := range FreeAccessMethodTags() {
		freeTags[tag] = true
	}
	activeTags := make(map[string]bool, len(activeFreeAccessTags))
	for _, tag := range activeFreeAccessTags {
		activeTags[tag] = true
	}

	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return nil
	}

	filteredOutbounds := make([]interface{}, 0, len(outbounds)+1)
	removedFreeOutbounds := make([]string, 0)
	needsNoRouteOutbound := false
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			filteredOutbounds = append(filteredOutbounds, outbound)
			continue
		}

		tag, _ := outboundMap["tag"].(string)
		if freeTags[tag] && !activeTags[tag] {
			removedFreeOutbounds = append(removedFreeOutbounds, tag)
			continue
		}

		if candidates := interfaceStringSlice(outboundMap["outbounds"]); len(candidates) > 0 {
			filtered := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				if freeTags[candidate] && !activeTags[candidate] {
					continue
				}
				filtered = append(filtered, candidate)
			}
			if len(filtered) == 0 {
				if tag == SmartBypassGroupTag || strings.HasPrefix(tag, "bypass-") {
					fallback := a.emptyBypassGroupFallbackOutbound()
					filtered = []string{fallback}
					if fallback == NoRouteOutboundTag {
						needsNoRouteOutbound = true
					}
				} else {
					filtered = []string{"direct"}
				}
			}
			outboundMap["outbounds"] = filtered
			if defaultValue, ok := outboundMap["default"].(string); ok && !stringSliceContains(filtered, defaultValue) {
				outboundMap["default"] = filtered[0]
			}
		}

		filteredOutbounds = append(filteredOutbounds, outboundMap)
	}
	if needsNoRouteOutbound && !outboundTagExists(filteredOutbounds, NoRouteOutboundTag) {
		filteredOutbounds = append(filteredOutbounds, map[string]interface{}{
			"type": "block",
			"tag":  NoRouteOutboundTag,
		})
	}

	config["outbounds"] = filteredOutbounds
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(configPath, encoded, 0600); err != nil {
		return err
	}

	if len(removedFreeOutbounds) > 0 {
		a.writeLog(fmt.Sprintf("[ByeDPI] removed inactive strategy outbounds from config: %s", strings.Join(removedFreeOutbounds, ",")))
	}
	return nil
}

func (a *App) emptyBypassGroupFallbackOutbound() string {
	if a != nil {
		status := a.currentNetworkModeStatus()
		if status.Active == NetworkModeDeepWindows {
			a.writeLog("[FreeAccess] empty bypass group fallback: direct via Deep Windows transparent engine")
			return "direct"
		}
	}
	return NoRouteOutboundTag
}

func stringSliceContains(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func (a *App) logActiveConfigDiagnostics(configPath string) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		a.writeLog(fmt.Sprintf("[Diag] failed to read active config: %v", err))
		return
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		a.writeLog(fmt.Sprintf("[Diag] failed to parse active config: %v", err))
		return
	}

	if a.storage != nil {
		settings := a.storage.GetAppSettings()
		a.writeLog(fmt.Sprintf("[Diag] routing_mode=%s free_methods_allowed=%v disable_free_access=%v hide_ru=%v",
			settings.RoutingMode, FreeMethodsAllowed(settings), settings.DisableFreeAccess, settings.HideRuTraffic))
	}

	outbounds, _ := config["outbounds"].([]interface{})
	outboundLabels := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}

		tag, _ := outboundMap["tag"].(string)
		outboundType, _ := outboundMap["type"].(string)
		if tag == "" {
			continue
		}
		outboundLabels = append(outboundLabels, fmt.Sprintf("%s:%s", tag, outboundType))

		if tag == SmartBypassGroupTag || tag == VpnOrDirectGroupTag || tag == RuRouteGroupTag || tag == "proxy" || tag == "auto-select" || strings.HasPrefix(tag, "bypass-") {
			a.writeLog(fmt.Sprintf("[Diag] outbound %s candidates=%s", tag, strings.Join(interfaceStringSlice(outboundMap["outbounds"]), ",")))
		}
		if tag == "direct" {
			a.writeLog(fmt.Sprintf("[Diag] outbound direct bind_interface=%v", outboundMap["bind_interface"]))
		}
	}
	a.writeLog(fmt.Sprintf("[Diag] outbounds=%s", strings.Join(outboundLabels, ",")))

	if inbounds, ok := config["inbounds"].([]interface{}); ok {
		for _, inbound := range inbounds {
			inboundMap, ok := inbound.(map[string]interface{})
			if !ok || inboundMap["type"] != "tun" {
				continue
			}
			a.writeLog(fmt.Sprintf("[Diag] tun auto_route=%v strict_route=%v stack=%v interface=%v address=%v",
				inboundMap["auto_route"], inboundMap["strict_route"], inboundMap["stack"], inboundMap["interface_name"], inboundMap["address"]))
		}
	}

	route, _ := config["route"].(map[string]interface{})
	if route == nil {
		a.writeLog("[Diag] route section missing")
		return
	}
	a.writeLog(fmt.Sprintf("[Diag] route auto_detect_interface=%v default_interface=%v default_domain_resolver=%v",
		route["auto_detect_interface"], route["default_interface"], route["default_domain_resolver"]))

	rules, _ := route["rules"].([]interface{})
	ruleSets, _ := route["rule_set"].([]interface{})
	routeCounts := map[string]int{}
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		outbound, _ := ruleMap["outbound"].(string)
		if outbound != "" {
			routeCounts[outbound]++
		}
	}

	serviceRoutes := 0
	for outbound, count := range routeCounts {
		if strings.HasPrefix(outbound, "bypass-") {
			serviceRoutes += count
		}
	}

	a.writeLog(fmt.Sprintf("[Diag] route final=%v rules=%d rule_sets=%d routes smart-bypass=%d service-bypass=%d vpn-or-direct=%d direct=%d proxy=%d",
		route["final"], len(rules), len(ruleSets), routeCounts[SmartBypassGroupTag], serviceRoutes, routeCounts[VpnOrDirectGroupTag], routeCounts["direct"], routeCounts["proxy"]))
}

func interfaceStringSlice(value interface{}) []string {
	switch items := value.(type) {
	case string:
		if items == "" {
			return nil
		}
		return []string{items}
	case []string:
		return append([]string(nil), items...)
	case []interface{}:
		result := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

// startNativeWireGuardTunnels starts all configured Native WireGuard tunnels
func (a *App) startNativeWireGuardTunnels() {
	a.writeLog("[WireGuard] startNativeWireGuardTunnels called")

	if a.nativeWG == nil {
		a.writeLog("[WireGuard] nativeWG is nil, skipping")
		return
	}

	if a.storage == nil {
		a.writeLog("[WireGuard] storage is nil, skipping")
		return
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		a.writeLog(fmt.Sprintf("[WireGuard] Error getting user settings: %v", err))
		return
	}

	a.writeLog(fmt.Sprintf("[WireGuard] Found %d WireGuard config(s)", len(settings.WireGuardConfigs)))

	if len(settings.WireGuardConfigs) == 0 {
		a.writeLog("[WireGuard] No WireGuard configs found, skipping")
		return
	}

	a.writeLog(fmt.Sprintf("Starting %d Native WireGuard tunnel(s)...", len(settings.WireGuardConfigs)))

	// Set up restart callback for health check
	a.nativeWG.SetTunnelRestartCallback(func(configID int) {
		a.writeLog(fmt.Sprintf("[WireGuard] Tunnel %d was restarted by health check", configID))
		a.AddToLogBuffer(fmt.Sprintf("WireGuard туннель %d: переподключен", configID))
		// Emit event to frontend
		a.emitEvent("wireguard-tunnel-restarted", configID)
	})

	started := 0
	for i, wg := range settings.WireGuardConfigs {
		a.writeLog(fmt.Sprintf("[WireGuard] Processing config %d: tag=%s, name=%s, endpoint=%s, allowedIPs=%v",
			i, wg.Tag, wg.Name, wg.Endpoint, wg.AllowedIPs))

		nativeConfig := wg.ToWireGuardConfig()
		a.writeLog(fmt.Sprintf("[WireGuard] Native config: Address=%v, DNS=%s, Peers=%d",
			nativeConfig.Address, nativeConfig.DNS, len(nativeConfig.Peers)))

		if err := a.nativeWG.StartTunnel(i, nativeConfig); err != nil {
			a.writeLog(fmt.Sprintf("[WireGuard] Failed to start %s: %v", wg.Tag, err))
			a.AddToLogBuffer(fmt.Sprintf("WireGuard %s: ошибка запуска", wg.Name))
		} else {
			started++
			a.AddToLogBuffer(fmt.Sprintf("WireGuard %s: подключен", wg.Name))
		}
	}

	if started > 0 {
		a.writeLog(fmt.Sprintf("[WireGuard] Started %d/%d tunnels", started, len(settings.WireGuardConfigs)))

		// Start health check monitoring
		a.nativeWG.StartHealthCheck()
		a.writeLog("[WireGuard] Health check monitoring started")
	}
}

// stopNativeWireGuardTunnels stops all Native WireGuard tunnels
func (a *App) stopNativeWireGuardTunnels() {
	if a.nativeWG == nil {
		return
	}

	// Stop health check first
	a.nativeWG.StopHealthCheck()

	a.writeLog("Stopping Native WireGuard tunnels...")
	a.nativeWG.StopAllTunnels()
	a.writeLog("Native WireGuard tunnels stopped")
}

func (a *App) startXrayBridge() error {
	if a.xrayBridge == nil || a.storage == nil {
		return nil
	}

	configPath, hasConfig, err := a.storage.WriteActiveXrayConfigToFile()
	if err != nil {
		return err
	}
	if !hasConfig {
		a.writeLog("[Xray] no xhttp bridge config for active profile")
		return nil
	}
	a.writeLog(fmt.Sprintf("[Xray] active xhttp bridge config: %s", configPath))
	return a.xrayBridge.Start()
}

func (a *App) stopXrayBridge() {
	if a.xrayBridge == nil {
		return
	}
	a.xrayBridge.Stop()
}

func (a *App) ensureDefaultZapretForDeepWindowsStart(plan DeepWindowsRoutePlan, busyID string) error {
	if a == nil || a.zapret == nil || a.storage == nil {
		return nil
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		a.writeLog("[NetworkMode] Deep Windows transparent engine skipped: free methods are disabled")
		return nil
	}
	if len(plan.TransparentServices) == 0 {
		return nil
	}

	// Compose a single winws engine from per-service profiles: each service
	// keeps its own best method (cached, or found by the first-run search),
	// because no single desync technique unblocks every service.
	if err := a.startDeepWindowsPerServiceEngine(busyID); err != nil {
		a.logDeepWindowsError(fmt.Sprintf("per-service engine failed to start: %v", err))
		return fmt.Errorf("failed to start per-service transparent engine: %w", err)
	}
	return nil
}

// startFreeAccess starts the ByeDPI process if the "Free access" feature is
// enabled in settings and the bundled binary is available. Best-effort: a
// failure here does not block VPN startup, it just means the "smart-bypass"
// outbound group will fail its health-check and fall back to VPN/direct.
func (a *App) startFreeAccessForConfig(configPath string) []string {
	config, err := readJSONConfig(configPath)
	if err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to inspect active config before starting helpers: %v", err))
	}
	return a.startFreeAccess(config)
}

func (a *App) startFreeAccess(activeConfig map[string]interface{}) []string {
	if a.storage == nil {
		return nil
	}

	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		a.writeLog("[FreeAccess] free methods are disabled by settings")
		return nil
	}

	activeTags := []string{}
	if a.zapret != nil {
		a.zapret.Stop()
	}
	if a.freeProxySidecarsCapturedByActiveNetwork(activeConfig) {
		a.writeLog("[FreeAccess] ByeDPI/SpoofDPI proxy helpers skipped: Windows TUN auto_route captures helper process traffic; transparent methods/VPN will be used")
		return activeTags
	}
	if runtime.GOOS == "windows" && tunAutoRouteEnabled(activeConfig) {
		a.writeLog("[FreeAccess] Windows TUN auto_route detected; proxy helpers are allowed by process-direct route rules")
	}

	if a.byeDPI == nil {
		a.writeLog("[ByeDPI] manager is not initialized")
	} else if !a.byeDPI.IsInstalled() {
		a.writeLog("[ByeDPI] binary not bundled, ByeDPI strategies unavailable this session")
	}

	if a.byeDPI != nil && a.byeDPI.IsInstalled() {
		if err := a.byeDPI.Start(); err != nil {
			a.writeLog(fmt.Sprintf("[ByeDPI] Failed to start: %v", err))
			a.AddToLogBuffer("Бесплатный доступ: не удалось запустить ByeDPI")
		} else {
			tags := a.byeDPI.WaitForActiveTags(byeDPIStartupWait)
			activeTags = append(activeTags, tags...)
			a.writeLog(fmt.Sprintf("[ByeDPI] Free access bypass started (%d/%d strategy/strategies active: %s)",
				len(tags), len(DefaultByeDPIStrategies), strings.Join(tags, ",")))
		}
	}

	if a.spoofDPI != nil {
		if a.spoofDPI.IsInstalled() {
			if err := a.spoofDPI.Start(); err != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] SpoofDPI failed to start: %v", err))
			} else {
				tags := a.spoofDPI.WaitForActiveTags(freeProxyStartupWait)
				activeTags = append(activeTags, tags...)
				a.writeLog(fmt.Sprintf("[FreeAccess] proxy methods active: %s", strings.Join(tags, ",")))
			}
		} else {
			a.writeLog("[FreeAccess] SpoofDPI binary not bundled, SpoofDPI methods unavailable this session")
		}
	}

	a.startTelegramProxyIfNeeded(activeConfig)

	return activeTags
}

// telegram transport decisions returned by resolveTelegramTransport.
const (
	telegramTransportFree = "free" // local MTProto sidecar (WS bypass) is the primary path
	telegramTransportVPN  = "vpn"  // route Telegram straight through the subscription, no sidecar/injection
	telegramTransportNone = "none" // no usable path (e.g. Telegram not proxy-handled, or free disabled and no VPN)
)

// resolveTelegramTransport decides how Telegram traffic is carried. The default
// (auto) keeps the free MTProto sidecar primary even when a subscription is
// present — the VPN stays as the per-service backstop route. Choosing the "VPN
// подписка" method for Telegram (FreeAccessMethods["telegram"]=vpn) switches to
// a clean VPN-only path that never injects a proxy into Telegram.
func resolveTelegramTransport(settings GlobalAppSettings, hasVPN bool) string {
	if serviceBlockType("telegram") != "proxy" {
		return telegramTransportNone
	}
	if FreeAccessServiceMethod(settings, "telegram") == FreeAccessMethodVPN {
		if hasVPN {
			return telegramTransportVPN
		}
		return telegramTransportNone
	}
	if !FreeMethodsAllowed(settings) {
		if hasVPN {
			return telegramTransportVPN
		}
		return telegramTransportNone
	}
	return telegramTransportFree
}

// startTelegramProxyIfNeeded launches (or stops) the tg-ws-proxy MTProto sidecar
// according to the Telegram transport policy.
//
//   - free (default/auto): sidecar primary; its egress goes DIRECT (its WS
//     obfuscation works there, free) via the process-direct route rule.
//   - vpn: sidecar kept ALIVE but its egress is routed through the subscription
//     (config omits it from the direct rule, so its DC traffic falls to
//     bypass-telegram→VPN). Needs a non-RU exit to actually reach Telegram.
//   - none: stop the sidecar.
//
// The tg://proxy link is offered at most once per VPN session. Telegram exposes
// no API to inspect or remove saved proxies, so repeated prompts are intrusive.
func (a *App) startTelegramProxyIfNeeded(activeConfig map[string]interface{}) {
	if a.tgwsproxy == nil || !a.tgwsproxy.IsInstalled() {
		return
	}

	var settings GlobalAppSettings
	if a.storage != nil {
		settings = a.storage.GetAppSettings()
	}
	hasVPN := len(collectVPNProbeCandidateSpecs(activeConfig)) > 0
	transport := resolveTelegramTransport(settings, hasVPN)

	if transport == telegramTransportNone {
		a.tgwsproxy.Stop()
		return
	}

	// Always keep the sidecar ALIVE when Telegram is proxy-handled, in BOTH free
	// and vpn transport. Telegram may already have the local proxy saved (even on
	// a fresh portable extract, where the injected flag is reset); if the sidecar
	// is not listening, Telegram is stuck on "соединение…". The egress is routed
	// by the config: DIRECT for free/auto (the WS bypass that actually unblocks
	// Telegram), or through the VPN when Telegram is set to the subscription.
	if err := a.tgwsproxy.Start(); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] Telegram MTProto proxy failed to start: %v", err))
		return
	}
	// The sidecar ran this session, so a proxy may be (re)injected into Telegram —
	// the exit cleanup notice is relevant. If the user never starts the VPN this
	// session, the notice is not shown (nothing was touched).
	a.tgProxyStartedSession.Store(true)
	if transport == telegramTransportVPN {
		a.writeLog("[Telegram] MTProto sidecar alive; egress routed through the VPN subscription (needs a non-RU exit to reach Telegram)")
	} else {
		a.writeLog("[Telegram] MTProto sidecar active (free WS bypass primary)")
	}

	// Give an already-saved proxy a short grace period to connect, then offer the
	// tg://proxy link once if Telegram is running and not connected to the local
	// sidecar. We do not keep re-opening Telegram mid-session.
	go a.telegramProxyInitialPromptOnce()
}

// reAddTelegramProxyIfMissing opens the tg://proxy link when Telegram is running
// but not currently connected to our local sidecar. Returns true if a link was
// opened. Logged so the decision is visible in client logs.
func (a *App) reAddTelegramProxyIfMissing(reason string) bool {
	if a.tgwsproxy == nil || !a.tgwsproxy.IsInstalled() || !a.tgwsproxy.IsRunning() {
		return false
	}
	if serviceBlockType("telegram") != "proxy" {
		return false
	}
	if os.Getenv(tgProxyAutoConnectEnv) == "0" {
		return false
	}
	if !isProcessRunningByName(TelegramProcessName) {
		a.writeLog(fmt.Sprintf("[Telegram] presence check (%s): Telegram not running — skip", reason))
		return false
	}
	port := TgWsProxyDefaultPort
	if cfg, ok := a.tgwsproxy.readConfig(); ok {
		cfg = normalizeTgWsProxyConfig(cfg)
		if cfg.Port > 0 && cfg.Port <= 65535 {
			port = cfg.Port
		}
	}
	if telegramProxyHasActiveConnection(uint16(port)) {
		a.writeLog(fmt.Sprintf("[Telegram] presence check (%s): proxy already in use — nothing to do", reason))
		return false
	}
	a.writeLog(fmt.Sprintf("[Telegram] presence check (%s): no active proxy connection — opening tg://proxy once", reason))
	a.tgwsproxy.AutoConnectTelegram()
	return true
}

// telegramProxyInitialPromptOnce runs once per VPN start: after a short grace
// period (so an already-saved proxy has time to connect) it offers the proxy if
// missing. It never repeats within the same VPN session.
func (a *App) telegramProxyInitialPromptOnce() {
	time.Sleep(3 * time.Second)
	if a.isShuttingDown() {
		return
	}
	if !a.tgProxyPromptedSession.CompareAndSwap(false, true) {
		return
	}
	if a.reAddTelegramProxyIfMissing("vpn-start") {
		a.markTelegramProxyInjected()
	}
}

// markTelegramProxyInjected persists that dropo manages a Telegram proxy, so the
// cleanup notice on exit knows one may be saved inside Telegram. Idempotent.
func (a *App) markTelegramProxyInjected() {
	if a.storage == nil {
		return
	}
	settings := a.storage.GetAppSettings()
	if settings.TelegramProxyInjected {
		return
	}
	settings.TelegramProxyInjected = true
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		a.writeLog(fmt.Sprintf("[Telegram] failed to persist proxy-injected flag: %v", err))
	}
}

// TelegramProxyStatusInfo is returned to the frontend so it can show the cleanup
// banner/button for the local MTProto proxy saved inside Telegram.
type TelegramProxyStatusInfo struct {
	Injected         bool   `json:"injected"`         // dropo opened the tg://proxy link at least once
	RecommendRemove  bool   `json:"recommendRemove"`  // Telegram is now carried by the VPN, so the saved proxy should be removed
	ProxyLink        string `json:"proxyLink"`        // the tg://proxy link (for re-adding / reference)
	ActiveConnection bool   `json:"activeConnection"` // Telegram is actively connected to the local proxy port
	ShowNotice       bool   `json:"showNotice"`       // show the exit cleanup notice
}

// TelegramProxyStatus reports whether a local Telegram proxy was injected and
// whether the user should now remove it (because Telegram moved to the VPN).
func (a *App) TelegramProxyStatus() TelegramProxyStatusInfo {
	info := TelegramProxyStatusInfo{}
	if a.storage != nil {
		info.Injected = a.storage.GetAppSettings().TelegramProxyInjected
	}
	if a.tgwsproxy != nil {
		if link, ok := a.tgwsproxy.TelegramProxyLink(); ok {
			info.ProxyLink = link
		}
		if cfg, ok := a.tgwsproxy.readConfig(); ok {
			cfg = normalizeTgWsProxyConfig(cfg)
			if cfg.Port > 0 && cfg.Port <= 65535 {
				info.ActiveConnection = telegramProxyHasActiveConnection(uint16(cfg.Port))
			}
		}
	}
	// Recommend removal when a proxy is saved in Telegram but the sidecar is no
	// longer the chosen transport (VPN carries Telegram, or free is disabled).
	info.RecommendRemove = info.Injected && (info.ActiveConnection || (a.tgwsproxy != nil && !a.tgwsproxy.IsRunning()))
	return info
}

// OpenTelegramProxySettings opens Telegram's settings deep link so the user can
// delete the local proxy. Auto-removal is impossible; this is a best-effort
// shortcut to the right screen.
func (a *App) OpenTelegramProxySettings() {
	if err := openExternalURL("tg://settings"); err != nil {
		a.writeLog(fmt.Sprintf("[Telegram] could not open Telegram settings: %v", err))
	}
}

// AcknowledgeTelegramProxyRemoved clears the injected flag after the user has
// removed the proxy inside Telegram, so the cleanup hint stops appearing.
func (a *App) AcknowledgeTelegramProxyRemoved() {
	if a.storage == nil {
		return
	}
	settings := a.storage.GetAppSettings()
	if !settings.TelegramProxyInjected {
		return
	}
	settings.TelegramProxyInjected = false
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		a.writeLog(fmt.Sprintf("[Telegram] failed to clear proxy-injected flag: %v", err))
	}
}

func liveFreeAccessProxyTags(tags []string) []string {
	live := make([]string, 0, len(tags))
	for _, tag := range tags {
		port, ok := freeAccessProxyPort(tag)
		if !ok {
			continue
		}
		if loopbackPortReady(port, 250*time.Millisecond) {
			live = append(live, tag)
		}
	}
	return live
}

func freeAccessProxyPort(tag string) (int, bool) {
	for _, strategy := range DefaultByeDPIStrategies {
		if strategy.Tag == tag {
			return strategy.Port, true
		}
	}
	for _, method := range DefaultSpoofDPIMethods {
		if method.Tag == tag {
			return method.Port, true
		}
	}
	return 0, false
}

// stopFreeAccess stops the ByeDPI process, if running.
func (a *App) stopFreeAccess() {
	if a.byeDPI != nil {
		a.byeDPI.Stop()
	}
	if a.spoofDPI != nil {
		a.spoofDPI.Stop()
	}
	if a.zapret != nil {
		a.zapret.Stop()
	}
	if a.tgwsproxy != nil {
		a.tgwsproxy.Stop()
	}
	a.cleanupDropoRuntimeResidue("free access stop")
}
