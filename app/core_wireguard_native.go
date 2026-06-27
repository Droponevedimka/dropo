// Package main provides native WireGuard management for dropo.
// Manages WireGuard tunnels through Native OS integration:
// - Windows: WireGuard Service (wireguard.exe /installtunnelservice) with bundled binaries
// - macOS: wireguard-go with utun interface
// - Linux: wg-quick or wireguard-tools
// This separates WireGuard from sing-box for better stability and performance.
//
// Bundled binaries are stored in app/dependencies/wireguard-windows-v{version}/
// and copied to build/bin/ during wails build.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version variables - injected via ldflags at build time
var (
	// WireGuard version - bundled with the application
	WireGuardVersion = "0.5.3"

	// Wintun version (TUN driver for Windows)
	WintunVersion = "0.14.1"
)

const (
	// Tunnel naming
	TunnelPrefix       = "dropo-wg-"
	LegacyTunnelPrefix = "kampus-wg-"

	// Timeouts
	TunnelStartTimeout = 10 * time.Second
	TunnelStopTimeout  = 5 * time.Second
)

// NativeWireGuardManager manages WireGuard tunnels via native OS integration
type NativeWireGuardManager struct {
	basePath        string                  // Application base path (where exe is)
	configDir       string                  // Directory for .conf files
	wireguardPath   string                  // Path to wireguard executable
	wgPath          string                  // Path to wg tool (for status)
	wintunPath      string                  // Path to wintun.dll (Windows only)
	tunnels         map[string]*TunnelState // Active tunnels
	mu              sync.RWMutex
	logger          func(string)       // Logging function
	healthCheckStop chan struct{}      // Stop signal for health check
	healthCheckWg   sync.WaitGroup     // Wait group for health check goroutine
	onTunnelRestart func(configID int) // Callback when tunnel is restarted
}

// TunnelState tracks the state of a WireGuard tunnel
type TunnelState struct {
	Name          string           `json:"name"`
	ConfigID      int              `json:"config_id"`
	ConfigPath    string           `json:"config_path"`
	StartedAt     time.Time        `json:"started_at"`
	Active        bool             `json:"active"`
	PID           int              `json:"pid,omitempty"`  // For Linux/macOS processes
	LastHandshake time.Time        `json:"last_handshake"` // Last successful handshake
	Healthy       bool             `json:"healthy"`        // Current health status
	RestartCount  int              `json:"restart_count"`  // Number of restarts
	Config        *WireGuardConfig `json:"-"`              // Original config for restart
}

// HealthCheckInterval defines how often to check tunnel health
const HealthCheckInterval = 30 * time.Second

// HandshakeTimeout defines maximum time since last handshake before considering unhealthy
const HandshakeTimeout = 3 * time.Minute

// MaxRestartAttempts defines maximum restart attempts before giving up
const MaxRestartAttempts = 3

// NewNativeWireGuardManager creates a new Native WireGuard Manager
// Expects bundled binaries in the same directory as the executable
func NewNativeWireGuardManager(basePath string, logger func(string)) *NativeWireGuardManager {
	m := &NativeWireGuardManager{
		basePath:  basePath,
		configDir: filepath.Join(basePath, "wireguard"),
		tunnels:   make(map[string]*TunnelState),
		logger:    logger,
	}

	// Set paths to bundled binaries (in same dir as executable)
	m.setPlatformPaths()

	return m
}

// setPlatformPaths sets executable paths based on current OS
// Binaries are bundled in bin/ subdirectory relative to the main executable
func (m *NativeWireGuardManager) setPlatformPaths() {
	binDir := filepath.Join(m.basePath, "bin")

	switch runtime.GOOS {
	case "windows":
		// Bundled binaries in bin/ subdirectory
		m.wireguardPath = filepath.Join(binDir, "wireguard.exe")
		m.wgPath = filepath.Join(binDir, "wg.exe")
		m.wintunPath = filepath.Join(binDir, "wintun.dll")
	case "darwin":
		m.wireguardPath = filepath.Join(binDir, "wireguard-go")
		m.wgPath = filepath.Join(binDir, "wg")
	case "linux":
		m.wireguardPath = filepath.Join(binDir, "wg-quick")
		m.wgPath = filepath.Join(binDir, "wg")
	}
}

