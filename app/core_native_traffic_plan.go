package main

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	traffic "dropo/trafficorchestrator"
)

func (a *App) buildNativeTrafficPlan(selections map[string]serviceWinwsSelection) (traffic.TrafficPlan, error) {
	strategies := traffic.BuiltinStrategies()
	strategyIDs := make([]string, 0, len(strategies))
	strategySet := make(map[string]struct{}, len(strategies))
	for _, strategy := range strategies {
		strategyIDs = append(strategyIDs, strategy.ID)
		strategySet[strategy.ID] = struct{}{}
	}
	plan := traffic.TrafficPlan{
		Revision:        1,
		CatalogRevision: traffic.BuiltinCatalogRevision,
		Strategies:      strategies,
	}
	if a != nil && a.trafficEngine != nil {
		plan.Revision = a.trafficEngine.CurrentPlan().Revision + 1
	}
	for _, service := range DefaultFreeAccessServices {
		selection, selected := selections[service.Tag]
		if !selected {
			continue
		}
		rule := nativeServiceRule(service, strategyIDs)
		if service.Tag == "discord" && a != nil && a.discordRealtime != nil {
			_, tcpPorts, udpPorts, udpIPs := a.discordRealtime.snapshot()
			rule.TCPPorts = append(rule.TCPPorts, tcpPorts...)
			rule.TCPPorts = uniqueSortedPorts(rule.TCPPorts)
			// A nil UDP port list deliberately means all ports. Discord allocates
			// voice/video ports dynamically; IP/fingerprint proof provides scope.
			rule.UDPPorts = nil
			for _, ip := range udpIPs {
				if parsed := net.ParseIP(ip); parsed != nil {
					if parsed.To4() != nil {
						rule.IPCIDRs = append(rule.IPCIDRs, ip+"/32")
					} else {
						rule.IPCIDRs = append(rule.IPCIDRs, ip+"/128")
					}
				}
			}
			for index, port := range udpPorts {
				if index >= len(udpIPs) {
					break
				}
				rule.ProbeTargets = append(rule.ProbeTargets, traffic.ProbeTarget{
					ID: fmt.Sprintf("discord-media-%d", index+1), Network: traffic.NetworkUDP,
					Kind: traffic.ProbeDiscordMedia, Host: udpIPs[index], Port: port, TimeoutMS: 2000,
				})
			}
		}
		strategyID := selection.Method.NativeStrategyID
		if _, ok := strategySet[strategyID]; !ok {
			strategyID = strategies[0].ID
		}
		plan.Services = append(plan.Services, rule)
		plan.Selections = append(plan.Selections, traffic.ServiceSelection{ServiceID: service.Tag, StrategyID: strategyID})
	}
	if selection, selected := selections[commonBlockedServiceTag]; selected {
		catalog, err := loadBlockedCatalog(a.runtimeBasePath())
		if err != nil {
			return traffic.TrafficPlan{}, fmt.Errorf("load common blocked catalog: %w", err)
		}
		strategyID := selection.Method.NativeStrategyID
		if _, ok := strategySet[strategyID]; !ok {
			return traffic.TrafficPlan{}, fmt.Errorf("unknown common blocked strategy %q", strategyID)
		}
		plan.Services = append(plan.Services, traffic.ServiceRule{
			ID: commonBlockedServiceTag, DisplayName: "Bundled blocked catalog",
			DomainSuffixes: catalog.Domains, IPCIDRs: catalog.IPCIDRs,
			TCPPorts: []int{80, 443}, UDPPorts: []int{443},
			CandidateStrategyIDs: append([]string(nil), strategyIDs...),
			AllowVPNFallback:     true, AllowDirectFallback: true,
		})
		plan.Selections = append(plan.Selections, traffic.ServiceSelection{
			ServiceID: commonBlockedServiceTag, StrategyID: strategyID,
		})
	}
	a.addNativeWireGuardRules(&plan, strategyIDs)
	if err := traffic.ValidatePlan(plan); err != nil {
		return traffic.TrafficPlan{}, err
	}
	return plan, nil
}

