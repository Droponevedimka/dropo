// Package main provides unified storage management for dropo.
// All profile data is stored in a single resources/settings.json file.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ProfileData contains all data for a single profile.
type ProfileData struct {
	// Profile metadata
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`

	// Subscription settings (was user_settings.json)
	SubscriptionURL  string                `json:"subscription_url,omitempty"`
	LastUpdated      string                `json:"last_updated,omitempty"`
	ProxyCount       int                   `json:"proxy_count,omitempty"`
	WireGuardConfigs []UserWireGuardConfig `json:"wireguard_configs,omitempty"`

	// Generated sing-box config (was config.json)
	SingboxConfig   map[string]interface{} `json:"singbox_config,omitempty"`
	XrayConfig      map[string]interface{} `json:"xray_config,omitempty"`
	XrayConfigReady bool                   `json:"xray_config_ready,omitempty"`
}

// GlobalAppSettings contains global application settings (stored in settings.json).
type GlobalAppSettings struct {
	// General settings
	AutoStart           bool `json:"auto_start"`
	AutoStartPrompted   bool `json:"auto_start_prompted"` // first-run autostart dialog has been answered
	RestoreVPNOnStartup bool `json:"restore_vpn_on_startup"`
	Notifications       bool `json:"notifications"`
	CheckUpdates        bool `json:"check_updates"`

	// Logging settings
	EnableLogging bool     `json:"enable_logging"`
	LogLevel      LogLevel `json:"log_level"`

	// Appearance
	Theme    Theme    `json:"theme"`
	Language Language `json:"language"`

	// Routing settings
	RoutingMode RoutingMode `json:"routing_mode"` // How traffic is routed: blocked_only, except_russia, all_traffic
	NetworkMode NetworkMode `json:"network_mode"` // Windows desktop always migrates to windows_unified

	// Free access settings — opening blocked-in-RF
	// services without a VPN key, via local DPI-bypass methods (ByeDPI).
	FreeAccessEnabled  bool              `json:"free_access_enabled"`  // Master switch for the "Free access" tab
	FreeAccessReverse  bool              `json:"free_access_reverse"`  // Prefer ByeDPI candidates before VPN when latency is equal
	FreeAccessServices map[string]bool   `json:"free_access_services"` // tag -> enabled, see DefaultFreeAccessServices
	FreeAccessMethods  map[string]string `json:"free_access_methods"`  // tag -> forced method (auto/direct/vpn/byedpi/...)
	DisableFreeAccess  bool              `json:"disable_free_access"`  // Opt-out: require VPN/WireGuard/subscription instead of free methods

	// TelegramProxyInjected records that dropo opened the tg://proxy link at
	// least once, so the local MTProto proxy is saved inside Telegram. It cannot
	// be removed programmatically (tg:// only adds; tdata is encrypted), so this
	// drives the in-app cleanup guidance when Telegram is later moved to the VPN
	// or the app is uninstalled. See [[telegram-tg-ws-proxy-sidecar]].
	TelegramProxyInjected bool `json:"telegram_proxy_injected"`

	// RU-traffic settings — RU domains are direct by
	// default in every routing mode; this opt-in hides them behind a proxy.
	HideRuTraffic  bool   `json:"hide_ru_traffic"`  // "Скрывать RU-трафик от провайдера"
	RuProxyAddress string `json:"ru_proxy_address"` // Optional proxy link used for RU traffic when the toggle above is on

	// Subscription settings
	AutoUpdateSub     bool      `json:"auto_update_sub"`
	SubUpdateInterval int       `json:"sub_update_interval"`
	LastSubUpdate     time.Time `json:"last_sub_update"`

	// Update tracking
	LastUpdateCheck string `json:"last_update_check"`

	// Active profile
	ActiveProfileID int `json:"active_profile_id"`

	// WireGuard settings
	WireGuardVersion string `json:"wireguard_version"` // Native WireGuard version (e.g., "0.5.3")
}

// SettingsFile represents the complete settings.json structure.
type SettingsFile struct {
	Version  int               `json:"version"`  // Schema version for migrations
	App      GlobalAppSettings `json:"app"`      // Global app settings
	Profiles []ProfileData     `json:"profiles"` // Array of profiles with their configs
}

// Storage manages the unified settings.json file.
type Storage struct {
	resourcesPath string // Path to resources folder
	settingsPath  string // Path to settings.json
	templatePath  string // Path to template.json
	data          *SettingsFile
	mu            sync.RWMutex
}

const (
	SettingsVersion  = 3
	ResourcesFolder  = "resources"
	SettingsFileName = "settings.json"
)

// NewStorage creates a new storage manager.
func NewStorage(basePath string) *Storage {
	resourcesPath := filepath.Join(basePath, ResourcesFolder)

	s := &Storage{
		resourcesPath: resourcesPath,
		settingsPath:  filepath.Join(resourcesPath, SettingsFileName),
		templatePath:  filepath.Join(resourcesPath, TemplateFileName),
	}

	return s
}

// MigrateUnifiedSettingsIfMissing copies the last portable settings file into
// the per-user store without overwriting a profile that was already migrated.
func (s *Storage) MigrateUnifiedSettingsIfMissing(oldPath string) error {
	if fileExists(s.settingsPath) || !fileExists(oldPath) {
		return nil
	}
	data, err := os.ReadFile(oldPath)
	if err != nil {
		return err
	}
	var settings SettingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("invalid portable settings: %w", err)
	}
	if err := os.MkdirAll(s.resourcesPath, 0700); err != nil {
		return err
	}
	return atomicWriteFile(s.settingsPath, data, 0600)
}

// Init initializes storage, creating directories and files as needed.
func (s *Storage) Init() error {
	// Create resources directory
	if err := os.MkdirAll(s.resourcesPath, 0700); err != nil {
		return fmt.Errorf("failed to create resources directory: %w", err)
	}

	// Copy template.json to resources if not exists
	if !fileExists(s.templatePath) {
		if err := copyEmbeddedTemplate(s.templatePath); err != nil {
			return fmt.Errorf("failed to copy template.json: %w", err)
		}
	}

	// Load or create settings.json
	return s.Load()
}

// Load loads settings from file.
func (s *Storage) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create default settings
			s.data = s.createDefaultSettings()
			return s.saveInternal()
		}
		return fmt.Errorf("failed to read settings: %w", err)
	}

	var settings SettingsFile
	if err := json.Unmarshal(data, &settings); err != nil {
		// Backup corrupted file and create new
		backupPath := s.settingsPath + ".backup"
		os.Rename(s.settingsPath, backupPath)
		s.data = s.createDefaultSettings()
		return s.saveInternal()
	}

	s.data = &settings

	// Ensure at least one profile exists
	if len(s.data.Profiles) == 0 {
		s.data.Profiles = []ProfileData{s.createDefaultProfile()}
	}

	// Ensure default profile exists (ID=1, cannot be deleted)
	hasDefaultProfile := false
	for _, p := range s.data.Profiles {
		if p.ID == DefaultProfileID {
			hasDefaultProfile = true
			break
		}
	}
	if !hasDefaultProfile {
		// Insert default profile at the beginning
		s.data.Profiles = append([]ProfileData{s.createDefaultProfile()}, s.data.Profiles...)
	}

	// Ensure active profile ID is valid
	if s.data.App.ActiveProfileID <= 0 {
		s.data.App.ActiveProfileID = DefaultProfileID
	} else {
		// Check if active profile exists
		activeExists := false
		for _, p := range s.data.Profiles {
			if p.ID == s.data.App.ActiveProfileID {
				activeExists = true
				break
			}
		}
		if !activeExists {
			s.data.App.ActiveProfileID = DefaultProfileID
		}
	}

	s.normalizeAppSettings()

	return s.saveInternal()
}

// createDefaultSettings creates default settings structure.
func (s *Storage) createDefaultSettings() *SettingsFile {
	return &SettingsFile{
		Version: SettingsVersion,
		App: GlobalAppSettings{
			AutoStart:           true,
			RestoreVPNOnStartup: false,
			Notifications:       true,
			CheckUpdates:        true,
			EnableLogging:       true,
			LogLevel:            LogLevelInfo, // info by default; users can raise to trace for diagnosis (review.md §2.3)
			Theme:               ThemeSystem,
			Language:            LangRussian,
			RoutingMode:         DefaultRoutingMode, // blocked_only by default
			NetworkMode:         DefaultNetworkMode,
			AutoUpdateSub:       true,
			SubUpdateInterval:   24,
			ActiveProfileID:     DefaultProfileID,
			FreeAccessEnabled:   true, // enabled by default
			FreeAccessReverse:   false,
			FreeAccessServices:  DefaultFreeAccessServiceState(), // every service enabled by default
			FreeAccessMethods:   DefaultFreeAccessServiceMethodState(),
			DisableFreeAccess:   false,
			HideRuTraffic:       false, // RU traffic stays direct by default
			RuProxyAddress:      "",
		},
		Profiles: []ProfileData{s.createDefaultProfile()},
	}
}

func (s *Storage) normalizeAppSettings() {
	app := &s.data.App
	applyCurrentDefaults := s.data.Version < SettingsVersion

	if app.LogLevel == "" {
		app.LogLevel = LogLevelInfo
	}
	if app.Theme == "" {
		app.Theme = ThemeSystem
	}
	if app.Language == "" {
		app.Language = LangRussian
	}
	if app.RoutingMode == "" {
		app.RoutingMode = DefaultRoutingMode
	}
	app.NetworkMode = NormalizeNetworkMode(app.NetworkMode)
	if app.SubUpdateInterval <= 0 {
		app.SubUpdateInterval = 24
	}

	if applyCurrentDefaults {
		app.Notifications = true
		app.EnableLogging = true
		app.LogLevel = LogLevelInfo
		app.AutoUpdateSub = true
		app.FreeAccessEnabled = true
		app.FreeAccessReverse = false
		app.NetworkMode = DefaultNetworkMode
	}
	if app.FreeAccessServices == nil {
		app.FreeAccessServices = DefaultFreeAccessServiceState()
	}
	if app.FreeAccessMethods == nil {
		app.FreeAccessMethods = DefaultFreeAccessServiceMethodState()
	} else {
		for _, svc := range DefaultFreeAccessServices {
			app.FreeAccessMethods[svc.Tag] = NormalizeFreeAccessServiceMethod(app.FreeAccessMethods[svc.Tag])
		}
	}
	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID != DefaultProfileID {
			continue
		}
		name := strings.TrimSpace(s.data.Profiles[i].Name)
		if name == "" || name == "Work" || name == "Default" {
			s.data.Profiles[i].Name = DefaultProfileName
		}
	}
	s.data.Version = SettingsVersion
}

// createDefaultProfile creates a default profile.
func (s *Storage) createDefaultProfile() ProfileData {
	return ProfileData{
		ID:        DefaultProfileID,
		Name:      DefaultProfileName,
		CreatedAt: time.Now(),
	}
}

// saveInternal saves settings without locking. Writes atomically (temp+rename)
// so a crash mid-write cannot corrupt settings.json (review.md §3).
func (s *Storage) saveInternal() error {
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal settings: %w", err)
	}
	return atomicWriteFile(s.settingsPath, data, 0600)
}

// Save saves settings to file.
func (s *Storage) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveInternal()
}

// GetTemplatePath returns path to template.json.
func (s *Storage) GetTemplatePath() string {
	return s.templatePath
}

// GetResourcesPath returns path to resources folder.
func (s *Storage) GetResourcesPath() string {
	return s.resourcesPath
}

// --- App Settings ---

// GetAppSettings returns a copy of app settings.
func (s *Storage) GetAppSettings() GlobalAppSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.App
}

// UpdateAppSettings updates app settings.
func (s *Storage) UpdateAppSettings(settings GlobalAppSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.App = settings
	return s.saveInternal()
}

// GetActiveProfileID returns the active profile ID.
// Always returns a valid profile ID (at least DefaultProfileID).
func (s *Storage) GetActiveProfileID() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeID := s.data.App.ActiveProfileID

	// If not set or invalid, return default
	if activeID <= 0 {
		return DefaultProfileID
	}

	// Verify the profile exists
	for _, p := range s.data.Profiles {
		if p.ID == activeID {
			return activeID
		}
	}

	// Profile doesn't exist, return default
	return DefaultProfileID
}

// SetActiveProfileID sets the active profile ID.
func (s *Storage) SetActiveProfileID(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.App.ActiveProfileID = id
	return s.saveInternal()
}

// --- Profile Management ---

// GetAllProfiles returns all profiles.
func (s *Storage) GetAllProfiles() []ProfileData {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]ProfileData, len(s.data.Profiles))
	copy(result, s.data.Profiles)
	return result
}

// GetProfile returns a profile by ID.
func (s *Storage) GetProfile(id int) (*ProfileData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			profile := s.data.Profiles[i]
			return &profile, nil
		}
	}
	return nil, fmt.Errorf("profile with ID %d not found", id)
}

// GetActiveProfile returns the currently active profile.
func (s *Storage) GetActiveProfile() (*ProfileData, error) {
	return s.GetProfile(s.GetActiveProfileID())
}

// CreateProfile creates a new profile.
func (s *Storage) CreateProfile(name string) (*ProfileData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.data.Profiles) >= MaxProfiles {
		return nil, fmt.Errorf("maximum number of profiles (%d) reached", MaxProfiles)
	}

	// Find next available ID
	maxID := 0
	for _, p := range s.data.Profiles {
		if p.ID > maxID {
			maxID = p.ID
		}
	}

	profile := ProfileData{
		ID:        maxID + 1,
		Name:      name,
		CreatedAt: time.Now(),
	}

	s.data.Profiles = append(s.data.Profiles, profile)
	if err := s.saveInternal(); err != nil {
		return nil, err
	}

	return &profile, nil
}

// UpdateProfile updates a profile's metadata.
func (s *Storage) UpdateProfile(id int, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].Name = name
			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// DeleteProfile deletes a profile.
func (s *Storage) DeleteProfile(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if id == DefaultProfileID {
		return fmt.Errorf("нельзя удалить профиль по умолчанию")
	}

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles = append(s.data.Profiles[:i], s.data.Profiles[i+1:]...)

			// Switch to default profile if deleted profile was active
			if s.data.App.ActiveProfileID == id {
				s.data.App.ActiveProfileID = DefaultProfileID
			}

			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// ReplaceAllProfiles replaces ALL profiles with imported ones.
// This is used for full import - all existing profiles are removed and replaced.
func (s *Storage) ReplaceAllProfiles(profiles []ProfileData) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(profiles) == 0 {
		return fmt.Errorf("cannot import empty profiles list")
	}

	// Ensure at least one profile has ID=1 (default profile)
	hasDefault := false
	for _, p := range profiles {
		if p.ID == DefaultProfileID {
			hasDefault = true
			break
		}
	}

	if !hasDefault {
		// Set first profile as default
		profiles[0].ID = DefaultProfileID
	}

	// Replace all profiles
	s.data.Profiles = profiles

	// Validate active profile ID
	activeExists := false
	for _, p := range profiles {
		if p.ID == s.data.App.ActiveProfileID {
			activeExists = true
			break
		}
	}

	if !activeExists {
		// Set to default profile
		s.data.App.ActiveProfileID = DefaultProfileID
	}

	return s.saveInternal()
}

// --- Profile Settings (Subscription, WireGuard) ---

// UpdateProfileSubscription updates a profile's subscription settings.
func (s *Storage) UpdateProfileSubscription(id int, subscriptionURL string, proxyCount int, wireGuardConfigs []UserWireGuardConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].SubscriptionURL = subscriptionURL
			s.data.Profiles[i].ProxyCount = proxyCount
			s.data.Profiles[i].WireGuardConfigs = wireGuardConfigs
			s.data.Profiles[i].LastUpdated = time.Now().Format("2006-01-02 15:04:05")
			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// UpdateProfileWireGuard updates only WireGuard configs for a profile.
func (s *Storage) UpdateProfileWireGuard(id int, wireGuardConfigs []UserWireGuardConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].WireGuardConfigs = wireGuardConfigs
			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// --- Sing-box Config ---

// UpdateProfileConfig updates the generated sing-box config for a profile.
func (s *Storage) UpdateProfileConfig(id int, config map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].SingboxConfig = config
			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// GetProfileConfig returns the sing-box config for a profile.
func (s *Storage) GetProfileConfig(id int) (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			return s.data.Profiles[i].SingboxConfig, nil
		}
	}
	return nil, fmt.Errorf("profile with ID %d not found", id)
}

// UpdateProfileXrayConfig updates the generated Xray bridge config for a profile.
func (s *Storage) UpdateProfileXrayConfig(id int, config map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			s.data.Profiles[i].XrayConfig = config
			s.data.Profiles[i].XrayConfigReady = true
			return s.saveInternal()
		}
	}
	return fmt.Errorf("profile with ID %d not found", id)
}

// GetProfileXrayConfig returns the Xray bridge config for a profile.
func (s *Storage) GetProfileXrayConfig(id int) (map[string]interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == id {
			return s.data.Profiles[i].XrayConfig, nil
		}
	}
	return nil, fmt.Errorf("profile with ID %d not found", id)
}

// WriteActiveXrayConfigToFile writes the active profile's Xray bridge config.
func (s *Storage) WriteActiveXrayConfigToFile() (string, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeID := s.data.App.ActiveProfileID
	configPath := filepath.Join(s.resourcesPath, XrayBridgeConfigFileName)

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == activeID {
			config := s.data.Profiles[i].XrayConfig
			if len(config) == 0 {
				_ = os.Remove(configPath)
				return configPath, false, nil
			}

			data, err := json.MarshalIndent(config, "", "  ")
			if err != nil {
				return "", false, fmt.Errorf("failed to marshal xray config: %w", err)
			}
			if err := os.WriteFile(configPath, data, 0600); err != nil {
				return "", false, fmt.Errorf("failed to write xray config: %w", err)
			}
			return configPath, true, nil
		}
	}

	return "", false, fmt.Errorf("active profile %d not found", activeID)
}

func (s *Storage) ActiveConfigFilePath() string {
	return filepath.Join(s.resourcesPath, "active_config.json")
}

func (s *Storage) ActiveProfileHasConfig() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeID := s.data.App.ActiveProfileID
	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == activeID {
			return len(s.data.Profiles[i].SingboxConfig) > 0
		}
	}
	return false
}

// WriteActiveConfigToFile writes the active profile's config to a temporary file for sing-box.
// This is needed because sing-box requires a file path.
func (s *Storage) WriteActiveConfigToFile() (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeID := s.data.App.ActiveProfileID

	for i := range s.data.Profiles {
		if s.data.Profiles[i].ID == activeID {
			config := s.data.Profiles[i].SingboxConfig
			if len(config) == 0 {
				return "", fmt.Errorf("no config for profile %d", activeID)
			}

			// WireGuard is now managed by Native WireGuard Manager
			// Remove old WireGuard outbounds from config if present
			s.removeWireGuardFromConfig(config)

			// Clean up deprecated/problematic fields
			// Remove endpoints (WireGuard is managed separately)
			delete(config, "endpoints")

			// Remove log output to make sing-box write to stdout
			if logSection, ok := config["log"].(map[string]interface{}); ok {
				delete(logSection, "output")
			}

			// Avoid startup failure when Windows reserves the template mixed-in
			// port (common with Hyper-V/WinNAT excluded ranges).
			s.ensureMixedInboundPort(config)

			// Windows TUN + TCP Fast Open/MPTCP can break TLS handshakes for
			// Google/YouTube in direct fallback mode. Keep direct conservative.
			s.disableRiskyDirectOptions(config)
			s.preferIPv4Resolution(config)
			relaxTunStrictRoute(config)
			disableTunIPv6(config)
			clearDirectOutboundInterface(config)
			addTunRouteExcludesForProxyEndpoints(config, s.data.Profiles[i].XrayConfig)

			// Write to temp config file
			configPath := filepath.Join(s.resourcesPath, "active_config.json")
			data, err := json.MarshalIndent(config, "", "  ")
			if err != nil {
				return "", fmt.Errorf("failed to marshal config: %w", err)
			}

			if err := os.WriteFile(configPath, data, 0600); err != nil {
				return "", fmt.Errorf("failed to write config: %w", err)
			}

			return configPath, nil
		}
	}

	return "", fmt.Errorf("active profile %d not found", activeID)
}

func (s *Storage) ensureMixedInboundPort(config map[string]interface{}) {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return
	}

	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}

		listen := "127.0.0.1"
		if value, ok := inboundMap["listen"].(string); ok && value != "" {
			listen = value
		}

		port := mixedInboundPort(inboundMap["listen_port"])
		if port > 0 && tcpPortAvailable(listen, port) {
			return
		}

		nextPort, err := findAvailableTCPPort(listen)
		if err != nil {
			return
		}
		inboundMap["listen_port"] = nextPort
		return
	}
}

func mixedInboundPort(value interface{}) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func tcpPortAvailable(host string, port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func findAvailableTCPPort(host string) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:0", host))
	if err != nil {
		return 0, err
	}
	defer ln.Close()

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address: %s", ln.Addr().String())
	}
	return addr.Port, nil
}

func (s *Storage) disableRiskyDirectOptions(config map[string]interface{}) {
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return
	}

	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || outboundMap["tag"] != "direct" {
			continue
		}
		delete(outboundMap, "tcp_fast_open")
		delete(outboundMap, "tcp_multi_path")
	}
}

func (s *Storage) preferIPv4Resolution(config map[string]interface{}) {
	resolver := "dns-direct"
	if dns, ok := config["dns"].(map[string]interface{}); ok {
		dns["strategy"] = "ipv4_only"
		dns["reverse_mapping"] = true
		if final, ok := dns["final"].(string); ok && final != "" {
			resolver = final
		}
	}

	route, ok := config["route"].(map[string]interface{})
	if !ok {
		return
	}
	if existing, ok := route["default_domain_resolver"].(string); ok && existing != "" {
		resolver = existing
	}
	if existing, ok := route["default_domain_resolver"].(map[string]interface{}); ok {
		if server, ok := existing["server"].(string); ok && server != "" {
			resolver = server
		}
	}
	route["default_domain_resolver"] = map[string]interface{}{
		"server":   resolver,
		"strategy": "ipv4_only",
	}
}

func addTunRouteExcludesForProxyEndpoints(config map[string]interface{}, xrayConfig map[string]interface{}) {
	hosts := collectProxyEndpointHosts(config)
	hosts = append(hosts, collectXrayEndpointHosts(xrayConfig)...)
	cidrs := resolveEndpointHostsToCIDRs(hosts)
	if len(cidrs) == 0 {
		return
	}
	addTunRouteExcludeAddresses(config, cidrs)
	fmt.Printf("[TUN] Excluded proxy endpoint IPs from auto_route: %v\n", cidrs)
}

func collectProxyEndpointHosts(config map[string]interface{}) []string {
	outbounds, _ := config["outbounds"].([]interface{})
	hosts := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || isSingBoxGroupType(stringMapValue(outboundMap, "type")) {
			continue
		}
		hosts = appendEndpointHost(hosts, outboundMap["server"])
		if tlsMap, ok := outboundMap["tls"].(map[string]interface{}); ok {
			hosts = appendEndpointHost(hosts, tlsMap["server_name"])
		}
		if transportMap, ok := outboundMap["transport"].(map[string]interface{}); ok {
			hosts = appendEndpointHost(hosts, transportMap["host"])
			hosts = appendEndpointHost(hosts, transportMap["hosts"])
			if headers, ok := transportMap["headers"].(map[string]interface{}); ok {
				hosts = appendEndpointHost(hosts, headers["Host"])
				hosts = appendEndpointHost(hosts, headers["host"])
			}
		}
	}
	return uniqueStrings(hosts)
}

func collectXrayEndpointHosts(xrayConfig map[string]interface{}) []string {
	if len(xrayConfig) == 0 {
		return nil
	}
	outbounds, _ := xrayConfig["outbounds"].([]interface{})
	hosts := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		settings, _ := outboundMap["settings"].(map[string]interface{})
		vnext, _ := settings["vnext"].([]interface{})
		for _, item := range vnext {
			server, ok := item.(map[string]interface{})
			if ok {
				hosts = appendEndpointHost(hosts, server["address"])
			}
		}
		stream, _ := outboundMap["streamSettings"].(map[string]interface{})
		if tlsSettings, ok := stream["tlsSettings"].(map[string]interface{}); ok {
			hosts = appendEndpointHost(hosts, tlsSettings["serverName"])
		}
		if realitySettings, ok := stream["realitySettings"].(map[string]interface{}); ok {
			hosts = appendEndpointHost(hosts, realitySettings["serverName"])
		}
		if xhttpSettings, ok := stream["xhttpSettings"].(map[string]interface{}); ok {
			hosts = appendEndpointHost(hosts, xhttpSettings["host"])
		}
		if wsSettings, ok := stream["wsSettings"].(map[string]interface{}); ok {
			if headers, ok := wsSettings["headers"].(map[string]interface{}); ok {
				hosts = appendEndpointHost(hosts, headers["Host"])
				hosts = appendEndpointHost(hosts, headers["host"])
			}
		}
	}
	return uniqueStrings(hosts)
}

func appendEndpointHost(hosts []string, value interface{}) []string {
	for _, item := range interfaceStringSlice(value) {
		if host := normalizeEndpointHost(item); host != "" && !isLoopbackHost(host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func normalizeEndpointHost(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if parsed, err := url.Parse(value); err == nil {
			value = parsed.Host
		}
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	value = strings.TrimSuffix(value, ".")
	return strings.ToLower(value)
}

func isLoopbackHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func resolveEndpointHostsToCIDRs(hosts []string) []string {
	hosts = uniqueStrings(hosts)
	cidrs := make([]string, 0, len(hosts))
	for _, host := range hosts {
		ip := net.ParseIP(host)
		if ip != nil {
			cidrs = appendEndpointIPCIDR(cidrs, ip)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		cancel()
		if err != nil {
			fmt.Printf("[TUN] Failed to resolve proxy endpoint %s for route exclusion: %v\n", host, err)
			continue
		}
		for _, addr := range addrs {
			cidrs = appendEndpointIPCIDR(cidrs, addr.IP)
		}
	}
	return uniqueStrings(cidrs)
}

func appendEndpointIPCIDR(cidrs []string, ip net.IP) []string {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return cidrs
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return append(cidrs, ipv4.String()+"/32")
	}
	if ipv6 := ip.To16(); ipv6 != nil {
		return append(cidrs, ipv6.String()+"/128")
	}
	return cidrs
}

func addTunRouteExcludeAddresses(config map[string]interface{}, cidrs []string) {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return
	}
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		existing := interfaceStringSlice(inboundMap["route_exclude_address"])
		inboundMap["route_exclude_address"] = uniqueStrings(append(existing, cidrs...))
		return
	}
}

func stringMapValue(value map[string]interface{}, key string) string {
	text, _ := value[key].(string)
	return strings.ToLower(text)
}

// removeWireGuardFromConfig removes WireGuard outbounds and related DNS/route rules
// WireGuard is now managed by Native WireGuard Manager
func (s *Storage) removeWireGuardFromConfig(config map[string]interface{}) {
	// Remove WireGuard outbounds
	if outbounds, ok := config["outbounds"].([]interface{}); ok {
		filtered := []interface{}{}
		for _, ob := range outbounds {
			if obMap, ok := ob.(map[string]interface{}); ok {
				if obType, _ := obMap["type"].(string); obType != "wireguard" {
					filtered = append(filtered, ob)
				}
			}
		}
		config["outbounds"] = filtered
	}

	// Remove dns-wg-* servers and rules
	if dns, ok := config["dns"].(map[string]interface{}); ok {
		// Remove WireGuard DNS servers
		if servers, ok := dns["servers"].([]interface{}); ok {
			filtered := []interface{}{}
			for _, srv := range servers {
				if srvMap, ok := srv.(map[string]interface{}); ok {
					if tag, _ := srvMap["tag"].(string); !strings.HasPrefix(tag, "dns-wg-") {
						filtered = append(filtered, srv)
					}
				}
			}
			dns["servers"] = filtered
		}

		// Remove WireGuard DNS rules (those with dns-wg-* server)
		if rules, ok := dns["rules"].([]interface{}); ok {
			filtered := []interface{}{}
			for _, rule := range rules {
				if ruleMap, ok := rule.(map[string]interface{}); ok {
					if server, _ := ruleMap["server"].(string); !strings.HasPrefix(server, "dns-wg-") {
						filtered = append(filtered, rule)
					}
				}
			}
			dns["rules"] = filtered
		}
	}

	// Remove WireGuard route rules (those routing to wg-* outbounds)
	if route, ok := config["route"].(map[string]interface{}); ok {
		if rules, ok := route["rules"].([]interface{}); ok {
			filtered := []interface{}{}
			for _, rule := range rules {
				if ruleMap, ok := rule.(map[string]interface{}); ok {
					if outbound, _ := ruleMap["outbound"].(string); !strings.HasPrefix(outbound, "wg-") {
						filtered = append(filtered, rule)
					}
				}
			}
			route["rules"] = filtered
		}
	}
}

// migrateWireGuardDNS ensures DNS rules for .local domains exist
//
//lint:ignore U1000 Kept as a compatibility transform for importing legacy settings snapshots.
func (s *Storage) migrateWireGuardDNS(config map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	dns, ok := config["dns"].(map[string]interface{})
	if !ok {
		return
	}

	servers, ok := dns["servers"].([]interface{})
	if !ok {
		servers = []interface{}{}
	}

	rules, _ := dns["rules"].([]interface{})
	if rules == nil {
		rules = []interface{}{}
	}

	for _, wg := range wireGuardConfigs {
		if wg.DNS == "" {
			continue
		}

		dnsTag := wg.Tag + "-dns"

		// Check if DNS server exists
		serverExists := false
		for _, srv := range servers {
			if srvMap, ok := srv.(map[string]interface{}); ok {
				if tag, ok := srvMap["tag"].(string); ok && tag == dnsTag {
					serverExists = true
					break
				}
			}
		}

		// Add DNS server if not exists
		if !serverExists {
			servers = append(servers, map[string]interface{}{
				"type":   "udp",
				"tag":    dnsTag,
				"server": wg.DNS,
				"detour": wg.Tag,
			})
		}

		// Check if .local DNS rule exists
		localRuleExists := false
		for _, rule := range rules {
			if ruleMap, ok := rule.(map[string]interface{}); ok {
				if server, ok := ruleMap["server"].(string); ok && server == dnsTag {
					localRuleExists = true
					break
				}
			}
		}

		// Add .local DNS rule at the beginning if not exists
		if !localRuleExists {
			localRule := map[string]interface{}{
				"domain_suffix": []string{"local", wg.Tag + ".local"},
				"action":        "route",
				"server":        dnsTag,
			}
			rules = append([]interface{}{localRule}, rules...)
		}
	}

	dns["servers"] = servers
	dns["rules"] = rules
}

// migrateWireGuardRouteRules ensures route rules for WireGuard AllowedIPs exist
// Порядок: WireGuard IP rules → ip_is_private → geosite-ru → proxy
//
//lint:ignore U1000 Kept as a compatibility transform for importing legacy settings snapshots.
func (s *Storage) migrateWireGuardRouteRules(config map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	route, ok := config["route"].(map[string]interface{})
	if !ok {
		return
	}

	rules, _ := route["rules"].([]interface{})
	if rules == nil {
		rules = []interface{}{}
	}

	// Находим позицию после hijack-dns (перед ip_is_private)
	insertIdx := 0
	for i, rule := range rules {
		if ruleMap, ok := rule.(map[string]interface{}); ok {
			action, _ := ruleMap["action"].(string)
			if action == "hijack-dns" {
				insertIdx = i + 1
				break
			}
			if action == "sniff" {
				insertIdx = i + 1
			}
		}
	}

	// Проверяем и добавляем IP rules для каждого WireGuard
	for _, wg := range wireGuardConfigs {
		if len(wg.AllowedIPs) == 0 {
			continue
		}

		// Проверяем существует ли уже правило для этого WireGuard
		ruleExists := false
		for _, rule := range rules {
			if ruleMap, ok := rule.(map[string]interface{}); ok {
				if outbound, ok := ruleMap["outbound"].(string); ok && outbound == wg.Tag {
					if _, hasIP := ruleMap["ip_cidr"]; hasIP {
						ruleExists = true
						break
					}
				}
			}
		}

		// Добавляем правило если не существует
		if !ruleExists {
			ipRule := map[string]interface{}{
				"ip_cidr":  wg.AllowedIPs,
				"outbound": wg.Tag,
			}
			// Вставляем в позицию insertIdx
			newRules := make([]interface{}, 0, len(rules)+1)
			newRules = append(newRules, rules[:insertIdx]...)
			newRules = append(newRules, ipRule)
			newRules = append(newRules, rules[insertIdx:]...)
			rules = newRules
			insertIdx++ // Сдвигаем позицию для следующего WireGuard
		}
	}

	route["rules"] = rules
}

// --- Migration from old format ---

// ConfigBuilderForStorage provides config building functionality for Storage.
type ConfigBuilderForStorage struct {
	storage       *Storage
	fetcher       *SubscriptionFetcher
	routingMode   RoutingMode
	filterManager *FilterManager
	templateOnce  sync.Once
	templateData  []byte
	templateErr   error
	clashAPI      *clashAPIAccess
}

// NewConfigBuilderForStorage creates a config builder that works with Storage.
func NewConfigBuilderForStorage(storage *Storage) *ConfigBuilderForStorage {
	// Filter manager path: go up from resources to parent, then bin/filters
	basePath := filepath.Dir(storage.resourcesPath)
	return NewConfigBuilderForStorageWithRuntime(storage, basePath)
}

func NewConfigBuilderForStorageWithRuntime(storage *Storage, runtimeBasePath string) *ConfigBuilderForStorage {
	return &ConfigBuilderForStorage{
		storage:       storage,
		fetcher:       NewSubscriptionFetcher(),
		routingMode:   DefaultRoutingMode,
		filterManager: NewFilterManager(runtimeBasePath),
		clashAPI:      newClashAPIAccess(),
	}
}

// SetRoutingMode sets the routing mode for config generation
func (b *ConfigBuilderForStorage) SetRoutingMode(mode RoutingMode) {
	b.routingMode = mode
}

// GetRoutingMode returns current routing mode
func (b *ConfigBuilderForStorage) GetRoutingMode() RoutingMode {
	return b.routingMode
}

// GetFilterManager returns the filter manager
func (b *ConfigBuilderForStorage) GetFilterManager() *FilterManager {
	return b.filterManager
}

// TestSubscription tests a subscription URL and returns available proxies.
func (b *ConfigBuilderForStorage) TestSubscription(subscriptionURL string) (*SubscriptionTestResult, error) {
	result := &SubscriptionTestResult{
		Success: false,
		Proxies: []ProxyInfo{},
	}

	isDirectLink := isDirectProxyLink(subscriptionURL)

	var proxies []ProxyConfig
	var err error

	if isDirectLink {
		proxy, err := b.fetcher.ParseSingleLink(subscriptionURL)
		if err != nil {
			result.Error = fmt.Sprintf("Ошибка парсинга ссылки: %v", err)
			return result, nil
		}
		proxies = []ProxyConfig{proxy}
	} else {
		proxies, err = b.fetcher.FetchAndParse(subscriptionURL)
		if err != nil {
			result.Error = fmt.Sprintf("Ошибка загрузки подписки: %v", err)
			return result, nil
		}
	}

	// Filter unsupported transports (e.g., xhttp which is Xray-only)
	filterResult := FilterUnsupportedTransports(proxies)
	proxies = filterResult.Supported

	if len(proxies) == 0 {
		if filterResult.AllFiltered {
			result.Error = filterResult.Message
		} else {
			result.Error = "Подписка не содержит доступных прокси"
		}
		return result, nil
	}

	result.Success = true
	result.Count = len(proxies)
	result.IsDirectLink = isDirectLink

	// Add warning about filtered proxies
	if len(filterResult.Filtered) > 0 {
		result.Warning = filterResult.Message
		result.FilteredCount = len(filterResult.Filtered)
	}

	for _, p := range proxies {
		result.Proxies = append(result.Proxies, ProxyInfo{
			Type:   p.Type,
			Raw:    p.Raw,
			Name:   p.Name,
			Server: p.Server,
			Port:   p.ServerPort,
		})
	}

	return result, nil
}

// BuildConfig builds sing-box config for the active profile.
func (b *ConfigBuilderForStorage) BuildConfig(subscriptionURL string) error {
	profile, err := b.storage.GetActiveProfile()
	if err != nil || profile == nil {
		return fmt.Errorf("no active profile")
	}

	return b.BuildConfigForProfile(profile.ID, subscriptionURL, profile.WireGuardConfigs)
}

// BuildConfigForProfile builds sing-box config for a specific profile.
func (b *ConfigBuilderForStorage) BuildConfigForProfile(profileID int, subscriptionURL string, wireGuardConfigs []UserWireGuardConfig) error {
	fmt.Printf("[BuildConfigForProfile] Called with profileID=%d, %d WireGuard configs\n", profileID, len(wireGuardConfigs))
	for i, wg := range wireGuardConfigs {
		fmt.Printf("[BuildConfigForProfile] WireGuard[%d]: tag=%s, dns=%s, allowedIPs=%v\n", i, wg.Tag, wg.DNS, wg.AllowedIPs)
	}

	// Load template
	b.templateOnce.Do(func() {
		b.templateData, b.templateErr = os.ReadFile(b.storage.templatePath)
	})
	if b.templateErr != nil {
		err := b.templateErr
		return fmt.Errorf("не удалось загрузить template.json: %w", err)
	}

	var template map[string]interface{}
	if err := json.Unmarshal(b.templateData, &template); err != nil {
		return fmt.Errorf("ошибка парсинга template.json: %w", err)
	}

	// Disable strict_route when WireGuard is used to allow system routes to work
	fmt.Printf("[BuildConfigForProfile] Configuring TUN for WireGuard compatibility...\n")
	b.disableStrictRouteForWireGuard(template, wireGuardConfigs)

	// Add DNS servers and rules for WireGuard networks
	// (WireGuard works natively, DNS queries go through direct and WireGuard interface handles routing)
	fmt.Printf("[BuildConfigForProfile] Adding WireGuard DNS rules for %d configs...\n", len(wireGuardConfigs))
	b.addWireGuardDNSNew(template, wireGuardConfigs)

	// Update route rules for WireGuard AllowedIPs
	fmt.Printf("[BuildConfigForProfile] Adding WireGuard route rules...\n")
	b.updateRouteRulesForWireGuardNew(template, wireGuardConfigs)

	// Get proxies from subscription
	var proxies []ProxyConfig
	var err error

	if subscriptionURL != "" {
		isDirectLink := isDirectProxyLink(subscriptionURL)

		if isDirectLink {
			proxy, err := b.fetcher.ParseSingleLink(subscriptionURL)
			if err != nil {
				return fmt.Errorf("ошибка парсинга ссылки: %w", err)
			}
			proxy.Tag = generateTag(proxy, 0)
			proxies = []ProxyConfig{proxy}
		} else {
			proxies, err = b.fetcher.FetchAndParse(subscriptionURL)
			if err != nil {
				return fmt.Errorf("ошибка загрузки подписки: %w", err)
			}
			for i := range proxies {
				proxies[i].Tag = generateTag(proxies[i], i)
			}
		}

		splitResult := SplitProxyConfigs(proxies)
		if splitResult.AllFiltered {
			return fmt.Errorf("%s", splitResult.Message)
		}
		if len(splitResult.Filtered) > 0 {
			fmt.Printf("[BuildConfigForProfile] Warning: %s\n", splitResult.Message)
		}
		xrayBridge := BuildXrayBridgeConfig(splitResult.XrayBridge)
		proxies = append(splitResult.SingBox, xrayBridge.SingBoxProxies...)
		if len(splitResult.XrayBridge) > 0 {
			fmt.Printf("[BuildConfigForProfile] Bridged %d xhttp proxy/proxies through Xray\n", len(splitResult.XrayBridge))
		}

		if err := b.storage.UpdateProfileXrayConfig(profileID, xrayBridge.XrayConfig); err != nil {
			return err
		}
	} else {
		if err := b.storage.UpdateProfileXrayConfig(profileID, nil); err != nil {
			return err
		}
	}

	// Generate outbounds
	outbounds := b.generateOutbounds(template, proxies)
	template["outbounds"] = outbounds

	// WireGuard is now managed by Native WireGuard Manager
	// Remove any existing WireGuard from config
	delete(template, "endpoints")

	// Apply routing mode (blocked_only, except_russia, all_traffic)
	b.applyRoutingMode(template)

	// Add experimental section
	if err := b.addExperimentalAPI(template); err != nil {
		return err
	}

	// Remove template fields
	delete(template, "outbounds_template")
	delete(template, "_comment_outbounds")

	// Update profile in storage
	if err := b.storage.UpdateProfileSubscription(profileID, subscriptionURL, len(proxies), wireGuardConfigs); err != nil {
		return err
	}

	if err := b.storage.UpdateProfileConfig(profileID, template); err != nil {
		return err
	}

	return nil
}

// generateOutbounds generates outbounds list.
func (b *ConfigBuilderForStorage) generateOutbounds(template map[string]interface{}, proxies []ProxyConfig) []interface{} {
	outbounds := []interface{}{}
	proxyTags := []string{}

	for _, p := range proxies {
		outbounds = append(outbounds, p.ToSingboxOutbound())
		proxyTags = append(proxyTags, p.Tag)
	}

	outboundsTemplate, ok := template["outbounds_template"].(map[string]interface{})
	if !ok {
		outboundsTemplate = map[string]interface{}{}
	}

	if len(proxyTags) > 0 {
		outbounds = append(outbounds, buildAutoSelectOutbound(proxyTags))

		selectorOutbounds := append([]string{"auto-select"}, proxyTags...)
		selectorOutbounds = append(selectorOutbounds, "direct")

		if selector, ok := outboundsTemplate["selector"].(map[string]interface{}); ok {
			selector = copyMap(selector)
			selector["outbounds"] = selectorOutbounds
			outbounds = append(outbounds, selector)
		} else {
			outbounds = append(outbounds, map[string]interface{}{
				"type":      "selector",
				"tag":       "proxy",
				"outbounds": selectorOutbounds,
				"default":   "auto-select",
			})
		}
	} else {
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "proxy",
			"outbounds": []string{"direct"},
			"default":   "direct",
		})
	}

	if direct, ok := outboundsTemplate["direct"].(map[string]interface{}); ok {
		outbounds = append(outbounds, copyMap(direct))
	} else {
		outbounds = append(outbounds, map[string]interface{}{
			"type": "direct",
			"tag":  "direct",
		})
	}

	// block и dns-out удалены - в sing-box 1.11+ используются rule actions
	// action: "reject" вместо outbound: "block"
	// action: "hijack-dns" вместо outbound: "dns-out"

	return outbounds
}

// addWireGuardDNS adds DNS servers for WireGuard networks.
//
//lint:ignore U1000 Kept to decode and migrate pre-1.13 sing-box DNS layouts.
func (b *ConfigBuilderForStorage) addWireGuardDNS(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	dns, ok := template["dns"].(map[string]interface{})
	if !ok {
		return
	}

	servers, _ := dns["servers"].([]interface{})
	if servers == nil {
		servers = []interface{}{}
	}

	for _, wg := range wireGuardConfigs {
		if wg.DNS == "" {
			continue
		}

		serverTag := fmt.Sprintf("%s-dns", wg.Tag)
		// New sing-box 1.12+ DNS server format
		server := map[string]interface{}{
			"type":   "udp",
			"tag":    serverTag,
			"server": wg.DNS,
			"detour": wg.Tag,
		}
		servers = append(servers, server)
	}

	dns["servers"] = servers
}

// disableStrictRouteForWireGuard disables strict_route in TUN when WireGuard is used.
// This allows system routes (WireGuard interface) to work alongside sing-box TUN.
func (b *ConfigBuilderForStorage) disableStrictRouteForWireGuard(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	inbounds, ok := template["inbounds"].([]interface{})
	if !ok {
		return
	}

	for i, inbound := range inbounds {
		if inboundMap, ok := inbound.(map[string]interface{}); ok {
			if inboundMap["type"] == "tun" {
				// Disable strict_route to allow WireGuard routes to work
				inboundMap["strict_route"] = false
				inbounds[i] = inboundMap
				fmt.Printf("[disableStrictRouteForWireGuard] Disabled strict_route for TUN\n")
				break
			}
		}
	}

	template["inbounds"] = inbounds
}

// addWireGuardDNSNew adds DNS servers for WireGuard networks (native WireGuard mode).
// DNS queries go through "direct" - the WireGuard interface handles routing.
func (b *ConfigBuilderForStorage) addWireGuardDNSNew(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	dns, ok := template["dns"].(map[string]interface{})
	if !ok {
		return
	}

	servers, _ := dns["servers"].([]interface{})
	if servers == nil {
		servers = []interface{}{}
	}

	dnsRules, _ := dns["rules"].([]interface{})
	if dnsRules == nil {
		dnsRules = []interface{}{}
	}

	for _, wg := range wireGuardConfigs {
		if wg.DNS == "" {
			continue
		}

		dnsTag := fmt.Sprintf("dns-%s", wg.Tag)

		// Add DNS server - no special binding needed
		// Traffic to DNS server IP will be excluded from TUN and go through WireGuard
		server := map[string]interface{}{
			"type":        "udp",
			"tag":         dnsTag,
			"server":      wg.DNS,
			"server_port": 53,
		}
		servers = append(servers, server)

		// Build domain suffixes for DNS rule
		domainSuffixes := []string{}
		if wg.Endpoint != "" {
			parts := strings.Split(wg.Endpoint, ".")
			if len(parts) >= 2 {
				baseDomain := strings.Join(parts[len(parts)-2:], ".")
				domainSuffixes = append(domainSuffixes, baseDomain)
			}
		}
		domainSuffixes = append(domainSuffixes, "local", fmt.Sprintf("%s.local", wg.Tag))

		// Add DNS rule at the beginning
		dnsRule := map[string]interface{}{
			"domain_suffix": domainSuffixes,
			"action":        "route",
			"server":        dnsTag,
		}
		dnsRules = append([]interface{}{dnsRule}, dnsRules...)

		fmt.Printf("[addWireGuardDNSNew] Added DNS server %s (%s) for domains: %v\n", dnsTag, wg.DNS, domainSuffixes)
	}

	dns["servers"] = servers
	dns["rules"] = dnsRules
}

// updateRouteRulesForWireGuardNew updates route rules for WireGuard (native mode).
// Traffic goes through "direct" - the WireGuard interface handles routing based on AllowedIPs.
func (b *ConfigBuilderForStorage) updateRouteRulesForWireGuardNew(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	route, ok := template["route"].(map[string]interface{})
	if !ok {
		return
	}

	rules, ok := route["rules"].([]interface{})
	if !ok {
		rules = []interface{}{}
	}

	// Collect all AllowedIPs from WireGuard configs
	allWireGuardCIDRs := []string{}
	for _, wg := range wireGuardConfigs {
		allWireGuardCIDRs = append(allWireGuardCIDRs, wg.AllowedIPs...)
	}

	if len(allWireGuardCIDRs) == 0 {
		return
	}

	// Find position after hijack-dns
	insertIdx := 0
	for i, rule := range rules {
		if ruleMap, ok := rule.(map[string]interface{}); ok {
			action, _ := ruleMap["action"].(string)
			if action == "hijack-dns" {
				insertIdx = i + 1
				break
			}
			if action == "sniff" {
				insertIdx = i + 1
			}
		}
	}

	// Create route rule for WireGuard networks
	wgRule := map[string]interface{}{
		"ip_cidr":  allWireGuardCIDRs,
		"outbound": "direct",
	}

	// Insert rule after hijack-dns
	finalRules := make([]interface{}, 0, len(rules)+1)
	finalRules = append(finalRules, rules[:insertIdx]...)
	finalRules = append(finalRules, wgRule)
	finalRules = append(finalRules, rules[insertIdx:]...)

	route["rules"] = finalRules

	fmt.Printf("[updateRouteRulesForWireGuardNew] Added route rule for CIDRs: %v at position %d\n", allWireGuardCIDRs, insertIdx)
}

// updateRouteRulesForWireGuard updates route rules for WireGuard.
//
//lint:ignore U1000 Kept to decode and migrate pre-1.13 sing-box route layouts.
func (b *ConfigBuilderForStorage) updateRouteRulesForWireGuard(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	route, ok := template["route"].(map[string]interface{})
	if !ok {
		route = map[string]interface{}{}
		template["route"] = route
	}

	rules, _ := route["rules"].([]interface{})
	if rules == nil {
		rules = []interface{}{}
	}

	// Use existing GenerateRouteRulesForWireGuard function
	newRules := GenerateRouteRulesForWireGuard(wireGuardConfigs)

	// Convert to []interface{}
	newRulesInterface := make([]interface{}, len(newRules))
	for i, r := range newRules {
		newRulesInterface[i] = r
	}

	// Prepend new rules to existing ones
	newRulesInterface = append(newRulesInterface, rules...)
	route["rules"] = newRulesInterface
}

// addExperimentalAPI adds experimental section for traffic stats.
func (b *ConfigBuilderForStorage) addExperimentalAPI(template map[string]interface{}) error {
	if b.clashAPI == nil {
		b.clashAPI = newClashAPIAccess()
	}
	return b.clashAPI.apply(template)
}

// applyRoutingMode applies routing rules based on the selected routing mode.
func (b *ConfigBuilderForStorage) applyRoutingMode(template map[string]interface{}) {
	route, ok := template["route"].(map[string]interface{})
	if !ok {
		route = map[string]interface{}{}
		template["route"] = route
	}

	// Clean up DNS rules that reference remote rule_sets (geosite-*)
	b.cleanupDNSRuleSets(template)

	// Free access and RU-traffic settings are
	// cross-cutting: read fresh on every build rather than caching them on
	// the builder, so a settings change always takes effect on next rebuild
	// without needing every call site to push the new value down explicitly.
	settings := b.storage.GetAppSettings()
	outbounds, _ := template["outbounds"].([]interface{})
	hasVPNProxy := outboundTagExists(outbounds, "auto-select")
	b.applyDNSRouting(template, settings, hasVPNProxy)
	b.addFreeAccessOutbounds(template, settings)
	outbounds, _ = template["outbounds"].([]interface{})
	hasSmartBypass := outboundTagExists(outbounds, SmartBypassGroupTag)

	switch b.routingMode {
	case RoutingModeBlockedOnly:
		// Only blocked sites through VPN - use Re:filter + community rule-sets
		b.applyBlockedOnlyMode(route, settings, hasVPNProxy)

	case RoutingModeExceptRussia:
		// All except Russia through VPN - use built-in RU domain list
		b.applyExceptRussiaMode(route, settings, hasSmartBypass, hasVPNProxy)

	case RoutingModeAllTraffic:
		// All traffic through VPN - remove direct rules for Russia
		b.applyAllTrafficMode(route, settings, hasVPNProxy)

	default:
		// Unknown mode, use blocked_only as safest default
		fmt.Printf("[applyRoutingMode] Unknown mode %s, using blocked_only\n", b.routingMode)
		b.applyBlockedOnlyMode(route, settings, hasVPNProxy)
	}
}

// addFreeAccessOutbounds appends the outbounds needed for the free-access
// bypass and RU-traffic routing scheme:
//   - "byedpi" — local ByeDPI SOCKS5 outbound, only if the feature is enabled
//   - "ru-proxy" — only if "Hide RU traffic" is on and an address is set
//   - shared resilient groups (smart-bypass / vpn-or-direct / ru-route)
//   - one service-specific urltest group per enabled blocked service
//
// Generated groups/outbounds are skipped entirely when not needed, so a
// user who never touches these settings pays no extra health-check cost
// and preserves existing user preferences.
func (b *ConfigBuilderForStorage) addFreeAccessOutbounds(template map[string]interface{}, settings GlobalAppSettings) {
	outbounds, _ := template["outbounds"].([]interface{})

	if FreeMethodsAllowed(settings) {
		for _, strategy := range DefaultByeDPIStrategies {
			outbounds = append(outbounds, map[string]interface{}{
				"type":        "socks",
				"tag":         strategy.Tag,
				"server":      "127.0.0.1",
				"server_port": strategy.Port,
				"version":     "5",
			})
		}
	}

	ruProxyConfigured := false
	if settings.HideRuTraffic && settings.RuProxyAddress != "" {
		proxy, err := b.fetcher.ParseSingleLink(settings.RuProxyAddress)
		if err != nil {
			fmt.Printf("[addFreeAccessOutbounds] Failed to parse RU proxy address: %v\n", err)
		} else {
			proxy.Tag = RuProxyOutboundTag
			outbounds = append(outbounds, proxy.ToSingboxOutbound())
			ruProxyConfigured = true
		}
	}

	hasVPNProxy := outboundTagExists(outbounds, "auto-select")

	vpnOrDirect := []string{"direct"}
	if hasVPNProxy {
		vpnOrDirect = []string{"auto-select", "direct"}
	}

	if FreeMethodsAllowed(settings) || hasVPNProxy {
		// Blocked-service bypass must never include direct. A neutral urltest
		// target such as gstatic is often reachable directly, which made direct
		// win the group while Discord/YouTube still timed out.
		smartBypass := []string{"auto-select"}
		if FreeMethodsAllowed(settings) {
			smartBypass = FreeAccessCandidateTags(hasVPNProxy, true)
		}
		outbounds = append(outbounds, BuildResilientGroupWithURL(SmartBypassGroupTag, smartBypass, "https://discord.com"))

		if FreeMethodsAllowed(settings) || hasVPNProxy {
			needsNoRouteOutbound := false
			for _, svc := range DefaultFreeAccessServices {
				serviceCandidates := FreeAccessServiceCandidateTagsForSettings(svc, settings, hasVPNProxy)
				if len(serviceCandidates) == 0 {
					if FreeAccessServiceRouteOutboundForSettings(svc, settings, hasVPNProxy) == NoRouteOutboundTag {
						needsNoRouteOutbound = true
					}
					continue
				}
				outbounds = append(outbounds, BuildServiceRouteGroup(ServiceBypassGroupTag(svc.Tag), serviceCandidates))
			}
			if needsNoRouteOutbound {
				outbounds = append(outbounds, map[string]interface{}{
					"type": "block",
					"tag":  NoRouteOutboundTag,
				})
			}
		}
	}

	// Discord voice servers allocate both their WebSocket and UDP media ports
	// dynamically. Route that realtime plane through its own runtime-controlled
	// selector so the health monitor can keep web/API traffic untouched while it
	// keeps voice/video/Go Live on one route. Automatic mode prefers a
	// UDP-capable subscription node; the stable direct zapret2 discovery profile
	// remains available when a subscription is absent or explicitly bypassed.
	if hasVPNProxy {
		vpnCandidates := outboundGroupCandidates(outbounds, "auto-select")
		if len(vpnCandidates) == 0 {
			vpnCandidates = []string{"auto-select"}
		}
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       discordVPNGroupTag,
			"outbounds": vpnCandidates,
			"default":   vpnCandidates[0],
		})
	}
	realtimeCandidates := []string{"direct"}
	if hasVPNProxy {
		realtimeCandidates = append(realtimeCandidates, discordVPNGroupTag)
	}
	realtimeDefault := "direct"
	discordMethod := FreeAccessServiceMethod(settings, "discord")
	if discordMethod == FreeAccessMethodVPN || discordMethod == FreeAccessMethodAuto || !FreeMethodsAllowed(settings) {
		if hasVPNProxy {
			realtimeDefault = discordVPNGroupTag
		}
	}
	outbounds = append(outbounds, map[string]interface{}{
		"type":      "selector",
		"tag":       discordRealtimeGroupTag,
		"outbounds": realtimeCandidates,
		"default":   realtimeDefault,
	})

	outbounds = append(outbounds, BuildResilientGroup(VpnOrDirectGroupTag, vpnOrDirect))

	if settings.HideRuTraffic {
		ruCandidates := make([]string, 0, 3)
		if ruProxyConfigured {
			ruCandidates = append(ruCandidates, RuProxyOutboundTag)
		}
		ruCandidates = append(ruCandidates, vpnOrDirect...)
		outbounds = append(outbounds, BuildResilientGroup(RuRouteGroupTag, ruCandidates))
	}

	if freeAccessSettingsNeedNoRouteOutbound(settings, hasVPNProxy) && !outboundTagExists(outbounds, NoRouteOutboundTag) {
		outbounds = append(outbounds, map[string]interface{}{
			"type": "block",
			"tag":  NoRouteOutboundTag,
		})
	}

	template["outbounds"] = outbounds
}

func freeAccessSettingsNeedNoRouteOutbound(settings GlobalAppSettings, hasVPNProxy bool) bool {
	if hasVPNProxy {
		return false
	}
	for _, svc := range DefaultFreeAccessServices {
		if FreeAccessServiceRouteOutboundForSettings(svc, settings, hasVPNProxy) == NoRouteOutboundTag {
			return true
		}
	}
	return false
}

func outboundTagExists(outbounds []interface{}, tag string) bool {
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		if outboundMap["tag"] == tag {
			return true
		}
	}
	return false
}

func outboundGroupCandidates(outbounds []interface{}, tag string) []string {
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || outboundMap["tag"] != tag {
			continue
		}
		return interfaceStringSlice(outboundMap["outbounds"])
	}
	return nil
}

func buildFreeAccessProcessRules(settings GlobalAppSettings) []interface{} {
	processNames := []string{XrayExeName}
	if FreeMethodsAllowed(settings) {
		processNames = append(processNames, freeAccessProcessNames()...)
	}
	// Telegram MTProto sidecar egress: route DIRECT (its WS obfuscation works on
	// the direct path — free) UNLESS Telegram is forced to the VPN, in which case
	// we omit it so its DC-bound traffic falls through to the telegram ip_cidr
	// rule → bypass-telegram → VPN. This lets Telegram ride the subscription
	// through the already-saved local proxy without the user removing it.
	if FreeAccessServiceMethod(settings, "telegram") != FreeAccessMethodVPN {
		processNames = append(processNames, TgWsProxyProcessName)
	}
	processNames = uniqueStrings(processNames)
	if len(processNames) == 0 {
		return nil
	}

	return []interface{}{
		map[string]interface{}{
			"process_name": processNames,
			"action":       "route",
			"outbound":     "direct",
		},
	}
}

func freeAccessProcessNames() []string {
	return []string{ByeDPIProcessName, ZapretProcessName}
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		result = append(result, item)
	}
	return result
}

func buildDirectServiceRules() []interface{} {
	rules := make([]interface{}, 0, 2)
	if len(DirectIPCIDRs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr":  DirectIPCIDRs,
			"action":   "route",
			"outbound": "direct",
		})
	}
	if len(DirectProcessNames) > 0 {
		rules = append(rules, map[string]interface{}{
			"process_name": DirectProcessNames,
			"action":       "route",
			"outbound":     "direct",
		})
	}
	return rules
}

func buildBlockedServiceProtocolFallbackRules() []interface{} {
	youtubeDomains := freeAccessServiceDomainSuffixes("youtube")
	if len(youtubeDomains) == 0 {
		return nil
	}

	return []interface{}{
		map[string]interface{}{
			"domain_suffix": youtubeDomains,
			"network":       "udp",
			"port":          443,
			"action":        "reject",
		},
	}
}

func freeAccessServiceDomainSuffixes(tag string) []string {
	for _, svc := range DefaultFreeAccessServices {
		if svc.Tag == tag {
			return append([]string(nil), svc.DomainSuffixes...)
		}
	}
	return nil
}

func insertAfterFirstRouteRule(rules []interface{}, extra []interface{}) []interface{} {
	if len(extra) == 0 {
		return rules
	}
	if len(rules) == 0 {
		return extra
	}

	result := make([]interface{}, 0, len(rules)+len(extra))
	result = append(result, rules[:1]...)
	result = append(result, extra...)
	result = append(result, rules[1:]...)
	return result
}

// buildFreeAccessRules returns route rules sending each catalogued service to
// its own latency-tested bypass group (toggle on) or vpn-or-direct group
// (toggle off). Same rule set regardless of routing mode.
func (b *ConfigBuilderForStorage) buildFreeAccessRules(settings GlobalAppSettings, hasVPNProxy bool) []interface{} {
	rules := make([]interface{}, 0, len(DefaultFreeAccessServices)+1)
	rules = append(rules, buildDiscordRealtimeRules(settings, hasVPNProxy)...)

	for _, svc := range DefaultFreeAccessServices {
		outbound := FreeAccessServiceRouteOutboundForSettings(svc, settings, hasVPNProxy)
		if outbound == "" {
			continue
		}

		if len(svc.DomainSuffixes) > 0 {
			rules = append(rules, map[string]interface{}{
				"domain_suffix": svc.DomainSuffixes,
				"action":        "route",
				"outbound":      outbound,
			})
		}
		if len(svc.IPCIDRs) > 0 {
			rules = append(rules, map[string]interface{}{
				"ip_cidr":  svc.IPCIDRs,
				"action":   "route",
				"outbound": outbound,
			})
		}
		if len(svc.ProcessNames) > 0 {
			rules = append(rules, map[string]interface{}{
				"process_name": svc.ProcessNames,
				"action":       "route",
				"outbound":     outbound,
			})
		}
	}

	return rules
}

// buildDiscordRealtimeRules sends the complete realtime plane to a dedicated
// selector. package/process matching catches the IP-only dynamic UDP flow,
// while the discord.media rule keeps its voice WebSocket on the same route.
func buildDiscordRealtimeRules(settings GlobalAppSettings, hasVPNProxy bool) []interface{} {
	var discord *FreeAccessService
	for i := range DefaultFreeAccessServices {
		if DefaultFreeAccessServices[i].Tag == "discord" {
			discord = &DefaultFreeAccessServices[i]
			break
		}
	}
	if discord == nil || len(discord.ProcessNames) == 0 {
		return nil
	}

	return []interface{}{
		map[string]interface{}{
			"process_name": discord.ProcessNames,
			"network":      "udp",
			"action":       "route",
			"outbound":     discordRealtimeGroupTag,
		},
		map[string]interface{}{
			"domain_suffix": []string{"discord.media"},
			"network":       "tcp",
			"action":        "route",
			"outbound":      discordRealtimeGroupTag,
		},
	}
}

// blockedCatchAllOutbound returns which group the broad Re:filter/community
// rule_set catch-all (everything blocked that isn't one of the named
// free-access services) should route through.
func (b *ConfigBuilderForStorage) blockedCatchAllOutbound(settings GlobalAppSettings, hasVPNProxy bool) string {
	if FreeMethodsAllowed(settings) || hasVPNProxy {
		return SmartBypassGroupTag
	}
	return VpnOrDirectGroupTag
}

// ruRuleOutbound returns which outbound RU-classified domains should use:
// the "ru-route" group when the hide toggle is on, "direct" otherwise
// (default, unchanged behaviour).
func (b *ConfigBuilderForStorage) ruRuleOutbound(settings GlobalAppSettings) string {
	if settings.HideRuTraffic {
		return RuRouteGroupTag
	}
	return "direct"
}

// applyDNSRouting keeps DNS aligned with route rules. In blocked_only mode the
// route final is direct, so DNS must also be direct by default; otherwise
// ordinary RU sites can hang on an unreachable remote resolver before routing
// has a chance to select direct.
func (b *ConfigBuilderForStorage) applyDNSRouting(template map[string]interface{}, settings GlobalAppSettings, hasVPNProxy bool) {
	dns, ok := template["dns"].(map[string]interface{})
	if !ok {
		dns = map[string]interface{}{}
		template["dns"] = dns
	}

	preserved := preserveCustomDNSRules(dns["rules"])
	rules := make([]interface{}, 0, len(preserved)+4+len(DefaultFreeAccessServices))
	rules = append(rules, preserved...)
	rules = append(rules, map[string]interface{}{
		"domain_suffix": LocalDomainSuffixes,
		"action":        "route",
		"server":        "dns-local",
	})

	ruDNSServer := b.ruDNSServer(settings)
	if len(RuDomainSuffixes) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_suffix": RuDomainSuffixes,
			"action":        "route",
			"server":        ruDNSServer,
		})
	}
	if len(RuDomainKeywords) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_keyword": RuDomainKeywords,
			"action":         "route",
			"server":         ruDNSServer,
		})
	}

	for _, svc := range DefaultFreeAccessServices {
		if len(svc.DomainSuffixes) == 0 {
			continue
		}
		outbound := FreeAccessServiceRouteOutboundForSettings(svc, settings, hasVPNProxy)
		if outbound == "" {
			continue
		}
		server := "dns-remote"
		if outbound == "direct" {
			server = "dns-direct"
		}
		rules = append(rules, map[string]interface{}{
			"domain_suffix": svc.DomainSuffixes,
			"action":        "route",
			"server":        server,
		})
	}

	dns["rules"] = rules
	dns["final"] = b.finalDNSServer(settings)
	dns["strategy"] = "ipv4_only"
	dns["reverse_mapping"] = true
	dns["independent_cache"] = true

	if route, ok := template["route"].(map[string]interface{}); ok {
		route["default_domain_resolver"] = map[string]interface{}{
			"server":   b.finalDNSServer(settings),
			"strategy": "ipv4_only",
		}
	}
}

func preserveCustomDNSRules(value interface{}) []interface{} {
	rules, ok := value.([]interface{})
	if !ok {
		return nil
	}

	preserved := make([]interface{}, 0, len(rules))
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			preserved = append(preserved, rule)
			continue
		}

		server, _ := ruleMap["server"].(string)
		switch server {
		case "dns-local", "dns-direct", "dns-remote", "":
			continue
		default:
			preserved = append(preserved, normalizeDNSRuleSuffixes(ruleMap))
		}
	}

	return preserved
}

func normalizeDNSRuleSuffixes(rule map[string]interface{}) map[string]interface{} {
	raw, ok := rule["domain_suffix"]
	if !ok {
		return rule
	}

	normalized := normalizeStringList(raw)
	if len(normalized) == 0 {
		return rule
	}

	copyRule := make(map[string]interface{}, len(rule))
	for key, value := range rule {
		copyRule[key] = value
	}
	copyRule["domain_suffix"] = normalized
	return copyRule
}

func normalizeStringList(value interface{}) []string {
	switch typed := value.(type) {
	case []string:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if normalized := strings.TrimPrefix(item, "."); normalized != "" {
				result = append(result, normalized)
			}
		}
		return result
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				continue
			}
			if normalized := strings.TrimPrefix(text, "."); normalized != "" {
				result = append(result, normalized)
			}
		}
		return result
	default:
		return nil
	}
}

func (b *ConfigBuilderForStorage) ruDNSServer(settings GlobalAppSettings) string {
	if settings.HideRuTraffic {
		return "dns-remote"
	}
	return "dns-direct"
}

func (b *ConfigBuilderForStorage) finalDNSServer(settings GlobalAppSettings) string {
	if settings.HideRuTraffic || b.routingMode == RoutingModeAllTraffic || b.routingMode == RoutingModeExceptRussia {
		return "dns-remote"
	}
	return "dns-direct"
}

// cleanupDNSRuleSets removes DNS rules that reference remote rule_sets (geosite-*).
// These are not available in blocked_only and all_traffic modes.
func (b *ConfigBuilderForStorage) cleanupDNSRuleSets(template map[string]interface{}) {
	dns, ok := template["dns"].(map[string]interface{})
	if !ok {
		return
	}

	rules, ok := dns["rules"].([]interface{})
	if !ok {
		return
	}

	// Filter out rules that use rule_set with geosite-*
	newRules := make([]interface{}, 0, len(rules))
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			newRules = append(newRules, rule)
			continue
		}

		// Check if this rule uses rule_set
		if ruleSet, hasRuleSet := ruleMap["rule_set"]; hasRuleSet {
			// Skip rules with geosite-* rule_sets
			if ruleSetArr, ok := ruleSet.([]interface{}); ok {
				hasGeosite := false
				for _, rs := range ruleSetArr {
					if rsStr, ok := rs.(string); ok {
						if strings.HasPrefix(rsStr, "geosite-") || strings.HasPrefix(rsStr, "geoip-") {
							hasGeosite = true
							break
						}
					}
				}
				if hasGeosite {
					fmt.Printf("[cleanupDNSRuleSets] Removed DNS rule with remote rule_set: %v\n", ruleSet)
					continue
				}
			}
		}

		newRules = append(newRules, rule)
	}

	dns["rules"] = newRules
}

// applyBlockedOnlyMode configures routing for blocked sites only.
func (b *ConfigBuilderForStorage) applyBlockedOnlyMode(route map[string]interface{}, settings GlobalAppSettings, hasVPNProxy bool) {
	fmt.Printf("[applyRoutingMode] Using blocked_only mode with local filters\n")

	// Get local filter rule_sets
	filterRuleSets := b.filterManager.GetRuleSetConfigs()
	if len(filterRuleSets) == 0 {
		fmt.Printf("[applyRoutingMode] WARNING: No filter files found, falling back to except_russia\n")
		return
	}

	// Build new rule_set array with only local filters
	newRuleSets := make([]interface{}, 0, len(filterRuleSets))
	for _, rs := range filterRuleSets {
		newRuleSets = append(newRuleSets, rs)
	}
	route["rule_set"] = newRuleSets

	// Build new rules for blocked_only mode
	// Order: sniff → dns → private → RU (direct/ru-route) → free-access services →
	//        broad blocked catch-all (smart-bypass/vpn-or-direct) → final:direct
	newRules := []interface{}{
		// 1. Sniff for protocol detection
		map[string]interface{}{
			"action": "sniff",
		},
		// 2. Hijack DNS (early for faster resolution)
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		// 3. Private IPs direct (covers .local, LAN, etc.)
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
		// 4. Local/internal domain suffixes direct
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
	}
	newRules = insertAfterFirstRouteRule(newRules, buildFreeAccessProcessRules(settings))

	// 5. WireGuard routing for internal networks (if configured)
	// This is handled separately by WireGuard integration, not here
	// Internal networks go through WireGuard tunnel directly

	// 6. RU traffic: direct by default, "ru-route" if the hide toggle is on.
	newRules = append(newRules, buildDirectServiceRules()...)

	newRules = append(newRules,
		map[string]interface{}{
			"domain_suffix": RuDomainSuffixes,
			"action":        "route",
			"outbound":      b.ruRuleOutbound(settings),
		},
		map[string]interface{}{
			"domain_keyword": RuDomainKeywords,
			"action":         "route",
			"outbound":       b.ruRuleOutbound(settings),
		},
	)

	newRules = append(newRules, buildBlockedServiceProtocolFallbackRules()...)

	// 7. Named free-access services: smart-bypass (toggle on) or vpn-or-direct (toggle off)
	newRules = append(newRules, b.buildFreeAccessRules(settings, hasVPNProxy)...)

	// 8. Everything else blocked by RKN filters. Community rule-sets are too
	// broad for blocked_only mode and can catch ordinary traffic such as
	// Microsoft Edge update hosts; geo/AI/YouTube services are covered by the
	// named free-access rules above.
	newRules = append(newRules, map[string]interface{}{
		"rule_set": []string{
			"refilter-domains", // RKN blocked domains
			"refilter-ips",     // RKN blocked IPs
			"discord-ips",      // Discord voice/media IPs
		},
		"action":   "route",
		"outbound": b.blockedCatchAllOutbound(settings, hasVPNProxy),
	})

	route["rules"] = newRules
	route["final"] = "direct"

	fmt.Printf("[applyRoutingMode] Applied blocked_only: %d rule_sets, %d rules, final=direct\n",
		len(newRuleSets), len(newRules))
}

// applyAllTrafficMode configures routing for all traffic through VPN.
//
// RU traffic is intentionally NOT carved out here even when "Скрывать
// RU-трафик" is on: this mode's entire point is "everything through VPN",
// which already includes RU domains via final=proxy — the hide-toggle has
// no additional effect in this mode.
func (b *ConfigBuilderForStorage) applyAllTrafficMode(route map[string]interface{}, settings GlobalAppSettings, hasVPNProxy bool) {
	fmt.Printf("[applyRoutingMode] Using all_traffic mode\n")

	// Remove rule_sets (not needed for all traffic mode)
	route["rule_set"] = []interface{}{}

	// Minimal rules
	newRules := []interface{}{
		map[string]interface{}{
			"action": "sniff",
		},
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
	}
	newRules = insertAfterFirstRouteRule(newRules, buildFreeAccessProcessRules(settings))

	// Named free-access services still get a chance at a faster bypass route
	// before falling through to the VPN proxy everything else uses here.
	newRules = append(newRules, buildBlockedServiceProtocolFallbackRules()...)
	newRules = append(newRules, b.buildFreeAccessRules(settings, hasVPNProxy)...)

	route["rules"] = newRules
	route["final"] = "proxy"

	fmt.Printf("[applyRoutingMode] Applied all_traffic: %d rules, final=proxy\n", len(newRules))
}

// applyExceptRussiaMode configures routing for all traffic except Russia through VPN.
// Uses built-in domain list instead of remote geosite to avoid download issues.
func (b *ConfigBuilderForStorage) applyExceptRussiaMode(route map[string]interface{}, settings GlobalAppSettings, hasSmartBypass bool, hasVPNProxy bool) {
	fmt.Printf("[applyRoutingMode] Using except_russia mode with built-in domain list\n")

	// No remote rule_sets needed - we use built-in domain suffixes
	route["rule_set"] = []interface{}{}

	newRules := []interface{}{
		// 1. Sniff for protocol detection
		map[string]interface{}{
			"action": "sniff",
		},
		// 2. Local domains direct
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		// 3. Hijack DNS
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		// 4. Private IPs direct
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
		// 5. Russian domains: direct by default, "ru-route" if the hide toggle is on.
		map[string]interface{}{
			"domain_suffix": RuDomainSuffixes,
			"action":        "route",
			"outbound":      b.ruRuleOutbound(settings),
		},
		// 6. Russian domain keywords: same outbound as above
		map[string]interface{}{
			"domain_keyword": RuDomainKeywords,
			"action":         "route",
			"outbound":       b.ruRuleOutbound(settings),
		},
	}
	newRules = insertAfterFirstRouteRule(newRules, buildFreeAccessProcessRules(settings))

	// 7. Named free-access services get their own latency-tested bypass route
	// before falling through to the shared foreign-traffic bypass/VPN group.
	newRules = append(newRules, buildBlockedServiceProtocolFallbackRules()...)
	newRules = append(newRules, b.buildFreeAccessRules(settings, hasVPNProxy)...)

	finalOutbound := VpnOrDirectGroupTag
	if hasSmartBypass {
		finalOutbound = SmartBypassGroupTag
	}

	route["rules"] = newRules
	route["final"] = finalOutbound

	fmt.Printf("[applyRoutingMode] Applied except_russia: %d domain suffixes, %d keywords, final=%s\n",
		len(RuDomainSuffixes), len(RuDomainKeywords), finalOutbound)
}

// isDirectProxyLink checks if URL is a direct proxy link.
func isDirectProxyLink(url string) bool {
	if len(url) < 5 {
		return false
	}
	return strings.HasPrefix(url, "vless://") ||
		strings.HasPrefix(url, "trojan://") ||
		strings.HasPrefix(url, "ss://") ||
		strings.HasPrefix(url, "vmess://")
}

// GetUserSettings returns user settings for active profile (compatibility method).
func (s *Storage) GetUserSettings() (*UserSettings, error) {
	profile, err := s.GetActiveProfile()
	if err != nil || profile == nil {
		return &UserSettings{}, nil
	}

	return &UserSettings{
		SubscriptionURL:  profile.SubscriptionURL,
		LastUpdated:      profile.LastUpdated,
		ProxyCount:       profile.ProxyCount,
		WireGuardConfigs: profile.WireGuardConfigs,
	}, nil
}

// GetConfigPath returns path to active config file (written on demand).
func (s *Storage) GetConfigPath() (string, error) {
	return s.WriteActiveConfigToFile()
}

// MigrateFromOldFormat migrates data from old file structure to new settings.json.
// Only migrates if settings.json didn't exist before (was just created).
func (s *Storage) MigrateFromOldFormat(basePath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Skip migration if we already have profiles with data (settings.json existed)
	if len(s.data.Profiles) > 0 && s.data.Profiles[0].SubscriptionURL != "" {
		return nil // Already have data, skip migration
	}

	migrated := false

	// Try to migrate old profiles.json
	oldProfilesPath := filepath.Join(basePath, "profiles.json")
	if fileExists(oldProfilesPath) {
		data, err := os.ReadFile(oldProfilesPath)
		if err == nil {
			var oldProfiles []ConnectionProfile
			if json.Unmarshal(data, &oldProfiles) == nil {
				for _, oldP := range oldProfiles {
					// Check if profile already exists
					exists := false
					for _, p := range s.data.Profiles {
						if p.ID == oldP.ID {
							exists = true
							break
						}
					}

					if !exists {
						s.data.Profiles = append(s.data.Profiles, ProfileData{
							ID:        oldP.ID,
							Name:      oldP.Name,
							CreatedAt: oldP.CreatedAt,
						})
					}
				}
				migrated = true
			}
		}
	}

	// Try to migrate old user_settings files
	for i := range s.data.Profiles {
		profileID := s.data.Profiles[i].ID

		var settingsPath string
		if profileID == DefaultProfileID {
			settingsPath = filepath.Join(basePath, "user_settings.json")
		} else {
			settingsPath = filepath.Join(basePath, fmt.Sprintf("user_settings_%d.json", profileID))
		}

		if fileExists(settingsPath) {
			data, err := os.ReadFile(settingsPath)
			if err == nil {
				var oldSettings UserSettings
				if json.Unmarshal(data, &oldSettings) == nil {
					s.data.Profiles[i].SubscriptionURL = oldSettings.SubscriptionURL
					s.data.Profiles[i].LastUpdated = oldSettings.LastUpdated
					s.data.Profiles[i].ProxyCount = oldSettings.ProxyCount
					s.data.Profiles[i].WireGuardConfigs = oldSettings.WireGuardConfigs
					migrated = true
				}
			}
		}

		// Try to migrate old config files
		var configPath string
		if profileID == DefaultProfileID {
			configPath = filepath.Join(basePath, "config.json")
		} else {
			configPath = filepath.Join(basePath, fmt.Sprintf("config_%d.json", profileID))
		}

		if fileExists(configPath) {
			data, err := os.ReadFile(configPath)
			if err == nil {
				var oldConfig map[string]interface{}
				if json.Unmarshal(data, &oldConfig) == nil {
					s.data.Profiles[i].SingboxConfig = oldConfig
					migrated = true
				}
			}
		}
	}

	// Migrate old app_config.json
	oldAppConfigPath := filepath.Join(os.Getenv("LOCALAPPDATA"), LegacyAppDataDirName, "app_config.json")
	if fileExists(oldAppConfigPath) {
		data, err := os.ReadFile(oldAppConfigPath)
		if err == nil {
			var oldConfig AppConfigLegacy
			if json.Unmarshal(data, &oldConfig) == nil {
				s.data.App.AutoStart = oldConfig.AutoStart
				s.data.App.Notifications = true
				s.data.App.CheckUpdates = oldConfig.CheckUpdates
				s.data.App.EnableLogging = true
				s.data.App.LogLevel = oldConfig.LogLevel
				s.data.App.Theme = oldConfig.Theme
				s.data.App.Language = oldConfig.Language
				s.data.App.AutoUpdateSub = true
				s.data.App.SubUpdateInterval = oldConfig.SubUpdateInterval
				s.data.App.LastSubUpdate = oldConfig.LastSubUpdate
				s.data.App.LastUpdateCheck = oldConfig.LastUpdateCheck
				s.data.App.ActiveProfileID = oldConfig.ActiveProfileID
				migrated = true
				// Remove old file after migration
				os.Remove(oldAppConfigPath)
			}
		}
	}

	if migrated {
		// Remove old files after successful migration
		os.Remove(filepath.Join(basePath, "profiles.json"))
		os.Remove(filepath.Join(basePath, "user_settings.json"))
		os.Remove(filepath.Join(basePath, "config.json"))
		// Remove profile-specific old files
		for i := 2; i <= 10; i++ {
			os.Remove(filepath.Join(basePath, fmt.Sprintf("user_settings_%d.json", i)))
			os.Remove(filepath.Join(basePath, fmt.Sprintf("config_%d.json", i)))
		}
		return s.saveInternal()
	}

	return nil
}
