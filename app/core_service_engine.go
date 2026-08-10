package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	traffic "dropo/trafficorchestrator"
)

// isFreeAccessFallbackTag reports whether a cached selection is a VPN/direct
// fallback decision rather than a transparent desync method.
func isFreeAccessFallbackTag(tag string) bool {
	return tag == FreeAccessMethodVPN || tag == FreeAccessMethodDirect
}

const (
	serviceStrategyCacheFileName   = "service_strategy_cache.json"
	serviceStrategyCacheVersion    = 2
	serviceHostlistDirName         = "service-hostlists"
	serviceStrategyFallbackTTL     = 30 * time.Minute
	serviceStrategyProbeRetryDelay = 300 * time.Millisecond
	maxAutomaticServiceStrategies  = 3
	// A subscription gives automatic services a fast, bounded local attempt
	// before the working VPN fallback. Without a subscription there is no such
	// fallback, so retry the complete native ladder over a bounded one-hour
	// campaign. Repeated cycles are useful because DPI behaviour can vary with
	// the current CDN address and network load; the session token still cancels
	// the campaign immediately when VPN stops.
	serviceStrategyExtendedCycles        = 12
	serviceStrategyExtendedRetryInterval = 4*time.Minute + 30*time.Second
	serviceStrategyExtendedMaxDuration   = time.Hour
)

const backgroundServiceStrategySource = "background-service-strategy"

type serviceStrategySearchCampaign struct {
	Cycle      int
	CycleTotal int
	HasVPN     bool
}

type serviceStrategyCacheFile struct {
	Version           int                                  `json:"version"`
	StrategiesVersion int                                  `json:"strategiesVersion"`
	UpdatedAt         time.Time                            `json:"updatedAt"`
	Services          map[string]serviceStrategyCacheEntry `json:"services"`
}

type serviceStrategyCacheEntry struct {
	MethodTag          string    `json:"methodTag"`
	State              string    `json:"state"`
	Source             string    `json:"source"`
	UpdatedAt          time.Time `json:"updatedAt"`
	RetryAfter         time.Time `json:"retryAfter,omitempty"`
	NetworkFingerprint string    `json:"networkFingerprint,omitempty"`
}

const (
	serviceStrategyStateWorking  = "working"
	serviceStrategyStateFallback = "fallback"
)

// currentNetworkFingerprint invalidates selections when the active network
// changes. A working DPI strategy is a property of the current network path.
func currentNetworkFingerprint() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	parts := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		name := strings.ToLower(iface.Name)
		if strings.Contains(name, "singbox") || strings.Contains(name, "wintun") || strings.Contains(name, "wireguard") || strings.Contains(name, "dropo") {
			continue
		}
		addrs, _ := iface.Addrs()
		addrParts := make([]string, 0, len(addrs))
		for _, addr := range addrs {
			if prefix := networkPrefixForFingerprint(addr); prefix != "" {
				addrParts = append(addrParts, prefix)
			}
		}
		if len(addrParts) == 0 {
			continue
		}
		sort.Strings(addrParts)
		parts = append(parts, name+"|"+strings.Join(addrParts, ","))
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:8])
}

func networkPrefixForFingerprint(addr net.Addr) string {
	var ip net.IP
	var mask net.IPMask
	switch value := addr.(type) {
	case *net.IPNet:
		ip, mask = value.IP, value.Mask
	case *net.IPAddr:
		ip = value.IP
		if ip.To4() != nil {
			mask = net.CIDRMask(32, 32)
		} else {
			mask = net.CIDRMask(64, 128)
		}
	default:
		parsedIP, parsedNet, err := net.ParseCIDR(addr.String())
		if err != nil {
			return ""
		}
		ip, mask = parsedIP, parsedNet.Mask
	}
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return ""
	}
	ones, bits := mask.Size()
	if ones < 0 || bits == 0 {
		return ""
	}
	if bits == 128 && ones > 64 {
		// RFC 4941 temporary IPv6 addresses rotate their interface identifier.
		// Fingerprint the stable network prefix, never the temporary host bits.
		ones = 64
		mask = net.CIDRMask(ones, bits)
	}
	networkIP := ip.Mask(mask)
	if networkIP == nil {
		return ""
	}
	return (&net.IPNet{IP: networkIP, Mask: mask}).String()
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
	fingerprint := currentNetworkFingerprint()
	now := time.Now()
	for tag, entry := range file.Services {
		if entry.MethodTag == "" {
			continue
		}
		if entry.NetworkFingerprint != "" && fingerprint != "" && entry.NetworkFingerprint != fingerprint {
			continue
		}
		// Negative results are short-lived. A temporary outage must not pin a
		// service to VPN/direct until the next strategy database release.
		if isFreeAccessFallbackTag(entry.MethodTag) {
			retryAfter := entry.RetryAfter
			if retryAfter.IsZero() {
				retryAfter = entry.UpdatedAt.Add(serviceStrategyFallbackTTL)
			}
			if !now.Before(retryAfter) {
				continue
			}
			out[tag] = entry
			continue
		}
		// Drop entries whose method no longer exists in the ranked registry.
		if tag == commonBlockedServiceTag {
			if _, ok := findCommonBlockedMethod(entry.MethodTag); !ok {
				continue
			}
		} else if _, ok := findServiceBypassMethod(tag, entry.MethodTag); !ok {
			continue
		}
		out[tag] = entry
	}
	return out
}

func (a *App) serviceStrategiesDueForRetry(now time.Time, fingerprint string) []string {
	path := a.serviceStrategyCachePath()
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var file serviceStrategyCacheFile
	if json.Unmarshal(data, &file) != nil || file.Version != serviceStrategyCacheVersion || file.StrategiesVersion != serviceStrategiesVersion() {
		return nil
	}
	due := make([]string, 0)
	for tag, entry := range file.Services {
		if entry.MethodTag == "" {
			continue
		}
		if entry.NetworkFingerprint != "" && fingerprint != "" && entry.NetworkFingerprint != fingerprint {
			due = append(due, tag)
			continue
		}
		if !isFreeAccessFallbackTag(entry.MethodTag) {
			continue
		}
		retryAfter := entry.RetryAfter
		if retryAfter.IsZero() {
			retryAfter = entry.UpdatedAt.Add(serviceStrategyFallbackTTL)
		}
		if !now.Before(retryAfter) {
			due = append(due, tag)
		}
	}
	sort.Strings(due)
	return due
}

func (a *App) retryDueServiceStrategies() {
	if a == nil || !a.routeStrategyWorkAllowed() {
		return
	}
	a.mu.Lock()
	running := a.isRunning && !a.stoppedManually
	a.mu.Unlock()
	if !running {
		return
	}
	due := a.serviceStrategiesDueForRetry(time.Now(), currentNetworkFingerprint())
	if len(due) == 0 {
		return
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] retrying service strategies after fallback TTL/network change: %s", strings.Join(due, ",")))
	hasVPN := false
	if a.storage != nil {
		hasVPN, _ = configHasVPNProbeCandidates(a.storage.ActiveConfigFilePath())
	}
	_, err := a.validateWindowsUnifiedServiceStrategies(
		"fallback TTL or network change", false, a.currentRouteStrategySession(), due,
		serviceStrategySearchCampaign{Cycle: 1, CycleTotal: 1, HasVPN: hasVPN},
	)
	if err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] scheduled service strategy retry failed: %v", err))
	}
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
	now := time.Now()
	entry := serviceStrategyCacheEntry{
		MethodTag:          methodTag,
		State:              serviceStrategyStateWorking,
		Source:             source,
		UpdatedAt:          now,
		NetworkFingerprint: currentNetworkFingerprint(),
	}
	if isFreeAccessFallbackTag(methodTag) {
		entry.State = serviceStrategyStateFallback
		entry.RetryAfter = now.Add(serviceStrategyFallbackTTL)
	}
	file.Services[serviceTag] = entry
	file.UpdatedAt = now
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	if data, err := json.MarshalIndent(file, "", "  "); err == nil {
		_ = atomicWriteFile(path, data, 0644)
	}
}

