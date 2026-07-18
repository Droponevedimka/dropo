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

	return a.switchServiceRoute(serviceTag, "auto-select")
}

func (a *App) switchServiceRoute(serviceTag, outboundTag string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	groupTag := ServiceBypassGroupTag(serviceTag)
	deadline := time.Now().Add(5 * time.Second)
	for !loopbackPortReady(ClashAPIPort, 250*time.Millisecond) && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if !loopbackPortReady(ClashAPIPort, 250*time.Millisecond) {
		a.writeLog(fmt.Sprintf("[FreeAccess] cannot switch %s to VPN fallback: Clash API is not ready", groupTag))
		return false
	}
	bodyData, err := json.Marshal(map[string]string{"name": outboundTag})
	if err != nil {
		return false
	}
	body := bytes.NewBuffer(bodyData)
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
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to switch %s to %s: HTTP %d", groupTag, outboundTag, resp.StatusCode))
		return false
	}
	return true
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
