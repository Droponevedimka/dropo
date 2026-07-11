package dropocore

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const androidSingBoxVersion = "1.13.14"

var safeTagChars = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func BuildSingBoxConfig() string {
	mu.Lock()
	subscription := strings.TrimSpace(current.Subscription)
	enableLogging := current.Config.EnableLogging
	logLevel := effectiveAndroidLogLevel(enableLogging, current.Config.LogLevel)
	routingMode := strings.TrimSpace(current.Config.RoutingMode)
	hideRuTraffic := current.Config.HideRuTraffic
	ruProxyAddress := strings.TrimSpace(current.Config.RuProxyAddress)
	autoUpdateSub := current.Config.AutoUpdateSub
	routePolicies := androidRoutePoliciesLocked()
	cachedConfig := current.CachedSingBoxConfig
	cachedProxyCount := current.CachedProxyCount
	cachedSubscription := current.CachedConfigSubscription
	cachedSignature := current.CachedConfigSignature
	signature := androidConfigSignature(subscription, enableLogging, logLevel, routingMode, hideRuTraffic, ruProxyAddress, routePolicies)
	if !autoUpdateSub && cachedConfig != "" && subscription != "" && subscription == cachedSubscription && signature == cachedSignature {
		appendLogIfChangedLocked("android sing-box config cache reused (auto-update disabled)")
		_ = saveLocked()
		mu.Unlock()
		return encode(map[string]interface{}{
			"success":    true,
			"config":     cachedConfig,
			"proxyCount": cachedProxyCount,
			"version":    androidSingBoxVersion,
			"cached":     true,
		})
	}
	mu.Unlock()

	config, proxies, err := buildAndroidSingBoxConfig(subscription, logLevel, routingMode, hideRuTraffic, ruProxyAddress, routePolicies)

	mu.Lock()
	defer mu.Unlock()
	if err != nil {
		if cachedConfig != "" && subscription != "" && subscription == cachedSubscription && signature == cachedSignature {
			appendLogLocked("android sing-box config failed, using cached config: " + err.Error())
			current.Version.SingboxVersion = androidSingBoxVersion
			current.LastError = ""
			_ = saveLocked()
			return encode(map[string]interface{}{
				"success":    true,
				"config":     cachedConfig,
				"proxyCount": cachedProxyCount,
				"version":    androidSingBoxVersion,
				"cached":     true,
				"warning":    err.Error(),
			})
		}
		current.Connected = false
		current.StartedAt = ""
		current.LastError = err.Error()
		appendLogLocked("android sing-box config failed: " + err.Error())
		emitLocked("vpn-error", map[string]interface{}{"error": err.Error()})
		_ = saveLocked()
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}

	appendLogLocked(fmt.Sprintf("android sing-box config ready (%d proxy/proxies)", len(proxies)))
	current.Version.SingboxVersion = androidSingBoxVersion
	current.LastError = ""
	current.CachedSingBoxConfig = config
	current.CachedProxyCount = len(proxies)
	current.CachedConfigSubscription = subscription
	current.CachedConfigSignature = signature
	current.CachedConfigUpdatedAt = currentTimeRFC3339()
	_ = saveLocked()
	return encode(map[string]interface{}{
		"success":    true,
		"config":     config,
		"proxyCount": len(proxies),
		"version":    androidSingBoxVersion,
		"cached":     false,
	})
}

func currentTimeRFC3339() string {
	return time.Now().Format(time.RFC3339)
}