// Init initializes the manager, creating directories
func (m *NativeWireGuardManager) Init() error {
	// Create wireguard config directory
	if err := os.MkdirAll(m.configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Check if WireGuard binaries exist
	if !m.IsInstalled() {
		m.log("WireGuard binaries not found - bundled binaries missing")
	} else {
		m.log(fmt.Sprintf("WireGuard v%s ready at: %s", WireGuardVersion, m.wireguardPath))
	}

	// Clean up orphaned tunnels from previous sessions
	m.CleanupOrphanedTunnels()

	return nil
}

// CleanupOrphanedTunnels removes dropo-wg-* tunnels and legacy kampus-wg-* tunnels.
func (m *NativeWireGuardManager) CleanupOrphanedTunnels() {
	if runtime.GOOS != "windows" {
		return // Only needed on Windows where services persist
	}

	m.log("Checking for orphaned tunnels...")

	// Query Windows services for dropo and legacy WireGuard tunnel services.
	cmd := exec.Command("sc", "query", "type=", "service", "state=", "all")
	output, err := combinedOutputManagedCommand(cmd, "sc query wireguard services", m.log)
	if err != nil {
		m.log(fmt.Sprintf("Failed to query services: %v", err))
		return
	}

	// Find all dropo-wg-* and legacy kampus-wg-* services.
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "SERVICE_NAME:") {
			serviceName := strings.TrimPrefix(line, "SERVICE_NAME:")
			serviceName = strings.TrimSpace(serviceName)

			if strings.HasPrefix(serviceName, "WireGuardTunnel$"+TunnelPrefix) ||
				strings.HasPrefix(serviceName, "WireGuardTunnel$"+LegacyTunnelPrefix) {
				// Extract tunnel name from service name
				tunnelName := strings.TrimPrefix(serviceName, "WireGuardTunnel$")
				m.log(fmt.Sprintf("Found orphaned tunnel: %s, stopping...", tunnelName))

				// Stop using wireguard.exe /uninstalltunnelservice
				stopCmd := exec.Command(m.wireguardPath, "/uninstalltunnelservice", tunnelName)
				if stopOutput, stopErr := combinedOutputManagedCommand(stopCmd, "wireguard uninstall orphan tunnel", m.log); stopErr != nil {
					m.log(fmt.Sprintf("Failed to stop orphaned tunnel %s: %v, output: %s", tunnelName, stopErr, string(stopOutput)))
				} else {
					m.log(fmt.Sprintf("Stopped orphaned tunnel: %s", tunnelName))
				}
			}
		}
	}
}

// log writes a log message
func (m *NativeWireGuardManager) log(msg string) {
	if m.logger != nil {
		m.logger(fmt.Sprintf("[WireGuard] %s", msg))
	}
}

// IsInstalled checks if WireGuard binaries are available
func (m *NativeWireGuardManager) IsInstalled() bool {
	// Check our bundled executable
	if fileExists(m.wireguardPath) {
		return true
	}

	// Check system-wide installation
	return m.checkSystemWireGuard()
}

// checkSystemWireGuard checks for system-wide WireGuard installation
func (m *NativeWireGuardManager) checkSystemWireGuard() bool {
	switch runtime.GOOS {
	case "windows":
		// Check common Windows paths
		paths := []string{
			`C:\Program Files\WireGuard\wireguard.exe`,
			`C:\Program Files (x86)\WireGuard\wireguard.exe`,
		}
		for _, p := range paths {
			if fileExists(p) {
				m.wireguardPath = p
				m.wgPath = filepath.Join(filepath.Dir(p), "wg.exe")
				return true
			}
		}

		// Check PATH
		if path, err := exec.LookPath("wireguard.exe"); err == nil {
			m.wireguardPath = path
			m.wgPath = filepath.Join(filepath.Dir(path), "wg.exe")
			return true
		}

	case "darwin":
		// macOS: check Homebrew installation
		brewPaths := []string{
			"/opt/homebrew/bin/wg",
			"/usr/local/bin/wg",
		}
		for _, p := range brewPaths {
			if fileExists(p) {
				m.wgPath = p
				// wireguard-go might be in same directory
				wgGo := filepath.Join(filepath.Dir(p), "wireguard-go")
				if fileExists(wgGo) {
					m.wireguardPath = wgGo
				}
				return true
			}
		}

		// Check PATH
		if path, err := exec.LookPath("wg"); err == nil {
			m.wgPath = path
			return true
		}

	case "linux":
		// Linux: check for wg-quick
		paths := []string{
			"/usr/bin/wg-quick",
			"/usr/local/bin/wg-quick",
		}
		for _, p := range paths {
			if fileExists(p) {
				m.wireguardPath = p
				m.wgPath = strings.TrimSuffix(p, "-quick")
				return true
			}
		}

		// Check PATH
		if path, err := exec.LookPath("wg-quick"); err == nil {
			m.wireguardPath = path
			if wgPath, err := exec.LookPath("wg"); err == nil {
				m.wgPath = wgPath
			}
			return true
		}
	}

	return false
}

