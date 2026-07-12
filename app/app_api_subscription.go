package main

// Subscription management methods for dropo.
// This file contains subscription-related API methods

import (
	"fmt"
	"time"
)

// TestSubscription tests a subscription URL and returns available proxies
func (a *App) TestSubscription(url string) map[string]interface{} {
	fetcher := NewSubscriptionFetcher()
	proxies, err := fetcher.FetchAndParse(url)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
			"count":   0,
		}
	}

	// Filter unsupported transports (e.g., xhttp which is Xray-only)
	filterResult := FilterUnsupportedTransports(proxies)
	filteredProxies := filterResult.Supported

	// Convert proxies to simple format for frontend
	proxyList := []map[string]interface{}{}
	for _, p := range filteredProxies {
		proxyList = append(proxyList, map[string]interface{}{
			"type":   p.Type,
			"raw":    p.Raw,
			"name":   p.Name,
			"server": p.Server,
			"port":   p.ServerPort,
		})
	}

	result := map[string]interface{}{
		"success": true,
		"count":   len(filteredProxies),
		"proxies": proxyList,
	}

	// Add warning if some proxies were filtered out
	if len(filterResult.Filtered) > 0 {
		result["warning"] = filterResult.Message
		result["filteredCount"] = len(filterResult.Filtered)
		result["totalOriginal"] = len(proxies)

		// If ALL proxies were filtered, return error
		if filterResult.AllFiltered {
			return map[string]interface{}{
				"success": false,
				"error":   filterResult.Message,
				"count":   0,
			}
		}
	}

	return result
}

// GenerateAndSaveConfig generates config from settings and saves it
func (a *App) GenerateAndSaveConfig() map[string]interface{} {
	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to load settings: %v", err),
		}
	}

	if err := a.configBuilder.BuildConfig(settings.SubscriptionURL); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Failed to generate config: %v", err),
		}
	}

	configPath, _ := a.storage.GetConfigPath()
	return map[string]interface{}{
		"success": true,
		"path":    configPath,
	}
}

// UpdateSubscriptions fetches all subscriptions and regenerates config
func (a *App) UpdateSubscriptions() map[string]interface{} {
	busyID := a.beginBusy("Обновляем подписки...")
	defer a.endBusy(busyID)

	// Stop VPN if running
	wasRunning := a.isVPNRunning()
	if wasRunning {
		a.Stop()
	}

	// Generate new config
	result := a.GenerateAndSaveConfig()
	if !result["success"].(bool) {
		return result
	}

	// Restart VPN if it was running
	if wasRunning {
		a.Start()
	}

	return map[string]interface{}{
		"success":    true,
		"wasRunning": wasRunning,
	}
}

// ==================== Subscription Management (New API) ====================

// GetCurrentSubscription возвращает текущую подписку пользователя
func (a *App) GetCurrentSubscription() map[string]interface{} {
	// Ждём инициализации
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"hasSubscription": false,
			"error":           "Storage не инициализирован",
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return map[string]interface{}{
			"hasSubscription": false,
			"error":           err.Error(),
		}
	}

	if settings.SubscriptionURL == "" {
		return map[string]interface{}{
			"hasSubscription": false,
		}
	}

	return map[string]interface{}{
		"hasSubscription": true,
		"url":             settings.SubscriptionURL,
		"lastUpdated":     settings.LastUpdated,
		"proxyCount":      settings.ProxyCount,
	}
}

// TestVPNConnection тестирует подписку или прямую ссылку
func (a *App) TestVPNConnection(url string) map[string]interface{} {
	busyID := a.beginBusy("Проверяем VPN-подписку...")
	defer a.endBusy(busyID)

	// Ждём инициализации
	a.waitForInit()

	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	a.updateBusy(busyID, "Скачиваем и разбираем список серверов...")
	result, err := a.configBuilder.TestSubscription(url)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success":      result.Success,
		"error":        result.Error,
		"count":        result.Count,
		"isDirectLink": result.IsDirectLink,
		"proxies":      result.Proxies,
	}
}

// SetVPNSubscription устанавливает подписку и генерирует конфиг
func (a *App) SetVPNSubscription(url string) map[string]interface{} {
	busyID := a.beginBusy("Сохраняем VPN-подписку...")
	defer a.endBusy(busyID)

	// Ждём инициализации
	a.waitForInit()

	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	// Останавливаем VPN если запущен
	wasRunning := a.isVPNRunning()
	if wasRunning {
		a.Stop()
	}

	// Генерируем новый конфиг
	a.updateBusy(busyID, "Генерируем новый конфиг...")
	if err := a.configBuilder.BuildConfig(url); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Перезапускаем VPN если был запущен
	if wasRunning {
		go func() {
			// Небольшая задержка чтобы конфиг сохранился
			time.Sleep(500 * time.Millisecond)
			a.Start()
		}()
	}

	// Загружаем обновлённые настройки
	settings, _ := a.storage.GetUserSettings()

	return map[string]interface{}{
		"success":    true,
		"proxyCount": settings.ProxyCount,
	}
}

// RemoveVPNSubscription удаляет подписку и генерирует конфиг без прокси
func (a *App) RemoveVPNSubscription() map[string]interface{} {
	// Ждём инициализации
	a.waitForInit()

	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	// Останавливаем VPN
	wasRunning := a.isVPNRunning()
	if wasRunning {
		a.Stop()
	}

	// Генерируем конфиг без подписки
	if err := a.configBuilder.BuildConfig(""); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success":    true,
		"wasRunning": wasRunning,
	}
}

// RefreshVPNSubscription обновляет текущую подписку
func (a *App) RefreshVPNSubscription() map[string]interface{} {
	if a.configBuilder == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "ConfigBuilder не инициализирован",
		}
	}

	settings, err := a.storage.GetUserSettings()
	if err != nil || settings.SubscriptionURL == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "Нет сохранённой подписки",
		}
	}

	return a.SetVPNSubscription(settings.SubscriptionURL)
}
