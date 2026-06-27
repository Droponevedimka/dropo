package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBuildConfigWithoutSubscriptionUsesFreeAccess(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	filtersPath := filepath.Join(basePath, "bin", FiltersFolder)
	if err := os.MkdirAll(filtersPath, 0755); err != nil {
		t.Fatalf("create filters dir failed: %v", err)
	}
	for _, filter := range FilterFiles {
		if err := os.WriteFile(filepath.Join(filtersPath, filter.Name), []byte("test"), 0644); err != nil {
			t.Fatalf("write filter %s failed: %v", filter.Name, err)
		}
	}

	builder := NewConfigBuilderForStorage(storage)
	if err := builder.BuildConfig(""); err != nil {
		t.Fatalf("BuildConfig without subscription failed: %v", err)
	}

	profile, err := storage.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile failed: %v", err)
	}
	config, err := storage.GetProfileConfig(profile.ID)
	if err != nil {
		t.Fatalf("get profile config failed: %v", err)
	}

	if !containsOutbound(config, ByeDPIOutboundTag) {
		t.Fatalf("generated config does not contain %q outbound", ByeDPIOutboundTag)
	}
	if !containsOutbound(config, SpoofDPIOutboundTag) {
		t.Fatalf("generated config does not contain %q outbound", SpoofDPIOutboundTag)
	}
	if !containsOutbound(config, SmartBypassGroupTag) {
		t.Fatalf("generated config does not contain %q group", SmartBypassGroupTag)
	}
	if containsOutbound(config, ServiceBypassGroupTag("openai")) {
		t.Fatalf("generated config without subscription must not create %q", ServiceBypassGroupTag("openai"))
	}
	for _, serviceTag := range []string{"telegram", "meta", "whatsapp"} {
		if containsOutbound(config, ServiceBypassGroupTag(serviceTag)) {
			t.Fatalf("generated config without subscription must not create %q", ServiceBypassGroupTag(serviceTag))
		}
		if containsRouteOutbound(config, ServiceBypassGroupTag(serviceTag)) {
			t.Fatalf("generated config without subscription must not route through %q", ServiceBypassGroupTag(serviceTag))
		}
	}
	if !containsDomainSuffixRouteRule(config, "openai.com", "direct") {
		t.Fatal("AI services must stay direct when no VPN subscription exists")
	}
	if !containsDNSDomainSuffixServer(config, "openai.com", "dns-direct") {
		t.Fatal("AI services must use direct DNS when no VPN subscription exists")
	}

	candidates := getOutboundCandidates(config, SmartBypassGroupTag)
	methodTags := FreeAccessMethodTags()
	if !sameStringSet(candidates, methodTags) {
		t.Fatalf("%s candidates without subscription = %v, want %v", SmartBypassGroupTag, candidates, methodTags)
	}
	if containsString(candidates, "direct") {
		t.Fatalf("%s must not contain direct for blocked services: %v", SmartBypassGroupTag, candidates)
	}
	if !containsProcessDirectRule(config, ByeDPIProcessName) {
		t.Fatalf("generated config does not bypass %s process traffic directly", ByeDPIProcessName)
	}
	if !containsProcessDirectRule(config, SpoofDPIExeName) {
		t.Fatalf("generated config does not bypass %s process traffic directly", SpoofDPIExeName)
	}
	if !containsProcessDirectRule(config, ZapretProcessName) {
		t.Fatalf("generated config does not bypass %s process traffic directly", ZapretProcessName)
	}
	if !containsProcessDirectRule(config, XrayExeName) {
		t.Fatalf("generated config does not bypass %s process traffic directly", XrayExeName)
	}
	if containsProcessRouteRule(config, "Telegram.exe", ServiceBypassGroupTag("telegram")) {
		t.Fatal("generated config without subscription must not route Telegram.exe through a fake Telegram bypass group")
	}
	if containsIPRouteRule(config, "149.154.160.0/20", ServiceBypassGroupTag("telegram")) ||
		containsIPRouteRule(config, "91.105.192.0/23", ServiceBypassGroupTag("telegram")) {
		t.Fatal("generated config without subscription must not route Telegram IP ranges through a fake Telegram bypass group")
	}
	if containsDomainSuffixRouteRule(config, "telegram.org", ServiceBypassGroupTag("telegram")) {
		t.Fatal("generated config without subscription must not route telegram.org through a fake Telegram bypass group")
	}
	if containsDNSDomainSuffixServer(config, "telegram.org", "dns-remote") {
		t.Fatal("generated config without subscription must not force telegram.org through dns-remote")
	}
	for _, domain := range []string{"youtubei.googleapis.com", "googlevideo.com", "www.gstatic.com", "linkedin.com", "facetime.apple.com", "viber.com", "snapchat.com", "tiktok.com"} {
		tag := expectedServiceTagForDomain(domain)
		if tag == "" {
			t.Fatalf("test fixture has no expected service for %s", domain)
		}
		if !containsDomainSuffixRouteRule(config, domain, ServiceBypassGroupTag(tag)) {
			t.Fatalf("generated config does not route %s through %s", domain, ServiceBypassGroupTag(tag))
		}
		if !containsDNSDomainSuffixServer(config, domain, "dns-remote") {
			t.Fatalf("generated config does not resolve %s through dns-remote", domain)
		}
	}
	if !containsIPRouteRule(config, "66.22.192.0/18", ServiceBypassGroupTag("discord")) {
		t.Fatal("generated config does not route Discord voice/media IP range through Discord bypass group")
	}
	if containsIPRouteRule(config, "185.76.151.0/24", ServiceBypassGroupTag("telegram")) {
		t.Fatal("generated config without subscription must not route current Telegram IPv4 range through Telegram bypass group")
	}
	if containsIPRouteRule(config, "31.13.64.0/18", ServiceBypassGroupTag("meta")) {
		t.Fatal("generated config without subscription must not route Meta CDN IP ranges through Meta bypass group")
	}
	for _, domain := range []string{"yandex.ru", "ozon.ru", "sber.ru", "gosuslugi.ru", "vk.com", "learn.javascript.ru"} {
		if !containsDomainSuffixRouteRule(config, domain, "direct") {
			t.Fatalf("generated config does not route %s directly by default", domain)
		}
		if !containsDNSDomainSuffixServer(config, domain, "dns-direct") {
			t.Fatalf("generated config does not resolve %s through dns-direct by default", domain)
		}
	}
	if !routeRuleBeforeRuleSet(config, "domain_suffix", "yandex.ru", "refilter-domains") {
		t.Fatal("RU direct domain rules must be before RKN catch-all")
	}
	if containsRouteRuleSet(config, "community-domains") || containsRouteRuleSet(config, "community-ips") {
		t.Fatal("blocked_only catch-all must not route broad community rule-sets through smart-bypass")
	}
	if final := getDNSFinal(config); final != "dns-direct" {
		t.Fatalf("dns final = %q, want dns-direct for blocked_only defaults", final)
	}
	if strategy := getDNSStrategy(config); strategy != "ipv4_only" {
		t.Fatalf("dns strategy = %q, want ipv4_only", strategy)
	}
	if resolver := getDefaultDomainResolverStrategy(config); resolver != "ipv4_only" {
		t.Fatalf("route default domain resolver strategy = %q, want ipv4_only", resolver)
	}

	activeConfigPath, err := storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	activeConfigData, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatalf("read active config failed: %v", err)
	}
	var activeConfig map[string]interface{}
	if err := json.Unmarshal(activeConfigData, &activeConfig); err != nil {
		t.Fatalf("parse active config failed: %v", err)
	}
	if strictRoute := getTunStrictRoute(activeConfig); strictRoute {
		t.Fatal("active TUN config must disable strict_route for split/free-access routing")
	}
	if addresses := getTunAddresses(activeConfig); containsIPv6Address(addresses) {
		t.Fatalf("active TUN config must stay IPv4-only on Windows split routing, got %v", addresses)
	}
}

func TestFilterActiveFreeAccessOutboundsRemovesInactiveStrategies(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{"type": "socks", "tag": "byedpi-sni"},
			map[string]interface{}{"type": "socks", "tag": "byedpi-oob"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"byedpi", "byedpi-sni", "auto-select"},
				"default":   "byedpi-sni",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi-oob", "byedpi-sni"},
				"default":   "byedpi-oob",
			},
		},
	}

	path := filepath.Join(t.TempDir(), "active_config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	app := &App{}
	if err := app.filterActiveFreeAccessOutbounds(path, []string{"byedpi"}); err != nil {
		t.Fatalf("filter active free access outbounds failed: %v", err)
	}

	filteredData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read filtered config failed: %v", err)
	}
	var filtered map[string]interface{}
	if err := json.Unmarshal(filteredData, &filtered); err != nil {
		t.Fatalf("parse filtered config failed: %v", err)
	}

	if !containsOutbound(filtered, "byedpi") {
		t.Fatal("active byedpi outbound was removed")
	}
	if containsOutbound(filtered, "byedpi-sni") || containsOutbound(filtered, "byedpi-oob") {
		t.Fatal("inactive ByeDPI strategy outbounds must be removed")
	}

	smartCandidates := getOutboundCandidates(filtered, SmartBypassGroupTag)
	if !sameStringSet(smartCandidates, []string{"byedpi", "auto-select"}) {
		t.Fatalf("%s candidates = %v, want active byedpi plus auto-select", SmartBypassGroupTag, smartCandidates)
	}
	telegramCandidates := getOutboundCandidates(filtered, ServiceBypassGroupTag("telegram"))
	if len(telegramCandidates) != 1 || telegramCandidates[0] != NoRouteOutboundTag {
		t.Fatalf("%s candidates = %v, want no-route fallback when no strategy is active", ServiceBypassGroupTag("telegram"), telegramCandidates)
	}
	if !containsOutbound(filtered, NoRouteOutboundTag) {
		t.Fatalf("filtered config does not contain %q outbound", NoRouteOutboundTag)
	}
	if got := interfaceStringSlice([]string{"byedpi", "auto-select"}); !sameStringSet(got, []string{"byedpi", "auto-select"}) {
		t.Fatalf("interfaceStringSlice([]string) = %v", got)
	}
}

