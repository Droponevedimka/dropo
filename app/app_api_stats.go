package main

// Traffic statistics methods for dropo.
// This file contains traffic monitoring and statistics

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"time"
)

// initTrafficStats инициализирует статистику трафика
func (a *App) initTrafficStats() {
	statsPath := a.getTrafficStatsPath()
	a.trafficStats = LoadTrafficStats(statsPath)
}

// getTrafficStatsPath возвращает путь к файлу статистики
func (a *App) getTrafficStatsPath() string {
	if a.storage != nil {
		return filepath.Join(a.storage.GetResourcesPath(), "traffic_stats.json")
	}
	return filepath.Join(a.basePath, "traffic_stats.json")
}

// GetTrafficStats возвращает статистику трафика (API для фронтенда)
func (a *App) GetTrafficStats() map[string]interface{} {
	a.waitForInit()

	if a.trafficStats == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Статистика не загружена",
		}
	}

	current := a.trafficStats.GetCurrentSession()
	last := a.trafficStats.GetLastSession()
	total := a.trafficStats.GetTotalStats()

	return map[string]interface{}{
		"success": true,
		"current": map[string]interface{}{
			"uploaded":      current.Uploaded,
			"downloaded":    current.Downloaded,
			"duration":      int64(current.Duration.Seconds()),
			"uploadedStr":   FormatBytes(current.Uploaded),
			"downloadedStr": FormatBytes(current.Downloaded),
			"durationStr":   FormatDuration(current.Duration),
		},
		"last": map[string]interface{}{
			"uploaded":      last.Uploaded,
			"downloaded":    last.Downloaded,
			"duration":      int64(last.Duration.Seconds()),
			"uploadedStr":   FormatBytes(last.Uploaded),
			"downloadedStr": FormatBytes(last.Downloaded),
			"durationStr":   FormatDuration(last.Duration),
		},
		"total": map[string]interface{}{
			"uploaded":      total.Uploaded,
			"downloaded":    total.Downloaded,
			"duration":      int64(total.Duration.Seconds()),
			"sessions":      total.Sessions,
			"uploadedStr":   FormatBytes(total.Uploaded),
			"downloadedStr": FormatBytes(total.Downloaded),
			"durationStr":   FormatDuration(total.Duration),
		},
	}
}

// ResetTrafficStats сбрасывает статистику трафика
func (a *App) ResetTrafficStats() map[string]interface{} {
	a.waitForInit()

	if a.trafficStats == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Статистика не загружена",
		}
	}

	a.trafficStats.mu.Lock()
	a.trafficStats.Total = TrafficData{}
	a.trafficStats.LastSession = TrafficData{}
	a.trafficStats.mu.Unlock()

	a.trafficStats.Save()

	return map[string]interface{}{
		"success": true,
		"message": "Статистика сброшена",
	}
}

// fetchClashTraffic получает статистику трафика через Clash API
func (a *App) fetchClashTraffic() (upload, download int64) {
	client := &http.Client{Timeout: 2 * time.Second}

	// Используем /connections endpoint для получения суммарного трафика
	resp, err := client.Get("http://127.0.0.1:9090/connections")
	if err != nil {
		return 0, 0
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0
	}

	var connections struct {
		DownloadTotal int64 `json:"downloadTotal"`
		UploadTotal   int64 `json:"uploadTotal"`
	}

	if err := json.Unmarshal(body, &connections); err != nil {
		return 0, 0
	}

	return connections.UploadTotal, connections.DownloadTotal
}

// UpdateTrafficFromClash обновляет статистику трафика из Clash API (вызывается периодически)
func (a *App) UpdateTrafficFromClash() map[string]interface{} {
	if !a.isRunning || a.trafficStats == nil {
		return map[string]interface{}{
			"success": false,
		}
	}

	upload, download := a.fetchClashTraffic()
	a.trafficStats.UpdateTraffic(upload, download)

	return map[string]interface{}{
		"success":  true,
		"upload":   upload,
		"download": download,
	}
}
