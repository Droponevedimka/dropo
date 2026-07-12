package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (a *App) handleClientQuickCheckFailures(results []clientQuickCheckResult) {
	// Don't react to a test that ran while the strategy engine was being
	// switched: those failures are an artifact of the switch, not of the
	// strategy, and acting on them creates a churn feedback loop.
	if a.isRouteProbeDiscoveryRunning() {
		a.writeLog("[FreeAccess] quick-check failures ignored: strategy discovery is in progress")
		return
	}
	a.mu.Lock()
	running := a.isRunning
	a.mu.Unlock()
	if !running {
		// Without an active VPN session there is no transparent engine to
		// retune; the test is purely informational.
		return
	}

	queued := map[string]bool{}
	for _, result := range results {
		if result.Name == "" || result.Category != "Blocked" {
			continue
		}
		if result.Success && !result.ProxyRescued {
			continue
		}
		serviceTag := clientQuickCheckServiceTag(result.Name)
		if serviceTag == "" || queued[serviceTag] {
			continue
		}
		queued[serviceTag] = true
		if a.switchServiceToVPNFallback(serviceTag) {
			a.writeLog(fmt.Sprintf("[FreeAccess] %s failed TUN quick check; switched service group to VPN fallback", serviceTag))
		}
		a.requestRouteStrategyMaintenance(fmt.Sprintf("service:%s quick-check failure: %s", serviceTag, firstNonEmpty(result.NormalError, result.ProxyError, result.StatusText)))
	}
}

func clientQuickCheckServiceTag(name string) string {
	value := strings.ToLower(strings.TrimSpace(name))
	switch {
	case strings.HasPrefix(value, "discord"):
		return "discord"
	case strings.HasPrefix(value, "youtube"):
		return "youtube"
	case value == "instagram" || value == "facebook":
		return "meta"
	case value == "x":
		return "twitter"
	case value == "linkedin":
		return "linkedin"
	case value == "spotify":
		return "spotify"
	case value == "twitch":
		return "twitch"
	case value == "telegram":
		return "telegram"
	case value == "signal":
		return "signal"
	case strings.HasPrefix(value, "whatsapp"):
		return "whatsapp"
	case value == "facetime":
		return "facetime"
	case value == "viber":
		return "viber"
	case value == "snapchat":
		return "snapchat"
	case value == "tiktok":
		return "tiktok"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "failed"
}

func (a *App) switchServiceToVPNFallback(serviceTag string) bool {
	configPath := ""
	if a.storage != nil {
		configPath = a.storage.ActiveConfigFilePath()
	}
	if configPath == "" {
		return false
	}
	hasVPN, err := configHasVPNProbeCandidates(configPath)
	if err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] VPN fallback check failed for %s: %v", serviceTag, err))
		return false
	}
	if !hasVPN {
		a.writeLog(fmt.Sprintf("[FreeAccess] %s has no VPN fallback candidate; queued free strategy search", serviceTag))
		return false
	}

	client := &http.Client{Timeout: 2 * time.Second}
	groupTag := ServiceBypassGroupTag(serviceTag)
	if !loopbackPortReady(ClashAPIPort, 250*time.Millisecond) {
		a.writeLog(fmt.Sprintf("[FreeAccess] cannot switch %s to VPN fallback: Clash API is not ready", groupTag))
		return false
	}
	body := bytes.NewBufferString(`{"name":"auto-select"}`)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d/proxies/%s", ClashAPIPort, url.PathEscape(groupTag))
	req, err := http.NewRequest(http.MethodPut, endpoint, body)
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to switch %s to VPN fallback: %v", groupTag, err))
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to switch %s to VPN fallback: HTTP %d", groupTag, resp.StatusCode))
		return false
	}
	return true
}

