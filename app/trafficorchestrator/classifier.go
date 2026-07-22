package trafficorchestrator

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"sort"
	"strings"
)

type compiledServiceRule struct {
	rule         ServiceRule
	exactHosts   map[string]struct{}
	suffixes     []string
	prefixes     []netip.Prefix
	processNames map[string]struct{}
	fingerprints map[string]struct{}
	tcpPorts     map[int]struct{}
	udpPorts     map[int]struct{}
}

// Classifier is immutable and safe for concurrent use. A new classifier is
// built off-path and swapped together with its TrafficPlan revision.
type Classifier struct {
	revision     uint64
	rules        []compiledServiceRule
	workNetworks []compiledWorkNetwork
}

type compiledWorkNetwork struct {
	id       string
	suffixes []string
	prefixes []netip.Prefix
}

// NewClassifier compiles a validated plan into matching structures.
func NewClassifier(plan TrafficPlan) (*Classifier, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	classifier := &Classifier{revision: plan.Revision, rules: make([]compiledServiceRule, 0, len(plan.Services))}
	for _, service := range plan.Services {
		compiled := compiledServiceRule{
			rule:         service,
			exactHosts:   stringSet(service.ExactHosts, normalizeHost),
			processNames: stringSet(service.ProcessNames, normalizeProcessName),
			fingerprints: stringSet(service.Fingerprints, normalizeToken),
			tcpPorts:     intSet(service.TCPPorts),
			udpPorts:     intSet(service.UDPPorts),
		}
		for _, suffix := range service.DomainSuffixes {
			compiled.suffixes = append(compiled.suffixes, normalizeHost(suffix))
		}
		sort.Slice(compiled.suffixes, func(i, j int) bool { return len(compiled.suffixes[i]) > len(compiled.suffixes[j]) })
		for _, cidr := range service.IPCIDRs {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(cidr))
			if err != nil {
				return nil, fmt.Errorf("compile service %s CIDR %s: %w", service.ID, cidr, err)
			}
			compiled.prefixes = append(compiled.prefixes, prefix.Masked())
		}
		classifier.rules = append(classifier.rules, compiled)
	}
	for _, network := range plan.WorkNetworks {
		compiled := compiledWorkNetwork{id: network.ID}
		for _, suffix := range network.DomainSuffixes {
			compiled.suffixes = append(compiled.suffixes, normalizeHost(suffix))
		}
		for _, cidr := range network.IPCIDRs {
			prefix, _ := netip.ParsePrefix(strings.TrimSpace(cidr))
			compiled.prefixes = append(compiled.prefixes, prefix.Masked())
		}
		classifier.workNetworks = append(classifier.workNetworks, compiled)
	}
	return classifier, nil
}

// Revision returns the TrafficPlan revision compiled into the classifier.
func (c *Classifier) Revision() uint64 {
	if c == nil {
		return 0
	}
	return c.revision
}

// Classify uses only observable flow evidence. Process name and port increase
// confidence but can never identify a service without host/IP/fingerprint proof.
func (c *Classifier) Classify(flow FlowEvidence) Classification {
	if c == nil || (flow.Network != NetworkTCP && flow.Network != NetworkUDP) || flow.Port < 1 || flow.Port > 65535 {
		return Classification{}
	}
	host := normalizeHost(flow.Host)
	processName := normalizeProcessName(flow.ProcessName)
	fingerprints := stringSet(flow.Fingerprints, normalizeToken)
	address, addressErr := netip.ParseAddr(strings.TrimSpace(flow.Destination))
	hasAddress := addressErr == nil
	for _, network := range c.workNetworks {
		for _, suffix := range network.suffixes {
			if host != "" && domainMatches(host, suffix) {
				return Classification{WorkNetwork: true, WorkNetworkID: network.id, Evidence: []string{"work-domain"}}
			}
		}
		if hasAddress {
			for _, prefix := range network.prefixes {
				if prefix.Contains(address) {
					return Classification{WorkNetwork: true, WorkNetworkID: network.id, Evidence: []string{"work-cidr"}}
				}
			}
		}
	}

	type candidate struct {
		result Classification
		index  int
	}
	best := candidate{index: -1}
	tied := false
	for index, rule := range c.rules {
		if !rule.portAllows(flow.Network, flow.Port) {
			continue
		}
		score, evidence, primary := rule.score(host, address, hasAddress, processName, fingerprints, flow.Port)
		if !primary {
			continue
		}
		if score > best.result.Score {
			best = candidate{result: Classification{Matched: true, ServiceID: rule.rule.ID, Score: score, Evidence: evidence}, index: index}
			tied = false
		} else if score == best.result.Score && score > 0 && best.index >= 0 && rule.rule.ID != best.result.ServiceID {
			tied = true
		}
	}
	if tied {
		return Classification{}
	}
	return best.result
}

func (r compiledServiceRule) portAllows(network Network, port int) bool {
	if network == NetworkTCP && len(r.tcpPorts) > 0 {
		_, ok := r.tcpPorts[port]
		return ok
	}
	if network == NetworkUDP && len(r.udpPorts) > 0 {
		_, ok := r.udpPorts[port]
		return ok
	}
	return true
}

func (r compiledServiceRule) score(host string, address netip.Addr, hasAddress bool, processName string, fingerprints map[string]struct{}, port int) (int, []string, bool) {
	score := 0
	primary := false
	evidence := make([]string, 0, 5)
	if _, ok := r.exactHosts[host]; ok && host != "" {
		score += 120
		primary = true
		evidence = append(evidence, "exact-host")
	}
	if host != "" {
		for _, suffix := range r.suffixes {
			if domainMatches(host, suffix) {
				score += 90 + min(len(strings.Split(suffix, ".")), 9)
				primary = true
				evidence = append(evidence, "domain-suffix")
				break
			}
		}
	}
	if hasAddress {
		bestBits := -1
		for _, prefix := range r.prefixes {
			if prefix.Contains(address) && prefix.Bits() > bestBits {
				bestBits = prefix.Bits()
			}
		}
		if bestBits >= 0 {
			score += 65 + min(bestBits/8, 20)
			primary = true
			evidence = append(evidence, "destination-cidr")
		}
	}
	for fingerprint := range fingerprints {
		if _, ok := r.fingerprints[fingerprint]; ok {
			score += 110
			primary = true
			evidence = append(evidence, "protocol-fingerprint")
			break
		}
	}
	if processName != "" {
		if _, ok := r.processNames[processName]; ok {
			score += 10
			evidence = append(evidence, "process")
		}
	}
	if primary {
		score += 2
		evidence = append(evidence, fmt.Sprintf("port:%d", port))
	}
	return score, evidence, primary
}

func stringSet(values []string, normalize func(string) string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if normalized := normalize(value); normalized != "" {
			result[normalized] = struct{}{}
		}
	}
	return result
}

func intSet(values []int) map[int]struct{} {
	result := make(map[int]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func normalizeHost(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, ".")
	value = strings.TrimPrefix(value, ".")
	if value == "" || strings.ContainsAny(value, " /\\\t\r\n") {
		return ""
	}
	return value
}

func normalizeProcessName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(value))
}

func normalizeToken(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func domainMatches(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}
