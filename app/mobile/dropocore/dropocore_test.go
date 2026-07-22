package dropocore

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAndroidCoreLifecycle(t *testing.T) {
	mu.Lock()
	current = defaultState()
	mu.Unlock()

	if ok := decodeSuccess(EnsureStarted(t.TempDir(), "2.1.3")); !ok {
		t.Fatal("EnsureStarted success = false")
	}
	if ok := decodeSuccess(SetConnected(true)); !ok {
		t.Fatal("SetConnected(true) success = false")
	}

	var status map[string]interface{}
	if err := json.Unmarshal([]byte(Status()), &status); err != nil {
		t.Fatal(err)
	}
	if status["connected"] != true {
		t.Fatalf("connected = %v, want true", status["connected"])
	}
	if status["networkMode"] != "android_vpn" {
		t.Fatalf("networkMode = %v, want android_vpn", status["networkMode"])
	}
}

func TestAndroidCoreSubscriptionCall(t *testing.T) {
	mu.Lock()
	current = defaultState()
	mu.Unlock()

	args := `["vless://example"]`
	if ok := decodeSuccess(Call("SetVPNSubscription", args)); !ok {
		t.Fatal("SetVPNSubscription success = false")
	}

	var sub map[string]interface{}
	if err := json.Unmarshal([]byte(Call("GetCurrentSubscription", "[]")), &sub); err != nil {
		t.Fatal(err)
	}
	if sub["hasSubscription"] != true {
		t.Fatalf("hasSubscription = %v, want true", sub["hasSubscription"])
	}
	if sub["proxyCount"].(float64) != 1 {
		t.Fatalf("proxyCount = %v, want 1", sub["proxyCount"])
	}
}

func TestSubscriptionSummaryDoesNotLeakCredentials(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "subscription URL",
			value: "https://api.example.test/sub/secret-token?user=private",
			want:  "https://[redacted]",
		},
		{
			name:  "direct proxy URL",
			value: "vless://secret-user@example.test:443?security=tls#private-name",
			want:  "vless://[redacted]",
		},
		{
			name:  "encoded subscription",
			value: "c2VjcmV0LXN1YnNjcmlwdGlvbi1wYXlsb2Fk",
			want:  "[redacted]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := subscriptionSummary(tt.value); got != tt.want {
				t.Fatalf("subscriptionSummary() = %q, want %q", got, tt.want)
			}
			for _, secret := range []string{"api.example.test", "example.test", "secret-token", "secret-user", "private", "c2VjcmV0"} {
				if strings.Contains(subscriptionSummary(tt.value), secret) {
					t.Fatalf("subscriptionSummary() leaked %q", secret)
				}
			}
		})
	}
}

func TestProxyListSummaryDoesNotLeakServers(t *testing.T) {
	proxies := []proxyConfig{
		{Type: "vless", Network: "ws", Server: "secret.example.test", ServerPort: 443},
		{Type: "trojan", Server: "192.0.2.10", ServerPort: 8443},
	}
	got := proxyListSummary(proxies)
	if got != "vless/ws, trojan" {
		t.Fatalf("proxyListSummary() = %q", got)
	}
	for _, secret := range []string{"secret.example.test", "192.0.2.10", "443", "8443"} {
		if strings.Contains(got, secret) {
			t.Fatalf("proxyListSummary() leaked %q", secret)
		}
	}
}

func TestBuildSingBoxConfigForDirectVLESS(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	mu.Unlock()

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(BuildSingBoxConfig()), &response); err != nil {
		t.Fatal(err)
	}
	if response["success"] != true {
		t.Fatalf("success = %v, error = %v", response["success"], response["error"])
	}
	configText, ok := response["config"].(string)
	if !ok || configText == "" {
		t.Fatalf("config missing: %#v", response["config"])
	}

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(configText, `"type": "tun"`) {
		t.Fatal("config does not contain tun inbound")
	}
	if !strings.Contains(configText, `"type": "vless"`) {
		t.Fatal("config does not contain vless outbound")
	}
	if !strings.Contains(configText, `"type": "ws"`) {
		t.Fatal("config does not contain websocket transport")
	}
}

