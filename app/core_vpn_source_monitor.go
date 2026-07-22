package main

import (
	"context"
	"fmt"
	"time"
)

const vpnSourceHealthInterval = 30 * time.Second

const (
	vpnSourceFailureThreshold  = 2
	vpnSourceRecoveryThreshold = 3
	vpnSourceCircuitOpen       = 2 * time.Minute
	vpnSourceSwitchCooldown    = 20 * time.Second
)

type vpnSourceHealthState struct {
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	OpenUntil            time.Time
}

func nextVPNSourceHealthState(state vpnSourceHealthState, healthy bool, now time.Time) vpnSourceHealthState {
	if healthy {
		state.ConsecutiveFailures = 0
		state.ConsecutiveSuccesses++
		if !now.Before(state.OpenUntil) {
			state.OpenUntil = time.Time{}
		}
		return state
	}
	state.ConsecutiveSuccesses = 0
	state.ConsecutiveFailures++
	if state.ConsecutiveFailures >= vpnSourceFailureThreshold {
		state.OpenUntil = now.Add(vpnSourceCircuitOpen)
	}
	return state
}

func (a *App) configuredVPNSourceTags() []string {
	if a == nil || a.storage == nil {
		return nil
	}
	profile, err := a.storage.GetActiveProfile()
	if err != nil {
		return nil
	}
	config, _ := a.storage.GetProfileConfig(profile.ID)
	outbounds, _ := config["outbounds"].([]interface{})
	result := make([]string, 0, len(profile.VPNSources))
	for _, source := range profile.VPNSources {
		if source.Disabled {
			continue
		}
		tag := "vpn-source-" + source.ID
		if outboundTagExists(outbounds, tag) {
			result = append(result, tag)
		}
	}
	return result
}

func (a *App) selectFirstHealthyVPNSource() string {
	tags := a.configuredVPNSourceTags()
	if len(tags) == 0 {
		return ""
	}
	for _, tag := range tags {
		healthy := a.vpnSourceHealthy(tag)
		a.recordVPNSourceHealth(tag, healthy, time.Now())
		if healthy && a.switchOutboundSelector("auto-select", tag) {
			a.activateVPNSource(tag, false)
			a.writeLog(fmt.Sprintf("[VPNSources] active source=%s; fallback order contains %d source(s)", tag, len(tags)))
			return tag
		}
		a.writeLog(fmt.Sprintf("[VPNSources] source %s failed its selected-node health check; trying next source", tag))
	}
	a.writeLog("[VPNSources] no selected source node passed health check; service routing will use its direct/local fallback policy")
	return ""
}

func (a *App) vpnSourceHealthy(tag string) bool {
	for attempt := 0; attempt < 2; attempt++ {
		result := a.TestProxyDelay(tag)
		if success, _ := result["success"].(bool); success {
			if delay, _ := result["delay"].(int); delay > 0 {
				return true
			}
		}
		if attempt == 0 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return false
}

func (a *App) startVPNSourceMonitor() {
	if a == nil {
		return
	}
	a.stopVPNSourceMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	a.vpnSourceMonitorMu.Lock()
	a.vpnSourceMonitorCancel = cancel
	a.vpnSourceHealth = make(map[string]vpnSourceHealthState)
	a.vpnSourceManual = ""
	a.vpnSourceLastSwitch = time.Time{}
	a.vpnSourceMonitorMu.Unlock()
	a.selectFirstHealthyVPNSource()
	go func() {
		ticker := time.NewTicker(vpnSourceHealthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.checkActiveVPNSource()
			}
		}
	}()
}

