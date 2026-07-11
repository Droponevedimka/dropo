package main

import "strings"

var UnsupportedTransports = []string{
	"kcp",
	"mkcp",
}

type ProxySplitResult struct {
	SingBox     []ProxyConfig
	XrayBridge  []ProxyConfig
	Filtered    []ProxyConfig
	Message     string
	AllFiltered bool
}

type FilterResult struct {
	Supported   []ProxyConfig
	Filtered    []ProxyConfig
	Message     string
	AllFiltered bool
}

func NormalizeTransport(transport string) string {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "splithttp" {
		return "xhttp"
	}
	if transport == "" {
		return "tcp"
	}
	return transport
}

func RequiresXrayBridge(proxy ProxyConfig) bool {
	return proxy.Type == "vless" && NormalizeTransport(proxy.Network) == "xhttp"
}

func IsTransportSupported(transport string) bool {
	transport = NormalizeTransport(transport)
	if transport == "xhttp" {
		return false
	}
	for _, unsupported := range UnsupportedTransports {
		if transport == unsupported {
			return false
		}
	}
	return true
}

func SplitProxyConfigs(proxies []ProxyConfig) ProxySplitResult {
	result := ProxySplitResult{
		SingBox:    make([]ProxyConfig, 0, len(proxies)),
		XrayBridge: make([]ProxyConfig, 0),
		Filtered:   make([]ProxyConfig, 0),
	}

	filteredInfo := []string{}
	for _, proxy := range proxies {
		proxy.Network = NormalizeTransport(proxy.Network)
		if RequiresXrayBridge(proxy) {
			result.XrayBridge = append(result.XrayBridge, proxy)
			continue
		}
		if IsTransportSupported(proxy.Network) {
			result.SingBox = append(result.SingBox, proxy)
			continue
		}

		result.Filtered = append(result.Filtered, proxy)
		info := proxy.Name
		if info == "" {
			info = proxy.Server
		}
		filteredInfo = append(filteredInfo, info+" (транспорт: "+proxy.Network+")")
	}

	supportedCount := len(result.SingBox) + len(result.XrayBridge)
	result.AllFiltered = supportedCount == 0 && len(result.Filtered) > 0
	if len(result.Filtered) > 0 {
		if result.AllFiltered {
			result.Message = "Все серверы в подписке используют неподдерживаемый транспорт. Поддерживаются sing-box транспорты и VLESS xhttp через Xray-core."
		} else {
			result.Message = "Некоторые серверы (" + joinStrings(filteredInfo, ", ") + ") используют неподдерживаемый транспорт и были пропущены."
		}
	}

	return result
}

func FilterUnsupportedTransports(proxies []ProxyConfig) FilterResult {
	split := SplitProxyConfigs(proxies)
	supported := append([]ProxyConfig{}, split.SingBox...)
	supported = append(supported, split.XrayBridge...)
	return FilterResult{
		Supported:   supported,
		Filtered:    split.Filtered,
		Message:     split.Message,
		AllFiltered: split.AllFiltered,
	}
}

func joinStrings(strs []string, sep string) string {
	if len(strs) == 0 {
		return ""
	}
	result := strs[0]
	for i := 1; i < len(strs); i++ {
		result += sep + strs[i]
	}
	return result
}
