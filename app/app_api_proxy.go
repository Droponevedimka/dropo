package main

// Proxy methods for dropo.
// This file contains Clash API proxy operations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	routeSummaryPingTTL     = 15 * time.Second
	routeSummaryPingTimeout = 2500 * time.Millisecond
)

type routeSummaryLatencyEntry struct {
	Delay     int
	CheckedAt time.Time
	InFlight  bool
}

type clashProxyHistoryEntry struct {
	Delay int `json:"delay"`
}

type clashProxyInfo struct {
	Name    string                   `json:"name"`
	Type    string                   `json:"type"`
	Now     string                   `json:"now"`
	All     []string                 `json:"all"`
	History []clashProxyHistoryEntry `json:"history"`
}

type clashProxiesResponse struct {
	Proxies map[string]clashProxyInfo `json:"proxies"`
}

// GetProxiesWithDelay returns list of proxies with delay (ping)
func (a *App) GetProxiesWithDelay() map[string]interface{} {
	if !a.isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "VPN не запущен",
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// Get list of proxies
	resp, err := client.Get("http://127.0.0.1:9090/proxies")
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Не удалось подключиться к API: " + err.Error(),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Ошибка чтения ответа",
		}
	}

	var proxiesResp struct {
		Proxies map[string]struct {
			Name    string `json:"name"`
			Type    string `json:"type"`
			History []struct {
				Delay int `json:"delay"`
			} `json:"history"`
		} `json:"proxies"`
	}

	if err := json.Unmarshal(body, &proxiesResp); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Ошибка парсинга: " + err.Error(),
		}
	}

	// Form list of proxies with delays
	proxies := []map[string]interface{}{}
	for name, proxy := range proxiesResp.Proxies {
		// Skip service proxies
		if name == "DIRECT" || name == "REJECT" || name == "GLOBAL" ||
			proxy.Type == "Selector" || proxy.Type == "URLTest" || proxy.Type == "Fallback" {
			continue
		}

		delay := 0
		if len(proxy.History) > 0 {
			delay = proxy.History[len(proxy.History)-1].Delay
		}

		proxies = append(proxies, map[string]interface{}{
			"name":  name,
			"type":  proxy.Type,
			"delay": delay,
		})
	}

	return map[string]interface{}{
		"success": true,
		"proxies": proxies,
	}
}

// TestProxyDelay tests delay of a specific proxy
func (a *App) TestProxyDelay(proxyName string) map[string]interface{} {
	if !a.isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "VPN не запущен",
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}

	// Test proxy delay
	url := fmt.Sprintf("http://127.0.0.1:9090/proxies/%s/delay?timeout=5000&url=http://www.gstatic.com/generate_204", proxyName)
	resp, err := client.Get(url)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"delay":   0,
			"error":   err.Error(),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"delay":   0,
		}
	}

	var delayResp struct {
		Delay   int    `json:"delay"`
		Message string `json:"message"`
	}

	if err := json.Unmarshal(body, &delayResp); err != nil {
		return map[string]interface{}{
			"success": false,
			"delay":   0,
		}
	}

	if delayResp.Delay == 0 && delayResp.Message != "" {
		return map[string]interface{}{
			"success": false,
			"delay":   0,
			"error":   delayResp.Message,
		}
	}

	return map[string]interface{}{
		"success": true,
		"delay":   delayResp.Delay,
		"name":    proxyName,
	}
}