func TestAndroidBlockedOnlyRoutesOnlyBlockedServicesThroughVPN(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	mu.Unlock()

	config := buildConfigForTest(t)
	route := config["route"].(map[string]interface{})
	if final := route["final"]; final != "direct" {
		t.Fatalf("route final = %v, want direct", final)
	}
	if !androidContainsDomainRoute(config, "instagram.com", "proxy") {
		t.Fatal("instagram.com must route through proxy by default")
	}
	if !androidContainsPackageRoute(config, "com.discord", "proxy") {
		t.Fatal("Discord package traffic, including IP-only voice UDP, must route through proxy by default")
	}
	if !androidContainsPackageRoute(config, "org.telegram.messenger", "proxy") {
		t.Fatal("Telegram package traffic, including IP-only endpoints, must route through proxy by default")
	}
	if !androidContainsDNSServer(config, "instagram.com", "dns-remote") {
		t.Fatal("instagram.com must resolve through remote DNS by default")
	}
	if !androidContainsDNSServer(config, "yandex.ru", "dns-direct") {
		t.Fatal("yandex.ru must resolve through direct DNS in blocked-only mode")
	}
	dns := config["dns"].(map[string]interface{})
	if dns["final"] != "dns-direct" {
		t.Fatalf("dns final = %v, want dns-direct", dns["final"])
	}
	defaultResolver := route["default_domain_resolver"].(map[string]interface{})
	if defaultResolver["server"] != "dns-direct" {
		t.Fatalf("default_domain_resolver = %v, want dns-direct", defaultResolver["server"])
	}
	dnsDirect := androidDNSServerByTag(config, "dns-direct")
	if _, ok := dnsDirect["detour"]; ok {
		t.Fatalf("dns-direct must not set detour: %#v", dnsDirect)
	}
	dnsRemote := androidDNSServerByTag(config, "dns-remote")
	if dnsRemote["detour"] != "proxy" {
		t.Fatalf("dns-remote detour = %v, want proxy", dnsRemote["detour"])
	}
	if androidContainsAnyDomainSuffix(config, "2ip.io") {
		t.Fatal("2ip.io must not be part of Android blocked-service rules")
	}
}

func TestAndroidRoutePolicyCanForceBlockedServiceDirect(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	current.RoutePolicies = map[string]string{"meta": "direct"}
	mu.Unlock()

	config := buildConfigForTest(t)
	if !androidContainsDomainRoute(config, "instagram.com", "direct") {
		t.Fatal("instagram.com must route direct when meta policy is direct")
	}
	if androidContainsDomainRoute(config, "instagram.com", "proxy") {
		t.Fatal("instagram.com must not route through proxy when meta policy is direct")
	}
	if !androidContainsDNSServer(config, "instagram.com", "dns-direct") {
		t.Fatal("instagram.com must use direct DNS when meta policy is direct")
	}
}

func TestAndroidDiscordRoutePolicyAlsoControlsIPOnlyVoiceTraffic(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	current.RoutePolicies = map[string]string{"discord": "direct"}
	mu.Unlock()

	config := buildConfigForTest(t)
	if !androidContainsPackageRoute(config, "com.discord", "direct") {
		t.Fatal("Discord package traffic must route direct when Discord policy is direct")
	}
	if androidContainsPackageRoute(config, "com.discord", "proxy") {
		t.Fatal("Discord package traffic must not route through proxy when Discord policy is direct")
	}
}

func TestAndroidHideRuTrafficRoutesRuDomainsThroughVPN(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	current.Config.HideRuTraffic = true
	mu.Unlock()

	config := buildConfigForTest(t)
	if !androidContainsDomainRoute(config, "yandex.ru", "proxy") {
		t.Fatal("yandex.ru must route through proxy when HideRuTraffic is enabled")
	}
	if !androidContainsDNSServer(config, "yandex.ru", "dns-remote") {
		t.Fatal("yandex.ru must resolve through remote DNS when HideRuTraffic is enabled")
	}
}

func TestAndroidHideRuTrafficCanUseDedicatedProxy(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#main"
	current.Config.HideRuTraffic = true
	current.Config.RuProxyAddress = "vless://11111111-1111-1111-1111-111111111111@ru.example.com:443?security=tls&type=ws&path=%2Fws&host=ru.example.com&sni=ru.example.com&fp=chrome#ru"
	mu.Unlock()

	config := buildConfigForTest(t)
	if !androidContainsDomainRoute(config, "yandex.ru", "ru-proxy-1") {
		t.Fatal("yandex.ru must route through dedicated RU proxy when configured")
	}
	if !androidContainsOutbound(config, "ru-proxy-1") {
		t.Fatal("dedicated RU proxy outbound is missing")
	}
}

