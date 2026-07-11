package main

// App settings methods for dropo.
// This file contains app configuration API methods

import (
	"fmt"
	"os"
	"time"
)

// GetAppConfig возвращает текущие настройки приложения (API для фронтенда)
func (a *App) GetAppConfig() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()
	versionInfo := GetVersionInfo()
	networkMode := a.currentNetworkModeStatus()

	return map[string]interface{}{
		"success":           true,
		"autoStart":         settings.AutoStart,
		"autoStartPrompted": settings.AutoStartPrompted,
		"enableLogging":     settings.EnableLogging,
		"checkUpdates":      settings.CheckUpdates,
		"notifications":     settings.Notifications,
		"theme":             settings.Theme,
		"language":          settings.Language,
		"logLevel":          settings.LogLevel,
		"routingMode":       string(settings.RoutingMode),
		"networkMode":       string(settings.NetworkMode),
		"autoUpdateSub":     settings.AutoUpdateSub,
		"subUpdateInterval": settings.SubUpdateInterval,
		"lastSubUpdate":     settings.LastSubUpdate.Format(time.RFC3339),
		"hideRuTraffic":     settings.HideRuTraffic,
		"ruProxyAddress":    settings.RuProxyAddress,
		"disableFreeAccess": settings.DisableFreeAccess,
		"wireGuardVersion":  settings.WireGuardVersion,
		"appVersion":        versionInfo["version"],
		"appFullVersion":    versionInfo["fullVersion"],
		"appName":           versionInfo["name"],
		"singboxVersion":    versionInfo["singboxVersion"],
		"buildHash":         versionInfo["buildHash"],
		"buildTime":         versionInfo["buildTime"],
		"githubRepo":        versionInfo["githubRepo"],
		"githubURL":         versionInfo["githubURL"],
		"telegramName":      versionInfo["telegramName"],
		"telegramURL":       versionInfo["telegramURL"],
		"networkModeStatus": networkModeStatusPayload(networkMode),
	}
}

// SaveAppConfig сохраняет настройки приложения (API для фронтенда)
func (a *App) SaveAppConfig(autoStart, enableLogging, checkUpdates, notifications, autoUpdateSub bool, theme, language, logLevel string, subUpdateInterval int) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()

	// Обновляем настройки
	settings.AutoStart = autoStart
	// Изменение тумблера автозапуска в настройках — это осознанный выбор, поэтому
	// первичный диалог автозапуска больше показывать не нужно.
	settings.AutoStartPrompted = true
	settings.EnableLogging = enableLogging
	settings.CheckUpdates = checkUpdates
	settings.Notifications = notifications
	settings.AutoUpdateSub = autoUpdateSub
	settings.Theme = Theme(theme)
	settings.Language = Language(language)
	settings.SubUpdateInterval = subUpdateInterval

	// Обновляем уровень логирования
	if logLevel != "" {
		settings.LogLevel = LogLevel(logLevel)
	}

	// Сохраняем в storage
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	// Применяем автозапуск
	if err := applyAutoStart(autoStart); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка настройки автозапуска: %v", err),
		}
	}

	return map[string]interface{}{
		"success": true,
		"message": "Настройки сохранены",
	}
}

// ResolveAutoStartPrompt records the user's answer to the first-run autostart
// dialog: enable=true keeps launch-at-logon on and registers it; enable=false
// flips the stored default to off and ensures nothing is registered. Either way
// the choice is remembered so the dialog is not shown again.
func (a *App) ResolveAutoStartPrompt(enable bool) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()
	settings.AutoStart = enable
	settings.AutoStartPrompted = true
	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения выбора автозапуска: %v", err),
		}
	}

	if err := applyAutoStart(enable); err != nil {
		return map[string]interface{}{
			"success":           false,
			"error":             fmt.Sprintf("Ошибка настройки автозапуска: %v", err),
			"autoStart":         enable,
			"autoStartPrompted": true,
		}
	}

	return map[string]interface{}{
		"success":           true,
		"autoStart":         enable,
		"autoStartPrompted": true,
	}
}

