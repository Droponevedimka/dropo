package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// TgWsProxyProcessName is the bundled tg-ws-proxy executable. It runs a
// local MTProto proxy (default 127.0.0.1:1443) that tunnels Telegram's MTProto
// inside WebSocket/TLS to the datacenters — bypassing the IP/protocol block that
// pure desync (winws) cannot. It is auto-started as a background sidecar; the
// Telegram app connects to it via the tg://proxy link (one in-Telegram confirm,
// which is Telegram's own security and cannot be bypassed from outside).
const (
	TgWsProxyProcessName  = "tg-ws-proxy.exe"
	TgWsProxyDefaultPort  = 1443
	tgProxyAutoConnectEnv = "DROPO_TG_PROXY_AUTOCONNECT"
	TelegramProcessName   = "Telegram.exe"
)

// TgWsProxyManager launches and supervises the tg-ws-proxy sidecar.
type TgWsProxyManager struct {
	basePath string
	dataPath string
	exePath  string
	logger   func(string)

	mu  sync.Mutex
	cmd *exec.Cmd
}

func NewTgWsProxyManager(basePath string, logger func(string)) *TgWsProxyManager {
	return NewTgWsProxyManagerWithData(basePath, basePath, logger)
}

func NewTgWsProxyManagerWithData(basePath, dataPath string, logger func(string)) *TgWsProxyManager {
	return &TgWsProxyManager{
		basePath: basePath,
		dataPath: dataPath,
		exePath:  filepath.Join(basePath, "bin", TgWsProxyProcessName),
		logger:   logger,
	}
}

func (m *TgWsProxyManager) log(msg string) {
	if m.logger != nil {
		m.logger(fmt.Sprintf("[TgWsProxy] %s", msg))
	}
}

func (m *TgWsProxyManager) IsInstalled() bool {
	return m != nil && fileExists(m.exePath)
}

func (m *TgWsProxyManager) IsRunning() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil && m.cmd.Process != nil
}

// Start launches the proxy as a managed background process (idempotent).
func (m *TgWsProxyManager) Start() error {
	if !m.IsInstalled() {
		return fmt.Errorf("%s not found: %s", TgWsProxyProcessName, m.exePath)
	}
	cfg, err := m.ensureConfig()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != nil && m.cmd.Process != nil {
		return nil
	}

	cmd := exec.Command(
		m.exePath,
		"--host", cfg.Host,
		"--port", strconv.Itoa(cfg.Port),
		"--secret", cfg.Secret,
	)
	cmd.Dir = filepath.Dir(m.exePath)
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start tg-ws-proxy: %w", err)
	}
	attachManagedCmdToJob(cmd, "tg-ws-proxy", m.log)
	m.cmd = cmd
	m.log(fmt.Sprintf("MTProto proxy started on %s:%d (pid=%d)", cfg.Host, cfg.Port, cmd.Process.Pid))
	go m.waitExit(cmd)
	return nil
}

func (m *TgWsProxyManager) waitExit(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	if m.cmd == cmd {
		m.cmd = nil
	}
	m.mu.Unlock()
	if err != nil {
		m.log(fmt.Sprintf("exited: %v", err))
		return
	}
	m.log("exited")
}

func (m *TgWsProxyManager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	m.mu.Unlock()
	if cmd != nil {
		terminateProcessTree(cmd)
		m.log("MTProto proxy stopped")
	}
}

type tgWsProxyConfig struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Secret string `json:"secret"`
}

func (m *TgWsProxyManager) configPath() string {
	return filepath.Join(m.dataPath, "resources", "tg-ws-proxy.json")
}

