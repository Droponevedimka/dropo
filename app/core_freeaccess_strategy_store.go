package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	freeAccessStrategyFileName = "free_access_strategies.json"
	freeAccessStrategyVersion  = 1
)

type freeAccessStrategyFile struct {
	Version   int                           `json:"version"`
	UpdatedAt time.Time                     `json:"updatedAt"`
	Services  []freeAccessStrategySelection `json:"services"`
}

type freeAccessStrategySelection struct {
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	MethodTag   string `json:"methodTag"`
	MethodLabel string `json:"methodLabel"`
	MethodKind  string `json:"methodKind"`
	Source      string `json:"source"`
	LatencyMS   int64  `json:"latencyMs,omitempty"`
}

func (a *App) freeAccessStrategyPath() string {
	if a.storage != nil {
		return filepath.Join(a.storage.GetResourcesPath(), freeAccessStrategyFileName)
	}
	if a.basePath != "" {
		return filepath.Join(a.basePath, ResourcesFolder, freeAccessStrategyFileName)
	}
	return ""
}

func (a *App) loadFreeAccessStrategies() (map[string]freeAccessStrategySelection, error) {
	path := a.freeAccessStrategyPath()
	if path == "" {
		return nil, errors.New("free access strategy path is not available")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file freeAccessStrategyFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if file.Version != freeAccessStrategyVersion {
		return nil, fmt.Errorf("unsupported free access strategy version %d", file.Version)
	}
	out := make(map[string]freeAccessStrategySelection, len(file.Services))
	for _, selection := range file.Services {
		selection.MethodTag = NormalizeFreeAccessServiceMethod(selection.MethodTag)
		if selection.Tag == "" || selection.MethodTag == FreeAccessMethodAuto {
			continue
		}
		selection.MethodKind = freeAccessMethodKind(selection.MethodTag)
		if selection.MethodKind == "" {
			continue
		}
		if selection.MethodLabel == "" {
			selection.MethodLabel = FreeAccessOutboundLabel(selection.MethodTag)
		}
		out[selection.Tag] = selection
	}
	return out, nil
}

func (a *App) saveFreeAccessStrategySelections(results []routeProbeServiceResult) error {
	path := a.freeAccessStrategyPath()
	if path == "" {
		return errors.New("free access strategy path is not available")
	}
	services := make([]freeAccessStrategySelection, 0, len(results))
	for _, result := range results {
		if !result.Success || result.MethodTag == "" {
			continue
		}
		services = append(services, freeAccessStrategySelection{
			Tag:         result.Tag,
			Name:        result.Name,
			MethodTag:   result.MethodTag,
			MethodLabel: result.MethodLabel,
			MethodKind:  result.MethodKind,
			Source:      "manual-probe",
			LatencyMS:   result.LatencyMS,
		})
	}
	file := freeAccessStrategyFile{
		Version:   freeAccessStrategyVersion,
		UpdatedAt: time.Now(),
		Services:  services,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return err
	}
	a.writeLog(fmt.Sprintf("[FreeAccess] strategy file saved: %s", path))
	return nil
}

func (a *App) applyStoredFreeAccessStrategiesToConfig(configPath string, activeFreeAccessTags []string) (bool, error) {
	if configPath == "" {
		return false, errors.New("active config path is empty")
	}
	if a.storage == nil {
		return false, errors.New("storage is not initialized")
	}
	settings := a.storage.GetAppSettings()
	if !FreeMethodsAllowed(settings) {
		return false, nil
	}

	hasVPNProxy := false
	if ok, err := configHasVPNProbeCandidates(configPath); err == nil {
		hasVPNProxy = ok
	}
	stored, err := a.loadFreeAccessStrategies()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		a.writeLog(fmt.Sprintf("[FreeAccess] failed to load strategy file: %v", err))
	}
	serviceFallbackCache := a.loadServiceStrategyCache()

	activeProxyTags := make(map[string]bool, len(activeFreeAccessTags))
	for _, tag := range activeFreeAccessTags {
		activeProxyTags[tag] = true
	}
	transparentTags := a.availableTransparentStrategyTags()

	results := make([]routeProbeServiceResult, 0, len(DefaultFreeAccessServices))
	for _, svc := range DefaultFreeAccessServices {
		if !FreeAccessServiceEnabled(settings, svc.Tag) {
			continue
		}
		selection := a.selectFreeAccessStrategyForService(settings, svc, stored, serviceFallbackCache, activeProxyTags, transparentTags, hasVPNProxy)
		if selection.MethodTag == "" {
			continue
		}
		results = append(results, routeProbeServiceResult{
			Tag:         svc.Tag,
			Name:        svc.DisplayName,
			MethodTag:   selection.MethodTag,
			MethodLabel: selection.MethodLabel,
			MethodKind:  selection.MethodKind,
			LatencyMS:   selection.LatencyMS,
			Success:     true,
		})
	}
	if len(results) == 0 {
		return false, nil
	}
	config, err := readJSONConfig(configPath)
	if err != nil {
		return false, err
	}
	changed := applyRouteProbeSelectionsToConfig(config, results)
	if changed {
		if err := writeJSONConfig(configPath, config); err != nil {
			return false, err
		}
	}
	a.applyTransparentRouteProbeSelection(results)
	a.rememberRouteProbeResults(results)
	return changed, nil
}