// cacheWebValidatedServiceMethod records ordinary HTTP-validated services.
// Discord is intentionally excluded: its strategy is only working after the
// realtime monitor proves sustained bidirectional media on the same policy.
func (a *App) cacheWebValidatedServiceMethod(serviceTag, methodTag, source string) {
	if strings.EqualFold(strings.TrimSpace(serviceTag), "discord") {
		a.writeLog("[FreeAccess] discord web/API precheck passed; waiting for live voice media before caching the strategy")
		return
	}
	a.cacheServiceMethod(serviceTag, methodTag, source)
}

func (a *App) removeServiceStrategyCacheEntry(serviceTag string) {
	path := a.serviceStrategyCachePath()
	serviceTag = strings.TrimSpace(serviceTag)
	if path == "" || serviceTag == "" {
		return
	}
	a.serviceStrategyCacheMu.Lock()
	defer a.serviceStrategyCacheMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var file serviceStrategyCacheFile
	if json.Unmarshal(data, &file) != nil || file.Services == nil {
		return
	}
	if _, exists := file.Services[serviceTag]; !exists {
		return
	}
	delete(file.Services, serviceTag)
	file.UpdatedAt = time.Now()
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err == nil {
		_ = atomicWriteFile(path, encoded, 0644)
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
// for diagnostics; startup validation itself deliberately checks every composed
// service so a stale cached method can never bypass the initial loading gate.
func (a *App) resolveServiceSelections(dir string, cache map[string]serviceStrategyCacheEntry) (map[string]serviceWinwsSelection, []string) {
	selections := map[string]serviceWinwsSelection{}
	needSearch := []string{}
	settings := GlobalAppSettings{}
	if a.storage != nil {
		settings = a.storage.GetAppSettings()
	}
	for _, svc := range a.enabledTransparentServices() {
		if !FreeAccessServiceEnabled(settings, svc.Tag) {
			continue
		}
		methodSetting := FreeAccessServiceMethod(settings, svc.Tag)
		if methodSetting == FreeAccessMethodVPN || methodSetting == FreeAccessMethodDirect {
			continue
		}
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

func (a *App) addCommonBlockedSelection(selections map[string]serviceWinwsSelection, cache map[string]serviceStrategyCacheEntry) (string, error) {
	if a == nil || a.storage == nil || !FreeMethodsAllowed(a.storage.GetAppSettings()) {
		return "", nil
	}
	if _, err := a.loadBlockedCatalogCached(); err != nil {
		return "", err
	}
	if entry, ok := cache[commonBlockedServiceTag]; ok && isFreeAccessFallbackTag(entry.MethodTag) {
		// Subscription availability may have changed since the fallback was
		// cached; preserve the required VPN -> direct order dynamically.
		return a.preferredCommonBlockedFallback(), nil
	}
	methods := commonBlockedMethods()
	if len(methods) == 0 {
		return "", fmt.Errorf("native common strategy catalog is empty")
	}
	method := methods[0]
	if entry, ok := cache[commonBlockedServiceTag]; ok {
		if cached, found := findCommonBlockedMethod(entry.MethodTag); found {
			method = cached
		}
	}
	selections[commonBlockedServiceTag] = serviceWinwsSelection{ServiceTag: commonBlockedServiceTag, Method: method}
	return "", nil
}

// orderedSelections returns selections in stable service order for deterministic
// winws2 composition.
func (a *App) orderedSelections(selections map[string]serviceWinwsSelection) []serviceWinwsSelection {
	ordered := make([]serviceWinwsSelection, 0, len(selections))
	for _, svc := range DefaultFreeAccessServices {
		if sel, ok := selections[svc.Tag]; ok {
			if strings.EqualFold(sel.ServiceTag, "discord") {
				sel = a.decorateDiscordRealtimeSelection(sel)
			}
			ordered = append(ordered, sel)
		}
	}
	return ordered
}

func (a *App) composeAndStartServiceEngine(selections map[string]serviceWinwsSelection) error {
	if a.trafficEngine == nil {
		return fmt.Errorf("native traffic engine is not initialized")
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	a.serviceEngineComposeMu.Lock()
	defer a.serviceEngineComposeMu.Unlock()
	wireGuardTargets := a.wireGuardCamouflageTargetsForSession()
	if len(selections) == 0 && len(wireGuardTargets) == 0 {
		a.trafficEngine.Stop()
		a.writeLog("[FreeAccess] native traffic engine stopped: no service currently uses a local strategy")
		return nil
	}
	if len(wireGuardTargets) > 0 {
		a.writeLog(fmt.Sprintf("[WireGuard] native handshake camouflage active for %d endpoint(s), scoped by resolved IP and UDP port", len(wireGuardTargets)))
	}
	plan, err := a.buildNativeTrafficPlan(selections)
	if err != nil {
		return fmt.Errorf("build native traffic plan: %w", err)
	}
	return a.trafficEngine.StartPlan(plan)
}

// winwsDebugEnabled retains the old Go method name for settings migration. It
// enables native packet diagnostics without launching an external process.
func (a *App) winwsDebugEnabled() bool {
	if trafficPacketDebugEnabled() {
		return true
	}
	marker := a.winwsDebugMarkerPath()
	return marker != "" && fileExists(marker)
}

func trafficPacketDebugEnabled() bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv("DROPO_TRAFFIC_PACKET_DEBUG")))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func (a *App) winwsDebugMarkerPath() string {
	if a.basePath == "" {
		return ""
	}
	return filepath.Join(a.basePath, "traffic-debug.txt")
}

func (a *App) prepareServiceWinwsDebugLog() (string, error) {
	if a == nil {
		return "", fmt.Errorf("app is not initialized")
	}
	if marker := a.winwsDebugMarkerPath(); marker != "" && fileExists(marker) {
		path := filepath.Join(a.basePath, "traffic-debug.log")
		_ = os.Remove(path)
		return path, nil
	}
	if a.trafficEngine == nil {
		return "", fmt.Errorf("native traffic engine is not initialized")
	}
	return a.trafficEngine.prepareDebugLog("per-service")
}

// startWindowsUnifiedServiceEngine performs only the bounded startup step: it
// composes and starts the native traffic engine from a valid cache entry or the
// top-ranked candidate. Network validation is deliberately deferred until the
// VPN is already connected so a slow or filtered service cannot hold the power
// button in the connecting state.
func (a *App) startWindowsUnifiedServiceEngine(_ string) error {
	if a == nil || a.trafficEngine == nil || a.storage == nil {
		return nil
	}
	settings := a.storage.GetAppSettings()
	freeMethodsAllowed := FreeMethodsAllowed(settings)
	if !freeMethodsAllowed {
		a.writeLog("[FreeAccess] per-service methods disabled; evaluating WireGuard camouflage only")
		return a.composeAndStartServiceEngine(map[string]serviceWinwsSelection{})
	}
	if !a.tryBeginRouteProbeDiscovery() {
		a.writeLog("[FreeAccess] per-service engine start deferred: strategy discovery is already running")
		return nil
	}
	defer a.finishRouteProbeDiscovery()

	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	selections, needSearch := a.resolveServiceSelections(dir, cache)
	commonFallback, commonErr := a.addCommonBlockedSelection(selections, cache)
	if commonErr != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] bundled blocked catalog unavailable; using VPN/direct fallback: %v", commonErr))
		commonFallback = a.preferredCommonBlockedFallback()
	}
	if len(selections) == 0 {
		if len(a.wireGuardCamouflageTargetsForSession()) > 0 {
			if err := a.composeAndStartServiceEngine(selections); err != nil {
				return fmt.Errorf("start WireGuard camouflage-only traffic plan: %w", err)
			}
		} else {
			a.trafficEngine.Stop()
		}
		a.logServiceStrategySummary("all services use a temporary fallback")
		if commonFallback != "" {
			a.applyCommonBlockedFallback(commonFallback)
		}
		return nil
	}

	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return fmt.Errorf("start per-service engine: %w", err)
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] per-service engine started for %d service(s); background validation queued, %d have no cached strategy",
		len(selections), len(needSearch)))
	if !a.winwsDebugEnabled() {
		if marker := a.winwsDebugMarkerPath(); marker != "" {
			a.writeLog(fmt.Sprintf("[FreeAccess] for detailed packet diagnostics: create empty file %q next to dropo.exe, reconnect, then send %q", marker, filepath.Join(a.basePath, "traffic-debug.log")))
		} else {
			a.writeLog("[FreeAccess] for detailed packet diagnostics: set DROPO_TRAFFIC_PACKET_DEBUG=1 and reconnect")
		}
	}

	if commonFallback != "" {
		a.applyCommonBlockedFallback(commonFallback)
	}
	return nil
}

