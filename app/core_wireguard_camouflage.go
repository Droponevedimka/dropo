package main

import (
	"context"
	"fmt"
	"net"
	"runtime"
	"sort"
	"strings"
	"time"
)

type wireGuardIPLookup func(context.Context, string) ([]net.IPAddr, error)

func resolveWireGuardCamouflageTargets(
	ctx context.Context,
	configs []UserWireGuardConfig,
	disabled map[int]bool,
	lookup wireGuardIPLookup,
) ([]wireGuardCamouflageTarget, []string) {
	targets := make([]wireGuardCamouflageTarget, 0, len(configs))
	warnings := make([]string, 0)
	for configID, config := range configs {
		if !config.CamouflageEnabled || disabled[configID] || config.EndpointPort <= 0 {
			continue
		}
		ips := make([]string, 0, 2)
		if parsed := net.ParseIP(strings.Trim(config.Endpoint, "[]")); parsed != nil {
			ips = append(ips, parsed.String())
		} else {
			resolved, err := lookup(ctx, config.Endpoint)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: endpoint DNS lookup failed: %v", config.Tag, err))
				continue
			}
			seen := map[string]bool{}
			for _, address := range resolved {
				if address.IP == nil || address.IP.IsUnspecified() || address.IP.IsMulticast() {
					continue
				}
				ip := address.IP.String()
				if !seen[ip] {
					seen[ip] = true
					ips = append(ips, ip)
				}
			}
		}
		if len(ips) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s: endpoint resolved to no usable addresses", config.Tag))
			continue
		}
		sort.Strings(ips)
		targets = append(targets, wireGuardCamouflageTarget{
			ConfigID: configID,
			Tag:      config.Tag,
			Port:     config.EndpointPort,
			IPs:      ips,
		})
	}
	return targets, warnings
}

func (a *App) resetWireGuardCamouflageSession() {
	a.wireGuardCamouflageMu.Lock()
	a.wireGuardCamouflageReady = false
	a.wireGuardCamouflageTargets = nil
	a.wireGuardCamouflageDisabled = make(map[int]bool)
	a.wireGuardCamouflageMu.Unlock()
}

func (a *App) wireGuardCamouflageRequested() bool {
	if runtime.GOOS != "windows" || a == nil || a.storage == nil {
		return false
	}
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return false
	}
	for _, config := range settings.WireGuardConfigs {
		if config.CamouflageEnabled {
			return true
		}
	}
	return false
}

func (a *App) wireGuardCamouflageTargetsForSession() []wireGuardCamouflageTarget {
	if runtime.GOOS != "windows" || a == nil || a.storage == nil {
		return nil
	}
	a.wireGuardCamouflageMu.RLock()
	if a.wireGuardCamouflageReady {
		result := append([]wireGuardCamouflageTarget(nil), a.wireGuardCamouflageTargets...)
		a.wireGuardCamouflageMu.RUnlock()
		return result
	}
	a.wireGuardCamouflageMu.RUnlock()

	settings, err := a.storage.GetUserSettings()
	if err != nil {
		a.writeLog(fmt.Sprintf("[WireGuard] camouflage settings unavailable: %v", err))
		return nil
	}
	a.wireGuardCamouflageMu.RLock()
	disabled := make(map[int]bool, len(a.wireGuardCamouflageDisabled))
	for id, value := range a.wireGuardCamouflageDisabled {
		disabled[id] = value
	}
	a.wireGuardCamouflageMu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	targets, warnings := resolveWireGuardCamouflageTargets(ctx, settings.WireGuardConfigs, disabled, net.DefaultResolver.LookupIPAddr)
	for _, warning := range warnings {
		a.writeLog("[WireGuard] camouflage skipped for " + warning)
	}
	a.wireGuardCamouflageMu.Lock()
	// A health callback may have disabled a target while DNS was resolving.
	filtered := targets[:0]
	for _, target := range targets {
		if !a.wireGuardCamouflageDisabled[target.ConfigID] {
			filtered = append(filtered, target)
		}
	}
	a.wireGuardCamouflageTargets = append([]wireGuardCamouflageTarget(nil), filtered...)
	a.wireGuardCamouflageReady = true
	result := append([]wireGuardCamouflageTarget(nil), a.wireGuardCamouflageTargets...)
	a.wireGuardCamouflageMu.Unlock()
	return result
}

// disableWireGuardCamouflageForSession is fail-safe rollback: after the first
// unhealthy handshake the sidecar profile is removed before WireGuard restarts.
// The persisted opt-in is kept, so the next VPN session can try again.
func (a *App) disableWireGuardCamouflageForSession(configID int) {
	if a == nil || runtime.GOOS != "windows" {
		return
	}
	a.wireGuardCamouflageMu.Lock()
	if a.wireGuardCamouflageDisabled == nil {
		a.wireGuardCamouflageDisabled = make(map[int]bool)
	}
	if a.wireGuardCamouflageDisabled[configID] {
		a.wireGuardCamouflageMu.Unlock()
		return
	}
	enabled := false
	for _, target := range a.wireGuardCamouflageTargets {
		if target.ConfigID == configID {
			enabled = true
			break
		}
	}
	if !enabled {
		a.wireGuardCamouflageMu.Unlock()
		return
	}
	a.wireGuardCamouflageDisabled[configID] = true
	filtered := a.wireGuardCamouflageTargets[:0]
	for _, target := range a.wireGuardCamouflageTargets {
		if target.ConfigID != configID {
			filtered = append(filtered, target)
		}
	}
	a.wireGuardCamouflageTargets = filtered
	a.wireGuardCamouflageMu.Unlock()

	a.writeLog(fmt.Sprintf("[WireGuard] camouflage disabled for tunnel %d after unhealthy handshake; recomposing winws2 before restart", configID))
	a.AddToLogBuffer(fmt.Sprintf("WireGuard %d: обход zapret2 отключён для стабильности", configID))
	a.emitEvent("wireguard-camouflage-disabled", configID)
	if err := a.recomposeServiceEngineWithoutDiscovery(); err != nil {
		a.writeLog(fmt.Sprintf("[WireGuard] failed to recompose winws2 after camouflage rollback: %v", err))
	}
}

func (a *App) recomposeServiceEngineWithoutDiscovery() error {
	if a == nil || a.zapret == nil || a.storage == nil {
		return nil
	}
	selections := map[string]serviceWinwsSelection{}
	if FreeMethodsAllowed(a.storage.GetAppSettings()) {
		selections, _ = a.resolveServiceSelections(a.serviceHostlistDir(), a.loadServiceStrategyCache())
	}
	return a.composeAndStartServiceEngine(selections)
}