// TestAllProxiesDelay tests delay of all proxies in parallel
func (a *App) TestAllProxiesDelay() map[string]interface{} {
	if !a.isRunning {
		return map[string]interface{}{
			"success": false,
			"error":   "VPN не запущен",
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// Get list of proxies from selector proxy
	resp, err := client.Get("http://127.0.0.1:9090/proxies/proxy")
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   "Ошибка чтения",
		}
	}

	var selectorInfo struct {
		All []string `json:"all"`
		Now string   `json:"now"`
	}

	if err := json.Unmarshal(body, &selectorInfo); err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	// Filter service proxies
	filteredProxies := []string{}
	hasVPNProxy := false
	for _, name := range selectorInfo.All {
		if name == "auto-select" {
			hasVPNProxy = true
		}
		// Skip service elements
		if name == "direct" || name == "block" || name == "dns-out" || name == "auto-select" {
			continue
		}
		filteredProxies = append(filteredProxies, name)
	}

	// Get WireGuard configs from settings
	settings, _ := a.storage.GetUserSettings()
	wireGuardTags := []string{}
	wireGuardNames := map[string]string{} // tag -> name
	if settings != nil && len(settings.WireGuardConfigs) > 0 {
		for _, wg := range settings.WireGuardConfigs {
			wireGuardTags = append(wireGuardTags, wg.Tag)
			wireGuardNames[wg.Tag] = wg.Name
		}
	}

	// Free access / RU-traffic resilient groups (developing.md §4, §5) - only
	// the ones actually configured exist as outbounds, see addFreeAccessOutbounds.
	type freeAccessGroup struct {
		Tag   string
		Label string
	}
	freeAccessGroups := []freeAccessGroup{}
	if appSettings := a.storage.GetAppSettings(); FreeMethodsAllowed(appSettings) || hasVPNProxy {
		smartLabel := "Бесплатный доступ"
		if !FreeMethodsAllowed(appSettings) {
			smartLabel = "VPN для заблокированных"
		}
		freeAccessGroups = append(freeAccessGroups, freeAccessGroup{Tag: SmartBypassGroupTag, Label: smartLabel})
		for _, svc := range DefaultFreeAccessServices {
			if FreeAccessServiceEnabled(appSettings, svc.Tag) || hasVPNProxy {
				if svc.RequiresVPN && !hasVPNProxy {
					continue
				}
				freeAccessGroups = append(freeAccessGroups, freeAccessGroup{Tag: ServiceBypassGroupTag(svc.Tag), Label: svc.DisplayName})
			}
		}
		if appSettings.HideRuTraffic {
			freeAccessGroups = append(freeAccessGroups, freeAccessGroup{Tag: RuRouteGroupTag, Label: "RU-трафик"})
		}
	} else if appSettings.HideRuTraffic {
		freeAccessGroups = append(freeAccessGroups, freeAccessGroup{Tag: RuRouteGroupTag, Label: "RU-трафик"})
	}

	totalCount := len(filteredProxies) + len(wireGuardTags) + len(freeAccessGroups)
	if totalCount == 0 {
		return map[string]interface{}{
			"success":      true,
			"proxies":      []map[string]interface{}{},
			"currentProxy": selectorInfo.Now,
			"count":        0,
		}
	}

	// Test delay for each proxy in parallel
	type proxyResult struct {
		Name       string
		Delay      int
		Type       string
		IsInternal bool
	}

	results := make(chan proxyResult, totalCount)

	// Test external proxies
	for _, proxyName := range filteredProxies {
		go func(name string) {
			delay := 0
			proxyType := ""

			// Get proxy info
			infoResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:9090/proxies/%s", name))
			if err == nil {
				defer infoResp.Body.Close()
				infoBody, _ := io.ReadAll(infoResp.Body)
				var info struct {
					Type    string `json:"type"`
					History []struct {
						Delay int `json:"delay"`
					} `json:"history"`
				}
				if json.Unmarshal(infoBody, &info) == nil {
					proxyType = info.Type
					if len(info.History) > 0 {
						delay = info.History[len(info.History)-1].Delay
					}
				}
			}

			// If no history, test delay
			if delay == 0 {
				delayResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:9090/proxies/%s/delay?timeout=3000&url=http://www.gstatic.com/generate_204", name))
				if err == nil {
					defer delayResp.Body.Close()
					delayBody, _ := io.ReadAll(delayResp.Body)
					var d struct {
						Delay int `json:"delay"`
					}
					if json.Unmarshal(delayBody, &d) == nil {
						delay = d.Delay
					}
				}
			}

			results <- proxyResult{Name: name, Delay: delay, Type: proxyType, IsInternal: false}
		}(proxyName)
	}

	// Test WireGuard servers
	for _, wgTag := range wireGuardTags {
		go func(tag string) {
			delay := -1 // -1 means "active but ping not measured"
			displayName := wireGuardNames[tag]
			if displayName == "" {
				displayName = tag
			}

			// Check that WireGuard endpoint is accessible in Clash API
			infoResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:9090/proxies/%s", tag))
			if err == nil {
				defer infoResp.Body.Close()
				infoBody, _ := io.ReadAll(infoResp.Body)
				var info struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(infoBody, &info) == nil && info.Type == "WireGuard" {
					delay = -1 // WireGuard is active
				}
			}

			results <- proxyResult{Name: displayName + " (внутр.)", Delay: delay, Type: "WireGuard", IsInternal: true}
		}(wgTag)
	}

	// Test free access / RU-traffic resilient groups
	for _, group := range freeAccessGroups {
		go func(tag, label string) {
			delay := 0
			active := ""

			infoResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:9090/proxies/%s", tag))
			if err == nil {
				defer infoResp.Body.Close()
				infoBody, _ := io.ReadAll(infoResp.Body)
				var info struct {
					Now     string `json:"now"`
					History []struct {
						Delay int `json:"delay"`
					} `json:"history"`
				}
				if json.Unmarshal(infoBody, &info) == nil {
					active = info.Now
					if len(info.History) > 0 {
						delay = info.History[len(info.History)-1].Delay
					}
				}
			}

			name := label
			if active != "" {
				name = fmt.Sprintf("%s (%s)", label, FreeAccessOutboundLabel(active))
			}

			results <- proxyResult{Name: name, Delay: delay, Type: "FreeAccess", IsInternal: false}
		}(group.Tag, group.Label)
	}

	// Collect results
	proxies := []map[string]interface{}{}
	timeout := time.After(10 * time.Second)

	for i := 0; i < totalCount; i++ {
		select {
		case result := <-results:
			proxies = append(proxies, map[string]interface{}{
				"name":       result.Name,
				"delay":      result.Delay,
				"type":       result.Type,
				"isInternal": result.IsInternal,
			})
		case <-timeout:
			break
		}
	}

	return map[string]interface{}{
		"success":      true,
		"proxies":      proxies,
		"currentProxy": selectorInfo.Now,
		"count":        len(proxies),
	}
}