// GetStatus returns current WireGuard status
func (m *NativeWireGuardManager) GetStatus() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	activeTunnels := make([]map[string]interface{}, 0)
	for _, t := range m.tunnels {
		if t.Active {
			activeTunnels = append(activeTunnels, map[string]interface{}{
				"name":       t.Name,
				"config_id":  t.ConfigID,
				"started_at": t.StartedAt.Format(time.RFC3339),
			})
		}
	}

	return map[string]interface{}{
		"installed":      m.IsInstalled(),
		"version":        WireGuardVersion,
		"wintun_version": WintunVersion,
		"platform":       runtime.GOOS,
		"arch":           runtime.GOARCH,
		"wireguard_path": m.wireguardPath,
		"active_tunnels": activeTunnels,
		"tunnel_count":   len(activeTunnels),
	}
}

// GenerateConfFile generates a WireGuard .conf file from config
func (m *NativeWireGuardManager) GenerateConfFile(config *WireGuardConfig) string {
	var sb strings.Builder

	sb.WriteString("[Interface]\n")
	sb.WriteString(fmt.Sprintf("PrivateKey = %s\n", config.PrivateKey))

	// Address - can be multiple
	if len(config.Address) > 0 {
		sb.WriteString(fmt.Sprintf("Address = %s\n", strings.Join(config.Address, ", ")))
	}

	// DNS
	if config.DNS != "" {
		sb.WriteString(fmt.Sprintf("DNS = %s\n", config.DNS))
	}

	// MTU
	if config.MTU > 0 {
		sb.WriteString(fmt.Sprintf("MTU = %d\n", config.MTU))
	}

	// Peers
	for _, peer := range config.Peers {
		sb.WriteString("\n[Peer]\n")
		sb.WriteString(fmt.Sprintf("PublicKey = %s\n", peer.PublicKey))

		if peer.PresharedKey != "" {
			sb.WriteString(fmt.Sprintf("PresharedKey = %s\n", peer.PresharedKey))
		}

		// Endpoint
		if peer.Endpoint != "" && peer.Port > 0 {
			sb.WriteString(fmt.Sprintf("Endpoint = %s:%d\n", peer.Endpoint, peer.Port))
		}

		// AllowedIPs
		if len(peer.AllowedIPs) > 0 {
			sb.WriteString(fmt.Sprintf("AllowedIPs = %s\n", strings.Join(peer.AllowedIPs, ", ")))
		}

		// PersistentKeepalive
		if peer.PersistentKeepalive > 0 {
			sb.WriteString(fmt.Sprintf("PersistentKeepalive = %d\n", peer.PersistentKeepalive))
		}
	}

	return sb.String()
}

// WriteConfigFile writes config to .conf file and returns path
func (m *NativeWireGuardManager) WriteConfigFile(name string, config *WireGuardConfig) (string, error) {
	confPath := filepath.Join(m.configDir, name+".conf")
	content := m.GenerateConfFile(config)

	// Write with restricted permissions (contains private key)
	if err := os.WriteFile(confPath, []byte(content), 0600); err != nil {
		return "", fmt.Errorf("failed to write config file: %w", err)
	}

	m.log(fmt.Sprintf("Config written to: %s", confPath))
	return confPath, nil
}

