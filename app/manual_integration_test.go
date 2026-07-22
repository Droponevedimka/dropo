package main

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestManualSubscriptionURLFromEnv(t *testing.T) {
	subscriptionURL := os.Getenv("DROPO_TEST_SUBSCRIPTION_URL")
	if subscriptionURL == "" {
		t.Skip("DROPO_TEST_SUBSCRIPTION_URL is not set")
	}

	builder := NewConfigBuilder(t.TempDir())
	result, err := builder.TestSubscription(subscriptionURL)
	if err != nil {
		t.Fatalf("TestSubscription returned error: %v", err)
	}
	if result == nil || !result.Success {
		if result != nil {
			t.Fatalf("subscription validation failed: %s", result.Error)
		}
		t.Fatal("subscription validation failed without a result")
	}
	if result.Count == 0 {
		t.Fatal("subscription validation succeeded with zero proxies")
	}
}

func TestManualWireGuardConfigFromEnv(t *testing.T) {
	configText := os.Getenv("DROPO_TEST_WG_CONFIG")
	if configText == "" {
		t.Skip("DROPO_TEST_WG_CONFIG is not set")
	}

	config, err := ParseWireGuardConfig(configText)
	if err != nil {
		t.Fatalf("ParseWireGuardConfig returned error: %v", err)
	}
	if config.PrivateKey == "" {
		t.Fatal("missing interface private key")
	}
	if config.PublicKey == "" {
		t.Fatal("missing peer public key")
	}
	if config.Endpoint == "" {
		t.Fatal("missing peer endpoint")
	}
	if len(config.LocalAddress) == 0 {
		t.Fatal("missing interface address")
	}
}