// GetBypassRouteSummary returns currently selected bypass/VPN methods for
// each enabled blocked service. sing-box urltest groups own the actual
// latency checks; this method only reads their current decision from the
// Clash-compatible API.
func (a *App) GetBypassRouteSummary() map[string]interface{} {
	settings := GlobalAppSettings{}
	if a.storage != nil {
		settings = a.storage.GetAppSettings()
	}

	mode := settings.RoutingMode
	if mode == "" {
		mode = DefaultRoutingMode
	}

	if !a.isRunning {
		return map[string]interface{}{
			"success":    true,
			"running":    false,
			"mode":       string(mode),
			"foreignVpn": mode == RoutingModeExceptRussia,
			"services":   []map[string]interface{}{},
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}
	proxies, err := fetchClashProxies(client)
	if err != nil {
		a.mu.Lock()
		transparentOnly := a.isRunning && a.cmd == nil
		a.mu.Unlock()
		if transparentOnly {
			return a.transparentBypassRouteSummary(settings, mode)
		}
		return map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}
	}

	_, hasVPNProxy := proxies["auto-select"]
	services := make([]map[string]interface{}, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		enabled := FreeAccessServiceEnabled(settings, svc.Tag)
		if !enabled {
			if hasVPNProxy {
				enabled = true
			} else {
				continue
			}
		}

		groupTag := ServiceBypassGroupTag(svc.Tag)
		info, ok := proxies[groupTag]
		if !ok {
			method := "waiting"
			outbound := ""
			override := FreeAccessServiceMethod(settings, svc.Tag)
			switch override {
			case FreeAccessMethodDirect:
				method = "Direct"
				outbound = "direct"
			case FreeAccessMethodVPN:
				method = "VPN"
				outbound = "auto-select"
			default:
				if IsFreeAccessProxyMethod(override) || IsFreeAccessTransparentMethod(override) {
					method = FreeAccessOutboundLabel(override)
					if IsFreeAccessTransparentMethod(override) {
						outbound = "direct"
					} else {
						outbound = override
					}
				}
			}
			if method == "waiting" && svc.RequiresVPN {
				if hasVPNProxy {
					method = "VPN"
					outbound = "auto-select"
				} else {
					method = "Direct (no VPN key)"
					outbound = "direct"
				}
			}
			services = append(services, map[string]interface{}{
				"tag":      svc.Tag,
				"name":     svc.DisplayName,
				"group":    groupTag,
				"method":   method,
				"outbound": outbound,
				"delay":    0,
			})
			continue
		}

		method, outbound, delay := summarizeBypassProxy(proxies, info)
		if probe, ok := a.lastRouteProbeResult(svc.Tag); ok && probe.Success && probe.MethodKind == "transparent" {
			method = probe.MethodLabel
			outbound = probe.MethodTag
			delay = int(probe.LatencyMS)
		}
		if delay <= 0 {
			delay = a.cachedRouteServiceDelay(svc)
		}

		services = append(services, map[string]interface{}{
			"tag":      svc.Tag,
			"name":     svc.DisplayName,
			"group":    groupTag,
			"method":   method,
			"outbound": outbound,
			"delay":    delay,
		})
	}

	catchAll := map[string]interface{}{}
	if info, ok := proxies[SmartBypassGroupTag]; ok {
		method, outbound, delay := summarizeBypassProxy(proxies, info)
		if probe, ok := a.lastRouteProbeCatchAll(); ok {
			method = probe.MethodLabel
			outbound = probe.MethodTag
			delay = int(probe.LatencyMS)
		}
		catchAll = map[string]interface{}{
			"method":   method,
			"outbound": outbound,
			"delay":    delay,
		}
	}

	return map[string]interface{}{
		"success":    true,
		"running":    true,
		"mode":       string(mode),
		"foreignVpn": mode == RoutingModeExceptRussia,
		"services":   services,
		"catchAll":   catchAll,
	}
}

