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

func TestDiscordWebPrecheckIsNotCachedAsWorkingVoice(t *testing.T) {
	app := newServiceEngineTestApp(t)
	method := rankedMethodsForService("discord")[0]
	app.cacheWebValidatedServiceMethod("discord", method.Tag, "test-web-only")
	if _, ok := app.loadServiceStrategyCache()["discord"]; ok {
		t.Fatal("Discord web-only validation was cached as a working voice strategy")
	}
	app.cacheServiceMethod("discord", method.Tag, "discord-live-media")
	entry, ok := app.loadServiceStrategyCache()["discord"]
	if !ok || entry.MethodTag != method.Tag || entry.Source != "discord-live-media" {
		t.Fatalf("live-media strategy was not cached: %#v", entry)
	}
	app.removeServiceStrategyCacheEntry("discord")
	if _, ok := app.loadServiceStrategyCache()["discord"]; ok {
		t.Fatal("failed voice strategy remained cached after invalidation")
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

func TestWindowsUnifiedBootstrapUsesSafeFallbackUntilStrategyIsProven(t *testing.T) {
	settings := GlobalAppSettings{
		FreeAccessEnabled:  true,
		FreeAccessServices: DefaultFreeAccessServiceState(),
		FreeAccessMethods:  DefaultFreeAccessServiceMethodState(),
	}
	youtube, ok := findFreeAccessService("youtube")
	if !ok {
		t.Fatal("YouTube service is missing")
	}
	if got := windowsUnifiedBootstrapServiceRoute(settings, youtube, serviceStrategyCacheEntry{}, true); got != "auto-select" {
		t.Fatalf("uncached automatic service bootstrap = %q, want VPN fallback", got)
	}
	method := rankedMethodsForService("youtube")[0]
	if got := windowsUnifiedBootstrapServiceRoute(settings, youtube, serviceStrategyCacheEntry{MethodTag: method.Tag}, true); got != "direct" {
		t.Fatalf("proven transparent service bootstrap = %q, want direct", got)
	}
	if got := windowsUnifiedBootstrapDiscordRealtimeRoute(settings, serviceStrategyCacheEntry{}, true); got != discordVPNGroupTag {
		t.Fatalf("uncached Discord realtime bootstrap = %q, want safe VPN route", got)
	}
	if got := windowsUnifiedBootstrapDiscordRealtimeRoute(settings, serviceStrategyCacheEntry{MethodTag: method.Tag}, true); got != "direct" {
		t.Fatalf("proven Discord realtime bootstrap = %q, want direct", got)
	}
}

func TestConnectedValidationRetriesTemporaryFallbacksEverySession(t *testing.T) {
	transparent := rankedMethodsForService("youtube")[0]
	validation := serviceStrategyCacheForConnectedValidation(map[string]serviceStrategyCacheEntry{
		"youtube":  {MethodTag: transparent.Tag},
		"discord":  {MethodTag: FreeAccessMethodVPN, Source: "fallback-vpn"},
		"telegram": {MethodTag: FreeAccessMethodDirect, Source: "fallback-direct"},
	})
	if validation["youtube"].MethodTag != transparent.Tag {
		t.Fatalf("proven transparent cache was discarded: %#v", validation)
	}
	if _, ok := validation["discord"]; ok {
		t.Fatalf("VPN fallback was not queued for connected-session retry: %#v", validation)
	}
	if _, ok := validation["telegram"]; ok {
		t.Fatalf("direct fallback was not queued for connected-session retry: %#v", validation)
	}
}

func TestBackgroundValidationTokenExpiresWhenVPNSessionEnds(t *testing.T) {
	app := &App{isRunning: true}
	app.resetRouteStrategySession()
	session := app.currentRouteStrategySession()
	if !app.routeStrategySessionActive(session) {
		t.Fatal("new running VPN session did not accept its validation token")
	}
	app.invalidateRouteStrategySession()
	if app.routeStrategySessionActive(session) {
		t.Fatal("ended VPN session still accepted a stale background validation token")
	}
}

func TestWindowsUnifiedBootstrapKeepsManualServicePolicyAuthoritative(t *testing.T) {
	settings := GlobalAppSettings{
		FreeAccessEnabled:  true,
		FreeAccessServices: DefaultFreeAccessServiceState(),
		FreeAccessMethods:  DefaultFreeAccessServiceMethodState(),
	}
	discord, ok := findFreeAccessService("discord")
	if !ok {
		t.Fatal("Discord service is missing")
	}
	settings.FreeAccessMethods["discord"] = FreeAccessMethodVPN
	cached := serviceStrategyCacheEntry{MethodTag: rankedMethodsForService("discord")[0].Tag}
	if got := windowsUnifiedBootstrapServiceRoute(settings, discord, cached, true); got != "auto-select" {
		t.Fatalf("manual Discord VPN bootstrap = %q, want auto-select", got)
	}
	if got := windowsUnifiedBootstrapDiscordRealtimeRoute(settings, cached, true); got != discordVPNGroupTag {
		t.Fatalf("manual Discord realtime VPN bootstrap = %q, want %q", got, discordVPNGroupTag)
	}

	settings.FreeAccessMethods["discord"] = FreeAccessMethodDirect
	if got := windowsUnifiedBootstrapServiceRoute(settings, discord, serviceStrategyCacheEntry{MethodTag: FreeAccessMethodVPN}, true); got != "direct" {
		t.Fatalf("manual Discord direct bootstrap = %q, want direct", got)
	}
	if got := windowsUnifiedBootstrapDiscordRealtimeRoute(settings, serviceStrategyCacheEntry{MethodTag: FreeAccessMethodVPN}, true); got != "direct" {
		t.Fatalf("manual Discord realtime direct bootstrap = %q, want direct", got)
	}
}

func TestSelectBootstrapOutboundPinsExistingCandidateWithoutHealthChecks(t *testing.T) {
	group := map[string]interface{}{
		"type":      "urltest",
		"outbounds": []interface{}{"direct", "auto-select"},
		"url":       "https://example.com",
		"interval":  "90s",
	}
	if !selectBootstrapOutbound(group, "auto-select") {
		t.Fatal("bootstrap selector did not report a route change")
	}
	if group["type"] != "selector" || group["default"] != "auto-select" {
		t.Fatalf("bootstrap group = %#v, want pinned auto-select selector", group)
	}
	if got := interfaceStringSlice(group["outbounds"]); len(got) != 2 || got[0] != "auto-select" || got[1] != "direct" {
		t.Fatalf("bootstrap candidates = %v, want VPN then direct", got)
	}
	if _, exists := group["url"]; exists {
		t.Fatalf("bootstrap selector retained urltest fields: %#v", group)
	}
	if selectBootstrapOutbound(group, "missing") {
		t.Fatal("bootstrap selector accepted a missing outbound candidate")
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

func TestManualVPNPolicyNeverFallsBackToFreeStrategy(t *testing.T) {
	app := newServiceEngineTestApp(t)
	service, ok := findFreeAccessService("discord")
	if !ok {
		t.Fatal("Discord service is missing")
	}
	settings := app.storage.GetAppSettings()
	settings.FreeAccessMethods = map[string]string{service.Tag: FreeAccessMethodVPN}
	freeMethod := rankedMethodsForService(service.Tag)[0]

	selection := app.selectFreeAccessStrategyForService(
		settings,
		service,
		nil,
		map[string]serviceStrategyCacheEntry{
			service.Tag: {MethodTag: freeMethod.Tag, State: serviceStrategyStateWorking, Source: "test"},
		},
		nil,
		map[string]bool{freeMethod.Tag: true},
		false,
	)
	if selection.MethodTag != "" {
		t.Fatalf("strict manual VPN selection = %+v, want no route while VPN is unavailable", selection)
	}
}

func TestStartupServiceSearchLadderKeepsWorkingCachedMethodFirst(t *testing.T) {
	ranked := rankedMethodsForService("discord")
	if len(ranked) < 3 {
		t.Fatalf("Discord strategy ladder is too short: %d", len(ranked))
	}
	cached := ranked[2]
	ladder := startupServiceSearchLadder("discord", cached, maxNoSubscriptionStrategies)
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

func TestAutomaticServiceStrategySearchIsBoundedBeforeVPNFallback(t *testing.T) {
	ranked := rankedMethodsForService("youtube")
	if len(ranked) != maxNoSubscriptionStrategies {
		t.Fatalf("YouTube native ladder = %d strategies, want %d distinct typed plans", len(ranked), maxNoSubscriptionStrategies)
	}
	cached := ServiceBypassMethod{Tag: "cached-legacy-method", Label: "Cached legacy method"}
	startup := startupServiceSearchLadder("youtube", cached)
	if len(startup) != maxAutomaticServiceStrategies || startup[0].Tag != cached.Tag {
		t.Fatalf("startup ladder = %#v, want cached first and %d total strategies", startup, maxAutomaticServiceStrategies)
	}
	withoutVPN := startupServiceSearchLadder("youtube", cached, maxNoSubscriptionStrategies)
	if len(withoutVPN) != maxNoSubscriptionStrategies || withoutVPN[0].Tag != cached.Tag {
		t.Fatalf("no-subscription ladder = %#v, want cached first and %d attempts", withoutVPN, maxNoSubscriptionStrategies)
	}
	recovery := recoveryServiceSearchLadder("youtube", cached.Tag)
	if len(recovery) != maxAutomaticServiceStrategies-1 {
		t.Fatalf("recovery ladder = %#v, want %d alternatives after failed current strategy", recovery, maxAutomaticServiceStrategies-1)
	}
	for _, method := range recovery {
		if method.Tag == cached.Tag {
			t.Fatalf("recovery ladder repeats failed current strategy %q", cached.Tag)
		}
	}
}

func TestNoSubscriptionStrategyCampaignIsExtendedButBounded(t *testing.T) {
	campaign := serviceStrategySearchCampaign{
		Cycle:      serviceStrategyExtendedCycles,
		CycleTotal: serviceStrategyExtendedCycles,
		HasVPN:     false,
	}
	attempt, total := backgroundStrategyAttempt(maxAutomaticServiceStrategies-1, maxAutomaticServiceStrategies, campaign)
	wantTotal := serviceStrategyExtendedCycles * maxAutomaticServiceStrategies
	if attempt != wantTotal || total != wantTotal {
		t.Fatalf("last extended attempt = %d/%d, want %d/%d", attempt, total, wantTotal, wantTotal)
	}
	if total <= maxAutomaticServiceStrategies {
		t.Fatalf("no-subscription campaign has only %d attempts, want more than the fast VPN ladder", total)
	}
	waitBudget := time.Duration(serviceStrategyExtendedCycles-1) * serviceStrategyExtendedRetryInterval
	if waitBudget >= serviceStrategyExtendedMaxDuration {
		t.Fatalf("retry waits consume %s, want room for probes inside %s", waitBudget, serviceStrategyExtendedMaxDuration)
	}
}

func TestConnectedValidationRetriesOnlyRequestedFallbacks(t *testing.T) {
	now := time.Now()
	cache := map[string]serviceStrategyCacheEntry{
		"youtube":  {MethodTag: FreeAccessMethodDirect, UpdatedAt: now},
		"discord":  {MethodTag: FreeAccessMethodDirect, UpdatedAt: now},
		"telegram": {MethodTag: "telegram-native-split", UpdatedAt: now},
	}
	filtered := serviceStrategyCacheForConnectedValidationTags(cache, []string{"youtube"})
	if _, ok := filtered["youtube"]; ok {
		t.Fatal("requested YouTube fallback was retained instead of being retried")
	}
	if got := filtered["discord"].MethodTag; got != FreeAccessMethodDirect {
		t.Fatalf("unrelated Discord fallback = %q, want it preserved", got)
	}
	if got := filtered["telegram"].MethodTag; got != "telegram-native-split" {
		t.Fatalf("working Telegram strategy = %q, want it preserved", got)
	}
}

func TestCommonBlockedStrategySearchIsBounded(t *testing.T) {
	all := commonBlockedMethods()
	if len(all) < maxAutomaticServiceStrategies {
		t.Fatalf("common strategy catalog is too short: %d", len(all))
	}
	cached := all[len(all)-1]
	ladder := commonBlockedSearchLadder(cached)
	if len(ladder) != maxAutomaticServiceStrategies || ladder[0].Tag != cached.Tag {
		t.Fatalf("common startup ladder = %#v, want cached first and %d attempts", ladder, maxAutomaticServiceStrategies)
	}
	seen := map[string]bool{}
	for _, method := range ladder {
		if seen[method.Tag] {
			t.Fatalf("common startup ladder repeats %q", method.Tag)
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

func TestLatencySensitiveDirectRoutingMigrationGate(t *testing.T) {
	stale := map[string]interface{}{
		"route": map[string]interface{}{"rules": []interface{}{}},
		"dns":   map[string]interface{}{"rules": []interface{}{}},
	}
	if !configNeedsLatencySensitiveDirectMigration(stale, RoutingModeExceptRussia) {
		t.Fatal("except_russia config without latency-sensitive game direct rules did not request migration")
	}
	if configNeedsLatencySensitiveDirectMigration(stale, RoutingModeAllTraffic) {
		t.Fatal("all_traffic must not require a latency-sensitive game direct carve-out")
	}
	previousRelease := map[string]interface{}{
		"route": map[string]interface{}{"rules": []interface{}{
			map[string]interface{}{
				"domain_suffix": []string{
					"steam.com", "steampowered.com", "steamcommunity.com", "steamstatic.com",
					"steamcontent.com", "steamserver.net", "steamgames.com", "steam-chat.com",
					"valvesoftware.com", "valvesoftware.net", "valvecdn.com", "counter-strike.net",
				},
				"action": "route", "outbound": "direct",
			},
			map[string]interface{}{
				"process_name": []string{"steam.exe", "steamservice.exe", "steamwebhelper.exe", "cs2.exe"},
				"action":       "route", "outbound": "direct",
			},
		}},
		"dns": map[string]interface{}{"rules": []interface{}{
			map[string]interface{}{
				"domain_suffix": []string{
					"steam.com", "steampowered.com", "steamcommunity.com", "steamstatic.com",
					"steamcontent.com", "steamserver.net", "steamgames.com", "steam-chat.com",
					"valvesoftware.com", "valvesoftware.net", "valvecdn.com", "counter-strike.net",
				},
				"action": "route", "server": "dns-direct",
			},
		}},
	}
	if !configNeedsLatencySensitiveDirectMigration(previousRelease, RoutingModeExceptRussia) {
		t.Fatal("pre-Riot split-routing config did not request migration")
	}

	current := map[string]interface{}{
		"route": map[string]interface{}{"rules": buildDirectServiceRules()},
		"dns": map[string]interface{}{"rules": []interface{}{
			map[string]interface{}{
				"domain_suffix": DirectDomainSuffixes,
				"action":        "route",
				"server":        "dns-direct",
			},
		}},
	}
	if configNeedsLatencySensitiveDirectMigration(current, RoutingModeExceptRussia) {
		t.Fatal("current latency-sensitive game direct rules unexpectedly requested migration")
	}
}

func TestDefenderDegradedModePinsBlockedServicesToSubscription(t *testing.T) {
	path := filepath.Join(t.TempDir(), "active.json")
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "vless", "tag": "node-a"},
			map[string]interface{}{"type": "selector", "tag": "auto-select", "outbounds": []interface{}{"node-a"}},
			map[string]interface{}{"type": "urltest", "tag": ServiceBypassGroupTag("discord"), "outbounds": []interface{}{"direct", "auto-select"}, "url": "https://discord.com", "interval": "90s"},
			map[string]interface{}{"type": "urltest", "tag": SmartBypassGroupTag, "outbounds": []interface{}{"direct", "auto-select"}, "url": "https://example.com"},
		},
	}
	if err := writeJSONConfig(path, config); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	changed, err := app.forceSubscriptionFallbackForTransparentRuntime(path)
	if err != nil || !changed {
		t.Fatalf("force subscription fallback changed=%v err=%v", changed, err)
	}
	updated, err := readJSONConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{ServiceBypassGroupTag("discord"), SmartBypassGroupTag} {
		group := findOutboundMap(updated["outbounds"].([]interface{}), tag)
		if group == nil || group["type"] != "selector" || group["default"] != "auto-select" {
			t.Fatalf("degraded group %s = %#v", tag, group)
		}
		if _, exists := group["url"]; exists {
			t.Fatalf("degraded group %s retained an active health probe: %#v", tag, group)
		}
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
		selection := serviceWinwsSelection{
			ServiceTag:   tag,
			HostlistPath: hostlist,
			Method:       rankedMethodsForService(tag)[0],
		}
		selections = append(selections, selection)
	}
	args := append(composeServiceWinwsArgs(selections, binDir), "--dry-run")
	cmd := exec.Command(exePath, args...)
	cmd.Dir = binDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("packaged winws2 rejected composed Discord+YouTube config: %v\n%s", err, output)
	}
}
