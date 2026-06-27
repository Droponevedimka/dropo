// Package main provides settings import/export functionality for dropo.
package main

import (
	"encoding/json"
	"fmt"
	"time"
)

// FullExportData represents complete exportable application data.
// This includes ALL profiles with ALL their settings.
type FullExportData struct {
	Version         string            `json:"version"`          // App version that created export
	ExportedAt      time.Time         `json:"exported_at"`      // Export timestamp
	SchemaVersion   int               `json:"schema_version"`   // Settings schema version
	AppSettings     GlobalAppSettings `json:"app_settings"`     // Global application settings
	Profiles        []ProfileData     `json:"profiles"`         // ALL profiles with configs
	TemplateContent string            `json:"template_content"` // Custom template.json content
}

// ExportAllProfiles exports ALL profiles and settings to JSON.
// Returns JSON string that can be saved to file.
func (a *App) ExportAllProfiles() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	export := FullExportData{
		Version:       Version,
		ExportedAt:    time.Now(),
		SchemaVersion: SettingsVersion,
	}

	// Export app settings
	export.AppSettings = a.storage.GetAppSettings()

	// Export ALL profiles with their configs
	export.Profiles = a.storage.GetAllProfiles()

	// Export template content
	templatePath := a.storage.GetTemplatePath()
	if templatePath != "" {
		content, err := readFileContent(templatePath)
		if err == nil {
			export.TemplateContent = content
		}
	}

	data, err := json.MarshalIndent(export, "", "  ")
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка экспорта: %v", err),
		}
	}

	return map[string]interface{}{
		"success":        true,
		"data":           string(data),
		"profiles_count": len(export.Profiles),
		"version":        Version,
	}
}

// ValidateImportData validates JSON import data without applying it.
// Returns validation result and parsed data info.
func (a *App) ValidateImportData(jsonData string) map[string]interface{} {
	if jsonData == "" {
		return map[string]interface{}{
			"success": false,
			"error":   "Пустые данные для импорта",
		}
	}

	// Try to parse JSON
	var export FullExportData
	if err := json.Unmarshal([]byte(jsonData), &export); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Неверный формат JSON: %v", err),
		}
	}

	// Validate required fields
	if len(export.Profiles) == 0 {
		return map[string]interface{}{
			"success": false,
			"error":   "Файл не содержит профилей",
		}
	}

	// Validate template if present
	if export.TemplateContent != "" {
		var templateTest interface{}
		if err := json.Unmarshal([]byte(export.TemplateContent), &templateTest); err != nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Неверный формат шаблона: %v", err),
			}
		}
	}

	// Validate each profile
	profileNames := []string{}
	totalWireGuard := 0
	for _, p := range export.Profiles {
		if p.Name == "" {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("Профиль ID=%d не имеет имени", p.ID),
			}
		}
		profileNames = append(profileNames, p.Name)
		totalWireGuard += len(p.WireGuardConfigs)
	}

	return map[string]interface{}{
		"success":           true,
		"version":           export.Version,
		"exported_at":       export.ExportedAt.Format("2006-01-02 15:04:05"),
		"profiles_count":    len(export.Profiles),
		"profile_names":     profileNames,
		"wireguard_count":   totalWireGuard,
		"has_template":      export.TemplateContent != "",
		"has_app_settings":  true,
		"active_profile_id": export.AppSettings.ActiveProfileID,
	}
}

// ImportAllProfiles imports ALL profiles from JSON, replacing existing ones.
// This is a FULL REPLACE operation - all existing profiles will be deleted!
func (a *App) ImportAllProfiles(jsonData string) map[string]interface{} {
	a.waitForInit()

	// Check VPN is not running
	a.mu.Lock()
	if a.isRunning {
		a.mu.Unlock()
		return map[string]interface{}{
			"success": false,
			"error":   "Нельзя импортировать пока VPN активен. Сначала отключите VPN.",
		}
	}
	a.mu.Unlock()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	// Validate first
	validationResult := a.ValidateImportData(jsonData)
	if !validationResult["success"].(bool) {
		return validationResult
	}

	// Parse data
	var export FullExportData
	if err := json.Unmarshal([]byte(jsonData), &export); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка парсинга: %v", err),
		}
	}

	// Import app settings
	a.storage.UpdateAppSettings(export.AppSettings)

	// Import ALL profiles (this replaces existing profiles)
	if err := a.storage.ReplaceAllProfiles(export.Profiles); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка импорта профилей: %v", err),
		}
	}

	// Import template if present
	if export.TemplateContent != "" {
		templatePath := a.storage.GetTemplatePath()
		if templatePath != "" {
			if err := writeFileContent(templatePath, export.TemplateContent); err != nil {
				a.writeLog(fmt.Sprintf("Warning: failed to import template: %v", err))
			}
		}
	}

	// Set active profile
	activeID := export.AppSettings.ActiveProfileID
	if activeID == 0 && len(export.Profiles) > 0 {
		activeID = export.Profiles[0].ID
	}
	a.storage.SetActiveProfileID(activeID)

	// Rebuild config for active profile
	if a.configBuilder != nil {
		settings, err := a.storage.GetUserSettings()
		if err == nil {
			a.configBuilder.BuildConfigForProfile(activeID, settings.SubscriptionURL, settings.WireGuardConfigs)
		}
	}

	a.writeLog(fmt.Sprintf("Imported %d profiles successfully", len(export.Profiles)))
	a.AddToLogBuffer(fmt.Sprintf("Импортировано %d профилей", len(export.Profiles)))

	return map[string]interface{}{
		"success":        true,
		"message":        fmt.Sprintf("Успешно импортировано %d профилей", len(export.Profiles)),
		"profiles_count": len(export.Profiles),
		"active_profile": activeID,
	}
}

// ============================================================================
// Legacy methods for backward compatibility
// ============================================================================

// ExportData represents exportable data (legacy format).
type ExportData struct {
	Version          string                `json:"version"`
	ExportedAt       time.Time             `json:"exported_at"`
	AppSettings      GlobalAppSettings     `json:"app_settings"`
	Profiles         []ProfileData         `json:"profiles"`
	WireGuardConfigs []UserWireGuardConfig `json:"wireguard_configs"`
	TemplateContent  string                `json:"template_content"`
}

// ExportSettings exports settings (legacy method, calls ExportAllProfiles).
func (a *App) ExportSettings() map[string]interface{} {
	return a.ExportAllProfiles()
}

// ImportSettings imports settings (legacy method, calls ImportAllProfiles).
func (a *App) ImportSettings(jsonData string) map[string]interface{} {
	return a.ImportAllProfiles(jsonData)
}

// readFileContent reads file content as string.
func readFileContent(path string) (string, error) {
	data, err := readFile(path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// writeFileContent writes string content to file.
func writeFileContent(path, content string) error {
	return writeFile(path, []byte(content))
}
