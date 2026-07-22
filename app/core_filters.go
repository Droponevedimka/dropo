// Package main provides filter management for routing modes.
// Filters are rule-set files (.srs) used by sing-box for smart routing.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FilterVersion contains metadata about bundled filters.
type FilterVersion struct {
	FiltersVersion string    `json:"filters_version"` // Version string (e.g., "2025.06.04")
	UpdatedAt      time.Time `json:"updated_at"`      // When filters were last updated
	MaxAgeDays     int       `json:"max_age_days"`    // Days before warning (default 30)
	Sources        []string  `json:"sources"`         // Source URLs for reference
	Source         string    `json:"source"`          // Canonical upstream repository
	ReleaseURL     string    `json:"release_url"`     // Exact reviewed upstream release
}

// FilterInfo contains information about filters for UI.
type FilterInfo struct {
	Version       string `json:"version"`        // Filter version
	UpdatedAt     string `json:"updated_at"`     // Human-readable date
	DaysOld       int    `json:"days_old"`       // Days since last update
	MaxAgeDays    int    `json:"max_age_days"`   // Max age before warning
	IsOutdated    bool   `json:"is_outdated"`    // True if older than max_age_days
	FilterCount   int    `json:"filter_count"`   // Number of .srs files
	TotalSizeKB   int    `json:"total_size_kb"`  // Total size in KB
	CanUpdate     bool   `json:"can_update"`     // True if update is available
	UpdateMessage string `json:"update_message"` // Message about update availability
}

// FilterFile represents a single filter file.
type FilterFile struct {
	Name     string `json:"name"`      // Filename without path
	Tag      string `json:"tag"`       // sing-box rule_set tag
	SizeKB   int    `json:"size_kb"`   // Size in KB
	IsLoaded bool   `json:"is_loaded"` // True if file exists
}

// FilterManager manages rule-set filter files.
type FilterManager struct {
	filtersPath string // Path to bin/filters/ directory
	logger      func(string)
}

// Filter file constants
const (
	FiltersFolder      = "filters"
	FiltersVersionFile = "version.json"
	DefaultMaxAgeDays  = 30
)

// Filter file names (must match files in dependencies/filters/)
var FilterFiles = []struct {
	Name string
	Tag  string
}{
	{"refilter_domains.srs", "refilter-domains"},
	{"refilter_ips.srs", "refilter-ips"},
	{"community_domains.srs", "community-domains"},
	{"community_ips.srs", "community-ips"},
	{"discord_ips.srs", "discord-ips"},
}

// NewFilterManager creates a new filter manager.
func NewFilterManager(basePath string) *FilterManager {
	return &FilterManager{
		filtersPath: filepath.Join(basePath, "bin", FiltersFolder),
	}
}

func (fm *FilterManager) SetLogger(logger func(string)) {
	fm.logger = logger
}

func (fm *FilterManager) logf(format string, args ...interface{}) {
	if fm.logger != nil {
		fm.logger(fmt.Sprintf(format, args...))
		return
	}
	fmt.Printf(format+"\n", args...)
}

// GetFiltersPath returns the path to filters directory.
func (fm *FilterManager) GetFiltersPath() string {
	return fm.filtersPath
}

// LoadVersion loads filter version info from version.json.
func (fm *FilterManager) LoadVersion() (*FilterVersion, error) {
	versionPath := filepath.Join(fm.filtersPath, FiltersVersionFile)

	data, err := os.ReadFile(versionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &FilterVersion{
				FiltersVersion: "unknown",
				UpdatedAt:      time.Time{},
				MaxAgeDays:     DefaultMaxAgeDays,
			}, nil
		}
		return nil, fmt.Errorf("failed to read version.json: %w", err)
	}

	var version FilterVersion
	if err := json.Unmarshal(data, &version); err != nil {
		return nil, fmt.Errorf("failed to parse version.json: %w", err)
	}

	// Ensure max_age_days has a default
	if version.MaxAgeDays <= 0 {
		version.MaxAgeDays = DefaultMaxAgeDays
	}

	return &version, nil
}

// SaveVersion saves filter version info to version.json.
func (fm *FilterManager) SaveVersion(version *FilterVersion) error {
	versionPath := filepath.Join(fm.filtersPath, FiltersVersionFile)

	data, err := json.MarshalIndent(version, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal version: %w", err)
	}

	return os.WriteFile(versionPath, data, 0644)
}

