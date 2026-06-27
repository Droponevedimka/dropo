package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	routeProbeCacheFileName = "route_probe_cache.json"
	routeProbeCacheVersion  = 1
	routeProbeCacheTTL      = 24 * time.Hour
)

type routeProbeCacheFile struct {
	Version    int                       `json:"version"`
	UpdatedAt  time.Time                 `json:"updatedAt"`
	DurationMS int64                     `json:"durationMs"`
	Services   []routeProbeServiceResult `json:"services"`
}

func (a *App) startRouteProbeDiscoveryInBackground(reason string) {
	if a.basePath == "" || a.isShuttingDown() {
		return
	}
	go func() {
		report, err := a.runRouteProbeDiscovery(reason)
		if err != nil {
			a.writeLog(fmt.Sprintf("[RouteProbe] background discovery skipped/failed (%s): %v", reason, err))
			return
		}
		successCount := routeProbeSuccessCount(report.Services)
		a.writeLog(fmt.Sprintf("[RouteProbe] background discovery completed (%s): %d/%d service(s) have a route",
			reason, successCount, len(report.Services)))
	}()
}

func (a *App) startRouteStrategyMaintenanceListener() {
	if a.routeStrategyJobs == nil {
		return
	}
	if !a.routeStrategyLoop.CompareAndSwap(false, true) {
		return
	}
	go func() {
		a.writeLog("[FreeAccess] strategy maintenance listener started")
		for reason := range a.routeStrategyJobs {
			if a.isShuttingDown() {
				return
			}
			serviceTag, serviceReason := parseServiceStrategyMaintenanceReason(reason)
			// Dequeuing the job counts as the one search this service gets for
			// the current VPN session, no matter which branch handles it below.
			if serviceTag != "" {
				a.finishRouteStrategyService(serviceTag)
			}
			a.mu.Lock()
			stopping := a.stoppedManually || a.vpnStopping.Load()
			running := (a.isRunning || a.isStarting) && !stopping
			a.mu.Unlock()
			if stopping {
				a.writeLog(fmt.Sprintf("[FreeAccess] strategy maintenance skipped while VPN is stopping: %s", reason))
				continue
			}
			if serviceTag != "" {
				if running {
					// Per-service retune: the composed winws engine lets each
					// service keep its own method, so a failing service is
					// retuned on its own ladder (cache → next working → VPN /
					// direct) without disturbing the others. The dequeue above
					// already enforced one search per service per session.
					if a.deepWindowsTransparentOnlyActive() && a.zapret != nil && a.zapret.ActiveTag() == composedStrategyTag {
						if err := a.retunePerServiceStrategy(serviceTag, serviceReason); err != nil {
							a.writeLog(fmt.Sprintf("[FreeAccess] per-service retune failed (%s): %v", serviceReason, err))
						}
						a.sleepRouteStrategyMaintenancePause()
						continue
					}
					result, err := a.runActiveServiceStrategyMaintenance(serviceTag, serviceReason)
					if err != nil {
						a.writeLog(fmt.Sprintf("[FreeAccess] service strategy maintenance failed (%s): %v", serviceReason, err))
					} else if result != nil && result.Success {
						a.writeLog(fmt.Sprintf("[FreeAccess] service strategy maintenance selected %s for %s (%d ms)",
							result.MethodLabel, result.Name, result.LatencyMS))
					}
					// Unhurried: pace consecutive searches so the single
					// transparent engine is never thrashed by a burst of jobs.
					a.sleepRouteStrategyMaintenancePause()
					continue
				}
				reason = serviceReason
			} else if running {
				a.writeLog(fmt.Sprintf("[FreeAccess] strategy maintenance deferred while VPN is active: %s", reason))
				continue
			}
			report, err := a.runRouteProbeDiscovery("maintenance: " + reason)
			if err != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] strategy maintenance failed (%s): %v", reason, err))
				continue
			}
			if report != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] strategy maintenance completed (%s): %d/%d service(s)",
					reason, routeProbeSuccessCount(report.Services), len(report.Services)))
			}
		}
	}()
}