func TestAndroidSaveConfigAffectsLogLevelAndInvalidatesCache(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	current.CachedSingBoxConfig = `{"old":true}`
	current.CachedConfigSubscription = current.Subscription
	current.CachedConfigSignature = androidConfigSignature(current.Subscription, current.Config.EnableLogging, current.Config.LogLevel, current.Config.RoutingMode, current.Config.HideRuTraffic, current.Config.RuProxyAddress, nil)
	mu.Unlock()

	if ok := decodeSuccess(Call("SaveAppConfig", `[false,false,true,true,true,"dark","ru","debug",24]`)); !ok {
		t.Fatal("SaveAppConfig success = false")
	}
	mu.Lock()
	if current.CachedSingBoxConfig != "" {
		t.Fatal("cached config must be cleared when effective log level changes")
	}
	mu.Unlock()

	config := buildConfigForTest(t)
	logConfig := config["log"].(map[string]interface{})
	if logConfig["level"] != "error" {
		t.Fatalf("log level = %v, want error when logging is disabled", logConfig["level"])
	}
}

func TestAndroidSaveConfigRejectsInvalidValuesAndLiveLoggingChange(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	original := current.Config
	mu.Unlock()

	invalid := []string{
		`[false,true,true,true,true,"neon","ru","info",24]`,
		`[false,true,true,true,true,"system","en","info",24]`,
		`[false,true,true,true,true,"system","ru","verbose",24]`,
		`[false,true,true,true,true,"system","ru","info",0]`,
	}
	for _, args := range invalid {
		if ok := decodeSuccess(Call("SaveAppConfig", args)); ok {
			t.Fatalf("SaveAppConfig accepted invalid args %s", args)
		}
	}

	mu.Lock()
	if current.Config != original {
		t.Fatalf("invalid settings mutated config: got %#v, want %#v", current.Config, original)
	}
	current.Connected = true
	mu.Unlock()
	if ok := decodeSuccess(Call("SaveAppConfig", `[false,false,true,true,true,"system","ru","info",24]`)); ok {
		t.Fatal("SaveAppConfig changed logging while VPN was connected")
	}
}

func TestAndroidAutoUpdateDisabledReusesMatchingCache(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "https://offline.invalid/sub"
	current.Config.AutoUpdateSub = false
	current.CachedSingBoxConfig = `{"cached":true}`
	current.CachedProxyCount = 2
	current.CachedConfigSubscription = current.Subscription
	current.CachedConfigSignature = androidConfigSignature(current.Subscription, current.Config.EnableLogging, current.Config.LogLevel, current.Config.RoutingMode, current.Config.HideRuTraffic, current.Config.RuProxyAddress, nil)
	mu.Unlock()

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(BuildSingBoxConfig()), &response); err != nil {
		t.Fatal(err)
	}
	if response["success"] != true || response["cached"] != true {
		t.Fatalf("response = %#v, want cached success", response)
	}
	if response["config"] != `{"cached":true}` {
		t.Fatalf("config = %v, want cached config", response["config"])
	}
}

func TestAndroidRuntimeSettingsValidateAndInvalidateCache(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	current.CachedSingBoxConfig = `{"old":true}`
	current.CachedConfigSubscription = current.Subscription
	current.CachedConfigSignature = androidConfigSignature(current.Subscription, current.Config.EnableLogging, current.Config.LogLevel, current.Config.RoutingMode, current.Config.HideRuTraffic, current.Config.RuProxyAddress, nil)
	mu.Unlock()

	if ok := decodeSuccess(Call("SetRoutingMode", `["invalid"]`)); ok {
		t.Fatal("SetRoutingMode accepted an invalid mode")
	}
	if ok := decodeSuccess(Call("SetRoutingMode", `["all_traffic"]`)); !ok {
		t.Fatal("SetRoutingMode(all_traffic) success = false")
	}
	mu.Lock()
	if current.CachedSingBoxConfig != "" {
		t.Fatal("cached config must be cleared after routing mode change")
	}
	mu.Unlock()
	config := buildConfigForTest(t)
	route := config["route"].(map[string]interface{})
	if route["final"] != "proxy" {
		t.Fatalf("all_traffic final = %v, want proxy", route["final"])
	}
	dns := config["dns"].(map[string]interface{})
	if dns["final"] != "dns-remote" {
		t.Fatalf("all_traffic dns final = %v, want dns-remote", dns["final"])
	}
}

