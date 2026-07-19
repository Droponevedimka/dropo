package main

import (
	"encoding/json"
	"strings"
)

// ExternalVPNConflict is a user-facing summary of another active VPN-like
// connection that can fight dropo for routes, DNS, or TUN ownership.
type ExternalVPNConflict struct {
	Name   string `json:"name"`
	Kind   string `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
	Source string `json:"source,omitempty"`
}

type externalVPNCandidate struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Source string `json:"source"`
	Status string `json:"status"`
}

// CheckExternalVPNConflicts reports active VPN-like connections controlled by
// other apps. It is intentionally a preflight API for the UI; Start remains
// deterministic for tests and automation.
func (a *App) CheckExternalVPNConflicts() map[string]interface{} {
	a.waitForInit()

	conflicts, err := detectExternalVPNConflicts()
	if conflicts == nil {
		conflicts = []ExternalVPNConflict{}
	}
	result := map[string]interface{}{
		"success":      true,
		"supported":    systemInspectorSupported,
		"hasConflicts": len(conflicts) > 0,
		"conflicts":    conflicts,
	}
	if err != nil {
		if a != nil {
			a.writeLog("[VPNConflict] preflight failed: " + err.Error())
		}
		result["warning"] = err.Error()
	}
	return result
}

func parseExternalVPNCandidates(data []byte) ([]externalVPNCandidate, error) {
	text := strings.TrimSpace(string(data))
	if text == "" || text == "null" {
		return nil, nil
	}
	if strings.HasPrefix(text, "[") {
		var candidates []externalVPNCandidate
		if err := json.Unmarshal([]byte(text), &candidates); err != nil {
			return nil, err
		}
		return candidates, nil
	}

	var candidate externalVPNCandidate
	if err := json.Unmarshal([]byte(text), &candidate); err != nil {
		return nil, err
	}
	return []externalVPNCandidate{candidate}, nil
}

func filterExternalVPNConflicts(candidates []externalVPNCandidate) []ExternalVPNConflict {
	conflicts := make([]ExternalVPNConflict, 0)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if !candidateLooksLikeExternalVPN(candidate) {
			continue
		}

		name := strings.TrimSpace(candidate.Name)
		detail := strings.TrimSpace(candidate.Detail)
		if name == "" {
			name = detail
			detail = ""
		}
		if name == "" {
			continue
		}

		key := strings.ToLower(candidate.Source + "|" + name + "|" + detail)
		if seen[key] {
			continue
		}
		seen[key] = true

		conflicts = append(conflicts, ExternalVPNConflict{
			Name:   name,
			Kind:   externalVPNKind(candidate),
			Detail: detail,
			Source: strings.TrimSpace(candidate.Source),
		})
	}
	return conflicts
}

func candidateLooksLikeExternalVPN(candidate externalVPNCandidate) bool {
	source := strings.ToLower(strings.TrimSpace(candidate.Source))
	status := strings.ToLower(strings.TrimSpace(candidate.Status))
	haystack := strings.ToLower(candidate.Name + " " + candidate.Detail)
	if strings.TrimSpace(haystack) == "" || knownDropoOrNonVPNAdapter(haystack) {
		return false
	}

	if source == "vpn-connection" {
		return status == "" || status == "connected"
	}
	if source == "dpi-process" || source == "packet-filter-service" {
		return status == "" || status == "running"
	}
	if status != "" && status != "up" && status != "connected" && status != "true" {
		return false
	}

	for _, keyword := range externalVPNAdapterKeywords {
		if strings.Contains(haystack, keyword) {
			return true
		}
	}
	return false
}

func knownDropoOrNonVPNAdapter(text string) bool {
	for _, needle := range ignoredExternalVPNAdapterKeywords {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func externalVPNKind(candidate externalVPNCandidate) string {
	switch strings.ToLower(strings.TrimSpace(candidate.Source)) {
	case "vpn-connection":
		return "VPN profile"
	case "dpi-process":
		return "DPI bypass process"
	case "packet-filter-service":
		return "Packet filter service"
	}
	return "Network adapter"
}

var externalVPNAdapterKeywords = []string{
	"vpn",
	"wireguard",
	"wintun",
	"tap-windows",
	"tap adapter",
	"openvpn",
	"tailscale",
	"zerotier",
	"nordvpn",
	"nordlynx",
	"mullvad",
	"protonvpn",
	"surfshark",
	"expressvpn",
	"windscribe",
	"cloudflare warp",
	"warp",
	"anyconnect",
	"cisco secure client",
	"forticlient",
	"fortinet",
	"globalprotect",
	"palo alto",
	"check point",
	"checkpoint",
	"pulse secure",
	"juniper",
	"sonicwall",
	"outline",
	"shadowsocks",
	"clash",
	"mihomo",
	"v2ray",
	"xray",
}

var ignoredExternalVPNAdapterKeywords = []string{
	"dropo-wg-",
	"kampus-wg-",
	"loopback",
	"hyper-v",
	"vethernet",
	"vmware",
	"virtualbox",
	"docker",
	"bluetooth",
	"wi-fi direct",
	"wifi direct",
	"npcap",
	"wan miniport",
	"teredo",
	"isatap",
	"6to4",
	"kernel debug",
}
