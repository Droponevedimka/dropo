package main

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"

	traffic "dropo/trafficorchestrator"
)

const (
	commonBlockedServiceTag = "blocked-catch-all"
	blockedDomainsFileName  = "domains_all.lst"
	blockedIPsFileName      = "ipsum.lst"
	commonBlockedProbeCount = 4
	maxBlockedCatalogItems  = 250_000
)

type blockedCatalog struct {
	Domains []string
	IPCIDRs []string
}

func loadBlockedCatalog(runtimeBasePath string) (blockedCatalog, error) {
	filtersPath := filepath.Join(runtimeBasePath, "bin", FiltersFolder)
	domains, err := readBlockedCatalogLines(filepath.Join(filtersPath, blockedDomainsFileName), true)
	if err != nil {
		return blockedCatalog{}, err
	}
	cidrs, err := readBlockedCatalogLines(filepath.Join(filtersPath, blockedIPsFileName), false)
	if err != nil {
		return blockedCatalog{}, err
	}
	domains = excludeNamedServiceDomains(domains)
	cidrs = excludeNamedServiceCIDRs(cidrs)
	if len(domains) < commonBlockedProbeCount {
		return blockedCatalog{}, fmt.Errorf("blocked domain catalog has only %d usable entries", len(domains))
	}
	return blockedCatalog{Domains: domains, IPCIDRs: cidrs}, nil
}

func readBlockedCatalogLines(path string, domains bool) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open bundled blocked catalog %s: %w", filepath.Base(path), err)
	}
	defer file.Close()

	seen := make(map[string]struct{})
	items := make([]string, 0, 32_768)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 64*1024)
	for scanner.Scan() {
		value := strings.ToLower(strings.TrimSpace(scanner.Text()))
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if domains {
			if !validCatalogDomain(value) {
				return nil, fmt.Errorf("invalid domain %q in %s", value, filepath.Base(path))
			}
		} else {
			prefix, parseErr := netip.ParsePrefix(value)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid CIDR %q in %s: %w", value, filepath.Base(path), parseErr)
			}
			prefix = prefix.Masked()
			if !publicCatalogPrefix(prefix) {
				continue
			}
			value = prefix.String()
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		if len(items) >= maxBlockedCatalogItems {
			return nil, fmt.Errorf("%s exceeds the %d-entry safety limit", filepath.Base(path), maxBlockedCatalogItems)
		}
		seen[value] = struct{}{}
		items = append(items, value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read bundled blocked catalog %s: %w", filepath.Base(path), err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("bundled blocked catalog %s is empty", filepath.Base(path))
	}
	sort.Strings(items)
	return items, nil
}

func validCatalogDomain(value string) bool {
	if len(value) > 253 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func publicCatalogPrefix(prefix netip.Prefix) bool {
	address := prefix.Addr()
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() || address.IsPrivate() || address.IsLinkLocalUnicast() {
		return false
	}
	reserved := []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"),
		netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
	}
	for _, blocked := range reserved {
		if blocked.Contains(address) || prefix.Contains(blocked.Addr()) {
			return false
		}
	}
	return true
}

func excludeNamedServiceDomains(domains []string) []string {
	suffixes := make([]string, 0)
	for _, service := range DefaultFreeAccessServices {
		for _, suffix := range service.DomainSuffixes {
			if suffix = strings.ToLower(strings.TrimSpace(suffix)); suffix != "" {
				suffixes = append(suffixes, suffix)
			}
		}
	}
	result := domains[:0]
	for _, domain := range domains {
		named := false
		for _, suffix := range suffixes {
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				named = true
				break
			}
		}
		if !named {
			result = append(result, domain)
		}
	}
	return result
}

func excludeNamedServiceCIDRs(cidrs []string) []string {
	named := make([]netip.Prefix, 0)
	for _, service := range DefaultFreeAccessServices {
		for _, raw := range service.IPCIDRs {
			if prefix, err := netip.ParsePrefix(raw); err == nil {
				named = append(named, prefix.Masked())
			}
		}
	}
	result := cidrs[:0]
	for _, raw := range cidrs {
		prefix, _ := netip.ParsePrefix(raw)
		overlaps := false
		for _, reserved := range named {
			if prefix.Contains(reserved.Addr()) || reserved.Contains(prefix.Addr()) {
				overlaps = true
				break
			}
		}
		if !overlaps {
			result = append(result, raw)
		}
	}
	return result
}

func commonBlockedMethods() []ServiceBypassMethod {
	strategies := traffic.BuiltinStrategies()
	methods := make([]ServiceBypassMethod, 0, len(strategies))
	for _, strategy := range strategies {
		methods = append(methods, ServiceBypassMethod{
			Tag: strategy.ID, Label: strategy.Label, NativeStrategyID: strategy.ID,
		})
	}
	return methods
}

func findCommonBlockedMethod(tag string) (ServiceBypassMethod, bool) {
	for _, method := range commonBlockedMethods() {
		if method.Tag == tag {
			return method, true
		}
	}
	return ServiceBypassMethod{}, false
}

func randomBlockedProbeTargets(domains []string, count int) ([]traffic.ProbeTarget, error) {
	if count < 1 || len(domains) < count {
		return nil, fmt.Errorf("need %d probe domains, catalog contains %d", count, len(domains))
	}
	indexes := make([]int, len(domains))
	for index := range indexes {
		indexes[index] = index
	}
	for index := 0; index < count; index++ {
		var bytes [8]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return nil, fmt.Errorf("select random blocked probes: %w", err)
		}
		selected := index + int(binary.LittleEndian.Uint64(bytes[:])%uint64(len(indexes)-index))
		indexes[index], indexes[selected] = indexes[selected], indexes[index]
	}
	targets := make([]traffic.ProbeTarget, 0, count)
	for index := 0; index < count; index++ {
		domain := domains[indexes[index]]
		targets = append(targets, traffic.ProbeTarget{
			ID: fmt.Sprintf("blocked-sample-%d", index+1), Network: traffic.NetworkTCP,
			Kind: traffic.ProbeHTTP, URL: "https://" + domain + "/", Port: 443, TimeoutMS: 5000,
		})
	}
	return targets, nil
}