func TestAndroidRuntimeRouteChangesAreRejectedWhileConnected(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Connected = true
	mu.Unlock()

	for method, args := range map[string]string{
		"SetRoutingMode":             `["all_traffic"]`,
		"SetHideRuTraffic":           `[true,""]`,
		"SetAndroidRoutePolicy":      `["meta","direct"]`,
		"SetFreeAccessServiceMethod": `["meta","direct"]`,
	} {
		if ok := decodeSuccess(Call(method, args)); ok {
			t.Fatalf("%s unexpectedly succeeded while connected", method)
		}
	}
}

func TestAndroidVersionCompareAndAssetSelection(t *testing.T) {
	if compareAndroidVersions("2.1.4", "2.1.3") <= 0 {
		t.Fatal("2.1.4 must be newer than 2.1.3")
	}
	if compareAndroidVersions("v2.1.3", "2.1.3") != 0 {
		t.Fatal("v2.1.3 must equal 2.1.3")
	}
	release := androidGitHubRelease{}
	release.Assets = append(release.Assets, struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	}{Name: "dropo-Windows.zip", BrowserDownloadURL: "windows", Size: 10})
	release.Assets = append(release.Assets, struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Size               int64  `json:"size"`
	}{Name: "dropo-Android-arm64.apk", BrowserDownloadURL: "android", Size: 20})
	name, url, size := androidUpdateAsset(release)
	if name != "dropo-Android-arm64.apk" || url != "android" || size != 20 {
		t.Fatalf("asset = %q %q %d, want Android APK", name, url, size)
	}
}

func TestAndroidGeneratedConfigAcceptedBySingBox(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("local sing-box check uses the bundled Windows binary")
	}
	exe := filepath.Clean(`..\..\..\dependencies\sing-box-v1.13.14\windows-amd64\sing-box-1.13.14-windows-amd64\sing-box.exe`)
	if _, err := os.Stat(exe); err != nil {
		t.Skipf("sing-box binary is not available: %v", err)
	}

	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls&type=ws&path=%2Fws&host=example.com&sni=example.com&fp=chrome#demo"
	mu.Unlock()

	config := buildConfigForTest(t)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "android-sing-box.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(exe, "check", "-c", path).CombinedOutput()
	if err != nil {
		t.Fatalf("sing-box check failed: %v\n%s", err, output)
	}
}

func TestBuildSingBoxConfigRejectsEmptySubscription(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	mu.Unlock()

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(BuildSingBoxConfig()), &response); err != nil {
		t.Fatal(err)
	}
	if response["success"] != false {
		t.Fatalf("success = %v, want false", response["success"])
	}
}

func TestAndroidEngineErrorIsPersistedInStatusAndLogs(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	mu.Unlock()

	raw := Call("AndroidEngineError", `["check config failed: bad vless"]`)
	if ok := decodeSuccess(raw); ok {
		t.Fatal("AndroidEngineError success = true, want false")
	}

	var status map[string]interface{}
	if err := json.Unmarshal([]byte(Status()), &status); err != nil {
		t.Fatal(err)
	}
	if status["hasError"] != true {
		t.Fatalf("hasError = %v, want true", status["hasError"])
	}
	if !strings.Contains(status["error"].(string), "bad vless") {
		t.Fatalf("error = %q, want bad vless", status["error"])
	}

	var logs map[string]interface{}
	if err := json.Unmarshal([]byte(Logs()), &logs); err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, item := range logs["logs"].([]interface{}) {
		text += item.(string) + "\n"
	}
	if !strings.Contains(text, "android engine error: check config failed: bad vless") {
		t.Fatalf("logs do not contain engine error:\n%s", text)
	}
}

func TestAndroidServiceStatesAreExposedInStatus(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	mu.Unlock()

	if ok := decodeSuccess(Call("AndroidServiceState", `["starting","booting",""]`)); !ok {
		t.Fatal("AndroidServiceState(starting) success = false")
	}

	var status map[string]interface{}
	if err := json.Unmarshal([]byte(Status()), &status); err != nil {
		t.Fatal(err)
	}
	if status["vpnState"] != "starting" {
		t.Fatalf("vpnState = %v, want starting", status["vpnState"])
	}
	if status["connecting"] != true {
		t.Fatalf("connecting = %v, want true", status["connecting"])
	}
	if status["connected"] == true {
		t.Fatal("connected = true while starting")
	}

	if ok := decodeSuccess(Call("AndroidServiceState", `["connected","ready",""]`)); !ok {
		t.Fatal("AndroidServiceState(connected) success = false")
	}
	if err := json.Unmarshal([]byte(Status()), &status); err != nil {
		t.Fatal(err)
	}
	if status["connected"] != true || status["vpnState"] != "connected" {
		t.Fatalf("connected status mismatch: %#v", status)
	}
}