// GetWireGuardVersion returns current WireGuard version (bundled with app)
func (a *App) GetWireGuardVersion() map[string]interface{} {
	installed := false
	wireguardPath := ""

	if a.nativeWG != nil {
		installed = a.nativeWG.IsInstalled()
		wireguardPath = a.nativeWG.wireguardPath
	}

	return map[string]interface{}{
		"success":       true,
		"version":       WireGuardVersion,
		"wintunVersion": WintunVersion,
		"installed":     installed,
		"wireguardPath": wireguardPath,
	}
}

// GetAutoStartStatus проверяет статус автозапуска
func (a *App) GetAutoStartStatus() map[string]interface{} {
	return map[string]interface{}{
		"success":   true,
		"autoStart": IsAutoStartEnabled(),
	}
}

// ============================================================================
// Import/Export API methods
// ============================================================================

// ExportProfilesToFile opens save dialog and exports all profiles to JSON file.
func (a *App) ExportProfilesToFile() map[string]interface{} {
	return map[string]interface{}{
		"success": false,
		"error":   "file dialog moved to Flutter; call ExportProfilesToPath with a selected path",
	}
}

func (a *App) ExportProfilesToPath(filename string) map[string]interface{} {
	a.waitForInit()
	if filename == "" {
		return map[string]interface{}{"success": false, "error": "empty export path"}
	}

	exportResult := a.ExportAllProfiles()
	if ok, _ := exportResult["success"].(bool); !ok {
		return exportResult
	}

	jsonData, _ := exportResult["data"].(string)
	if err := os.WriteFile(filename, []byte(jsonData), 0644); err != nil {
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("failed to write export file: %v", err)}
	}

	profilesCount, _ := exportResult["profiles_count"].(int)
	a.writeLog(fmt.Sprintf("Exported %d profiles to %s", profilesCount, filename))
	return map[string]interface{}{
		"success":        true,
		"filename":       filename,
		"profiles_count": profilesCount,
	}
}

// ImportProfilesFromFile opens file dialog and imports profiles from JSON file.
func (a *App) ImportProfilesFromFile() map[string]interface{} {
	return map[string]interface{}{
		"success": false,
		"error":   "file dialog moved to Flutter; call ImportProfilesFromPath with a selected path",
	}
}

func (a *App) ImportProfilesFromPath(filename string) map[string]interface{} {
	a.waitForInit()
	if filename == "" {
		return map[string]interface{}{"success": false, "error": "empty import path"}
	}

	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{"success": false, "error": "VPN must be stopped before importing profiles"}
	}
	a.mu.Unlock()

	data, err := os.ReadFile(filename)
	if err != nil {
		return map[string]interface{}{"success": false, "error": fmt.Sprintf("failed to read import file: %v", err)}
	}

	validationResult := a.ValidateImportData(string(data))
	if ok, _ := validationResult["success"].(bool); !ok {
		return validationResult
	}
	validationResult["filename"] = filename
	validationResult["file_data"] = string(data)
	validationResult["needs_confirmation"] = true
	return validationResult
}

// ConfirmImportProfiles confirms and executes import after user approval.
func (a *App) ConfirmImportProfiles(jsonData string) map[string]interface{} {
	return a.ImportAllProfiles(jsonData)
}

// ============================================================================
// Routing Mode API methods
// ============================================================================

