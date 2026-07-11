package dropocore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	stateFileName      = "dropocore_state.json"
	singBoxLogFileName = "sing-box.log"
	maxLogs            = 1200
	maxEvents          = 256
	maxSingBoxLogBytes = 512 * 1024
)

var (
	mu      sync.Mutex
	current = defaultState()
)

type coreState struct {
	BasePath                 string            `json:"basePath"`
	Connected                bool              `json:"connected"`
	StartedAt                string            `json:"startedAt"`
	Subscription             string            `json:"subscription"`
	Config                   appConfig         `json:"config"`
	Version                  versionInfo       `json:"version"`
	Events                   []bridgeEvent     `json:"events"`
	Logs                     []string          `json:"logs"`
	LastError                string            `json:"lastError"`
	ServiceState             string            `json:"serviceState"`
	ServiceMessage           string            `json:"serviceMessage"`
	ServiceUpdatedAt         string            `json:"serviceUpdatedAt"`
	WireGuards               []wireGuardConfig `json:"wireguards"`
	RoutePolicies            map[string]string `json:"routePolicies,omitempty"`
	NextEventID              int64             `json:"nextEventId"`
	TotalSessions            int               `json:"totalSessions"`
	LastDurationMs           int64             `json:"lastDurationMs"`
	CachedSingBoxConfig      string            `json:"cachedSingBoxConfig,omitempty"`
	CachedProxyCount         int               `json:"cachedProxyCount,omitempty"`
	CachedConfigSubscription string            `json:"cachedConfigSubscription,omitempty"`
	CachedConfigSignature    string            `json:"cachedConfigSignature,omitempty"`
	CachedConfigUpdatedAt    string            `json:"cachedConfigUpdatedAt,omitempty"`
}

type appConfig struct {
	AutoStart         bool   `json:"autoStart"`
	AutoStartPrompted bool   `json:"autoStartPrompted"`
	EnableLogging     bool   `json:"enableLogging"`
	CheckUpdates      bool   `json:"checkUpdates"`
	Notifications     bool   `json:"notifications"`
	AutoUpdateSub     bool   `json:"autoUpdateSub"`
	Theme             string `json:"theme"`
	Language          string `json:"language"`
	LogLevel          string `json:"logLevel"`
	SubUpdateInterval int    `json:"subUpdateInterval"`
	HideRuTraffic     bool   `json:"hideRuTraffic"`
	RuProxyAddress    string `json:"ruProxyAddress"`
	DisableFreeAccess bool   `json:"disableFreeAccess"`
	RoutingMode       string `json:"routingMode"`
	NetworkMode       string `json:"networkMode"`
	GithubRepo        string `json:"githubRepo"`
	GithubURL         string `json:"githubURL"`
	TelegramName      string `json:"telegramName"`
	TelegramURL       string `json:"telegramURL"`
}

type versionInfo struct {
	Version        string `json:"version"`
	FullVersion    string `json:"fullVersion"`
	SingboxVersion string `json:"singboxVersion"`
}

type bridgeEvent struct {
	ID      int64                  `json:"id"`
	Name    string                 `json:"name"`
	Payload map[string]interface{} `json:"payload"`
}

type routeInfo struct {
	Tag                  string   `json:"tag"`
	Name                 string   `json:"name"`
	Method               string   `json:"method"`
	EffectiveMethodLabel string   `json:"effectiveMethodLabel"`
	RequiresVPN          bool     `json:"requiresVpn"`
	DelayMS              int      `json:"delayMs"`
	DomainSuffixes       []string `json:"domainSuffixes"`
	IPCidrs              []string `json:"ipCidrs"`
}

func defaultState() coreState {
	return coreState{
		Config: appConfig{
			AutoStart:         false,
			AutoStartPrompted: true,
			EnableLogging:     true,
			CheckUpdates:      true,
			Notifications:     true,
			AutoUpdateSub:     true,
			DisableFreeAccess: true,
			Theme:             "system",
			Language:          "ru",
			LogLevel:          "info",
			SubUpdateInterval: 24,
			RoutingMode:       "blocked_only",
			NetworkMode:       "android_vpn",
			GithubRepo:        "Droponevedimka/dropo",
			GithubURL:         "https://github.com/Droponevedimka/dropo",
			TelegramName:      "t.me/droponevedimka555",
			TelegramURL:       "https://t.me/droponevedimka555",
		},
		Version: versionInfo{
			Version:        "dev",
			FullVersion:    "dev-android-native",
			SingboxVersion: androidSingBoxVersion,
		},
		ServiceState: "stopped",
		NextEventID:  1,
	}
}

