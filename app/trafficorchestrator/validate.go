package trafficorchestrator

import (
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

const (
	maxSyntheticPackets  = 32
	maxBufferedFlowBytes = 256 * 1024
	maxPacketPosition    = 64 * 1024
)

// ValidatePlan rejects ambiguous or unsafe plans before they reach WinDivert.
func ValidatePlan(plan TrafficPlan) error {
	if plan.Revision == 0 {
		return errors.New("traffic plan revision must be positive")
	}
	if strings.TrimSpace(plan.CatalogRevision) == "" {
		return errors.New("traffic plan catalog revision is required")
	}

	strategies := make(map[string]TrafficStrategy, len(plan.Strategies))
	for _, strategy := range plan.Strategies {
		if err := ValidateStrategy(strategy); err != nil {
			return fmt.Errorf("strategy %q: %w", strategy.ID, err)
		}
		if _, exists := strategies[strategy.ID]; exists {
			return fmt.Errorf("duplicate strategy id %q", strategy.ID)
		}
		strategies[strategy.ID] = strategy
	}

	services := make(map[string]struct{}, len(plan.Services))
	for _, service := range plan.Services {
		if err := validateServiceRule(service, strategies); err != nil {
			return fmt.Errorf("service %q: %w", service.ID, err)
		}
		if _, exists := services[service.ID]; exists {
			return fmt.Errorf("duplicate service id %q", service.ID)
		}
		services[service.ID] = struct{}{}
	}
	workNetworks := make(map[string]struct{}, len(plan.WorkNetworks))
	for _, network := range plan.WorkNetworks {
		if err := validateWorkNetwork(network); err != nil {
			return fmt.Errorf("work network %q: %w", network.ID, err)
		}
		if _, duplicate := workNetworks[network.ID]; duplicate {
			return fmt.Errorf("duplicate work network id %q", network.ID)
		}
		workNetworks[network.ID] = struct{}{}
	}
	selections := make(map[string]struct{}, len(plan.Selections))
	for _, selection := range plan.Selections {
		if _, exists := services[selection.ServiceID]; !exists {
			return fmt.Errorf("selection references unknown service %q", selection.ServiceID)
		}
		if _, exists := strategies[selection.StrategyID]; !exists {
			return fmt.Errorf("selection for %q references unknown strategy %q", selection.ServiceID, selection.StrategyID)
		}
		if _, duplicate := selections[selection.ServiceID]; duplicate {
			return fmt.Errorf("duplicate selection for service %q", selection.ServiceID)
		}
		selections[selection.ServiceID] = struct{}{}
	}
	return nil
}

func validateWorkNetwork(network WorkNetworkRule) error {
	if !validIdentifier(network.ID) {
		return errors.New("invalid work network id")
	}
	if len(network.DomainSuffixes)+len(network.IPCIDRs) == 0 {
		return errors.New("at least one domain suffix or CIDR is required")
	}
	for _, suffix := range network.DomainSuffixes {
		if normalizeHost(suffix) == "" {
			return fmt.Errorf("invalid domain suffix %q", suffix)
		}
	}
	for _, cidr := range network.IPCIDRs {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	return nil
}

// ValidateStrategy enforces bounded transformations and supported fields.
func ValidateStrategy(strategy TrafficStrategy) error {
	if !validIdentifier(strategy.ID) {
		return errors.New("invalid strategy id")
	}
	if strategy.Revision <= 0 {
		return errors.New("revision must be positive")
	}
	if strings.TrimSpace(strategy.Label) == "" {
		return errors.New("label is required")
	}
	if len(strategy.TCP) == 0 && len(strategy.UDP) == 0 {
		return errors.New("at least one TCP or UDP action is required")
	}
	if strategy.Cost.SyntheticPackets < 0 || strategy.Cost.SyntheticPackets > maxSyntheticPackets {
		return fmt.Errorf("synthetic packet cost must be within 0..%d", maxSyntheticPackets)
	}
	if strategy.Cost.BufferedBytes < 0 || strategy.Cost.BufferedBytes > maxBufferedFlowBytes {
		return fmt.Errorf("buffered byte cost must be within 0..%d", maxBufferedFlowBytes)
	}
	if strategy.Cost.Risk < 0 || strategy.Cost.Risk > 100 {
		return errors.New("risk cost must be within 0..100")
	}
	if strategy.Constraints.MaxFlowData < 0 || strategy.Constraints.MaxFlowData > maxBufferedFlowBytes {
		return fmt.Errorf("maxFlowData must be within 0..%d", maxBufferedFlowBytes)
	}
	if err := validateNetworkList(strategy.Constraints.Networks); err != nil {
		return err
	}
	for i, action := range strategy.TCP {
		if err := validateAction(NetworkTCP, action); err != nil {
			return fmt.Errorf("TCP action %d: %w", i, err)
		}
	}
	for i, action := range strategy.UDP {
		if err := validateAction(NetworkUDP, action); err != nil {
			return fmt.Errorf("UDP action %d: %w", i, err)
		}
	}
	return nil
}

func validateAction(network Network, action PacketAction) error {
	switch action.Kind {
	case ActionPass:
		if action.Position != 0 || action.SequenceDelta != 0 || action.Overlap != 0 || action.TTL != 0 || action.Repeats != 0 || action.Payload != "" || action.InvalidSum {
			return errors.New("pass action cannot contain transformation fields")
		}
	case ActionFake:
		if action.Repeats < 1 || action.Repeats > maxSyntheticPackets {
			return fmt.Errorf("fake repeats must be within 1..%d", maxSyntheticPackets)
		}
		if strings.TrimSpace(action.Payload) == "" {
			return errors.New("fake payload is required")
		}
	case ActionSplit, ActionDisorder:
		if network != NetworkTCP {
			return fmt.Errorf("%s is TCP-only", action.Kind)
		}
		if action.Position < 1 || action.Position > maxPacketPosition {
			return fmt.Errorf("position must be within 1..%d", maxPacketPosition)
		}
	case ActionTTL:
		if action.TTL < 1 || action.TTL > 255 {
			return errors.New("ttl must be within 1..255")
		}
	case ActionSequenceOverlap:
		if network != NetworkTCP {
			return errors.New("sequence overlap is TCP-only")
		}
		if action.Overlap < 1 || action.Overlap > maxPacketPosition {
			return fmt.Errorf("overlap must be within 1..%d", maxPacketPosition)
		}
	case ActionRepeat:
		if action.Repeats < 1 || action.Repeats > maxSyntheticPackets {
			return fmt.Errorf("repeats must be within 1..%d", maxSyntheticPackets)
		}
	default:
		return fmt.Errorf("unsupported action kind %q", action.Kind)
	}
	return nil
}

func validateServiceRule(service ServiceRule, strategies map[string]TrafficStrategy) error {
	if !validIdentifier(service.ID) {
		return errors.New("invalid service id")
	}
	if strings.TrimSpace(service.DisplayName) == "" {
		return errors.New("display name is required")
	}
	if len(service.ExactHosts)+len(service.DomainSuffixes)+len(service.IPCIDRs)+len(service.Fingerprints) == 0 {
		return errors.New("at least one host, CIDR or protocol fingerprint is required")
	}
	for _, host := range append(append([]string(nil), service.ExactHosts...), service.DomainSuffixes...) {
		if normalizeHost(host) == "" {
			return fmt.Errorf("invalid host %q", host)
		}
	}
	for _, cidr := range service.IPCIDRs {
		if _, err := netip.ParsePrefix(strings.TrimSpace(cidr)); err != nil {
			return fmt.Errorf("invalid CIDR %q: %w", cidr, err)
		}
	}
	if err := validatePorts(NetworkTCP, service.TCPPorts); err != nil {
		return err
	}
	if err := validatePorts(NetworkUDP, service.UDPPorts); err != nil {
		return err
	}
	seenStrategies := make(map[string]struct{}, len(service.CandidateStrategyIDs))
	for _, id := range service.CandidateStrategyIDs {
		strategy, exists := strategies[id]
		if !exists {
			return fmt.Errorf("unknown candidate strategy %q", id)
		}
		if _, duplicate := seenStrategies[id]; duplicate {
			return fmt.Errorf("duplicate candidate strategy %q", id)
		}
		seenStrategies[id] = struct{}{}
		if !strategySupportsService(strategy, service) {
			return fmt.Errorf("candidate strategy %q does not support service transports", id)
		}
	}
	seenTargets := make(map[string]struct{}, len(service.ProbeTargets))
	for _, target := range service.ProbeTargets {
		if err := ValidateProbeTarget(target); err != nil {
			return fmt.Errorf("probe %q: %w", target.ID, err)
		}
		if _, duplicate := seenTargets[target.ID]; duplicate {
			return fmt.Errorf("duplicate probe id %q", target.ID)
		}
		seenTargets[target.ID] = struct{}{}
	}
	return nil
}

// ValidateProbeTarget validates externally supplied selector input.
func ValidateProbeTarget(target ProbeTarget) error {
	if !validIdentifier(target.ID) {
		return errors.New("invalid probe id")
	}
	if target.Network != NetworkTCP && target.Network != NetworkUDP {
		return fmt.Errorf("unsupported network %q", target.Network)
	}
	if target.Port < 1 || target.Port > 65535 {
		return errors.New("port must be within 1..65535")
	}
	switch target.Kind {
	case ProbeHTTP:
		u, err := url.Parse(strings.TrimSpace(target.URL))
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return errors.New("HTTP probe requires an http/https URL with a host")
		}
	case ProbeTCPConnect:
		if target.Network != NetworkTCP || normalizeHost(target.Host) == "" {
			return errors.New("TCP connect probe requires TCP and a host")
		}
	case ProbeUDPExchange, ProbeSTUN, ProbeDiscordMedia:
		if target.Network != NetworkUDP || normalizeHost(target.Host) == "" {
			return errors.New("UDP probe requires UDP and a host")
		}
	default:
		return fmt.Errorf("unsupported probe kind %q", target.Kind)
	}
	if target.Timeout < 0 || target.TimeoutMS < 0 {
		return errors.New("timeout cannot be negative")
	}
	return nil
}

func validateNetworkList(networks []Network) error {
	seen := map[Network]bool{}
	for _, network := range networks {
		if network != NetworkTCP && network != NetworkUDP {
			return fmt.Errorf("unsupported network %q", network)
		}
		if seen[network] {
			return fmt.Errorf("duplicate network %q", network)
		}
		seen[network] = true
	}
	return nil
}

func validatePorts(network Network, ports []int) error {
	copyPorts := append([]int(nil), ports...)
	sort.Ints(copyPorts)
	for i, port := range copyPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("%s port %d is outside 1..65535", network, port)
		}
		if i > 0 && port == copyPorts[i-1] {
			return fmt.Errorf("duplicate %s port %d", network, port)
		}
	}
	return nil
}

func strategySupportsService(strategy TrafficStrategy, service ServiceRule) bool {
	if len(service.TCPPorts) > 0 && len(strategy.TCP) == 0 {
		return false
	}
	if len(service.UDPPorts) > 0 && len(strategy.UDP) == 0 {
		return false
	}
	return true
}

func validIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 96 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}