func buildAndroidSingBoxConfig(subscription, logLevel, routingMode string, hideRuTraffic bool, ruProxyAddress string, routePolicies map[string]string) (string, []proxyConfig, error) {
	if subscription == "" {
		return "", nil, fmt.Errorf("VPN subscription is empty")
	}
	if logLevel == "" {
		logLevel = "info"
	}

	filtered, err := parseAndroidProxyCandidates(subscription)
	if err != nil {
		return "", nil, err
	}

	outbounds, proxyTags := buildAndroidOutbounds(filtered)
	ruOutbound := "proxy"
	ruProxyAddress = strings.TrimSpace(ruProxyAddress)
	if hideRuTraffic && ruProxyAddress != "" {
		ruProxies, err := parseAndroidProxyCandidates(ruProxyAddress)
		if err != nil {
			return "", nil, fmt.Errorf("RU proxy address is invalid: %w", err)
		}
		outbounds, ruOutbound = appendAndroidProxyOutbounds(outbounds, ruProxies, "ru-proxy", "ru-auto-select")
	}
	dnsServers := buildAndroidDNSServers(proxyTags)
	dnsRules := buildAndroidDNSRules(routingMode, hideRuTraffic, routePolicies)
	finalOutbound := androidFinalOutbound(routingMode)
	finalDNSServer := androidFinalDNSServer(routingMode)
	config := map[string]interface{}{
		"log": map[string]interface{}{
			"level":     logLevel,
			"timestamp": true,
		},
		"dns": map[string]interface{}{
			"servers":           dnsServers,
			"rules":             dnsRules,
			"final":             finalDNSServer,
			"strategy":          "ipv4_only",
			"reverse_mapping":   true,
			"independent_cache": true,
		},
		"inbounds": []interface{}{
			map[string]interface{}{
				"type":         "tun",
				"tag":          "tun-in",
				"address":      []string{"172.19.0.1/30", "fdfe:dcba:9876::1/126"},
				"mtu":          1500,
				"auto_route":   true,
				"strict_route": true,
				"stack":        "mixed",
			},
		},
		"outbounds": outbounds,
		"route": map[string]interface{}{
			"rules":                   buildAndroidRouteRules(routingMode, hideRuTraffic, ruOutbound, routePolicies),
			"final":                   finalOutbound,
			"auto_detect_interface":   true,
			"default_domain_resolver": map[string]interface{}{"server": "dns-direct", "strategy": "ipv4_only"},
		},
		"experimental": map[string]interface{}{
			"cache_file": map[string]interface{}{"enabled": true, "path": "cache.db"},
		},
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return string(data), filtered, nil
}

func effectiveAndroidLogLevel(enableLogging bool, logLevel string) string {
	if !enableLogging {
		return "error"
	}
	switch strings.ToLower(strings.TrimSpace(logLevel)) {
	case "trace", "debug", "info", "warn", "error":
		return strings.ToLower(strings.TrimSpace(logLevel))
	default:
		return "info"
	}
}

func parseAndroidProxyCandidates(subscription string) ([]proxyConfig, error) {
	fetcher := newSubscriptionFetcher()
	var proxies []proxyConfig
	var err error
	if isDirectProxyLink(subscription) {
		proxy, parseErr := fetcher.parseSingleLink(subscription)
		err = parseErr
		proxies = []proxyConfig{proxy}
	} else {
		proxies, err = fetcher.fetchAndParse(subscription)
	}
	if err != nil {
		return nil, err
	}

	filtered := make([]proxyConfig, 0, len(proxies))
	for _, proxy := range proxies {
		proxy.Network = normalizeTransport(proxy.Network)
		if !isTransportSupported(proxy.Network) {
			continue
		}
		if proxy.Tag == "" {
			proxy.Tag = generateProxyTag(proxy, len(filtered))
		}
		filtered = append(filtered, proxy)
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("subscription does not contain supported Android sing-box proxies")
	}
	return filtered, nil
}

func buildAndroidOutbounds(proxies []proxyConfig) ([]interface{}, []string) {
	outbounds := make([]interface{}, 0, len(proxies)+3)
	proxyTags := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		outbounds = append(outbounds, proxyToSingBoxOutbound(proxy))
		proxyTags = append(proxyTags, proxy.Tag)
	}
	if len(proxyTags) == 1 {
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "proxy",
			"outbounds": proxyTags,
			"default":   proxyTags[0],
		})
	} else {
		outbounds = append(outbounds, map[string]interface{}{
			"type":                        "urltest",
			"tag":                         "auto-select",
			"outbounds":                   proxyTags,
			"url":                         "https://www.gstatic.com/generate_204",
			"interval":                    "5m",
			"tolerance":                   50,
			"interrupt_exist_connections": false,
		})
		selectorOutbounds := append([]string{"auto-select"}, proxyTags...)
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "proxy",
			"outbounds": selectorOutbounds,
			"default":   "auto-select",
		})
	}
	outbounds = append(outbounds, map[string]interface{}{"type": "direct", "tag": "direct"})
	return outbounds, proxyTags
}

