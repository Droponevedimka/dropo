package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const (
	routeProbeHTTPTimeout    = 5 * time.Second
	routeProbeProxyReadyWait = 5 * time.Second
)

type routeProbeCandidate struct {
	Tag       string
	Label     string
	Kind      string
	Client    *http.Client
	Start     func() error
	Stop      func()
	Cleanup   func()
	Outbound  map[string]interface{}
	LastErr   string
	Available bool
}

type routeProbeServiceResult struct {
	Tag          string `json:"tag"`
	Name         string `json:"name"`
	MethodTag    string `json:"methodTag,omitempty"`
	MethodLabel  string `json:"methodLabel,omitempty"`
	MethodKind   string `json:"methodKind,omitempty"`
	LatencyMS    int64  `json:"latencyMs,omitempty"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
	RequiresVPN  bool   `json:"requiresVpn,omitempty"`
	Skipped      bool   `json:"skipped,omitempty"`
	CandidateNum int    `json:"candidateNum"`
}

type routeProbeCandidateResult struct {
	ServiceTag  string `json:"serviceTag"`
	ServiceName string `json:"serviceName"`
	MethodTag   string `json:"methodTag"`
	MethodLabel string `json:"methodLabel"`
	MethodKind  string `json:"methodKind"`
	LatencyMS   int64  `json:"latencyMs,omitempty"`
	Success     bool   `json:"success"`
	Error       string `json:"error,omitempty"`
	ProbeCount  int    `json:"probeCount,omitempty"`
}

type routeProbeReport struct {
	Services   []routeProbeServiceResult `json:"services"`
	DurationMS int64                     `json:"durationMs"`
	Reason     string                    `json:"reason,omitempty"`
}

func (a *App) runRouteProbeAndApply(configPath string, activeFreeAccessTags []string, reason string) (*routeProbeReport, error) {
	startedAt := time.Now()
	config, err := readJSONConfig(configPath)
	if err != nil {
		return nil, err
	}
	a.rememberRouteProbeResults(nil)

	settings := GlobalAppSettings{}
	if a.storage != nil {
		settings = a.storage.GetAppSettings()
	}

	proxySpecs := collectVPNProbeCandidateSpecs(config)
	hasVPNProxy := len(proxySpecs) > 0
	services := enabledRouteProbeServices(settings, hasVPNProxy)
	freeTags := activeFreeAccessTags
	if !FreeMethodsAllowed(settings) {
		freeTags = nil
	}
	if filteredFreeTags, skipped := a.routeProbeFreeProxyTagsForActiveNetwork(config, freeTags); skipped {
		a.writeLog("[RouteProbe] skipping proxy free methods under Windows TUN: helper process traffic is captured by auto_route")
		a.emitRouteProbe("route-probe-log", map[string]interface{}{
			"message": "Skipping ByeDPI/SpoofDPI proxy methods: Windows TUN auto_route captures helper process traffic. Using transparent methods and VPN candidates.",
		})
		freeTags = filteredFreeTags
	}
	transparentStrategies := []TransparentFreeAccessStrategy{}
	if FreeMethodsAllowed(settings) && a.zapret != nil {
		transparentStrategies = a.zapret.AvailableStrategies()
	}

	if len(services) == 0 {
		report := &routeProbeReport{
			DurationMS: time.Since(startedAt).Milliseconds(),
			Reason:     reason,
		}
		a.emitRouteProbe("route-probe-complete", report)
		return report, nil
	}

	a.emitRouteProbe("route-probe-start", map[string]interface{}{
		"reason":                 reason,
		"serviceCount":           len(services),
		"freeMethodCount":        len(freeTags),
		"transparentMethodCount": len(transparentStrategies),
		"vpnCandidateCount":      len(proxySpecs),
		"services":               routeProbeServiceSummaries(services),
	})
	a.emitRouteProbe("route-probe-log", map[string]interface{}{
		"message": fmt.Sprintf("Route check started: %d service(s), %d free proxy method(s), %d transparent method(s), %d VPN candidate(s)",
			len(services), len(freeTags), len(transparentStrategies), len(proxySpecs)),
	})

	candidates := make([]routeProbeCandidate, 0, len(freeTags)+len(transparentStrategies)+len(proxySpecs)+1)
	candidates = append(candidates, newDirectRouteProbeCandidate())
	for _, tag := range freeTags {
		candidate, err := newFreeRouteProbeCandidate(tag)
		if err != nil {
			a.emitRouteProbe("route-probe-log", map[string]interface{}{
				"message": fmt.Sprintf("%s is not available for checks: %v", FreeAccessOutboundLabel(tag), err),
			})
			continue
		}
		candidates = append(candidates, candidate)
	}
	for _, strategy := range transparentStrategies {
		candidates = append(candidates, a.newTransparentRouteProbeCandidate(strategy))
	}

	for _, spec := range proxySpecs {
		a.emitRouteProbe("route-probe-log", map[string]interface{}{
			"message": fmt.Sprintf("Starting temporary check proxy for %s", spec.Label),
		})
		candidate, err := a.newVPNRouteProbeCandidate(spec)
		if err != nil {
			a.emitRouteProbe("route-probe-log", map[string]interface{}{
				"message": fmt.Sprintf("%s is not available for checks: %v", spec.Label, err),
			})
			continue
		}
		candidates = append(candidates, candidate)
	}
	defer cleanupRouteProbeCandidates(candidates)

	report := &routeProbeReport{
		Services: make([]routeProbeServiceResult, 0, len(services)),
		Reason:   reason,
	}

	for _, svc := range services {
		serviceCandidates := candidatesForRouteProbeServiceWithSettings(svc, candidates, settings)
		a.emitRouteProbe("route-probe-log", map[string]interface{}{
			"message": fmt.Sprintf("Checking %s through %d candidate(s)", svc.DisplayName, len(serviceCandidates)),
		})

		result := a.probeServiceCandidates(svc, serviceCandidates)
		report.Services = append(report.Services, result)
		a.emitRouteProbe("route-probe-service", result)
	}

	if changed := applyRouteProbeSelectionsToConfig(config, report.Services); changed {
		if err := writeJSONConfig(configPath, config); err != nil {
			report.DurationMS = time.Since(startedAt).Milliseconds()
			a.emitRouteProbe("route-probe-complete", report)
			return report, err
		}
	}
	a.applyTransparentRouteProbeSelection(report.Services)
	a.rememberRouteProbeResults(report.Services)

	report.DurationMS = time.Since(startedAt).Milliseconds()
	a.emitRouteProbe("route-probe-complete", report)
	a.writeLog(fmt.Sprintf("[RouteProbe] completed in %d ms", report.DurationMS))
	for _, result := range report.Services {
		if result.Success {
			a.writeLog(fmt.Sprintf("[RouteProbe] %s -> %s (%d ms)", result.Name, result.MethodLabel, result.LatencyMS))
			continue
		}
		a.writeLog(fmt.Sprintf("[RouteProbe] %s -> no working method (%s)", result.Name, result.Error))
	}

	return report, nil
}

func enabledRouteProbeServices(settings GlobalAppSettings, hasVPNProxy bool) []FreeAccessService {
	services := make([]FreeAccessService, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			if hasVPNProxy {
				services = append(services, svc)
			}
			continue
		}
		if FreeAccessServiceEnabled(settings, svc.Tag) || hasVPNProxy {
			services = append(services, svc)
		}
	}
	return services
}

func routeProbeServiceSummaries(services []FreeAccessService) []map[string]interface{} {
	summaries := make([]map[string]interface{}, 0, len(services))
	for _, svc := range services {
		summaries = append(summaries, map[string]interface{}{
			"tag":            svc.Tag,
			"name":           svc.DisplayName,
			"domainSuffixes": append([]string(nil), svc.DomainSuffixes...),
			"ipCidrs":        append([]string(nil), svc.IPCIDRs...),
			"requiresVpn":    svc.RequiresVPN,
		})
	}
	return summaries
}

func (a *App) requireWorkingRouteAfterProbe(configPath string) error {
	if a.storage == nil {
		return nil
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		return nil
	}

	config, err := readJSONConfig(configPath)
	if err != nil {
		return err
	}
	hasVPNProxy := len(collectVPNProbeCandidateSpecs(config)) > 0
	if hasVPNProxy {
		return nil
	}

	services := enabledRouteProbeServices(settings, false)
	required := 0
	successes := 0
	failedNames := make([]string, 0, len(services))
	for _, svc := range services {
		if svc.RequiresVPN {
			continue
		}
		required++
		result, ok := a.lastRouteProbeResult(svc.Tag)
		if ok && result.Success && serviceBypassGroupHasUsableRoute(config, svc.Tag) {
			successes++
			continue
		}
		failedNames = append(failedNames, svc.DisplayName)
	}
	if required == 0 || successes > 0 {
		return nil
	}
	if len(failedNames) > 4 {
		failedNames = failedNames[:4]
	}
	return fmt.Errorf("не найден рабочий бесплатный метод обхода без VPN/VLESS; проверьте права администратора, зависшие процессы и доступность методов: %s", strings.Join(failedNames, ", "))
}

func serviceBypassGroupHasUsableRoute(config map[string]interface{}, serviceTag string) bool {
	outbounds, _ := config["outbounds"].([]interface{})
	groupTag := ServiceBypassGroupTag(serviceTag)
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || outboundMap["tag"] != groupTag {
			continue
		}
		for _, candidate := range interfaceStringSlice(outboundMap["outbounds"]) {
			if candidate != "" && candidate != NoRouteOutboundTag {
				return true
			}
		}
		return false
	}
	return false
}

func candidatesForRouteProbeService(service FreeAccessService, candidates []routeProbeCandidate) []routeProbeCandidate {
	return candidatesForRouteProbeServiceWithSettings(service, candidates, GlobalAppSettings{})
}

func candidatesForRouteProbeServiceWithSettings(service FreeAccessService, candidates []routeProbeCandidate, settings GlobalAppSettings) []routeProbeCandidate {
	filtered := make([]routeProbeCandidate, 0, len(candidates))
	override := FreeAccessServiceMethod(settings, service.Tag)
	for _, candidate := range candidates {
		if !candidate.Available || candidate.Client == nil {
			continue
		}
		if override == FreeAccessMethodDirect {
			if candidate.Kind != "direct" {
				continue
			}
		} else if override == FreeAccessMethodVPN {
			if candidate.Kind != "vpn" {
				continue
			}
		} else if override != FreeAccessMethodAuto {
			if candidate.Tag != override {
				continue
			}
		} else if candidate.Kind == "direct" {
			continue
		}
		if service.RequiresVPN && candidate.Kind != "vpn" && override != FreeAccessMethodDirect {
			continue
		}
		filtered = append(filtered, candidate)
	}
	return filtered
}

func (a *App) probeServiceCandidates(service FreeAccessService, candidates []routeProbeCandidate) routeProbeServiceResult {
	result := routeProbeServiceResult{
		Tag:          service.Tag,
		Name:         service.DisplayName,
		RequiresVPN:  service.RequiresVPN,
		CandidateNum: len(candidates),
	}
	if len(service.ProbeTargets()) == 0 {
		result.Skipped = true
		result.Error = "probe URL is not configured"
		return result
	}
	if len(candidates) == 0 {
		result.Error = "no available candidates"
		return result
	}

	proxyCandidates := make([]routeProbeCandidate, 0, len(candidates))
	transparentCandidates := make([]routeProbeCandidate, 0)
	for _, candidate := range candidates {
		if candidate.Kind == "transparent" {
			transparentCandidates = append(transparentCandidates, candidate)
			continue
		}
		proxyCandidates = append(proxyCandidates, candidate)
	}

	results := make([]routeProbeCandidateResult, len(proxyCandidates), len(candidates))
	var wg sync.WaitGroup
	for i, candidate := range proxyCandidates {
		wg.Add(1)
		go func(index int, c routeProbeCandidate) {
			defer wg.Done()
			results[index] = a.probeSingleCandidate(service, c)
		}(i, candidate)
	}
	wg.Wait()

	for _, candidate := range transparentCandidates {
		results = append(results, a.probeSingleCandidate(service, candidate))
	}

	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Success != results[j].Success {
			return results[i].Success
		}
		priorityI := routeProbeResultPriority(service, results[i])
		priorityJ := routeProbeResultPriority(service, results[j])
		if priorityI != priorityJ {
			return priorityI < priorityJ
		}
		return results[i].LatencyMS < results[j].LatencyMS
	})

	if len(results) > 0 && results[0].Success {
		best := results[0]
		result.Success = true
		result.MethodTag = best.MethodTag
		result.MethodLabel = best.MethodLabel
		result.MethodKind = best.MethodKind
		result.LatencyMS = best.LatencyMS
		return result
	}

	failures := make([]string, 0, len(results))
	for _, item := range results {
		if item.Error != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", item.MethodLabel, item.Error))
		}
	}
	if len(failures) == 0 {
		result.Error = "all candidates failed"
	} else {
		result.Error = strings.Join(failures, "; ")
	}
	return result
}

func routeProbeResultPriority(service FreeAccessService, result routeProbeCandidateResult) int {
	if !result.Success {
		return 100
	}
	if service.RequiresVPN {
		if result.MethodKind == "vpn" {
			return 0
		}
		return 10
	}
	switch result.MethodKind {
	case "transparent":
		return 0
	case "proxy", "free":
		return 1
	case "direct":
		return 2
	case "vpn":
		return 3
	default:
		return 4
	}
}

func (a *App) probeSingleCandidate(service FreeAccessService, candidate routeProbeCandidate) routeProbeCandidateResult {
	a.emitRouteProbe("route-probe-log", map[string]interface{}{
		"message": fmt.Sprintf("%s: probing %s", service.DisplayName, candidate.Label),
	})

	var startErr error
	if candidate.Start != nil {
		startErr = candidate.Start()
	}
	if candidate.Stop != nil {
		defer candidate.Stop()
	}

	item := routeProbeCandidateResult{
		ServiceTag:  service.Tag,
		ServiceName: service.DisplayName,
		MethodTag:   candidate.Tag,
		MethodLabel: candidate.Label,
		MethodKind:  candidate.Kind,
	}
	if startErr != nil {
		item.Error = compactProbeError(startErr)
		a.emitRouteProbe("route-probe-candidate", item)
		return item
	}

	targets := service.ProbeTargets()
	var totalLatency time.Duration
	for index, targetURL := range targets {
		a.emitRouteProbe("route-probe-log", map[string]interface{}{
			"message": fmt.Sprintf("%s: %s target %d/%d %s", service.DisplayName, candidate.Label, index+1, len(targets), probeTargetLabel(targetURL)),
		})

		latency, err := probeHTTPThroughClient(candidate.Client, targetURL)
		totalLatency += latency
		item.ProbeCount = index + 1
		item.LatencyMS = averageProbeLatency(totalLatency, item.ProbeCount).Milliseconds()
		if err != nil {
			item.Success = false
			item.Error = fmt.Sprintf("%s: %s", probeTargetLabel(targetURL), compactProbeError(err))
			a.emitRouteProbe("route-probe-candidate", item)
			return item
		}
	}

	item.Success = true
	a.emitRouteProbe("route-probe-candidate", item)
	return item
}

func averageProbeLatency(total time.Duration, count int) time.Duration {
	if count <= 0 {
		return 0
	}
	return total / time.Duration(count)
}

func probeTargetLabel(targetURL string) string {
	parsed, err := url.Parse(targetURL)
	if err != nil || parsed.Host == "" {
		return targetURL
	}
	if parsed.Path == "" || parsed.Path == "/" {
		return parsed.Host
	}
	return parsed.Host + parsed.Path
}

func newFreeRouteProbeCandidate(tag string) (routeProbeCandidate, error) {
	for _, strategy := range DefaultByeDPIStrategies {
		if strategy.Tag == tag {
			client, err := newSOCKSHTTPClient(net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", strategy.Port)))
			if err != nil {
				return routeProbeCandidate{}, err
			}
			return routeProbeCandidate{
				Tag:       tag,
				Label:     FreeAccessOutboundLabel(tag),
				Kind:      "free",
				Client:    client,
				Available: true,
			}, nil
		}
	}
	for _, method := range DefaultSpoofDPIMethods {
		if method.Tag != tag {
			continue
		}
		address := net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", method.Port))
		client, err := newFreeProxyHTTPClient(method.Scheme, address)
		if err != nil {
			return routeProbeCandidate{}, err
		}
		return routeProbeCandidate{
			Tag:       tag,
			Label:     FreeAccessOutboundLabel(tag),
			Kind:      "free",
			Client:    client,
			Available: true,
		}, nil
	}

	return routeProbeCandidate{}, fmt.Errorf("unknown free method tag %q", tag)
}

func newDirectRouteProbeCandidate() routeProbeCandidate {
	return routeProbeCandidate{
		Tag:       "direct",
		Label:     "Direct",
		Kind:      "direct",
		Client:    newDirectHTTPClient(),
		Available: true,
	}
}

func newFreeProxyHTTPClient(scheme, address string) (*http.Client, error) {
	switch strings.ToLower(scheme) {
	case "", "socks", "socks5":
		return newSOCKSHTTPClient(address)
	case "http":
		return newHTTPProxyClient("http://" + address), nil
	default:
		return nil, fmt.Errorf("unsupported free proxy scheme %q", scheme)
	}
}

func (a *App) newTransparentRouteProbeCandidate(strategy TransparentFreeAccessStrategy) routeProbeCandidate {
	var cleanup func()
	return routeProbeCandidate{
		Tag:    strategy.Tag,
		Label:  strategy.Label,
		Kind:   "transparent",
		Client: newDirectHTTPClient(),
		Start: func() error {
			var err error
			cleanup, err = a.startTransparentProbe(strategy)
			return err
		},
		Stop: func() {
			if cleanup != nil {
				cleanup()
				cleanup = nil
			}
		},
		Available: true,
	}
}

func (a *App) startTransparentProbe(strategy TransparentFreeAccessStrategy) (func(), error) {
	if a.zapret == nil {
		return nil, fmt.Errorf("zapret manager is not initialized")
	}
	return a.zapret.StartForProbe(strategy)
}

func (a *App) newVPNRouteProbeCandidate(spec routeProbeCandidate) (routeProbeCandidate, error) {
	if a.singboxPath == "" || !fileExists(a.singboxPath) {
		return routeProbeCandidate{}, errors.New("sing-box is not available")
	}
	workDir := a.basePath
	if a.storage != nil {
		workDir = a.storage.GetResourcesPath()
	}

	instance, err := startTemporarySingBoxProxy(a.singboxPath, workDir, spec.Outbound)
	if err != nil {
		return routeProbeCandidate{}, err
	}
	return routeProbeCandidate{
		Tag:       spec.Tag,
		Label:     spec.Label,
		Kind:      "vpn",
		Client:    newHTTPProxyClient(instance.proxyURL),
		Cleanup:   instance.cleanup,
		Outbound:  spec.Outbound,
		Available: true,
	}, nil
}

func collectVPNProbeCandidateSpecs(config map[string]interface{}) []routeProbeCandidate {
	outbounds, _ := config["outbounds"].([]interface{})
	byTag := make(map[string]map[string]interface{}, len(outbounds))
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		if tag != "" {
			byTag[tag] = outboundMap
		}
	}

	autoSelect, ok := byTag["auto-select"]
	if !ok {
		return nil
	}

	proxyTags := interfaceStringSlice(autoSelect["outbounds"])
	candidates := make([]routeProbeCandidate, 0, len(proxyTags))
	for _, tag := range proxyTags {
		if isRouteProbeNonVPNTag(tag) {
			continue
		}
		outbound, ok := byTag[tag]
		if !ok {
			continue
		}
		outboundType, _ := outbound["type"].(string)
		if outboundType == "" || isSingBoxGroupType(outboundType) {
			continue
		}
		candidates = append(candidates, routeProbeCandidate{
			Tag:      tag,
			Label:    routeProbeVPNLabel(tag, outbound),
			Kind:     "vpn",
			Outbound: cloneJSONMap(outbound),
		})
	}
	return candidates
}

func configHasVPNProbeCandidates(configPath string) (bool, error) {
	config, err := readJSONConfig(configPath)
	if err != nil {
		return false, err
	}
	return len(collectVPNProbeCandidateSpecs(config)) > 0, nil
}

func isRouteProbeNonVPNTag(tag string) bool {
	if tag == "" || tag == "direct" || tag == "proxy" || tag == "auto-select" || tag == RuProxyOutboundTag || tag == NoRouteOutboundTag {
		return true
	}
	for _, freeTag := range FreeAccessMethodTags() {
		if tag == freeTag {
			return true
		}
	}
	for _, freeTag := range FreeAccessTransparentMethodTags() {
		if tag == freeTag {
			return true
		}
	}
	return strings.HasPrefix(tag, "bypass-") || tag == SmartBypassGroupTag || tag == VpnOrDirectGroupTag || tag == RuRouteGroupTag
}

func isSingBoxGroupType(outboundType string) bool {
	switch outboundType {
	case "selector", "urltest", "fallback", "loadbalance":
		return true
	default:
		return false
	}
}

func routeProbeVPNLabel(tag string, outbound map[string]interface{}) string {
	outboundType, _ := outbound["type"].(string)
	server, _ := outbound["server"].(string)
	if outboundType == "" {
		outboundType = "proxy"
	}
	if server == "" || strings.HasPrefix(server, "127.") || server == "localhost" {
		return fmt.Sprintf("VPN %s (%s)", tag, outboundType)
	}
	return fmt.Sprintf("VPN %s (%s)", server, outboundType)
}

func applyRouteProbeSelectionsToConfig(config map[string]interface{}, results []routeProbeServiceResult) bool {
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return false
	}

	selections := make(map[string]string, len(results))
	tagStats := map[string][]int64{}
	tagKinds := map[string]string{}
	for _, result := range results {
		if !result.Success || result.MethodTag == "" {
			continue
		}
		selections[result.Tag] = result.MethodTag
		tagStats[result.MethodTag] = append(tagStats[result.MethodTag], result.LatencyMS)
		tagKinds[result.MethodTag] = result.MethodKind
	}

	changed := false
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok {
			continue
		}
		tag, _ := outboundMap["tag"].(string)
		if strings.HasPrefix(tag, "bypass-") {
			serviceTag := strings.TrimPrefix(tag, "bypass-")
			if selected := selections[serviceTag]; selected != "" {
				applyRouteProbeSelectionToGroup(outboundMap, routeProbeConfigOutbound(selected, tagKinds[selected]), tagKinds[selected])
				changed = true
			}
			continue
		}
		if tag == SmartBypassGroupTag {
			selected := bestAggregateProbeTag(tagStats)
			if selected == "" {
				continue
			}
			// smart-bypass is the catch-all for blocked domains NOT covered by a
			// per-service winws profile, so 'direct' would not desync them. If the
			// aggregate winner is a transparent (winws) method and a VPN fallback
			// exists, leave the group on its built free-proxy+VPN form instead of
			// pinning it to transparent-direct. A VPN/proxy winner is applied
			// normally (prefer it).
			if tagKinds[selected] == "transparent" {
				hasVPNFallback := false
				for _, c := range interfaceStringSlice(outboundMap["outbounds"]) {
					if c == "auto-select" || c == "proxy" {
						hasVPNFallback = true
						break
					}
				}
				if hasVPNFallback {
					continue
				}
			}
			applyRouteProbeSelectionToGroup(outboundMap, routeProbeConfigOutbound(selected, tagKinds[selected]), tagKinds[selected])
			changed = true
		}
	}
	return changed
}

func ensureNoRouteOutbound(config map[string]interface{}) {
	outbounds, _ := config["outbounds"].([]interface{})
	if outboundTagExists(outbounds, NoRouteOutboundTag) {
		return
	}
	config["outbounds"] = append(outbounds, map[string]interface{}{
		"type": "block",
		"tag":  NoRouteOutboundTag,
	})
}

func routeProbeConfigOutbound(selected, kind string) string {
	if kind == "transparent" {
		return "direct"
	}
	if kind == "vpn" && selected == FreeAccessMethodVPN {
		return "auto-select"
	}
	return selected
}

func applyRouteProbeSelectionToGroup(outboundMap map[string]interface{}, selected, kind string) {
	if kind == "transparent" {
		pinTransparentOutboundGroup(outboundMap)
		return
	}
	preferOutboundGroupCandidate(outboundMap, selected)
}

func pinTransparentOutboundGroup(outboundMap map[string]interface{}) {
	candidates := interfaceStringSlice(outboundMap["outbounds"])
	filtered := []string{"direct"}
	for _, candidate := range candidates {
		switch candidate {
		case "direct":
			continue
		case "auto-select", "proxy":
			filtered = append(filtered, candidate)
		}
	}
	outboundMap["outbounds"] = filtered

	if len(filtered) > 1 {
		// Hybrid (VPN subscription present): keep the service on a urltest of
		// [direct, VPN]. winws desyncs the 'direct' path, so when the desync
		// works direct wins the health probe (free); when it can't open the
		// service, the probe over direct fails and the group auto-falls to the
		// VPN. The probe keeps the service-specific health URL set by the
		// original resilient group so it reflects real reachability.
		outboundMap["type"] = "urltest"
		if url, ok := outboundMap["url"].(string); !ok || url == "" {
			outboundMap["url"] = resilientGroupTestURL
		}
		outboundMap["interval"] = "90s"
		outboundMap["tolerance"] = 0
		outboundMap["interrupt_exist_connections"] = false
		delete(outboundMap, "default")
		return
	}

	// No VPN fallback: pin to direct (winws handles it; nothing else to try).
	outboundMap["type"] = "selector"
	outboundMap["default"] = "direct"
	deleteOutboundGroupHealthCheckFields(outboundMap)
}

func preferOutboundGroupCandidate(outboundMap map[string]interface{}, selected string) {
	if selected == "" {
		return
	}
	candidates := interfaceStringSlice(outboundMap["outbounds"])
	if len(candidates) == 0 {
		pinOutboundGroup(outboundMap, selected)
		return
	}

	reordered := make([]string, 0, len(candidates)+1)
	reordered = append(reordered, selected)
	for _, candidate := range candidates {
		if candidate != selected {
			reordered = append(reordered, candidate)
		}
	}
	outboundMap["outbounds"] = reordered
	if outboundMap["type"] == "selector" {
		outboundMap["default"] = selected
	}
}

func pinOutboundGroup(outboundMap map[string]interface{}, selected string) {
	outboundMap["type"] = "selector"
	outboundMap["outbounds"] = []string{selected}
	outboundMap["default"] = selected
	deleteOutboundGroupHealthCheckFields(outboundMap)
}

func deleteOutboundGroupHealthCheckFields(outboundMap map[string]interface{}) {
	delete(outboundMap, "url")
	delete(outboundMap, "interval")
	delete(outboundMap, "tolerance")
	delete(outboundMap, "interrupt_exist_connections")
}

func bestAggregateProbeTag(tagStats map[string][]int64) string {
	type scoredTag struct {
		tag      string
		wins     int
		avgDelay int64
	}
	scores := make([]scoredTag, 0, len(tagStats))
	for tag, delays := range tagStats {
		if len(delays) == 0 {
			continue
		}
		var sum int64
		for _, delay := range delays {
			sum += delay
		}
		scores = append(scores, scoredTag{tag: tag, wins: len(delays), avgDelay: sum / int64(len(delays))})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].wins != scores[j].wins {
			return scores[i].wins > scores[j].wins
		}
		return scores[i].avgDelay < scores[j].avgDelay
	})
	if len(scores) == 0 {
		return ""
	}
	return scores[0].tag
}

func probeHTTPThroughClient(client *http.Client, targetURL string) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(context.Background(), routeProbeHTTPTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "dropo-route-probe/2.0")
	req.Header.Set("Accept", "*/*")

	startedAt := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 1024)

	latency := time.Since(startedAt)
	if resp.StatusCode >= 500 {
		return latency, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return latency, nil
}

func newSOCKSHTTPClient(proxyAddress string) (*http.Client, error) {
	forward := &net.Dialer{Timeout: routeProbeHTTPTimeout, KeepAlive: 30 * time.Second}
	socksDialer, err := proxy.SOCKS5("tcp", proxyAddress, nil, forward)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			type dialResult struct {
				conn net.Conn
				err  error
			}
			done := make(chan dialResult, 1)
			go func() {
				conn, err := socksDialer.Dial(network, address)
				done <- dialResult{conn: conn, err: err}
			}()
			select {
			case result := <-done:
				return result.conn, result.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   routeProbeHTTPTimeout,
		ResponseHeaderTimeout: routeProbeHTTPTimeout,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &http.Client{Transport: transport, Timeout: routeProbeHTTPTimeout}, nil
}

func newHTTPProxyClient(proxyAddress string) *http.Client {
	proxyURL, _ := url.Parse(proxyAddress)
	return &http.Client{
		Timeout: routeProbeHTTPTimeout,
		Transport: &http.Transport{
			Proxy:                 http.ProxyURL(proxyURL),
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   routeProbeHTTPTimeout,
			ResponseHeaderTimeout: routeProbeHTTPTimeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func newDirectHTTPClient() *http.Client {
	return &http.Client{
		Timeout: routeProbeHTTPTimeout,
		Transport: &http.Transport{
			ForceAttemptHTTP2:     true,
			TLSHandshakeTimeout:   routeProbeHTTPTimeout,
			ResponseHeaderTimeout: routeProbeHTTPTimeout,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

type temporarySingBoxProxy struct {
	proxyURL   string
	cmd        *exec.Cmd
	configPath string
}

func startTemporarySingBoxProxy(singboxPath, workDir string, outbound map[string]interface{}) (*temporarySingBoxProxy, error) {
	port, err := reserveLocalTCPPort()
	if err != nil {
		return nil, err
	}

	tag, _ := outbound["tag"].(string)
	if tag == "" {
		return nil, errors.New("proxy outbound has no tag")
	}

	config := map[string]interface{}{
		"log": map[string]interface{}{"level": "error"},
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":        "mixed",
				"tag":         "route-probe-in",
				"listen":      "127.0.0.1",
				"listen_port": port,
			},
		},
		"outbounds": []interface{}{
			cloneJSONMap(outbound),
			map[string]interface{}{"type": "direct", "tag": "direct"},
		},
		"route": map[string]interface{}{
			"auto_detect_interface": true,
			"final":                 tag,
		},
	}

	file, err := os.CreateTemp("", "dropo-route-probe-*.json")
	if err != nil {
		return nil, err
	}
	configPath := file.Name()
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		file.Close()
		os.Remove(configPath)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(configPath)
		return nil, err
	}

	cmd := exec.Command(singboxPath, "run", "-c", configPath)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if workDir != "" {
		cmd.Dir = workDir
	}
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(configPath)
		return nil, err
	}
	attachManagedCmdToJob(cmd, "route-probe sing-box", nil)

	instance := &temporarySingBoxProxy{
		proxyURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		cmd:        cmd,
		configPath: configPath,
	}
	go cmd.Wait()

	if err := waitForTCPPort("127.0.0.1", port, routeProbeProxyReadyWait); err != nil {
		instance.cleanup()
		return nil, err
	}
	return instance, nil
}

func (p *temporarySingBoxProxy) cleanup() {
	if p == nil {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		terminateProcessTree(p.cmd)
	}
	if p.configPath != "" {
		_ = os.Remove(p.configPath)
	}
}

func cleanupRouteProbeCandidates(candidates []routeProbeCandidate) {
	for _, candidate := range candidates {
		if candidate.Cleanup != nil {
			candidate.Cleanup()
		}
	}
}

func reserveLocalTCPPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return 0, errors.New("failed to reserve TCP port")
	}
	return addr.Port, nil
}

func waitForTCPPort(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	address := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(120 * time.Millisecond)
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("port %d did not become ready", port)
}

func readJSONConfig(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return config, nil
}

func writeJSONConfig(path string, config map[string]interface{}) error {
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0644)
}

func cloneJSONMap(source map[string]interface{}) map[string]interface{} {
	data, err := json.Marshal(source)
	if err != nil {
		return copyMap(source)
	}
	var cloned map[string]interface{}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return copyMap(source)
	}
	return cloned
}

func compactProbeError(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	text = strings.ReplaceAll(text, "\r", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if len(text) > 160 {
		text = text[:157] + "..."
	}
	return text
}

func (a *App) applyTransparentRouteProbeSelection(results []routeProbeServiceResult) {
	if a.zapret == nil {
		return
	}
	// The per-service composed engine (startDeepWindowsPerServiceEngine in Deep
	// Windows mode, startComposedTransparentEngine in hybrid TUN mode) is now the
	// ONLY winws engine. Never start a competing single global strategy here —
	// that caused a redundant winws start + restart at every connect.
	return
}

func (a *App) routeProbeResultsSnapshot() []routeProbeServiceResult {
	a.lastRouteProbeMu.RLock()
	defer a.lastRouteProbeMu.RUnlock()

	results := make([]routeProbeServiceResult, 0, len(a.lastRouteProbe))
	for _, result := range a.lastRouteProbe {
		results = append(results, result)
	}
	sort.SliceStable(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})
	return results
}

func (a *App) rememberRouteProbeResults(results []routeProbeServiceResult) {
	a.lastRouteProbeMu.Lock()
	defer a.lastRouteProbeMu.Unlock()

	a.lastRouteProbe = make(map[string]routeProbeServiceResult, len(results))
	for _, result := range results {
		a.lastRouteProbe[result.Tag] = result
	}
}

func (a *App) lastRouteProbeResult(serviceTag string) (routeProbeServiceResult, bool) {
	a.lastRouteProbeMu.RLock()
	defer a.lastRouteProbeMu.RUnlock()
	if a.lastRouteProbe == nil {
		return routeProbeServiceResult{}, false
	}
	result, ok := a.lastRouteProbe[serviceTag]
	return result, ok
}

func (a *App) lastRouteProbeCatchAll() (routeProbeServiceResult, bool) {
	a.lastRouteProbeMu.RLock()
	defer a.lastRouteProbeMu.RUnlock()
	if a.lastRouteProbe == nil {
		return routeProbeServiceResult{}, false
	}

	stats := map[string][]int64{}
	byTag := map[string]routeProbeServiceResult{}
	for _, result := range a.lastRouteProbe {
		if !result.Success || result.MethodKind != "transparent" || result.MethodTag == "" {
			continue
		}
		stats[result.MethodTag] = append(stats[result.MethodTag], result.LatencyMS)
		byTag[result.MethodTag] = result
	}
	selected := bestAggregateProbeTag(stats)
	if selected == "" {
		return routeProbeServiceResult{}, false
	}
	return byTag[selected], true
}

func (a *App) emitRouteProbe(event string, payload interface{}) {
	if a.isShuttingDown() {
		return
	}
	a.emitEvent(event, payload)
}