// validateWindowsUnifiedServiceStrategies runs the complete service gates after
// the base VPN session has already been published as connected. It owns the
// discovery lock for the whole operation, so maintenance cannot recompose the
// immutable traffic plan at the same time. Cached candidates are rechecked too;
// failures advance through the bounded ladder and end at VPN/direct fallback.
func (a *App) validateWindowsUnifiedServiceStrategies(
	reason string,
	retryFallbacks bool,
	session uint64,
	onlyTags []string,
	campaign serviceStrategySearchCampaign,
) ([]string, error) {
	if a == nil || a.trafficEngine == nil || a.storage == nil {
		return nil, nil
	}
	if !FreeMethodsAllowed(a.storage.GetAppSettings()) {
		return nil, nil
	}
	if !a.routeStrategySessionActive(session) {
		return nil, fmt.Errorf("VPN session ended before strategy validation")
	}
	if !a.tryBeginRouteProbeDiscovery() {
		return nil, fmt.Errorf("strategy discovery is already running")
	}
	defer a.finishRouteProbeDiscovery()

	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	if retryFallbacks {
		retryDiscord := onlyTags == nil || containsStringValue(onlyTags, "discord")
		if entry, ok := cache["discord"]; retryDiscord && ok && isFreeAccessFallbackTag(entry.MethodTag) {
			// Discord web/API success is intentionally not cached as proof of
			// voice. Remove the old fallback before retrying so the realtime
			// monitor observes the newly selected local policy instead of being
			// pulled back to a stale VPN decision.
			a.removeServiceStrategyCacheEntry("discord")
		}
		cache = serviceStrategyCacheForConnectedValidationTags(cache, onlyTags)
	}
	selections, needSearch := a.resolveServiceSelections(dir, cache)
	commonFallback, commonErr := a.addCommonBlockedSelection(selections, cache)
	if commonErr != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] bundled blocked catalog unavailable during background validation; using VPN/direct fallback: %v", commonErr))
		commonFallback = a.preferredCommonBlockedFallback()
	}
	if len(selections) == 0 {
		if commonFallback != "" {
			a.applyCommonBlockedFallback(commonFallback)
		}
		a.logServiceStrategySummary("background validation has no transparent services")
		return nil, nil
	}

	allowed := make(map[string]bool, len(onlyTags))
	for _, tag := range onlyTags {
		allowed[tag] = true
	}
	if onlyTags != nil && !allowed["discord"] {
		a.preserveCurrentDiscordSelection(selections)
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return nil, fmt.Errorf("prepare background service validation: %w", err)
	}
	validationTags := make([]string, 0, len(selections))
	for _, service := range DefaultFreeAccessServices {
		if _, ok := selections[service.Tag]; ok && (onlyTags == nil || allowed[service.Tag]) {
			validationTags = append(validationTags, service.Tag)
		}
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] background service validation started (%s): %d service(s), %d uncached",
		firstNonEmpty(reason, "connected session"), len(validationTags), len(needSearch)))
	failed, err := a.firstRunServiceSearch("", selections, validationTags, session, campaign)
	if err != nil {
		return failed, fmt.Errorf("background service strategy validation: %w", err)
	}
	if onlyTags == nil {
		if _, selected := selections[commonBlockedServiceTag]; selected {
			if err := a.selectCommonBlockedStrategy("", selections, session); err != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] common blocked strategy selection failed: %v", err))
				delete(selections, commonBlockedServiceTag)
				if composeErr := a.composeAndStartServiceEngine(selections); composeErr != nil {
					return failed, fmt.Errorf("remove failed common blocked strategy: %w", composeErr)
				}
				a.applyCommonBlockedFallback(a.preferredCommonBlockedFallback())
			}
		} else if commonFallback != "" {
			a.applyCommonBlockedFallback(commonFallback)
		}
	}
	a.writeLog("[FreeAccess] background service validation completed")
	return failed, nil
}