func (a *App) requestRouteStrategyMaintenance(reason string) {
	if a.routeStrategyJobs == nil || a.isShuttingDown() {
		return
	}
	if reason == "" {
		reason = "unspecified"
	}
	if !a.routeStrategyWorkAllowed() {
		a.writeLog("[FreeAccess] strategy maintenance skipped while VPN is stopping: " + reason)
		return
	}
	serviceTag, _ := parseServiceStrategyMaintenanceReason(reason)
	if serviceTag != "" && !a.markRouteStrategyQueued(serviceTag) {
		a.writeLog("[FreeAccess] strategy maintenance skipped for " + serviceTag + " (already searched this session or queued)")
		return
	}
	select {
	case a.routeStrategyJobs <- reason:
		a.writeLog("[FreeAccess] strategy maintenance queued: " + reason)
	default:
		// Could not enqueue: release the de-dup reservation so a later failure
		// can retry instead of being silently suppressed forever.
		if serviceTag != "" {
			a.releaseRouteStrategyQueued(serviceTag)
		}
		a.writeLog("[FreeAccess] strategy maintenance queue is full; skipped: " + reason)
	}
}

const routeStrategyMaintenancePause = 3 * time.Second

// sleepRouteStrategyMaintenancePause paces consecutive searches without blocking
// shutdown for the full interval.
func (a *App) sleepRouteStrategyMaintenancePause() {
	deadline := time.Now().Add(routeStrategyMaintenancePause)
	for time.Now().Before(deadline) {
		if !a.routeStrategyWorkAllowed() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (a *App) routeStrategyWorkAllowed() bool {
	if a == nil || a.isShuttingDown() {
		return false
	}
	if a.vpnStopping.Load() {
		return false
	}
	a.mu.Lock()
	stopping := a.stoppedManually
	a.mu.Unlock()
	return !stopping
}

// resetRouteStrategySession clears the per-session "searched once" state so the
// next VPN session is allowed exactly one strategy search per service again.
func (a *App) resetRouteStrategySession() {
	a.routeStrategyMu.Lock()
	a.routeStrategyAttempted = nil
	a.routeStrategyQueued = nil
	a.transparentReselectionDone = false
	a.routeStrategyMu.Unlock()
}

// beginTransparentReselectionOncePerSession returns true exactly once per VPN
// session, gating the single global transparent strategy reselection.
func (a *App) beginTransparentReselectionOncePerSession() bool {
	a.routeStrategyMu.Lock()
	defer a.routeStrategyMu.Unlock()
	if a.transparentReselectionDone {
		return false
	}
	a.transparentReselectionDone = true
	return true
}

// markRouteStrategyQueued reserves a service for a single strategy search. It
// returns false when the service was already searched this session or is still
// queued, which is how the one-search-per-service rule is enforced.
func (a *App) markRouteStrategyQueued(serviceTag string) bool {
	a.routeStrategyMu.Lock()
	defer a.routeStrategyMu.Unlock()
	if a.routeStrategyAttempted[serviceTag] || a.routeStrategyQueued[serviceTag] {
		return false
	}
	if a.routeStrategyQueued == nil {
		a.routeStrategyQueued = map[string]bool{}
	}
	a.routeStrategyQueued[serviceTag] = true
	return true
}

func (a *App) releaseRouteStrategyQueued(serviceTag string) {
	a.routeStrategyMu.Lock()
	delete(a.routeStrategyQueued, serviceTag)
	a.routeStrategyMu.Unlock()
}

// finishRouteStrategyService records that a service has used its single search
// for this session and drops it from the queued set.
func (a *App) finishRouteStrategyService(serviceTag string) {
	a.routeStrategyMu.Lock()
	defer a.routeStrategyMu.Unlock()
	if a.routeStrategyAttempted == nil {
		a.routeStrategyAttempted = map[string]bool{}
	}
	a.routeStrategyAttempted[serviceTag] = true
	delete(a.routeStrategyQueued, serviceTag)
}

func parseServiceStrategyMaintenanceReason(reason string) (string, string) {
	reason = strings.TrimSpace(reason)
	if !strings.HasPrefix(reason, "service:") {
		return "", reason
	}
	rest := strings.TrimSpace(strings.TrimPrefix(reason, "service:"))
	if rest == "" {
		return "", reason
	}
	tag := rest
	detail := ""
	if idx := strings.IndexAny(rest, " \t|:;"); idx >= 0 {
		tag = strings.TrimSpace(rest[:idx])
		detail = strings.TrimSpace(rest[idx+1:])
	}
	if tag == "" {
		return "", reason
	}
	if detail == "" {
		detail = "service " + tag
	}
	return tag, detail
}

func (a *App) runRouteProbeDiscovery(reason string) (*routeProbeReport, error) {
	if !a.tryBeginRouteProbeDiscovery() {
		return nil, errors.New("route method discovery is already running")
	}
	defer a.finishRouteProbeDiscovery()

	a.waitForInit()
	if a.isShuttingDown() {
		return nil, errors.New("application is shutting down")
	}

	a.mu.Lock()
	isRunning := a.isRunning
	isStarting := a.isStarting
	a.mu.Unlock()
	if isRunning || isStarting {
		return nil, errors.New("VPN is active or starting")
	}
	if a.singboxPath == "" || !fileExists(a.singboxPath) {
		return nil, errors.New("sing-box is not available")
	}

	startedAt := time.Now()
	a.writeLog(fmt.Sprintf("[RouteProbe] discovery started: %s", reason))
	a.cleanupManagedSidecarOrphans("before route method discovery")
	defer func() {
		a.stopFreeAccess()
		a.stopXrayBridge()
		a.cleanupManagedSidecarOrphans("after route method discovery")
		a.writeLog(fmt.Sprintf("[RouteProbe] discovery cleanup finished in %s", time.Since(startedAt).Round(time.Millisecond)))
	}()

	if err := a.ensureActiveConfigForStart(); err != nil {
		return nil, fmt.Errorf("prepare active config: %w", err)
	}
	configPath, err := a.getActiveConfigPath()
	if err != nil || configPath == "" {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("active config is not available")
	}

	if err := a.startXrayBridge(); err != nil {
		return nil, fmt.Errorf("start Xray bridge: %w", err)
	}
	activeFreeAccessTags := a.startFreeAccessForConfig(configPath)
	if err := a.filterActiveFreeAccessOutbounds(configPath, activeFreeAccessTags); err != nil {
		a.writeLog(fmt.Sprintf("[RouteProbe] failed to filter active free-access outbounds: %v", err))
	}

	report, err := a.runRouteProbeAndApply(configPath, activeFreeAccessTags, reason)
	if err != nil {
		return report, err
	}
	if err := a.saveRouteProbeCache(report); err != nil {
		a.writeLog(fmt.Sprintf("[RouteProbe] failed to save discovery cache: %v", err))
	}
	if report != nil {
		if err := a.saveFreeAccessStrategySelections(report.Services); err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] failed to save strategy selections: %v", err))
		}
	}
	return report, nil
}

