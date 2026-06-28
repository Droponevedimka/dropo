package routing

import (
	"net"
	"strings"
)

type Route string

const (
	RouteDirect            Route = "direct"
	RouteFreeBypass        Route = "free_bypass"
	RouteVPNForced         Route = "vpn_forced"
	RouteVPNFallback       Route = "vpn_fallback"
	RouteBlockedWithoutVPN Route = "blocked_without_vpn"
)

type Rule struct {
	Tag         string   `json:"tag"`
	Label       string   `json:"label"`
	Route       Route    `json:"route"`
	Domains     []string `json:"domains"`
	Processes   []string `json:"processes,omitempty"`
	Description string   `json:"description,omitempty"`
}

type Decision struct {
	Host        string `json:"host"`
	Process     string `json:"process,omitempty"`
	Route       Route  `json:"route"`
	ServiceTag  string `json:"serviceTag,omitempty"`
	ServiceName string `json:"serviceName,omitempty"`
	Reason      string `json:"reason"`
}

type Policy struct {
	Rules []Rule
}

func DefaultPolicy() Policy {
	return Policy{Rules: []Rule{
		{
			Tag:   "openai",
			Label: "AI/dev services",
			Route: RouteVPNForced,
			Domains: []string{
				"chatgpt.com",
				"openai.com",
				"api.openai.com",
				"oaistatic.com",
				"oaiusercontent.com",
				"claude.ai",
				"anthropic.com",
				"api.anthropic.com",
				"githubcopilot.com",
				"api.githubcopilot.com",
				"copilot-proxy.githubusercontent.com",
				"cursor.com",
				"cursor.sh",
				"anysphere.co",
				"perplexity.ai",
				"gemini.google.com",
				"generativelanguage.googleapis.com",
				"grok.com",
				"x.ai",
				"meta.ai",
			},
			Processes:   []string{"chatgpt.exe", "claude.exe", "cursor.exe", "code.exe"},
			Description: "Remote-side RU restrictions require a VPN/subscription path.",
		},
		{
			Tag:         "youtube",
			Label:       "YouTube",
			Route:       RouteFreeBypass,
			Domains:     []string{"youtube.com", "youtu.be", "googlevideo.com", "ytimg.com", "youtubei.googleapis.com"},
			Description: "YouTube is service-specific and must not inherit generic Google direct routing.",
		},
		{
			Tag:         "discord",
			Label:       "Discord",
			Route:       RouteFreeBypass,
			Domains:     []string{"discord.com", "discord.gg", "discordapp.com", "discord.media"},
			Description: "DPI-blocked service: try free bypass before VPN fallback.",
		},
		{
			Tag:         "telegram",
			Label:       "Telegram",
			Route:       RouteVPNFallback,
			Domains:     []string{"telegram.org", "t.me", "telegram.me"},
			Description: "Protocol/IP blocking usually needs a proxy or VPN fallback.",
		},
		{
			Tag:         "google-direct",
			Label:       "Google direct",
			Route:       RouteDirect,
			Domains:     []string{"google.com", "gstatic.com", "googleapis.com", "googleusercontent.com", "google.ru"},
			Description: "Generic Google traffic stays direct unless a specific blocked service matched earlier.",
		},
		{
			Tag:   "ru-direct",
			Label: "RU direct",
			Route: RouteDirect,
			Domains: []string{
				"gosuslugi.ru",
				"yandex.ru",
				"yandex.com",
				"vk.com",
				"vk.ru",
				"ozon.ru",
				"sberbank.ru",
				"sber.ru",
				"mail.ru",
				"rambler.ru",
			},
			Description: "Russian public and local services stay direct.",
		},
	}}
}

func (p Policy) Decide(host, process string, subscriptionAvailable bool) Decision {
	host = normalizeHost(host)
	process = strings.ToLower(strings.TrimSpace(process))
	if host == "" && process == "" {
		return Decision{Route: RouteDirect, Reason: "empty target defaults to direct"}
	}

	for _, rule := range p.Rules {
		if ruleMatches(rule, host, process) {
			route := rule.Route
			if route == RouteVPNForced && !subscriptionAvailable {
				route = RouteBlockedWithoutVPN
			}
			return Decision{
				Host:        host,
				Process:     process,
				Route:       route,
				ServiceTag:  rule.Tag,
				ServiceName: rule.Label,
				Reason:      rule.Description,
			}
		}
	}

	if strings.HasSuffix(host, ".ru") || strings.HasSuffix(host, ".xn--p1ai") || isPrivateOrLocalIP(host) {
		return Decision{Host: host, Process: process, Route: RouteDirect, ServiceTag: "ru-direct", ServiceName: "RU direct", Reason: "RU/local target defaults to direct"}
	}

	return Decision{Host: host, Process: process, Route: RouteDirect, Reason: "normal unclassified traffic defaults to direct"}
}

func ruleMatches(rule Rule, host, process string) bool {
	for _, domain := range rule.Domains {
		if domainMatches(host, domain) {
			return true
		}
	}
	for _, candidate := range rule.Processes {
		if process != "" && strings.EqualFold(process, candidate) {
			return true
		}
	}
	return false
}

func normalizeHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	if slash := strings.IndexByte(host, '/'); slash >= 0 {
		host = host[:slash]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, ".")
}

func domainMatches(host, domain string) bool {
	domain = strings.Trim(strings.ToLower(domain), ".")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func isPrivateOrLocalIP(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