func (a *App) transparentBypassRouteSummary(settings GlobalAppSettings, mode RoutingMode) map[string]interface{} {
	storedStrategies, _ := a.loadFreeAccessStrategies()
	serviceFallbackCache := a.loadServiceStrategyCache()
	transparentTags := a.availableTransparentStrategyTags()
	services := make([]map[string]interface{}, 0, len(DefaultFreeAccessServices))

	for _, svc := range DefaultFreeAccessServices {
		if !FreeAccessServiceEnabled(settings, svc.Tag) && !svc.RequiresVPN {
			continue
		}

		method := "Direct"
		outbound := "direct"
		delay := 0

		if probe, ok := a.lastRouteProbeResult(svc.Tag); ok && probe.Success {
			method = probe.MethodLabel
			outbound = probe.MethodTag
			delay = int(probe.LatencyMS)
		} else {
			selected := a.selectFreeAccessStrategyForService(settings, svc, storedStrategies, serviceFallbackCache, map[string]bool{}, transparentTags, false)
			if selected.MethodTag != "" {
				method = selected.MethodLabel
				outbound = selected.MethodTag
				delay = int(selected.LatencyMS)
			}
			if svc.RequiresVPN && selected.MethodKind != "vpn" {
				method = "Direct (no VPN key)"
				outbound = "direct"
				delay = 0
			}
		}
		if delay <= 0 && method != "Direct (no VPN key)" {
			delay = a.cachedRouteServiceDelay(svc)
		}

		services = append(services, map[string]interface{}{
			"tag":      svc.Tag,
			"name":     svc.DisplayName,
			"group":    ServiceBypassGroupTag(svc.Tag),
			"method":   method,
			"outbound": outbound,
			"delay":    delay,
		})
	}

	catchAll := map[string]interface{}{}
	if probe, ok := a.lastRouteProbeCatchAll(); ok {
		catchAll = map[string]interface{}{
			"method":   probe.MethodLabel,
			"outbound": probe.MethodTag,
			"delay":    int(probe.LatencyMS),
		}
	} else if tag := defaultZapretStrategyTag(transparentTags); tag != "" {
		catchAll = map[string]interface{}{
			"method":   FreeAccessOutboundLabel(tag),
			"outbound": tag,
			"delay":    0,
		}
	}

	return map[string]interface{}{
		"success":       true,
		"running":       true,
		"mode":          string(mode),
		"foreignVpn":    mode == RoutingModeExceptRussia,
		"networkEngine": "deep_windows_transparent",
		"services":      services,
		"catchAll":      catchAll,
	}
}