// GetRoutingMode returns current routing mode
func (a *App) GetRoutingMode() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()
	mode := settings.RoutingMode

	// Default to blocked_only if empty
	if mode == "" {
		mode = DefaultRoutingMode
	}

	// Get mode descriptions for UI
	modeDescriptions := map[string]string{
		string(RoutingModeBlockedOnly):  "Только заблокированные",
		string(RoutingModeExceptRussia): "Всё кроме России",
		string(RoutingModeAllTraffic):   "Весь трафик",
	}

	return map[string]interface{}{
		"success":     true,
		"mode":        string(mode),
		"description": modeDescriptions[string(mode)],
		"modes": []map[string]string{
			{"value": string(RoutingModeBlockedOnly), "label": "Только заблокированные", "description": "Через VPN идут только заблокированные сайты (РКН + сервисы, блокирующие РФ). Минимальная нагрузка на VPN."},
			{"value": string(RoutingModeExceptRussia), "label": "Всё кроме России", "description": "Весь зарубежный трафик через VPN, российские сайты напрямую."},
			{"value": string(RoutingModeAllTraffic), "label": "Весь трафик", "description": "Весь трафик через VPN. Максимальная приватность, высокая нагрузка."},
		},
	}
}

// SetRoutingMode sets routing mode and rebuilds config
func (a *App) SetRoutingMode(mode string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	// Validate mode
	routingMode := RoutingMode(mode)
	switch routingMode {
	case RoutingModeBlockedOnly, RoutingModeExceptRussia, RoutingModeAllTraffic:
		// Valid mode
	default:
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Неизвестный режим маршрутизации: %s", mode),
		}
	}

	// Check if VPN is running
	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()

	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить режим пока VPN активен. Сначала отключите VPN.",
		}
	}

	// Update settings
	settings := a.storage.GetAppSettings()
	settings.RoutingMode = routingMode

	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	// Update config builder
	if a.configBuilder != nil {
		a.configBuilder.SetRoutingMode(routingMode)
	}

	// Rebuild config for active profile
	if err := a.RebuildActiveProfileConfig(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка перестройки конфига: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Routing mode changed to: %s", mode))

	return map[string]interface{}{
		"success": true,
		"message": "Режим маршрутизации изменён",
		"mode":    mode,
	}
}

// ============================================================================
// Filters API methods
// ============================================================================

// GetFiltersInfo returns information about bundled filters
func (a *App) GetFiltersInfo() map[string]interface{} {
	a.waitForInit()

	// Create filter manager pointing to bin/filters
	filterManager := NewFilterManager(a.runtimeBasePath())

	info, err := filterManager.GetInfo()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка получения информации о фильтрах: %v", err),
		}
	}

	files := filterManager.GetFilterFiles()

	return map[string]interface{}{
		"success":        true,
		"version":        info.Version,
		"updated_at":     info.UpdatedAt,
		"days_old":       info.DaysOld,
		"max_age_days":   info.MaxAgeDays,
		"is_outdated":    info.IsOutdated,
		"filter_count":   info.FilterCount,
		"total_size_kb":  info.TotalSizeKB,
		"update_message": info.UpdateMessage,
		"can_update":     info.CanUpdate,
		"files":          files,
	}
}

// UpdateFilters is intentionally disabled at runtime. Routing filters are
// updated by the release build pipeline and shipped as reviewed bundled assets.
func (a *App) UpdateFilters() map[string]interface{} {
	a.waitForInit()

	return map[string]interface{}{
		"success": false,
		"started": false,
		"error":   "Обновление баз выполняется только при сборке приложения.",
	}
}

// ============================================================================
// Free Access API methods (developing.md §4) — opening blocked-in-RF
// services without a VPN key, via local DPI-bypass methods (ByeDPI).
// ============================================================================