// startWindowsUnifiedServiceValidationAsync keeps first connect responsive.
// Discord automatic realtime monitoring starts after the first fast cycle.
// Later no-subscription cycles use the discovery lock and preserve the current
// Discord selection, so realtime media recovery and generic probes cannot race.
func (a *App) startWindowsUnifiedServiceValidationAsync(startDiscordMonitorAfter bool) {
	session := a.currentRouteStrategySession()
	go func() {
		startedAt := time.Now()
		hasVPN := false
		if a.storage != nil {
			hasVPN, _ = configHasVPNProbeCandidates(a.storage.ActiveConfigFilePath())
		}
		cycleTotal := 1
		if !hasVPN {
			cycleTotal = serviceStrategyExtendedCycles
		}
		serviceTags := a.backgroundServiceStrategyTags()
		a.emitRouteProbe("route-probe-start", map[string]interface{}{
			"source":             backgroundServiceStrategySource,
			"reason":             "post-connect",
			"serviceCount":       len(serviceTags),
			"services":           backgroundServiceStrategySummaries(serviceTags),
			"hasSubscription":    hasVPN,
			"extended":           !hasVPN,
			"maxDurationMinutes": int(serviceStrategyExtendedMaxDuration / time.Minute),
			"cycleTotal":         cycleTotal,
		})

		var validationErr error
		var failed []string
		if !a.waitForRouteProbeDiscoverySession(session, 30*time.Second) {
			validationErr = fmt.Errorf("another strategy discovery did not finish before the background validation deadline")
		} else {
			failed, validationErr = a.validateWindowsUnifiedServiceStrategies(
				"post-connect", true, session, nil,
				serviceStrategySearchCampaign{Cycle: 1, CycleTotal: cycleTotal, HasVPN: hasVPN},
			)
		}
		if startDiscordMonitorAfter && a.routeStrategySessionActive(session) {
			a.startDiscordRealtimeMonitor()
		}

		// Discord uses real bidirectional media evidence after the web/API
		// precheck. Its monitor owns further local attempts, so the generic
		// campaign retries only the remaining failed services.
		failed = removeServiceTag(failed, "discord")
		deadline := startedAt.Add(serviceStrategyExtendedMaxDuration)
		lastCycle := 1
		for cycle := 2; validationErr == nil && !hasVPN && len(failed) > 0 && cycle <= cycleTotal; cycle++ {
			if !a.waitForServiceStrategyRetry(session, serviceStrategyExtendedRetryInterval, deadline) {
				break
			}
			if !a.waitForRouteProbeDiscoverySession(session, 30*time.Second) {
				validationErr = fmt.Errorf("another strategy discovery did not finish before retry %d/%d", cycle, cycleTotal)
			} else {
				failed, validationErr = a.validateWindowsUnifiedServiceStrategies(
					fmt.Sprintf("post-connect retry %d/%d", cycle, cycleTotal), true, session, failed,
					serviceStrategySearchCampaign{Cycle: cycle, CycleTotal: cycleTotal, HasVPN: false},
				)
			}
			lastCycle = cycle
			failed = removeServiceTag(failed, "discord")
		}
		if validationErr == nil && !hasVPN && len(failed) > 0 && lastCycle < cycleTotal && a.routeStrategySessionActive(session) {
			for _, tag := range failed {
				a.emitRouteProbe("route-probe-service", map[string]interface{}{
					"source":     backgroundServiceStrategySource,
					"tag":        tag,
					"name":       serviceDisplayNameForTag(tag),
					"success":    false,
					"final":      true,
					"status":     "failed",
					"error":      "Часовой фоновый подбор завершён без рабочей стратегии",
					"cycle":      lastCycle,
					"cycleTotal": cycleTotal,
				})
			}
		}
		if validationErr != nil {
			a.mu.Lock()
			running := a.isRunning && !a.stoppedManually
			a.mu.Unlock()
			if running && a.routeStrategySessionActive(session) {
				a.writeLog(fmt.Sprintf("[FreeAccess] background service validation failed: %v", validationErr))
				switched := a.activateSubscriptionFallbackForTransparentRuntime()
				if switched > 0 {
					a.writeLog(fmt.Sprintf("[FreeAccess] background validation failure switched %d blocked-service group(s) to VPN fallback", switched))
				}
			}
		}
		a.emitRouteProbe("route-probe-complete", map[string]interface{}{
			"source":     backgroundServiceStrategySource,
			"reason":     "post-connect",
			"durationMs": time.Since(startedAt).Milliseconds(),
			"error":      compactProbeError(validationErr),
		})
	}()
}

func (a *App) backgroundServiceStrategyTags() []string {
	if a == nil || a.storage == nil {
		return nil
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		return nil
	}
	tags := make([]string, 0, len(DefaultFreeAccessServices))
	for _, service := range DefaultFreeAccessServices {
		if service.RequiresVPN || !serviceHasFreeBypass(service.Tag) ||
			!FreeAccessServiceEnabled(settings, service.Tag) ||
			FreeAccessServiceMethod(settings, service.Tag) != FreeAccessMethodAuto {
			continue
		}
		tags = append(tags, service.Tag)
	}
	return tags
}

func backgroundServiceStrategySummaries(tags []string) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, len(tags))
	for _, tag := range tags {
		result = append(result, map[string]interface{}{
			"tag":  tag,
			"name": serviceDisplayNameForTag(tag),
		})
	}
	return result
}

func removeServiceTag(tags []string, excluded string) []string {
	result := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag != excluded {
			result = append(result, tag)
		}
	}
	return result
}

func (a *App) preserveCurrentDiscordSelection(selections map[string]serviceWinwsSelection) {
	if a == nil || a.trafficEngine == nil {
		return
	}
	current, ok := selections["discord"]
	if !ok {
		return
	}
	selectedID := ""
	for _, selection := range a.trafficEngine.CurrentPlan().Selections {
		if selection.ServiceID == "discord" {
			selectedID = selection.StrategyID
			break
		}
	}
	for _, method := range rankedMethodsForService("discord") {
		if method.NativeStrategyID == selectedID {
			current.Method = method
			selections["discord"] = current
			return
		}
	}
}

func (a *App) waitForServiceStrategyRetry(session uint64, delay time.Duration, deadline time.Time) bool {
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return false
	}
	if delay > remaining {
		delay = remaining
	}
	timer := time.NewTimer(delay)
	ticker := time.NewTicker(time.Second)
	defer timer.Stop()
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			return a.routeStrategySessionActive(session) && time.Now().Before(deadline)
		case <-ticker.C:
			if !a.routeStrategySessionActive(session) {
				return false
			}
		}
	}
}