func appendAndroidProxyOutbounds(outbounds []interface{}, proxies []proxyConfig, selectorTag, autoTag string) ([]interface{}, string) {
	proxyTags := make([]string, 0, len(proxies))
	for i, proxy := range proxies {
		proxy.Tag = fmt.Sprintf("%s-%d", selectorTag, i+1)
		outbounds = append(outbounds, proxyToSingBoxOutbound(proxy))
		proxyTags = append(proxyTags, proxy.Tag)
	}
	if len(proxyTags) == 0 {
		return outbounds, "proxy"
	}
	if len(proxyTags) == 1 {
		return outbounds, proxyTags[0]
	}
	outbounds = append(outbounds, map[string]interface{}{
		"type":                        "urltest",
		"tag":                         autoTag,
		"outbounds":                   proxyTags,
		"url":                         "https://www.gstatic.com/generate_204",
		"interval":                    "5m",
		"tolerance":                   50,
		"interrupt_exist_connections": false,
	})
	selectorOutbounds := append([]string{autoTag}, proxyTags...)
	outbounds = append(outbounds, map[string]interface{}{
		"type":      "selector",
		"tag":       selectorTag,
		"outbounds": selectorOutbounds,
		"default":   autoTag,
	})
	return outbounds, selectorTag
}

func proxyToSingBoxOutbound(proxy proxyConfig) map[string]interface{} {
	out := map[string]interface{}{
		"type":        proxy.Type,
		"tag":         proxy.Tag,
		"server":      proxy.Server,
		"server_port": proxy.ServerPort,
	}
	switch proxy.Type {
	case "vless":
		out["uuid"] = proxy.UUID
		if proxy.Flow != "" {
			out["flow"] = proxy.Flow
		}
		addTLS(out, proxy)
		addTransport(out, proxy)
	case "trojan":
		out["password"] = proxy.Password
		if proxy.Security == "" {
			proxy.Security = "tls"
		}
		addTLS(out, proxy)
		addTransport(out, proxy)
	case "shadowsocks":
		out["method"] = proxy.Method
		out["password"] = proxy.Password
	case "vmess":
		out["uuid"] = proxy.UUID
		out["security"] = "auto"
		addTLS(out, proxy)
		addTransport(out, proxy)
	case "hysteria2":
		out["password"] = proxy.Password
		addTLS(out, proxyConfig{
			Security: "tls",
			SNI:      firstNonEmpty(proxy.SNI, proxy.Server),
			ALPN:     proxy.ALPN,
		})
		if proxy.Obfs != "" && proxy.ObfsPassword != "" {
			out["obfs"] = map[string]interface{}{"type": proxy.Obfs, "password": proxy.ObfsPassword}
		}
		if proxy.UpMbps > 0 {
			out["up_mbps"] = proxy.UpMbps
		}
		if proxy.DownMbps > 0 {
			out["down_mbps"] = proxy.DownMbps
		}
	case "tuic":
		out["uuid"] = proxy.UUID
		out["password"] = proxy.Password
		out["congestion_control"] = firstNonEmpty(proxy.CongestionControl, "cubic")
		out["udp_relay_mode"] = firstNonEmpty(proxy.UDPRelayMode, "native")
		addTLS(out, proxyConfig{Security: "tls", SNI: firstNonEmpty(proxy.SNI, proxy.Server), ALPN: proxy.ALPN})
	}
	return out
}

func addTLS(out map[string]interface{}, proxy proxyConfig) {
	if proxy.Security != "tls" && proxy.Security != "reality" {
		return
	}
	tls := map[string]interface{}{"enabled": true}
	if proxy.SNI != "" {
		tls["server_name"] = proxy.SNI
	}
	if proxy.Fingerprint != "" && proxy.Security != "hysteria2" {
		tls["utls"] = map[string]interface{}{"enabled": true, "fingerprint": proxy.Fingerprint}
	}
	if alpn := splitCSV(proxy.ALPN); len(alpn) > 0 {
		tls["alpn"] = alpn
	}
	if proxy.Security == "reality" {
		reality := map[string]interface{}{"enabled": true, "public_key": proxy.PublicKey}
		if proxy.ShortID != "" {
			reality["short_id"] = proxy.ShortID
		}
		tls["reality"] = reality
	}
	out["tls"] = tls
}

