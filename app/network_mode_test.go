package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveNetworkModeAutoFallsBackToCompatTun(t *testing.T) {
	app := &App{basePath: t.TempDir()}

	status := app.resolveNetworkMode(NetworkModeAuto)

	if status.Requested != NetworkModeWindowsUnified {
		t.Fatalf("requested = %q, want %q", status.Requested, NetworkModeWindowsUnified)
	}
	if status.Active != NetworkModeWindowsUnified {
		t.Fatalf("active = %q, want %q", status.Active, NetworkModeWindowsUnified)
	}
	if !status.Fallback {
		t.Fatal("auto mode must report fallback while Deep Windows engine is not shipped")
	}
	if status.FallbackReason == "" {
		t.Fatal("fallback reason must be visible for logs and UI")
	}
	if status.Label != networkModeLabel(NetworkModeWindowsUnified) {
		t.Fatalf("label = %q, want %q", status.Label, networkModeLabel(NetworkModeWindowsUnified))
	}
}

func TestResolveNetworkModeExplicitCompatDoesNotWarn(t *testing.T) {
	app := &App{basePath: t.TempDir()}

	status := app.resolveNetworkMode(NetworkModeCompatTun)

	if status.Requested != NetworkModeWindowsUnified || status.Active != NetworkModeWindowsUnified {
		t.Fatalf("status = %+v, want legacy compat migrated to unified", status)
	}
	if !status.Fallback {
		t.Fatalf("missing native packet runtime must be visible in unified mode: %+v", status)
	}
}

func TestResolveNetworkModeExplicitCompatIsFallbackOnlyWhenDeepWindowsReady(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app := newDeepWindowsReadyApp(t)
	status := app.resolveNetworkMode(NetworkModeCompatTun)

	if status.Requested != NetworkModeWindowsUnified {
		t.Fatalf("requested = %q, want legacy request migrated", status.Requested)
	}
	if status.Active != NetworkModeWindowsUnified {
		t.Fatalf("active = %q, want Windows Unified", status.Active)
	}
	if status.Fallback {
		t.Fatalf("fallback = true, want false while primary engine is available: %+v", status)
	}
	if status.FallbackReason != "" {
		t.Fatalf("ready unified runtime must not report fallback reason: %q", status.FallbackReason)
	}
}

func TestResolveNetworkModeUsesDeepWindowsWhenWinDivertIsBundled(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app := newDeepWindowsReadyApp(t)
	status := app.resolveNetworkMode(NetworkModeAuto)

	if status.Active != NetworkModeWindowsUnified {
		t.Fatalf("active = %q, want %q with bundled WinDivert", status.Active, NetworkModeWindowsUnified)
	}
	if status.Fallback {
		t.Fatalf("fallback = true, want false with bundled WinDivert: %+v", status)
	}
	if !status.DriverReady {
		t.Fatal("driverReady must be true with bundled WinDivert")
	}
}

func TestResolveNetworkModeRequiresBothWinDivertFiles(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	for _, name := range []string{"WinDivert.dll"} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("test"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	app := &App{basePath: basePath}
	status := app.resolveNetworkMode(NetworkModeAuto)
	if status.Active != NetworkModeWindowsUnified {
		t.Fatalf("active = %q, want unified mode even when payload diagnostics fail", status.Active)
	}
	if !status.Fallback || status.DriverReady {
		t.Fatalf("status = %+v, want fallback and driverReady=false without the driver", status)
	}
	if !strings.Contains(status.FallbackReason, "WinDivert64.sys") {
		t.Fatalf("fallback reason = %q, want missing driver file", status.FallbackReason)
	}
}

func TestNormalizeNetworkMode(t *testing.T) {
	if got := NormalizeNetworkMode(NetworkMode("bad")); got != DefaultNetworkMode {
		t.Fatalf("invalid mode normalized to %q, want %q", got, DefaultNetworkMode)
	}
	if got := NormalizeNetworkMode(NetworkModeDeepWindows); got != NetworkModeWindowsUnified {
		t.Fatalf("deep mode normalized to %q", got)
	}
}

func TestDeepWindowsTransparentOnlySkipsTunWithoutSubscription(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "selector", "tag": "proxy", "outbounds": []interface{}{"direct"}},
		},
	})

	ok, reason := app.shouldUseDeepWindowsPrimary(configPath, NetworkModeStatus{Active: NetworkModeDeepWindows})
	if ok || !strings.Contains(reason, "Windows Unified") {
		t.Fatalf("legacy transparent-only branch must stay disabled; ok=%v reason=%s", ok, reason)
	}
}

func TestDeepWindowsFallsBackToTunWithSubscriptionProxyRoutes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "example.com", "server_port": 443, "uuid": "00000000-0000-0000-0000-000000000000"},
			map[string]interface{}{"type": "urltest", "tag": "auto-select", "outbounds": []interface{}{"vless-fast"}},
		},
	})

	ok, reason := app.shouldUseDeepWindowsPrimary(configPath, NetworkModeStatus{Active: NetworkModeDeepWindows})
	if ok {
		t.Fatalf("Deep Windows primary = true, want TUN fallback for subscription proxy routes; reason=%s", reason)
	}
	if !strings.Contains(reason, "Windows Unified") || !strings.Contains(reason, "sing-box TUN") {
		t.Fatalf("reason = %q, want unified TUN explanation", reason)
	}
}

