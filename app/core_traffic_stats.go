package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// TrafficData представляет данные о трафике
type TrafficData struct {
	Uploaded   int64         `json:"uploaded"`
	Downloaded int64         `json:"downloaded"`
	Duration   time.Duration `json:"duration"`
	Sessions   int           `json:"sessions,omitempty"`
}

// TrafficStats хранит статистику трафика
type TrafficStats struct {
	// Общая статистика (за всё время)
	Total TrafficData `json:"total"`

	// Статистика последней сессии
	LastSession   TrafficData `json:"last_session"`
	LastStartTime time.Time   `json:"last_start_time"`
	LastEndTime   time.Time   `json:"last_end_time"`

	// Текущая сессия (не сохраняется)
	current      TrafficData
	sessionStart time.Time
	configPath   string // путь к файлу статистики
	mu           sync.RWMutex
}

// NewTrafficStats создаёт новый объект статистики
func NewTrafficStats() *TrafficStats {
	return &TrafficStats{}
}

// LoadTrafficStats загружает статистику из файла
func LoadTrafficStats(configPath string) *TrafficStats {
	data, err := os.ReadFile(configPath)
	if err != nil {
		stats := NewTrafficStats()
		stats.configPath = configPath
		return stats
	}

	var stats TrafficStats
	if err := json.Unmarshal(data, &stats); err != nil {
		stats := NewTrafficStats()
		stats.configPath = configPath
		return stats
	}

	stats.configPath = configPath
	return &stats
}

// Save сохраняет статистику в файл
func (s *TrafficStats) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.configPath == "" {
		return nil
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.configPath, data, 0644)
}

// StartSession начинает новую сессию
func (s *TrafficStats) StartSession() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sessionStart = time.Now()
	s.current = TrafficData{}
	s.Total.Sessions++
}

// EndSession завершает текущую сессию
func (s *TrafficStats) EndSession() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.sessionStart.IsZero() {
		return
	}

	duration := time.Since(s.sessionStart)

	// Обновляем общую статистику
	s.Total.Uploaded += s.current.Uploaded
	s.Total.Downloaded += s.current.Downloaded
	s.Total.Duration += duration

	// Сохраняем как последнюю сессию
	s.LastSession = TrafficData{
		Uploaded:   s.current.Uploaded,
		Downloaded: s.current.Downloaded,
		Duration:   duration,
	}
	s.LastStartTime = s.sessionStart
	s.LastEndTime = time.Now()

	// Сбрасываем текущую сессию
	s.sessionStart = time.Time{}
	s.current = TrafficData{}
}

// UpdateTraffic обновляет статистику трафика
func (s *TrafficStats) UpdateTraffic(upload, download int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.current.Uploaded = upload
	s.current.Downloaded = download
}

// GetCurrentSession возвращает статистику текущей сессии
func (s *TrafficStats) GetCurrentSession() TrafficData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := TrafficData{
		Uploaded:   s.current.Uploaded,
		Downloaded: s.current.Downloaded,
	}

	if !s.sessionStart.IsZero() {
		result.Duration = time.Since(s.sessionStart)
	}

	return result
}

// GetLastSession возвращает статистику последней сессии
func (s *TrafficStats) GetLastSession() TrafficData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.LastSession
}

// GetTotalStats возвращает общую статистику
func (s *TrafficStats) GetTotalStats() TrafficData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.Total
}

// IsSessionActive возвращает true если сессия активна
func (s *TrafficStats) IsSessionActive() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.sessionStart.IsZero()
}

// FormatBytes форматирует байты в читаемый формат
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// FormatDuration форматирует время в читаемый формат
func FormatDuration(d time.Duration) string {
	seconds := int64(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%d сек", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%d мин", seconds/60)
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	return fmt.Sprintf("%d ч %d мин", hours, minutes)
}