// EnsureStarted initializes the Android core state. It is intentionally small:
// Android owns the VpnService lifecycle, while this package owns persisted
// state, events, and the JSON API consumed by Flutter.
func EnsureStarted(basePath, appVersion string) string {
	mu.Lock()
	defer mu.Unlock()

	if strings.TrimSpace(basePath) != "" {
		current.BasePath = basePath
		_ = os.MkdirAll(basePath, 0o755)
	}
	if err := loadLocked(); err != nil {
		appendLogLocked("state load failed: " + err.Error())
	}
	if strings.TrimSpace(appVersion) != "" {
		current.Version.Version = strings.TrimSpace(appVersion)
		current.Version.FullVersion = strings.TrimSpace(appVersion) + "-android-native"
	}
	current.Version.SingboxVersion = androidSingBoxVersion
	appendLogIfChangedLocked("dropocore android bridge ready")
	emitLocked("core-ready", map[string]interface{}{"platform": "android"})
	_ = saveLocked()
	return encode(map[string]interface{}{"success": true})
}

func Shutdown() string {
	mu.Lock()
	defer mu.Unlock()

	applyServiceStateLocked("stopped", "core shutdown", "")
	current.LastError = ""
	appendLogLocked("android core shutdown")
	_ = saveLocked()
	return encode(map[string]interface{}{"success": true})
}

func Status() string {
	mu.Lock()
	defer mu.Unlock()
	return encode(statusLocked())
}

func Logs() string {
	mu.Lock()
	defer mu.Unlock()
	return encode(map[string]interface{}{"success": true, "logs": append([]string(nil), current.Logs...)})
}

func Events(since int64) string {
	mu.Lock()
	defer mu.Unlock()

	events := make([]bridgeEvent, 0)
	for _, event := range current.Events {
		if event.ID > since {
			events = append(events, event)
		}
	}
	return encode(map[string]interface{}{"success": true, "events": events})
}

func SetConnected(connected bool) string {
	mu.Lock()
	defer mu.Unlock()

	state := "stopped"
	if connected {
		state = "connected"
	}
	payload := applyServiceStateLocked(state, "", "")
	_ = saveLocked()
	return encode(payload)
}

func applyServiceStateLocked(state, message, errText string) map[string]interface{} {
	state = normalizeServiceState(state)
	previousState := normalizeServiceState(current.ServiceState)
	wasConnected := current.Connected
	now := time.Now().Format(time.RFC3339)

	current.ServiceState = state
	current.ServiceMessage = strings.TrimSpace(message)
	current.ServiceUpdatedAt = now

	switch state {
	case "starting":
		current.Connected = false
		current.LastError = ""
	case "connected":
		current.Connected = true
		current.LastError = ""
		if current.StartedAt == "" {
			current.StartedAt = now
		}
		if !wasConnected {
			current.TotalSessions++
			appendLogLocked("android VpnService connected")
		}
	case "disconnecting":
		current.Connected = false
		current.LastError = ""
	case "failed":
		current.LastDurationMs = currentSessionDurationLocked().Milliseconds()
		current.Connected = false
		current.StartedAt = ""
		if strings.TrimSpace(errText) != "" {
			current.LastError = strings.TrimSpace(errText)
		}
	case "stopped":
		current.LastDurationMs = currentSessionDurationLocked().Milliseconds()
		current.Connected = false
		current.StartedAt = ""
		if previousState != "failed" {
			current.LastError = ""
		}
		if wasConnected {
			appendLogLocked("android VpnService disconnected")
		}
	}

	payload := serviceStatePayloadLocked()
	emitLocked("vpn-status-changed", payload)
	return payload
}

func serviceStatePayloadLocked() map[string]interface{} {
	state := normalizeServiceState(current.ServiceState)
	connected := state == "connected"
	connecting := state == "starting"
	disconnecting := state == "disconnecting"
	return map[string]interface{}{
		"success":       true,
		"state":         state,
		"vpnState":      state,
		"running":       connected,
		"connected":     connected,
		"connecting":    connecting,
		"disconnecting": disconnecting,
		"hasError":      state == "failed" || current.LastError != "",
		"error":         current.LastError,
		"message":       current.ServiceMessage,
		"updatedAt":     current.ServiceUpdatedAt,
	}
}

func normalizeServiceState(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "starting", "connected", "disconnecting", "failed", "stopped":
		return strings.ToLower(strings.TrimSpace(state))
	default:
		return "stopped"
	}
}

