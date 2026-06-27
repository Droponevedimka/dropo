package main

import (
	"net"
	"runtime"
)

func relaxTunStrictRoute(config map[string]interface{}) bool {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return false
	}

	changed := false
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		if inboundMap["strict_route"] != false {
			inboundMap["strict_route"] = false
			changed = true
		}
	}
	return changed
}

func disableTunIPv6(config map[string]interface{}) bool {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return false
	}

	changed := false
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		if filterAddressFieldIPv4Only(inboundMap, "address", true) {
			changed = true
		}
		for _, key := range []string{
			"route_address",
			"route_exclude_address",
			"inet4_route_address",
			"inet4_route_exclude_address",
		} {
			if filterAddressFieldIPv4Only(inboundMap, key, false) {
				changed = true
			}
		}
		for _, key := range []string{
			"inet6_address",
			"inet6_route_address",
			"inet6_route_exclude_address",
		} {
			if _, ok := inboundMap[key]; ok {
				delete(inboundMap, key)
				changed = true
			}
		}
	}
	return changed
}

func filterAddressFieldIPv4Only(values map[string]interface{}, key string, ensureAddress bool) bool {
	value, ok := values[key]
	if !ok {
		return false
	}

	switch typed := value.(type) {
	case string:
		if isIPv6AddressOrCIDR(typed) {
			if ensureAddress {
				values[key] = "172.19.0.1/30"
			} else {
				delete(values, key)
			}
			return true
		}
	case []string:
		filtered := make([]string, 0, len(typed))
		changed := false
		for _, item := range typed {
			if isIPv6AddressOrCIDR(item) {
				changed = true
				continue
			}
			filtered = append(filtered, item)
		}
		if ensureAddress && len(filtered) == 0 {
			filtered = append(filtered, "172.19.0.1/30")
			changed = true
		}
		if changed {
			if len(filtered) == 0 {
				delete(values, key)
			} else {
				values[key] = filtered
			}
		}
		return changed
	case []interface{}:
		filtered := make([]interface{}, 0, len(typed))
		changed := false
		for _, item := range typed {
			text, ok := item.(string)
			if ok && isIPv6AddressOrCIDR(text) {
				changed = true
				continue
			}
			filtered = append(filtered, item)
		}
		if ensureAddress && len(filtered) == 0 {
			filtered = append(filtered, "172.19.0.1/30")
			changed = true
		}
		if changed {
			if len(filtered) == 0 {
				delete(values, key)
			} else {
				values[key] = filtered
			}
		}
		return changed
	}

	return false
}

func isIPv6AddressOrCIDR(value string) bool {
	if ip, _, err := net.ParseCIDR(value); err == nil {
		return ip.To4() == nil
	}
	if ip := net.ParseIP(value); ip != nil {
		return ip.To4() == nil
	}
	return false
}

func clearDirectOutboundInterface(config map[string]interface{}) bool {
	outbounds, ok := config["outbounds"].([]interface{})
	if !ok {
		return false
	}

	changed := false
	for _, outbound := range outbounds {
		outboundMap, ok := outbound.(map[string]interface{})
		if !ok || outboundMap["tag"] != "direct" {
			continue
		}
		if _, ok := outboundMap["bind_interface"]; ok {
			delete(outboundMap, "bind_interface")
			changed = true
		}
		if _, ok := outboundMap["inet4_bind_address"]; ok {
			delete(outboundMap, "inet4_bind_address")
			changed = true
		}
		if _, ok := outboundMap["inet6_bind_address"]; ok {
			delete(outboundMap, "inet6_bind_address")
			changed = true
		}
	}
	return changed
}

func tunAutoRouteEnabled(config map[string]interface{}) bool {
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return false
	}

	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "tun" {
			continue
		}
		if enabled, _ := inboundMap["auto_route"].(bool); enabled {
			return true
		}
	}
	return false
}

func freeProxySidecarsCapturedByTun(config map[string]interface{}) bool {
	if runtime.GOOS != "windows" || !tunAutoRouteEnabled(config) {
		return false
	}
	// Older generated configs did not route helper processes directly. With
	// auto_route enabled those helpers were captured back into TUN and their
	// bypass packets effectively degraded to plain direct traffic. Current
	// configs add an early process_name -> direct rule for ciadpi/winws/etc.,
	// so proxy free methods are valid candidates again.
	return !freeAccessProcessRuleRoutesDirect(config)
}

func routeProbeFreeProxyTagsForConfig(config map[string]interface{}, tags []string) ([]string, bool) {
	if len(tags) == 0 || !freeProxySidecarsCapturedByTun(config) {
		return tags, false
	}
	return nil, true
}

func (a *App) freeProxySidecarsCapturedByActiveNetwork(config map[string]interface{}) bool {
	if a != nil {
		status := a.currentNetworkModeStatus()
		if status.Active == NetworkModeDeepWindows {
			return false
		}
	}
	return freeProxySidecarsCapturedByTun(config)
}

func (a *App) routeProbeFreeProxyTagsForActiveNetwork(config map[string]interface{}, tags []string) ([]string, bool) {
	if len(tags) == 0 || !a.freeProxySidecarsCapturedByActiveNetwork(config) {
		return tags, false
	}
	return nil, true
}

func freeAccessProcessRuleRoutesDirect(config map[string]interface{}) bool {
	route, ok := config["route"].(map[string]interface{})
	if !ok {
		return false
	}
	rules, ok := route["rules"].([]interface{})
	if !ok {
		return false
	}

	freeProcesses := make(map[string]bool)
	for _, name := range freeAccessProcessNames() {
		freeProcesses[name] = true
	}
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok || ruleMap["outbound"] != "direct" {
			continue
		}
		for _, processName := range normalizeProcessNameRule(ruleMap["process_name"]) {
			if freeProcesses[processName] {
				return true
			}
		}
	}
	return false
}

func normalizeProcessNameRule(value interface{}) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	default:
		return interfaceStringSlice(value)
	}
}
