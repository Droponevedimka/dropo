package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newServiceEngineTestApp(t *testing.T) *App {
	t.Helper()
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}
	return &App{basePath: basePath, storage: storage}
}

func writeServiceStrategyCacheForTest(t *testing.T, app *App, entries map[string]serviceStrategyCacheEntry) {
	t.Helper()
	file := serviceStrategyCacheFile{
		Version:           serviceStrategyCacheVersion,
		StrategiesVersion: serviceStrategiesVersion(),
		UpdatedAt:         time.Now(),
		Services:          entries,
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatalf("marshal cache failed: %v", err)
	}
	path := app.serviceStrategyCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create cache dir failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write cache failed: %v", err)
	}
}

func TestServiceFallbackCacheExpiresAndRetries(t *testing.T) {
	app := newServiceEngineTestApp(t)
	now := time.Now()
	writeServiceStrategyCacheForTest(t, app, map[string]serviceStrategyCacheEntry{
		"youtube": {
			MethodTag:          FreeAccessMethodDirect,
			State:              serviceStrategyStateFallback,
			UpdatedAt:          now.Add(-serviceStrategyFallbackTTL),
			RetryAfter:         now.Add(-time.Second),
			NetworkFingerprint: currentNetworkFingerprint(),
		},
	})

	if cache := app.loadServiceStrategyCache(); cache["youtube"].MethodTag != "" {
		t.Fatalf("expired negative result remained cached: %+v", cache["youtube"])
	}
	selections, needSearch := app.resolveServiceSelections(app.serviceHostlistDir(), app.loadServiceStrategyCache())
	if _, ok := selections["youtube"]; !ok {
		t.Fatal("expired fallback must return YouTube to the composed engine")
	}
	if !containsString(needSearch, "youtube") {
		t.Fatalf("expired fallback must queue a new first-success search, got %v", needSearch)
	}
	due := app.serviceStrategiesDueForRetry(now, currentNetworkFingerprint())
	if !containsString(due, "youtube") {
		t.Fatalf("active-session retry timer did not see expired fallback: %v", due)
	}
}

func TestNetworkPrefixIgnoresTemporaryIPv6InterfaceIdentifier(t *testing.T) {
	first := &net.IPNet{IP: net.ParseIP("2001:db8:1234:5678:1111:2222:3333:4444"), Mask: net.CIDRMask(128, 128)}
	second := &net.IPNet{IP: net.ParseIP("2001:db8:1234:5678:aaaa:bbbb:cccc:dddd"), Mask: net.CIDRMask(128, 128)}
	if got, want := networkPrefixForFingerprint(first), networkPrefixForFingerprint(second); got != want || got != "2001:db8:1234:5678::/64" {
		t.Fatalf("temporary IPv6 prefixes = %q and %q", got, want)
	}
	if got := networkPrefixForFingerprint(&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)}); got != "" {
		t.Fatalf("link-local IPv6 address contributed %q to fingerprint", got)
	}
}

func TestServiceFallbackCacheIsTemporaryOnCurrentNetwork(t *testing.T) {
	app := newServiceEngineTestApp(t)
	now := time.Now()
	writeServiceStrategyCacheForTest(t, app, map[string]serviceStrategyCacheEntry{
		"youtube": {
			MethodTag:          FreeAccessMethodVPN,
			State:              serviceStrategyStateFallback,
			UpdatedAt:          now,
			RetryAfter:         now.Add(serviceStrategyFallbackTTL),
			NetworkFingerprint: currentNetworkFingerprint(),
		},
	})

	cache := app.loadServiceStrategyCache()
	if cache["youtube"].MethodTag != FreeAccessMethodVPN {
		t.Fatalf("fresh fallback was not loaded: %+v", cache["youtube"])
	}
	selections, _ := app.resolveServiceSelections(app.serviceHostlistDir(), cache)
	if _, ok := selections["youtube"]; ok {
		t.Fatal("service under a fresh temporary fallback must not remain in winws2 composition")
	}
}

func TestServiceCacheInvalidatesAfterNetworkChange(t *testing.T) {
	app := newServiceEngineTestApp(t)
	method := rankedMethodsForService("youtube")[0]
	writeServiceStrategyCacheForTest(t, app, map[string]serviceStrategyCacheEntry{
		"youtube": {
			MethodTag:          method.Tag,
			State:              serviceStrategyStateWorking,
			UpdatedAt:          time.Now(),
			NetworkFingerprint: "another-network",
		},
	})
	if _, ok := app.loadServiceStrategyCache()["youtube"]; ok {
		t.Fatal("strategy from another network must be invalidated")
	}
}

func TestWindowsUnifiedServiceGroupIsDeterministicSelector(t *testing.T) {
	group := BuildServiceRouteGroup("bypass-youtube", []string{"direct", "auto-select"})
	if group["type"] != "selector" || group["default"] != "direct" {
		t.Fatalf("service route group = %+v, want direct-first selector", group)
	}
	if runtime.GOOS == "windows" {
		settings := GlobalAppSettings{FreeAccessEnabled: true}
		service, _ := findFreeAccessService("youtube")
		got := FreeAccessServiceCandidateTagsForSettings(service, settings, true)
		if !sameStringSet(got, []string{"direct", "auto-select"}) || got[0] != "direct" {
			t.Fatalf("Windows Unified candidates = %v, want direct then VPN fallback", got)
		}
	}
}