func Call(method, argsJSON string) string {
	args := decodeArgs(argsJSON)
	if method == "RunClientQuickCheck" {
		return runAndroidClientQuickCheck()
	}
	if method == "CheckForUpdates" {
		return checkAndroidUpdates()
	}

	mu.Lock()
	defer mu.Unlock()

	switch method {
	case "GetCurrentSubscription":
		return encode(subscriptionLocked())
	case "GetAppConfig":
		return encode(appConfigLocked())
	case "SaveAppConfig":
		applyConfigArgsLocked(args)
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true, "message": "Android settings saved"})
	case "ResolveAutoStartPrompt":
		current.Config.AutoStart = boolArg(args, 0, false)
		current.Config.AutoStartPrompted = true
		_ = saveLocked()
		return encode(map[string]interface{}{
			"success":            true,
			"autoStart":          current.Config.AutoStart,
			"autoStartPrompted":  true,
			"androidAlwaysOnVpn": true,
		})
	case "GetProfiles":
		return encode(profilesLocked())
	case "SetActiveProfile":
		return encode(map[string]interface{}{"success": true, "message": "Android profile is active"})
	case "CreateProfile", "UpdateProfile", "DeleteProfile":
		return encode(map[string]interface{}{"success": false, "error": "Profiles are not editable on Android yet"})
	case "GetRoutingMode":
		return encode(map[string]interface{}{"success": true, "mode": current.Config.RoutingMode})
	case "SetRoutingMode":
		if current.Connected {
			return encode(map[string]interface{}{"success": false, "error": "VPN must be stopped before changing routing mode"})
		}
		mode := normalizeAndroidRoutingMode(stringArg(args, 0, current.Config.RoutingMode))
		if mode == "" {
			return encode(map[string]interface{}{"success": false, "error": "Unknown routing mode"})
		}
		if mode != current.Config.RoutingMode {
			current.Config.RoutingMode = mode
			clearCachedConfigLocked()
			appendLogLocked("android routing mode changed: " + mode)
		}
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true, "mode": current.Config.RoutingMode})
	case "GetNetworkMode":
		return encode(map[string]interface{}{"success": true, "mode": current.Config.NetworkMode})
	case "SetNetworkMode":
		if current.Connected {
			return encode(map[string]interface{}{"success": false, "error": "VPN must be stopped before changing network mode"})
		}
		mode := normalizeAndroidNetworkMode(stringArg(args, 0, current.Config.NetworkMode))
		if mode == "" {
			return encode(map[string]interface{}{"success": false, "error": "Unknown Android network mode"})
		}
		current.Config.NetworkMode = mode
		_ = saveLocked()
		return encode(map[string]interface{}{
			"success": true,
			"mode":    current.Config.NetworkMode,
			"status": map[string]interface{}{
				"requested":   current.Config.NetworkMode,
				"active":      "android_vpn",
				"fallback":    false,
				"label":       "Android VPN",
				"description": "Android VpnService + sing-box libbox",
			},
		})
	case "GetHideRuTraffic":
		return encode(map[string]interface{}{
			"success":      true,
			"enabled":      current.Config.HideRuTraffic,
			"proxyAddress": current.Config.RuProxyAddress,
		})
	case "SetHideRuTraffic":
		if current.Connected {
			return encode(map[string]interface{}{"success": false, "error": "VPN must be stopped before changing RU traffic settings"})
		}
		enabled := boolArg(args, 0, current.Config.HideRuTraffic)
		proxyAddress := stringArg(args, 1, current.Config.RuProxyAddress)
		if enabled && proxyAddress != "" {
			if _, err := parseAndroidProxyCandidates(proxyAddress); err != nil {
				return encode(map[string]interface{}{"success": false, "error": "Invalid RU proxy address: " + err.Error()})
			}
		}
		if enabled != current.Config.HideRuTraffic || proxyAddress != current.Config.RuProxyAddress {
			current.Config.HideRuTraffic = enabled
			current.Config.RuProxyAddress = proxyAddress
			clearCachedConfigLocked()
			appendLogLocked(fmt.Sprintf("android hide RU traffic: %v", enabled))
		}
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true, "enabled": current.Config.HideRuTraffic, "proxyAddress": current.Config.RuProxyAddress})
	case "GetFreeAccessConfig":
		return encode(freeAccessLocked(false))
	case "SetDisableFreeAccess":
		if current.Connected {
			return encode(map[string]interface{}{"success": false, "error": "VPN must be stopped before changing free access settings"})
		}
		current.Config.DisableFreeAccess = boolArg(args, 0, current.Config.DisableFreeAccess)
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true, "disableFreeAccess": current.Config.DisableFreeAccess})
	case "SetAndroidRoutePolicy", "SetFreeAccessServiceMethod":
		return encode(setAndroidRoutePolicyLocked(args))
	case "GetBypassRouteSummary":
		return encode(freeAccessLocked(true))
	case "GetTrafficStats":
		return encode(trafficStatsLocked())
	case "ResetTrafficStats":
		current.TotalSessions = 0
		current.LastDurationMs = 0
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true})
	case "GetWireGuardList":
		return encode(map[string]interface{}{"success": true, "configs": wireGuardListPayloadLocked(), "count": len(current.WireGuards)})
	case "GetWireGuardConfig":
		tag := stringArg(args, 0, "")
		for _, wg := range current.WireGuards {
			if wg.Tag == tag {
				return encode(wireGuardPayload(wg))
			}
		}
		return encode(map[string]interface{}{"success": false, "error": "WireGuard config not found"})
	case "ParseWireGuardConfigAPI":
		wg, err := parseWireGuardConfigText(stringArg(args, 0, ""))
		if err != nil {
			return encode(map[string]interface{}{"success": false, "error": err.Error()})
		}
		return encode(wireGuardPayload(wg))
	case "AddWireGuard":
		return addWireGuardLocked(args)
	case "UpdateWireGuard":
		return updateWireGuardLocked(args)
	case "DeleteWireGuard":
		return deleteWireGuardLocked(args)
	case "TestVPNConnection":
		value := strings.TrimSpace(stringArg(args, 0, ""))
		if value == "" {
			return encode(map[string]interface{}{"success": false, "error": "Subscription URL is empty"})
		}
		if isDirectProxyLink(value) {
			proxies, err := parseAndroidProxyCandidates(value)
			if err != nil {
				appendLogLocked("android subscription test failed: " + err.Error())
				_ = saveLocked()
				return encode(map[string]interface{}{"success": false, "error": err.Error()})
			}
			appendLogLocked("android subscription test ok: " + proxyListSummary(proxies))
			_ = saveLocked()
			return encode(map[string]interface{}{
				"success":      true,
				"count":        len(proxies),
				"isDirectLink": true,
				"proxies":      proxyCandidatesPayload(proxies),
			})
		}
		return encode(map[string]interface{}{
			"success":      true,
			"count":        estimateProxyCount(value),
			"isDirectLink": strings.Contains(value, "://"),
			"proxies":      []interface{}{},
		})
	case "SetVPNSubscription":
		nextSubscription := strings.TrimSpace(stringArg(args, 0, ""))
		if nextSubscription != current.Subscription {
			clearCachedConfigLocked()
		}
		current.Subscription = nextSubscription
		current.LastError = ""
		appendLogLocked("android subscription saved: " + subscriptionSummary(current.Subscription))
		_ = saveLocked()
		return encode(map[string]interface{}{
			"success":    true,
			"proxyCount": estimateProxyCount(current.Subscription),
			"wasRunning": current.Connected,
		})
	case "RemoveVPNSubscription":
		current.Subscription = ""
		current.LastError = ""
		clearCachedConfigLocked()
		appendLogLocked("android subscription removed")
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true, "proxyCount": 0, "wasRunning": current.Connected})
	case "AndroidEngineStarting":
		applyServiceStateLocked("starting", "Android VpnService is starting sing-box", "")
		appendLogLocked("android engine starting")
		emitLocked("vpn-starting", map[string]interface{}{})
		_ = saveLocked()
		return encode(map[string]interface{}{"success": true})
	case "AndroidServiceState":
		state := stringArg(args, 0, "stopped")
		message := stringArg(args, 1, "")
		errText := stringArg(args, 2, "")
		payload := applyServiceStateLocked(state, message, errText)
		_ = saveLocked()
		return encode(payload)
	case "AndroidEngineLog":
		message := stringArg(args, 0, "")
		if message != "" {
			appendLogLocked("android engine: " + message)
			_ = saveLocked()
		}
		return encode(map[string]interface{}{"success": true})
	case "AndroidSingBoxLog":
		message := stringArg(args, 0, "")
		if message != "" {
			appendSingBoxLogLocked(message)
			appendLogLocked("sing-box: " + truncateLogLine(message, 500))
		}
		return encode(map[string]interface{}{"success": true})
	case "AndroidEngineError":
		message := stringArg(args, 0, "unknown Android VPN engine error")
		applyServiceStateLocked("failed", message, message)
		appendLogLocked("android engine error: " + message)
		emitLocked("vpn-error", map[string]interface{}{"error": message})
		_ = saveLocked()
		return encode(map[string]interface{}{"success": false, "error": message})
	case "AndroidDiagnostics":
		return encode(androidDiagnosticsLocked())
	case "CaptureDPIFingerprint":
		return encode(map[string]interface{}{"success": true, "android": true})
	case "CheckExternalVPNConflicts":
		return encode(map[string]interface{}{"supported": true, "hasConflicts": false, "conflicts": []interface{}{}, "warning": ""})
	case "PrepareQuit":
		return encode(map[string]interface{}{"showNotice": false, "injected": false, "recommendRemove": false})
	case "OpenFingerprintFolder", "OpenConfigFolder", "OpenLogs", "ShowWindow", "OpenExternalLink":
		return encode(map[string]interface{}{"success": true})
	default:
		return encode(map[string]interface{}{"success": true, "android": true, "method": method})
	}
}