func (a *App) runActiveServiceStrategyMaintenance(serviceTag, reason string) (*routeProbeServiceResult, error) {
	if !a.tryBeginRouteProbeDiscovery() {
		return nil, fmt.Errorf("route method discovery is already running")
	}
	defer a.finishRouteProbeDiscovery()

	a.waitForInit()
	if !a.routeStrategyWorkAllowed() {
		return nil, fmt.Errorf("VPN is stopping")
	}
	if a.storage == nil {
		return nil, fmt.Errorf("storage is not initialized")
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		return nil, fmt.Errorf("free methods are disabled")
	}

	service, ok := findFreeAccessService(serviceTag)
	if !ok {
		return nil, fmt.Errorf("unknown service tag %q", serviceTag)
	}
	if service.RequiresVPN {
		return nil, fmt.Errorf("%s requires VPN and cannot be solved by free methods", service.DisplayName)
	}

	a.writeLog(fmt.Sprintf("[FreeAccess] service strategy maintenance started for %s: %s", service.DisplayName, reason))
	previousTransparent := ""
	if a.zapret != nil {
		previousTransparent = a.zapret.ActiveTag()
	}

	// Don't tear down a transparent engine that is already running just to
	// re-confirm it. Re-probe the service through the live engine first; the
	// original failure may have been transient (a single reset connection), in
	// which case we keep the current strategy and never restart winws.
	if previousTransparent != "" {
		if result, ok := a.probeServiceThroughActiveTransparent(service, previousTransparent); ok {
			a.writeLog(fmt.Sprintf("[FreeAccess] %s already works through active %s (%d ms); keeping it",
				service.DisplayName, result.MethodLabel, result.LatencyMS))
			a.applyServiceStrategySelection(result)
			return &result, nil
		}
	}

	candidates := a.activeServiceMaintenanceCandidates()
	serviceCandidates := candidatesForRouteProbeServiceWithSettings(service, candidates, settings)
	if a.deepWindowsTransparentOnlyActive() {
		serviceCandidates = transparentRouteProbeCandidatesOnly(serviceCandidates)
	}
	if len(serviceCandidates) == 0 {
		return nil, fmt.Errorf("no active free candidates are available")
	}

	result := a.probeServiceCandidates(service, serviceCandidates)
	if result.Success {
		if !a.routeStrategyWorkAllowed() {
			return nil, fmt.Errorf("VPN is stopping")
		}
		a.applyServiceStrategySelection(result)
		return &result, nil
	}
	if previousTransparent != "" && a.zapret != nil && a.routeStrategyWorkAllowed() {
		if err := a.zapret.StartSelected(previousTransparent); err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] failed to restore previous transparent method %s: %v", previousTransparent, err))
		}
	}
	return nil, fmt.Errorf("no working free strategy found for %s: %s", service.DisplayName, result.Error)
}

// probeServiceThroughActiveTransparent tests the service against the transparent
// engine that is already running, without restarting it (no Start/Stop on the
// candidate). It returns a successful result only if every probe target passes,
// which lets the caller keep a working winws instance instead of tearing it down.
func (a *App) probeServiceThroughActiveTransparent(service FreeAccessService, tag string) (routeProbeServiceResult, bool) {
	if a.zapret == nil || tag == "" || a.zapret.ActiveTag() != tag {
		return routeProbeServiceResult{}, false
	}
	label := FreeAccessOutboundLabel(tag)
	candidate := routeProbeCandidate{
		Tag:       tag,
		Label:     label,
		Kind:      "transparent",
		Client:    newDirectHTTPClient(),
		Available: true,
		// Deliberately no Start/Stop: the engine is already up and must stay up.
	}
	item := a.probeSingleCandidate(service, candidate)
	if !item.Success {
		return routeProbeServiceResult{}, false
	}
	return routeProbeServiceResult{
		Tag:          service.Tag,
		Name:         service.DisplayName,
		MethodTag:    tag,
		MethodLabel:  label,
		MethodKind:   "transparent",
		LatencyMS:    item.LatencyMS,
		Success:      true,
		CandidateNum: 1,
	}, true
}