// configCandidates lists where tg-ws-proxy may persist host/port/secret. Dropo
// owns the first path so headless tg-ws-proxy starts with a stable secret; the
// other paths keep compatibility with earlier tray-based builds.
func (m *TgWsProxyManager) configCandidates() []string {
	paths := []string{
		m.configPath(),
		filepath.Join(filepath.Dir(m.exePath), "config.json"),
		filepath.Join(m.basePath, "config.json"),
	}
	for _, env := range []string{"APPDATA", "LOCALAPPDATA"} {
		dir := os.Getenv(env)
		if dir == "" {
			continue
		}
		for _, name := range []string{"TgWsProxy", "tg-ws-proxy", "tgwsproxy"} {
			paths = append(paths, filepath.Join(dir, name, "config.json"))
		}
	}
	return paths
}

func (m *TgWsProxyManager) readConfig() (tgWsProxyConfig, bool) {
	for _, path := range m.configCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var cfg tgWsProxyConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.Secret != "" {
			return cfg, true
		}
	}
	return tgWsProxyConfig{}, false
}

func (m *TgWsProxyManager) ensureConfig() (tgWsProxyConfig, error) {
	cfg, ok := m.readConfig()
	if !ok {
		cfg = tgWsProxyConfig{}
	}
	cfg = normalizeTgWsProxyConfig(cfg)
	if !isValidTgWsProxySecret(cfg.Secret) {
		secret, err := generateTgWsProxySecret()
		if err != nil {
			return tgWsProxyConfig{}, err
		}
		cfg.Secret = secret
	}

	path := m.configPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return tgWsProxyConfig{}, fmt.Errorf("failed to create tg-ws-proxy config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return tgWsProxyConfig{}, fmt.Errorf("failed to encode tg-ws-proxy config: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return tgWsProxyConfig{}, fmt.Errorf("failed to write tg-ws-proxy config: %w", err)
	}
	return cfg, nil
}

func normalizeTgWsProxyConfig(cfg tgWsProxyConfig) tgWsProxyConfig {
	if cfg.Host == "" {
		cfg.Host = "127.0.0.1"
	}
	if cfg.Port == 0 {
		cfg.Port = TgWsProxyDefaultPort
	}
	return cfg
}

func isValidTgWsProxySecret(secret string) bool {
	if len(secret) != 32 {
		return false
	}
	_, err := hex.DecodeString(secret)
	return err == nil
}

func generateTgWsProxySecret() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("failed to generate tg-ws-proxy secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// TelegramProxyLink returns the tg://proxy deep link Telegram uses to connect to
// the local proxy, once the secret is available.
func (m *TgWsProxyManager) TelegramProxyLink() (string, bool) {
	cfg, err := m.ensureConfig()
	if err != nil {
		m.log(err.Error())
		return "", false
	}
	// The "dd" prefix marks an MTProto random-padding secret (per tg-ws-proxy).
	return fmt.Sprintf("tg://proxy?server=%s&port=%d&secret=dd%s", cfg.Host, cfg.Port, cfg.Secret), true
}

// AutoConnectTelegram opens the tg://proxy link so Telegram can show its native
// confirmation dialog. Dropo does not edit Telegram Desktop settings directly:
// the user still decides in Telegram whether to add/enable the local proxy.
func (m *TgWsProxyManager) AutoConnectTelegram() {
	if m == nil {
		return
	}
	if os.Getenv(tgProxyAutoConnectEnv) == "0" {
		m.log("Telegram auto-connect skipped by environment setting")
		return
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if link, ok := m.TelegramProxyLink(); ok {
			if err := openExternalURL(link); err != nil {
				m.log(fmt.Sprintf("could not open Telegram proxy link: %v", err))
				return
			}
			m.log("opened tg://proxy link; confirm the proxy inside Telegram to connect")
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	m.log("Telegram proxy secret not found yet; open it from the tg-ws-proxy tray icon")
}

var openExternalURL = openExternalURLWithDefaultHandler

// openExternalURLWithDefaultHandler opens a URL/deep-link with the OS default handler.
func openExternalURLWithDefaultHandler(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	configureBackgroundCommand(cmd)
	return cmd.Start()
}