func (a *App) tryBeginRouteProbeDiscovery() bool {
	a.routeProbeRunMu.Lock()
	defer a.routeProbeRunMu.Unlock()
	if a.routeProbeRunning {
		return false
	}
	a.routeProbeRunning = true
	a.routeProbeDone = make(chan struct{})
	return true
}

func (a *App) finishRouteProbeDiscovery() {
	a.routeProbeRunMu.Lock()
	done := a.routeProbeDone
	a.routeProbeRunning = false
	a.routeProbeDone = nil
	a.routeProbeRunMu.Unlock()
	if done != nil {
		close(done)
	}
}

func (a *App) isRouteProbeDiscoveryRunning() bool {
	a.routeProbeRunMu.Lock()
	defer a.routeProbeRunMu.Unlock()
	return a.routeProbeRunning
}

func (a *App) waitForRouteProbeDiscovery(timeout time.Duration) bool {
	a.routeProbeRunMu.Lock()
	running := a.routeProbeRunning
	done := a.routeProbeDone
	a.routeProbeRunMu.Unlock()
	if !running || done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func (a *App) routeProbeCachePath() string {
	if a.storage != nil {
		return filepath.Join(a.storage.GetResourcesPath(), routeProbeCacheFileName)
	}
	if a.basePath != "" {
		return filepath.Join(a.basePath, "resources", routeProbeCacheFileName)
	}
	return ""
}

func (a *App) loadRouteProbeCache() (*routeProbeCacheFile, error) {
	path := a.routeProbeCachePath()
	if path == "" {
		return nil, errors.New("route probe cache path is not available")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cache routeProbeCacheFile
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, err
	}
	if cache.Version != routeProbeCacheVersion {
		return nil, fmt.Errorf("unsupported route probe cache version %d", cache.Version)
	}
	return &cache, nil
}

func (a *App) saveRouteProbeCache(report *routeProbeReport) error {
	if report == nil {
		return nil
	}
	path := a.routeProbeCachePath()
	if path == "" {
		return errors.New("route probe cache path is not available")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	cache := routeProbeCacheFile{
		Version:    routeProbeCacheVersion,
		UpdatedAt:  time.Now(),
		DurationMS: report.DurationMS,
		Services:   append([]routeProbeServiceResult(nil), report.Services...),
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	a.writeLog(fmt.Sprintf("[RouteProbe] discovery cache saved: %s", path))
	return nil
}

func (a *App) applyCachedRouteProbeToConfig(configPath string, allowStale bool, activeFreeAccessTags []string) (bool, bool, error) {
	cache, err := a.loadRouteProbeCache()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, false, err
		}
		return false, false, nil
	}
	if len(cache.Services) == 0 {
		return false, routeProbeCacheFresh(cache), nil
	}
	services := a.usableCachedRouteProbeResults(cache.Services, activeFreeAccessTags)
	if len(services) == 0 {
		return false, routeProbeCacheFresh(cache), nil
	}

	fresh := routeProbeCacheFresh(cache)
	if !fresh && !allowStale {
		return false, false, nil
	}

	config, err := readJSONConfig(configPath)
	if err != nil {
		return false, fresh, err
	}
	if changed := applyRouteProbeSelectionsToConfig(config, services); changed {
		if err := writeJSONConfig(configPath, config); err != nil {
			return false, fresh, err
		}
	}
	a.applyTransparentRouteProbeSelection(services)
	a.rememberRouteProbeResults(services)
	return true, fresh, nil
}

func (a *App) usableCachedRouteProbeResults(results []routeProbeServiceResult, activeFreeAccessTags []string) []routeProbeServiceResult {
	if len(results) == 0 {
		return nil
	}

	activeProxyTags := map[string]bool{}
	for _, tag := range activeFreeAccessTags {
		activeProxyTags[tag] = true
	}
	freeTags := map[string]bool{}
	for _, tag := range FreeAccessMethodTags() {
		freeTags[tag] = true
	}
	transparentTags := map[string]bool{}
	if a.zapret != nil {
		for _, strategy := range a.zapret.AvailableStrategies() {
			transparentTags[strategy.Tag] = true
		}
	}

	filtered := make([]routeProbeServiceResult, 0, len(results))
	for _, result := range results {
		if result.Success && result.MethodTag != "" {
			if result.MethodKind == "transparent" {
				if !transparentTags[result.MethodTag] {
					continue
				}
			} else if result.MethodKind != "vpn" && freeTags[result.MethodTag] && !activeProxyTags[result.MethodTag] {
				continue
			}
		}
		filtered = append(filtered, result)
	}
	return filtered
}

func routeProbeCacheFresh(cache *routeProbeCacheFile) bool {
	if cache == nil || cache.UpdatedAt.IsZero() {
		return false
	}
	return time.Since(cache.UpdatedAt) <= routeProbeCacheTTL
}

func routeProbeSuccessCount(results []routeProbeServiceResult) int {
	count := 0
	for _, result := range results {
		if result.Success {
			count++
		}
	}
	return count
}

func (a *App) routeProbeCacheSummary() map[string]interface{} {
	summary := map[string]interface{}{
		"exists":  false,
		"fresh":   false,
		"running": a.isRouteProbeDiscoveryRunning(),
	}
	cache, err := a.loadRouteProbeCache()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			summary["error"] = err.Error()
		}
		return summary
	}
	summary["exists"] = true
	summary["fresh"] = routeProbeCacheFresh(cache)
	summary["updatedAt"] = cache.UpdatedAt.Format(time.RFC3339)
	summary["ageSeconds"] = int64(time.Since(cache.UpdatedAt).Seconds())
	summary["durationMs"] = cache.DurationMS
	summary["serviceCount"] = len(cache.Services)
	summary["successCount"] = routeProbeSuccessCount(cache.Services)
	return summary
}