func addTransport(out map[string]interface{}, proxy proxyConfig) {
	if proxy.Network == "" || proxy.Network == "tcp" {
		return
	}
	transport := map[string]interface{}{"type": proxy.Network}
	switch proxy.Network {
	case "ws":
		if proxy.Path != "" {
			transport["path"] = proxy.Path
		}
		if proxy.Host != "" {
			transport["headers"] = map[string]interface{}{"Host": proxy.Host}
		}
	case "grpc":
		if proxy.Path != "" {
			transport["service_name"] = strings.TrimPrefix(proxy.Path, "/")
		}
	case "http":
		if proxy.Path != "" {
			transport["path"] = proxy.Path
		}
		if proxy.Host != "" {
			transport["host"] = []string{proxy.Host}
		}
	case "httpupgrade":
		if proxy.Path != "" {
			transport["path"] = proxy.Path
		}
		if proxy.Host != "" {
			transport["host"] = proxy.Host
		}
	}
	out["transport"] = transport
}

func buildAndroidDNSServers(proxyTags []string) []interface{} {
	servers := []interface{}{
		map[string]interface{}{"type": "local", "tag": "dns-local"},
		map[string]interface{}{
			"type":        "https",
			"tag":         "dns-direct",
			"server":      "8.8.4.4",
			"server_port": 443,
			"path":        "/dns-query",
			"tls":         map[string]interface{}{"server_name": "dns.google"},
		},
	}
	remote := map[string]interface{}{
		"type":        "https",
		"tag":         "dns-remote",
		"server":      "8.8.8.8",
		"server_port": 443,
		"path":        "/dns-query",
		"tls":         map[string]interface{}{"server_name": "dns.google"},
	}
	if len(proxyTags) > 0 {
		remote["detour"] = "proxy"
	}
	servers = append(servers, remote)
	return servers
}

func buildAndroidDNSRules(routingMode string, hideRuTraffic bool, routePolicies map[string]string) []interface{} {
	rules := []interface{}{
		map[string]interface{}{
			"domain_suffix": []string{"local", "internal", "corp", "lan", "home", "intranet", "private"},
			"action":        "route",
			"server":        "dns-local",
		},
	}
	if directDomains := androidServiceDomainSuffixesByPolicy(routePolicies, androidRoutePolicyDirect); len(directDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_suffix": directDomains,
			"action":        "route",
			"server":        "dns-direct",
		})
	}
	if vpnDomains := androidServiceDomainSuffixesByPolicy(routePolicies, androidRoutePolicyVPN); len(vpnDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_suffix": vpnDomains,
			"action":        "route",
			"server":        "dns-remote",
		})
	}
	if routingMode != "all_traffic" {
		ruServer := "dns-direct"
		if hideRuTraffic {
			ruServer = "dns-remote"
		}
		rules = append(rules,
			map[string]interface{}{"domain_suffix": androidDirectDomainSuffixes(), "action": "route", "server": ruServer},
			map[string]interface{}{"domain_keyword": androidDirectDomainKeywords(), "action": "route", "server": ruServer},
		)
	}
	return rules
}

func buildAndroidRouteRules(routingMode string, hideRuTraffic bool, ruOutbound string, routePolicies map[string]string) []interface{} {
	rules := []interface{}{
		map[string]interface{}{"action": "sniff"},
		map[string]interface{}{"protocol": "dns", "action": "hijack-dns"},
		map[string]interface{}{"ip_is_private": true, "action": "route", "outbound": "direct"},
		map[string]interface{}{
			"domain_suffix": []string{"local", "internal", "corp", "lan", "home", "intranet", "private"},
			"action":        "route",
			"outbound":      "direct",
		},
	}
	if directDomains := androidServiceDomainSuffixesByPolicy(routePolicies, androidRoutePolicyDirect); len(directDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_suffix": directDomains,
			"action":        "route",
			"outbound":      "direct",
		})
	}
	if directPackages := androidServicePackageNamesByPolicy(routePolicies, androidRoutePolicyDirect); len(directPackages) > 0 {
		rules = append(rules, map[string]interface{}{
			"package_name": directPackages,
			"action":       "route",
			"outbound":     "direct",
		})
	}
	if directIPs := androidServiceIPCIDRsByPolicy(routePolicies, androidRoutePolicyDirect); len(directIPs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr":  directIPs,
			"action":   "route",
			"outbound": "direct",
		})
	}
	if vpnDomains := androidServiceDomainSuffixesByPolicy(routePolicies, androidRoutePolicyVPN); len(vpnDomains) > 0 {
		rules = append(rules, map[string]interface{}{
			"domain_suffix": vpnDomains,
			"action":        "route",
			"outbound":      "proxy",
		})
	}
	if vpnPackages := androidServicePackageNamesByPolicy(routePolicies, androidRoutePolicyVPN); len(vpnPackages) > 0 {
		rules = append(rules, map[string]interface{}{
			"package_name": vpnPackages,
			"action":       "route",
			"outbound":     "proxy",
		})
	}
	if vpnIPs := androidServiceIPCIDRsByPolicy(routePolicies, androidRoutePolicyVPN); len(vpnIPs) > 0 {
		rules = append(rules, map[string]interface{}{
			"ip_cidr":  vpnIPs,
			"action":   "route",
			"outbound": "proxy",
		})
	}
	if routingMode != "all_traffic" {
		ruRouteOutbound := "direct"
		if hideRuTraffic {
			ruRouteOutbound = ruOutbound
		}
		rules = append(rules,
			map[string]interface{}{"domain_suffix": androidDirectDomainSuffixes(), "action": "route", "outbound": ruRouteOutbound},
			map[string]interface{}{"domain_keyword": androidDirectDomainKeywords(), "action": "route", "outbound": ruRouteOutbound},
		)
	}
	return rules
}