func statusLocked() map[string]interface{} {
	state := normalizeServiceState(current.ServiceState)
	connected := state == "connected"
	connecting := state == "starting"
	disconnecting := state == "disconnecting"
	return map[string]interface{}{
		"success":                true,
		"connected":              connected,
		"running":                connected,
		"connecting":             connecting,
		"disconnecting":          disconnecting,
		"vpnState":               state,
		"serviceMessage":         current.ServiceMessage,
		"serviceUpdatedAt":       current.ServiceUpdatedAt,
		"hasError":               state == "failed" || current.LastError != "",
		"error":                  current.LastError,
		"configExists":           true,
		"singboxExists":          true,
		"networkMode":            "android_vpn",
		"networkModeLabel":       "Android VPN",
		"networkModeDescription": "Android VpnService + sing-box libbox",
		"dependencies": map[string]interface{}{
			"managed":   true,
			"ready":     true,
			"required":  "",
			"installed": "sing-box libbox " + current.Version.SingboxVersion,
			"sizeMB":    0,
		},
		"version": current.Version,
	}
}

func clearCachedConfigLocked() {
	current.CachedSingBoxConfig = ""
	current.CachedProxyCount = 0
	current.CachedConfigSubscription = ""
	current.CachedConfigSignature = ""
	current.CachedConfigUpdatedAt = ""
}