func TestGenerateOutboundsUsesURLTestAutoSelectForMultipleSubscriptions(t *testing.T) {
	builder := &ConfigBuilderForStorage{}
	outbounds := builder.generateOutbounds(map[string]interface{}{}, []ProxyConfig{
		{Type: "vless", Tag: "vless-fast", Server: "example.com", ServerPort: 443, UUID: "11111111-1111-1111-1111-111111111111"},
		{Type: "vless", Tag: "vless-backup", Server: "backup.example.com", ServerPort: 443, UUID: "22222222-2222-2222-2222-222222222222"},
	})

	auto := getOutbound(map[string]interface{}{"outbounds": outbounds}, "auto-select")
	if auto == nil {
		t.Fatal("auto-select outbound was not generated")
	}
	if auto["type"] != "urltest" {
		t.Fatalf("auto-select type = %v, want urltest", auto["type"])
	}
	if !sameStringSet(interfaceStringSlice(auto["outbounds"]), []string{"vless-fast", "vless-backup"}) {
		t.Fatalf("auto-select outbounds = %v, want both subscription candidates", auto["outbounds"])
	}
	for _, key := range []string{"url", "interval", "tolerance", "interrupt_exist_connections"} {
		if _, ok := auto[key]; !ok {
			t.Fatalf("auto-select urltest missing field %s: %#v", key, auto)
		}
	}
	if _, ok := auto["default"]; ok {
		t.Fatalf("auto-select urltest must not pin the first subscription as default: %#v", auto)
	}
}

func TestGenerateOutboundsUsesSelectorAutoSelectForSingleSubscription(t *testing.T) {
	builder := &ConfigBuilderForStorage{}
	outbounds := builder.generateOutbounds(map[string]interface{}{}, []ProxyConfig{
		{Type: "vless", Tag: "vless-only", Server: "example.com", ServerPort: 443, UUID: "11111111-1111-1111-1111-111111111111"},
	})

	auto := getOutbound(map[string]interface{}{"outbounds": outbounds}, "auto-select")
	if auto == nil {
		t.Fatal("auto-select outbound was not generated")
	}
	if auto["type"] != "selector" {
		t.Fatalf("auto-select type = %v, want selector for a single candidate", auto["type"])
	}
	if auto["default"] != "vless-only" {
		t.Fatalf("auto-select default = %v, want the only subscription tag", auto["default"])
	}
	for _, key := range []string{"url", "interval", "tolerance", "interrupt_exist_connections"} {
		if _, ok := auto[key]; ok {
			t.Fatalf("single-candidate auto-select should not keep urltest field %s: %#v", key, auto)
		}
	}
}

func TestFilterActiveFreeAccessOutboundsUsesDirectWhenDeepWindowsAvailable(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	for _, name := range []string{
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
	} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("test"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": "byedpi-sni"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi-sni"},
				"default":   "byedpi-sni",
			},
		},
	}

	path := filepath.Join(t.TempDir(), "active_config.json")
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config failed: %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	app := &App{basePath: basePath}
	if err := app.filterActiveFreeAccessOutbounds(path, nil); err != nil {
		t.Fatalf("filter active free access outbounds failed: %v", err)
	}

	filteredData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read filtered config failed: %v", err)
	}
	var filtered map[string]interface{}
	if err := json.Unmarshal(filteredData, &filtered); err != nil {
		t.Fatalf("parse filtered config failed: %v", err)
	}

	telegramCandidates := getOutboundCandidates(filtered, ServiceBypassGroupTag("telegram"))
	if len(telegramCandidates) != 1 || telegramCandidates[0] != "direct" {
		t.Fatalf("%s candidates = %v, want direct via Deep Windows transparent engine", ServiceBypassGroupTag("telegram"), telegramCandidates)
	}
	if containsOutbound(filtered, NoRouteOutboundTag) {
		t.Fatalf("filtered config must not contain %q when Deep Windows is available", NoRouteOutboundTag)
	}
}

func TestApplyRouteProbeSelectionsPrefersServiceGroupsWithoutDroppingFallbacks(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{"type": "socks", "tag": "byedpi-sni"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"byedpi", "byedpi-sni"},
				"url":       "https://discord.com",
				"interval":  "90s",
				"tolerance": float64(0),
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi", "byedpi-sni"},
				"url":       "https://telegram.org",
				"interval":  "90s",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("discord"),
				"outbounds": []interface{}{"byedpi", "byedpi-sni"},
				"url":       "https://discord.com",
				"interval":  "90s",
			},
		},
	}

	changed := applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{
		{Tag: "telegram", Name: "Telegram", Success: true, MethodTag: "byedpi-sni", LatencyMS: 40},
		{Tag: "discord", Name: "Discord", Success: true, MethodTag: "byedpi", LatencyMS: 20},
		{Tag: "youtube", Name: "YouTube", Success: false, Error: "timeout"},
	})
	if !changed {
		t.Fatal("expected route probe selections to update config")
	}

	telegram := getOutbound(config, ServiceBypassGroupTag("telegram"))
	if telegram["type"] != "urltest" {
		t.Fatalf("telegram group type = %v, want urltest", telegram["type"])
	}
	if candidates := getOutboundCandidates(config, ServiceBypassGroupTag("telegram")); len(candidates) != 2 || candidates[0] != "byedpi-sni" || candidates[1] != "byedpi" {
		t.Fatalf("telegram candidates = %v, want byedpi-sni first with byedpi fallback", candidates)
	}
	if _, ok := telegram["url"]; !ok {
		t.Fatal("preferred telegram urltest must keep health-check URL")
	}

	smartCandidates := getOutboundCandidates(config, SmartBypassGroupTag)
	if len(smartCandidates) != 2 || smartCandidates[0] != "byedpi" || smartCandidates[1] != "byedpi-sni" {
		t.Fatalf("%s candidates = %v, want aggregate winner byedpi first with fallback", SmartBypassGroupTag, smartCandidates)
	}

	if candidates := getOutboundCandidates(config, ServiceBypassGroupTag("youtube")); len(candidates) != 0 {
		t.Fatalf("non-existent youtube group candidates = %v, want none", candidates)
	}
}

func TestProbeServiceCandidatesPrefersWorkingFreeMethodBeforeVPN(t *testing.T) {
	service := FreeAccessService{
		Tag:         "youtube",
		DisplayName: "YouTube",
		HealthURL:   "https://probe.local/ok",
	}
	candidates := []routeProbeCandidate{
		{
			Tag:       "byedpi",
			Label:     "ByeDPI",
			Kind:      "proxy",
			Client:    &http.Client{Transport: delayedProbeTransport{Delay: 40 * time.Millisecond}},
			Available: true,
		},
		{
			Tag:       "vless-fast",
			Label:     "VPN vless-fast",
			Kind:      "vpn",
			Client:    &http.Client{Transport: delayedProbeTransport{Delay: time.Millisecond}},
			Available: true,
		},
	}

	result := (&App{}).probeServiceCandidates(service, candidates)
	if !result.Success {
		t.Fatalf("route probe failed: %s", result.Error)
	}
	if result.MethodTag != "byedpi" || result.MethodKind != "proxy" {
		t.Fatalf("selected method = %s/%s, want working free method before VPN fallback", result.MethodTag, result.MethodKind)
	}
}

func TestApplyRouteProbeSelectionsCanPreferVPNOverFreeStrategy(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-fast"},
				"url":       "https://cp.cloudflare.com/generate_204",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://discord.com",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("youtube"),
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://www.youtube.com/generate_204",
			},
		},
	}

	changed := applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{
		{Tag: "youtube", Name: "YouTube", Success: true, MethodTag: "vless-fast", MethodKind: "vpn", MethodLabel: "VPN vless-fast", LatencyMS: 12},
	})
	if !changed {
		t.Fatal("expected route probe selection to update config")
	}

	youtubeCandidates := getOutboundCandidates(config, ServiceBypassGroupTag("youtube"))
	if len(youtubeCandidates) < 3 || youtubeCandidates[0] != "vless-fast" || youtubeCandidates[1] != "byedpi" || youtubeCandidates[2] != "auto-select" {
		t.Fatalf("%s candidates = %v, want VPN first with free fallback preserved", ServiceBypassGroupTag("youtube"), youtubeCandidates)
	}
	smartCandidates := getOutboundCandidates(config, SmartBypassGroupTag)
	if len(smartCandidates) < 3 || smartCandidates[0] != "vless-fast" {
		t.Fatalf("%s candidates = %v, want aggregate VPN winner first", SmartBypassGroupTag, smartCandidates)
	}
}

func TestApplyRouteProbeSelectionsMapsInternalVPNMethodToAutoSelect(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example"},
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
		},
	}

	changed := applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{
		{Tag: "openai", Name: "AI services", Success: true, MethodTag: FreeAccessMethodVPN, MethodKind: "vpn", MethodLabel: "VPN", LatencyMS: 0},
	})
	if !changed {
		t.Fatal("expected route probe selection to update config")
	}

	candidates := getOutboundCandidates(config, ServiceBypassGroupTag("openai"))
	if len(candidates) != 1 || candidates[0] != "auto-select" {
		t.Fatalf("%s candidates = %v, want only real auto-select outbound", ServiceBypassGroupTag("openai"), candidates)
	}
	if containsString(candidates, FreeAccessMethodVPN) {
		t.Fatalf("%s candidates must not contain internal method tag %q: %v", ServiceBypassGroupTag("openai"), FreeAccessMethodVPN, candidates)
	}
}