// GetInfo returns information about filters for UI.
func (fm *FilterManager) GetInfo() (*FilterInfo, error) {
	version, err := fm.LoadVersion()
	if err != nil {
		return nil, err
	}

	info := &FilterInfo{
		Version:    version.FiltersVersion,
		MaxAgeDays: version.MaxAgeDays,
	}

	// Calculate age
	if !version.UpdatedAt.IsZero() {
		info.UpdatedAt = version.UpdatedAt.Format("2006-01-02")
		info.DaysOld = int(time.Since(version.UpdatedAt).Hours() / 24)
		info.IsOutdated = info.DaysOld > version.MaxAgeDays
	} else {
		info.UpdatedAt = "неизвестно"
		info.DaysOld = -1
		info.IsOutdated = true
	}

	// Count and size filters
	var totalSize int64
	filterCount := 0

	for _, f := range FilterFiles {
		filterPath := filepath.Join(fm.filtersPath, f.Name)
		if stat, err := os.Stat(filterPath); err == nil {
			filterCount++
			totalSize += stat.Size()
		}
	}

	info.FilterCount = filterCount
	info.TotalSizeKB = int(totalSize / 1024)

	// Runtime updates are intentionally disabled. The build/release pipeline
	// owns the reviewed catalog and embeds it into the signed runtime.
	info.CanUpdate = false

	if info.IsOutdated {
		info.UpdateMessage = fmt.Sprintf("Фильтры устарели (обновлены %d дней назад)", info.DaysOld)
	} else if info.DaysOld >= 0 {
		info.UpdateMessage = fmt.Sprintf("Фильтры актуальны (обновлены %d дней назад)", info.DaysOld)
	} else {
		info.UpdateMessage = "Информация о версии недоступна"
	}

	return info, nil
}

// GetFilterFiles returns list of filter files with their status.
func (fm *FilterManager) GetFilterFiles() []FilterFile {
	files := make([]FilterFile, 0, len(FilterFiles))

	for _, f := range FilterFiles {
		filterPath := filepath.Join(fm.filtersPath, f.Name)

		ff := FilterFile{
			Name: f.Name,
			Tag:  f.Tag,
		}

		if stat, err := os.Stat(filterPath); err == nil {
			ff.IsLoaded = true
			ff.SizeKB = int(stat.Size() / 1024)
		}

		files = append(files, ff)
	}

	return files
}

// CheckFreshness checks if filters need update.
// Returns true if filters are outdated.
func (fm *FilterManager) CheckFreshness() (bool, int, error) {
	version, err := fm.LoadVersion()
	if err != nil {
		return true, -1, err
	}

	if version.UpdatedAt.IsZero() {
		return true, -1, nil
	}

	daysOld := int(time.Since(version.UpdatedAt).Hours() / 24)
	return daysOld > version.MaxAgeDays, daysOld, nil
}

// UpdateRefilters is retained for API compatibility with old diagnostic code.
// Runtime network updates are forbidden; builds use
// scripts/filters/update-blocked-lists.ps1 before packaging.
func (fm *FilterManager) UpdateRefilters() (int, error) {
	if !fm.EnsureFiltersExist() {
		return 0, fmt.Errorf("bundled routing filters are incomplete; rebuild the application")
	}
	fm.logf("[FilterManager] Runtime update skipped; using the signed bundled catalog")
	return 0, nil
}

// EnsureFiltersExist checks if filter files exist.
// Returns true if all required filters are present.
func (fm *FilterManager) EnsureFiltersExist() bool {
	requiredFilters := []string{FiltersVersionFile, blockedDomainsFileName, blockedIPsFileName}
	for _, filter := range FilterFiles {
		requiredFilters = append(requiredFilters, filter.Name)
	}

	for _, f := range requiredFilters {
		filterPath := filepath.Join(fm.filtersPath, f)
		if _, err := os.Stat(filterPath); os.IsNotExist(err) {
			return false
		}
	}

	return true
}

// GetRuleSetConfigs returns sing-box rule_set configurations for template.
// These are local file-based rule_sets.
func (fm *FilterManager) GetRuleSetConfigs() []map[string]interface{} {
	configs := make([]map[string]interface{}, 0, len(FilterFiles))

	for _, f := range FilterFiles {
		filterPath := filepath.Join(fm.filtersPath, f.Name)

		// Only include existing files
		if _, err := os.Stat(filterPath); err != nil {
			continue
		}

		config := map[string]interface{}{
			"type":   "local",
			"tag":    f.Tag,
			"format": "binary",
			"path":   filterPath, // Absolute path to .srs file
		}

		configs = append(configs, config)
	}

	return configs
}