func androidFinalOutbound(routingMode string) string {
	switch routingMode {
	case "all_traffic", "except_russia":
		return "proxy"
	default:
		return "direct"
	}
}

func androidFinalDNSServer(routingMode string) string {
	if androidFinalOutbound(routingMode) == "proxy" {
		return "dns-remote"
	}
	return "dns-direct"
}

func androidDirectDomainSuffixes() []string {
	return []string{
		"ru", "su", "xn--p1ai",
		"yandex.com", "yandex.net", "yandex.ru", "ya.ru",
		"google.com", "google.ru", "gstatic.com", "googleusercontent.com",
		"mail.ru", "vk.com", "vkontakte.ru", "ok.ru",
		"sberbank.ru", "sber.ru", "tinkoff.ru", "vtb.ru",
		"gazprom.ru", "mos.ru", "gosuslugi.ru", "nalog.ru",
		"government.ru", "kremlin.ru", "duma.gov.ru", "cbr.ru",
		"ria.ru", "rbc.ru", "interfax.ru", "tass.ru", "kommersant.ru",
		"lenta.ru", "gazeta.ru", "kp.ru", "mk.ru",
		"rutube.ru", "ivi.ru", "okko.tv", "more.tv", "kinopoisk.ru",
		"dzen.ru", "2gis.ru", "avito.ru", "ozon.ru", "wildberries.ru",
		"mts.ru", "megafon.ru", "beeline.ru", "tele2.ru", "rostelecom.ru",
	}
}

func androidDirectDomainKeywords() []string {
	return []string{"yandex", "sber", "tinkoff", "gosuslugi", "rutube", "vkontakte", "mailru", "rambler", "wildberries", "ozon"}
}

func androidConfigSignature(subscription string, enableLogging bool, logLevel, routingMode string, hideRuTraffic bool, ruProxyAddress string, routePolicies map[string]string) string {
	parts := []string{
		"singbox=" + androidSingBoxVersion,
		"schema=android-package-routing-v4",
		"subscription=" + strings.TrimSpace(subscription),
		"log=" + effectiveAndroidLogLevel(enableLogging, logLevel),
		"routing=" + normalizeAndroidRoutingMode(routingMode),
		fmt.Sprintf("hideRu=%v", hideRuTraffic),
		"ruProxy=" + strings.TrimSpace(ruProxyAddress),
	}
	keys := make([]string, 0, len(routePolicies))
	for key := range routePolicies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, "route."+key+"="+normalizeAndroidRoutePolicy(routePolicies[key]))
	}
	return strings.Join(parts, "\n")
}

func generateProxyTag(proxy proxyConfig, index int) string {
	base := proxy.Name
	if base == "" {
		base = proxy.Server
	}
	base = strings.Trim(safeTagChars.ReplaceAllString(base, "-"), "-")
	if base == "" {
		base = proxy.Type
	}
	return fmt.Sprintf("%s-%d", strings.ToLower(base), index+1)
}

func splitCSV(value string) []string {
	var result []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