// StartTunnel starts a WireGuard tunnel
func (m *NativeWireGuardManager) StartTunnel(configID int, config *WireGuardConfig) error {
	if !m.IsInstalled() {
		return fmt.Errorf("WireGuard is not installed")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Generate tunnel name
	name := fmt.Sprintf("%s%d", TunnelPrefix, configID)

	// Check if already running
	if state, exists := m.tunnels[name]; exists && state.Active {
		m.log(fmt.Sprintf("Tunnel %s already running", name))
		return nil
	}

	// Write config file
	confPath, err := m.WriteConfigFile(name, config)
	if err != nil {
		return err
	}

	m.log(fmt.Sprintf("Starting tunnel: %s", name))

	// Start tunnel using wireguard.exe /installtunnelservice
	cmd := exec.Command(m.wireguardPath, "/installtunnelservice", confPath)

	output, err := combinedOutputManagedCommand(cmd, "wireguard install tunnel", m.log)
	if err != nil {
		m.log(fmt.Sprintf("Failed to start tunnel: %v, output: %s", err, string(output)))
		return fmt.Errorf("failed to start tunnel: %w", err)
	}

	// Track tunnel state
	m.tunnels[name] = &TunnelState{
		Name:       name,
		ConfigID:   configID,
		ConfigPath: confPath,
		StartedAt:  time.Now(),
		Active:     true,
		Healthy:    true,   // Assume healthy on start
		Config:     config, // Store config for potential restart
	}

	m.log(fmt.Sprintf("Tunnel %s started successfully", name))
	return nil
}

// StopTunnel stops a WireGuard tunnel
func (m *NativeWireGuardManager) StopTunnel(configID int) error {
	if !m.IsInstalled() {
		return fmt.Errorf("WireGuard is not installed")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	name := fmt.Sprintf("%s%d", TunnelPrefix, configID)

	state, exists := m.tunnels[name]
	if !exists || !state.Active {
		m.log(fmt.Sprintf("Tunnel %s not running", name))
		return nil
	}

	m.log(fmt.Sprintf("Stopping tunnel: %s", name))

	// Stop tunnel using wireguard.exe /uninstalltunnelservice
	cmd := exec.Command(m.wireguardPath, "/uninstalltunnelservice", name)

	output, err := combinedOutputManagedCommand(cmd, "wireguard uninstall tunnel", m.log)
	if err != nil {
		m.log(fmt.Sprintf("Failed to stop tunnel: %v, output: %s", err, string(output)))
		// Continue anyway to clean up state
	}

	// Update state
	state.Active = false

	m.log(fmt.Sprintf("Tunnel %s stopped", name))
	return nil
}

// StopAllTunnels stops all managed tunnels and cleans up orphaned ones
func (m *NativeWireGuardManager) StopAllTunnels() {
	m.mu.RLock()
	tunnelIDs := make([]int, 0)
	for _, state := range m.tunnels {
		if state.Active {
			tunnelIDs = append(tunnelIDs, state.ConfigID)
		}
	}
	m.mu.RUnlock()

	for _, id := range tunnelIDs {
		if err := m.StopTunnel(id); err != nil {
			m.log(fmt.Sprintf("Error stopping tunnel %d: %v", id, err))
		}
	}

	// Also cleanup any orphaned tunnels (in case of crash recovery)
	m.CleanupOrphanedTunnels()
}

// GetActiveTunnels returns list of active tunnels
func (m *NativeWireGuardManager) GetActiveTunnels() []TunnelState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var active []TunnelState
	for _, state := range m.tunnels {
		if state.Active {
			active = append(active, *state)
		}
	}
	return active
}

// IsTunnelActive checks if a specific tunnel is active
func (m *NativeWireGuardManager) IsTunnelActive(configID int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	name := fmt.Sprintf("%s%d", TunnelPrefix, configID)
	if state, exists := m.tunnels[name]; exists {
		return state.Active
	}
	return false
}

// GetTunnelStats gets statistics for a tunnel (requires wg.exe)
func (m *NativeWireGuardManager) GetTunnelStats(configID int) (map[string]interface{}, error) {
	if !fileExists(m.wgPath) {
		return nil, fmt.Errorf("wg.exe not found")
	}

	name := fmt.Sprintf("%s%d", TunnelPrefix, configID)

	cmd := exec.Command(m.wgPath, "show", name)
	output, err := combinedOutputManagedCommand(cmd, "wg show", m.log)
	if err != nil {
		return nil, fmt.Errorf("failed to get tunnel stats: %w", err)
	}

	// Parse wg show output
	stats := m.parseWgShowOutput(string(output))
	return stats, nil
}

// parseWgShowOutput parses the output of `wg show` command
func (m *NativeWireGuardManager) parseWgShowOutput(output string) map[string]interface{} {
	stats := make(map[string]interface{})

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "transfer:") {
			// Parse transfer stats
			parts := strings.Split(line, ",")
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if strings.Contains(part, "received") {
					stats["received"] = strings.TrimSuffix(strings.TrimPrefix(part, "transfer: "), " received")
				} else if strings.Contains(part, "sent") {
					stats["sent"] = strings.TrimSuffix(part, " sent")
				}
			}
		} else if strings.HasPrefix(line, "latest handshake:") {
			stats["last_handshake"] = strings.TrimPrefix(line, "latest handshake: ")
		}
	}

	return stats
}