func (a *App) selectFreeAccessStrategyForService(settings GlobalAppSettings, svc FreeAccessService, stored map[string]freeAccessStrategySelection, serviceFallbackCache map[string]serviceStrategyCacheEntry, activeProxyTags map[string]bool, transparentTags map[string]bool, hasVPNProxy bool) freeAccessStrategySelection {
	manual := FreeAccessServiceMethod(settings, svc.Tag)
	if manual != FreeAccessMethodAuto {
		if strategyMethodAvailable(manual, activeProxyTags, transparentTags, hasVPNProxy) {
			return makeFreeAccessStrategySelection(svc, manual, "manual", 0)
		}
		a.writeLog(fmt.Sprintf("[FreeAccess] manual method %s for %s is unavailable, falling back to automatic strategy", manual, svc.DisplayName))
	}
	if cached, ok := serviceFallbackCache[svc.Tag]; ok && isFreeAccessFallbackTag(cached.MethodTag) &&
		strategyMethodAvailable(cached.MethodTag, activeProxyTags, transparentTags, hasVPNProxy) {
		return makeFreeAccessStrategySelection(svc, cached.MethodTag, "service-cache-"+cached.Source, 0)
	}
	if svc.RequiresVPN {
		if !hasVPNProxy {
			return freeAccessStrategySelection{}
		}
		if selected, ok := stored[svc.Tag]; ok && selected.MethodTag == FreeAccessMethodVPN && strategyMethodAvailable(selected.MethodTag, activeProxyTags, transparentTags, hasVPNProxy) {
			selected.Source = "stored"
			return selected
		}
		return makeFreeAccessStrategySelection(svc, FreeAccessMethodVPN, "default-vpn", 0)
	}
	// Only services that actually have a free desync/proxy method (blockType
	// "dpi") may use winws/ByeDPI here. blockType "vpn"/"proxy" services (meta,
	// whatsapp, telegram) have NO working free route through sing-box: giving
	// them a transparent zapret default sent them to a DEAD 'direct' path under
	// the hybrid urltest (winws never desyncs them), so they appeared blocked
	// instead of falling back to the VPN. They must go straight to the VPN.
	// (Telegram's free path is the separate tg-ws-proxy sidecar, not this group.)
	hasFreeBypass := serviceHasFreeBypass(svc.Tag)

	var storedVPNFallback *freeAccessStrategySelection
	var storedFreeFallback *freeAccessStrategySelection
	if selected, ok := stored[svc.Tag]; ok && strategyMethodAvailable(selected.MethodTag, activeProxyTags, transparentTags, hasVPNProxy) {
		switch selected.MethodTag {
		case FreeAccessMethodVPN:
			storedVPNFallback = &selected
		default:
			if hasFreeBypass {
				selected.Source = "stored-fallback"
				storedFreeFallback = &selected
			}
		}
	}
	if hasFreeBypass {
		if tag := defaultZapretStrategyTag(transparentTags); tag != "" {
			return makeFreeAccessStrategySelection(svc, tag, "default-zapret", 0)
		}
		if storedFreeFallback != nil {
			return *storedFreeFallback
		}
		for tag := range activeProxyTags {
			if IsFreeAccessProxyMethod(tag) {
				return makeFreeAccessStrategySelection(svc, tag, "default-proxy", 0)
			}
		}
	}
	if storedVPNFallback != nil {
		storedVPNFallback.Source = "stored-fallback"
		return *storedVPNFallback
	}
	if hasVPNProxy {
		return makeFreeAccessStrategySelection(svc, FreeAccessMethodVPN, "default-vpn", 0)
	}
	return freeAccessStrategySelection{}
}

