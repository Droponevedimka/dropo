// Package main provides application configuration management for dropo.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// AppConfig stores application preferences and settings.
type AppConfig struct {
	// General settings
	AutoStart     bool `json:"auto_start"`    // Launch at system startup
	Notifications bool `json:"notifications"` // Show connection notifications
	CheckUpdates  bool `json:"check_updates"` // Check for updates on startup

	// Logging settings
	EnableLogging bool     `json:"enable_logging"` // Enable sing-box logging
	LogLevel      LogLevel `json:"log_level"`      // Log level (debug/info/warn/error)

	// Appearance
	Theme    Theme    `json:"theme"`    // UI theme (dark/light/system)
	Language Language `json:"language"` // UI language (currently ru only)

	// Subscription settings
	AutoUpdateSub     bool      `json:"auto_update_sub"`     // Auto-update subscription
	SubUpdateInterval int       `json:"sub_update_interval"` // Update interval in hours
	LastSubUpdate     time.Time `json:"last_sub_update"`     // Last subscription update time

	// Update tracking
	LastUpdateCheck string `json:"last_update_check"` // Last update check timestamp

	// Active profile
	ActiveProfileID int `json:"active_profile_id"` // Currently active profile ID
}

// DefaultAppConfig returns default application configuration.
func DefaultAppConfig() *AppConfig {
	return &AppConfig{
		AutoStart:         true,
		Notifications:     true,
		CheckUpdates:      true,
		EnableLogging:     true,
		LogLevel:          LogLevelTrace,
		Theme:             ThemeSystem,
		Language:          LangRussian,
		AutoUpdateSub:     true,
		SubUpdateInterval: 24,
		LastSubUpdate:     time.Time{},
		ActiveProfileID:   DefaultProfileID,
	}
}

// LoadAppConfig loads application configuration from file.
func LoadAppConfig(configPath string) *AppConfig {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return DefaultAppConfig()
	}

	var config AppConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return DefaultAppConfig()
	}

	// Validate and set defaults for missing fields
	if config.LogLevel == "" {
		config.LogLevel = LogLevelTrace
	}
	if config.Theme == "" {
		config.Theme = ThemeSystem
	}
	if config.Language == "" {
		config.Language = LangRussian
	}
	if config.ActiveProfileID == 0 {
		config.ActiveProfileID = DefaultProfileID
	}

	return &config
}

// Save saves application configuration to file.
func (c *AppConfig) Save(configPath string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	return os.WriteFile(configPath, data, 0600)
}

// SetAutoStart enables or disables system startup launch (method on AppConfig).
func (c *AppConfig) SetAutoStart(enable bool) error {
	return SetAutoStart(enable)
}

// GetLogLevelString returns the log level as string for sing-box config.
func (c *AppConfig) GetLogLevelString() string {
	if !c.EnableLogging {
		return string(LogLevelSilent)
	}
	return string(c.LogLevel)
}