// CleanupConfigs removes all .conf files for stopped tunnels
func (m *NativeWireGuardManager) CleanupConfigs() error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	files, err := os.ReadDir(m.configDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".conf") {
			name := strings.TrimSuffix(file.Name(), ".conf")

			// Check if tunnel is active
			if state, exists := m.tunnels[name]; !exists || !state.Active {
				confPath := filepath.Join(m.configDir, file.Name())
				if err := os.Remove(confPath); err != nil {
					m.log(fmt.Sprintf("Failed to remove config: %s", confPath))
				}
			}
		}
	}

	return nil
}

// GetWireGuardVersion returns the bundled WireGuard version
func (m *NativeWireGuardManager) GetWireGuardVersion() string {
	return WireGuardVersion
}

// copyFileNative copies a file from src to dst (for WireGuard native manager)
func copyFileNative(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

// checksumFile calculates SHA256 checksum of a file
func checksumFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

// SetTunnelRestartCallback sets a callback function to be called when a tunnel is restarted
func (m *NativeWireGuardManager) SetTunnelRestartCallback(callback func(configID int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onTunnelRestart = callback
}

// StartHealthCheck starts a background goroutine that monitors tunnel health
func (m *NativeWireGuardManager) StartHealthCheck() {
	m.mu.Lock()
	if m.healthCheckStop != nil {
		m.mu.Unlock()
		return // Already running
	}
	m.healthCheckStop = make(chan struct{})
	m.mu.Unlock()

	m.healthCheckWg.Add(1)
	go m.healthCheckLoop()
	m.log("Health check started")
}

// StopHealthCheck stops the health check goroutine
func (m *NativeWireGuardManager) StopHealthCheck() {
	m.mu.Lock()
	if m.healthCheckStop == nil {
		m.mu.Unlock()
		return // Not running
	}
	close(m.healthCheckStop)
	m.healthCheckStop = nil
	m.mu.Unlock()

	m.healthCheckWg.Wait()
	m.log("Health check stopped")
}

// healthCheckLoop periodically checks tunnel health
func (m *NativeWireGuardManager) healthCheckLoop() {
	defer m.healthCheckWg.Done()

	ticker := time.NewTicker(HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.healthCheckStop:
			return
		case <-ticker.C:
			m.checkAllTunnels()
		}
	}
}

// checkAllTunnels checks health of all active tunnels
func (m *NativeWireGuardManager) checkAllTunnels() {
	m.mu.RLock()
	tunnelsToCheck := make([]*TunnelState, 0)
	for _, state := range m.tunnels {
		if state.Active {
			tunnelsToCheck = append(tunnelsToCheck, state)
		}
	}
	m.mu.RUnlock()

	for _, state := range tunnelsToCheck {
		healthy, lastHandshake := m.checkTunnelHealth(state.ConfigID)

		m.mu.Lock()
		if tunnelState, exists := m.tunnels[state.Name]; exists {
			tunnelState.LastHandshake = lastHandshake
			oldHealthy := tunnelState.Healthy
			tunnelState.Healthy = healthy

			if !healthy && oldHealthy {
				m.log(fmt.Sprintf("Tunnel %s became unhealthy (last handshake: %v)",
					state.Name, lastHandshake))
			}

			// Attempt restart if unhealthy and under max attempts
			if !healthy && tunnelState.RestartCount < MaxRestartAttempts && tunnelState.Config != nil {
				tunnelState.RestartCount++
				m.mu.Unlock()

				m.log(fmt.Sprintf("Attempting to restart tunnel %s (attempt %d/%d)",
					state.Name, tunnelState.RestartCount, MaxRestartAttempts))

				if err := m.restartTunnel(state.ConfigID, tunnelState.Config); err != nil {
					m.log(fmt.Sprintf("Failed to restart tunnel %s: %v", state.Name, err))
				} else {
					m.log(fmt.Sprintf("Tunnel %s restarted successfully", state.Name))
					if m.onTunnelRestart != nil {
						m.onTunnelRestart(state.ConfigID)
					}
				}
				continue
			}
		}
		m.mu.Unlock()
	}
}

// checkTunnelHealth checks if a tunnel is healthy based on handshake time
func (m *NativeWireGuardManager) checkTunnelHealth(configID int) (bool, time.Time) {
	stats, err := m.GetTunnelStats(configID)
	if err != nil {
		return false, time.Time{}
	}

	// Parse last handshake time
	handshakeStr, _ := stats["last_handshake"].(string)
	if handshakeStr == "" || handshakeStr == "never" {
		return false, time.Time{}
	}

	// Parse relative time like "1 minute, 30 seconds ago"
	lastHandshake := m.parseHandshakeTime(handshakeStr)
	if lastHandshake.IsZero() {
		return false, time.Time{}
	}

	// Check if handshake is within timeout
	healthy := time.Since(lastHandshake) < HandshakeTimeout
	return healthy, lastHandshake
}

// parseHandshakeTime parses the handshake time string from wg show output
func (m *NativeWireGuardManager) parseHandshakeTime(s string) time.Time {
	s = strings.TrimSpace(s)

	// Handle "never"
	if s == "never" || s == "" {
		return time.Time{}
	}

	// Try to parse relative time like "1 minute, 30 seconds ago"
	s = strings.TrimSuffix(s, " ago")

	var duration time.Duration
	parts := strings.Split(s, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)

		// Try to parse each part
		if strings.Contains(part, "second") {
			var n int
			fmt.Sscanf(part, "%d", &n)
			duration += time.Duration(n) * time.Second
		} else if strings.Contains(part, "minute") {
			var n int
			fmt.Sscanf(part, "%d", &n)
			duration += time.Duration(n) * time.Minute
		} else if strings.Contains(part, "hour") {
			var n int
			fmt.Sscanf(part, "%d", &n)
			duration += time.Duration(n) * time.Hour
		} else if strings.Contains(part, "day") {
			var n int
			fmt.Sscanf(part, "%d", &n)
			duration += time.Duration(n) * 24 * time.Hour
		}
	}

	if duration == 0 {
		return time.Time{}
	}

	return time.Now().Add(-duration)
}