// GetFreeAccessConfig returns the current "Free access" settings and the
// list of services for the settings UI, each with its enabled state.
func (a *App) GetFreeAccessConfig() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()
	methodOptions := FreeAccessServiceMethodOptions()
	storedStrategies, _ := a.loadFreeAccessStrategies()
	serviceFallbackCache := a.loadServiceStrategyCache()
	transparentTags := a.availableTransparentStrategyTags()
	hasVPNProxy := false
	if configPath := a.storage.ActiveConfigFilePath(); configPath != "" {
		if ok, err := configHasVPNProbeCandidates(configPath); err == nil {
			hasVPNProxy = ok
		}
	}

	services := make([]map[string]interface{}, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		selectedMethod := FreeAccessServiceMethod(settings, svc.Tag)
		effectiveMethod := selectedMethod
		effectiveSource := "manual"
		if selectedMethod == FreeAccessMethodAuto {
			effective := a.selectFreeAccessStrategyForService(settings, svc, storedStrategies, serviceFallbackCache, map[string]bool{}, transparentTags, hasVPNProxy)
			effectiveMethod = effective.MethodTag
			effectiveSource = effective.Source
			if effectiveMethod == "" {
				effectiveMethod = FreeAccessMethodAuto
				effectiveSource = "auto"
			}
		}
		services = append(services, map[string]interface{}{
			"tag":                  svc.Tag,
			"name":                 svc.DisplayName,
			"domainSuffixes":       append([]string(nil), svc.DomainSuffixes...),
			"ipCidrs":              append([]string(nil), svc.IPCIDRs...),
			"enabled":              true,
			"requiresVpn":          svc.RequiresVPN,
			"selectedMethod":       selectedMethod,
			"methodLabel":          FreeAccessOutboundLabel(selectedMethod),
			"effectiveMethod":      effectiveMethod,
			"effectiveMethodLabel": FreeAccessOutboundLabel(effectiveMethod),
			"effectiveSource":      effectiveSource,
		})
	}

	byeDPIInstalled := false
	byeDPIRunning := false
	if a.byeDPI != nil {
		byeDPIInstalled = a.byeDPI.IsInstalled()
		byeDPIRunning = a.byeDPI.IsRunning()
	}

	return map[string]interface{}{
		"success":            true,
		"enabled":            FreeMethodsAllowed(settings),
		"reverse":            false,
		"disableFreeAccess":  settings.DisableFreeAccess,
		"freeMethodsAllowed": FreeMethodsAllowed(settings),
		"services":           services,
		"methodOptions":      methodOptions,
		"byedpiInstalled":    byeDPIInstalled,
		"byedpiRunning":      byeDPIRunning,
		"methodCache":        a.routeProbeCacheSummary(),
	}
}

// SetFreeAccessEnabled toggles the "Free access" master switch and rebuilds config.
func (a *App) SetFreeAccessEnabled(enabled bool) map[string]interface{} {
	return a.SetDisableFreeAccess(!enabled)
}

// SetDisableFreeAccess toggles the opt-out from automatic free DPI-bypass
// methods. Free methods are the default strategy; this switch requires an
// explicit VPN/subscription or WireGuard-only setup at connection time.
func (a *App) SetDisableFreeAccess(disabled bool) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()
	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить настройки пока VPN активен. Сначала отключите VPN.",
		}
	}

	settings := a.storage.GetAppSettings()
	settings.DisableFreeAccess = disabled
	settings.FreeAccessEnabled = true
	settings.FreeAccessReverse = false

	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	if err := a.RebuildActiveProfileConfig(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка перестройки конфига: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Free methods disabled: %v", disabled))

	return map[string]interface{}{
		"success":           true,
		"disableFreeAccess": disabled,
		"enabled":           !disabled,
	}
}

// SetFreeAccessReverse toggles the preference for ByeDPI candidates before
// VPN candidates in blocked-service urltest groups and rebuilds config.
func (a *App) SetFreeAccessReverse(reverse bool) map[string]interface{} {
	a.writeLog("Ignoring deprecated FreeAccessReverse setting; route probe selects by latency")
	return map[string]interface{}{
		"success": true,
		"reverse": false,
	}
}

// ToggleFreeAccessService enables/disables a single service in the "Free access" list.
func (a *App) ToggleFreeAccessService(tag string, enabled bool) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	found := false
	for _, svc := range DefaultFreeAccessServices {
		if svc.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Неизвестный сервис: %s", tag),
		}
	}

	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()
	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить настройки пока VPN активен. Сначала отключите VPN.",
		}
	}

	settings := a.storage.GetAppSettings()
	if settings.FreeAccessServices == nil {
		settings.FreeAccessServices = DefaultFreeAccessServiceState()
	}
	settings.FreeAccessServices[tag] = enabled

	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	if err := a.RebuildActiveProfileConfig(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка перестройки конфига: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Free access service %s: %v", tag, enabled))

	return map[string]interface{}{
		"success": true,
		"tag":     tag,
		"enabled": enabled,
	}
}

