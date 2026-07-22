package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	discordRealtimeGroupTag = "discord-realtime"
	discordVPNGroupTag      = "discord-vpn"
	discordRealtimeBusyID   = "discord-realtime-connect"
	// Encrypted Discord RTP cannot be identified by payload after the discovery
	// exchange. Keep one stable native discovery/STUN profile and never mutate
	// opaque RTP. Automatic mode starts direct and uses an ordered VPN-source
	// fallback only after bidirectional realtime health fails.
	discordRealtimeMaxTrials = 1
)

var discordDefaultMediaTCPPorts = []int{2048, 2053, 2083, 2087, 2096, 8443}

// Kept for diagnostics/tests that display the stable bootstrap list. Live
// endpoints are logged by the monitor but never trigger a disruptive restart.
var discordMediaTCPPorts = discordTCPPortList(nil)

// discordRealtimeProfile is deliberately independent from the HTTPS service
// method. A Discord web/API method can be healthy while voice discovery or the
// dynamic voice WebSocket is blocked, so the realtime plane must keep its own
// stable profile and route selector.
type discordRealtimeProfile struct {
	Tag          string
	Label        string
	VoiceTCPArgs []string
	VoiceUDPArgs []string
}

func discordRealtimeProfiles() []discordRealtimeProfile {
	return []discordRealtimeProfile{{
		Tag:   "upstream-discovery-stun",
		Label: "official Discord discovery/STUN profile",
		VoiceTCPArgs: []string{
			"--payload=tls_client_hello",
			"--lua-desync=multisplit:pos=1:seqovl=681:seqovl_pattern=google_tls",
		},
		VoiceUDPArgs: []string{
			"--filter-l7=discord,stun",
			"--payload=discord_ip_discovery,stun",
			"--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=2",
		},
	}}
}

func defaultDiscordRealtimeProfile() discordRealtimeProfile {
	return discordRealtimeProfiles()[0]
}

func effectiveDiscordRealtimeProfile(profile discordRealtimeProfile) discordRealtimeProfile {
	if strings.TrimSpace(profile.Tag) == "" {
		return defaultDiscordRealtimeProfile()
	}
	return profile
}

func normalizedDiscordTCPPorts(dynamic []int) []int {
	set := make(map[int]struct{}, len(discordDefaultMediaTCPPorts)+len(dynamic))
	for _, port := range append(append([]int(nil), discordDefaultMediaTCPPorts...), dynamic...) {
		if port > 0 && port <= 65535 && port != 80 && port != 443 {
			set[port] = struct{}{}
		}
	}
	ports := make([]int, 0, len(set))
	for port := range set {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func discordTCPPortList(dynamic []int) string {
	ports := normalizedDiscordTCPPorts(dynamic)
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, strconv.Itoa(port))
	}
	return strings.Join(values, ",")
}

func discordRealtimeProfileAt(index int) (discordRealtimeProfile, bool) {
	profiles := discordRealtimeProfiles()
	if index < 0 || index >= len(profiles) {
		return discordRealtimeProfile{}, false
	}
	return profiles[index], true
}

func validateDiscordRealtimeProfiles() error {
	profiles := discordRealtimeProfiles()
	if len(profiles) != discordRealtimeMaxTrials {
		return fmt.Errorf("Discord realtime profile count = %d, want %d", len(profiles), discordRealtimeMaxTrials)
	}
	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if profile.Tag == "" || len(profile.VoiceTCPArgs) == 0 || len(profile.VoiceUDPArgs) == 0 {
			return fmt.Errorf("incomplete Discord realtime profile: %q", profile.Tag)
		}
		if _, ok := seen[profile.Tag]; ok {
			return fmt.Errorf("duplicate Discord realtime profile: %s", profile.Tag)
		}
		seen[profile.Tag] = struct{}{}
	}
	return nil
}