func nativeServiceRule(service FreeAccessService, strategyIDs []string) traffic.ServiceRule {
	rule := traffic.ServiceRule{
		ID: service.Tag, DisplayName: service.DisplayName,
		DomainSuffixes:       append([]string(nil), service.DomainSuffixes...),
		IPCIDRs:              append([]string(nil), service.IPCIDRs...),
		ProcessNames:         append([]string(nil), service.ProcessNames...),
		TCPPorts:             []int{80, 443},
		UDPPorts:             []int{443},
		CandidateStrategyIDs: append([]string(nil), strategyIDs...),
		AllowVPNFallback:     true,
		AllowDirectFallback:  true,
	}
	if service.Tag == "discord" {
		rule.TCPPorts = append(rule.TCPPorts, normalizedDiscordTCPPorts(nil)...)
		rule.Fingerprints = []string{"stun", "discord-media"}
	}
	for index, targetURL := range service.ProbeTargets() {
		parsed, err := url.Parse(targetURL)
		if err != nil || parsed.Hostname() == "" {
			continue
		}
		port := 443
		if parsed.Scheme == "http" {
			port = 80
		}
		rule.ProbeTargets = append(rule.ProbeTargets, traffic.ProbeTarget{
			ID: fmt.Sprintf("%s-web-%d", service.Tag, index+1), Network: traffic.NetworkTCP,
			Kind: traffic.ProbeHTTP, URL: targetURL, Port: port, TimeoutMS: 5000,
		})
	}
	return rule
}

func (a *App) addNativeWireGuardRules(plan *traffic.TrafficPlan, strategyIDs []string) {
	if a == nil || a.storage == nil || plan == nil {
		return
	}
	settings, err := a.storage.GetUserSettings()
	if err != nil {
		return
	}
	for index, config := range settings.WireGuardConfigs {
		workID := fmt.Sprintf("work-%d", index+1)
		work := traffic.WorkNetworkRule{ID: workID}
		for _, cidr := range config.AllowedIPs {
			if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil {
				work.IPCIDRs = append(work.IPCIDRs, strings.TrimSpace(cidr))
			}
		}
		for _, domain := range config.GetInternalDomains() {
			domain = strings.TrimPrefix(strings.TrimSpace(domain), ".")
			if domain != "" {
				work.DomainSuffixes = append(work.DomainSuffixes, domain)
			}
		}
		if len(work.IPCIDRs)+len(work.DomainSuffixes) > 0 {
			plan.WorkNetworks = append(plan.WorkNetworks, work)
		}
	}
	for _, endpoint := range a.wireGuardCamouflageTargetsForSession() {
		cidrs := make([]string, 0, len(endpoint.IPs))
		for _, ip := range endpoint.IPs {
			parsed := net.ParseIP(ip)
			if parsed == nil {
				continue
			}
			if parsed.To4() != nil {
				cidrs = append(cidrs, ip+"/32")
			} else {
				cidrs = append(cidrs, ip+"/128")
			}
		}
		if endpoint.Port < 1 || len(cidrs) == 0 {
			continue
		}
		id := fmt.Sprintf("wg-handshake-%d", endpoint.ConfigID+1)
		plan.Services = append(plan.Services, traffic.ServiceRule{
			ID: id, DisplayName: "WireGuard handshake " + endpoint.Tag,
			IPCIDRs: cidrs, UDPPorts: []int{endpoint.Port},
			Fingerprints:         []string{"wireguard-initiation", "wireguard-cookie"},
			CandidateStrategyIDs: append([]string(nil), strategyIDs...),
			AllowDirectFallback:  true,
		})
		plan.Selections = append(plan.Selections, traffic.ServiceSelection{ServiceID: id, StrategyID: "native-decoy-split"})
	}
}

func uniqueSortedPorts(values []int) []int {
	set := make(map[int]struct{}, len(values))
	for _, value := range values {
		if value > 0 && value <= 65535 {
			set[value] = struct{}{}
		}
	}
	result := make([]int, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}