func TestApplyStoredFreeAccessStrategiesUsesAutoSelectForVPNRequiredServices(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example"},
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
		},
	}
	configPath := filepath.Join(basePath, "resources", "active_config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("create resources failed: %v", err)
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	app := &App{basePath: basePath, storage: storage}
	changed, err := app.applyStoredFreeAccessStrategiesToConfig(configPath, nil)
	if err != nil {
		t.Fatalf("apply stored free access strategies failed: %v", err)
	}
	if !changed {
		t.Fatal("expected VPN-required service group to be normalized")
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	candidates := getOutboundCandidates(updated, ServiceBypassGroupTag("openai"))
	if len(candidates) != 1 || candidates[0] != "auto-select" {
		t.Fatalf("%s candidates = %v, want only real auto-select outbound", ServiceBypassGroupTag("openai"), candidates)
	}
	if containsString(candidates, FreeAccessMethodVPN) {
		t.Fatalf("%s candidates must not contain internal method tag %q: %v", ServiceBypassGroupTag("openai"), FreeAccessMethodVPN, candidates)
	}
}

func TestApplyRouteProbeSelectionsPreservesFallbacksForTransparentMethods(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://discord.com",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://telegram.org",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("discord"),
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://discord.com",
			},
		},
	}

	changed := applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{
		{Tag: "telegram", Name: "Telegram", Success: true, MethodTag: "zapret-winws-desync", MethodKind: "transparent", LatencyMS: 35},
		{Tag: "discord", Name: "Discord", Success: true, MethodTag: "zapret-winws-desync", MethodKind: "transparent", LatencyMS: 25},
	})
	if !changed {
		t.Fatal("expected transparent route probe selections to update config")
	}

	// Hybrid: a VPN fallback exists, so transparent services become a urltest of
	// [direct (winws desync), VPN] — direct wins when desync works, VPN when not.
	telegramCandidates := getOutboundCandidates(config, ServiceBypassGroupTag("telegram"))
	if len(telegramCandidates) != 2 || telegramCandidates[0] != "direct" || telegramCandidates[1] != "auto-select" {
		t.Fatalf("%s candidates = %v, want direct with VPN fallback only", ServiceBypassGroupTag("telegram"), telegramCandidates)
	}
	telegramGroup := getOutbound(config, ServiceBypassGroupTag("telegram"))
	if telegramGroup["type"] != "urltest" {
		t.Fatalf("%s type = %v, want urltest for hybrid direct↔VPN auto-fallback", ServiceBypassGroupTag("telegram"), telegramGroup["type"])
	}
	if _, hasDefault := telegramGroup["default"]; hasDefault {
		t.Fatalf("%s must not pin a default in hybrid urltest mode", ServiceBypassGroupTag("telegram"))
	}
	if url, _ := telegramGroup["url"].(string); url != "https://telegram.org" {
		t.Fatalf("%s url = %v, want the service health URL preserved", ServiceBypassGroupTag("telegram"), telegramGroup["url"])
	}

	// smart-bypass (generic blocked catch-all not covered by per-service winws)
	// keeps its built free-proxy + VPN form when a VPN fallback exists, rather
	// than being pinned to transparent-direct.
	smartCandidates := getOutboundCandidates(config, SmartBypassGroupTag)
	if len(smartCandidates) != 2 || smartCandidates[0] != "byedpi" || smartCandidates[1] != "auto-select" {
		t.Fatalf("%s candidates = %v, want generic free-proxy + VPN fallback preserved", SmartBypassGroupTag, smartCandidates)
	}
}

func TestApplyRouteProbeSelectionsPreservesFailedServiceCandidates(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi"},
				"url":       "https://telegram.org",
			},
		},
	}

	changed := applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{
		{Tag: "telegram", Name: "Telegram", Success: false, Error: "timeout"},
	})
	if changed {
		t.Fatal("failed route probe must not rewrite service group to no-route")
	}

	candidates := getOutboundCandidates(config, ServiceBypassGroupTag("telegram"))
	if len(candidates) != 1 || candidates[0] != "byedpi" {
		t.Fatalf("%s candidates = %v, want original candidates preserved", ServiceBypassGroupTag("telegram"), candidates)
	}
	if containsOutbound(config, NoRouteOutboundTag) {
		t.Fatalf("failed route probe must not add %s outbound", NoRouteOutboundTag)
	}
}

func TestApplyCachedRouteProbeToConfigUsesPersistedSelection(t *testing.T) {
	basePath := t.TempDir()
	app := &App{basePath: basePath}
	report := &routeProbeReport{
		DurationMS: 123,
		Services: []routeProbeServiceResult{
			{Tag: "telegram", Name: "Telegram", Success: true, MethodTag: "byedpi-sni", MethodKind: "proxy", MethodLabel: "ByeDPI SNI split", LatencyMS: 41},
			{Tag: "discord", Name: "Discord", Success: true, MethodTag: "byedpi", MethodKind: "proxy", MethodLabel: "ByeDPI auto", LatencyMS: 33},
		},
	}
	if err := app.saveRouteProbeCache(report); err != nil {
		t.Fatalf("save route probe cache failed: %v", err)
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{"type": "socks", "tag": "byedpi-sni"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi", "byedpi-sni", "auto-select"},
				"url":       "https://telegram.org",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"byedpi", "byedpi-sni", "auto-select"},
				"url":       "https://discord.com",
			},
		},
	}
	configPath := filepath.Join(basePath, "active_config.json")
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	applied, fresh, err := app.applyCachedRouteProbeToConfig(configPath, true, []string{"byedpi", "byedpi-sni"})
	if err != nil {
		t.Fatalf("apply cached route probe failed: %v", err)
	}
	if !applied {
		t.Fatal("expected cached route probe to be applied")
	}
	if !fresh {
		t.Fatal("newly saved cache must be fresh")
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated active config failed: %v", err)
	}
	telegramCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("telegram"))
	if len(telegramCandidates) != 3 || telegramCandidates[0] != "byedpi-sni" || telegramCandidates[2] != "auto-select" {
		t.Fatalf("telegram candidates = %v, want cached winner first and VPN fallback preserved", telegramCandidates)
	}
	smartCandidates := getOutboundCandidates(updated, SmartBypassGroupTag)
	if len(smartCandidates) != 3 || smartCandidates[0] != "byedpi" {
		t.Fatalf("smart-bypass candidates = %v, want aggregate cached winner first", smartCandidates)
	}

	snapshot := app.routeProbeResultsSnapshot()
	if len(snapshot) != 2 {
		t.Fatalf("remembered route probe results = %d, want 2", len(snapshot))
	}
}

func TestApplyCachedRouteProbeSkipsUnavailableTransparentMethod(t *testing.T) {
	basePath := t.TempDir()
	app := &App{basePath: basePath}
	report := &routeProbeReport{
		DurationMS: 50,
		Services: []routeProbeServiceResult{
			{Tag: "telegram", Name: "Telegram", Success: true, MethodTag: "zapret-winws-desync", MethodKind: "transparent", MethodLabel: "Zapret winws desync", LatencyMS: 20},
		},
	}
	if err := app.saveRouteProbeCache(report); err != nil {
		t.Fatalf("save route probe cache failed: %v", err)
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{"byedpi", "auto-select"},
				"url":       "https://telegram.org",
			},
		},
	}
	configPath := filepath.Join(basePath, "active_config.json")
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	applied, _, err := app.applyCachedRouteProbeToConfig(configPath, true, []string{"byedpi"})
	if err != nil {
		t.Fatalf("apply cached route probe failed: %v", err)
	}
	if applied {
		t.Fatal("transparent cache selection must not be applied when zapret is unavailable")
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated active config failed: %v", err)
	}
	candidates := getOutboundCandidates(updated, ServiceBypassGroupTag("telegram"))
	if len(candidates) != 2 || candidates[0] != "byedpi" || candidates[1] != "auto-select" {
		t.Fatalf("telegram candidates = %v, want original candidates preserved", candidates)
	}
}

func TestStoredFreeAccessDefaultsToZapretWithoutByeDPIFallback(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("zapret transparent strategies are Windows-only")
	}
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin failed: %v", err)
	}
	for _, name := range []string{
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
	} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("sidecar"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": ByeDPIOutboundTag},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("youtube"),
				"outbounds": []interface{}{ByeDPIOutboundTag, "byedpi-sni"},
				"url":       "https://www.youtube.com",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{ByeDPIOutboundTag, "byedpi-sni"},
				"url":       "https://discord.com",
			},
		},
	}
	configPath := filepath.Join(basePath, "resources", "active_config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		t.Fatalf("create resources failed: %v", err)
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	app := &App{
		basePath: basePath,
		storage:  storage,
		zapret:   NewTransparentBypassManager(basePath, DefaultZapretTransparentStrategies, nil),
	}
	changed, err := app.applyStoredFreeAccessStrategiesToConfig(configPath, []string{ByeDPIOutboundTag, "byedpi-sni"})
	if err != nil {
		t.Fatalf("apply stored free access strategies failed: %v", err)
	}
	if !changed {
		t.Fatal("expected default zapret strategies to rewrite bypass groups")
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	youtubeCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("youtube"))
	if len(youtubeCandidates) != 1 || youtubeCandidates[0] != "direct" {
		t.Fatalf("youtube candidates = %v, want only direct under zapret", youtubeCandidates)
	}
	youtubeGroup := getOutbound(updated, ServiceBypassGroupTag("youtube"))
	if youtubeGroup["type"] != "selector" {
		t.Fatalf("youtube group type = %v, want selector when only transparent direct is available", youtubeGroup["type"])
	}
	for _, key := range []string{"url", "interval", "tolerance", "interrupt_exist_connections"} {
		if _, ok := youtubeGroup[key]; ok {
			t.Fatalf("youtube selector kept unsupported urltest field %s: %#v", key, youtubeGroup)
		}
	}
	smartCandidates := getOutboundCandidates(updated, SmartBypassGroupTag)
	if len(smartCandidates) != 1 || smartCandidates[0] != "direct" {
		t.Fatalf("smart candidates = %v, want only direct under zapret", smartCandidates)
	}
	smartGroup := getOutbound(updated, SmartBypassGroupTag)
	for _, key := range []string{"url", "interval", "tolerance", "interrupt_exist_connections"} {
		if _, ok := smartGroup[key]; ok {
			t.Fatalf("smart selector kept unsupported urltest field %s: %#v", key, smartGroup)
		}
	}
}