func androidDiagnosticsLocked() map[string]interface{} {
	lastLogs := append([]string(nil), current.Logs...)
	if len(lastLogs) > 160 {
		lastLogs = lastLogs[len(lastLogs)-160:]
	}
	singBoxLogPath := singBoxLogPathLocked()
	singBoxLogs := tailTextFileLocked(singBoxLogPath, 180)
	if len(singBoxLogs) == 0 {
		singBoxLogs = []string{"empty"}
	}
	return map[string]interface{}{
		"success": true,
		"text": strings.Join([]string{
			"dropo Android diagnostics",
			"serviceState: " + normalizeServiceState(current.ServiceState),
			"serviceMessage: " + current.ServiceMessage,
			"connected: " + fmt.Sprint(current.Connected),
			"startedAt: " + current.StartedAt,
			"lastError: " + current.LastError,
			"version: " + current.Version.FullVersion,
			"singBoxVersion: " + current.Version.SingboxVersion,
			"basePath: " + current.BasePath,
			"subscription: " + subscriptionSummary(current.Subscription),
			"cachedConfig: " + fmt.Sprintf("%v (%d proxies, updated %s)", current.CachedSingBoxConfig != "", current.CachedProxyCount, current.CachedConfigUpdatedAt),
			"cachedConfigSummary: " + androidConfigSummaryLocked(),
			"routingMode: " + current.Config.RoutingMode,
			"logLevel: " + current.Config.LogLevel,
			"singBoxLog: " + valueOrEmpty(singBoxLogPath),
			"",
			"recent logs:",
			strings.Join(lastLogs, "\n"),
			"",
			"recent sing-box logs:",
			strings.Join(singBoxLogs, "\n"),
		}, "\n"),
		"serviceState":        normalizeServiceState(current.ServiceState),
		"cachedConfig":        current.CachedSingBoxConfig != "",
		"cachedProxyCount":    current.CachedProxyCount,
		"cachedConfigAt":      current.CachedConfigUpdatedAt,
		"subscription":        subscriptionSummary(current.Subscription),
		"recentLogLineCount":  len(lastLogs),
		"singBoxLogPath":      singBoxLogPath,
		"singBoxLogLineCount": len(singBoxLogs),
	}
}

func subscriptionLocked() map[string]interface{} {
	return map[string]interface{}{
		"hasSubscription": strings.TrimSpace(current.Subscription) != "",
		"url":             current.Subscription,
		"proxyCount":      estimateProxyCount(current.Subscription),
	}
}