func TestBuildSingBoxConfigFallsBackToMatchingCache(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "ftp://offline.example/sub"
	current.CachedConfigSubscription = current.Subscription
	current.CachedConfigSignature = androidConfigSignature(current.Subscription, current.Config.EnableLogging, current.Config.LogLevel, current.Config.RoutingMode, current.Config.HideRuTraffic, current.Config.RuProxyAddress, nil)
	current.CachedSingBoxConfig = `{"log":{"level":"info"},"inbounds":[],"outbounds":[{"type":"direct","tag":"direct"}]}`
	current.CachedProxyCount = 1
	mu.Unlock()

	var response map[string]interface{}
	if err := json.Unmarshal([]byte(BuildSingBoxConfig()), &response); err != nil {
		t.Fatal(err)
	}
	if response["success"] != true {
		t.Fatalf("success = %v, error = %v", response["success"], response["error"])
	}
	if response["cached"] != true {
		t.Fatalf("cached = %v, want true", response["cached"])
	}
	if response["config"] == "" {
		t.Fatal("cached config missing")
	}
}

func TestAndroidDiagnosticsIncludesCacheAndState(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	current.Subscription = "vless://example"
	current.CachedSingBoxConfig = "{}"
	current.CachedProxyCount = 2
	current.CachedConfigSubscription = current.Subscription
	current.ServiceState = "connected"
	mu.Unlock()

	var diagnostics map[string]interface{}
	if err := json.Unmarshal([]byte(Call("AndroidDiagnostics", "[]")), &diagnostics); err != nil {
		t.Fatal(err)
	}
	text := diagnostics["text"].(string)
	if !strings.Contains(text, "serviceState: connected") {
		t.Fatalf("diagnostics missing service state:\n%s", text)
	}
	if !strings.Contains(text, "cachedConfig: true") {
		t.Fatalf("diagnostics missing cache summary:\n%s", text)
	}
}

func TestAndroidRoutesExposeDirectOrVPNMethods(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	mu.Unlock()

	var config map[string]interface{}
	if err := json.Unmarshal([]byte(Call("GetFreeAccessConfig", "[]")), &config); err != nil {
		t.Fatal(err)
	}
	options, ok := config["methodOptions"].([]interface{})
	if !ok || len(options) != 2 {
		t.Fatalf("methodOptions = %#v, want direct/vpn options", config["methodOptions"])
	}

	services, ok := config["services"].([]interface{})
	if !ok || len(services) == 0 {
		t.Fatalf("services = %#v", config["services"])
	}
	if got, want := len(services), len(androidServiceCatalog()); got != want {
		t.Fatalf("services count = %d, want %d", got, want)
	}
	for _, raw := range services {
		service := raw.(map[string]interface{})
		if service["effectiveMethodLabel"] != androidRoutePolicyLabel(androidRoutePolicyVPN) {
			t.Fatalf("default method label for %v = %v, want VPN", service["tag"], service["effectiveMethodLabel"])
		}
	}

	if ok := decodeSuccess(Call("SetAndroidRoutePolicy", `["meta","direct"]`)); !ok {
		t.Fatal("SetAndroidRoutePolicy success = false")
	}
	if err := json.Unmarshal([]byte(Call("GetFreeAccessConfig", "[]")), &config); err != nil {
		t.Fatal(err)
	}
	cache := config["methodCache"].(map[string]interface{})
	if cache["meta"] != androidRoutePolicyDirect {
		t.Fatalf("meta method cache = %v, want direct", cache["meta"])
	}
	if cache["youtube"] != androidRoutePolicyVPN {
		t.Fatalf("youtube method cache = %v, want vpn", cache["youtube"])
	}
}

