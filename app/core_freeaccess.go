package main

import (
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type FreeAccessService struct {
	Tag            string   `json:"tag"`
	DisplayName    string   `json:"display_name"`
	DomainSuffixes []string `json:"domain_suffixes"`
	IPCIDRs        []string `json:"ip_cidrs,omitempty"`
	ProcessNames   []string `json:"process_names,omitempty"`
	HealthURL      string   `json:"health_url,omitempty"`
	ProbeURLs      []string `json:"probe_urls,omitempty"`
	RequiresVPN    bool     `json:"requires_vpn,omitempty"`
}

var DefaultFreeAccessServices = []FreeAccessService{
	{
		Tag:         "discord",
		DisplayName: "Discord",
		DomainSuffixes: []string{
			"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discord.gift",
			"discordcdn.com", "discordstatus.com",
		},
		IPCIDRs: []string{
			"66.22.192.0/18",
		},
		ProcessNames: []string{"Discord.exe", "DiscordCanary.exe", "DiscordPTB.exe"},
		HealthURL:    "https://discord.com/api/v10/gateway",
		ProbeURLs: []string{
			"https://discord.com",
			"https://cdn.discordapp.com",
			"https://media.discordapp.net",
		},
	},
	{
		Tag:         "youtube",
		DisplayName: "YouTube",
		DomainSuffixes: []string{
			"youtube.com", "youtu.be", "youtube-nocookie.com", "youtubeeducation.com",
			"ytimg.com", "yt3.ggpht.com", "ggpht.com", "googlevideo.com",
			"youtube-ui.l.google.com", "wide-youtube.l.google.com",
			"youtubei.googleapis.com", "youtube.googleapis.com",
			"ytstatic.com", "www.gstatic.com", "gvt1.com", "gvt2.com",
		},
		HealthURL: "https://www.youtube.com",
		ProbeURLs: []string{
			"https://www.youtube.com/generate_204",
			"https://youtubei.googleapis.com",
			"https://redirector.googlevideo.com",
			"https://i.ytimg.com/generate_204",
			"https://yt3.ggpht.com",
		},
	},
	{
		Tag:         "meta",
		DisplayName: "Instagram / Facebook",
		DomainSuffixes: []string{
			"instagram.com", "cdninstagram.com", "facebook.com", "fbcdn.net", "fb.com", "facebook.net",
			"messenger.com", "m.me", "threads.net", "connect.facebook.net",
		},
		IPCIDRs: []string{
			"31.13.64.0/18",
			"66.220.144.0/20",
			"69.63.176.0/20",
			"69.171.224.0/19",
			"129.134.0.0/16",
			"157.240.0.0/16",
			"173.252.64.0/18",
			"185.60.216.0/22",
		},
		HealthURL: "https://www.instagram.com",
		ProbeURLs: []string{
			"https://www.facebook.com",
			"https://connect.facebook.net",
		},
	},
	{
		Tag:            "twitter",
		DisplayName:    "X (Twitter)",
		DomainSuffixes: []string{"twitter.com", "x.com", "twimg.com", "t.co", "ads-twitter.com"},
		HealthURL:      "https://x.com",
		ProbeURLs:      []string{"https://abs.twimg.com"},
	},
	{
		Tag:            "linkedin",
		DisplayName:    "LinkedIn",
		DomainSuffixes: []string{"linkedin.com", "licdn.com", "lnkd.in", "linkedin.cn"},
		HealthURL:      "https://www.linkedin.com",
		ProbeURLs:      []string{"https://static.licdn.com"},
	},
	{
		Tag:            "signal",
		DisplayName:    "Signal",
		DomainSuffixes: []string{"signal.org", "signal.me", "whispersystems.org", "signal.art"},
		ProcessNames:   []string{"Signal.exe"},
		HealthURL:      "https://signal.org",
		ProbeURLs:      []string{"https://updates.signal.org"},
	},
	{
		Tag:         "telegram",
		DisplayName: "Telegram",
		DomainSuffixes: []string{
			"telegram.org", "telegram.me", "t.me", "telegra.ph", "telesco.pe", "tdesktop.com",
			"telegram-cdn.org",
		},
		IPCIDRs: []string{
			"149.154.160.0/20",
			"91.105.192.0/23",
			"91.108.4.0/22",
			"91.108.8.0/22",
			"91.108.12.0/22",
			"91.108.16.0/22",
			"91.108.20.0/22",
			"91.108.56.0/22",
			"185.76.151.0/24",
			"2001:b28:f23d::/48",
			"2001:b28:f23f::/48",
			"2001:67c:4e8::/48",
			"2001:b28:f23c::/48",
			"2a0a:f280::/32",
		},
		ProcessNames: []string{"Telegram.exe"},
		HealthURL:    "https://telegram.org",
		ProbeURLs: []string{
			"https://web.telegram.org",
			"https://t.me",
		},
	},
	{
		Tag:         "whatsapp",
		DisplayName: "WhatsApp",
		DomainSuffixes: []string{
			"whatsapp.com", "whatsapp.net", "wa.me", "whatsappbrand.com",
			"cdn.whatsapp.net", "static.whatsapp.net", "scontent.whatsapp.net",
			"graph.whatsapp.com", "wa.meta.vc",
		},
		ProcessNames: []string{"WhatsApp.exe", "WhatsAppBeta.exe"},
		HealthURL:    "https://web.whatsapp.com",
		ProbeURLs: []string{
			"https://www.whatsapp.com",
			"https://static.whatsapp.net",
			"https://graph.whatsapp.com",
		},
	},
	{
		Tag:         "facetime",
		DisplayName: "FaceTime / iMessage",
		DomainSuffixes: []string{
			"facetime.apple.com", "ess.apple.com", "identity.apple.com",
			"push.apple.com", "init.itunes.apple.com",
		},
		HealthURL: "https://facetime.apple.com",
		ProbeURLs: []string{
			"https://identity.apple.com",
			"https://init.itunes.apple.com",
		},
	},
	{
		Tag:            "viber",
		DisplayName:    "Viber",
		DomainSuffixes: []string{"viber.com", "vb.me", "viberdns.com", "viber.co", "viberapp.com"},
		ProcessNames:   []string{"Viber.exe"},
		HealthURL:      "https://www.viber.com",
		ProbeURLs:      []string{"https://account.viber.com"},
	},
	{
		Tag:         "snapchat",
		DisplayName: "Snapchat",
		DomainSuffixes: []string{
			"snapchat.com", "sc-gw.com", "sc-cdn.net", "snapkit.com", "sc-static.net",
			"sc-prod.net", "sc-jpl.com", "sc-corp.net", "snapads.com", "snap.com",
			"addlive.io", "feelinsonice.com", "snapmap.com", "snapmap.org", "snapmaps.com",
		},
		ProcessNames: []string{"Snapchat.exe"},
		HealthURL:    "https://www.snapchat.com",
		ProbeURLs: []string{
			"https://accounts.snapchat.com",
			"https://app.snapchat.com",
		},
	},
	{
		Tag:            "twitch",
		DisplayName:    "Twitch",
		DomainSuffixes: []string{"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "ext-twitch.tv"},
		HealthURL:      "https://www.twitch.tv",
		ProbeURLs: []string{
			"https://static.twitchcdn.net",
			"https://usher.ttvnw.net",
		},
	},
	{
		Tag:            "spotify",
		DisplayName:    "Spotify",
		DomainSuffixes: []string{"spotify.com", "scdn.co", "spotifycdn.com", "spoti.fi"},
		ProcessNames:   []string{"Spotify.exe"},
		HealthURL:      "https://open.spotify.com",
		ProbeURLs: []string{
			"https://api.spotify.com",
			"https://i.scdn.co",
		},
	},
	{
		Tag:         "tiktok",
		DisplayName: "TikTok",
		DomainSuffixes: []string{
			"tiktok.com", "tiktokcdn.com", "tiktokcdn-us.com", "tiktokv.com", "tiktokv.us",
			"tiktokw.us", "ttdns2.com", "byteoversea.com", "ibyteimg.com", "ibytedtos.com",
			"ttwstatic.com",
		},
		ProcessNames: []string{"TikTok.exe"},
		HealthURL:    "https://www.tiktok.com",
		ProbeURLs: []string{
			"https://www.tiktok.com/api/recommend/item_list/",
			"https://lf16-tiktok-web.ttwstatic.com",
		},
	},
	{
		Tag:         "canva",
		DisplayName: "Canva",
		DomainSuffixes: []string{
			"canva.com", "canva.site", "canva.design", "canva.me", "canva-apps.com",
		},
		HealthURL:   "https://www.canva.com",
		RequiresVPN: true,
	},
	{
		Tag:         "notion",
		DisplayName: "Notion",
		DomainSuffixes: []string{
			"notion.com", "notion.so", "notion.site", "notion-static.com", "notionusercontent.com",
			"app.notion.com", "api.notion.com", "img.notionusercontent.com", "secure.notion-static.com",
		},
		IPCIDRs: []string{
			"131.149.232.0/21",
			"208.103.161.0/24",
			"2602:F79A::/36",
		},
		ProcessNames: []string{"Notion.exe"},
		HealthURL:    "https://www.notion.com",
		RequiresVPN:  true,
	},
	{
		Tag:         "slack",
		DisplayName: "Slack",
		DomainSuffixes: []string{
			"slack.com", "slackb.com", "slack-edge.com", "slack-files.com", "slack-imgs.com",
			"slack-msgs.com", "slack-core.com", "slack-redir.net",
		},
		ProcessNames: []string{"Slack.exe"},
		HealthURL:    "https://slack.com",
		RequiresVPN:  true,
	},
	{
		Tag:         "miro",
		DisplayName: "Miro",
		DomainSuffixes: []string{
			"miro.com", "miro-apps.com", "mirostatic.com", "realtimeboard.com",
			"onlinewhiteboard.com", "webwhiteboard.com",
		},
		HealthURL:   "https://miro.com",
		RequiresVPN: true,
	},
	{
		Tag:         "wix",
		DisplayName: "Wix",
		DomainSuffixes: []string{
			"wix.com", "wixsite.com", "wixstatic.com", "wixmp.com", "wixapps.net",
			"editorx.com", "parastorage.com",
		},
		HealthURL:   "https://www.wix.com",
		RequiresVPN: true,
	},
	{
		Tag:         "coda",
		DisplayName: "Coda",
		DomainSuffixes: []string{
			"coda.io", "codahosted.io", "codacontent.io",
		},
		HealthURL:   "https://coda.io",
		RequiresVPN: true,
	},
	{
		Tag:         "grammarly",
		DisplayName: "Grammarly",
		DomainSuffixes: []string{
			"grammarly.com", "grammarly.io", "grammarly.net", "grammarlyaws.com",
		},
		ProcessNames: []string{"Grammarly.exe", "Grammarly Desktop.exe", "Grammarly for Windows.exe"},
		HealthURL:    "https://www.grammarly.com",
		RequiresVPN:  true,
	},
	{
		Tag:         "docker",
		DisplayName: "Docker Hub",
		DomainSuffixes: []string{
			"docker.com", "docker.io", "dockerhub.com", "login.docker.com", "auth.docker.com",
			"registry-1.docker.io", "auth.docker.io", "desktop.docker.com", "hub.docker.com",
			"production.cloudfront.docker.com", "production.cloudflare.docker.com",
			"docker-pinata-support.s3.amazonaws.com", "api.docker.com", "api.dso.docker.com",
			"dhi.io", "registry.scout.docker.com",
		},
		ProcessNames: []string{"Docker Desktop.exe", "com.docker.backend.exe", "docker.exe"},
		HealthURL:    "https://hub.docker.com",
		ProbeURLs:    []string{"https://registry-1.docker.io", "https://auth.docker.io"},
		RequiresVPN:  true,
	},
	{
		Tag:         "clickup",
		DisplayName: "ClickUp",
		DomainSuffixes: []string{
			"clickup.com", "clickup-au.com", "clickup-attachments.com", "clickup-prod.com",
			"clickup-eu.com", "clickup-sg.com", "clickup.ada.support", "codox.io",
		},
		ProcessNames: []string{"ClickUp.exe"},
		HealthURL:    "https://app.clickup.com",
		ProbeURLs:    []string{"https://api.clickup.com", "https://attachments.clickup.com"},
		RequiresVPN:  true,
	},
	{
		Tag:         "manychat",
		DisplayName: "Manychat",
		DomainSuffixes: []string{
			"manychat.com",
		},
		HealthURL:   "https://app.manychat.com",
		ProbeURLs:   []string{"https://api.manychat.com"},
		RequiresVPN: true,
	},
	{
		Tag:         "helpscout",
		DisplayName: "Help Scout",
		DomainSuffixes: []string{
			"helpscout.com", "helpscout.net", "helpscoutdocs.com",
		},
		HealthURL:   "https://secure.helpscout.net",
		RequiresVPN: true,
	},
	{
		Tag:         "atlassian",
		DisplayName: "Atlassian / Trello",
		DomainSuffixes: []string{
			"atlassian.com", "atlassian.net", "atlassian.io", "atlassianstatus.com",
			"atlassianusercontent.com", "jira.com", "trello.com", "trello.services",
			"trellocdn.com", "bitbucket.org", "bitbucket.io", "bitbucketusercontent.com",
			"statuspage.io", "opsgenie.com",
		},
		ProcessNames: []string{"Trello.exe"},
		HealthURL:    "https://trello.com",
		ProbeURLs:    []string{"https://id.atlassian.com", "https://bitbucket.org"},
		RequiresVPN:  true,
	},
	{
		Tag:         "openai",
		DisplayName: "AI services",
		DomainSuffixes: []string{
			"openai.com", "chatgpt.com", "ws.chatgpt.com", "oaistatic.com", "oaiusercontent.com", "oaistatsig.com",
			"openaimerge.com", "auth0.openai.com", "workos.com", "workoscdn.com",
			"githubcopilot.com", "copilot-proxy.githubusercontent.com", "origin-tracker.githubusercontent.com",
			"copilot-telemetry.githubusercontent.com", "default.exp-tas.com", "api.github.com", "github.com",
			"cursor.com", "cursor.sh", "api2.cursor.sh", "api3.cursor.sh", "repo42.cursor.sh",
			"cursorapi.com", "cursor-cdn.com", "download.todesktop.com",
			"copilot.microsoft.com", "perplexity.ai", "api.perplexity.ai", "poe.com",
			"gemini.google.com", "aistudio.google.com", "ai.google.dev", "generativelanguage.googleapis.com",
			"notebooklm.google.com", "grok.com", "x.ai", "api.x.ai", "console.x.ai",
			"meta.ai", "ai.meta.com", "llama.com",
		},
		ProcessNames: []string{
			"Code.exe", "Cursor.exe", "GitHubCopilot.exe",
		},
		HealthURL:   "https://chatgpt.com",
		ProbeURLs:   []string{"https://api.openai.com"},
		RequiresVPN: true,
	},
}

func (s FreeAccessService) ProbeTargets() []string {
	targets := make([]string, 0, 1+len(s.ProbeURLs))
	if strings.TrimSpace(s.HealthURL) != "" {
		targets = append(targets, strings.TrimSpace(s.HealthURL))
	}
	for _, target := range s.ProbeURLs {
		target = strings.TrimSpace(target)
		if target != "" {
			targets = append(targets, target)
		}
	}
	return uniqueStrings(targets)
}

type ByeDPIStrategy struct {
	Tag   string
	Label string
	Port  int
	Args  []string
}

type TransparentFreeAccessStrategy struct {
	Tag           string
	Label         string
	ExeName       string
	Args          []string
	Platforms     []string
	ManualScope   bool
	RequiredFiles []string
}

const (
	FreeAccessMethodAuto   = "auto"
	FreeAccessMethodDirect = "direct"
	FreeAccessMethodVPN    = "vpn"

	ByeDPIProcessName   = "ciadpi.exe"
	ByeDPIOutboundTag   = "byedpi"
	ZapretProcessName   = "winws2.exe"
	RuProxyOutboundTag  = "ru-proxy"
	SmartBypassGroupTag = "smart-bypass"
	VpnOrDirectGroupTag = "vpn-or-direct"
	RuRouteGroupTag     = "ru-route"
	NoRouteOutboundTag  = "dropo-block"

	ByeDPILocalPort = 18091

	byeDPIHealthCheckInterval = 30 * time.Second
	byeDPIMaxRestartAttempts  = 3
	byeDPIRestartBackoff      = 5 * time.Second
	byeDPIStartupWait         = 8 * time.Second
	freeProxyStartupWait      = 8 * time.Second
	// transparentStartupWait gives winws2 + WinDivert time to actually attach its
	// packet filter and start desyncing before we probe through it. 800ms was
	// too short: probes ran before the filter was effective, reported false
	// failures, and triggered needless strategy churn. QUIC in particular needs
	// more settle time.
	transparentStartupWait = 2500 * time.Millisecond
)

var DefaultByeDPIStrategies = []ByeDPIStrategy{
	{
		Tag:   ByeDPIOutboundTag,
		Label: "ByeDPI auto",
		Port:  ByeDPILocalPort,
		Args:  []string{"--split", "1", "--disorder", "3+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"},
	},
	{
		Tag:   "byedpi-sni",
		Label: "ByeDPI SNI split",
		Port:  18092,
		Args:  []string{"--split", "1+s", "--disorder", "3+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"},
	},
	{
		Tag:   "byedpi-oob",
		Label: "ByeDPI OOB",
		Port:  18093,
		Args:  []string{"--oob", "1+s", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"},
	},
	{
		Tag:   "byedpi-fake",
		Label: "ByeDPI fake packet",
		Port:  18094,
		Args:  []string{"--fake", "1+s", "--ttl", "5", "--mod-http=h,d", "--auto=torst", "--tlsrec", "1+s"},
	},
}

// DefaultZapretTransparentStrategies uses zapret2's Lua strategy API and
// outbound-only WinDivert filters. Tags stay stable so existing client
// selections migrate without resetting user preferences.
var DefaultZapretTransparentStrategies = defaultZapret2TransparentStrategies()

func FreeAccessServiceTags() []string {
	tags := make([]string, len(DefaultFreeAccessServices))
	for i, s := range DefaultFreeAccessServices {
		tags[i] = s.Tag
	}
	return tags
}

func DefaultFreeAccessServiceState() map[string]bool {
	state := make(map[string]bool, len(DefaultFreeAccessServices))
	for _, s := range DefaultFreeAccessServices {
		state[s.Tag] = true
	}
	return state
}

func DefaultFreeAccessServiceMethodState() map[string]string {
	state := make(map[string]string, len(DefaultFreeAccessServices))
	for _, s := range DefaultFreeAccessServices {
		state[s.Tag] = FreeAccessMethodAuto
	}
	return state
}

func FreeAccessMethodTags() []string {
	tags := make([]string, 0, len(DefaultByeDPIStrategies))
	for _, strategy := range DefaultByeDPIStrategies {
		tags = append(tags, strategy.Tag)
	}
	return tags
}

func FreeAccessTransparentMethodTags() []string {
	tags := make([]string, 0, len(DefaultZapretTransparentStrategies))
	for _, strategy := range DefaultZapretTransparentStrategies {
		if methodSupportsCurrentPlatform(strategy.Platforms) {
			tags = append(tags, strategy.Tag)
		}
	}
	return tags
}

func methodSupportsCurrentPlatform(platforms []string) bool {
	if len(platforms) == 0 {
		return true
	}
	for _, platform := range platforms {
		if platform == runtime.GOOS {
			return true
		}
	}
	return false
}

func FreeMethodsAllowed(settings GlobalAppSettings) bool {
	return !settings.DisableFreeAccess
}

func FreeAccessServiceEnabled(settings GlobalAppSettings, serviceTag string) bool {
	return FreeMethodsAllowed(settings)
}

func NormalizeFreeAccessServiceMethod(method string) string {
	method = strings.TrimSpace(strings.ToLower(method))
	switch method {
	case "", FreeAccessMethodAuto:
		return FreeAccessMethodAuto
	case FreeAccessMethodDirect:
		return FreeAccessMethodDirect
	case FreeAccessMethodVPN, "subscription", "auto-select", "proxy":
		return FreeAccessMethodVPN
	}
	if IsFreeAccessProxyMethod(method) || IsFreeAccessTransparentMethod(method) {
		return method
	}
	return FreeAccessMethodAuto
}

func FreeAccessServiceMethod(settings GlobalAppSettings, serviceTag string) string {
	if settings.FreeAccessMethods == nil {
		return FreeAccessMethodAuto
	}
	method := NormalizeFreeAccessServiceMethod(settings.FreeAccessMethods[serviceTag])
	if runtime.GOOS == "windows" && (IsFreeAccessProxyMethod(method) || IsFreeAccessTransparentMethod(method)) {
		return FreeAccessMethodAuto
	}
	return method
}

func IsFreeAccessProxyMethod(tag string) bool {
	for _, methodTag := range FreeAccessMethodTags() {
		if tag == methodTag {
			return true
		}
	}
	return false
}

func IsFreeAccessTransparentMethod(tag string) bool {
	for _, methodTag := range FreeAccessTransparentMethodTags() {
		if tag == methodTag {
			return true
		}
	}
	return false
}

func FreeAccessServiceMethodOptions() []map[string]string {
	options := []map[string]string{
		{"value": FreeAccessMethodAuto, "label": "Автоматически"},
		{"value": FreeAccessMethodDirect, "label": "Direct"},
		{"value": FreeAccessMethodVPN, "label": "VPN подписка"},
	}
	if runtime.GOOS == "windows" {
		return options
	}
	for _, strategy := range DefaultByeDPIStrategies {
		options = append(options, map[string]string{"value": strategy.Tag, "label": strategy.Label})
	}
	for _, strategy := range DefaultZapretTransparentStrategies {
		if methodSupportsCurrentPlatform(strategy.Platforms) {
			options = append(options, map[string]string{"value": strategy.Tag, "label": strategy.Label})
		}
	}
	return options
}

func ServiceBypassGroupTag(serviceTag string) string {
	return "bypass-" + serviceTag
}

func FreeAccessCandidateTags(hasVPNProxy bool, preferFreeAccess bool) []string {
	methodTags := FreeAccessMethodTags()
	candidates := append([]string{}, methodTags...)
	if !hasVPNProxy {
		return candidates
	}
	if preferFreeAccess {
		return append(candidates, "auto-select")
	}
	return append([]string{"auto-select"}, candidates...)
}

func FreeAccessServiceCandidateTags(service FreeAccessService, hasVPNProxy bool, preferFreeAccess bool) []string {
	if service.RequiresVPN || !serviceHasFreeBypass(service.Tag) {
		if !hasVPNProxy {
			return nil
		}
		return []string{"auto-select"}
	}
	return FreeAccessCandidateTags(hasVPNProxy, preferFreeAccess)
}

func FreeAccessServiceCandidateTagsForSettings(service FreeAccessService, settings GlobalAppSettings, hasVPNProxy bool) []string {
	method := FreeAccessServiceMethod(settings, service.Tag)
	if runtime.GOOS == "windows" {
		// Windows has one automatic path: direct traffic passes through the
		// service-specific profile in the composed winws2 process. VPN is its
		// only fallback; legacy ByeDPI/global-zapret choices migrate to auto.
		switch method {
		case FreeAccessMethodDirect:
			return []string{"direct"}
		case FreeAccessMethodVPN:
			if hasVPNProxy {
				return []string{"auto-select"}
			}
			return nil
		}
		if service.RequiresVPN {
			if hasVPNProxy {
				return []string{"auto-select"}
			}
			return nil
		}
		if !FreeMethodsAllowed(settings) || !serviceHasFreeBypass(service.Tag) {
			if hasVPNProxy {
				return []string{"auto-select"}
			}
			return nil
		}
		if hasVPNProxy {
			return []string{"direct", "auto-select"}
		}
		return []string{"direct"}
	}
	switch method {
	case FreeAccessMethodDirect:
		return []string{"direct"}
	case FreeAccessMethodVPN:
		if hasVPNProxy {
			return []string{"auto-select"}
		}
		return nil
	case FreeAccessMethodAuto:
		if !FreeMethodsAllowed(settings) {
			if hasVPNProxy {
				return []string{"auto-select"}
			}
			return nil
		}
		return FreeAccessServiceCandidateTags(service, hasVPNProxy, true)
	default:
		if !FreeMethodsAllowed(settings) {
			return nil
		}
		if IsFreeAccessTransparentMethod(method) {
			return []string{"direct"}
		}
		if IsFreeAccessProxyMethod(method) {
			return []string{method}
		}
		return FreeAccessServiceCandidateTags(service, hasVPNProxy, false)
	}
}

func FreeAccessServiceRouteOutbound(service FreeAccessService, enabled bool, hasVPNProxy bool) string {
	if !enabled && hasVPNProxy {
		return ServiceBypassGroupTag(service.Tag)
	}
	if service.RequiresVPN {
		if hasVPNProxy {
			if enabled {
				return ServiceBypassGroupTag(service.Tag)
			}
			return "auto-select"
		}
		return "direct"
	}
	if enabled {
		return ServiceBypassGroupTag(service.Tag)
	}
	return VpnOrDirectGroupTag
}

func FreeAccessServiceRouteOutboundForSettings(service FreeAccessService, settings GlobalAppSettings, hasVPNProxy bool) string {
	method := FreeAccessServiceMethod(settings, service.Tag)
	switch method {
	case FreeAccessMethodDirect:
		return "direct"
	case FreeAccessMethodVPN:
		if !hasVPNProxy {
			return NoRouteOutboundTag
		}
		return ServiceBypassGroupTag(service.Tag)
	default:
		if IsFreeAccessProxyMethod(method) || IsFreeAccessTransparentMethod(method) {
			if !FreeMethodsAllowed(settings) {
				return NoRouteOutboundTag
			}
			return ServiceBypassGroupTag(service.Tag)
		}
	}

	if !serviceHasFreeBypass(service.Tag) {
		if hasVPNProxy {
			return ServiceBypassGroupTag(service.Tag)
		}
		if service.RequiresVPN {
			return "direct"
		}
		return ""
	}

	enabled := FreeAccessServiceEnabled(settings, service.Tag)
	return FreeAccessServiceRouteOutbound(service, enabled, hasVPNProxy)
}

func FreeAccessOutboundLabel(outboundTag string) string {
	for _, strategy := range DefaultByeDPIStrategies {
		if outboundTag == strategy.Tag {
			return strategy.Label
		}
	}
	for _, strategy := range DefaultZapretTransparentStrategies {
		if outboundTag == strategy.Tag {
			return strategy.Label
		}
	}
	switch outboundTag {
	case RuProxyOutboundTag:
		return "RU-proxy"
	case "auto-select":
		return "VPN"
	case "proxy":
		return "VPN"
	case "direct":
		return "Direct"
	case NoRouteOutboundTag:
		return "No route"
	default:
		return outboundTag
	}
}

type ByeDPIManager struct {
	exePath    string
	strategies []ByeDPIStrategy
	logger     func(string)

	mu       sync.Mutex
	cmds     map[string]*exec.Cmd
	stopCh   chan struct{}
	wg       sync.WaitGroup
	restarts map[string]int
}

func NewByeDPIManager(basePath string, logger func(string)) *ByeDPIManager {
	return &ByeDPIManager{
		exePath:    filepath.Join(basePath, "bin", ByeDPIProcessName),
		strategies: DefaultByeDPIStrategies,
		logger:     logger,
		cmds:       make(map[string]*exec.Cmd),
		restarts:   make(map[string]int),
	}
}

func (m *ByeDPIManager) log(msg string) {
	if m.logger != nil {
		m.logger(fmt.Sprintf("[ByeDPI] %s", msg))
	}
}

func (m *ByeDPIManager) IsInstalled() bool {
	return fileExists(m.exePath)
}

func (m *ByeDPIManager) Port() int {
	if len(m.strategies) == 0 {
		return ByeDPILocalPort
	}
	return m.strategies[0].Port
}

func (m *ByeDPIManager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.cmds) > 0
}

func (m *ByeDPIManager) ActiveTags() []string {
	m.mu.Lock()
	running := make(map[string]bool, len(m.cmds))
	for tag := range m.cmds {
		running[tag] = true
	}
	strategies := append([]ByeDPIStrategy(nil), m.strategies...)
	m.mu.Unlock()

	tags := make([]string, 0, len(strategies))
	for _, strategy := range strategies {
		if running[strategy.Tag] && m.isAlive(strategy.Port) {
			tags = append(tags, strategy.Tag)
		}
	}
	return tags
}

func (m *ByeDPIManager) WaitForActiveTags(timeout time.Duration) []string {
	deadline := time.Now().Add(timeout)
	var last []string

	for {
		last = m.ActiveTags()

		m.mu.Lock()
		runningCount := len(m.cmds)
		m.mu.Unlock()
		if runningCount == 0 || len(last) == runningCount || time.Now().After(deadline) {
			return last
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func (m *ByeDPIManager) Start() error {
	if !m.IsInstalled() {
		return fmt.Errorf("ciadpi.exe not found: %s", m.exePath)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.cmds) > 0 {
		return nil
	}
	if m.cmds == nil {
		m.cmds = make(map[string]*exec.Cmd)
	}
	if m.restarts == nil {
		m.restarts = make(map[string]int)
	}

	startErrors := make([]error, 0)
	for _, strategy := range m.strategies {
		if err := m.startProcessLocked(strategy); err != nil {
			startErrors = append(startErrors, err)
			m.log(fmt.Sprintf("%s failed to start: %v", strategy.Label, err))
		}
	}
	if len(m.cmds) == 0 {
		return fmt.Errorf("no ByeDPI strategy started: %v", startErrors)
	}

	for tag := range m.restarts {
		m.restarts[tag] = 0
	}
	m.stopCh = make(chan struct{})
	m.wg.Add(1)
	go m.superviseLoop(m.stopCh)

	return nil
}

func (m *ByeDPIManager) startProcessLocked(strategy ByeDPIStrategy) error {
	args := []string{"--ip", "127.0.0.1", "--port", fmt.Sprintf("%d", strategy.Port)}
	args = append(args, strategy.Args...)

	cmd := exec.Command(m.exePath, args...)
	stdout, stdoutErr := cmd.StdoutPipe()
	stderr, stderrErr := cmd.StderrPipe()
	configureBackgroundCommand(cmd)

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", strategy.Tag, err)
	}
	attachManagedCmdToJob(cmd, strategy.Label, m.log)
	if stdoutErr == nil {
		go streamCmdOutput(stdout, strategy.Label+" OUT", m.log)
	}
	if stderrErr == nil {
		go streamCmdOutput(stderr, strategy.Label+" ERR", m.log)
	}

	m.cmds[strategy.Tag] = cmd
	m.log(fmt.Sprintf("%s started on 127.0.0.1:%d (pid=%d)", strategy.Label, strategy.Port, cmd.Process.Pid))
	return nil
}

func (m *ByeDPIManager) Stop() {
	m.mu.Lock()
	if len(m.cmds) == 0 {
		m.mu.Unlock()
		return
	}
	if m.stopCh != nil {
		close(m.stopCh)
		m.stopCh = nil
	}
	cmds := make([]*exec.Cmd, 0, len(m.cmds))
	for _, cmd := range m.cmds {
		cmds = append(cmds, cmd)
	}
	m.cmds = make(map[string]*exec.Cmd)
	m.mu.Unlock()

	m.wg.Wait()

	for _, cmd := range cmds {
		terminateProcessTree(cmd)
	}
	m.log("all strategies stopped")
}

func (m *ByeDPIManager) superviseLoop(stopCh chan struct{}) {
	defer m.wg.Done()

	ticker := time.NewTicker(byeDPIHealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			for _, strategy := range m.strategies {
				if !m.isAlive(strategy.Port) {
					m.handleCrash(strategy)
				}
			}
		}
	}
}

func (m *ByeDPIManager) isAlive(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func (m *ByeDPIManager) handleCrash(strategy ByeDPIStrategy) {
	m.mu.Lock()
	defer m.mu.Unlock()

	cmd, running := m.cmds[strategy.Tag]
	if !running {
		return
	}

	if m.restarts[strategy.Tag] >= byeDPIMaxRestartAttempts {
		m.log(fmt.Sprintf("%s exceeded restart attempts, disabled until next VPN start", strategy.Label))
		terminateProcessTree(cmd)
		delete(m.cmds, strategy.Tag)
		return
	}

	m.restarts[strategy.Tag]++
	m.log(fmt.Sprintf("%s is not responding, restart attempt %d/%d", strategy.Label, m.restarts[strategy.Tag], byeDPIMaxRestartAttempts))

	terminateProcessTree(cmd)
	delete(m.cmds, strategy.Tag)

	time.Sleep(byeDPIRestartBackoff)

	if err := m.startProcessLocked(strategy); err != nil {
		m.log(fmt.Sprintf("%s restart failed: %v", strategy.Label, err))
	}
}