func appConfigLocked() map[string]interface{} {
	return map[string]interface{}{
		"success":           true,
		"autoStart":         current.Config.AutoStart,
		"autoStartPrompted": current.Config.AutoStartPrompted,
		"enableLogging":     current.Config.EnableLogging,
		"checkUpdates":      current.Config.CheckUpdates,
		"notifications":     current.Config.Notifications,
		"autoUpdateSub":     current.Config.AutoUpdateSub,
		"theme":             current.Config.Theme,
		"language":          current.Config.Language,
		"logLevel":          current.Config.LogLevel,
		"subUpdateInterval": current.Config.SubUpdateInterval,
		"hideRuTraffic":     current.Config.HideRuTraffic,
		"ruProxyAddress":    current.Config.RuProxyAddress,
		"disableFreeAccess": current.Config.DisableFreeAccess,
		"routingMode":       current.Config.RoutingMode,
		"networkMode":       current.Config.NetworkMode,
		"githubRepo":        current.Config.GithubRepo,
		"githubURL":         current.Config.GithubURL,
		"telegramName":      current.Config.TelegramName,
		"telegramURL":       current.Config.TelegramURL,
		"appVersion":        current.Version.Version,
		"appFullVersion":    current.Version.FullVersion,
		"singboxVersion":    current.Version.SingboxVersion,
		"networkModeStatus": map[string]interface{}{
			"requested":   current.Config.NetworkMode,
			"active":      "android_vpn",
			"fallback":    false,
			"label":       "Android VPN",
			"description": "Android VpnService + sing-box libbox",
		},
	}
}

func profilesLocked() map[string]interface{} {
	return map[string]interface{}{
		"success":       true,
		"activeProfile": 1,
		"profiles": []map[string]interface{}{
			{
				"id":             1,
				"name":           "Android",
				"subscription":   current.Subscription,
				"wireguardCount": len(current.WireGuards),
				"proxyCount":     estimateProxyCount(current.Subscription),
				"isActive":       true,
				"createdAt":      time.Now().Format(time.RFC3339),
			},
		},
	}
}

func freeAccessLocked(live bool) map[string]interface{} {
	services := routesLocked(live)
	return map[string]interface{}{
		"success":            true,
		"enabled":            true,
		"disableFreeAccess":  true,
		"freeMethodsAllowed": false,
		"services":           services,
		"methodOptions":      androidRouteMethodOptions(),
		"methodCache":        androidRouteMethodCache(services),
	}
}

func routesLocked(live bool) []routeInfo {
	return androidServiceRoutesLocked(live)
}

func trafficStatsLocked() map[string]interface{} {
	currentDuration := currentSessionDurationLocked()
	currentMs := int64(0)
	if current.Connected {
		currentMs = currentDuration.Milliseconds()
	}
	currentTraffic := trafficBlock(currentMs, currentMs/15, currentMs/9)
	lastTraffic := trafficBlock(current.LastDurationMs, 0, 0)
	totalTraffic := trafficBlock(current.LastDurationMs+currentMs, currentMs/15, currentMs/9)
	totalTraffic["sessions"] = current.TotalSessions
	return map[string]interface{}{
		"success": true,
		"current": currentTraffic,
		"last":    lastTraffic,
		"total":   totalTraffic,
	}
}

func trafficBlock(durationMs, uploaded, downloaded int64) map[string]interface{} {
	seconds := durationMs / 1000
	return map[string]interface{}{
		"uploaded":      uploaded,
		"downloaded":    downloaded,
		"duration":      seconds,
		"uploadedStr":   formatBytes(uploaded),
		"downloadedStr": formatBytes(downloaded),
		"durationStr":   formatDuration(seconds),
	}
}

func currentSessionDurationLocked() time.Duration {
	if current.StartedAt == "" {
		return 0
	}
	started, err := time.Parse(time.RFC3339, current.StartedAt)
	if err != nil {
		return 0
	}
	return time.Since(started)
}