func TestDeepWindowsFallsBackToTunForAdvancedRoutingSettings(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	cases := []struct {
		name   string
		mutate func(*GlobalAppSettings)
	}{
		{
			name: "all traffic",
			mutate: func(settings *GlobalAppSettings) {
				settings.RoutingMode = RoutingModeAllTraffic
			},
		},
		{
			name: "except Russia",
			mutate: func(settings *GlobalAppSettings) {
				settings.RoutingMode = RoutingModeExceptRussia
			},
		},
		{
			name: "hide RU traffic",
			mutate: func(settings *GlobalAppSettings) {
				settings.HideRuTraffic = true
				settings.RuProxyAddress = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls#ru"
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
				"inbounds": []interface{}{
					map[string]interface{}{"type": "tun", "tag": "tun-in", "auto_route": true},
					map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2088, "set_system_proxy": false},
				},
				"outbounds": []interface{}{
					map[string]interface{}{"type": "direct", "tag": "direct"},
					map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "example.com", "server_port": 443, "uuid": "00000000-0000-0000-0000-000000000000"},
					map[string]interface{}{"type": "urltest", "tag": "auto-select", "outbounds": []interface{}{"vless-fast"}},
				},
			})
			settings := app.storage.GetAppSettings()
			tc.mutate(&settings)
			if err := app.storage.UpdateAppSettings(settings); err != nil {
				t.Fatalf("update settings failed: %v", err)
			}

			ok, reason := app.shouldUseDeepWindowsPrimary(configPath, NetworkModeStatus{Active: NetworkModeDeepWindows})
			if ok {
				t.Fatalf("Deep Windows primary = true for %s, want TUN fallback; reason=%s", tc.name, reason)
			}
			if !strings.Contains(reason, "Windows Unified") || !strings.Contains(reason, "sing-box TUN") {
				t.Fatalf("reason = %q, want unified TUN explanation", reason)
			}
			plan := app.buildDeepWindowsRoutePlan(configPath)
			if !plan.RequiresSingBoxProxy {
				t.Fatalf("plan for %s should require local proxy endpoint, got %+v", tc.name, plan)
			}
		})
	}
}

func TestDeepWindowsProxyFallbackConfigRemovesTunAndEnablesSystemProxy(t *testing.T) {
	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "tun", "tag": "tun-in", "auto_route": true},
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2088, "set_system_proxy": false},
		},
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "urltest", "tag": "auto-select", "outbounds": []interface{}{"vless-fast"}},
		},
	})

	proxyConfigPath, err := app.writeDeepWindowsProxyFallbackConfig(configPath)
	if err != nil {
		t.Fatalf("write proxy fallback config failed: %v", err)
	}
	config, err := readJSONConfig(proxyConfigPath)
	if err != nil {
		t.Fatalf("read proxy fallback config failed: %v", err)
	}
	inbounds, _ := config["inbounds"].([]interface{})
	if len(inbounds) != 1 {
		t.Fatalf("proxy fallback inbounds = %d, want only mixed inbound", len(inbounds))
	}
	mixed, _ := inbounds[0].(map[string]interface{})
	if mixed["type"] != "mixed" {
		t.Fatalf("proxy fallback inbound type = %v, want mixed", mixed["type"])
	}
	if mixed["set_system_proxy"] != true {
		t.Fatalf("proxy fallback mixed set_system_proxy = %v, want true", mixed["set_system_proxy"])
	}
	if mixed["listen"] != "127.0.0.1" {
		t.Fatalf("proxy fallback mixed listen = %v, want 127.0.0.1", mixed["listen"])
	}
	if port := mixedInboundPort(mixed["listen_port"]); port != defaultDropoMixedProxyPort {
		t.Fatalf("proxy fallback mixed listen_port = %v, want %d", mixed["listen_port"], defaultDropoMixedProxyPort)
	}
}

