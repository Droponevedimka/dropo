package main

import (
	"runtime"
	"testing"
)

func TestDefaultStorageSettingsMatchCurrentNetworkPolicy(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)

	settings := app.storage.GetAppSettings()
	if settings.RoutingMode != RoutingModeBlockedOnly {
		t.Fatalf("default routing mode = %q, want blocked_only", settings.RoutingMode)
	}
	if settings.NetworkMode != NetworkModeAuto {
		t.Fatalf("default network mode = %q, want auto", settings.NetworkMode)
	}
	if settings.DisableFreeAccess {
		t.Fatal("free methods must be allowed by default")
	}
	if !settings.FreeAccessEnabled {
		t.Fatal("free access state should remain enabled for legacy UI/API compatibility")
	}
	if !settings.Notifications {
		t.Fatal("notifications should be enabled by default")
	}
	if !settings.AutoUpdateSub {
		t.Fatal("subscription auto-update should be enabled by default")
	}
	if !settings.EnableLogging || settings.LogLevel != LogLevelInfo {
		t.Fatalf("logging defaults = enabled:%t level:%q, want enabled info", settings.EnableLogging, settings.LogLevel)
	}
	profile, err := app.storage.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile failed: %v", err)
	}
	if profile.Name != DefaultProfileName {
		t.Fatalf("default profile name = %q, want %q", profile.Name, DefaultProfileName)
	}
}

func TestSettingsAPIsPersistRoutingAndNetworkMode(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)

	result := app.SetRoutingMode(string(RoutingModeExceptRussia))
	requireAPISuccess(t, result)
	settings := app.storage.GetAppSettings()
	if settings.RoutingMode != RoutingModeExceptRussia {
		t.Fatalf("stored routing mode = %q, want except_russia", settings.RoutingMode)
	}
	if app.configBuilder.GetRoutingMode() != RoutingModeExceptRussia {
		t.Fatalf("builder routing mode = %q, want except_russia", app.configBuilder.GetRoutingMode())
	}

	configPath := writeActiveScenarioConfig(t, app)
	plan := app.buildDeepWindowsRoutePlan(configPath)
	if plan.RUTraffic != DeepWindowsTrafficDirect || plan.ForeignTraffic != DeepWindowsTrafficProxy || plan.DefaultTraffic != DeepWindowsTrafficProxy {
		t.Fatalf("except-russia plan ru/foreign/default = %s/%s/%s, want direct/proxy/proxy", plan.RUTraffic, plan.ForeignTraffic, plan.DefaultTraffic)
	}

	networkResult := app.SetNetworkMode(string(NetworkModeCompatTun))
	requireAPISuccess(t, networkResult)
	settings = app.storage.GetAppSettings()
	if settings.NetworkMode != NetworkModeCompatTun {
		t.Fatalf("stored network mode = %q, want compat_tun request", settings.NetworkMode)
	}
	status := networkResult["status"].(map[string]interface{})
	active := status["active"].(string)
	if runtime.GOOS == "windows" {
		if active != string(NetworkModeDeepWindows) {
			t.Fatalf("active network mode = %q, want Deep Windows while engine files are available", active)
		}
	} else if active != string(NetworkModeCompatTun) {
		t.Fatalf("active network mode = %q, want compat fallback on non-Windows", active)
	}

	invalidResult := app.SetRoutingMode("bad-mode")
	if success, _ := invalidResult["success"].(bool); success {
		t.Fatalf("invalid routing mode unexpectedly succeeded: %+v", invalidResult)
	}
}

func TestSettingsAPIsPersistFreeAccessPolicy(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)

	result := app.SetDisableFreeAccess(true)
	requireAPISuccess(t, result)
	settings := app.storage.GetAppSettings()
	if !settings.DisableFreeAccess || !settings.FreeAccessEnabled || settings.FreeAccessReverse {
		t.Fatalf("free access settings = disable:%t enabled:%t reverse:%t, want disable=true enabled=true reverse=false",
			settings.DisableFreeAccess, settings.FreeAccessEnabled, settings.FreeAccessReverse)
	}
	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)
	if !planContainsString(plan.ProxyServices, "youtube") || len(plan.TransparentServices) != 0 {
		t.Fatalf("disable-free plan = %+v, want blocked services through subscription proxy only", plan)
	}

	result = app.SetDisableFreeAccess(false)
	requireAPISuccess(t, result)
	result = app.SetFreeAccessServiceMethod("telegram", "subscription")
	requireAPISuccess(t, result)
	settings = app.storage.GetAppSettings()
	if got := settings.FreeAccessMethods["telegram"]; got != FreeAccessMethodVPN {
		t.Fatalf("telegram method = %q, want vpn alias normalization", got)
	}
	plan = buildDeepWindowsRoutePlanForSettings(settings, false, true, true)
	if !planContainsString(plan.BlockedServices, "telegram") {
		t.Fatalf("forced-vpn-without-candidate plan = %+v, want telegram blocked", plan)
	}

	result = app.SetFreeAccessServiceMethod("telegram", DefaultZapretTransparentStrategies[0].Tag)
	requireAPISuccess(t, result)
	settings = app.storage.GetAppSettings()
	plan = buildDeepWindowsRoutePlanForSettings(settings, true, true, true)
	if interceptionEngineSupported() {
		if !planContainsString(plan.TransparentServices, "telegram") {
			t.Fatalf("manual zapret plan = %+v, want telegram transparent", plan)
		}
	} else if !planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("manual zapret plan = %+v, want proxy fallback without a platform interception engine", plan)
	}

	invalidResult := app.SetFreeAccessServiceMethod("unknown-service", FreeAccessMethodDirect)
	if success, _ := invalidResult["success"].(bool); success {
		t.Fatalf("invalid service method unexpectedly succeeded: %+v", invalidResult)
	}
}

func TestSettingsAPIsPersistHideRuTrafficPolicy(t *testing.T) {
	app := newInitializedSettingsScenarioApp(t)

	result := app.SetHideRuTraffic(true, "")
	requireAPISuccess(t, result)
	settings := app.storage.GetAppSettings()
	if !settings.HideRuTraffic {
		t.Fatal("hide RU traffic setting was not stored")
	}
	configPath := writeActiveScenarioConfig(t, app)
	plan := app.buildDeepWindowsRoutePlan(configPath)
	if plan.RUTraffic != DeepWindowsTrafficProxy {
		t.Fatalf("RU traffic action = %s, want proxy when hide-RU is enabled", plan.RUTraffic)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("hide-RU plan should require local proxy endpoint under Deep Windows: %+v", plan)
	}
}

func newInitializedSettingsScenarioApp(t *testing.T) *App {
	t.Helper()

	app, _ := newDeepWindowsTestApp(t, map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "tun", "tag": "tun-in", "auto_route": true},
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen": "127.0.0.1", "listen_port": 2088, "set_system_proxy": false},
		},
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
	})
	app.configBuilder = NewConfigBuilderForStorage(app.storage)
	app.logBuffer = make([]string, 0, MaxLogBufferSize)
	app.initialized = true
	app.initializedReady.Store(true)
	if err := app.configBuilder.BuildConfig(""); err != nil {
		t.Fatalf("initial config build failed: %v", err)
	}
	return app
}

func writeActiveScenarioConfig(t *testing.T, app *App) string {
	t.Helper()

	configPath, err := app.storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	return configPath
}

func requireAPISuccess(t *testing.T, result map[string]interface{}) {
	t.Helper()

	success, _ := result["success"].(bool)
	if !success {
		t.Fatalf("API result failed: %+v", result)
	}
}
