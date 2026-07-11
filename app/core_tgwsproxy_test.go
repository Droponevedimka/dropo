package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func telegramTestSubscriptionConfig() map[string]interface{} {
	return map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example", "server_port": 443},
			map[string]interface{}{
				"type":      "selector",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-fast"},
				"default":   "vless-fast",
			},
		},
	}
}

// In the default (auto) policy the MTProto sidecar stays primary even when a
// subscription is present — the VPN is only the backstop route, so adding a
// subscription must NOT silently break Telegram by killing the sidecar.
func TestTelegramProxyStaysPrimaryWithSubscriptionInAutoMode(t *testing.T) {
	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binPath, TgWsProxyProcessName), []byte("not an executable"), 0644); err != nil {
		t.Fatalf("write fake tg-ws-proxy failed: %v", err)
	}

	app := &App{
		logBuffer: make([]string, 0, MaxLogBufferSize),
		basePath:  basePath,
		tgwsproxy: NewTgWsProxyManager(basePath, nil),
	}

	app.startTelegramProxyIfNeeded(telegramTestSubscriptionConfig())

	if containsLogSubstring(app.logBuffer, "routing via VPN subscription") {
		t.Fatalf("auto mode must keep the sidecar primary, not move Telegram to VPN; logs = %v", app.logBuffer)
	}
	// The fake binary cannot actually exec, so we assert the attempt/failure path
	// was taken (sidecar was NOT skipped because of the subscription).
	if !containsLogSubstring(app.logBuffer, "Telegram MTProto proxy failed to start") &&
		!containsLogSubstring(app.logBuffer, "MTProto sidecar active") {
		t.Fatalf("auto mode must attempt to start the sidecar even with a subscription; logs = %v", app.logBuffer)
	}
}

// In VPN mode the sidecar must STILL be started (so Telegram's saved local proxy
// has a live endpoint — even on a fresh portable extract where injected=false);
// its egress is routed through the VPN by the config, not by stopping it.
func TestTelegramProxyKeptAliveInVPNModeEvenWhenNotInjected(t *testing.T) {
	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binPath, TgWsProxyProcessName), []byte("not an executable"), 0644); err != nil {
		t.Fatalf("write fake tg-ws-proxy failed: %v", err)
	}

	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("init storage failed: %v", err)
	}
	settings := storage.GetAppSettings()
	settings.FreeAccessMethods = map[string]string{"telegram": FreeAccessMethodVPN}
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}

	app := &App{
		logBuffer: make([]string, 0, MaxLogBufferSize),
		basePath:  basePath,
		storage:   storage,
		tgwsproxy: NewTgWsProxyManager(basePath, nil),
	}

	app.startTelegramProxyIfNeeded(telegramTestSubscriptionConfig())

	// Must NOT skip the sidecar; it must be attempted (fails here only because the
	// fake binary cannot exec).
	if containsLogSubstring(app.logBuffer, "no local proxy injected") {
		t.Fatalf("vpn mode must keep the sidecar alive, not skip it; logs = %v", app.logBuffer)
	}
	if !containsLogSubstring(app.logBuffer, "Telegram MTProto proxy failed to start") &&
		!containsLogSubstring(app.logBuffer, "egress routed through the VPN") {
		t.Fatalf("vpn mode must attempt to start the sidecar; logs = %v", app.logBuffer)
	}
}

// When Telegram is set to VPN but a proxy was already injected, the sidecar must
// be KEPT (so the existing local proxy keeps working, egress routed to VPN) and
// the tg://proxy link must NOT be re-opened.
func TestTelegramProxyKeptAliveInVPNModeWhenInjected(t *testing.T) {
	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(binPath, TgWsProxyProcessName), []byte("not an executable"), 0644); err != nil {
		t.Fatalf("write fake tg-ws-proxy failed: %v", err)
	}

	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("init storage failed: %v", err)
	}
	settings := storage.GetAppSettings()
	settings.FreeAccessMethods = map[string]string{"telegram": FreeAccessMethodVPN}
	settings.TelegramProxyInjected = true
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}

	app := &App{
		logBuffer: make([]string, 0, MaxLogBufferSize),
		basePath:  basePath,
		storage:   storage,
		tgwsproxy: NewTgWsProxyManager(basePath, nil),
	}

	app.startTelegramProxyIfNeeded(telegramTestSubscriptionConfig())

	// Must NOT take the clean no-injection skip; it must proceed to start (which
	// fails here only because the fake binary cannot exec).
	if containsLogSubstring(app.logBuffer, "no local proxy injected") {
		t.Fatalf("vpn+injected must keep the sidecar, not skip it; logs = %v", app.logBuffer)
	}
	if !containsLogSubstring(app.logBuffer, "Telegram MTProto proxy failed to start") &&
		!containsLogSubstring(app.logBuffer, "egress is routed through the VPN") {
		t.Fatalf("vpn+injected must attempt to keep the sidecar alive; logs = %v", app.logBuffer)
	}
}

