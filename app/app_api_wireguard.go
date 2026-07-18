package main

// WireGuard API methods for dropo.
// This file contains WireGuard configuration management
// Supports both sing-box integration and Native WireGuard tunnels

import (
	"fmt"
	"strings"
)

// GetWireGuardList возвращает список WireGuard конфигов
func (a *App) GetWireGuardList() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
			"configs": []WireGuardInfo{},
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"configs": []WireGuardInfo{},
		}
	}

	configs := make([]WireGuardInfo, 0, len(settings.WireGuardConfigs))
	for _, wg := range settings.WireGuardConfigs {
		configs = append(configs, wg.ToInfo())
	}

	return map[string]interface{}{
		"success": true,
		"configs": configs,
		"count":   len(configs),
	}
}

// GetWireGuardHealth возвращает статус здоровья WireGuard туннелей
func (a *App) GetWireGuardHealth() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard не инициализирован",
			"tunnels": []map[string]interface{}{},
		}
	}

	tunnels := a.nativeWG.GetTunnelHealthStatus()
	status := a.nativeWG.GetStatus()

	return map[string]interface{}{
		"success":      true,
		"tunnels":      tunnels,
		"tunnel_count": len(tunnels),
		"wg_installed": status["installed"],
		"wg_version":   status["version"],
	}
}

// ParseWireGuardConfigAPI парсит WireGuard конфиг и возвращает результат
func (a *App) ParseWireGuardConfigAPI(configText string) map[string]interface{} {
	wg, err := ParseWireGuardConfig(configText)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	endpoint := wg.Endpoint
	if wg.EndpointPort > 0 {
		endpoint = fmt.Sprintf("%s:%d", wg.Endpoint, wg.EndpointPort)
	}

	return map[string]interface{}{
		"success":              true,
		"private_key":          wg.PrivateKey,
		"local_address":        wg.LocalAddress,
		"dns":                  wg.DNS,
		"mtu":                  wg.MTU,
		"public_key":           wg.PublicKey,
		"preshared_key":        wg.PresharedKey,
		"allowed_ips":          wg.AllowedIPs,
		"endpoint":             endpoint,
		"endpoint_port":        wg.EndpointPort,
		"persistent_keepalive": wg.PersistentKeepalive,
	}
}

// AddWireGuard добавляет новый WireGuard конфиг
func (a *App) AddWireGuard(tag string, name string, configText string, camouflageEnabled bool) map[string]interface{} {
	a.waitForInit()

	// Проверяем что VPN выключен
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя добавлять VPN пока соединение активно. Сначала отключите VPN.",
		}
	}
	a.mu.Unlock()

	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	// Валидируем тег
	if err := ValidateTag(tag); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Парсим конфиг
	wg, err := ParseWireGuardConfig(configText)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка парсинга конфига: %v", err),
		}
	}

	// Валидируем AllowedIPs на конфликты с sing-box TUN
	if err := ValidateAllowedIPs(wg.AllowedIPs); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	wg.Tag = tag
	wg.Name = name
	wg.CamouflageEnabled = camouflageEnabled
	if wg.Name == "" {
		wg.Name = tag
	}

	// Загружаем текущие настройки
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Проверяем лимит
	if len(settings.WireGuardConfigs) >= MaxWireGuardConfigs {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Достигнут лимит WireGuard конфигов (%d)", MaxWireGuardConfigs),
		}
	}

	// Проверяем уникальность тега
	for _, existing := range settings.WireGuardConfigs {
		if existing.Tag == tag {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Конфиг с тегом '%s' уже существует", tag),
			}
		}
	}

	// Добавляем конфиг
	settings.WireGuardConfigs = append(settings.WireGuardConfigs, *wg)

	// Перегенерируем конфиг
	if err := a.configBuilder.BuildConfigForProfile(a.storage.GetActiveProfileID(), settings.SubscriptionURL, settings.WireGuardConfigs); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
		"count":   len(settings.WireGuardConfigs),
	}
}