func TestManualFreeAccessRuntimeFromEnv(t *testing.T) {
	basePath := os.Getenv("DROPO_TEST_FREE_ACCESS_BASE")
	if basePath == "" {
		t.Skip("DROPO_TEST_FREE_ACCESS_BASE is not set")
	}

	singboxPath := filepath.Join(basePath, "bin", "sing-box.exe")
	if !fileExists(singboxPath) {
		t.Fatalf("sing-box not found: %s", singboxPath)
	}

	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	settings := storage.GetAppSettings()
	settings.FreeAccessEnabled = true
	settings.RoutingMode = RoutingModeBlockedOnly
	if settings.FreeAccessServices == nil {
		settings.FreeAccessServices = DefaultFreeAccessServiceState()
	}
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update app settings failed: %v", err)
	}

	filterManager := NewFilterManager(basePath)
	updated, err := filterManager.UpdateRefilters()
	if err != nil {
		t.Fatalf("routing database update failed: %v", err)
	}
	t.Logf("routing database update finished, files updated: %d", updated)

	builder := NewConfigBuilderForStorage(storage)
	builder.SetRoutingMode(RoutingModeBlockedOnly)
	if err := builder.BuildConfig(""); err != nil {
		t.Fatalf("free-access-only config build failed: %v", err)
	}

	configPath, err := storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read active config failed: %v", err)
	}
	if !bytes.Contains(configData, []byte(ByeDPIOutboundTag)) ||
		!bytes.Contains(configData, []byte(SmartBypassGroupTag)) ||
		!bytes.Contains(configData, []byte("youtube.com")) ||
		!bytes.Contains(configData, []byte("discord.com")) {
		t.Fatalf("active config does not contain expected free-access routes")
	}

	if output, err := newBackgroundCommand(singboxPath, "check", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("sing-box config check failed: %v\n%s", err, output)
	}

	byeDPI := NewByeDPIManager(basePath, func(msg string) { t.Log(msg) })
	if err := byeDPI.Start(); err != nil {
		t.Fatalf("ByeDPI start failed: %v", err)
	}
	defer byeDPI.Stop()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(singboxPath, "run", "-c", configPath)
	cmd.Dir = storage.GetResourcesPath()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("sing-box start failed: %v", err)
	}
	defer stopProcessTree(cmd.Process.Pid)

	clashURL := builder.clashAPI.baseURL() + "/proxies"
	if !waitForHTTP(clashURL, builder.clashAPI.secret, 12*time.Second) {
		stopProcessTree(cmd.Process.Pid)
		t.Fatalf("clash api did not become ready\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	proxiesBody, err := httpGetBody(clashURL, builder.clashAPI.secret)
	if err != nil {
		t.Fatalf("failed to query proxies: %v", err)
	}
	if !strings.Contains(proxiesBody, SmartBypassGroupTag) ||
		!strings.Contains(proxiesBody, ServiceBypassGroupTag("telegram")) ||
		!strings.Contains(proxiesBody, ByeDPIOutboundTag) {
		t.Fatalf("clash api does not expose expected free-access outbounds: %s", proxiesBody)
	}

	for _, target := range []string{
		"https://discord.com",
		"https://www.youtube.com/generate_204",
		"https://www.gstatic.com/generate_204",
	} {
		if err := checkHTTPSReachable(target); err != nil {
			t.Fatalf("%s is not reachable while free access is running: %v", target, err)
		}
	}
}

// TestManualNativeRuntimeFromEnv validates the self-contained Windows runtime
// without starting a deprecated helper process. The deeper live smoke below
// additionally opens WinDivert and exercises lifecycle cleanup.
func TestManualNativeRuntimeFromEnv(t *testing.T) {
	basePath := os.Getenv("DROPO_TEST_FREE_ACCESS_BASE")
	if basePath == "" {
		t.Skip("DROPO_TEST_FREE_ACCESS_BASE is not set")
	}
	requireManualLiveRuntimeFiles(t, basePath)
	manager := NewNativeTrafficManager(basePath, func(message string) { t.Log(message) })
	if !manager.IsInstalled() {
		t.Fatal("native WinDivert runtime was not detected in the release bundle")
	}
	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}
	builder := NewConfigBuilderForStorage(storage)
	if err := builder.BuildConfig(""); err != nil {
		t.Fatalf("native free-access config build failed: %v", err)
	}
	configPath, err := storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	if output, err := newBackgroundCommand(filepath.Join(basePath, "bin", "sing-box.exe"), "check", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("sing-box config check failed: %v\n%s", err, output)
	}
}