func TestDeepWindowsProxyFallbackPrunesDeadVPNCandidatesFromRouteProbe(t *testing.T) {
	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "tun", "tag": "tun-in", "auto_route": true},
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2088},
		},
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": "vless-live", "server": "127.0.0.1", "server_port": 19081},
			map[string]interface{}{"type": "socks", "tag": "vless-dead", "server": "127.0.0.1", "server_port": 19082},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-live", "vless-dead"},
				"url":       resilientGroupTestURL,
				"interval":  "5m",
			},
			map[string]interface{}{
				"type":      "selector",
				"tag":       "proxy",
				"outbounds": []interface{}{"auto-select", "vless-live", "vless-dead", "direct"},
				"default":   "auto-select",
			},
		},
	})
	app.rememberRouteProbeResults([]routeProbeServiceResult{
		{Tag: "openai", Name: "AI services", Success: true, MethodKind: "vpn", MethodTag: "vless-live", MethodLabel: "live", LatencyMS: 100},
	})

	proxyConfigPath, err := app.writeDeepWindowsProxyFallbackConfig(configPath)
	if err != nil {
		t.Fatalf("write proxy fallback config failed: %v", err)
	}
	config, err := readJSONConfig(proxyConfigPath)
	if err != nil {
		t.Fatalf("read proxy fallback config failed: %v", err)
	}
	auto := getOutbound(config, "auto-select")
	if auto == nil {
		t.Fatal("auto-select outbound missing")
	}
	if got := interfaceStringSlice(auto["outbounds"]); len(got) != 1 || got[0] != "vless-live" {
		t.Fatalf("auto-select candidates = %v, want only live candidate", got)
	}
	if auto["type"] != "selector" || auto["default"] != "vless-live" {
		t.Fatalf("auto-select after pruning = %#v, want selector pinned to live candidate", auto)
	}
	proxy := getOutbound(config, "proxy")
	if got := interfaceStringSlice(proxy["outbounds"]); !sameStringSet(got, []string{"auto-select", "vless-live", "direct"}) {
		t.Fatalf("proxy selector candidates = %v, want dead candidate removed", got)
	}
}

func TestDeepWindowsStartFallsBackToTunWhenProxyRedirectorRequired(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2088},
		},
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example", "server_port": 443, "uuid": "00000000-0000-0000-0000-000000000000"},
			map[string]interface{}{
				"type":      "selector",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-fast"},
				"default":   "vless-fast",
			},
			map[string]interface{}{
				"type":      "selector",
				"tag":       ServiceBypassGroupTag("openai"),
				"outbounds": []interface{}{"auto-select"},
				"default":   "auto-select",
			},
			map[string]interface{}{"type": "selector", "tag": discordVPNGroupTag, "outbounds": []interface{}{"vless-fast"}, "default": "vless-fast"},
			map[string]interface{}{"type": "selector", "tag": discordRealtimeGroupTag, "outbounds": []interface{}{"direct", discordVPNGroupTag}, "default": "direct"},
		},
		"route": map[string]interface{}{"rules": []interface{}{
			map[string]interface{}{"process_name": []interface{}{"Discord.exe"}, "network": "udp", "outbound": discordRealtimeGroupTag},
			map[string]interface{}{"domain_suffix": []interface{}{"discord.media"}, "network": "tcp", "outbound": discordRealtimeGroupTag},
		}},
	}
	app, _ := newDeepWindowsTestApp(t, config)
	app.configBuilder = NewConfigBuilderForStorage(app.storage)
	app.initialized = true
	app.initializedReady.Store(true)
	app.logPath = filepath.Join(app.basePath, "logs", "app.log")
	app.tempLogPath = filepath.Join(app.basePath, "logs", "temp.log")
	if err := os.MkdirAll(filepath.Dir(app.logPath), 0755); err != nil {
		t.Fatalf("create log dir failed: %v", err)
	}
	settings := app.storage.GetAppSettings()
	settings.DisableFreeAccess = true
	settings.NetworkMode = NetworkModeDeepWindows
	if err := app.storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update app settings failed: %v", err)
	}
	if err := app.storage.UpdateProfileConfig(app.storage.GetActiveProfileID(), config); err != nil {
		t.Fatalf("update profile config failed: %v", err)
	}
	defer app.closeLogFile()

	result := app.Start()

	if result["success"] == true {
		t.Fatalf("Start() success = true, want failure when TUN fallback cannot start: %#v", result)
	}
	errText, _ := result["error"].(string)
	if !strings.Contains(errText, "sing-box") || !strings.Contains(errText, "Windows Unified") {
		t.Fatalf("Start() error = %q, want Windows Unified/sing-box error", errText)
	}
	if !app.hasError.Load() {
		t.Fatal("app.hasError = false, want true after failed TUN fallback startup")
	}
}

func newDeepWindowsTestApp(t *testing.T, config map[string]interface{}) (*App, string) {
	t.Helper()

	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	for _, name := range []string{"WinDivert.dll", "WinDivert64.sys"} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("test"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}
	resourcesPath := filepath.Join(basePath, ResourcesFolder)
	if err := os.MkdirAll(resourcesPath, 0755); err != nil {
		t.Fatalf("create resources dir failed: %v", err)
	}
	configPath := filepath.Join(resourcesPath, "active_config.json")
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	return &App{
		basePath:      basePath,
		storage:       storage,
		trafficEngine: NewNativeTrafficManager(basePath, nil),
	}, configPath
}

func newDeepWindowsReadyApp(t *testing.T) *App {
	t.Helper()

	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	for _, name := range append([]string{
		ZapretProcessName,
		"WinDivert.dll",
		"WinDivert64.sys",
		"cygwin1.dll",
		"quic_initial_www_google_com.bin",
		"quic_initial_dbankcloud_ru.bin",
		"tls_clienthello_www_google_com.bin",
		"discord-ip-discovery-without-port.bin",
		"stun.bin",
		"windivert_part.discord_media.txt",
		"windivert_part.stun.txt",
	}, zapret2RequiredFiles...) {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("test"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}
	return &App{basePath: basePath}
}