func (a *App) waitForRouteProbeDiscoverySession(session uint64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for a.isRouteProbeDiscoveryRunning() {
		if !a.routeStrategySessionActive(session) || time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
	return a.routeStrategySessionActive(session)
}

// serviceStrategyCacheForConnectedValidation retains proven transparent
// methods but removes temporary VPN/direct fallback decisions. The fallback is
// still used for the safe bootstrap; once connected, each new session gets a
// bounded background chance to recover a free route immediately instead of
// waiting up to the fallback TTL.
func serviceStrategyCacheForConnectedValidation(cache map[string]serviceStrategyCacheEntry) map[string]serviceStrategyCacheEntry {
	return serviceStrategyCacheForConnectedValidationTags(cache, nil)
}

func serviceStrategyCacheForConnectedValidationTags(cache map[string]serviceStrategyCacheEntry, retryTags []string) map[string]serviceStrategyCacheEntry {
	retryAll := retryTags == nil
	retry := make(map[string]bool, len(retryTags))
	for _, tag := range retryTags {
		retry[tag] = true
	}
	validation := make(map[string]serviceStrategyCacheEntry, len(cache))
	for tag, entry := range cache {
		if isFreeAccessFallbackTag(entry.MethodTag) && (retryAll || retry[tag]) {
			continue
		}
		validation[tag] = entry
	}
	return validation
}

// applyWindowsUnifiedBootstrapRoutesToConfig chooses a safe, non-probing route
// before sing-box starts. A proven transparent cache goes direct; an unproven
// automatic service starts on the subscription when available and is promoted
// to direct only by the post-connect validator. Manual direct/VPN policies are
// authoritative and are never replaced by automatic discovery.
func (a *App) applyWindowsUnifiedBootstrapRoutesToConfig(configPath string) (bool, error) {
	if a == nil || a.storage == nil || strings.TrimSpace(configPath) == "" {
		return false, nil
	}
	config, err := readJSONConfig(configPath)
	if err != nil {
		return false, err
	}
	outbounds, _ := config["outbounds"].([]interface{})
	hasVPN, err := configHasVPNProbeCandidates(configPath)
	if err != nil {
		return false, err
	}
	settings := a.storage.GetAppSettings()
	cache := a.loadServiceStrategyCache()
	changed := false

	for _, service := range DefaultFreeAccessServices {
		target := windowsUnifiedBootstrapServiceRoute(settings, service, cache[service.Tag], hasVPN)
		if target == "" {
			continue
		}
		if selectBootstrapOutbound(configOutboundByTag(outbounds, ServiceBypassGroupTag(service.Tag)), target) {
			changed = true
		}
	}

	commonTarget := "direct"
	if entry, ok := cache[commonBlockedServiceTag]; ok {
		if entry.MethodTag == FreeAccessMethodVPN && hasVPN {
			commonTarget = "auto-select"
		} else if entry.MethodTag == FreeAccessMethodDirect || !isFreeAccessFallbackTag(entry.MethodTag) {
			commonTarget = "direct"
		}
	} else if hasVPN {
		commonTarget = "auto-select"
	}
	if selectBootstrapOutbound(configOutboundByTag(outbounds, SmartBypassGroupTag), commonTarget) {
		changed = true
	}

	discordRealtimeTarget := windowsUnifiedBootstrapDiscordRealtimeRoute(settings, cache["discord"], hasVPN)
	if selectBootstrapOutbound(configOutboundByTag(outbounds, discordRealtimeGroupTag), discordRealtimeTarget) {
		changed = true
	}

	if !changed {
		return false, nil
	}
	config["outbounds"] = outbounds
	if err := writeJSONConfig(configPath, config); err != nil {
		return false, err
	}
	return true, nil
}

func windowsUnifiedBootstrapServiceRoute(settings GlobalAppSettings, service FreeAccessService, cached serviceStrategyCacheEntry, hasVPN bool) string {
	method := FreeAccessServiceMethod(settings, service.Tag)
	switch method {
	case FreeAccessMethodDirect:
		return "direct"
	case FreeAccessMethodVPN:
		if hasVPN {
			return "auto-select"
		}
		return ""
	}
	if !FreeAccessServiceEnabled(settings, service.Tag) || service.RequiresVPN || !serviceHasFreeBypass(service.Tag) || !FreeMethodsAllowed(settings) {
		if hasVPN {
			return "auto-select"
		}
		return ""
	}
	switch cached.MethodTag {
	case FreeAccessMethodVPN:
		if hasVPN {
			return "auto-select"
		}
		return "direct"
	case FreeAccessMethodDirect:
		return "direct"
	case "":
		if hasVPN {
			return "auto-select"
		}
		return "direct"
	default:
		// loadServiceStrategyCache already discarded stale or unknown methods.
		return "direct"
	}
}

func windowsUnifiedBootstrapDiscordRealtimeRoute(settings GlobalAppSettings, cached serviceStrategyCacheEntry, hasVPN bool) string {
	method := FreeAccessServiceMethod(settings, "discord")
	if method == FreeAccessMethodDirect || !hasVPN {
		return "direct"
	}
	if method == FreeAccessMethodVPN || !FreeMethodsAllowed(settings) || !FreeAccessServiceEnabled(settings, "discord") {
		return discordVPNGroupTag
	}
	if cached.MethodTag == FreeAccessMethodVPN {
		return discordVPNGroupTag
	}
	if cached.MethodTag == "" {
		return discordVPNGroupTag
	}
	return "direct"
}

func selectBootstrapOutbound(outbound map[string]interface{}, target string) bool {
	if outbound == nil || strings.TrimSpace(target) == "" {
		return false
	}
	candidates := interfaceStringSlice(outbound["outbounds"])
	if !containsStringValue(candidates, target) {
		return false
	}
	current, _ := outbound["default"].(string)
	_, hasURL := outbound["url"]
	_, hasInterval := outbound["interval"]
	_, hasTolerance := outbound["tolerance"]
	_, hasInterrupt := outbound["interrupt_exist_connections"]
	alreadySelected := outbound["type"] == "selector" && current == target && len(candidates) > 0 && candidates[0] == target &&
		!hasURL && !hasInterval && !hasTolerance && !hasInterrupt
	preferOutboundGroupCandidate(outbound, target)
	outbound["type"] = "selector"
	outbound["default"] = target
	deleteOutboundGroupHealthCheckFields(outbound)
	return !alreadySelected
}

func (a *App) preferredCommonBlockedFallback() string {
	if a != nil && a.storage != nil {
		if hasVPN, err := configHasVPNProbeCandidates(a.storage.ActiveConfigFilePath()); err == nil && hasVPN {
			return FreeAccessMethodVPN
		}
	}
	return FreeAccessMethodDirect
}

func (a *App) applyCommonBlockedFallback(method string) {
	if method == "" {
		method = a.preferredCommonBlockedFallback()
	}
	outbound := "direct"
	if method == FreeAccessMethodVPN {
		outbound = "auto-select"
	}
	persisted := a.persistCommonBlockedFallback(outbound)
	switched := a.switchOutboundSelector(SmartBypassGroupTag, outbound)
	if persisted || switched {
		a.cacheServiceMethod(commonBlockedServiceTag, method, "common-fallback")
		a.writeLog(fmt.Sprintf("[FreeAccess] bundled blocked catalog fallback -> %s (live=%t persisted=%t)", outbound, switched, persisted))
	}
}

func (a *App) persistCommonBlockedFallback(outboundTag string) bool {
	if a == nil || a.storage == nil || outboundTag == "" {
		return false
	}
	path := a.storage.ActiveConfigFilePath()
	config, err := readJSONConfig(path)
	if err != nil {
		return false
	}
	outbounds, _ := config["outbounds"].([]interface{})
	for _, raw := range outbounds {
		outbound, ok := raw.(map[string]interface{})
		if !ok || outbound["tag"] != SmartBypassGroupTag {
			continue
		}
		if !containsStringValue(interfaceStringSlice(outbound["outbounds"]), outboundTag) {
			return false
		}
		preferOutboundGroupCandidate(outbound, outboundTag)
		outbound["type"] = "selector"
		outbound["default"] = outboundTag
		deleteOutboundGroupHealthCheckFields(outbound)
		return writeJSONConfig(path, config) == nil
	}
	return false
}

func (a *App) selectCommonBlockedStrategy(busyID string, selections map[string]serviceWinwsSelection, session uint64) error {
	catalog, err := a.loadBlockedCatalogCached()
	if err != nil {
		return err
	}
	targets, err := randomBlockedProbeTargets(catalog.Domains, commonBlockedProbeCount)
	if err != nil {
		return err
	}
	if busyID != "" {
		a.updateBusy(busyID, "Проверяем общую DPI-стратегию на 4 случайных доменах...")
	}
	if !a.switchOutboundSelector(SmartBypassGroupTag, "direct") {
		return fmt.Errorf("cannot select direct route for common strategy validation")
	}

	current := selections[commonBlockedServiceTag].Method
	methods := commonBlockedSearchLadder(current)
	runner := nativeProbeRunner{}
	controller := nativeTrialController{manager: a.trafficEngine}
	probeNames := make([]string, 0, len(targets))
	for _, target := range targets {
		probeNames = append(probeNames, strings.TrimPrefix(strings.TrimSuffix(target.URL, "/"), "https://"))
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] common strategy random sample: %s", strings.Join(probeNames, ", ")))

	for _, method := range methods {
		if !a.routeStrategySessionActive(session) {
			return fmt.Errorf("common strategy selection interrupted because VPN is stopping")
		}
		strategy, found := nativeStrategyByID(method.NativeStrategyID)
		if !found {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		trial, beginErr := controller.BeginTrial(ctx, commonBlockedServiceTag, strategy)
		if beginErr != nil {
			cancel()
			continue
		}
		observations := make([]traffic.ProbeObservation, len(targets))
		var probes sync.WaitGroup
		probes.Add(len(targets))
		for index, target := range targets {
			go func() {
				defer probes.Done()
				observations[index] = runner.Probe(ctx, target)
			}()
		}
		probes.Wait()
		cancel()
		if !a.routeStrategySessionActive(session) {
			return fmt.Errorf("common strategy selection interrupted after probes")
		}
		passed := true
		for index, target := range targets {
			if observation := observations[index]; !observation.Success {
				passed = false
				a.writeLog(fmt.Sprintf("[FreeAccess] common %s failed %s (%s: %s)", method.Tag, target.URL, observation.Failure, observation.Detail))
			}
		}
		if !passed {
			if rollbackErr := trial.Rollback(); rollbackErr != nil {
				return fmt.Errorf("rollback common strategy %s: %w", method.Tag, rollbackErr)
			}
			continue
		}
		if err := trial.Commit(); err != nil {
			_ = trial.Rollback()
			return fmt.Errorf("commit common strategy %s: %w", method.Tag, err)
		}
		selections[commonBlockedServiceTag] = serviceWinwsSelection{ServiceTag: commonBlockedServiceTag, Method: method}
		_ = a.persistCommonBlockedFallback("direct")
		a.cacheServiceMethod(commonBlockedServiceTag, method.Tag, "common-random-four")
		a.writeLog(fmt.Sprintf("[FreeAccess] common blocked strategy = %s; all 4 random domains passed", method.Label))
		return nil
	}
	return fmt.Errorf("no native strategy passed all 4 random blocked domains")
}

func commonBlockedSearchLadder(current ServiceBypassMethod) []ServiceBypassMethod {
	methods := make([]ServiceBypassMethod, 0, maxAutomaticServiceStrategies)
	if current.Tag != "" {
		methods = append(methods, current)
	}
	for _, method := range commonBlockedMethods() {
		if method.Tag == current.Tag {
			continue
		}
		methods = append(methods, method)
		if len(methods) == maxAutomaticServiceStrategies {
			break
		}
	}
	return methods
}

func nativeStrategyByID(id string) (traffic.TrafficStrategy, bool) {
	for _, strategy := range traffic.BuiltinStrategies() {
		if strategy.ID == id {
			return strategy, true
		}
	}
	return traffic.TrafficStrategy{}, false
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

// startComposedTransparentEngine starts the single in-process Windows traffic
// engine. The legacy method name is retained only to keep API migration small.
func (a *App) startComposedTransparentEngine(busyID string) error {
	if a == nil || a.storage == nil {
		return fmt.Errorf("Windows Unified storage is not initialized")
	}
	freeMethodsAllowed := FreeMethodsAllowed(a.storage.GetAppSettings())
	wireGuardRequested := a.wireGuardCamouflageRequested()
	if !freeMethodsAllowed && !wireGuardRequested {
		return nil
	}
	if a.trafficEngine == nil || !a.trafficEngine.IsInstalled() {
		if wireGuardRequested && !freeMethodsAllowed {
			a.writeLog("[WireGuard] handshake transformation unavailable because the WinDivert runtime is not installed; continuing with native WireGuard")
			return nil
		}
		return fmt.Errorf("Windows Unified WinDivert runtime is not installed")
	}
	return a.startWindowsUnifiedServiceEngine(busyID)
}

// forceSubscriptionFallbackForTransparentRuntime rewrites only resilient
// routing groups that already contain the trusted subscription selector. It is
// used when endpoint protection blocks optional winws2: direct remains present
// for later recovery, but no blocked service is accidentally pinned to a path
// that requires the unavailable packet engine.
func (a *App) forceSubscriptionFallbackForTransparentRuntime(configPath string) (bool, error) {
	config, err := readJSONConfig(configPath)
	if err != nil {
		return false, err
	}
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok || !outboundTagExists(outbounds, "auto-select") {
		return false, nil
	}
	changed := false
	for _, raw := range outbounds {
		outbound, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := outbound["tag"].(string)
		if !(strings.HasPrefix(tag, "bypass-") || tag == SmartBypassGroupTag || tag == VpnOrDirectGroupTag) {
			continue
		}
		candidates := interfaceStringSlice(outbound["outbounds"])
		if !containsStringValue(candidates, "auto-select") {
			continue
		}
		preferOutboundGroupCandidate(outbound, "auto-select")
		outbound["type"] = "selector"
		outbound["default"] = "auto-select"
		deleteOutboundGroupHealthCheckFields(outbound)
		changed = true
	}
	if changed {
		if err := writeJSONConfig(configPath, config); err != nil {
			return false, err
		}
	}
	return changed, nil
}

func containsStringValue(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (a *App) activateSubscriptionFallbackForTransparentRuntime() int {
	groups := make([]string, 0, len(DefaultFreeAccessServices)+2)
	for _, service := range DefaultFreeAccessServices {
		groups = append(groups, ServiceBypassGroupTag(service.Tag))
	}
	groups = append(groups, SmartBypassGroupTag, VpnOrDirectGroupTag)
	switched := 0
	for _, group := range groups {
		if a.switchOutboundSelector(group, "auto-select") {
			switched++
		}
	}
	return switched
}

// firstRunServiceSearch is the connected-session validator retained under its
// historical name. It finds the first ranked method that works and is round-
// based: round R sets every still-failing
// service to its method[R] and recomposes the engine ONCE, then probes all of
// them in parallel. So the whole search costs at most (ladder length) restarts
// — not (services × methods) — keeping background work bounded with many services.
// Round 0 reuses the already-running top-method engine without a restart.
func (a *App) firstRunServiceSearch(
	busyID string,
	selections map[string]serviceWinwsSelection,
	needSearch []string,
	session uint64,
	campaign serviceStrategySearchCampaign,
) ([]string, error) {
	if campaign.Cycle < 1 {
		campaign.Cycle = 1
	}
	if campaign.CycleTotal < campaign.Cycle {
		campaign.CycleTotal = campaign.Cycle
	}
	ladders := make(map[string][]ServiceBypassMethod, len(needSearch))
	maxRounds := 0
	for _, tag := range needSearch {
		ladder := startupServiceSearchLadder(tag, selections[tag].Method)
		ladders[tag] = ladder
		if n := len(ladder); n > maxRounds {
			maxRounds = n
		}
	}

	pending := append([]string{}, needSearch...)
	for round := 0; round < maxRounds && len(pending) > 0; round++ {
		if !a.routeStrategySessionActive(session) {
			return pending, fmt.Errorf("service strategy search interrupted because VPN is stopping")
		}
		if busyID != "" {
			a.updateBusy(busyID, fmt.Sprintf("Проверяем стратегии, попытка %d/%d: %s", round+1, maxRounds, serviceSearchStatusList(pending)))
		}
		if round > 0 {
			for _, tag := range pending {
				ladder := ladders[tag]
				if round < len(ladder) {
					selections[tag] = serviceWinwsSelection{ServiceTag: tag, HostlistPath: selections[tag].HostlistPath, Method: ladder[round]}
				}
			}
			if err := a.composeAndStartServiceEngine(selections); err != nil {
				a.writeLog(fmt.Sprintf("[FreeAccess] background validation round %d recompose failed: %v", round, err))
				return pending, fmt.Errorf("background validation round %d recompose: %w", round, err)
			}
		}
		for _, tag := range pending {
			ladder := ladders[tag]
			if round >= len(ladder) {
				continue
			}
			method := ladder[round]
			a.emitBackgroundStrategyCandidate(tag, method, round, len(ladder), campaign)
		}

		failing := a.probeServicesThroughEngine(pending)
		if !a.routeStrategySessionActive(session) {
			return pending, fmt.Errorf("service strategy search interrupted after probes")
		}
		next := make([]string, 0, len(pending))
		for _, tag := range pending {
			if !failing[tag] {
				if tag == "discord" {
					a.seedDiscordRealtimeStrategyAttempts(ladders[tag], round)
				}
				a.cacheWebValidatedServiceMethod(tag, selections[tag].Method.Tag, "startup-validation")
				if !a.switchServiceRoute(tag, "direct") {
					return pending, fmt.Errorf("activate confirmed startup strategy for %s", tag)
				}
				if tag == "discord" {
					a.writeLog(fmt.Sprintf("[FreeAccess] discord: provisional method = %s; live voice proof is still required", selections[tag].Method.Label))
					a.emitBackgroundStrategyService(tag, selections[tag].Method, false, false, false, "voice-check", "Ожидаем подтверждение Discord voice по реальному медиапотоку", round, len(ladders[tag]), campaign)
				} else {
					a.writeLog(fmt.Sprintf("[FreeAccess] %s: working method = %s", tag, selections[tag].Method.Label))
					a.emitBackgroundStrategyService(tag, selections[tag].Method, true, true, false, "done", "", round, len(ladders[tag]), campaign)
				}
				continue
			}
			if tag == "discord" {
				a.removeServiceStrategyCacheEntry(tag)
			}
			if round+1 < len(ladders[tag]) {
				a.emitBackgroundStrategyService(tag, selections[tag].Method, false, false, true, "retrying", "Стратегия не прошла полную проверку; пробуем следующую", round, len(ladders[tag]), campaign)
				next = append(next, tag)
			} else {
				if tag == "discord" {
					a.seedDiscordRealtimeStrategyAttempts(ladders[tag], round)
				}
				if tag == "discord" && !campaign.HasVPN {
					// HTTP/API failure is not authoritative for Discord voice.
					// Keep the last complete Discord policy active so the realtime
					// monitor can test actual UDP media and own the extended retries.
					a.emitBackgroundStrategyService(tag, selections[tag].Method, false, false, true, "voice-check", "Web/API-проверка не прошла; продолжаем по реальному Discord voice", round, len(ladders[tag]), campaign)
					next = append(next, tag)
					continue
				}
				a.writeLog(fmt.Sprintf("[FreeAccess] %s: no transparent method worked; using VPN/direct fallback", tag))
				a.applyServiceFreeFallback(tag)
				delete(selections, tag)
				final := campaign.HasVPN || campaign.Cycle >= campaign.CycleTotal
				if campaign.HasVPN {
					a.emitBackgroundStrategyFallback(tag, FreeAccessMethodVPN, true, campaign)
				} else {
					a.emitBackgroundStrategyService(tag, ServiceBypassMethod{}, false, final, !final, map[bool]string{true: "failed", false: "retrying"}[final], "Рабочая стратегия пока не найдена", round, len(ladders[tag]), campaign)
				}
				next = append(next, tag)
			}
		}
		pending = next
	}

	// Lock in whatever each service ended on.
	if !a.routeStrategySessionActive(session) {
		return pending, fmt.Errorf("service strategy search interrupted before commit")
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to re-compose engine after background validation: %v", err))
		return pending, fmt.Errorf("commit background service selections: %w", err)
	}
	a.logServiceStrategySummary("background validation complete")
	return pending, nil
}

func backgroundStrategyAttempt(round, ladderSize int, campaign serviceStrategySearchCampaign) (int, int) {
	if ladderSize < 1 {
		ladderSize = 1
	}
	cycle := campaign.Cycle
	if cycle < 1 {
		cycle = 1
	}
	cycleTotal := campaign.CycleTotal
	if cycleTotal < cycle {
		cycleTotal = cycle
	}
	return (cycle-1)*ladderSize + round + 1, cycleTotal * ladderSize
}

func (a *App) emitBackgroundStrategyCandidate(tag string, method ServiceBypassMethod, round, ladderSize int, campaign serviceStrategySearchCampaign) {
	attempt, attemptTotal := backgroundStrategyAttempt(round, ladderSize, campaign)
	a.emitRouteProbe("route-probe-candidate", map[string]interface{}{
		"source":        backgroundServiceStrategySource,
		"serviceTag":    tag,
		"serviceName":   serviceDisplayNameForTag(tag),
		"methodTag":     method.Tag,
		"methodLabel":   method.Label,
		"methodKind":    "transparent",
		"status":        "checking",
		"attempt":       attempt,
		"attemptTotal":  attemptTotal,
		"strategyIndex": round + 1,
		"strategyTotal": ladderSize,
		"cycle":         campaign.Cycle,
		"cycleTotal":    campaign.CycleTotal,
	})
}

func (a *App) emitBackgroundStrategyService(
	tag string,
	method ServiceBypassMethod,
	success, final, retrying bool,
	status, detail string,
	round, ladderSize int,
	campaign serviceStrategySearchCampaign,
) {
	attempt, attemptTotal := backgroundStrategyAttempt(round, ladderSize, campaign)
	a.emitRouteProbe("route-probe-service", map[string]interface{}{
		"source":        backgroundServiceStrategySource,
		"tag":           tag,
		"name":          serviceDisplayNameForTag(tag),
		"methodTag":     method.Tag,
		"methodLabel":   method.Label,
		"success":       success,
		"final":         final,
		"retrying":      retrying,
		"status":        status,
		"error":         detail,
		"attempt":       attempt,
		"attemptTotal":  attemptTotal,
		"strategyIndex": round + 1,
		"strategyTotal": ladderSize,
		"cycle":         campaign.Cycle,
		"cycleTotal":    campaign.CycleTotal,
	})
}

func (a *App) emitBackgroundStrategyFallback(tag, method string, success bool, campaign serviceStrategySearchCampaign) {
	label := FreeAccessOutboundLabel(method)
	a.emitRouteProbe("route-probe-service", map[string]interface{}{
		"source":      backgroundServiceStrategySource,
		"tag":         tag,
		"name":        serviceDisplayNameForTag(tag),
		"methodTag":   method,
		"methodLabel": label,
		"success":     success,
		"final":       true,
		"fallback":    true,
		"status":      map[bool]string{true: "done", false: "failed"}[success],
		"cycle":       campaign.Cycle,
		"cycleTotal":  campaign.CycleTotal,
	})
}

// startupServiceSearchLadder keeps the current (usually cached) strategy first.
// If it still works the startup gate never changes it; only a confirmed failure
// advances through the remaining ranked methods.
func startupServiceSearchLadder(serviceTag string, current ServiceBypassMethod) []ServiceBypassMethod {
	ranked := rankedMethodsForService(serviceTag)
	ladder := make([]ServiceBypassMethod, 0, min(len(ranked), maxAutomaticServiceStrategies))
	if strings.TrimSpace(current.Tag) != "" {
		ladder = append(ladder, current)
	}
	for _, method := range ranked {
		if method.Tag == current.Tag {
			continue
		}
		ladder = append(ladder, method)
		if len(ladder) == maxAutomaticServiceStrategies {
			break
		}
	}
	return ladder
}

func recoveryServiceSearchLadder(serviceTag, currentTag string) []ServiceBypassMethod {
	ranked := rankedMethodsForService(serviceTag)
	limit := maxAutomaticServiceStrategies
	if strings.TrimSpace(currentTag) != "" {
		// The current strategy is confirmed immediately before recovery search,
		// so it consumes one of the bounded automatic attempts.
		limit--
	}
	ladder := make([]ServiceBypassMethod, 0, min(len(ranked), limit))
	for _, method := range ranked {
		if method.Tag == currentTag {
			continue
		}
		ladder = append(ladder, method)
		if len(ladder) == limit {
			break
		}
	}
	return ladder
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
	for _, method := range recoveryServiceSearchLadder(serviceTag, current.Method.Tag) {
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
	if a.trafficEngine == nil || a.trafficEngine.ActiveTag() != composedStrategyTag {
		for _, tag := range serviceTags {
			failing[tag] = true
		}
		return failing
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, tag := range serviceTags {
		svc, ok := findFreeAccessService(tag)
		if !ok || len(svc.ProbeTargets()) == 0 {
			continue
		}
		// The service group is a selector in Windows Unified. Force the direct
		// egress for this probe so VPN cannot create a false positive for winws2.
		previousRoute := a.currentServiceRoute(tag)
		if previousRoute == "" {
			mu.Lock()
			failing[tag] = true
			mu.Unlock()
			continue
		}
		if !a.switchServiceRoute(tag, "direct") {
			mu.Lock()
			failing[tag] = true
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(service FreeAccessService, restoreRoute string) {
			defer wg.Done()
			if restoreRoute != "" && restoreRoute != "direct" {
				defer func() {
					if !a.switchServiceRoute(service.Tag, restoreRoute) {
						a.writeLog(fmt.Sprintf("[FreeAccess] failed to restore %s selector to %s after probe", service.Tag, restoreRoute))
					}
				}()
			}
			candidate := routeProbeCandidate{
				Tag:       composedStrategyTag,
				Label:     "per-service",
				Kind:      "transparent",
				Client:    newDirectHTTPClient(),
				Available: true,
			}
			item := a.probeSingleCandidateQuiet(service, candidate)
			if !item.Success && a.routeStrategyWorkAllowed() {
				time.Sleep(serviceStrategyProbeRetryDelay)
				item = a.probeSingleCandidateQuiet(service, candidate)
			}
			if !item.Success {
				mu.Lock()
				failing[service.Tag] = true
				mu.Unlock()
			}
		}(svc, previousRoute)
	}
	wg.Wait()
	return failing
}

// applyServiceFreeFallback routes a service that no transparent method can fix to
// the VPN subscription when one exists, otherwise leaves it direct. In pure
// Windows Unified without a subscription this means the service stays blocked
// (the honest state) between attempts in the bounded one-hour campaign.
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

// retunePerServiceStrategy is the in-session reaction to a service that stopped
// working: confirm its current strategy, then search its ladder for a new first-
// working method, cache it, and recompose. If nothing works, fall back to
// VPN/direct. Runs under the discovery lock so the quick-check feedback guard
// suppresses spurious re-triggers.
func (a *App) retunePerServiceStrategy(serviceTag, reason string) error {
	if a.trafficEngine == nil || a.trafficEngine.ActiveTag() != composedStrategyTag {
		return fmt.Errorf("per-service engine is not active")
	}
	if !a.tryBeginRouteProbeDiscovery() {
		return fmt.Errorf("route method discovery is already running")
	}
	defer a.finishRouteProbeDiscovery()

	a.writeLog(fmt.Sprintf("[FreeAccess] per-service retune started for %s: %s", serviceTag, reason))
	if serviceTag == "discord" {
		a.removeServiceStrategyCacheEntry(serviceTag)
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	selections, _ := a.resolveServiceSelections(dir, cache)
	selection, handled := selections[serviceTag]
	if !handled {
		// A temporary VPN/direct fallback is not part of the composed engine. If
		// that fallback later fails, immediately give an automatic service's free
		// ladder another chance instead of waiting for the fallback TTL.
		service, exists := findFreeAccessService(serviceTag)
		if !exists || service.RequiresVPN || !serviceHasFreeBypass(serviceTag) {
			return fmt.Errorf("service %q has no transparent strategy to retune", serviceTag)
		}
		settings := a.storage.GetAppSettings()
		if !FreeMethodsAllowed(settings) {
			return fmt.Errorf("transparent strategies are disabled for service %q", serviceTag)
		}
		if method := FreeAccessServiceMethod(settings, serviceTag); method == FreeAccessMethodVPN || method == FreeAccessMethodDirect {
			return fmt.Errorf("service %q uses the explicit %s route", serviceTag, method)
		}
		hostlistPath, err := ensureServiceHostlist(dir, service)
		if err != nil {
			return fmt.Errorf("restore %s hostlist: %w", serviceTag, err)
		}
		ranked := rankedMethodsForService(serviceTag)
		if len(ranked) == 0 {
			return fmt.Errorf("service %q has an empty strategy ladder", serviceTag)
		}
		selection = serviceWinwsSelection{ServiceTag: serviceTag, HostlistPath: hostlistPath, Method: ranked[0]}
		selections[serviceTag] = selection
		if err := a.composeAndStartServiceEngine(selections); err != nil {
			return fmt.Errorf("restore %s to transparent engine: %w", serviceTag, err)
		}
		if !a.probeServicesThroughEngine([]string{serviceTag})[serviceTag] {
			a.cacheWebValidatedServiceMethod(serviceTag, selection.Method.Tag, "fallback-recovery")
			if !a.switchServiceRoute(serviceTag, "direct") {
				return fmt.Errorf("restore %s selector to confirmed transparent route", serviceTag)
			}
			if serviceTag == "discord" {
				a.writeLog(fmt.Sprintf("[FreeAccess] discord web/API recovered with %s; waiting for live voice proof", selection.Method.Label))
			} else {
				a.writeLog(fmt.Sprintf("[FreeAccess] %s recovered from fallback with %s", serviceTag, selection.Method.Label))
			}
			return nil
		}
	}

	// Confirm it actually still fails before disrupting the engine.
	if handled && !a.probeServicesThroughEngine([]string{serviceTag})[serviceTag] {
		if !a.switchServiceRoute(serviceTag, "direct") {
			return fmt.Errorf("keep %s on its confirmed transparent route", serviceTag)
		}
		a.writeLog(fmt.Sprintf("[FreeAccess] %s already works; keeping current method", serviceTag))
		return nil
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}

	method, ok := a.searchServiceStrategy(serviceTag, selections)
	if ok {
		selections[serviceTag] = serviceWinwsSelection{ServiceTag: serviceTag, HostlistPath: selections[serviceTag].HostlistPath, Method: method}
		a.cacheWebValidatedServiceMethod(serviceTag, method.Tag, "retune")
		if serviceTag == "discord" {
			a.writeLog(fmt.Sprintf("[FreeAccess] discord provisionally retuned to %s; waiting for live voice proof", method.Label))
		} else {
			a.writeLog(fmt.Sprintf("[FreeAccess] %s retuned to %s", serviceTag, method.Label))
		}
	} else {
		a.applyServiceFreeFallback(serviceTag)
		delete(selections, serviceTag)
	}
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("VPN is stopping")
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return fmt.Errorf("re-compose after retune: %w", err)
	}
	if ok && !a.switchServiceRoute(serviceTag, "direct") {
		return fmt.Errorf("activate confirmed transparent route for %s", serviceTag)
	}
	return nil
}