func TestAndroidWireGuardCRUD(t *testing.T) {
	mu.Lock()
	current = defaultState()
	current.BasePath = t.TempDir()
	mu.Unlock()

	config := `[Interface]
PrivateKey = private-key
Address = 10.7.0.2/32
DNS = 10.7.0.1
MTU = 1280

[Peer]
PublicKey = public-key
AllowedIPs = 10.7.0.0/24, 10.8.0.0/24
Endpoint = vpn.example.com:51820
PersistentKeepalive = 25`

	args, err := json.Marshal([]interface{}{"office", "Office", config})
	if err != nil {
		t.Fatal(err)
	}
	if ok := decodeSuccess(Call("AddWireGuard", string(args))); !ok {
		t.Fatal("AddWireGuard success = false")
	}

	var list map[string]interface{}
	if err := json.Unmarshal([]byte(Call("GetWireGuardList", "[]")), &list); err != nil {
		t.Fatal(err)
	}
	if list["count"].(float64) != 1 {
		t.Fatalf("count = %v, want 1", list["count"])
	}

	var item map[string]interface{}
	if err := json.Unmarshal([]byte(Call("GetWireGuardConfig", `["office"]`)), &item); err != nil {
		t.Fatal(err)
	}
	if item["endpoint"] != "vpn.example.com:51820" {
		t.Fatalf("endpoint = %v", item["endpoint"])
	}

	if ok := decodeSuccess(Call("DeleteWireGuard", `["office"]`)); !ok {
		t.Fatal("DeleteWireGuard success = false")
	}
	if err := json.Unmarshal([]byte(Call("GetWireGuardList", "[]")), &list); err != nil {
		t.Fatal(err)
	}
	if list["count"].(float64) != 0 {
		t.Fatalf("count after delete = %v, want 0", list["count"])
	}
}

func decodeSuccess(raw string) bool {
	var data map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return false
	}
	return data["success"] != false
}

func buildConfigForTest(t *testing.T) map[string]interface{} {
	t.Helper()
	var response map[string]interface{}
	if err := json.Unmarshal([]byte(BuildSingBoxConfig()), &response); err != nil {
		t.Fatal(err)
	}
	if response["success"] != true {
		t.Fatalf("success = %v, error = %v", response["success"], response["error"])
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(response["config"].(string)), &config); err != nil {
		t.Fatal(err)
	}
	return config
}

func androidContainsDomainRoute(config map[string]interface{}, suffix, outbound string) bool {
	route, _ := config["route"].(map[string]interface{})
	rules, _ := route["rules"].([]interface{})
	for _, raw := range rules {
		rule, _ := raw.(map[string]interface{})
		if rule["outbound"] != outbound {
			continue
		}
		if stringListContains(rule["domain_suffix"], suffix) {
			return true
		}
	}
	return false
}

func androidContainsPackageRoute(config map[string]interface{}, packageName, outbound string) bool {
	route := config["route"].(map[string]interface{})
	for _, raw := range route["rules"].([]interface{}) {
		rule := raw.(map[string]interface{})
		if rule["outbound"] != outbound {
			continue
		}
		if stringListContains(rule["package_name"], packageName) {
			return true
		}
	}
	return false
}

func androidContainsDNSServer(config map[string]interface{}, suffix, server string) bool {
	dns, _ := config["dns"].(map[string]interface{})
	rules, _ := dns["rules"].([]interface{})
	for _, raw := range rules {
		rule, _ := raw.(map[string]interface{})
		if rule["server"] != server {
			continue
		}
		if stringListContains(rule["domain_suffix"], suffix) {
			return true
		}
	}
	return false
}

func androidDNSServerByTag(config map[string]interface{}, tag string) map[string]interface{} {
	dns, _ := config["dns"].(map[string]interface{})
	servers, _ := dns["servers"].([]interface{})
	for _, raw := range servers {
		server, _ := raw.(map[string]interface{})
		if server["tag"] == tag {
			return server
		}
	}
	return nil
}

func androidContainsAnyDomainSuffix(config map[string]interface{}, suffix string) bool {
	route, _ := config["route"].(map[string]interface{})
	if rules, _ := route["rules"].([]interface{}); stringListRulesContain(rules, suffix) {
		return true
	}
	dns, _ := config["dns"].(map[string]interface{})
	rules, _ := dns["rules"].([]interface{})
	return stringListRulesContain(rules, suffix)
}

func androidContainsOutbound(config map[string]interface{}, tag string) bool {
	outbounds, _ := config["outbounds"].([]interface{})
	for _, raw := range outbounds {
		outbound, _ := raw.(map[string]interface{})
		if outbound["tag"] == tag {
			return true
		}
	}
	return false
}

func stringListRulesContain(rules []interface{}, suffix string) bool {
	for _, raw := range rules {
		rule, _ := raw.(map[string]interface{})
		if stringListContains(rule["domain_suffix"], suffix) {
			return true
		}
	}
	return false
}

func stringListContains(raw interface{}, value string) bool {
	switch typed := raw.(type) {
	case []interface{}:
		for _, item := range typed {
			if item == value {
				return true
			}
		}
	case []string:
		for _, item := range typed {
			if item == value {
				return true
			}
		}
	}
	return false
}
