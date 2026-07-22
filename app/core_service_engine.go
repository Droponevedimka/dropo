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
)

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
	if err := a.startWindowsUnifiedServiceEngine(""); err != nil {
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
	if _, err := loadBlockedCatalog(a.runtimeBasePath()); err != nil {
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

// startWindowsUnifiedServiceEngine composes and starts the native traffic engine
// from the per-service selections (cache or top-ranked), then validates every
// selected service before Start returns. A working cached method wins on the
// first round and stays unchanged; only a confirmed failure advances its ladder.
func (a *App) startWindowsUnifiedServiceEngine(busyID string) error {
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
		a.trafficEngine.Stop()
		a.logServiceStrategySummary("all services use a temporary fallback")
		a.applyCommonBlockedFallback(commonFallback)
		return nil
	}

	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return fmt.Errorf("start per-service engine: %w", err)
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] per-service engine started for %d service(s); validating all, %d have no cached strategy",
		len(selections), len(needSearch)))
	if !a.winwsDebugEnabled() {
		if marker := a.winwsDebugMarkerPath(); marker != "" {
			a.writeLog(fmt.Sprintf("[FreeAccess] for detailed packet diagnostics: create empty file %q next to dropo.exe, reconnect, then send %q", marker, filepath.Join(a.basePath, "traffic-debug.log")))
		} else {
			a.writeLog("[FreeAccess] for detailed packet diagnostics: set DROPO_TRAFFIC_PACKET_DEBUG=1 and reconnect")
		}
	}

	validationTags := make([]string, 0, len(selections))
	for _, service := range DefaultFreeAccessServices {
		if _, ok := selections[service.Tag]; ok {
			validationTags = append(validationTags, service.Tag)
		}
	}
	if err := a.firstRunServiceSearch(busyID, selections, validationTags); err != nil {
		return fmt.Errorf("startup service strategy validation: %w", err)
	}
	if _, selected := selections[commonBlockedServiceTag]; selected {
		if err := a.selectCommonBlockedStrategy(busyID, selections); err != nil {
			a.writeLog(fmt.Sprintf("[FreeAccess] common blocked strategy selection failed: %v", err))
			delete(selections, commonBlockedServiceTag)
			if composeErr := a.composeAndStartServiceEngine(selections); composeErr != nil {
				return fmt.Errorf("remove failed common blocked strategy: %w", composeErr)
			}
			a.applyCommonBlockedFallback(a.preferredCommonBlockedFallback())
		}
	} else if commonFallback != "" {
		a.applyCommonBlockedFallback(commonFallback)
	}
	return nil
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

func (a *App) selectCommonBlockedStrategy(busyID string, selections map[string]serviceWinwsSelection) error {
	catalog, err := loadBlockedCatalog(a.runtimeBasePath())
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
	methods := make([]ServiceBypassMethod, 0, len(commonBlockedMethods()))
	if current.Tag != "" {
		methods = append(methods, current)
	}
	for _, method := range commonBlockedMethods() {
		if method.Tag != current.Tag {
			methods = append(methods, method)
		}
	}
	runner := nativeProbeRunner{}
	controller := nativeTrialController{manager: a.trafficEngine}
	probeNames := make([]string, 0, len(targets))
	for _, target := range targets {
		probeNames = append(probeNames, strings.TrimPrefix(strings.TrimSuffix(target.URL, "/"), "https://"))
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] common strategy random sample: %s", strings.Join(probeNames, ", ")))

	for _, method := range methods {
		if !a.routeStrategyWorkAllowed() {
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

// firstRunServiceSearch validates each requested service and finds the first
// ranked method that works. It is round-based: round R sets every still-failing
// service to its method[R] and recomposes the engine ONCE, then probes all of
// them in parallel. So the whole search costs at most (ladder length) restarts
// — not (services × methods) — keeping first enable bounded with many services.
// Round 0 reuses the already-running top-method engine without a restart.
func (a *App) firstRunServiceSearch(busyID string, selections map[string]serviceWinwsSelection, needSearch []string) error {
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
		if !a.routeStrategyWorkAllowed() {
			return fmt.Errorf("first-run strategy search interrupted because VPN is stopping")
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
				a.writeLog(fmt.Sprintf("[FreeAccess] first-run round %d recompose failed: %v", round, err))
				return fmt.Errorf("first-run round %d recompose: %w", round, err)
			}
		}

		failing := a.probeServicesThroughEngine(pending)
		next := make([]string, 0, len(pending))
		for _, tag := range pending {
			if !failing[tag] {
				a.cacheServiceMethod(tag, selections[tag].Method.Tag, "startup-validation")
				if !a.switchServiceRoute(tag, "direct") {
					return fmt.Errorf("activate confirmed startup strategy for %s", tag)
				}
				a.writeLog(fmt.Sprintf("[FreeAccess] %s: working method = %s", tag, selections[tag].Method.Label))
				continue
			}
			if round+1 < len(ladders[tag]) {
				next = append(next, tag)
			} else {
				a.writeLog(fmt.Sprintf("[FreeAccess] %s: no transparent method worked; using VPN/direct fallback", tag))
				a.applyServiceFreeFallback(tag)
				delete(selections, tag)
			}
		}
		pending = next
	}

	// Lock in whatever each service ended on.
	if !a.routeStrategyWorkAllowed() {
		return fmt.Errorf("first-run strategy search interrupted before commit")
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to re-compose engine after first-run search: %v", err))
		return fmt.Errorf("commit first-run service selections: %w", err)
	}
	a.logServiceStrategySummary("first-run search complete")
	return nil
}

// startupServiceSearchLadder keeps the current (usually cached) strategy first.
// If it still works the startup gate never changes it; only a confirmed failure
// advances through the remaining ranked methods.
func startupServiceSearchLadder(serviceTag string, current ServiceBypassMethod) []ServiceBypassMethod {
	ranked := rankedMethodsForService(serviceTag)
	ladder := make([]ServiceBypassMethod, 0, len(ranked))
	if strings.TrimSpace(current.Tag) != "" {
		ladder = append(ladder, current)
	}
	for _, method := range ranked {
		if method.Tag == current.Tag {
			continue
		}
		ladder = append(ladder, method)
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
			item := a.probeSingleCandidate(service, candidate)
			if !item.Success && a.routeStrategyWorkAllowed() {
				time.Sleep(serviceStrategyProbeRetryDelay)
				item = a.probeSingleCandidate(service, candidate)
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
			a.cacheServiceMethod(serviceTag, selection.Method.Tag, "fallback-recovery")
			if !a.switchServiceRoute(serviceTag, "direct") {
				return fmt.Errorf("restore %s selector to confirmed transparent route", serviceTag)
			}
			a.writeLog(fmt.Sprintf("[FreeAccess] %s recovered from fallback with %s", serviceTag, selection.Method.Label))
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
		a.cacheServiceMethod(serviceTag, method.Tag, "retune")
		a.writeLog(fmt.Sprintf("[FreeAccess] %s retuned to %s", serviceTag, method.Label))
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