func (a *App) deepWindowsTransparentOnlyActive() bool {
	if a == nil {
		return false
	}
	status := a.currentNetworkModeStatus()
	if status.Active != NetworkModeDeepWindows {
		return false
	}
	a.mu.Lock()
	running := (a.isRunning || a.isStarting) && !a.stoppedManually && !a.vpnStopping.Load()
	hasLocalProxyEndpoint := a.cmd != nil
	a.mu.Unlock()
	return running && !hasLocalProxyEndpoint
}

func transparentRouteProbeCandidatesOnly(candidates []routeProbeCandidate) []routeProbeCandidate {
	filtered := make([]routeProbeCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Kind == "transparent" {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func (a *App) activeServiceMaintenanceCandidates() []routeProbeCandidate {
	candidates := []routeProbeCandidate{}
	activeTags := []string{}
	if a.byeDPI != nil {
		activeTags = append(activeTags, a.byeDPI.ActiveTags()...)
	}
	for _, tag := range uniqueStrings(activeTags) {
		candidate, err := newFreeRouteProbeCandidate(tag)
		if err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] %s is not available for service maintenance: %v", tag, err))
			continue
		}
		candidates = append(candidates, candidate)
	}

	if a.zapret != nil {
		for _, strategy := range a.zapret.AvailableStrategies() {
			strategy := strategy
			candidates = append(candidates, routeProbeCandidate{
				Tag:       strategy.Tag,
				Label:     strategy.Label,
				Kind:      "transparent",
				Client:    newDirectHTTPClient(),
				Available: true,
				Start: func() error {
					if !a.routeStrategyWorkAllowed() {
						return fmt.Errorf("VPN is stopping")
					}
					return a.zapret.StartSelected(strategy.Tag)
				},
			})
		}
	}
	return candidates
}

func (a *App) applyServiceStrategySelection(result routeProbeServiceResult) {
	configPath := ""
	if a.storage != nil {
		configPath = a.storage.ActiveConfigFilePath()
	}
	if configPath != "" {
		if config, err := readJSONConfig(configPath); err == nil {
			if applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{result}) {
				if err := writeJSONConfig(configPath, config); err != nil {
					a.writeLog(fmt.Sprintf("[FreeAccess] failed to persist %s strategy in active config: %v", result.Name, err))
				}
			}
		}
	}

	if result.MethodKind == "transparent" {
		a.applyTransparentRouteProbeSelection([]routeProbeServiceResult{result})
	}
	a.rememberRouteProbeResults([]routeProbeServiceResult{result})
	if err := a.mergeFreeAccessStrategySelection(result); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to save %s strategy selection: %v", result.Name, err))
	}
}

func (a *App) mergeFreeAccessStrategySelection(result routeProbeServiceResult) error {
	path := a.freeAccessStrategyPath()
	if path == "" || !result.Success || result.MethodTag == "" {
		return nil
	}
	existing := freeAccessStrategyFile{
		Version:  freeAccessStrategyVersion,
		Services: []freeAccessStrategySelection{},
	}
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		_ = json.Unmarshal(data, &existing)
		if existing.Version != freeAccessStrategyVersion {
			existing = freeAccessStrategyFile{Version: freeAccessStrategyVersion}
		}
	}
	selection := freeAccessStrategySelection{
		Tag:         result.Tag,
		Name:        result.Name,
		MethodTag:   result.MethodTag,
		MethodLabel: result.MethodLabel,
		MethodKind:  result.MethodKind,
		Source:      "maintenance",
		LatencyMS:   result.LatencyMS,
	}
	replaced := false
	for i := range existing.Services {
		if existing.Services[i].Tag == selection.Tag {
			existing.Services[i] = selection
			replaced = true
			break
		}
	}
	if !replaced {
		existing.Services = append(existing.Services, selection)
	}
	existing.Version = freeAccessStrategyVersion
	existing.UpdatedAt = time.Now()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func findFreeAccessService(tag string) (FreeAccessService, bool) {
	for _, svc := range DefaultFreeAccessServices {
		if svc.Tag == tag {
			return svc, true
		}
	}
	return FreeAccessService{}, false
}
