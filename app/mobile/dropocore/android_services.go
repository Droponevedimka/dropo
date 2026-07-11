package dropocore

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	androidRoutePolicyDirect = "direct"
	androidRoutePolicyVPN    = "vpn"
)

type androidService struct {
	Tag            string
	Name           string
	PackageNames   []string
	DomainSuffixes []string
	IPCIDRs        []string
	HealthURL      string
	ProbeURLs      []string
	DefaultPolicy  string
}

func androidServiceCatalog() []androidService {
	return []androidService{
		{
			Tag:  "discord",
			Name: "Discord",
			PackageNames: []string{
				"com.discord",
			},
			DomainSuffixes: []string{
				"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discord.gift",
				"discordcdn.com", "discordstatus.com",
			},
			IPCIDRs:       []string{"66.22.192.0/18"},
			HealthURL:     "https://discord.com/api/v10/gateway",
			ProbeURLs:     []string{"https://discord.com", "https://cdn.discordapp.com", "https://media.discordapp.net"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "youtube",
			Name: "YouTube",
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
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "meta",
			Name: "Instagram / Facebook",
			DomainSuffixes: []string{
				"instagram.com", "cdninstagram.com", "facebook.com", "fbcdn.net", "fb.com", "facebook.net",
				"messenger.com", "m.me", "threads.net", "connect.facebook.net",
			},
			IPCIDRs: []string{
				"31.13.64.0/18", "66.220.144.0/20", "69.63.176.0/20", "69.171.224.0/19",
				"129.134.0.0/16", "157.240.0.0/16", "173.252.64.0/18", "185.60.216.0/22",
			},
			HealthURL:     "https://www.instagram.com",
			ProbeURLs:     []string{"https://www.facebook.com", "https://connect.facebook.net"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "twitter",
			Name:           "X (Twitter)",
			DomainSuffixes: []string{"twitter.com", "x.com", "twimg.com", "t.co", "ads-twitter.com"},
			HealthURL:      "https://x.com",
			ProbeURLs:      []string{"https://abs.twimg.com"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:            "linkedin",
			Name:           "LinkedIn",
			DomainSuffixes: []string{"linkedin.com", "licdn.com", "lnkd.in", "linkedin.cn"},
			HealthURL:      "https://www.linkedin.com",
			ProbeURLs:      []string{"https://static.licdn.com"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:            "signal",
			Name:           "Signal",
			DomainSuffixes: []string{"signal.org", "signal.me", "whispersystems.org", "signal.art"},
			HealthURL:      "https://signal.org",
			ProbeURLs:      []string{"https://updates.signal.org"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "telegram",
			Name: "Telegram",
			PackageNames: []string{
				"org.telegram.messenger", "org.telegram.messenger.web",
			},
			DomainSuffixes: []string{
				"telegram.org", "telegram.me", "t.me", "telegra.ph", "telesco.pe", "tdesktop.com",
				"telegram-cdn.org",
			},
			IPCIDRs: []string{
				"149.154.160.0/20", "91.105.192.0/23", "91.108.4.0/22", "91.108.8.0/22",
				"91.108.12.0/22", "91.108.16.0/22", "91.108.20.0/22", "91.108.56.0/22",
				"185.76.151.0/24", "2001:b28:f23d::/48", "2001:b28:f23f::/48",
				"2001:67c:4e8::/48", "2001:b28:f23c::/48", "2a0a:f280::/32",
			},
			HealthURL:     "https://telegram.org",
			ProbeURLs:     []string{"https://web.telegram.org", "https://t.me"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "whatsapp",
			Name: "WhatsApp",
			DomainSuffixes: []string{
				"whatsapp.com", "whatsapp.net", "wa.me", "whatsappbrand.com",
				"cdn.whatsapp.net", "static.whatsapp.net", "scontent.whatsapp.net",
				"graph.whatsapp.com", "wa.meta.vc",
			},
			HealthURL: "https://web.whatsapp.com",
			ProbeURLs: []string{
				"https://www.whatsapp.com",
				"https://static.whatsapp.net",
				"https://graph.whatsapp.com",
			},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "facetime",
			Name: "FaceTime / iMessage",
			DomainSuffixes: []string{
				"facetime.apple.com", "ess.apple.com", "identity.apple.com",
				"push.apple.com", "init.itunes.apple.com",
			},
			HealthURL:     "https://facetime.apple.com",
			ProbeURLs:     []string{"https://identity.apple.com", "https://init.itunes.apple.com"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "viber",
			Name:           "Viber",
			DomainSuffixes: []string{"viber.com", "vb.me", "viberdns.com", "viber.co", "viberapp.com"},
			HealthURL:      "https://www.viber.com",
			ProbeURLs:      []string{"https://account.viber.com"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "snapchat",
			Name: "Snapchat",
			DomainSuffixes: []string{
				"snapchat.com", "sc-gw.com", "sc-cdn.net", "snapkit.com", "sc-static.net",
				"sc-prod.net", "sc-jpl.com", "sc-corp.net", "snapads.com", "snap.com",
				"addlive.io", "feelinsonice.com", "snapmap.com", "snapmap.org", "snapmaps.com",
			},
			HealthURL:     "https://www.snapchat.com",
			ProbeURLs:     []string{"https://accounts.snapchat.com", "https://app.snapchat.com"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "twitch",
			Name:           "Twitch",
			DomainSuffixes: []string{"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "ext-twitch.tv"},
			HealthURL:      "https://www.twitch.tv",
			ProbeURLs:      []string{"https://static.twitchcdn.net", "https://usher.ttvnw.net"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:            "spotify",
			Name:           "Spotify",
			DomainSuffixes: []string{"spotify.com", "scdn.co", "spotifycdn.com", "spoti.fi"},
			HealthURL:      "https://open.spotify.com",
			ProbeURLs:      []string{"https://api.spotify.com", "https://i.scdn.co"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "tiktok",
			Name: "TikTok",
			DomainSuffixes: []string{
				"tiktok.com", "tiktokcdn.com", "tiktokcdn-us.com", "tiktokv.com", "tiktokv.us",
				"tiktokw.us", "ttdns2.com", "byteoversea.com", "ibyteimg.com", "ibytedtos.com",
				"ttwstatic.com",
			},
			HealthURL: "https://www.tiktok.com",
			ProbeURLs: []string{
				"https://www.tiktok.com/api/recommend/item_list/",
				"https://lf16-tiktok-web.ttwstatic.com",
			},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "canva",
			Name:           "Canva",
			DomainSuffixes: []string{"canva.com", "canva.site", "canva.design", "canva.me", "canva-apps.com"},
			HealthURL:      "https://www.canva.com",
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "notion",
			Name: "Notion",
			DomainSuffixes: []string{
				"notion.com", "notion.so", "notion.site", "notion-static.com", "notionusercontent.com",
				"app.notion.com", "api.notion.com", "img.notionusercontent.com", "secure.notion-static.com",
			},
			IPCIDRs:       []string{"131.149.232.0/21", "208.103.161.0/24", "2602:F79A::/36"},
			HealthURL:     "https://www.notion.com",
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "slack",
			Name: "Slack",
			DomainSuffixes: []string{
				"slack.com", "slackb.com", "slack-edge.com", "slack-files.com", "slack-imgs.com",
				"slack-msgs.com", "slack-core.com", "slack-redir.net",
			},
			HealthURL:     "https://slack.com",
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "miro",
			Name: "Miro",
			DomainSuffixes: []string{
				"miro.com", "miro-apps.com", "mirostatic.com", "realtimeboard.com",
				"onlinewhiteboard.com", "webwhiteboard.com",
			},
			HealthURL:     "https://miro.com",
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "wix",
			Name: "Wix",
			DomainSuffixes: []string{
				"wix.com", "wixsite.com", "wixstatic.com", "wixmp.com", "wixapps.net",
				"editorx.com", "parastorage.com",
			},
			HealthURL:     "https://www.wix.com",
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "coda",
			Name:           "Coda",
			DomainSuffixes: []string{"coda.io", "codahosted.io", "codacontent.io"},
			HealthURL:      "https://coda.io",
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "grammarly",
			Name: "Grammarly",
			DomainSuffixes: []string{
				"grammarly.com", "grammarly.io", "grammarly.net", "grammarlyaws.com",
			},
			HealthURL:     "https://www.grammarly.com",
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "docker",
			Name: "Docker Hub",
			DomainSuffixes: []string{
				"docker.com", "docker.io", "dockerhub.com", "login.docker.com", "auth.docker.com",
				"registry-1.docker.io", "auth.docker.io", "desktop.docker.com", "hub.docker.com",
				"production.cloudfront.docker.com", "production.cloudflare.docker.com",
				"docker-pinata-support.s3.amazonaws.com", "api.docker.com", "api.dso.docker.com",
				"dhi.io", "registry.scout.docker.com",
			},
			HealthURL:     "https://hub.docker.com",
			ProbeURLs:     []string{"https://registry-1.docker.io", "https://auth.docker.io"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "clickup",
			Name: "ClickUp",
			DomainSuffixes: []string{
				"clickup.com", "clickup-au.com", "clickup-attachments.com", "clickup-prod.com",
				"clickup-eu.com", "clickup-sg.com", "clickup.ada.support", "codox.io",
			},
			HealthURL:     "https://app.clickup.com",
			ProbeURLs:     []string{"https://api.clickup.com", "https://attachments.clickup.com"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:            "manychat",
			Name:           "Manychat",
			DomainSuffixes: []string{"manychat.com"},
			HealthURL:      "https://app.manychat.com",
			ProbeURLs:      []string{"https://api.manychat.com"},
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:            "helpscout",
			Name:           "Help Scout",
			DomainSuffixes: []string{"helpscout.com", "helpscout.net", "helpscoutdocs.com"},
			HealthURL:      "https://secure.helpscout.net",
			DefaultPolicy:  androidRoutePolicyVPN,
		},
		{
			Tag:  "atlassian",
			Name: "Atlassian / Trello",
			DomainSuffixes: []string{
				"atlassian.com", "atlassian.net", "atlassian.io", "atlassianstatus.com",
				"atlassianusercontent.com", "jira.com", "trello.com", "trello.services",
				"trellocdn.com", "bitbucket.org", "bitbucket.io", "bitbucketusercontent.com",
				"statuspage.io", "opsgenie.com",
			},
			HealthURL:     "https://trello.com",
			ProbeURLs:     []string{"https://id.atlassian.com", "https://bitbucket.org"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
		{
			Tag:  "openai",
			Name: "AI services",
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
			HealthURL:     "https://chatgpt.com",
			ProbeURLs:     []string{"https://api.openai.com"},
			DefaultPolicy: androidRoutePolicyVPN,
		},
	}
}

func androidServiceByTag(tag string) (androidService, bool) {
	tag = strings.TrimSpace(tag)
	for _, service := range androidServiceCatalog() {
		if service.Tag == tag {
			return service, true
		}
	}
	return androidService{}, false
}

func androidServicePolicy(service androidService, policies map[string]string) string {
	if policies != nil {
		if policy := normalizeAndroidRoutePolicy(policies[service.Tag]); policy != "" {
			return policy
		}
	}
	if policy := normalizeAndroidRoutePolicy(service.DefaultPolicy); policy != "" {
		return policy
	}
	return androidRoutePolicyVPN
}

func normalizeAndroidRoutePolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case androidRoutePolicyDirect, "local":
		return androidRoutePolicyDirect
	case androidRoutePolicyVPN, "proxy", "subscription", "auto-select":
		return androidRoutePolicyVPN
	default:
		return ""
	}
}

func androidRoutePolicyLabel(policy string) string {
	if normalizeAndroidRoutePolicy(policy) == androidRoutePolicyDirect {
		return "Напрямую"
	}
	return "Через VPN"
}

func androidRoutePoliciesLocked() map[string]string {
	if len(current.RoutePolicies) == 0 {
		return nil
	}
	result := make(map[string]string, len(current.RoutePolicies))
	for tag, policy := range current.RoutePolicies {
		if normalized := normalizeAndroidRoutePolicy(policy); normalized != "" {
			result[tag] = normalized
		}
	}
	return result
}

func androidServiceRoutesLocked(live bool) []routeInfo {
	delay := 0
	if live && current.Connected {
		delay = 20
	}
	policies := androidRoutePoliciesLocked()
	catalog := androidServiceCatalog()
	routes := make([]routeInfo, 0, len(catalog))
	for index, service := range catalog {
		policy := androidServicePolicy(service, policies)
		methodLabel := androidRoutePolicyLabel(policy)
		routes = append(routes, routeInfo{
			Tag:                  service.Tag,
			Name:                 service.Name,
			Method:               methodLabel,
			EffectiveMethodLabel: methodLabel,
			RequiresVPN:          policy == androidRoutePolicyVPN,
			DelayMS:              delay + 8 + index%9,
			DomainSuffixes:       append([]string(nil), service.DomainSuffixes...),
			IPCidrs:              append([]string(nil), service.IPCIDRs...),
		})
	}
	return routes
}

func androidRouteMethodOptions() []map[string]string {
	return []map[string]string{
		{"tag": androidRoutePolicyDirect, "label": androidRoutePolicyLabel(androidRoutePolicyDirect)},
		{"tag": androidRoutePolicyVPN, "label": androidRoutePolicyLabel(androidRoutePolicyVPN)},
	}
}

func androidRouteMethodCache(services []routeInfo) map[string]string {
	result := make(map[string]string, len(services))
	for _, service := range services {
		if service.RequiresVPN {
			result[service.Tag] = androidRoutePolicyVPN
			continue
		}
		result[service.Tag] = androidRoutePolicyDirect
	}
	return result
}

func setAndroidRoutePolicyLocked(args []interface{}) map[string]interface{} {
	if current.Connected {
		return map[string]interface{}{"success": false, "error": "VPN must be stopped before changing service routes"}
	}
	tag := stringArg(args, 0, "")
	policy := normalizeAndroidRoutePolicy(stringArg(args, 1, ""))
	service, ok := androidServiceByTag(tag)
	if !ok {
		return map[string]interface{}{"success": false, "error": "Unknown Android route service: " + tag}
	}
	if policy == "" {
		return map[string]interface{}{"success": false, "error": "Unknown Android route policy"}
	}
	defaultPolicy := androidServicePolicy(service, nil)
	if current.RoutePolicies == nil {
		current.RoutePolicies = map[string]string{}
	}
	if policy == defaultPolicy {
		delete(current.RoutePolicies, tag)
	} else {
		current.RoutePolicies[tag] = policy
	}
	clearCachedConfigLocked()
	appendLogLocked(fmt.Sprintf("android route policy %s: %s", tag, policy))
	_ = saveLocked()
	return map[string]interface{}{
		"success":              true,
		"tag":                  tag,
		"method":               policy,
		"methodLabel":          androidRoutePolicyLabel(policy),
		"effectiveMethodLabel": androidRoutePolicyLabel(policy),
		"requiresVpn":          policy == androidRoutePolicyVPN,
	}
}

func androidServiceDomainSuffixesByPolicy(policies map[string]string, policy string) []string {
	policy = normalizeAndroidRoutePolicy(policy)
	if policy == "" {
		return nil
	}
	result := []string{}
	for _, service := range androidServiceCatalog() {
		if androidServicePolicy(service, policies) != policy {
			continue
		}
		result = append(result, service.DomainSuffixes...)
	}
	return uniqueNonEmptyStrings(result)
}

func androidServiceIPCIDRsByPolicy(policies map[string]string, policy string) []string {
	policy = normalizeAndroidRoutePolicy(policy)
	if policy == "" {
		return nil
	}
	result := []string{}
	for _, service := range androidServiceCatalog() {
		if androidServicePolicy(service, policies) != policy {
			continue
		}
		result = append(result, service.IPCIDRs...)
	}
	return uniqueNonEmptyStrings(result)
}

func androidServicePackageNamesByPolicy(policies map[string]string, policy string) []string {
	policy = normalizeAndroidRoutePolicy(policy)
	if policy == "" {
		return nil
	}
	result := []string{}
	for _, service := range androidServiceCatalog() {
		if androidServicePolicy(service, policies) != policy {
			continue
		}
		result = append(result, service.PackageNames...)
	}
	return uniqueNonEmptyStrings(result)
}

func runAndroidClientQuickCheck() string {
	mu.Lock()
	services := androidServiceRoutesLocked(true)
	startServices := make([]map[string]interface{}, 0, len(services))
	for _, service := range services {
		startServices = append(startServices, map[string]interface{}{"tag": service.Tag, "name": service.Name})
	}
	appendLogLocked(fmt.Sprintf("android service quick check started (%d services)", len(services)))
	emitLocked("route-probe-start", map[string]interface{}{
		"serviceCount": len(services),
		"services":     startServices,
	})
	_ = saveLocked()
	mu.Unlock()

	client := &http.Client{Timeout: 8 * time.Second}
	results := make([]map[string]interface{}, 0, len(services))
	failedCount := 0
	for _, service := range services {
		target := androidServiceHealthTarget(service.Tag)
		started := time.Now()
		statusCode, errText := probeAndroidService(client, target)
		delayMS := int(time.Since(started).Milliseconds())
		success := errText == ""
		if !success {
			failedCount++
		}
		result := map[string]interface{}{
			"tag":         service.Tag,
			"name":        service.Name,
			"success":     success,
			"target":      target,
			"status":      statusCode,
			"error":       errText,
			"latencyMs":   delayMS,
			"methodTag":   androidRouteMethodCache([]routeInfo{service})[service.Tag],
			"methodLabel": service.EffectiveMethodLabel,
		}
		results = append(results, result)

		mu.Lock()
		emitLocked("route-probe-service", result)
		if success {
			appendLogLocked(fmt.Sprintf("android service quick check ok: %s %s (%d ms)", service.Tag, target, delayMS))
		} else {
			appendLogLocked(fmt.Sprintf("android service quick check failed: %s %s: %s", service.Tag, target, errText))
		}
		_ = saveLocked()
		mu.Unlock()
	}

	success := failedCount == 0
	payload := map[string]interface{}{
		"success":     success,
		"android":     true,
		"services":    results,
		"total":       len(results),
		"totalCount":  len(results),
		"failedCount": failedCount,
		"checkedAt":   currentTimeRFC3339(),
	}
	mu.Lock()
	emitLocked("route-probe-complete", payload)
	appendLogLocked(fmt.Sprintf("android service quick check complete: %d/%d failed", failedCount, len(results)))
	_ = saveLocked()
	mu.Unlock()
	return encode(payload)
}

func androidServiceHealthTarget(tag string) string {
	service, ok := androidServiceByTag(tag)
	if !ok {
		return "https://www.gstatic.com/generate_204"
	}
	if strings.TrimSpace(service.HealthURL) != "" {
		return strings.TrimSpace(service.HealthURL)
	}
	for _, target := range service.ProbeURLs {
		if strings.TrimSpace(target) != "" {
			return strings.TrimSpace(target)
		}
	}
	if len(service.DomainSuffixes) > 0 {
		return "https://" + strings.TrimPrefix(service.DomainSuffixes[0], ".")
	}
	return "https://www.gstatic.com/generate_204"
}

func probeAndroidService(client *http.Client, target string) (int, string) {
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return 0, err.Error()
	}
	request.Header.Set("User-Agent", "dropo-android-check/1.0")
	response, err := client.Do(request)
	if err != nil {
		return 0, err.Error()
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode >= 200 && response.StatusCode < 500 {
		return response.StatusCode, ""
	}
	return response.StatusCode, response.Status
}

func uniqueNonEmptyStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
