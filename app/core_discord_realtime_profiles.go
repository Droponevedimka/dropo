package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const (
	discordRealtimeGroupTag  = "discord-realtime"
	discordVPNGroupTag       = "discord-vpn"
	discordRealtimeBusyID    = "discord-realtime-connect"
	discordRealtimeMaxTrials = 20
)

var discordDefaultMediaTCPPorts = []int{2048, 2053, 2083, 2087, 2096, 8443}

// Kept for diagnostics/tests that display the bootstrap list. Runtime matching
// also appends ports learned from live Discord voice gateway connections.
var discordMediaTCPPorts = discordTCPPortList(nil)

// discordRealtimeProfile is deliberately independent from the HTTPS service
// method. A Discord web/API method can be healthy while voice discovery or the
// dynamic voice WebSocket is blocked, so realtime failures must rotate only
// these profiles.
type discordRealtimeProfile struct {
	Tag          string
	Label        string
	VoiceTCPArgs []string
	VoiceUDPArgs []string
}

func discordRealtimeProfiles() []discordRealtimeProfile {
	tcpProfiles := []struct {
		tag   string
		label string
		args  []string
	}{
		{
			tag:   "tcp-multisplit-681",
			label: "voice TCP multisplit 681",
			args: []string{
				"--payload=tls_client_hello",
				"--lua-desync=multisplit:pos=1:seqovl=681:seqovl_pattern=google_tls",
			},
		},
		{
			tag:   "tcp-fakedsplit-ts",
			label: "voice TCP fakedsplit ts",
			args: []string{
				"--payload=tls_client_hello",
				"--lua-desync=fake:blob=google_tls:tcp_ts=-600000:repeats=6",
				"--lua-desync=fakedsplit:pos=2:pattern=0x00:tcp_ts=-600000",
			},
		},
		{
			tag:   "tcp-fake-multisplit",
			label: "voice TCP fake+multisplit",
			args: []string{
				"--payload=tls_client_hello",
				"--lua-desync=fake:blob=google_tls:tcp_ts=-600000:repeats=8",
				"--lua-desync=multisplit:pos=1:seqovl=664:seqovl_pattern=google_tls",
			},
		},
		{
			tag:   "tcp-multidisorder",
			label: "voice TCP multidisorder",
			args: []string{
				"--payload=tls_client_hello",
				"--lua-desync=fake:blob=google_tls:tcp_seq=-10000:repeats=6",
				"--lua-desync=multidisorder:pos=1,midsld",
			},
		},
	}
	udpProfiles := []struct {
		tag   string
		label string
		arg   string
	}{
		{"udp-upstream-r2", "UDP fake r2", "--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=2"},
		{"udp-r1", "UDP fake r1", "--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=1"},
		{"udp-r3", "UDP fake r3", "--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=3"},
		{"udp-r5", "UDP fake r5", "--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=5"},
		{"udp-autottl-r2", "UDP fake autottl r2", "--lua-desync=fake:blob=0x00000000000000000000000000000000:ip_autottl=-2,3-20:ip6_autottl=-2,3-20:repeats=2"},
	}

	profiles := make([]discordRealtimeProfile, 0, discordRealtimeMaxTrials)
	for _, tcp := range tcpProfiles {
		for _, udp := range udpProfiles {
			profiles = append(profiles, discordRealtimeProfile{
				Tag:          tcp.tag + "+" + udp.tag,
				Label:        tcp.label + " / " + udp.label,
				VoiceTCPArgs: append([]string(nil), tcp.args...),
				VoiceUDPArgs: []string{
					"--filter-l7=discord,stun",
					"--payload=discord_ip_discovery,stun",
					udp.arg,
				},
			})
		}
	}
	return profiles[:discordRealtimeMaxTrials]
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
