package main

// Profile management methods for dropo.
// This file contains profile CRUD operations

import (
	"fmt"
	"time"
)

// GetProfiles возвращает список всех профилей (API для фронтенда)
func (a *App) GetProfiles() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	profiles := a.storage.GetAllProfiles()
	activeID := a.storage.GetActiveProfileID()

	var profilesData []map[string]interface{}
	for _, p := range profiles {
		// Count WireGuard configs
		wgCount := len(p.WireGuardConfigs)
		var wgTags []string
		for _, wg := range p.WireGuardConfigs {
			wgTags = append(wgTags, wg.Tag)
		}

		profilesData = append(profilesData, map[string]interface{}{
			"id":             p.ID,
			"name":           p.Name,
			"subscription":   p.SubscriptionURL,
			"wireguards":     wgTags,
			"wireguardCount": wgCount,
			"isActive":       p.ID == activeID,
			"createdAt":      p.CreatedAt.Format(time.RFC3339),
			"proxyCount":     p.ProxyCount,
		})
	}

	return map[string]interface{}{
		"success":       true,
		"profiles":      profilesData,
		"activeProfile": activeID,
	}
}

// GetActiveProfile возвращает активный профиль (API для фронтенда)
func (a *App) GetActiveProfile() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	profile, err := a.storage.GetActiveProfile()
	if err != nil {
		// Fallback to default profile
		profile, _ = a.storage.GetProfile(DefaultProfileID)
	}

	if profile == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Профиль не найден",
		}
	}

	var wgTags []string
	for _, wg := range profile.WireGuardConfigs {
		wgTags = append(wgTags, wg.Tag)
	}

	return map[string]interface{}{
		"success": true,
		"profile": map[string]interface{}{
			"id":             profile.ID,
			"name":           profile.Name,
			"subscription":   profile.SubscriptionURL,
			"wireguards":     wgTags,
			"wireguardCount": len(profile.WireGuardConfigs),
			"isActive":       true,
			"createdAt":      profile.CreatedAt.Format(time.RFC3339),
			"proxyCount":     profile.ProxyCount,
		},
	}
}

// SetActiveProfile устанавливает активный профиль (API для фронтенда)
func (a *App) SetActiveProfile(id int) map[string]interface{} {
	a.waitForInit()

	// Check if VPN is running - don't allow profile change while connected
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Отключите VPN перед сменой профиля",
		}
	}
	a.mu.Unlock()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	// Verify profile exists
	if _, err := a.storage.GetProfile(id); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Set active profile in storage
	if err := a.storage.SetActiveProfileID(id); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	a.writeLog(fmt.Sprintf("Переключён на профиль %d", id))

	return map[string]interface{}{
		"success": true,
		"message": "Профиль активирован",
	}
}

// CreateProfile создает новый профиль (API для фронтенда)
func (a *App) CreateProfile(name string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	profile, err := a.storage.CreateProfile(name)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
		"profile": map[string]interface{}{
			"id":           profile.ID,
			"name":         profile.Name,
			"subscription": profile.SubscriptionURL,
			"wireguards":   []string{},
			"isActive":     false,
			"createdAt":    profile.CreatedAt.Format(time.RFC3339),
			"proxyCount":   profile.ProxyCount,
		},
	}
}

// UpdateProfile обновляет профиль (API для фронтенда)
func (a *App) UpdateProfile(id int, name string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	if err := a.storage.UpdateProfile(id, name); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
		"message": "Профиль обновлен",
	}
}

// DeleteProfile удаляет профиль (API для фронтенда)
func (a *App) DeleteProfile(id int) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Хранилище не инициализировано",
		}
	}

	if err := a.storage.DeleteProfile(id); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	return map[string]interface{}{
		"success": true,
		"message": "Профиль удален",
	}
}