func makeFreeAccessStrategySelection(svc FreeAccessService, method string, source string, latency int64) freeAccessStrategySelection {
	method = NormalizeFreeAccessServiceMethod(method)
	return freeAccessStrategySelection{
		Tag:         svc.Tag,
		Name:        svc.DisplayName,
		MethodTag:   method,
		MethodLabel: FreeAccessOutboundLabel(method),
		MethodKind:  freeAccessMethodKind(method),
		Source:      source,
		LatencyMS:   latency,
	}
}

func (a *App) availableTransparentStrategyTags() map[string]bool {
	tags := map[string]bool{}
	if a != nil && a.zapret != nil {
		for _, strategy := range a.zapret.AvailableStrategies() {
			tags[strategy.Tag] = true
		}
		return tags
	}
	for _, strategy := range DefaultZapretTransparentStrategies {
		if !methodSupportsCurrentPlatform(strategy.Platforms) {
			continue
		}
		if a == nil || a.basePath == "" {
			continue
		}
		if !fileExists(filepath.Join(a.basePath, "bin", strategy.ExeName)) {
			continue
		}
		missingRuntimeFile := false
		for _, file := range []string{"cygwin1.dll", "WinDivert.dll", "WinDivert64.sys"} {
			if !fileExists(filepath.Join(a.basePath, "bin", file)) {
				missingRuntimeFile = true
				break
			}
		}
		if missingRuntimeFile {
			continue
		}
		missingRequiredFile := false
		for _, file := range strategy.RequiredFiles {
			if !fileExists(filepath.Join(a.basePath, "bin", file)) {
				missingRequiredFile = true
				break
			}
		}
		if !missingRequiredFile {
			tags[strategy.Tag] = true
		}
	}
	return tags
}

func defaultZapretStrategyTag(transparentTags map[string]bool) string {
	for _, strategy := range DefaultZapretTransparentStrategies {
		if transparentTags[strategy.Tag] {
			return strategy.Tag
		}
	}
	return ""
}

func freeAccessMethodKind(method string) string {
	switch method {
	case FreeAccessMethodDirect:
		return "direct"
	case FreeAccessMethodVPN:
		return "vpn"
	}
	if IsFreeAccessTransparentMethod(method) {
		return "transparent"
	}
	if IsFreeAccessProxyMethod(method) {
		return "proxy"
	}
	return ""
}

func strategyMethodAvailable(method string, activeProxyTags map[string]bool, transparentTags map[string]bool, hasVPNProxy bool) bool {
	switch method {
	case FreeAccessMethodDirect:
		return true
	case FreeAccessMethodVPN:
		return hasVPNProxy
	}
	if IsFreeAccessTransparentMethod(method) {
		return transparentTags[method]
	}
	if IsFreeAccessProxyMethod(method) {
		return activeProxyTags[method]
	}
	return false
}