func TestStoredProxyStrategyDoesNotOverrideDefaultZapret(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("zapret transparent strategies are Windows-only")
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": ByeDPIOutboundTag},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("discord"),
				"outbounds": []interface{}{ByeDPIOutboundTag},
				"url":       "https://discord.com/api/v10/gateway",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{ByeDPIOutboundTag},
				"url":       "https://discord.com",
			},
		},
	}
	app, configPath := newDeepWindowsTestApp(t, config)

	strategyFile := freeAccessStrategyFile{
		Version:   freeAccessStrategyVersion,
		UpdatedAt: time.Now(),
		Services: []freeAccessStrategySelection{
			makeFreeAccessStrategySelection(FreeAccessService{Tag: "discord", DisplayName: "Discord"}, ByeDPIOutboundTag, "cached-probe", 47),
		},
	}
	strategyData, err := json.MarshalIndent(strategyFile, "", "  ")
	if err != nil {
		t.Fatalf("marshal strategy file failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app.storage.GetResourcesPath(), freeAccessStrategyFileName), strategyData, 0644); err != nil {
		t.Fatalf("write strategy file failed: %v", err)
	}

	changed, err := app.applyStoredFreeAccessStrategiesToConfig(configPath, []string{ByeDPIOutboundTag})
	if err != nil {
		t.Fatalf("apply stored free access strategies failed: %v", err)
	}
	if !changed {
		t.Fatal("expected default zapret strategy to override stored proxy strategy")
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	discordCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("discord"))
	if len(discordCandidates) != 1 || discordCandidates[0] != "direct" {
		t.Fatalf("discord candidates = %v, want only direct under default zapret", discordCandidates)
	}
	smartCandidates := getOutboundCandidates(updated, SmartBypassGroupTag)
	if len(smartCandidates) != 1 || smartCandidates[0] != "direct" {
		t.Fatalf("smart candidates = %v, want only direct under default zapret", smartCandidates)
	}
}

func TestStoredVPNStrategyDoesNotOverrideAvailableFreeMethod(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("zapret transparent strategies are Windows-only")
	}
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin failed: %v", err)
	}
	for _, name := range []string{
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
	} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("sidecar"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	strategyFile := freeAccessStrategyFile{
		Version:   freeAccessStrategyVersion,
		UpdatedAt: time.Now(),
		Services: []freeAccessStrategySelection{
			makeFreeAccessStrategySelection(FreeAccessService{Tag: "youtube", DisplayName: "YouTube"}, FreeAccessMethodVPN, "manual-probe", 120),
		},
	}
	strategyData, err := json.MarshalIndent(strategyFile, "", "  ")
	if err != nil {
		t.Fatalf("marshal strategy file failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(storage.GetResourcesPath(), freeAccessStrategyFileName), strategyData, 0644); err != nil {
		t.Fatalf("write strategy file failed: %v", err)
	}

	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": ByeDPIOutboundTag},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example"},
			map[string]interface{}{
				"type":      "selector",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-fast"},
				"default":   "vless-fast",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("youtube"),
				"outbounds": []interface{}{ByeDPIOutboundTag, "auto-select"},
				"url":       "https://www.youtube.com",
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{ByeDPIOutboundTag, "auto-select"},
				"url":       "https://discord.com",
			},
		},
	}
	configPath := filepath.Join(basePath, "resources", "active_config.json")
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}

	app := &App{
		basePath: basePath,
		storage:  storage,
		zapret:   NewTransparentBypassManager(basePath, DefaultZapretTransparentStrategies, nil),
	}
	changed, err := app.applyStoredFreeAccessStrategiesToConfig(configPath, []string{ByeDPIOutboundTag})
	if err != nil {
		t.Fatalf("apply stored free access strategies failed: %v", err)
	}
	if !changed {
		t.Fatal("expected available zapret strategy to override stale stored VPN strategy")
	}
	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	youtubeCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("youtube"))
	if len(youtubeCandidates) != 2 || youtubeCandidates[0] != "direct" || youtubeCandidates[1] != "auto-select" {
		t.Fatalf("youtube candidates = %v, want free transparent direct with VPN fallback", youtubeCandidates)
	}
}

func TestApplyServiceFreeFallbackUsesVPNWhenSubscriptionAvailable(t *testing.T) {
	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
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
				"tag":       ServiceBypassGroupTag("youtube"),
				"outbounds": []interface{}{"direct", "auto-select"},
				"default":   "direct",
			},
			map[string]interface{}{
				"type":      "selector",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{"direct", "auto-select"},
				"default":   "direct",
			},
		},
	})

	app.applyServiceFreeFallback("youtube")

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	youtubeCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("youtube"))
	if len(youtubeCandidates) == 0 || youtubeCandidates[0] != "auto-select" {
		t.Fatalf("%s candidates = %v, want VPN auto-select first", ServiceBypassGroupTag("youtube"), youtubeCandidates)
	}
	youtubeGroup := getOutbound(updated, ServiceBypassGroupTag("youtube"))
	if youtubeGroup["default"] != "auto-select" {
		t.Fatalf("%s default = %v, want auto-select", ServiceBypassGroupTag("youtube"), youtubeGroup["default"])
	}
	cache := app.loadServiceStrategyCache()
	if cache["youtube"].MethodTag != FreeAccessMethodVPN {
		t.Fatalf("youtube cached method = %+v, want VPN fallback", cache["youtube"])
	}
}

func TestStoredServiceVPNFallbackOverridesDefaultFreeStrategy(t *testing.T) {
	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": ByeDPIOutboundTag},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example", "server_port": 443, "uuid": "00000000-0000-0000-0000-000000000000"},
			map[string]interface{}{
				"type":      "selector",
				"tag":       "auto-select",
				"outbounds": []interface{}{"vless-fast"},
				"default":   "vless-fast",
			},
			map[string]interface{}{
				"type":      "selector",
				"tag":       ServiceBypassGroupTag("youtube"),
				"outbounds": []interface{}{ByeDPIOutboundTag, "auto-select"},
				"default":   ByeDPIOutboundTag,
			},
			map[string]interface{}{
				"type":      "selector",
				"tag":       SmartBypassGroupTag,
				"outbounds": []interface{}{ByeDPIOutboundTag, "auto-select"},
				"default":   ByeDPIOutboundTag,
			},
		},
	})
	app.cacheServiceMethod("youtube", FreeAccessMethodVPN, "fallback-vpn")

	changed, err := app.applyStoredFreeAccessStrategiesToConfig(configPath, []string{ByeDPIOutboundTag})
	if err != nil {
		t.Fatalf("apply stored free access strategies failed: %v", err)
	}
	if !changed {
		t.Fatal("expected cached service VPN fallback to rewrite bypass group")
	}
	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	youtubeCandidates := getOutboundCandidates(updated, ServiceBypassGroupTag("youtube"))
	if len(youtubeCandidates) == 0 || youtubeCandidates[0] != "auto-select" {
		t.Fatalf("%s candidates = %v, want cached VPN auto-select before free methods", ServiceBypassGroupTag("youtube"), youtubeCandidates)
	}
	youtubeGroup := getOutbound(updated, ServiceBypassGroupTag("youtube"))
	if youtubeGroup["default"] != "auto-select" {
		t.Fatalf("%s default = %v, want auto-select", ServiceBypassGroupTag("youtube"), youtubeGroup["default"])
	}
}

func TestProxyMaintenanceSelectionDoesNotStopDeepWindowsTransparentEngine(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app, configPath := newDeepWindowsTestApp(t, map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": ByeDPIOutboundTag},
			map[string]interface{}{
				"type":      "selector",
				"tag":       ServiceBypassGroupTag("discord"),
				"outbounds": []interface{}{"direct"},
			},
		},
	})
	app.isRunning = true

	app.applyServiceStrategySelection(routeProbeServiceResult{
		Tag:         "discord",
		Name:        "Discord",
		MethodTag:   ByeDPIOutboundTag,
		MethodLabel: "ByeDPI auto",
		MethodKind:  "proxy",
		LatencyMS:   42,
		Success:     true,
	})

	for _, entry := range app.logBuffer {
		if strings.Contains(entry, "no transparent route was selected") {
			t.Fatalf("proxy maintenance result must not stop/report missing Deep Windows transparent engine, log: %s", entry)
		}
	}

	updated, err := readJSONConfig(configPath)
	if err != nil {
		t.Fatalf("read updated config failed: %v", err)
	}
	candidates := getOutboundCandidates(updated, ServiceBypassGroupTag("discord"))
	if len(candidates) == 0 || candidates[0] != ByeDPIOutboundTag {
		t.Fatalf("discord candidates = %v, want proxy strategy persisted first in config", candidates)
	}
}