func TestPrepareQuitShowsTelegramNoticeWhenInjectedSidecarRanThisSession(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("init storage failed: %v", err)
	}
	settings := storage.GetAppSettings()
	settings.TelegramProxyInjected = true
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update settings failed: %v", err)
	}

	app := &App{
		logBuffer: make([]string, 0, MaxLogBufferSize),
		basePath:  basePath,
		storage:   storage,
		tgwsproxy: NewTgWsProxyManager(basePath, nil),
	}
	app.tgProxyStartedSession.Store(true)

	status := app.PrepareQuit()

	if !status.Injected {
		t.Fatal("expected Telegram proxy injected flag to be reported")
	}
	if !status.ShowNotice {
		t.Fatalf("expected exit cleanup notice when injected proxy exists and sidecar ran this session; status = %+v", status)
	}
	if !status.RecommendRemove {
		t.Fatalf("expected Telegram cleanup recommendation; status = %+v", status)
	}
}

func TestResolveTelegramTransport(t *testing.T) {
	if serviceBlockType("telegram") != "proxy" {
		t.Fatalf("precondition: telegram must be proxy-handled, got %q", serviceBlockType("telegram"))
	}

	base := GlobalAppSettings{FreeAccessEnabled: true}

	cases := []struct {
		name    string
		method  string
		hasVPN  bool
		disable bool
		want    string
	}{
		{"auto-no-vpn", FreeAccessMethodAuto, false, false, telegramTransportFree},
		{"auto-with-vpn", FreeAccessMethodAuto, true, false, telegramTransportFree},
		{"vpn-method-with-vpn", FreeAccessMethodVPN, true, false, telegramTransportVPN},
		{"vpn-method-no-vpn", FreeAccessMethodVPN, false, false, telegramTransportNone},
		{"free-disabled-with-vpn", FreeAccessMethodAuto, true, true, telegramTransportVPN},
		{"free-disabled-no-vpn", FreeAccessMethodAuto, false, true, telegramTransportNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := base
			settings.DisableFreeAccess = tc.disable
			settings.FreeAccessMethods = map[string]string{"telegram": tc.method}
			if got := resolveTelegramTransport(settings, tc.hasVPN); got != tc.want {
				t.Fatalf("resolveTelegramTransport(method=%s,vpn=%v,disable=%v) = %q, want %q", tc.method, tc.hasVPN, tc.disable, got, tc.want)
			}
		})
	}
}

func TestTelegramProxyAutoConnectOpensProxyLinkByDefault(t *testing.T) {
	t.Setenv(tgProxyAutoConnectEnv, "")

	logs := []string{}
	manager := NewTgWsProxyManager(t.TempDir(), func(message string) { logs = append(logs, message) })

	opened := []string{}
	previousOpenExternalURL := openExternalURL
	openExternalURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	defer func() { openExternalURL = previousOpenExternalURL }()

	manager.AutoConnectTelegram()

	if len(opened) != 1 {
		t.Fatalf("opened links = %v, want exactly one tg://proxy link", opened)
	}
	if !strings.HasPrefix(opened[0], "tg://proxy?server=127.0.0.1&port=1443&secret=dd") {
		t.Fatalf("opened link = %q, want local Telegram proxy link", opened[0])
	}
	if !containsLogSubstring(logs, "opened tg://proxy link") {
		t.Fatalf("logs = %v, want opened-link message", logs)
	}
}

func TestTelegramProxyAutoConnectCanBeDisabledByEnv(t *testing.T) {
	t.Setenv(tgProxyAutoConnectEnv, "0")

	logs := []string{}
	manager := NewTgWsProxyManager(t.TempDir(), func(message string) { logs = append(logs, message) })

	previousOpenExternalURL := openExternalURL
	openExternalURL = func(url string) error {
		return fmt.Errorf("openExternalURL must not be called when disabled")
	}
	defer func() { openExternalURL = previousOpenExternalURL }()

	manager.AutoConnectTelegram()

	if !containsLogSubstring(logs, "auto-connect skipped by environment") {
		t.Fatalf("logs = %v, want auto-connect skipped message", logs)
	}
}

func TestTelegramProxyLinkUsesStableDropoConfig(t *testing.T) {
	basePath := t.TempDir()
	manager := NewTgWsProxyManager(basePath, nil)

	firstLink, ok := manager.TelegramProxyLink()
	if !ok {
		t.Fatal("TelegramProxyLink must create a stable local proxy config")
	}
	secondLink, ok := manager.TelegramProxyLink()
	if !ok {
		t.Fatal("TelegramProxyLink must read the stable local proxy config")
	}
	if firstLink != secondLink {
		t.Fatalf("Telegram proxy link changed between calls: %q != %q", firstLink, secondLink)
	}

	cfg, ok := manager.readConfig()
	if !ok {
		t.Fatal("stored tg-ws-proxy config was not readable")
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != TgWsProxyDefaultPort || !isValidTgWsProxySecret(cfg.Secret) {
		t.Fatalf("unexpected stored tg-ws-proxy config: %+v", cfg)
	}
}

func containsLogSubstring(logs []string, needle string) bool {
	for _, log := range logs {
		if strings.Contains(log, needle) {
			return true
		}
	}
	return false
}