// UpdateWireGuard обновляет существующий WireGuard конфиг
func (a *App) UpdateWireGuard(oldTag string, tag string, name string, configText string, camouflageEnabled bool) map[string]interface{} {
	a.waitForInit()

	// Проверяем что VPN выключен
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя редактировать VPN пока соединение активно. Сначала отключите VPN.",
		}
	}
	a.mu.Unlock()

	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	// Валидируем новый тег
	if err := ValidateTag(tag); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Парсим конфиг
	wg, err := ParseWireGuardConfig(configText)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка парсинга конфига: %v", err),
		}
	}

	// Валидируем AllowedIPs на конфликты с sing-box TUN
	if err := ValidateAllowedIPs(wg.AllowedIPs); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	wg.Tag = tag
	wg.Name = name
	wg.CamouflageEnabled = camouflageEnabled
	if wg.Name == "" {
		wg.Name = tag
	}

	// Загружаем текущие настройки
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Находим и обновляем конфиг
	found := false
	for i, existing := range settings.WireGuardConfigs {
		if existing.Tag == oldTag {
			// Проверяем уникальность нового тега (если изменился)
			if tag != oldTag {
				for _, other := range settings.WireGuardConfigs {
					if other.Tag == tag {
						return map[string]interface{}{
							"success": false,
							"error":   fmt.Sprintf("Конфиг с тегом '%s' уже существует", tag),
						}
					}
				}
			}
			settings.WireGuardConfigs[i] = *wg
			found = true
			break
		}
	}

	if !found {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Конфиг с тегом '%s' не найден", oldTag),
		}
	}

	// Перегенерируем конфиг
	if err := a.configBuilder.BuildConfigForProfile(a.storage.GetActiveProfileID(), settings.SubscriptionURL, settings.WireGuardConfigs); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
	}
}

// DeleteWireGuard удаляет WireGuard конфиг
func (a *App) DeleteWireGuard(tag string) map[string]interface{} {
	a.waitForInit()

	// Проверяем что VPN выключен
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя удалять VPN пока соединение активно. Сначала отключите VPN.",
		}
	}
	a.mu.Unlock()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	// Загружаем текущие настройки
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Находим и удаляем конфиг
	newConfigs := make([]UserWireGuardConfig, 0, len(settings.WireGuardConfigs)-1)
	found := false
	for _, existing := range settings.WireGuardConfigs {
		if existing.Tag == tag {
			found = true
			continue
		}
		newConfigs = append(newConfigs, existing)
	}

	if !found {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Конфиг с тегом '%s' не найден", tag),
		}
	}

	settings.WireGuardConfigs = newConfigs

	// Перегенерируем конфиг
	if err := a.configBuilder.BuildConfigForProfile(a.storage.GetActiveProfileID(), settings.SubscriptionURL, settings.WireGuardConfigs); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
		"count":   len(settings.WireGuardConfigs),
	}
}

// GetWireGuardConfig возвращает полный конфиг WireGuard для редактирования
func (a *App) GetWireGuardConfig(tag string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	for _, wg := range settings.WireGuardConfigs {
		if wg.Tag == tag {
			endpoint := wg.Endpoint
			if wg.EndpointPort > 0 {
				endpoint = fmt.Sprintf("%s:%d", wg.Endpoint, wg.EndpointPort)
			}

			return map[string]interface{}{
				"success":              true,
				"tag":                  wg.Tag,
				"name":                 wg.Name,
				"private_key":          wg.PrivateKey,
				"local_address":        strings.Join(wg.LocalAddress, ", "),
				"dns":                  wg.DNS,
				"mtu":                  wg.MTU,
				"public_key":           wg.PublicKey,
				"preshared_key":        wg.PresharedKey,
				"allowed_ips":          wg.AllowedIPs,
				"endpoint":             endpoint,
				"persistent_keepalive": wg.PersistentKeepalive,
				"internal_domains":     wg.InternalDomains,
				"camouflage_enabled":   wg.CamouflageEnabled,
			}
		}
	}

	return map[string]interface{}{
		"success": false,
		"error":   fmt.Sprintf("Конфиг с тегом '%s' не найден", tag),
	}
}