func TestWindowsUnifiedCatalogUsesPerServiceWorkingCache(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows Unified is Windows-only")
	}
	app := newServiceEngineTestApp(t)
	service, _ := findFreeAccessService("youtube")
	method := rankedMethodsForService(service.Tag)[0]
	selection := app.selectFreeAccessStrategyForService(
		app.storage.GetAppSettings(),
		service,
		nil,
		map[string]serviceStrategyCacheEntry{
			service.Tag: {MethodTag: method.Tag, State: serviceStrategyStateWorking, Source: "test"},
		},
		nil,
		nil,
		false,
	)
	if selection.MethodTag != method.Tag || selection.MethodLabel != method.Label || selection.MethodKind != "transparent" {
		t.Fatalf("catalog selection = %+v, want cached per-service method %+v", selection, method)
	}
}

func TestStartupServiceSearchLadderKeepsWorkingCachedMethodFirst(t *testing.T) {
	ranked := rankedMethodsForService("discord")
	if len(ranked) < 3 {
		t.Fatalf("Discord strategy ladder is too short: %d", len(ranked))
	}
	cached := ranked[2]
	ladder := startupServiceSearchLadder("discord", cached)
	if len(ladder) != len(ranked) || ladder[0].Tag != cached.Tag {
		t.Fatalf("startup ladder = %#v, want cached method %q first and %d unique methods", ladder, cached.Tag, len(ranked))
	}
	seen := map[string]bool{}
	for _, method := range ladder {
		if seen[method.Tag] {
			t.Fatalf("startup ladder contains duplicate method %q", method.Tag)
		}
		seen[method.Tag] = true
	}
}

func TestConfigSupportsDiscordRealtimeRoutingMigrationGate(t *testing.T) {
	stale := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "selector", "tag": "auto-select", "outbounds": []interface{}{"node-a"}},
		},
		"route": map[string]interface{}{"rules": []interface{}{}},
	}
	if configSupportsDiscordRealtimeRouting(stale) {
		t.Fatal("config without Discord realtime selectors passed the migration gate")
	}

	current := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "selector", "tag": "auto-select", "outbounds": []interface{}{"node-a"}},
			map[string]interface{}{"type": "selector", "tag": discordVPNGroupTag, "outbounds": []interface{}{"node-a"}},
			map[string]interface{}{"type": "selector", "tag": discordRealtimeGroupTag, "outbounds": []interface{}{"direct", discordVPNGroupTag}},
		},
		"route": map[string]interface{}{"rules": []interface{}{
			map[string]interface{}{"process_name": []interface{}{"Discord.exe"}, "network": "udp", "outbound": discordRealtimeGroupTag},
			map[string]interface{}{"domain_suffix": []interface{}{"discord.media"}, "network": "tcp", "outbound": discordRealtimeGroupTag},
		}},
	}
	if !configSupportsDiscordRealtimeRouting(current) {
		t.Fatal("current Discord realtime config did not pass the migration gate")
	}

	withoutVPNSelector := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "selector", "tag": "auto-select", "outbounds": []interface{}{"node-a"}},
			map[string]interface{}{"type": "selector", "tag": discordRealtimeGroupTag, "outbounds": []interface{}{"direct"}},
		},
		"route": current["route"],
	}
	if configSupportsDiscordRealtimeRouting(withoutVPNSelector) {
		t.Fatal("config with VPN candidates but no Discord VPN selector passed the migration gate")
	}
}

func TestGeneratedFreeAccessConfigPassesDiscordRealtimeMigrationGate(t *testing.T) {
	template := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "selector", "tag": "auto-select", "outbounds": []interface{}{"node-a"}},
		},
	}
	builder := &ConfigBuilderForStorage{}
	builder.addFreeAccessOutbounds(template, GlobalAppSettings{})
	template["route"] = map[string]interface{}{
		"rules": builder.buildFreeAccessRules(GlobalAppSettings{}, true),
	}
	if !configSupportsDiscordRealtimeRouting(template) {
		t.Fatal("config produced by the current builder did not pass the Discord realtime migration gate")
	}
}

func TestPackagedZapret2ComposedDryRun(t *testing.T) {
	exePath := os.Getenv("DROPO_TEST_ZAPRET2_EXE")
	if exePath == "" {
		t.Skip("DROPO_TEST_ZAPRET2_EXE is not set")
	}
	exePath, err := filepath.Abs(exePath)
	if err != nil {
		t.Fatalf("resolve winws2 path failed: %v", err)
	}
	binDir := filepath.Dir(exePath)
	hostlistDir := t.TempDir()
	selections := make([]serviceWinwsSelection, 0, 2)
	for _, tag := range []string{"discord", "youtube"} {
		service, ok := findFreeAccessService(tag)
		if !ok {
			t.Fatalf("service %q not found", tag)
		}
		hostlist, err := ensureServiceHostlist(hostlistDir, service)
		if err != nil {
			t.Fatalf("create %s hostlist failed: %v", tag, err)
		}
		selections = append(selections, serviceWinwsSelection{
			ServiceTag:   tag,
			HostlistPath: hostlist,
			Method:       rankedMethodsForService(tag)[0],
		})
	}
	args := append(composeServiceWinwsArgs(selections, binDir), "--dry-run")
	cmd := exec.Command(exePath, args...)
	cmd.Dir = binDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("packaged winws2 rejected composed Discord+YouTube config: %v\n%s", err, output)
	}
}