func applyConfigArgsLocked(args []interface{}) {
	oldEffectiveLogLevel := effectiveAndroidLogLevel(current.Config.EnableLogging, current.Config.LogLevel)
	current.Config.AutoStart = boolArg(args, 0, current.Config.AutoStart)
	current.Config.EnableLogging = boolArg(args, 1, current.Config.EnableLogging)
	current.Config.CheckUpdates = boolArg(args, 2, current.Config.CheckUpdates)
	current.Config.Notifications = boolArg(args, 3, current.Config.Notifications)
	current.Config.AutoUpdateSub = boolArg(args, 4, current.Config.AutoUpdateSub)
	current.Config.Theme = stringArg(args, 5, current.Config.Theme)
	current.Config.Language = stringArg(args, 6, current.Config.Language)
	current.Config.LogLevel = stringArg(args, 7, current.Config.LogLevel)
	current.Config.SubUpdateInterval = intArg(args, 8, current.Config.SubUpdateInterval)
	current.Config.AutoStartPrompted = true
	if newEffectiveLogLevel := effectiveAndroidLogLevel(current.Config.EnableLogging, current.Config.LogLevel); newEffectiveLogLevel != oldEffectiveLogLevel {
		clearCachedConfigLocked()
		appendLogLocked("android logging settings changed: " + newEffectiveLogLevel)
	}
}

func normalizeAndroidRoutingMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "blocked_only":
		return "blocked_only"
	case "except_russia":
		return "except_russia"
	case "all_traffic":
		return "all_traffic"
	default:
		return ""
	}
}

func normalizeAndroidNetworkMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "android_vpn", "auto", "deep_windows", "compat_tun":
		return "android_vpn"
	default:
		return ""
	}
}

func emitLocked(name string, payload map[string]interface{}) {
	event := bridgeEvent{ID: current.NextEventID, Name: name, Payload: payload}
	current.NextEventID++
	current.Events = append(current.Events, event)
	if len(current.Events) > maxEvents {
		current.Events = current.Events[len(current.Events)-maxEvents:]
	}
}

func appendLogLocked(line string) {
	entry := time.Now().Format("15:04:05") + " " + line
	current.Logs = append(current.Logs, entry)
	if len(current.Logs) > maxLogs {
		current.Logs = current.Logs[len(current.Logs)-maxLogs:]
	}
}