// UpdateWireGuardInternalDomains обновляет список внутренних доменов для WireGuard конфига
// Эти домены будут резолвиться через системный DNS (WireGuard DNS) вместо hijack-dns
func (a *App) UpdateWireGuardInternalDomains(tag string, domains []string) map[string]interface{} {
	a.waitForInit()

	// Проверяем что VPN выключен
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя изменять настройки пока VPN активен. Сначала отключите VPN.",
		}
	}
	a.mu.Unlock()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Находим конфиг по тегу
	foundIndex := -1
	for i, wg := range settings.WireGuardConfigs {
		if wg.Tag == tag {
			foundIndex = i
			break
		}
	}

	if foundIndex == -1 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Конфиг с тегом '%s' не найден", tag),
		}
	}

	// Нормализуем домены (убираем пробелы, добавляем точку в начало)
	normalizedDomains := make([]string, 0, len(domains))
	for _, d := range domains {
		d = strings.TrimSpace(strings.ToLower(d))
		if d == "" {
			continue
		}
		if !strings.HasPrefix(d, ".") {
			d = "." + d
		}
		normalizedDomains = append(normalizedDomains, d)
	}

	// Обновляем домены
	settings.WireGuardConfigs[foundIndex].InternalDomains = normalizedDomains

	// Перегенерируем sing-box конфиг
	if err := a.configBuilder.BuildConfigForProfile(
		a.storage.GetActiveProfileID(),
		settings.SubscriptionURL,
		settings.WireGuardConfigs,
	); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Собираем все внутренние домены для информации
	allDomains := CollectAllInternalDomains(settings.WireGuardConfigs)

	return map[string]interface{}{
		"success":       true,
		"tag":           tag,
		"domains":       normalizedDomains,
		"all_domains":   allDomains,
		"domains_count": len(normalizedDomains),
	}
}

// GetAllInternalDomains возвращает все собранные внутренние домены из всех WireGuard конфигов
func (a *App) GetAllInternalDomains() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
			"domains": []string{},
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"domains": []string{},
		}
	}

	domains := CollectAllInternalDomains(settings.WireGuardConfigs)

	return map[string]interface{}{
		"success":         true,
		"domains":         domains,
		"count":           len(domains),
		"wireguard_count": len(settings.WireGuardConfigs),
	}
}

// =============================================================================
// Native WireGuard API (Windows Service based)
// =============================================================================

// GetNativeWireGuardStatus returns the status of Native WireGuard Manager
func (a *App) GetNativeWireGuardStatus() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success":   false,
			"installed": false,
			"error":     "Native WireGuard Manager not initialized",
		}
	}

	status := a.nativeWG.GetStatus()
	status["success"] = true
	return status
}

// StartNativeWireGuard starts a WireGuard tunnel using Native Windows Service
func (a *App) StartNativeWireGuard(tag string) map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	// Check if WireGuard is installed
	if !a.nativeWG.IsInstalled() {
		return map[string]interface{}{
			"success":          false,
			"error":            "WireGuard не установлен",
			"install_required": true,
		}
	}

	// Get WireGuard config from storage
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Find config by tag
	var foundConfig *UserWireGuardConfig
	var configIndex int
	for i, wg := range settings.WireGuardConfigs {
		if wg.Tag == tag {
			foundConfig = &settings.WireGuardConfigs[i]
			configIndex = i
			break
		}
	}

	if foundConfig == nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Конфиг '%s' не найден", tag),
		}
	}

	// Convert to WireGuardConfig format for native manager
	nativeConfig := foundConfig.ToWireGuardConfig()

	// Start the tunnel
	if err := a.nativeWG.StartTunnel(configIndex, nativeConfig); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка запуска туннеля: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Native WireGuard tunnel started: %s", tag))

	return map[string]interface{}{
		"success": true,
		"tunnel":  fmt.Sprintf("%s%d", TunnelPrefix, configIndex),
		"tag":     tag,
	}
}