func TestManualSubscriptionRuntimeFromEnv(t *testing.T) {
	basePath := os.Getenv("DROPO_TEST_FREE_ACCESS_BASE")
	subscriptionURL := os.Getenv("DROPO_TEST_SUBSCRIPTION_URL")
	if basePath == "" || subscriptionURL == "" {
		t.Skip("DROPO_TEST_FREE_ACCESS_BASE and DROPO_TEST_SUBSCRIPTION_URL must be set")
	}

	singboxPath := filepath.Join(basePath, "bin", "sing-box.exe")
	if !fileExists(singboxPath) {
		t.Fatalf("sing-box not found: %s", singboxPath)
	}

	storage := NewStorage(basePath)
	if err := storage.Init(); err != nil {
		t.Fatalf("storage init failed: %v", err)
	}

	settings := storage.GetAppSettings()
	settings.FreeAccessEnabled = true
	settings.FreeAccessReverse = false
	settings.RoutingMode = RoutingModeBlockedOnly
	if settings.FreeAccessServices == nil {
		settings.FreeAccessServices = DefaultFreeAccessServiceState()
	}
	if err := storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update app settings failed: %v", err)
	}

	filterManager := NewFilterManager(basePath)
	updated, err := filterManager.UpdateRefilters()
	if err != nil {
		t.Fatalf("routing database update failed: %v", err)
	}
	t.Logf("routing database update finished, files updated: %d", updated)

	builder := NewConfigBuilderForStorage(storage)
	builder.SetRoutingMode(RoutingModeBlockedOnly)
	if err := builder.BuildConfig(subscriptionURL); err != nil {
		t.Fatalf("subscription config build failed: %v", err)
	}

	profile, err := storage.GetActiveProfile()
	if err != nil {
		t.Fatalf("get active profile failed: %v", err)
	}
	config, err := storage.GetProfileConfig(profile.ID)
	if err != nil {
		t.Fatalf("get profile config failed: %v", err)
	}
	candidates := getOutboundCandidates(config, SmartBypassGroupTag)
	if len(candidates) == 0 || candidates[0] != "auto-select" || containsString(candidates, "direct") {
		t.Fatalf("%s candidates = %v, want auto-select first and no direct", SmartBypassGroupTag, candidates)
	}
	telegramCandidates := getOutboundCandidates(config, ServiceBypassGroupTag("telegram"))
	if len(telegramCandidates) == 0 || telegramCandidates[0] != "auto-select" || containsString(telegramCandidates, "direct") {
		t.Fatalf("%s candidates = %v, want auto-select first and no direct", ServiceBypassGroupTag("telegram"), telegramCandidates)
	}
	if !containsProcessDirectRule(config, ByeDPIProcessName) {
		t.Fatalf("generated config does not bypass %s process traffic directly", ByeDPIProcessName)
	}

	xrayConfigPath, hasXrayConfig, err := storage.WriteActiveXrayConfigToFile()
	if err != nil {
		t.Fatalf("write active xray config failed: %v", err)
	}
	var xrayBridge *XrayBridgeManager
	if hasXrayConfig {
		xrayPath := filepath.Join(basePath, "bin", XrayExeName)
		if !fileExists(xrayPath) {
			t.Fatalf("xray bridge config was generated, but xray.exe is not bundled: %s", xrayPath)
		}
		if output, err := newBackgroundCommand(xrayPath, "run", "-test", "-config", xrayConfigPath).CombinedOutput(); err != nil {
			t.Fatalf("xray config check failed: %v\n%s", err, output)
		}
		xrayBridge = NewXrayBridgeManager(basePath, storage.GetResourcesPath(), func(msg string) { t.Log(msg) })
		xrayPorts, err := xrayBridge.socksPorts()
		if err != nil {
			t.Fatalf("read xray bridge ports failed: %v", err)
		}
		if len(xrayPorts) == 0 {
			t.Fatal("xray bridge config does not expose a local SOCKS port")
		}
		if err := xrayBridge.Start(); err != nil {
			t.Fatalf("xray bridge start failed: %v", err)
		}
		defer xrayBridge.Stop()
		if !waitForTCP("127.0.0.1", xrayPorts[0], 12*time.Second) {
			t.Fatalf("xray bridge socks port did not become ready")
		}
		proxyURL := fmt.Sprintf("socks5h://127.0.0.1:%d", xrayPorts[0])
		if err := checkHTTPSReachableWithProxy(proxyURL, "https://www.gstatic.com/generate_204"); err != nil {
			t.Fatalf("xhttp bridge did not reach gstatic through local Xray SOCKS: %v", err)
		}
	}

	configPath, err := storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	if output, err := newBackgroundCommand(singboxPath, "check", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("sing-box config check failed: %v\n%s", err, output)
	}

	byeDPI := NewByeDPIManager(basePath, func(msg string) { t.Log(msg) })
	if err := byeDPI.Start(); err != nil {
		t.Fatalf("ByeDPI start failed: %v", err)
	}
	defer byeDPI.Stop()

	var stdout, stderr bytes.Buffer
	cmd := exec.Command(singboxPath, "run", "-c", configPath)
	cmd.Dir = storage.GetResourcesPath()
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("sing-box start failed: %v", err)
	}
	defer stopProcessTree(cmd.Process.Pid)

	clashURL := builder.clashAPI.baseURL() + "/proxies"
	if !waitForHTTP(clashURL, builder.clashAPI.secret, 12*time.Second) {
		stopProcessTree(cmd.Process.Pid)
		t.Fatalf("clash api did not become ready\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	proxiesBody, err := httpGetBody(clashURL, builder.clashAPI.secret)
	if err != nil {
		t.Fatalf("failed to query proxies: %v", err)
	}
	if !strings.Contains(proxiesBody, SmartBypassGroupTag) ||
		!strings.Contains(proxiesBody, ServiceBypassGroupTag("telegram")) ||
		!strings.Contains(proxiesBody, "auto-select") {
		t.Fatalf("clash api does not expose expected subscription outbounds: %s", proxiesBody)
	}
	if hasXrayConfig && !strings.Contains(strings.ToLower(proxiesBody), "xhttp") {
		t.Fatalf("clash api does not expose xhttp bridge outbounds: %s", proxiesBody)
	}

	for _, target := range []string{
		"https://discord.com",
		"https://www.youtube.com/generate_204",
		"https://www.gstatic.com/generate_204",
	} {
		if err := checkHTTPSReachable(target); err != nil {
			t.Fatalf("%s is not reachable while subscription runtime is running: %v", target, err)
		}
	}
}

func TestManualWindowsUnifiedAppRuntimeFromEnv(t *testing.T) {
	if os.Getenv("DROPO_TEST_DEEP_WINDOWS_LIVE") != "1" {
		t.Skip("DROPO_TEST_DEEP_WINDOWS_LIVE is not set")
	}
	if runtime.GOOS != "windows" {
		t.Skip("Deep Windows live runtime is Windows-only")
	}
	requireManualWindowsAdmin(t)

	basePath := os.Getenv("DROPO_TEST_FREE_ACCESS_BASE")
	if basePath == "" {
		t.Skip("DROPO_TEST_FREE_ACCESS_BASE is not set")
	}
	basePath, err := filepath.Abs(basePath)
	if err != nil {
		t.Fatalf("resolve runtime base path failed: %v", err)
	}
	requireManualLiveRuntimeFiles(t, basePath)

	if changed, current, err := resetWindowsSystemProxyForPorts([]int{defaultDropoMixedProxyPort, 7301}); err != nil {
		t.Fatalf("preflight proxy reset failed: %v", err)
	} else if changed {
		t.Logf("preflight reset stale dropo proxy: %s", current)
	}
	assertNoRuntimeProcesses(t, basePath, "before live smoke")

	app := newManualLiveApp(t, basePath)
	defer func() {
		app.requestShutdown()
		app.Stop()
		app.cleanupDropoRuntimeResidue("manual deep windows live test cleanup")
		closeManagedProcessJob(app.writeLog)
		app.closeLogFile()
		assertNoRuntimeProcesses(t, basePath, "after live smoke cleanup")
		assertWinDivertNotOwnedByRuntime(t, basePath)
		if changed, current, err := resetWindowsSystemProxyForPorts([]int{defaultDropoMixedProxyPort, 7301}); err != nil {
			t.Fatalf("post-cleanup proxy verification failed: %v", err)
		} else if changed {
			t.Fatalf("dropo system proxy was left after cleanup and had to be reset: %s", current)
		}
	}()

	prepareManualLiveConfig(t, app, "", func(settings *GlobalAppSettings) {
		settings.RoutingMode = RoutingModeBlockedOnly
		settings.NetworkMode = NetworkModeWindowsUnified
		settings.DisableFreeAccess = false
		settings.HideRuTraffic = false
	})
	status := app.currentNetworkModeStatus()
	if status.Active != NetworkModeWindowsUnified || !status.DriverReady {
		t.Fatalf("network mode = %+v, want ready Windows Unified", status)
	}

	startResult := app.Start()
	requireAPISuccess(t, startResult)
	if app.trafficEngine == nil || app.trafficEngine.SuccessfulOpenCount() == 0 {
		t.Fatalf("native traffic engine never opened WinDivert; runtime processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	}
	// The engine may be inactive here when startup validation proved that every
	// configured service is reachable directly in the current network. The
	// successful-open counter above is the lifecycle assertion; ActiveTag is the
	// final routing decision rather than evidence that driver startup was tried.
	if !waitForRuntimeProcessName(t, basePath, "sing-box.exe", 20*time.Second) {
		t.Fatalf("Windows Unified must start sing-box/TUN; processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	}
	assertActiveConfigHasTun(t, app)
	t.Logf("Windows Unified free mode processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	stopResult := app.Stop()
	requireAPISuccess(t, stopResult)
	waitForNoRuntimeProcesses(t, basePath, 12*time.Second)
	assertWinDivertNotOwnedByRuntime(t, basePath)

	subscriptionURL := os.Getenv("DROPO_TEST_SUBSCRIPTION_URL")
	if subscriptionURL == "" {
		t.Log("DROPO_TEST_SUBSCRIPTION_URL is not set; skipping subscription proxy endpoint live scenario")
		return
	}

	openCountBeforeSubscription := app.trafficEngine.SuccessfulOpenCount()
	prepareManualLiveConfig(t, app, subscriptionURL, func(settings *GlobalAppSettings) {
		settings.RoutingMode = RoutingModeBlockedOnly
		settings.NetworkMode = NetworkModeWindowsUnified
		settings.DisableFreeAccess = false
		settings.HideRuTraffic = false
	})
	startResult = app.Start()
	requireAPISuccess(t, startResult)
	if app.trafficEngine.SuccessfulOpenCount() <= openCountBeforeSubscription {
		t.Fatalf("native traffic engine did not reopen WinDivert for subscription scenario; runtime processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	}
	if !waitForRuntimeProcessName(t, basePath, "sing-box.exe", 20*time.Second) {
		t.Fatalf("sing-box TUN runtime did not start with subscription; runtime processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	}
	assertActiveConfigHasTun(t, app)
	if err := checkHTTPSReachableWithProxy(fmt.Sprintf("http://127.0.0.1:%d", defaultDropoMixedProxyPort), "https://www.gstatic.com/generate_204"); err != nil {
		t.Fatalf("local mixed proxy endpoint is not usable: %v", err)
	}
	t.Logf("subscription TUN fallback processes: %v", mustRuntimeProcessSnapshot(t, basePath))
	stopResult = app.Stop()
	requireAPISuccess(t, stopResult)
	waitForNoRuntimeProcesses(t, basePath, 15*time.Second)
	assertWinDivertNotOwnedByRuntime(t, basePath)

}

func httpGetBody(url, secret string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var body bytes.Buffer
	if _, err := body.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	return body.String(), nil
}

func waitForHTTP(url, secret string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return false
		}
		req.Header.Set("Authorization", "Bearer "+secret)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func waitForTCP(host string, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort(host, fmt.Sprint(port))
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func checkHTTPSReachable(url string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("unexpected server status %d", resp.StatusCode)
	}
	return nil
}

func checkHTTPSReachableWithProxy(proxyURL, targetURL string) error {
	nullDevice := "NUL"
	if runtime.GOOS != "windows" {
		nullDevice = "/dev/null"
	}
	cmd := exec.Command("curl", "-sS", "-L", "-o", nullDevice, "-w", "%{http_code}", "--proxy", proxyURL, "--max-time", "25", targetURL)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, output)
	}
	status := strings.TrimSpace(string(output))
	if status == "" || strings.HasPrefix(status, "0") {
		return fmt.Errorf("unexpected HTTP status %q", status)
	}
	return nil
}

func stopProcessTree(pid int) {
	if pid <= 0 {
		return
	}
	if runtime.GOOS == "windows" {
		_ = newBackgroundCommand("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid)).Run()
		return
	}
	process, err := os.FindProcess(pid)
	if err == nil {
		_ = process.Kill()
	}
}

func requireManualWindowsAdmin(t *testing.T) {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "net", "session")
	configureBackgroundCommand(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatalf("manual live runtime requires an elevated Administrator shell: %v", err)
	}
}

func requireManualLiveRuntimeFiles(t *testing.T, basePath string) {
	t.Helper()
	for _, rel := range []string{
		"bin/sing-box.exe",
		"bin/xray.exe",
		"bin/WinDivert.dll",
		"bin/WinDivert64.sys",
		"resources/template.json",
	} {
		path := filepath.Join(basePath, filepath.FromSlash(rel))
		if !fileExists(path) {
			t.Fatalf("manual live runtime file is missing: %s", path)
		}
	}
}

func newManualLiveApp(t *testing.T, basePath string) *App {
	t.Helper()
	app := NewApp()
	app.basePath = basePath
	app.dataPath = basePath
	app.singboxPath = filepath.Join(basePath, "bin", "sing-box.exe")
	app.initStorage()
	if app.storage == nil {
		t.Fatal("storage was not initialized")
	}
	app.initNativeWireGuard()
	app.initFreeAccess()
	app.initXrayBridge()
	app.initTrafficStats()
	app.initialized = true
	app.initializedReady.Store(true)
	app.startRouteStrategyMaintenanceListener()
	app.runStartupRoutingPreparation("")
	return app
}

func prepareManualLiveConfig(t *testing.T, app *App, subscriptionURL string, mutate func(*GlobalAppSettings)) string {
	t.Helper()
	if app.storage == nil || app.configBuilder == nil {
		t.Fatal("manual live app is not initialized")
	}
	settings := app.storage.GetAppSettings()
	settings.EnableLogging = true
	settings.LogLevel = LogLevelTrace
	settings.Notifications = true
	settings.AutoUpdateSub = false
	settings.FreeAccessEnabled = true
	settings.FreeAccessReverse = false
	settings.FreeAccessServices = DefaultFreeAccessServiceState()
	settings.FreeAccessMethods = DefaultFreeAccessServiceMethodState()
	settings.RoutingMode = RoutingModeBlockedOnly
	settings.NetworkMode = NetworkModeAuto
	settings.DisableFreeAccess = false
	settings.HideRuTraffic = false
	settings.RuProxyAddress = ""
	if mutate != nil {
		mutate(&settings)
	}
	if err := app.storage.UpdateAppSettings(settings); err != nil {
		t.Fatalf("update app settings failed: %v", err)
	}
	app.configBuilder.SetRoutingMode(settings.RoutingMode)
	if err := app.configBuilder.BuildConfig(subscriptionURL); err != nil {
		t.Fatalf("build live config failed: %v", err)
	}
	configPath, err := app.storage.WriteActiveConfigToFile()
	if err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	if output, err := newBackgroundCommand(app.singboxPath, "check", "-c", configPath).CombinedOutput(); err != nil {
		t.Fatalf("active sing-box config check failed: %v\n%s", err, output)
	}
	return configPath
}

func assertDeepWindowsProxyOnlyConfig(t *testing.T, app *App) {
	t.Helper()
	if app.storage == nil {
		t.Fatal("storage is not initialized")
	}
	proxyConfigPath := filepath.Join(app.storage.GetResourcesPath(), "deep_windows_proxy_config.json")
	config, err := readJSONConfig(proxyConfigPath)
	if err != nil {
		t.Fatalf("read proxy-only config failed: %v", err)
	}
	if count := countInboundType(config, "tun"); count != 0 {
		t.Fatalf("proxy-only config still contains %d tun inbound(s)", count)
	}
	if !mixedInboundIsLocalSystemProxy(config) {
		t.Fatalf("proxy-only config mixed inbound is not local system proxy: %v", config["inbounds"])
	}
	if output, err := newBackgroundCommand(app.singboxPath, "check", "-c", proxyConfigPath).CombinedOutput(); err != nil {
		t.Fatalf("proxy-only sing-box config check failed: %v\n%s", err, output)
	}
}

func assertActiveConfigHasTun(t *testing.T, app *App) {
	t.Helper()
	if app.storage == nil {
		t.Fatal("storage is not initialized")
	}
	config, err := readJSONConfig(app.storage.ActiveConfigFilePath())
	if err != nil {
		t.Fatalf("read active config failed: %v", err)
	}
	if count := countInboundType(config, "tun"); count == 0 {
		t.Fatalf("active subscription config does not contain a tun inbound: %v", config["inbounds"])
	}
}

func assertDeepWindowsSettingsMatrix(t *testing.T, app *App, subscriptionURL string) {
	t.Helper()
	scenarios := []struct {
		name       string
		configure  func(*GlobalAppSettings)
		assertPlan func(DeepWindowsRoutePlan)
	}{
		{
			name: "except-russia",
			configure: func(settings *GlobalAppSettings) {
				settings.RoutingMode = RoutingModeExceptRussia
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if plan.RUTraffic != DeepWindowsTrafficDirect || plan.ForeignTraffic != DeepWindowsTrafficProxy || plan.DefaultTraffic != DeepWindowsTrafficProxy {
					t.Fatalf("except-russia plan = %+v", plan)
				}
			},
		},
		{
			name: "all-traffic",
			configure: func(settings *GlobalAppSettings) {
				settings.RoutingMode = RoutingModeAllTraffic
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if plan.RUTraffic != DeepWindowsTrafficProxy || plan.ForeignTraffic != DeepWindowsTrafficProxy || plan.DefaultTraffic != DeepWindowsTrafficProxy {
					t.Fatalf("all-traffic plan = %+v", plan)
				}
			},
		},
		{
			name: "hide-ru",
			configure: func(settings *GlobalAppSettings) {
				settings.HideRuTraffic = true
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if plan.RUTraffic != DeepWindowsTrafficProxy {
					t.Fatalf("hide-ru plan = %+v", plan)
				}
			},
		},
		{
			name: "disable-free-access",
			configure: func(settings *GlobalAppSettings) {
				settings.DisableFreeAccess = true
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if plan.FreeMethodsAllowed || len(plan.TransparentServices) != 0 || !planContainsString(plan.ProxyServices, "youtube") {
					t.Fatalf("disable-free-access plan = %+v", plan)
				}
			},
		},
		{
			name: "manual-direct",
			configure: func(settings *GlobalAppSettings) {
				settings.FreeAccessMethods["telegram"] = FreeAccessMethodDirect
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if !planContainsString(plan.DirectServices, "telegram") {
					t.Fatalf("manual-direct plan = %+v", plan)
				}
			},
		},
		{
			name: "manual-vpn",
			configure: func(settings *GlobalAppSettings) {
				settings.FreeAccessMethods["telegram"] = FreeAccessMethodVPN
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if !planContainsString(plan.ProxyServices, "telegram") {
					t.Fatalf("manual-vpn plan = %+v", plan)
				}
			},
		},
		{
			name: "manual-native",
			configure: func(settings *GlobalAppSettings) {
				settings.FreeAccessMethods["telegram"] = DefaultZapretTransparentStrategies[0].Tag
			},
			assertPlan: func(plan DeepWindowsRoutePlan) {
				if !planContainsString(plan.TransparentServices, "telegram") {
					t.Fatalf("manual-zapret plan = %+v", plan)
				}
			},
		},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			configPath := prepareManualLiveConfig(t, app, subscriptionURL, scenario.configure)
			plan := app.buildDeepWindowsRoutePlan(configPath)
			scenario.assertPlan(plan)
			if plan.RequiresSingBoxProxy {
				if _, err := app.writeDeepWindowsProxyFallbackConfig(configPath); err != nil {
					t.Fatalf("write proxy-only config failed: %v", err)
				}
				assertDeepWindowsProxyOnlyConfig(t, app)
			}
		})
	}
}

func countInboundType(config map[string]interface{}, inboundType string) int {
	inbounds, _ := config["inbounds"].([]interface{})
	count := 0
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if ok && inboundMap["type"] == inboundType {
			count++
		}
	}
	return count
}

func mixedInboundIsLocalSystemProxy(config map[string]interface{}) bool {
	inbounds, _ := config["inbounds"].([]interface{})
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}
		if inboundMap["listen"] != "127.0.0.1" {
			return false
		}
		if mixedInboundPort(inboundMap["listen_port"]) != defaultDropoMixedProxyPort {
			return false
		}
		if value, ok := inboundMap["set_system_proxy"].(bool); !ok || !value {
			return false
		}
		return true
	}
	return false
}

func waitForRuntimeProcessName(t *testing.T, basePath, name string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if runtimeProcessNamePresent(t, basePath, name) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return runtimeProcessNamePresent(t, basePath, name)
}

func runtimeProcessNamePresent(t *testing.T, basePath, name string) bool {
	t.Helper()
	name = strings.ToLower(name)
	for _, line := range mustRuntimeProcessSnapshot(t, basePath) {
		if strings.HasPrefix(strings.ToLower(line), name+"|") {
			return true
		}
	}
	return false
}

func assertNoRuntimeProcesses(t *testing.T, basePath, stage string) {
	t.Helper()
	lines := mustRuntimeProcessSnapshot(t, basePath)
	if len(lines) > 0 {
		t.Fatalf("runtime processes are present %s: %v", stage, lines)
	}
}

func waitForNoRuntimeProcesses(t *testing.T, basePath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lines := mustRuntimeProcessSnapshot(t, basePath)
		if len(lines) == 0 {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	assertNoRuntimeProcesses(t, basePath, "after waiting")
}

func mustRuntimeProcessSnapshot(t *testing.T, basePath string) []string {
	t.Helper()
	lines, err := runtimeProcessSnapshot(basePath)
	if err != nil {
		t.Fatalf("runtime process snapshot failed: %v", err)
	}
	return lines
}

func runtimeProcessSnapshot(basePath string) ([]string, error) {
	script := `
$base = $env:DROPO_RUNTIME_BASE
$root = [System.IO.Path]::GetFullPath($base).TrimEnd('\').ToLowerInvariant()
$items = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
  if (-not $_.ExecutablePath) { return $false }
  try {
    $path = [System.IO.Path]::GetFullPath($_.ExecutablePath).ToLowerInvariant()
  } catch {
    return $false
  }
  return $path.StartsWith($root + '\')
}
foreach ($item in $items) {
  $name = [string]$item.Name
  $pidText = [string]$item.ProcessId
  $pathText = [string]$item.ExecutablePath
  Write-Output ($name + '|' + $pidText + '|' + $pathText)
}
`
	cmd := exec.Command("powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	cmd.Env = append(os.Environ(), "DROPO_RUNTIME_BASE="+basePath)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, text)
	}
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out, nil
}

func assertWinDivertNotOwnedByRuntime(t *testing.T, basePath string) {
	t.Helper()
	output, err := serviceControlOutput("qc", winDivertServiceName)
	text := string(output)
	if err != nil {
		if serviceControlSaysMissing(text) {
			return
		}
		t.Fatalf("WinDivert service query failed: %v; %s", err, compactServiceControlOutput(text))
	}
	binaryPath := parseWinDivertBinaryPath(text)
	if winDivertBinaryOwnedByRoots(binaryPath, []string{basePath}) {
		t.Fatalf("WinDivert service is still owned by runtime path after cleanup: %s", binaryPath)
	}
}
