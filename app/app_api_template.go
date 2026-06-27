package main

// Template methods for dropo.
// This file contains template.json management

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// HasTemplate проверяет наличие template.json
func (a *App) HasTemplate() bool {
	if a.storage == nil {
		return false
	}
	return fileExists(a.storage.GetTemplatePath())
}

// GetTemplateContent возвращает содержимое template.json
func (a *App) GetTemplateContent() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
			"content": "",
		}
	}

	templatePath := a.storage.GetTemplatePath()
	content, err := os.ReadFile(templatePath)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Не удалось прочитать template.json: %v", err),
			"content": "",
		}
	}

	return map[string]interface{}{
		"success": true,
		"content": string(content),
	}
}

// SaveTemplateContent сохраняет содержимое template.json
func (a *App) SaveTemplateContent(content string) map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	templatePath := a.storage.GetTemplatePath()

	// Валидируем JSON перед сохранением
	var jsonTest interface{}
	if err := json.Unmarshal([]byte(content), &jsonTest); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Некорректный JSON: %v", err),
		}
	}

	// Форматируем JSON для читабельности
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, []byte(content), "", "  "); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка форматирования JSON: %v", err),
		}
	}

	if err := os.WriteFile(templatePath, prettyJSON.Bytes(), 0644); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Не удалось сохранить template.json: %v", err),
		}
	}

	a.writeLog("Template.json обновлён пользователем")

	return map[string]interface{}{
		"success": true,
	}
}

// ResetTemplate сбрасывает template.json к оригинальному состоянию
func (a *App) ResetTemplate() map[string]interface{} {
	a.waitForInit()

	if a.storage == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Storage не инициализирован",
		}
	}

	templatePath := a.storage.GetTemplatePath()

	// Используем функцию из main.go для копирования embedded template
	if err := copyEmbeddedTemplate(templatePath); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Не удалось сбросить template.json: %v", err),
		}
	}

	a.writeLog("Template.json сброшен к оригинальному состоянию")

	return map[string]interface{}{
		"success": true,
	}
}