func appendSingBoxLogLocked(line string) {
	path := singBoxLogPathLocked()
	if path == "" {
		return
	}
	_ = os.MkdirAll(current.BasePath, 0o755)
	if info, err := os.Stat(path); err == nil && info.Size() > maxSingBoxLogBytes {
		_ = os.Rename(path, path+".old")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	entry := time.Now().Format("15:04:05") + " " + sanitizeLogLine(line) + "\n"
	_, _ = file.WriteString(entry)
}

func singBoxLogPathLocked() string {
	if current.BasePath == "" {
		return ""
	}
	return filepath.Join(current.BasePath, singBoxLogFileName)
}

func tailTextFileLocked(path string, maxLines int) []string {
	if path == "" || maxLines <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	text := strings.TrimRight(string(data), "\r\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

func androidConfigSummaryLocked() string {
	if strings.TrimSpace(current.CachedSingBoxConfig) == "" {
		return "empty"
	}
	var config map[string]interface{}
	if err := json.Unmarshal([]byte(current.CachedSingBoxConfig), &config); err != nil {
		return "unreadable: " + err.Error()
	}
	dns, _ := config["dns"].(map[string]interface{})
	route, _ := config["route"].(map[string]interface{})
	return strings.Join([]string{
		fmt.Sprintf("dns.final=%v", dns["final"]),
		"dns.servers=" + dnsServerSummary(dns["servers"]),
		fmt.Sprintf("route.final=%v", route["final"]),
		fmt.Sprintf("route.rules=%d", interfaceListLen(route["rules"])),
	}, " ")
}

func dnsServerSummary(raw interface{}) string {
	servers, _ := raw.([]interface{})
	parts := make([]string, 0, len(servers))
	for _, item := range servers {
		server, _ := item.(map[string]interface{})
		if server == nil {
			continue
		}
		part := fmt.Sprintf("%v/%v", server["tag"], server["type"])
		if detour, ok := server["detour"].(string); ok && detour != "" {
			part += "->" + detour
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, ",")
}

func interfaceListLen(raw interface{}) int {
	items, _ := raw.([]interface{})
	return len(items)
}

func sanitizeLogLine(line string) string {
	line = strings.ReplaceAll(line, "\r", " ")
	line = strings.ReplaceAll(line, "\n", " ")
	return truncateLogLine(strings.TrimSpace(line), 2000)
}

func truncateLogLine(line string, limit int) string {
	if limit <= 0 || len(line) <= limit {
		return line
	}
	return line[:limit] + "..."
}

func valueOrEmpty(value string) string {
	if strings.TrimSpace(value) == "" {
		return "empty"
	}
	return value
}

func appendLogIfChangedLocked(line string) {
	if len(current.Logs) > 0 && strings.HasSuffix(current.Logs[len(current.Logs)-1], " "+line) {
		return
	}
	appendLogLocked(line)
}

func subscriptionSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "empty"
	}
	if isDirectProxyLink(value) {
		if scheme, rest, ok := strings.Cut(value, "://"); ok {
			hostPort := rest
			if at := strings.LastIndex(hostPort, "@"); at >= 0 {
				hostPort = hostPort[at+1:]
			}
			hostPort = strings.SplitN(hostPort, "?", 2)[0]
			hostPort = strings.SplitN(hostPort, "#", 2)[0]
			if hostPort != "" {
				return scheme + "://" + hostPort
			}
			return scheme + "://direct-link"
		}
		return "direct-link"
	}
	if len(value) <= 64 {
		return value
	}
	return value[:61] + "..."
}

func proxyCandidatesPayload(proxies []proxyConfig) []interface{} {
	items := make([]interface{}, 0, len(proxies))
	for _, proxy := range proxies {
		items = append(items, map[string]interface{}{
			"type":     proxy.Type,
			"name":     proxy.Name,
			"server":   proxy.Server,
			"port":     proxy.ServerPort,
			"network":  proxy.Network,
			"security": proxy.Security,
			"raw":      proxy.Raw,
		})
	}
	return items
}

func proxyListSummary(proxies []proxyConfig) string {
	if len(proxies) == 0 {
		return "0 proxy"
	}
	parts := make([]string, 0, len(proxies))
	for i, proxy := range proxies {
		if i >= 3 {
			parts = append(parts, fmt.Sprintf("+%d more", len(proxies)-i))
			break
		}
		label := proxy.Type
		if proxy.Network != "" {
			label += "/" + proxy.Network
		}
		host := proxy.Server
		if proxy.ServerPort > 0 {
			host = fmt.Sprintf("%s:%d", host, proxy.ServerPort)
		}
		parts = append(parts, label+" "+host)
	}
	return strings.Join(parts, ", ")
}

func loadLocked() error {
	if current.BasePath == "" {
		return nil
	}
	path := filepath.Join(current.BasePath, stateFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	basePath := current.BasePath
	defaultVersion := current.Version
	if err := json.Unmarshal(data, &current); err != nil {
		current = defaultState()
		current.BasePath = basePath
		return err
	}
	current.BasePath = basePath
	if current.NextEventID <= 0 {
		current.NextEventID = 1
	}
	if current.Version.Version == "" {
		current.Version = defaultVersion
	}
	if current.ServiceState == "" {
		if current.Connected {
			current.ServiceState = "connected"
		} else {
			current.ServiceState = "stopped"
		}
	}
	if current.RoutePolicies == nil {
		current.RoutePolicies = map[string]string{}
	}
	return nil
}

func saveLocked() error {
	if current.BasePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return recordPersistenceErrorLocked(err)
	}
	tmp, err := os.CreateTemp(current.BasePath, stateFileName+"-*.tmp")
	if err != nil {
		return recordPersistenceErrorLocked(err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	err = tmp.Chmod(0o600)
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, filepath.Join(current.BasePath, stateFileName))
	}
	if err != nil {
		return recordPersistenceErrorLocked(err)
	}
	return nil
}

func recordPersistenceErrorLocked(err error) error {
	if err == nil {
		return nil
	}
	message := "state save failed: " + err.Error()
	current.LastError = message
	appendLogIfChangedLocked(message)
	return err
}

func encode(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf(`{"success":false,"error":%q}`, err.Error())
	}
	return string(data)
}

func decodeArgs(argsJSON string) []interface{} {
	var args []interface{}
	if strings.TrimSpace(argsJSON) == "" {
		return args
	}
	_ = json.Unmarshal([]byte(argsJSON), &args)
	return args
}

func stringArg(args []interface{}, index int, fallback string) string {
	if index < 0 || index >= len(args) || args[index] == nil {
		return fallback
	}
	return strings.TrimSpace(fmt.Sprint(args[index]))
}

func boolArg(args []interface{}, index int, fallback bool) bool {
	if index < 0 || index >= len(args) {
		return fallback
	}
	value, ok := args[index].(bool)
	if ok {
		return value
	}
	return fallback
}

func intArg(args []interface{}, index int, fallback int) int {
	if index < 0 || index >= len(args) {
		return fallback
	}
	switch value := args[index].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

func estimateProxyCount(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.Contains(value, "\n") {
		count := 0
		for _, line := range strings.Split(value, "\n") {
			if strings.TrimSpace(line) != "" {
				count++
			}
		}
		if count > 0 {
			return count
		}
	}
	return 1
}

func formatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	if value < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(value)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(value)/(1024*1024))
}

func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%d s", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%d min", minutes)
	}
	return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
}