func (a *App) stopVPNSourceMonitor() {
	if a == nil {
		return
	}
	a.vpnSourceMonitorMu.Lock()
	cancel := a.vpnSourceMonitorCancel
	a.vpnSourceMonitorCancel = nil
	a.vpnSourceActive = ""
	a.vpnSourceManual = ""
	a.vpnSourceLastSwitch = time.Time{}
	a.vpnSourceHealth = nil
	a.vpnSourceMonitorMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) checkActiveVPNSource() {
	tags := a.configuredVPNSourceTags()
	if len(tags) == 0 {
		return
	}
	active := a.activeVPNSource()
	if active == "" {
		a.selectFirstHealthyVPNSource()
		return
	}
	now := time.Now()
	healthy := a.vpnSourceHealthy(active)
	activeState := a.recordVPNSourceHealth(active, healthy, now)
	if healthy {
		a.maybeRecoverPreferredVPNSource(tags, active, now)
		return
	}
	if activeState.ConsecutiveFailures < vpnSourceFailureThreshold {
		a.writeLog(fmt.Sprintf("[VPNSources] transient health failure for %s (%d/%d); keeping the active source", active, activeState.ConsecutiveFailures, vpnSourceFailureThreshold))
		return
	}
	a.clearManualVPNSource(active)
	start := 0
	for index, tag := range tags {
		if tag == active {
			start = index + 1
			break
		}
	}
	for offset := 0; offset < len(tags); offset++ {
		tag := tags[(start+offset)%len(tags)]
		if tag == active || !a.vpnSourceCanAttempt(tag, now) {
			continue
		}
		candidateHealthy := a.vpnSourceHealthy(tag)
		a.recordVPNSourceHealth(tag, candidateHealthy, now)
		if candidateHealthy && a.switchOutboundSelector("auto-select", tag) {
			a.activateVPNSource(tag, false)
			a.writeLog(fmt.Sprintf("[VPNSources] failed over from %s to %s; no sibling node was selected", active, tag))
			return
		}
	}
	a.writeLog(fmt.Sprintf("[VPNSources] active source %s failed and no next source is healthy", active))
}

func (a *App) maybeRecoverPreferredVPNSource(tags []string, active string, now time.Time) {
	activeIndex := -1
	a.vpnSourceMonitorMu.Lock()
	manual := a.vpnSourceManual
	lastSwitch := a.vpnSourceLastSwitch
	a.vpnSourceMonitorMu.Unlock()
	if manual != "" || now.Sub(lastSwitch) < vpnSourceSwitchCooldown {
		return
	}
	for index, tag := range tags {
		if tag == active {
			activeIndex = index
			break
		}
	}
	if activeIndex <= 0 {
		return
	}
	for _, tag := range tags[:activeIndex] {
		if !a.vpnSourceCanAttempt(tag, now) {
			continue
		}
		healthy := a.vpnSourceHealthy(tag)
		state := a.recordVPNSourceHealth(tag, healthy, now)
		if !healthy || state.ConsecutiveSuccesses < vpnSourceRecoveryThreshold {
			continue
		}
		if a.switchOutboundSelector("auto-select", tag) {
			a.activateVPNSource(tag, false)
			a.writeLog(fmt.Sprintf("[VPNSources] recovered preferred source %s after %d consecutive successful checks", tag, state.ConsecutiveSuccesses))
		}
		return
	}
}

func (a *App) recordVPNSourceHealth(tag string, healthy bool, now time.Time) vpnSourceHealthState {
	a.vpnSourceMonitorMu.Lock()
	defer a.vpnSourceMonitorMu.Unlock()
	if a.vpnSourceHealth == nil {
		a.vpnSourceHealth = make(map[string]vpnSourceHealthState)
	}
	state := nextVPNSourceHealthState(a.vpnSourceHealth[tag], healthy, now)
	a.vpnSourceHealth[tag] = state
	return state
}

func (a *App) vpnSourceCanAttempt(tag string, now time.Time) bool {
	a.vpnSourceMonitorMu.Lock()
	defer a.vpnSourceMonitorMu.Unlock()
	return !now.Before(a.vpnSourceHealth[tag].OpenUntil)
}

func (a *App) activateVPNSource(tag string, manual bool) {
	a.vpnSourceMonitorMu.Lock()
	a.vpnSourceActive = tag
	a.vpnSourceLastSwitch = time.Now()
	if manual {
		a.vpnSourceManual = tag
	}
	a.vpnSourceMonitorMu.Unlock()
}

func (a *App) clearManualVPNSource(tag string) {
	a.vpnSourceMonitorMu.Lock()
	if a.vpnSourceManual == tag {
		a.vpnSourceManual = ""
	}
	a.vpnSourceMonitorMu.Unlock()
}

func (a *App) activeVPNSource() string {
	a.vpnSourceMonitorMu.Lock()
	defer a.vpnSourceMonitorMu.Unlock()
	return a.vpnSourceActive
}

func (a *App) SelectVPNSource(id string) map[string]interface{} {
	tag := "vpn-source-" + normalizeVPNSourceID(id)
	found := false
	for _, candidate := range a.configuredVPNSourceTags() {
		if candidate == tag {
			found = true
			break
		}
	}
	if !found {
		return map[string]interface{}{"success": false, "error": "VPN source is unavailable"}
	}
	if !a.switchOutboundSelector("auto-select", tag) {
		return map[string]interface{}{"success": false, "error": "failed to switch VPN source"}
	}
	a.activateVPNSource(tag, true)
	return map[string]interface{}{"success": true, "source": id}
}