func TestServiceBypassGroupUsableRouteRejectsNoRouteOnlyGroup(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{
				"type":      "selector",
				"tag":       ServiceBypassGroupTag("telegram"),
				"outbounds": []interface{}{NoRouteOutboundTag},
			},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       ServiceBypassGroupTag("discord"),
				"outbounds": []interface{}{NoRouteOutboundTag, "byedpi"},
			},
		},
	}

	if serviceBypassGroupHasUsableRoute(config, "telegram") {
		t.Fatal("telegram no-route-only group must not count as usable")
	}
	if !serviceBypassGroupHasUsableRoute(config, "discord") {
		t.Fatal("discord group with a real fallback must count as usable")
	}
}

func TestZapretHostlistContainsOnlyFreeAccessServices(t *testing.T) {
	manager := NewTransparentBypassManager(t.TempDir(), DefaultZapretTransparentStrategies, nil)
	hostlistPath, err := manager.ensureHostlist()
	if err != nil {
		t.Fatalf("ensureHostlist failed: %v", err)
	}
	data, err := os.ReadFile(hostlistPath)
	if err != nil {
		t.Fatalf("read hostlist failed: %v", err)
	}
	hostlist := string(data)
	for _, expected := range []string{"discord.com", "youtube.com", "telegram.org", "whatsapp.com", "tiktok.com"} {
		if !strings.Contains(hostlist, expected) {
			t.Fatalf("zapret hostlist does not contain %s: %s", expected, hostlist)
		}
	}
	for _, forbidden := range []string{"openai.com", "chatgpt.com", "claude.ai"} {
		if strings.Contains(hostlist, forbidden) {
			t.Fatalf("zapret hostlist must not contain VPN-only domain %s", forbidden)
		}
	}

	ipsetPath, err := manager.ensureIPSet()
	if err != nil {
		t.Fatalf("ensureIPSet failed: %v", err)
	}
	ipsetData, err := os.ReadFile(ipsetPath)
	if err != nil {
		t.Fatalf("read ipset failed: %v", err)
	}
	ipset := string(ipsetData)
	for _, expected := range []string{"149.154.160.0/20", "91.108.4.0/22", "66.22.192.0/18"} {
		if !strings.Contains(ipset, expected) {
			t.Fatalf("zapret ipset does not contain %s: %s", expected, ipset)
		}
	}
}

func TestTransparentScopeArgsAreAppliedToEveryFilterProfile(t *testing.T) {
	args := applyTransparentScopeArgs(
		[]string{
			"--wf-tcp=80,443",
			"--filter-tcp=80",
			"--dpi-desync=fake",
			"--new",
			"--filter-tcp=443",
			"--dpi-desync=fake,multidisorder",
			"--new",
			"--filter-udp=443",
			"--dpi-desync=fake",
		},
		[]string{"--hostlist=blocked.txt", "--ipset=blocked-ip.txt"},
	)

	filterCount := 0
	hostlistCount := 0
	ipsetCount := 0
	for _, arg := range args {
		if strings.HasPrefix(arg, "--filter-tcp=") || strings.HasPrefix(arg, "--filter-udp=") {
			filterCount++
		}
		if arg == "--hostlist=blocked.txt" {
			hostlistCount++
		}
		if arg == "--ipset=blocked-ip.txt" {
			ipsetCount++
		}
	}
	if filterCount != 3 || hostlistCount != filterCount || ipsetCount != filterCount {
		t.Fatalf("scope args were not applied to every filter: filters=%d hostlists=%d ipsets=%d args=%v",
			filterCount, hostlistCount, ipsetCount, args)
	}
}

func TestEnsureTransparentScopeArgsScopesManualProfileGaps(t *testing.T) {
	args := ensureTransparentScopeArgs(
		[]string{
			"--wf-tcp=80,443",
			"--filter-tcp=80",
			"--dpi-desync=fake",
			"--new",
			"--filter-tcp=443",
			"--hostlist=already-scoped.txt",
			"--dpi-desync=fake,multidisorder",
			"--new",
			"--filter-udp=443",
			"--dpi-desync=fake",
		},
		[]string{"--hostlist=blocked.txt", "--ipset=blocked-ip.txt"},
	)

	assertZapretFilterSegmentsScoped(t, "manual-gap-sample", args)
	if countString(args, "--hostlist=already-scoped.txt") != 1 {
		t.Fatalf("pre-scoped segment was modified unexpectedly: %v", args)
	}
	if got := countString(args, "--hostlist=blocked.txt"); got != 2 {
		t.Fatalf("runtime scope count = %d, want 2 for the unscoped profiles: %v", got, args)
	}
}

func TestDefaultZapretRuntimeArgsAreScopedToBlockedLists(t *testing.T) {
	scopeArgs := []string{"--hostlist=blocked.txt", "--ipset=blocked-ip.txt"}
	for _, strategy := range DefaultZapretTransparentStrategies {
		args := strategy.Args
		if strategy.ManualScope {
			args = resolveTransparentStrategyArgs(args, "blocked.txt", "blocked-ip.txt", filepath.Join("C:", "dropo", "bin"))
		} else {
			args = applyTransparentScopeArgs(args, scopeArgs)
		}
		args = ensureTransparentScopeArgs(args, scopeArgs)
		assertZapretFilterSegmentsScoped(t, strategy.Tag, args)
	}
}

func assertZapretFilterSegmentsScoped(t *testing.T, strategyTag string, args []string) {
	t.Helper()
	segment := make([]string, 0, len(args))
	flush := func() {
		if len(segment) == 0 {
			return
		}
		hasFilter := false
		hasScope := false
		for _, arg := range segment {
			if strings.HasPrefix(arg, "--filter-tcp=") || strings.HasPrefix(arg, "--filter-udp=") {
				hasFilter = true
			}
			if arg == "--hostlist" || strings.HasPrefix(arg, "--hostlist=") ||
				strings.HasPrefix(arg, "--hostlist-domains=") ||
				arg == "--ipset" || strings.HasPrefix(arg, "--ipset=") {
				hasScope = true
			}
		}
		if hasFilter && !hasScope {
			t.Fatalf("%s has unscoped WinDivert filter segment: %v\nfull args: %v", strategyTag, segment, args)
		}
		segment = segment[:0]
	}

	for _, arg := range args {
		if arg == "--new" {
			flush()
		}
		segment = append(segment, arg)
	}
	flush()
}

func countString(values []string, want string) int {
	count := 0
	for _, value := range values {
		if value == want {
			count++
		}
	}
	return count
}

// The default transparent strategy reproduces the Flowseal zapret-discord-youtube
// "general (ALT2)" preset (multisplit + split-seqovl), which is what clients run
// successfully standalone. It must be self-contained on the bundled
// www_google_com payloads so it is guaranteed to start.
func TestDefaultZapretStrategyMatchesFlowsealPresetShape(t *testing.T) {
	if len(DefaultZapretTransparentStrategies) == 0 {
		t.Fatal("expected at least one zapret strategy")
	}
	strategy := DefaultZapretTransparentStrategies[0]
	if strategy.Tag != "flowseal-general-alt2" {
		t.Fatalf("default zapret strategy = %s, want flowseal-general-alt2", strategy.Tag)
	}
	args := strings.Join(strategy.Args, " ")

	for _, expected := range []string{
		"--dpi-desync=multisplit",
		"--dpi-desync-split-seqovl=652",
		"--dpi-desync-split-seqovl-pattern=${BIN}tls_clienthello_www_google_com.bin",
		"--dpi-desync-fake-quic=${BIN}quic_initial_www_google_com.bin",
		"--filter-l7=discord,stun",
		"--dpi-desync-fake-discord=${BIN}quic_initial_dbankcloud_ru.bin",
	} {
		if !strings.Contains(args, expected) {
			t.Fatalf("default zapret strategy does not contain %q: %s", expected, args)
		}
	}

	// Every strategy must only reference payloads we actually bundle, otherwise
	// winws fails to start and the reselection silently skips it.
	bundledBins := map[string]bool{
		"quic_initial_www_google_com.bin":       true,
		"quic_initial_dbankcloud_ru.bin":        true,
		"tls_clienthello_www_google_com.bin":    true,
		"discord-ip-discovery-without-port.bin": true,
		"stun.bin":                              true,
		"windivert_part.discord_media.txt":      true,
		"windivert_part.stun.txt":               true,
	}
	for _, s := range DefaultZapretTransparentStrategies {
		for _, arg := range s.Args {
			idx := strings.Index(arg, "${BIN}")
			if idx < 0 {
				continue
			}
			bin := arg[idx+len("${BIN}"):]
			if !bundledBins[bin] {
				t.Fatalf("strategy %s references non-bundled payload %q", s.Tag, bin)
			}
		}
	}
}

func TestRouteProbeKeepsTransparentCandidateForIPCIDRService(t *testing.T) {
	client := &http.Client{}
	service := FreeAccessService{
		Tag:         "telegram",
		DisplayName: "Telegram",
		IPCIDRs:     []string{"149.154.160.0/20"},
		HealthURL:   "https://telegram.org",
	}
	candidates := []routeProbeCandidate{
		{Tag: "byedpi", Kind: "free", Client: client, Available: true},
		{Tag: "zapret-winws-desync", Kind: "transparent", Client: client, Available: true},
	}

	filtered := candidatesForRouteProbeService(service, candidates)
	if len(filtered) != 2 {
		t.Fatalf("Telegram candidates = %#v, want proxy and transparent candidates", filtered)
	}

	service.IPCIDRs = nil
	filtered = candidatesForRouteProbeService(service, candidates)
	if len(filtered) != 2 {
		t.Fatalf("domain-only candidates = %#v, want transparent candidate included", filtered)
	}
}