func (a *App) cachedRouteServiceDelay(svc FreeAccessService) int {
	target := strings.TrimSpace(svc.HealthURL)
	if target == "" && len(svc.ProbeURLs) > 0 {
		target = strings.TrimSpace(svc.ProbeURLs[0])
	}
	if target == "" {
		return 0
	}

	now := time.Now()
	a.routeLatencyMu.Lock()
	if a.routeLatencyCache == nil {
		a.routeLatencyCache = make(map[string]routeSummaryLatencyEntry)
	}
	entry := a.routeLatencyCache[svc.Tag]
	if !entry.CheckedAt.IsZero() && now.Sub(entry.CheckedAt) < routeSummaryPingTTL {
		delay := entry.Delay
		a.routeLatencyMu.Unlock()
		return delay
	}
	if entry.InFlight {
		delay := entry.Delay
		a.routeLatencyMu.Unlock()
		return delay
	}
	entry.InFlight = true
	a.routeLatencyCache[svc.Tag] = entry
	delay := entry.Delay
	a.routeLatencyMu.Unlock()

	go a.refreshRouteServiceDelay(svc.Tag, svc.DisplayName, target)
	return delay
}

func (a *App) refreshRouteServiceDelay(tag, name, target string) {
	ctx, cancel := context.WithTimeout(context.Background(), routeSummaryPingTimeout)
	defer cancel()

	client := newQuickCheckHTTPClient(nil)
	client.Timeout = routeSummaryPingTimeout
	result := invokeQuickCheckURL(ctx, client, target)
	delay := 0
	if result.Success && result.TimeMS > 0 {
		delay = int(result.TimeMS)
	}
	if !result.Success {
		a.writeLog(fmt.Sprintf("[RouteSummary] ping failed for %s (%s): %s", name, target, result.Error))
	}

	a.routeLatencyMu.Lock()
	if a.routeLatencyCache == nil {
		a.routeLatencyCache = make(map[string]routeSummaryLatencyEntry)
	}
	a.routeLatencyCache[tag] = routeSummaryLatencyEntry{
		Delay:     delay,
		CheckedAt: time.Now(),
		InFlight:  false,
	}
	a.routeLatencyMu.Unlock()
}

// TestRouteMethods runs the blocked-service method probe without starting the
// main VPN process. It is kept as an internal/manual route-probe API; the home
// screen uses RunClientQuickCheck for user-facing service availability checks.
func (a *App) TestRouteMethods() map[string]interface{} {
	a.waitForInit()

	startedAt := time.Now()
	report, err := a.runRouteProbeDiscovery("manual method probe")
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка проверки методов: %v", err),
		}
	}

	results := []routeProbeServiceResult{}
	if report != nil {
		results = report.Services
	}
	return map[string]interface{}{
		"success":    true,
		"durationMs": time.Since(startedAt).Milliseconds(),
		"services":   results,
	}
}