// SetFreeAccessServiceMethod forces a route method for a blocked service.
// "auto" keeps the latency-based picker; other values pin the service to
// direct, VPN subscription, or one of the bundled free methods.
func (a *App) SetFreeAccessServiceMethod(tag string, method string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	found := false
	for _, svc := range DefaultFreeAccessServices {
		if svc.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Неизвестный сервис: %s", tag),
		}
	}

	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()
	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить метод пока VPN активен. Сначала отключите VPN.",
		}
	}

	normalized := NormalizeFreeAccessServiceMethod(method)
	settings := a.storage.GetAppSettings()
	if settings.FreeAccessMethods == nil {
		settings.FreeAccessMethods = DefaultFreeAccessServiceMethodState()
	}
	settings.FreeAccessMethods[tag] = normalized

	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	if err := a.RebuildActiveProfileConfig(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка перестройки конфига: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Free access service %s method: %s", tag, normalized))

	return map[string]interface{}{
		"success": true,
		"tag":     tag,
		"method":  normalized,
	}
}

// ============================================================================
// RU-traffic API methods (developing.md §5) — RU domains are direct by
// default in every routing mode; this opt-in hides them behind a proxy.
// ============================================================================

// GetHideRuTraffic returns the current "Скрывать RU-трафик" setting.
func (a *App) GetHideRuTraffic() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	settings := a.storage.GetAppSettings()

	return map[string]interface{}{
		"success":      true,
		"enabled":      settings.HideRuTraffic,
		"proxyAddress": settings.RuProxyAddress,
	}
}

// SetHideRuTraffic toggles RU-traffic hiding and optionally sets a dedicated
// RU proxy address. proxyAddress may be empty (falls back to the main VPN
// proxy, then direct — see developing.md §5).
func (a *App) SetHideRuTraffic(enabled bool, proxyAddress string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	a.mu.Lock()
	isRunning := a.isRunning
	a.mu.Unlock()
	if isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменить настройки пока VPN активен. Сначала отключите VPN.",
		}
	}

	if enabled && proxyAddress != "" && a.configBuilder != nil {
		result, err := a.configBuilder.TestSubscription(proxyAddress)
		if err != nil || !result.Success {
			errMsg := "Адрес прокси недействителен"
			if result != nil && result.Error != "" {
				errMsg = result.Error
			}
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Ошибка проверки адреса прокси для RU-трафика: %s", errMsg),
			}
		}
	}

	settings := a.storage.GetAppSettings()
	settings.HideRuTraffic = enabled
	settings.RuProxyAddress = proxyAddress

	if err := a.storage.UpdateAppSettings(settings); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка сохранения настроек: %v", err),
		}
	}

	if err := a.RebuildActiveProfileConfig(); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка перестройки конфига: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Hide RU traffic: %v (proxy configured: %v)", enabled, proxyAddress != ""))

	return map[string]interface{}{
		"success": true,
		"enabled": enabled,
	}
}

// RebuildActiveProfileConfig rebuilds config for active profile
func (a *App) RebuildActiveProfileConfig() error {
	if a.storage == nil {
		return fmt.Errorf("storage not initialized")
	}

	profile, err := a.storage.GetActiveProfile()
	if err != nil || profile == nil {
		return fmt.Errorf("no active profile: %v", err)
	}

	// Get routing mode from settings
	settings := a.storage.GetAppSettings()
	if a.configBuilder != nil {
		a.configBuilder.SetRoutingMode(settings.RoutingMode)
	}

	// Rebuild using config builder
	return a.configBuilder.BuildConfig(profile.SubscriptionURL)
}