// restartTunnel stops and restarts a tunnel
func (m *NativeWireGuardManager) restartTunnel(configID int, config *WireGuardConfig) error {
	// Stop the tunnel first
	if err := m.StopTunnel(configID); err != nil {
		m.log(fmt.Sprintf("Warning: error stopping tunnel during restart: %v", err))
	}

	// Wait a bit for cleanup
	time.Sleep(2 * time.Second)

	// Start the tunnel again
	return m.StartTunnel(configID, config)
}

// GetTunnelHealthStatus returns health status for all tunnels
func (m *NativeWireGuardManager) GetTunnelHealthStatus() []map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []map[string]interface{}
	for _, state := range m.tunnels {
		if state.Active {
			status := map[string]interface{}{
				"name":           state.Name,
				"config_id":      state.ConfigID,
				"healthy":        state.Healthy,
				"last_handshake": state.LastHandshake.Format(time.RFC3339),
				"restart_count":  state.RestartCount,
				"uptime":         time.Since(state.StartedAt).String(),
			}
			result = append(result, status)
		}
	}
	return result
}

// ResetRestartCount resets the restart counter for a tunnel (called after successful reconnect)
func (m *NativeWireGuardManager) ResetRestartCount(configID int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	name := fmt.Sprintf("%s%d", TunnelPrefix, configID)
	if state, exists := m.tunnels[name]; exists {
		state.RestartCount = 0
		state.Healthy = true
	}
}
