package main

import (
	"fmt"
	"net"
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
	Tag               string
	Label             string
	VoiceTCPArgs      []string
	VoiceUDPArgs      []string
	VoiceMediaUDPArgs []string
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
		tag          string
		label        string
		blob         string
		repeats      int
		mediaPackets int
		autottl      bool
	}{
		{"zero-r2-d2", "zero fake r2 / first 2 media packets", "0x00000000000000000000000000000000", 2, 2, false},
		{"zero-r3-d2", "zero fake r3 / first 2 media packets", "0x00000000000000000000000000000000", 3, 2, false},
		{"zero-r5-d2", "zero fake r5 / first 2 media packets", "0x00000000000000000000000000000000", 5, 2, false},
		{"google-r2-d2", "QUIC fake r2 / first 2 media packets", "google_quic", 2, 2, false},
		{"google-r3-d2", "QUIC fake r3 / first 2 media packets", "google_quic", 3, 2, false},
		{"default-r2-d2", "default QUIC fake r2 / first 2 media packets", "fake_default_quic", 2, 2, false},
		{"zero-r1-d4", "zero fake r1 / first 4 media packets", "0x00000000000000000000000000000000", 1, 4, false},
		{"zero-r2-d4", "zero fake r2 / first 4 media packets", "0x00000000000000000000000000000000", 2, 4, false},
		{"zero-r3-d4", "zero fake r3 / first 4 media packets", "0x00000000000000000000000000000000", 3, 4, false},
		{"zero-r5-d4", "zero fake r5 / first 4 media packets", "0x00000000000000000000000000000000", 5, 4, false},
		{"zero-r8-d4", "zero fake r8 / first 4 media packets", "0x00000000000000000000000000000000", 8, 4, false},
		{"google-r2-d4", "QUIC fake r2 / first 4 media packets", "google_quic", 2, 4, false},
		{"google-r3-d4", "QUIC fake r3 / first 4 media packets", "google_quic", 3, 4, false},
		{"google-r5-d4", "QUIC fake r5 / first 4 media packets", "google_quic", 5, 4, false},
		{"default-r2-d4", "default QUIC fake r2 / first 4 media packets", "fake_default_quic", 2, 4, false},
		{"default-r3-d4", "default QUIC fake r3 / first 4 media packets", "fake_default_quic", 3, 4, false},
		{"zero-autottl-r2-d4", "zero fake autottl r2 / first 4 media packets", "0x00000000000000000000000000000000", 2, 4, true},
		{"google-autottl-r2-d4", "QUIC fake autottl r2 / first 4 media packets", "google_quic", 2, 4, true},
		{"zero-r3-d8", "zero fake r3 / first 8 media packets", "0x00000000000000000000000000000000", 3, 8, false},
		{"google-r3-d8", "QUIC fake r3 / first 8 media packets", "google_quic", 3, 8, false},
	}

	profiles := make([]discordRealtimeProfile, 0, discordRealtimeMaxTrials)
	for i, udp := range udpProfiles {
		tcp := tcpProfiles[(i*len(tcpProfiles))/len(udpProfiles)]
		fakeArgs := fmt.Sprintf("--lua-desync=fake:payload=all:blob=%s:repeats=%d", udp.blob, udp.repeats)
		if udp.autottl {
			fakeArgs += ":ip_autottl=-2,3-20:ip6_autottl=-2,3-20"
		}
		profiles = append(profiles, discordRealtimeProfile{
			Tag:          tcp.tag + "+" + udp.tag,
			Label:        tcp.label + " / " + udp.label,
			VoiceTCPArgs: append([]string(nil), tcp.args...),
			VoiceUDPArgs: []string{
				"--filter-l7=discord,stun",
				"--payload=discord_ip_discovery,stun",
				fakeArgs,
			},
			VoiceMediaUDPArgs: []string{
				"--out-range=-d" + strconv.Itoa(udp.mediaPackets),
				"--payload=all",
				fakeArgs,
			},
		})
	}
	return profiles
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

func normalizedDiscordUDPPorts(dynamic []int) []int {
	set := make(map[int]struct{}, len(dynamic))
	for _, port := range dynamic {
		if port > 0 && port <= 65535 {
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

func discordUDPPortList(dynamic []int) string {
	ports := normalizedDiscordUDPPorts(dynamic)
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, strconv.Itoa(port))
	}
	return strings.Join(values, ",")
}

func normalizedDiscordUDPIPs(dynamic []string) []string {
	set := make(map[string]struct{}, len(dynamic))
	for _, value := range dynamic {
		ip := net.ParseIP(strings.TrimSpace(value))
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() {
			continue
		}
		set[ip.String()] = struct{}{}
	}
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
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
		if profile.Tag == "" || len(profile.VoiceTCPArgs) == 0 || len(profile.VoiceUDPArgs) == 0 || len(profile.VoiceMediaUDPArgs) == 0 {
			return fmt.Errorf("incomplete Discord realtime profile: %q", profile.Tag)
		}
		if _, ok := seen[profile.Tag]; ok {
			return fmt.Errorf("duplicate Discord realtime profile: %s", profile.Tag)
		}
		seen[profile.Tag] = struct{}{}
	}
	return nil
}