func TestManagedSidecarPathsStayInsideAppRoot(t *testing.T) {
	basePath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "sing-box.exe")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0644); err != nil {
		t.Fatalf("write outside sing-box failed: %v", err)
	}
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin failed: %v", err)
	}
	for _, name := range []string{ByeDPIProcessName, ZapretProcessName, XrayExeName} {
		if err := os.WriteFile(filepath.Join(binPath, name), []byte("sidecar"), 0644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}

	app := &App{basePath: basePath, singboxPath: outsidePath}
	paths := app.managedSidecarPaths()
	for _, path := range paths {
		if !pathIsInside(path, basePath) {
			t.Fatalf("managed sidecar path escaped app root: %s", path)
		}
		if strings.EqualFold(path, outsidePath) {
			t.Fatalf("outside sing-box path was included: %s", path)
		}
	}
	if len(paths) != 3 {
		t.Fatalf("managed sidecar paths = %v, want 3 bundled sidecars", paths)
	}
}

func TestDropoPortableSidecarRootsIncludeSiblingBuilds(t *testing.T) {
	parent := t.TempDir()
	current := filepath.Join(parent, "dropo-2.0.0-current")
	old := filepath.Join(parent, "dropo-2.0.0-old")
	other := filepath.Join(parent, "other-vpn")
	for _, root := range []string{current, old, other} {
		binPath := filepath.Join(root, "bin")
		if err := os.MkdirAll(binPath, 0755); err != nil {
			t.Fatalf("create %s failed: %v", binPath, err)
		}
		if err := os.WriteFile(filepath.Join(binPath, ByeDPIProcessName), []byte("sidecar"), 0644); err != nil {
			t.Fatalf("write sidecar in %s failed: %v", root, err)
		}
	}

	app := &App{basePath: current}
	roots := app.dropoPortableSidecarRoots()
	if !containsPathFold(roots, current) {
		t.Fatalf("roots = %v, want current build root", roots)
	}
	if !containsPathFold(roots, old) {
		t.Fatalf("roots = %v, want sibling dropo build root", roots)
	}
	if containsPathFold(roots, other) {
		t.Fatalf("roots = %v, must not include non-dropo sibling", roots)
	}
}

func TestClearDirectOutboundInterfaceRemovesStaleBinding(t *testing.T) {
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{
				"type":               "direct",
				"tag":                "direct",
				"bind_interface":     "Wi-Fi",
				"inet4_bind_address": "192.0.2.10",
				"inet6_bind_address": "2001:db8::10",
			},
		},
	}

	if !clearDirectOutboundInterface(config) {
		t.Fatal("expected stale direct binding fields to be removed")
	}

	outbounds, _ := config["outbounds"].([]interface{})
	direct, _ := outbounds[0].(map[string]interface{})
	for _, key := range []string{"bind_interface", "inet4_bind_address", "inet6_bind_address"} {
		if _, ok := direct[key]; ok {
			t.Fatalf("direct outbound still contains %s", key)
		}
	}
}

func TestDisableTunIPv6RemovesStaleIPv6Routes(t *testing.T) {
	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":          "tun",
				"tag":           "tun-in",
				"address":       []interface{}{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"route_address": []interface{}{"0.0.0.0/1", "::/1"},
				"inet6_address": "fdfe:dcba:9876::1/126",
			},
		},
	}

	if !disableTunIPv6(config) {
		t.Fatal("expected TUN IPv6 fields to be removed")
	}

	inbounds, _ := config["inbounds"].([]interface{})
	tun, _ := inbounds[0].(map[string]interface{})
	if addresses := normalizeStringListForTest(tun["address"]); !sameStringSet(addresses, []string{"172.19.0.1/30"}) {
		t.Fatalf("tun address = %v, want only IPv4 address", addresses)
	}
	if routes := normalizeStringListForTest(tun["route_address"]); !sameStringSet(routes, []string{"0.0.0.0/1"}) {
		t.Fatalf("tun route_address = %v, want only IPv4 route", routes)
	}
	if _, ok := tun["inet6_address"]; ok {
		t.Fatal("tun inet6_address was not removed")
	}
}

func TestWindowsTunAutoRouteKeepsProxySidecarsWithProcessDirectRule(t *testing.T) {
	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":       "tun",
				"tag":        "tun-in",
				"auto_route": true,
			},
		},
		"route": map[string]interface{}{
			"rules": []interface{}{
				map[string]interface{}{
					"process_name": freeAccessProcessNames(),
					"action":       "route",
					"outbound":     "direct",
				},
			},
		},
	}
	tags := []string{ByeDPIOutboundTag, "byedpi-sni", SpoofDPIOutboundTag}

	filtered, skipped := routeProbeFreeProxyTagsForConfig(config, tags)
	if skipped || !sameStringSet(filtered, tags) {
		t.Fatalf("protected free proxy tags = %v skipped=%v, want original tags", filtered, skipped)
	}
}

func TestWindowsTunAutoRouteSkipsProxySidecarsWithoutProcessDirectRule(t *testing.T) {
	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":       "tun",
				"tag":        "tun-in",
				"auto_route": true,
			},
		},
	}
	tags := []string{ByeDPIOutboundTag, "byedpi-sni", SpoofDPIOutboundTag}

	filtered, skipped := routeProbeFreeProxyTagsForConfig(config, tags)
	if runtime.GOOS == "windows" {
		if !skipped {
			t.Fatal("Windows TUN auto_route without process direct rule must skip proxy sidecar methods")
		}
		if len(filtered) != 0 {
			t.Fatalf("filtered tags = %v, want none", filtered)
		}
		return
	}
	if skipped || !sameStringSet(filtered, tags) {
		t.Fatalf("non-Windows tags = %v skipped=%v, want original tags", filtered, skipped)
	}
}

func TestDeepWindowsDoesNotTreatInactiveTunAsProxySidecarCapture(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows engine is Windows-only")
	}

	app := newDeepWindowsReadyApp(t)
	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":       "tun",
				"tag":        "tun-in",
				"auto_route": true,
			},
		},
	}
	tags := []string{ByeDPIOutboundTag, "byedpi-sni", SpoofDPIOutboundTag}

	if app.freeProxySidecarsCapturedByActiveNetwork(config) {
		t.Fatal("Deep Windows must ignore inactive TUN inbound when deciding whether sidecars are captured")
	}
	filtered, skipped := app.routeProbeFreeProxyTagsForActiveNetwork(config, tags)
	if skipped || !sameStringSet(filtered, tags) {
		t.Fatalf("Deep Windows active tags = %v skipped=%v, want original tags", filtered, skipped)
	}
}

func TestGetStatusDoesNotRewriteActiveConfigWhileRunning(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	builder := NewConfigBuilderForStorage(storage)
	if err := builder.BuildConfig(""); err != nil {
		t.Fatalf("BuildConfig without subscription failed: %v", err)
	}

	activeConfigPath, err := storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	beforeData, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatalf("read active config failed: %v", err)
	}

	var activeConfig map[string]interface{}
	if err := json.Unmarshal(beforeData, &activeConfig); err != nil {
		t.Fatalf("parse active config failed: %v", err)
	}
	mixedPort := getMixedInboundPort(activeConfig)
	if mixedPort <= 0 {
		t.Fatal("active config does not contain mixed inbound port")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort)))
	if err != nil {
		t.Fatalf("listen on mixed port %d failed: %v", mixedPort, err)
	}
	defer listener.Close()

	app := &App{
		storage:     storage,
		initialized: true,
		isRunning:   true,
	}
	status := app.GetStatus()
	if status["configExists"] != true {
		t.Fatalf("GetStatus configExists = %v, want true", status["configExists"])
	}

	afterData, err := os.ReadFile(activeConfigPath)
	if err != nil {
		t.Fatalf("read active config after GetStatus failed: %v", err)
	}
	if string(afterData) != string(beforeData) {
		t.Fatal("GetStatus must not rewrite active_config.json while sing-box is running")
	}
}

func TestDomainSuffixCatalogsUseSingBoxFormat(t *testing.T) {
	for name, values := range map[string][]string{
		"local": LocalDomainSuffixes,
		"ru":    RuDomainSuffixes,
	} {
		for _, value := range values {
			if strings.HasPrefix(value, ".") {
				t.Fatalf("%s domain suffix %q must not start with a dot", name, value)
			}
		}
	}

	for _, svc := range DefaultFreeAccessServices {
		for _, value := range svc.DomainSuffixes {
			if strings.HasPrefix(value, ".") {
				t.Fatalf("%s domain suffix %q must not start with a dot", svc.Tag, value)
			}
		}
	}
}

func TestSmartBypassPrefersFreeAccessWhenSubscriptionExists(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	builder := NewConfigBuilderForStorage(storage)
	template := map[string]interface{}{
		"outbounds_template": map[string]interface{}{},
	}
	template["outbounds"] = builder.generateOutbounds(template, []ProxyConfig{
		{
			Type:       "socks",
			Tag:        "proxy-0",
			Server:     "127.0.0.1",
			ServerPort: 1080,
			Name:       "test",
		},
	})

	settings := storage.GetAppSettings()
	settings.FreeAccessEnabled = true
	settings.FreeAccessReverse = false
	builder.addFreeAccessOutbounds(template, settings)

	config := map[string]interface{}{"outbounds": template["outbounds"]}
	candidates := getOutboundCandidates(config, SmartBypassGroupTag)
	for _, tag := range FreeAccessMethodTags() {
		if !containsString(candidates, tag) {
			t.Fatalf("%s candidates with subscription = %v, missing %s", SmartBypassGroupTag, candidates, tag)
		}
	}
	if !containsString(candidates, "auto-select") {
		t.Fatalf("%s candidates with subscription = %v, want auto-select", SmartBypassGroupTag, candidates)
	}
	if len(candidates) == 0 || candidates[len(candidates)-1] != "auto-select" {
		t.Fatalf("%s should keep VPN auto-select only as fallback when a subscription exists, got %v", SmartBypassGroupTag, candidates)
	}
	if len(candidates) < 2 || candidates[0] == "auto-select" {
		t.Fatalf("%s should prefer free-access methods before VPN auto-select, got %v", SmartBypassGroupTag, candidates)
	}
	if containsString(candidates, "direct") || containsString(candidates, "proxy") {
		t.Fatalf("%s must use only real bypass/VPN candidates, got %v", SmartBypassGroupTag, candidates)
	}

	for _, serviceTag := range []string{"telegram", "meta", "whatsapp"} {
		candidates := getOutboundCandidates(config, ServiceBypassGroupTag(serviceTag))
		if len(candidates) != 1 || candidates[0] != "auto-select" {
			t.Fatalf("%s candidates with subscription = %v, want only auto-select", ServiceBypassGroupTag(serviceTag), candidates)
		}
	}

	aiCandidates := getOutboundCandidates(config, ServiceBypassGroupTag("openai"))
	if len(aiCandidates) != 1 || aiCandidates[0] != "auto-select" {
		t.Fatalf("%s candidates with subscription = %v, want only auto-select", ServiceBypassGroupTag("openai"), aiCandidates)
	}
}

