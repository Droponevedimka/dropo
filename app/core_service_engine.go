package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// isFreeAccessFallbackTag reports whether a cached selection is a VPN/direct
// fallback decision rather than a transparent desync method.
func isFreeAccessFallbackTag(tag string) bool {
	return tag == FreeAccessMethodVPN || tag == FreeAccessMethodDirect
}

const (
	serviceStrategyCacheFileName = "service_strategy_cache.json"
	serviceStrategyCacheVersion  = 1
	serviceHostlistDirName       = "service-hostlists"
)

type serviceStrategyCacheFile struct {
	Version           int                                  `json:"version"`
	StrategiesVersion int                                  `json:"strategiesVersion"`
	UpdatedAt         time.Time                            `json:"updatedAt"`
	Services          map[string]serviceStrategyCacheEntry `json:"services"`
}

type serviceStrategyCacheEntry struct {
	MethodTag string    `json:"methodTag"`
	Source    string    `json:"source"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (a *App) serviceStrategyCachePath() string {
	if a.storage != nil {
		return filepath.Join(a.storage.GetResourcesPath(), serviceStrategyCacheFileName)
	}
	if a.basePath != "" {
		return filepath.Join(a.basePath, ResourcesFolder, serviceStrategyCacheFileName)
	}
	return ""
}

func (a *App) loadServiceStrategyCache() map[string]serviceStrategyCacheEntry {
	out := map[string]serviceStrategyCacheEntry{}
	path := a.serviceStrategyCachePath()
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var file serviceStrategyCacheFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != serviceStrategyCacheVersion {
		return out
	}
	// A newer strategy file means new/reordered methods — ignore the stale cache
	// so every service is re-searched against the improved ladders.
	if file.StrategiesVersion != serviceStrategiesVersion() {
		return out
	}
	for tag, entry := range file.Services {
		if entry.MethodTag == "" {
			continue
		}
		// Keep VPN/direct fallback decisions so a service that no transparent
		// method could fix is not re-searched on every launch.
		if isFreeAccessFallbackTag(entry.MethodTag) {
			out[tag] = entry
			continue
		}
		// Drop entries whose method no longer exists in the ranked registry.
		if _, ok := findServiceBypassMethod(tag, entry.MethodTag); !ok {
			continue
		}
		out[tag] = entry
	}
	return out
}

func (a *App) cacheServiceMethod(serviceTag, methodTag, source string) {
	path := a.serviceStrategyCachePath()
	if path == "" || serviceTag == "" || methodTag == "" {
		return
	}
	a.serviceStrategyCacheMu.Lock()
	defer a.serviceStrategyCacheMu.Unlock()

	file := serviceStrategyCacheFile{Version: serviceStrategyCacheVersion, Services: map[string]serviceStrategyCacheEntry{}}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &file)
		if file.Version != serviceStrategyCacheVersion || file.Services == nil || file.StrategiesVersion != serviceStrategiesVersion() {
			file = serviceStrategyCacheFile{Version: serviceStrategyCacheVersion, Services: map[string]serviceStrategyCacheEntry{}}
		}
	}
	file.StrategiesVersion = serviceStrategiesVersion()
	file.Services[serviceTag] = serviceStrategyCacheEntry{MethodTag: methodTag, Source: source, UpdatedAt: time.Now()}
	file.UpdatedAt = time.Now()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	if data, err := json.MarshalIndent(file, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0644)
	}
}

func (a *App) serviceHostlistDir() string {
	base := a.basePath
	if a.storage != nil {
		base = a.storage.GetResourcesPath()
	} else if base != "" {
		base = filepath.Join(base, ResourcesFolder)
	}
	return filepath.Join(base, serviceHostlistDirName)
}

func (a *App) zapretBinDir() string {
	return a.binDir()
}

// enabledTransparentServices returns the non-VPN free-access services that the
// transparent engine should handle this session.
func (a *App) enabledTransparentServices() []FreeAccessService {
	services := make([]FreeAccessService, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN || len(svc.DomainSuffixes) == 0 {
			continue
		}
		services = append(services, svc)
	}
	return services
}

// resolveServiceSelections builds the per-service method selection for the
// composed engine: a cached method when present (and still valid), otherwise the
// top-ranked method. Services without a cache entry are returned in needSearch
// so the first-run search can find their first working method.
func (a *App) resolveServiceSelections(dir string, cache map[string]serviceStrategyCacheEntry) (map[string]serviceWinwsSelection, []string) {
	selections := map[string]serviceWinwsSelection{}
	needSearch := []string{}
	for _, svc := range a.enabledTransparentServices() {
		// Services with no free desync method (IP/protocol-blocked: Meta,
		// WhatsApp) are never composed into the engine or searched — they rely
		// on the VPN subscription (or stay direct). This is the "don't even try
		// to pick a strategy for VPN-only services" rule.
		if !serviceHasFreeBypass(svc.Tag) {
			continue
		}
		// A service already resolved to VPN/direct keeps that decision and is
		// not desynced or re-searched.
		if entry, ok := cache[svc.Tag]; ok && isFreeAccessFallbackTag(entry.MethodTag) {
			continue
		}
		hostlistPath, err := ensureServiceHostlist(dir, svc)
		if err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] hostlist for %s failed: %v", svc.Tag, err))
			continue
		}
		ranked := rankedMethodsForService(svc.Tag)
		if len(ranked) == 0 {
			continue
		}
		method := ranked[0]
		if entry, ok := cache[svc.Tag]; ok {
			if m, ok := findServiceBypassMethod(svc.Tag, entry.MethodTag); ok {
				method = m
			} else {
				needSearch = append(needSearch, svc.Tag)
			}
		} else {
			needSearch = append(needSearch, svc.Tag)
		}
		selections[svc.Tag] = serviceWinwsSelection{ServiceTag: svc.Tag, HostlistPath: hostlistPath, Method: method}
	}
	return selections, needSearch
}

// orderedSelections returns selections in stable service order for deterministic
// winws composition.
func (a *App) orderedSelections(selections map[string]serviceWinwsSelection) []serviceWinwsSelection {
	ordered := make([]serviceWinwsSelection, 0, len(selections))
	for _, svc := range DefaultFreeAccessServices {
		if sel, ok := selections[svc.Tag]; ok {
			ordered = append(ordered, sel)
		}
	}
	return ordered
}

func (a *App) composeAndStartServiceEngine(selections map[string]serviceWinwsSelection) error {
	if a.zapret == nil {
		return fmt.Errorf("zapret manager is not initialized")
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	ordered := a.orderedSelections(selections)
	args := composeServiceWinwsArgs(ordered, a.zapretBinDir())
	if len(args) == 0 {
		return fmt.Errorf("no per-service profiles to compose")
	}
	// Detailed per-connection winws diagnostics (hostlist matches, desync
	// decisions) are very noisy, so prefer a standalone file when possible.
	if a.winwsDebugEnabled() {
		if debugPath, err := a.prepareServiceWinwsDebugLog(); err == nil && debugPath != "" {
			args = append([]string{"--debug=@" + debugPath}, args...)
			a.writeLog(fmt.Sprintf("[FreeAccess] winws packet debug ENABLED: %s", debugPath))
		} else {
			args = append([]string{"--debug=1"}, args...)
			a.writeLog(fmt.Sprintf("[FreeAccess] winws packet debug ENABLED in app log; debug file unavailable: %v", err))
		}
	}
	return a.zapret.StartComposedStrategy("Per-service bypass", args)
}

// winwsDebugEnabled turns on verbose winws packet logging. It
// can be enabled by the developer (DROPO_ZAPRET_PACKET_DEBUG=1) or, for a
// non-technical client, by dropping a file named "winws-debug.txt" next to the
// app executable.
func (a *App) winwsDebugEnabled() bool {
	if zapretPacketDebugEnabled() {
		return true
	}
	marker := a.winwsDebugMarkerPath()
	return marker != "" && fileExists(marker)
}

func (a *App) winwsDebugMarkerPath() string {
	if a.basePath == "" {
		return ""
	}
	return filepath.Join(a.basePath, "winws-debug.txt")
}

func (a *App) prepareServiceWinwsDebugLog() (string, error) {
	if a == nil {
		return "", fmt.Errorf("app is not initialized")
	}
	if marker := a.winwsDebugMarkerPath(); marker != "" && fileExists(marker) {
		path := filepath.Join(a.basePath, "winws-debug.log")
		_ = os.Remove(path)
		return path, nil
	}
	if a.zapret == nil {
		return "", fmt.Errorf("zapret manager is not initialized")
	}
	return a.zapret.prepareDebugLog("per-service")
}

// startDeepWindowsPerServiceEngine composes and starts the shared winws engine
// from the per-service selections (cache or top-ranked) and then, on first run,
// searches the first working method for any service that has no cached choice.
// Subsequent starts are instant because every service hits the cache.
func (a *App) startDeepWindowsPerServiceEngine(busyID string) error {
	if a == nil || a.zapret == nil || a.storage == nil {
		return nil
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		a.writeLog("[FreeAccess] per-service engine skipped: free methods disabled")
		return nil
	}

	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	selections, needSearch := a.resolveServiceSelections(dir, cache)
	if len(selections) == 0 {
		return fmt.Errorf("no transparent services available to compose")
	}

	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return fmt.Errorf("start per-service engine: %w", err)
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] per-service engine started for %d service(s); %d need first-run search",
		len(selections), len(needSearch)))
	if !a.winwsDebugEnabled() {
		if marker := a.winwsDebugMarkerPath(); marker != "" {
			a.writeLog(fmt.Sprintf("[FreeAccess] for detailed winws diagnostics: create empty file %q next to dropo.exe, reconnect, then send %q", marker, filepath.Join(a.basePath, "winws-debug.log")))
		} else {
			a.writeLog("[FreeAccess] for detailed winws diagnostics: set DROPO_ZAPRET_PACKET_DEBUG=1 and reconnect")
		}
	}

	if len(needSearch) > 0 {
		a.firstRunServiceSearch(busyID, selections, needSearch)
	} else {
		a.logServiceStrategySummary("loaded from cache")
	}
	return nil
}

// serviceDisplayNameForTag maps a service tag to its human label (for status UI).
func serviceDisplayNameForTag(tag string) string {
	for _, svc := range DefaultFreeAccessServices {
		if svc.Tag == tag {
			if svc.DisplayName != "" {
				return svc.DisplayName
			}
			return svc.Tag
		}
	}
	return tag
}

// serviceSearchStatusList renders a short, status-bar-friendly list of the
// services currently being searched (caps the length so it stays readable).
func serviceSearchStatusList(tags []string) string {
	const max = 4
	names := make([]string, 0, len(tags)+1)
	for i, t := range tags {
		if i >= max {
			names = append(names, fmt.Sprintf("и ещё %d", len(tags)-max))
			break
		}
		names = append(names, serviceDisplayNameForTag(t))
	}
	return strings.Join(names, ", ")
}

// startComposedTransparentEngine runs the per-service winws desync engine in
// hybrid (Compatibility TUN + subscription) mode. sing-box owns routing and the
// bypass groups are urltest [direct, VPN]; winws desyncs the 'direct' path so
// desync-solvable services ride free and the rest auto-fall to the VPN. It does
// NOT run the first-run search (that probes via a direct client, which under TUN
// is ambiguous) — it composes from the cache or top-ranked method and lets the
// urltest groups pick direct-vs-VPN per service.
func (a *App) startComposedTransparentEngine() error {
	if a == nil || a.zapret == nil || !a.zapret.IsInstalled() || a.storage == nil {
		return nil
	}
	if !FreeMethodsAllowed(a.storage.GetAppSettings()) {
		return nil
	}
	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	selections, _ := a.resolveServiceSelections(dir, cache)
	if len(selections) == 0 {
		return nil
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return err
	}
	a.writeLog(fmt.Sprintf("[NetworkMode] hybrid: winws desync engine running alongside sing-box TUN for %d service(s)", len(selections)))
	return nil
}

// firstRunServiceSearch finds, for each uncached service, the first ranked
// method that works. It is round-based: round R sets every still-failing service
// to its method[R] and recomposes the engine ONCE, then probes all of them in
// parallel. So the whole search costs at most (ladder length) restarts — not
// (services × methods) — keeping first enable bounded even with many services.
// Round 0 reuses the already-running top-method engine without a restart.
func (a *App) firstRunServiceSearch(busyID string, selections map[string]serviceWinwsSelection, needSearch []string) {
	maxRounds := 0
	for _, tag := range needSearch {
		if n := len(rankedMethodsForService(tag)); n > maxRounds {
			maxRounds = n
		}
	}

	pending := append([]string{}, needSearch...)
	for round := 0; round < maxRounds && len(pending) > 0; round++ {
		if !a.routeStrategyWorkAllowed() {
			return
		}
		if busyID != "" {
			a.updateBusy(busyID, fmt.Sprintf("Ищем рабочий обход блокировки: %s", serviceSearchStatusList(pending)))
		}
		if round > 0 {
			for _, tag := range pending {
				ranked := rankedMethodsForService(tag)
				if round < len(ranked) {
					selections[tag] = serviceWinwsSelection{ServiceTag: tag, HostlistPath: selections[tag].HostlistPath, Method: ranked[round]}
				}
			}
			if err := a.composeAndStartServiceEngine(selections); err != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] first-run round %d recompose failed: %v", round, err))
				return
			}
		}

		failing := a.probeServicesThroughEngine(pending)
		next := make([]string, 0, len(pending))
		for _, tag := range pending {
			if !failing[tag] {
				a.cacheServiceMethod(tag, selections[tag].Method.Tag, "first-run")
				a.writeLog(fmt.Sprintf("[FreeAccess] %s: working method = %s", tag, selections[tag].Method.Label))
				continue
			}
			if round+1 < len(rankedMethodsForService(tag)) {
				next = append(next, tag)
			} else {
				a.writeLog(fmt.Sprintf("[FreeAccess] %s: no transparent method worked; using VPN/direct fallback", tag))
				a.applyServiceFreeFallback(tag)
			}
		}
		pending = next
	}

	// Lock in whatever each service ended on.
	if !a.routeStrategyWorkAllowed() {
		return
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to re-compose engine after first-run search: %v", err))
	}
	a.logServiceStrategySummary("first-run search complete")
}

// logServiceStrategySummary emits one line listing the chosen method per service
// (or its VPN/direct fallback). This is the report we use to promote a client's
// proven-working methods into the shipped defaults in service_strategies.json.
func (a *App) logServiceStrategySummary(context string) {
	cache := a.loadServiceStrategyCache()
	parts := make([]string, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			continue
		}
		if entry, ok := cache[svc.Tag]; ok && entry.MethodTag != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", svc.Tag, entry.MethodTag))
		} else {
			parts = append(parts, svc.Tag+"=?")
		}
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] STRATEGY SUMMARY (%s): %s", context, strings.Join(parts, " ")))
}

// searchServiceStrategy tries each ranked method for one service (skipping the
// one currently selected, which already failed), recomposing the shared engine
// with that service switched to the candidate and probing just that service.
// Other services keep their current method, so they are only briefly disrupted.
func (a *App) searchServiceStrategy(serviceTag string, selections map[string]serviceWinwsSelection) (ServiceBypassMethod, bool) {
	current := selections[serviceTag]
	for _, method := range rankedMethodsForService(serviceTag) {
		if method.Tag == current.Method.Tag {
			continue
		}
		if !a.routeStrategyWorkAllowed() {
			break
		}
		trial := map[string]serviceWinwsSelection{}
		for k, v := range selections {
			trial[k] = v
		}
		trial[serviceTag] = serviceWinwsSelection{ServiceTag: serviceTag, HostlistPath: current.HostlistPath, Method: method}
		if err := a.composeAndStartServiceEngine(trial); err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] %s: trial %s failed to start: %v", serviceTag, method.Label, err))
			continue
		}
		if !a.probeServicesThroughEngine([]string{serviceTag})[serviceTag] {
			return method, true
		}
	}
	return ServiceBypassMethod{}, false
}

// probeServicesThroughEngine probes the given services through the currently
// running engine (no restart) and returns the set that FAILED.
func (a *App) probeServicesThroughEngine(serviceTags []string) map[string]bool {
	failing := map[string]bool{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tag := range serviceTags {
		svc, ok := findFreeAccessService(tag)
		if !ok || len(svc.ProbeTargets()) == 0 {
			continue
		}
		wg.Add(1)
		go func(service FreeAccessService) {
			defer wg.Done()
			candidate := routeProbeCandidate{
				Tag:       composedStrategyTag,
				Label:     "per-service",
				Kind:      "transparent",
				Client:    newDirectHTTPClient(),
				Available: true,
			}
			item := a.probeSingleCandidate(service, candidate)
			if !item.Success {
				mu.Lock()
				failing[service.Tag] = true
				mu.Unlock()
			}
		}(svc)
	}
	wg.Wait()
	return failing
}

// applyServiceFreeFallback routes a service that no transparent method can fix to
// the VPN subscription when one exists, otherwise leaves it direct. In pure
// Deep Windows mode without a subscription this means the service stays blocked
// (the honest state), which is surfaced to the user rather than hidden behind
// endless strategy churn.
func (a *App) applyServiceFreeFallback(serviceTag string) {
	configPath := ""
	if a.storage != nil {
		configPath = a.storage.ActiveConfigFilePath()
	}
	if configPath == "" {
		return
	}
	blockType := serviceBlockType(serviceTag)
	if hasVPN, err := configHasVPNProbeCandidates(configPath); err == nil && hasVPN {
		changed := a.applyServiceFallbackSelectionToConfig(configPath, routeProbeServiceResult{
			Tag:         serviceTag,
			Name:        serviceTag,
			MethodTag:   FreeAccessMethodVPN,
			MethodKind:  "vpn",
			MethodLabel: FreeAccessOutboundLabel(FreeAccessMethodVPN),
			Success:     true,
		})
		switched := a.switchServiceToVPNFallback(serviceTag)
		a.cacheServiceMethod(serviceTag, FreeAccessMethodVPN, "fallback-vpn")
		switch {
		case switched:
			a.writeLog(fmt.Sprintf("[FreeAccess] %s (%s-blocked) routed to VPN subscription fallback", serviceTag, blockType))
		case changed:
			a.writeLog(fmt.Sprintf("[FreeAccess] %s (%s-blocked) queued for VPN subscription fallback before proxy endpoint is ready", serviceTag, blockType))
		default:
			a.writeLog(fmt.Sprintf("[FreeAccess] %s (%s-blocked) selected VPN subscription fallback; live switch is pending proxy endpoint readiness", serviceTag, blockType))
		}
		return
	}
	a.cacheServiceMethod(serviceTag, FreeAccessMethodDirect, "fallback-direct")
	a.writeLog(fmt.Sprintf("[FreeAccess] %s (%s-blocked) left direct: no working desync and no VPN subscription", serviceTag, blockType))
}

func (a *App) applyServiceFallbackSelectionToConfig(configPath string, result routeProbeServiceResult) bool {
	if configPath == "" || result.Tag == "" || result.MethodTag == "" {
		return false
	}
	config, err := readJSONConfig(configPath)
	if err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to read config for %s fallback: %v", result.Tag, err))
		return false
	}
	if !applyRouteProbeSelectionsToConfig(config, []routeProbeServiceResult{result}) {
		return false
	}
	if err := writeJSONConfig(configPath, config); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to persist %s fallback: %v", result.Tag, err))
		return false
	}
	return true
}

// retunePerServiceStrategy is the in-session, once-per-service reaction to a
// service that stopped working: search its ladder for a new first-working
// method, cache+prioritise it, and recompose. If nothing works, fall back to
// VPN/direct. Runs under the discovery lock so the quick-check feedback guard
// suppresses spurious re-triggers.
func (a *App) retunePerServiceStrategy(serviceTag, reason string) error {
	if a.zapret == nil || a.zapret.ActiveTag() != composedStrategyTag {
		return fmt.Errorf("per-service engine is not active")
	}
	if !a.tryBeginRouteProbeDiscovery() {
		return fmt.Errorf("route method discovery is already running")
	}
	defer a.finishRouteProbeDiscovery()

	a.writeLog(fmt.Sprintf("[FreeAccess] per-service retune started for %s: %s", serviceTag, reason))
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	selections, _ := a.resolveServiceSelections(dir, cache)
	if _, ok := selections[serviceTag]; !ok {
		return fmt.Errorf("service %q is not handled by the transparent engine", serviceTag)
	}

	// Confirm it actually still fails before disrupting the engine.
	if !a.probeServicesThroughEngine([]string{serviceTag})[serviceTag] {
		a.writeLog(fmt.Sprintf("[FreeAccess] %s already works; keeping current method", serviceTag))
		return nil
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}

	method, ok := a.searchServiceStrategy(serviceTag, selections)
	if ok {
		selections[serviceTag] = serviceWinwsSelection{ServiceTag: serviceTag, HostlistPath: selections[serviceTag].HostlistPath, Method: method}
		a.cacheServiceMethod(serviceTag, method.Tag, "retune")
		a.writeLog(fmt.Sprintf("[FreeAccess] %s retuned to %s", serviceTag, method.Label))
	} else {
		a.applyServiceFreeFallback(serviceTag)
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return fmt.Errorf("re-compose after retune: %w", err)
	}
	return nil
}