// RefreshFreeAccessMethods updates the persisted route-method discovery cache
// from Settings without opening the home-screen test modal.
func (a *App) RefreshFreeAccessMethods() map[string]interface{} {
	a.waitForInit()

	startedAt := time.Now()
	report, err := a.runRouteProbeDiscovery("settings refresh")
	if err != nil {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Ошибка обновления методов: %v", err),
		}
	}

	results := []routeProbeServiceResult{}
	durationMs := time.Since(startedAt).Milliseconds()
	if report != nil {
		results = report.Services
		durationMs = report.DurationMS
	}
	return map[string]interface{}{
		"success":      true,
		"durationMs":   durationMs,
		"services":     results,
		"successCount": routeProbeSuccessCount(results),
		"cache":        a.routeProbeCacheSummary(),
	}
}

func fetchClashProxies(client *http.Client) (map[string]clashProxyInfo, error) {
	resp, err := client.Get("http://127.0.0.1:9090/proxies")
	if err != nil {
		return nil, fmt.Errorf("не удалось подключиться к API: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения ответа: %w", err)
	}

	var proxiesResp clashProxiesResponse
	if err := json.Unmarshal(body, &proxiesResp); err != nil {
		return nil, fmt.Errorf("ошибка парсинга API: %w", err)
	}

	return proxiesResp.Proxies, nil
}

func latestProxyDelay(info clashProxyInfo) int {
	if len(info.History) == 0 {
		return 0
	}
	return info.History[len(info.History)-1].Delay
}

func summarizeBypassProxy(proxies map[string]clashProxyInfo, info clashProxyInfo) (string, string, int) {
	active := info.Now
	if active == "" {
		active = info.Name
	}

	methodTag := active
	outbound := active
	delay := latestProxyDelay(info)

	if activeInfo, ok := proxies[active]; ok {
		if delay == 0 {
			delay = latestProxyDelay(activeInfo)
		}
		if active == "auto-select" && activeInfo.Now != "" {
			outbound = activeInfo.Now
		}
	}

	return FreeAccessOutboundLabel(methodTag), outbound, delay
}

// GetCurrentProxy returns current active proxy and its delay
func (a *App) GetCurrentProxy() map[string]interface{} {
	if !a.isRunning {
		return map[string]interface{}{
			"success": false,
		}
	}

	client := &http.Client{Timeout: 5 * time.Second}

	// Get info about proxy selector
	resp, err := client.Get("http://127.0.0.1:9090/proxies/proxy")
	if err != nil {
		return map[string]interface{}{
			"success": false,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return map[string]interface{}{
			"success": false,
		}
	}

	var proxyInfo struct {
		Name string `json:"name"`
		Now  string `json:"now"`
		Type string `json:"type"`
	}

	if err := json.Unmarshal(body, &proxyInfo); err != nil {
		return map[string]interface{}{
			"success": false,
		}
	}

	currentProxy := proxyInfo.Now
	if currentProxy == "" {
		currentProxy = proxyInfo.Name
	}

	// Get delay for current proxy
	delay := 0
	if currentProxy != "" {
		delayResp, err := client.Get(fmt.Sprintf("http://127.0.0.1:9090/proxies/%s/delay?timeout=3000&url=http://www.gstatic.com/generate_204", currentProxy))
		if err == nil {
			defer delayResp.Body.Close()
			delayBody, _ := io.ReadAll(delayResp.Body)
			var delayInfo struct {
				Delay int `json:"delay"`
			}
			if json.Unmarshal(delayBody, &delayInfo) == nil {
				delay = delayInfo.Delay
			}
		}
	}

	return map[string]interface{}{
		"success": true,
		"name":    currentProxy,
		"type":    proxyInfo.Type,
		"delay":   delay,
	}
}