func TestDisableFreeAccessUsesVPNOnlyServiceGroups(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	builder := NewConfigBuilderForStorage(storage)
	template := map[string]interface{}{
		"outbounds_template": map[string]interface{}{},
	}
	template["outbounds"] = builder.generateOutbounds(template, []ProxyConfig{
		{
			Type:       "socks",
			Tag:        "proxy-0",
			Server:     "127.0.0.1",
			ServerPort: 1080,
			Name:       "test",
		},
	})

	settings := storage.GetAppSettings()
	settings.DisableFreeAccess = true
	builder.addFreeAccessOutbounds(template, settings)

	config := map[string]interface{}{"outbounds": template["outbounds"]}
	if containsOutbound(config, ByeDPIOutboundTag) {
		t.Fatalf("disabled free access must not create %q outbound", ByeDPIOutboundTag)
	}

	for _, group := range []string{SmartBypassGroupTag, ServiceBypassGroupTag("telegram"), ServiceBypassGroupTag("youtube"), ServiceBypassGroupTag("openai")} {
		candidates := getOutboundCandidates(config, group)
		if len(candidates) != 1 || candidates[0] != "auto-select" {
			t.Fatalf("%s candidates with disabled free access = %v, want [auto-select]", group, candidates)
		}
	}

	routeConfig := map[string]interface{}{
		"route": map[string]interface{}{
			"rules": builder.buildFreeAccessRules(settings, true),
		},
	}
	if !containsDomainSuffixRouteRule(routeConfig, "telegram.org", ServiceBypassGroupTag("telegram")) {
		t.Fatal("telegram.org must use Telegram VPN-only bypass group when free methods are disabled")
	}
	if !containsDomainSuffixRouteRule(routeConfig, "youtube.com", ServiceBypassGroupTag("youtube")) {
		t.Fatal("youtube.com must use YouTube VPN-only bypass group when free methods are disabled")
	}
	if !containsDomainSuffixRouteRule(routeConfig, "openai.com", ServiceBypassGroupTag("openai")) {
		t.Fatal("openai.com must use AI VPN-only bypass group when free methods are disabled")
	}

	processRuleConfig := map[string]interface{}{
		"route": map[string]interface{}{
			"rules": buildFreeAccessProcessRules(settings),
		},
	}
	if !containsProcessDirectRule(processRuleConfig, XrayExeName) {
		t.Fatalf("disabled free access must still bypass %s process traffic directly", XrayExeName)
	}
	if containsProcessDirectRule(processRuleConfig, ByeDPIProcessName) {
		t.Fatalf("disabled free access must not add direct process bypass for %s", ByeDPIProcessName)
	}
}

func TestExceptRussiaUsesBypassForForeignTraffic(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	settings := storage.GetAppSettings()
	settings.RoutingMode = RoutingModeExceptRussia
	settings.FreeAccessEnabled = true
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update app settings failed: %v", err)
	}

	builder := NewConfigBuilderForStorage(storage)
	builder.SetRoutingMode(RoutingModeExceptRussia)
	if err := builder.BuildConfig(""); err != nil {
		t.Fatalf("BuildConfig except_russia failed: %v", err)
	}

	profile, err := storage.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile failed: %v", err)
	}
	config, err := storage.GetProfileConfig(profile.ID)
	if err != nil {
		t.Fatalf("get profile config failed: %v", err)
	}

	if final := getRouteFinal(config); final != SmartBypassGroupTag {
		t.Fatalf("route final = %q, want %q for foreign traffic bypass", final, SmartBypassGroupTag)
	}
	if !containsDomainSuffixRouteRule(config, "yandex.ru", "direct") {
		t.Fatal("except_russia must keep yandex.ru direct")
	}
	if containsDomainSuffixRouteRule(config, "telegram.org", "direct") {
		t.Fatal("telegram.org must not be routed direct in except_russia")
	}
	if containsDomainSuffixRouteRule(config, "telegram.org", ServiceBypassGroupTag("telegram")) {
		t.Fatal("telegram.org must not use a fake Telegram bypass group without subscription")
	}
}

func TestBuildConfigWithVLESSXHTTPUsesXrayBridge(t *testing.T) {
	basePath := t.TempDir()
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	filtersPath := filepath.Join(basePath, "bin", FiltersFolder)
	if err := os.MkdirAll(filtersPath, 0755); err != nil {
		t.Fatalf("create filters dir failed: %v", err)
	}
	for _, filter := range FilterFiles {
		if err := os.WriteFile(filepath.Join(filtersPath, filter.Name), []byte("test"), 0644); err != nil {
			t.Fatalf("write filter %s failed: %v", filter.Name, err)
		}
	}

	link := "vless://11111111-1111-1111-1111-111111111111@example.com:443?security=tls&type=xhttp&path=%2Fbridge&host=example.com&mode=auto&extra=%7B%22xPaddingBytes%22%3A%22100-1000%22%7D&sni=example.com&fp=chrome&alpn=h2#xhttp-test"
	builder := NewConfigBuilderForStorage(storage)
	if err := builder.BuildConfig(link); err != nil {
		t.Fatalf("BuildConfig with xhttp failed: %v", err)
	}

	profile, err := storage.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile failed: %v", err)
	}
	config, err := storage.GetProfileConfig(profile.ID)
	if err != nil {
		t.Fatalf("get profile config failed: %v", err)
	}
	if !containsOutboundType(config, "xhttp-test", "socks") {
		t.Fatalf("sing-box config does not expose xhttp bridge as socks outbound")
	}
	if !containsOutbound(config, ServiceBypassGroupTag("openai")) {
		t.Fatalf("generated config with subscription does not contain %q", ServiceBypassGroupTag("openai"))
	}
	if !containsDomainSuffixRouteRule(config, "openai.com", ServiceBypassGroupTag("openai")) {
		t.Fatalf("generated config with subscription does not route AI services through %q", ServiceBypassGroupTag("openai"))
	}
	aiCandidates := getOutboundCandidates(config, ServiceBypassGroupTag("openai"))
	if len(aiCandidates) != 1 || aiCandidates[0] != "auto-select" {
		t.Fatalf("%s candidates with xhttp subscription = %v, want only auto-select", ServiceBypassGroupTag("openai"), aiCandidates)
	}
	if !containsProcessDirectRule(config, XrayExeName) {
		t.Fatalf("generated xhttp config does not bypass %s process traffic directly", XrayExeName)
	}

	xrayConfig, err := storage.GetProfileXrayConfig(profile.ID)
	if err != nil {
		t.Fatalf("get xray config failed: %v", err)
	}
	if len(xrayConfig) == 0 {
		t.Fatal("xhttp profile did not generate xray bridge config")
	}
	if !containsXrayNetwork(xrayConfig, "xhttp") {
		t.Fatalf("xray bridge config does not contain xhttp outbound: %#v", xrayConfig)
	}
}

func TestTunRouteExcludesProxyAndXrayEndpoints(t *testing.T) {
	config := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":       "tun",
				"tag":        "tun-in",
				"auto_route": true,
			},
		},
		"outbounds": []interface{}{
			map[string]interface{}{
				"type":        "vless",
				"tag":         "vless-ip",
				"server":      "198.51.100.10",
				"server_port": 443,
				"tls": map[string]interface{}{
					"server_name": "198.51.100.11",
				},
			},
			map[string]interface{}{
				"type":        "socks",
				"tag":         "xhttp-bridge",
				"server":      "127.0.0.1",
				"server_port": 19081,
			},
		},
	}
	xrayConfig := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{
				"protocol": "vless",
				"settings": map[string]interface{}{
					"vnext": []interface{}{
						map[string]interface{}{
							"address": "203.0.113.10",
							"port":    443,
						},
					},
				},
				"streamSettings": map[string]interface{}{
					"tlsSettings": map[string]interface{}{
						"serverName": "203.0.113.11",
					},
					"xhttpSettings": map[string]interface{}{
						"host": "203.0.113.12",
					},
				},
			},
		},
	}

	addTunRouteExcludesForProxyEndpoints(config, xrayConfig)

	for _, cidr := range []string{
		"198.51.100.10/32",
		"198.51.100.11/32",
		"203.0.113.10/32",
		"203.0.113.11/32",
		"203.0.113.12/32",
	} {
		if !containsTunRouteExcludeAddress(config, cidr) {
			t.Fatalf("TUN route_exclude_address does not contain %s: %#v", cidr, getTunRouteExcludeAddresses(config))
		}
	}
	if containsTunRouteExcludeAddress(config, "127.0.0.1/32") {
		t.Fatalf("TUN route_exclude_address must not include loopback bridge address: %#v", getTunRouteExcludeAddresses(config))
	}
}