// StopNativeWireGuard stops a WireGuard tunnel
func (a *App) StopNativeWireGuard(tag string) map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	// Get WireGuard config from storage to find index
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Find config by tag
	configIndex := -1
	for i, wg := range settings.WireGuardConfigs {
		if wg.Tag == tag {
			configIndex = i
			break
		}
	}

	if configIndex < 0 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Конфиг '%s' не найден", tag),
		}
	}

	// Stop the tunnel
	if err := a.nativeWG.StopTunnel(configIndex); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка остановки туннеля: %v", err),
		}
	}

	a.writeLog(fmt.Sprintf("Native WireGuard tunnel stopped: %s", tag))

	return map[string]interface{}{
		"success": true,
		"tag":     tag,
	}
}

// StopAllNativeWireGuard stops all active WireGuard tunnels
func (a *App) StopAllNativeWireGuard() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	a.nativeWG.StopAllTunnels()
	a.writeLog("All Native WireGuard tunnels stopped")

	return map[string]interface{}{
		"success": true,
	}
}

// StartAllNativeWireGuard starts all WireGuard configs as native tunnels
func (a *App) StartAllNativeWireGuard() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	if !a.nativeWG.IsInstalled() {
		return map[string]interface{}{
			"success":          false,
			"error":            "WireGuard не установлен",
			"install_required": true,
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	started := 0
	errors := []string{}

	for i, wg := range settings.WireGuardConfigs {
		nativeConfig := wg.ToWireGuardConfig()
		if err := a.nativeWG.StartTunnel(i, nativeConfig); err != nil {
			errors = append(errors, fmt.Sprintf("%s: %v", wg.Tag, err))
		} else {
			started++
		}
	}

	result := map[string]interface{}{
		"success": len(errors) == 0,
		"started": started,
		"total":   len(settings.WireGuardConfigs),
	}

	if len(errors) > 0 {
		result["errors"] = errors
	}

	a.writeLog(fmt.Sprintf("Started %d/%d Native WireGuard tunnels", started, len(settings.WireGuardConfigs)))

	return result
}

// GetNativeWireGuardTunnels returns list of active native tunnels
func (a *App) GetNativeWireGuardTunnels() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
			"tunnels": []TunnelState{},
		}
	}

	tunnels := a.nativeWG.GetActiveTunnels()

	// Enrich with config names
	settings, _ := a.storage.GetUserSettings()
	enrichedTunnels := make([]map[string]interface{}, 0, len(tunnels))

	for _, t := range tunnels {
		tunnel := map[string]interface{}{
			"name":       t.Name,
			"config_id":  t.ConfigID,
			"started_at": t.StartedAt,
			"active":     t.Active,
		}

		// Find config name
		if settings != nil && t.ConfigID >= 0 && t.ConfigID < len(settings.WireGuardConfigs) {
			tunnel["tag"] = settings.WireGuardConfigs[t.ConfigID].Tag
			tunnel["config_name"] = settings.WireGuardConfigs[t.ConfigID].Name
		}

		enrichedTunnels = append(enrichedTunnels, tunnel)
	}

	return map[string]interface{}{
		"success": true,
		"tunnels": enrichedTunnels,
		"count":   len(enrichedTunnels),
	}
}

// IsNativeWireGuardActive checks if a specific tunnel is active
func (a *App) IsNativeWireGuardActive(tag string) map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"active":  false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	// Find config index by tag
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"active":  false,
			"error":   err.Error(),
		}
	}

	for i, wg := range settings.WireGuardConfigs {
		if wg.Tag == tag {
			active := a.nativeWG.IsTunnelActive(i)
			return map[string]interface{}{
				"success": true,
				"active":  active,
				"tag":     tag,
			}
		}
	}

	return map[string]interface{}{
		"success": false,
		"active":  false,
		"error":   fmt.Sprintf("Конфиг '%s' не найден", tag),
	}
}

// GetWireGuardBundleInfo returns info about bundled WireGuard binaries
func (a *App) GetWireGuardBundleInfo() map[string]interface{} {
	a.waitForInit()

	if a.nativeWG == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Native WireGuard Manager not initialized",
		}
	}

	return map[string]interface{}{
		"success":       true,
		"version":       WireGuardVersion,
		"wintunVersion": WintunVersion,
		"installed":     a.nativeWG.IsInstalled(),
		"wireguardPath": a.nativeWG.wireguardPath,
		"wgPath":        a.nativeWG.wgPath,
	}
}