func TestSplitProxyConfigsFiltersNonVLESSXHTTP(t *testing.T) {
	result := SplitProxyConfigs([]ProxyConfig{
		{
			Type:    "vless",
			Network: "xhttp",
			Name:    "vless-xhttp",
			Server:  "vless.example",
		},
		{
			Type:    "trojan",
			Network: "xhttp",
			Name:    "trojan-xhttp",
			Server:  "trojan.example",
		},
		{
			Type:    "vmess",
			Network: "splithttp",
			Name:    "vmess-splithttp",
			Server:  "vmess.example",
		},
		{
			Type:    "trojan",
			Network: "tcp",
			Name:    "trojan-tcp",
			Server:  "tcp.example",
		},
	})

	if len(result.XrayBridge) != 1 || result.XrayBridge[0].Name != "vless-xhttp" {
		t.Fatalf("xray bridge proxies = %#v, want only vless-xhttp", result.XrayBridge)
	}
	if len(result.SingBox) != 1 || result.SingBox[0].Name != "trojan-tcp" {
		t.Fatalf("sing-box proxies = %#v, want only trojan-tcp", result.SingBox)
	}
	if len(result.Filtered) != 2 {
		t.Fatalf("filtered proxies = %#v, want trojan/vmess xhttp filtered", result.Filtered)
	}
	if result.AllFiltered {
		t.Fatal("mixed supported/filtered list must not be marked all-filtered")
	}
}

func TestRouteProbeRequiresEveryProbeTarget(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "probe dependency failed", http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()

	service := FreeAccessService{
		Tag:         "youtube",
		DisplayName: "YouTube",
		HealthURL:   server.URL + "/ok",
		ProbeURLs:   []string{server.URL + "/cdn"},
	}
	candidates := []routeProbeCandidate{
		{Tag: "byedpi", Label: "ByeDPI", Kind: "free", Client: server.Client(), Available: true},
	}

	result := (&App{}).probeServiceCandidates(service, candidates)
	if result.Success {
		t.Fatal("route probe accepted a candidate even though one service dependency failed")
	}
	if !strings.Contains(result.Error, "/cdn") {
		t.Fatalf("route probe error = %q, want failed target in message", result.Error)
	}
}

func TestConfigHasVPNProbeCandidatesIgnoresFreeOnlyConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "active_config.json")
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       "auto-select",
				"outbounds": []interface{}{"direct", "byedpi"},
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	hasVPN, err := configHasVPNProbeCandidates(configPath)
	if err != nil {
		t.Fatalf("configHasVPNProbeCandidates failed: %v", err)
	}
	if hasVPN {
		t.Fatal("free-only config must not be treated as having VPN candidates")
	}
}

func TestConfigHasVPNProbeCandidatesDetectsSubscriptionOutbound(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "active_config.json")
	config := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "socks", "tag": "byedpi"},
			map[string]interface{}{"type": "vless", "tag": "vless-fast", "server": "vpn.example"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       "auto-select",
				"outbounds": []interface{}{"byedpi", "vless-fast"},
			},
		},
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		t.Fatalf("write config failed: %v", err)
	}

	hasVPN, err := configHasVPNProbeCandidates(configPath)
	if err != nil {
		t.Fatalf("configHasVPNProbeCandidates failed: %v", err)
	}
	if !hasVPN {
		t.Fatal("config with VLESS auto-select candidate must be treated as having VPN candidates")
	}
}

type delayedProbeTransport struct {
	Delay time.Duration
}

func (t delayedProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	time.Sleep(t.Delay)
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}, nil
}

func expectedServiceTagForDomain(domain string) string {
	for _, svc := range DefaultFreeAccessServices {
		for _, suffix := range svc.DomainSuffixes {
			if suffix == domain {
				return svc.Tag
			}
		}
	}
	return ""
}

func containsOutbound(config map[string]interface{}, tag string) bool {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if ok && outboundMap["tag"] == tag {
			return true
		}
	}
	return false
}

func containsOutboundType(config map[string]interface{}, tag, outboundType string) bool {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if ok && outboundMap["tag"] == tag && outboundMap["type"] == outboundType {
			return true
		}
	}
	return false
}

func containsXrayNetwork(config map[string]interface{}, network string) bool {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		stream, _ := outboundMap["streamSettings"].(map[string]interface{})
		if stream != nil && stream["network"] == network {
			return true
		}
	}
	return false
}

func containsRouteOutbound(config map[string]interface{}, outboundTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["outbound"] != outboundTag {
			continue
		}
		if _, ok := ruleMap["domain_suffix"]; ok {
			return true
		}
	}
	return false
}

func getOutbound(config map[string]interface{}, tag string) map[string]interface{} {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if ok && outboundMap["tag"] == tag {
			return outboundMap
		}
	}
	return nil
}

func getOutboundCandidates(config map[string]interface{}, tag string) []string {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || outboundMap["tag"] != tag {
			continue
		}
		candidates, _ := outboundMap["outbounds"].([]string)
		if candidates != nil {
			return candidates
		}
		rawCandidates, _ := outboundMap["outbounds"].([]interface{})
		result := make([]string, 0, len(rawCandidates))
		for _, raw := range rawCandidates {
			if text, ok := raw.(string); ok {
				result = append(result, text)
			}
		}
		return result
	}
	return nil
}

func containsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func containsPathFold(items []string, value string) bool {
	expected, err := filepath.Abs(value)
	if err != nil {
		expected = value
	}
	expected = filepath.Clean(expected)
	for _, item := range items {
		actual, err := filepath.Abs(item)
		if err != nil {
			actual = item
		}
		if strings.EqualFold(filepath.Clean(actual), expected) {
			return true
		}
	}
	return false
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, item := range a {
		if !containsString(b, item) {
			return false
		}
	}
	return true
}

func containsProcessDirectRule(config map[string]interface{}, processName string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		if valuesContain(ruleMap["process_name"], processName) && ruleMap["outbound"] == "direct" {
			return true
		}
	}
	return false
}

func containsProcessRouteRule(config map[string]interface{}, processName, outboundTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["outbound"] != outboundTag {
			continue
		}
		if valuesContain(ruleMap["process_name"], processName) {
			return true
		}
	}
	return false
}

func getTunRouteExcludeAddresses(config map[string]interface{}) []string {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		return interfaceStringSlice(inboundMap["route_exclude_address"])
	}
	return nil
}

func containsTunRouteExcludeAddress(config map[string]interface{}, cidr string) bool {
	return containsString(getTunRouteExcludeAddresses(config), cidr)
}

func containsIPRouteRule(config map[string]interface{}, ipCIDR, outboundTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["outbound"] != outboundTag {
			continue
		}
		if valuesContain(ruleMap["ip_cidr"], ipCIDR) {
			return true
		}
	}
	return false
}

func containsDomainSuffixRouteRule(config map[string]interface{}, suffix, outboundTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["outbound"] != outboundTag {
			continue
		}
		if valuesContain(ruleMap["domain_suffix"], suffix) {
			return true
		}
	}
	return false
}

func containsDNSDomainSuffixServer(config map[string]interface{}, suffix, serverTag string) bool {
	dns, _ := config["dns"].(map[string]interface{})
	rules, _ := dns["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["server"] != serverTag {
			continue
		}
		if valuesContain(ruleMap["domain_suffix"], suffix) {
			return true
		}
	}
	return false
}

func routeRuleBeforeRuleSet(config map[string]interface{}, key, value, ruleSetTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	valueIndex := -1
	ruleSetIndex := -1
	for i, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		if valueIndex == -1 && valuesContain(ruleMap[key], value) {
			valueIndex = i
		}
		if ruleSetIndex == -1 && valuesContain(ruleMap["rule_set"], ruleSetTag) {
			ruleSetIndex = i
		}
	}
	return valueIndex >= 0 && ruleSetIndex >= 0 && valueIndex < ruleSetIndex
}

func containsRouteRuleSet(config map[string]interface{}, ruleSetTag string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			continue
		}
		if valuesContain(ruleMap["rule_set"], ruleSetTag) {
			return true
		}
	}
	return false
}

func getDNSFinal(config map[string]interface{}) string {
	dns, _ := config["dns"].(map[string]interface{})
	final, _ := dns["final"].(string)
	return final
}

func getRouteFinal(config map[string]interface{}) string {
	route, _ := config["route"].(map[string]interface{})
	final, _ := route["final"].(string)
	return final
}

func getDNSStrategy(config map[string]interface{}) string {
	dns, _ := config["dns"].(map[string]interface{})
	strategy, _ := dns["strategy"].(string)
	return strategy
}

func getDefaultDomainResolverStrategy(config map[string]interface{}) string {
	route, _ := config["route"].(map[string]interface{})
	resolver, _ := route["default_domain_resolver"].(map[string]interface{})
	strategy, _ := resolver["strategy"].(string)
	return strategy
}

func getTunStrictRoute(config map[string]interface{}) bool {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		strict, _ := inboundMap["strict_route"].(bool)
		return strict
	}
	return false
}

func getTunAddresses(config map[string]interface{}) []string {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		return normalizeStringListForTest(inboundMap["address"])
	}
	return nil
}

func normalizeStringListForTest(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []string:
		return append([]string(nil), typed...)
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func containsIPv6Address(values []string) bool {
	for _, value := range values {
		if isIPv6AddressOrCIDR(value) {
			return true
		}
	}
	return false
}

func getMixedInboundPort(config map[string]interface{}) int {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}
		return mixedInboundPort(inboundMap["listen_port"])
	}
	return 0
}

func valuesContain(value interface{}, needle string) bool {
	switch typed := value.(type) {
	case string:
		return typed == needle
	case []string:
		return containsString(typed, needle)
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok && text == needle {
				return true
			}
		}
	}
	return false
}
