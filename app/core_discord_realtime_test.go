package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDiscordRealtimeProfilesAreBoundedAndUnique(t *testing.T) {
	if err := validateDiscordRealtimeProfiles(); err != nil {
		t.Fatal(err)
	}
	profiles := discordRealtimeProfiles()
	if got := len(profiles); got != discordRealtimeMaxTrials {
		t.Fatalf("profile count = %d, want %d", got, discordRealtimeMaxTrials)
	}
	if !strings.Contains(strings.Join(profiles[0].VoiceUDPArgs, " "), "repeats=2") {
		t.Fatalf("first profile must be the upstream Discord UDP baseline: %#v", profiles[0])
	}
}

func TestDiscordDynamicMediaFilterHasNoPortOrAddressFamilyGuess(t *testing.T) {
	for _, forbidden := range []string{"DstPort", "50000", "19294", " and ip and"} {
		if strings.Contains(discordDynamicMediaFilter, forbidden) {
			t.Fatalf("dynamic Discord filter still contains %q: %s", forbidden, discordDynamicMediaFilter)
		}
	}
	for _, required := range []string{"udp.PayloadLength=74", "udp.Payload32[0]=0x00010046"} {
		if !strings.Contains(discordDynamicMediaFilter, required) {
			t.Fatalf("dynamic Discord filter is missing %q", required)
		}
	}
}

func TestDiscordSelectionUsesGeneratedFilterAndLearnedTCPPort(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.learnedPorts[32123] = time.Now()
	app := &App{basePath: t.TempDir(), discordRealtime: controller}
	selection := app.decorateDiscordRealtimeSelection(serviceWinwsSelection{
		ServiceTag:   "discord",
		HostlistPath: `C:\state\discord.txt`,
		Method:       methodMultisplit("m1", "M1", 652, 2, googleQUICPayload),
	})
	if selection.DiscordMediaRawFilter == "" {
		t.Fatal("generated media filter path is empty")
	}
	filter, err := os.ReadFile(selection.DiscordMediaRawFilter)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(filter), "DstPort") {
		t.Fatalf("generated filter still guesses ports: %s", filter)
	}
	joined := strings.Join(composeServiceWinwsArgs([]serviceWinwsSelection{selection}, `C:\dropo\bin`), " ")
	if !strings.Contains(joined, "--wf-tcp-out=80,443,2048,2053,2083,2087,2096,8443,32123") {
		t.Fatalf("learned TCP port is missing from WinDivert bootstrap: %s", joined)
	}
	if !strings.Contains(joined, "--wf-raw-part=@"+selection.DiscordMediaRawFilter) {
		t.Fatalf("generated raw filter is not used: %s", joined)
	}
}

func TestDiscordRealtimeLearnsDynamicTCPPortAndDetectsMissingUDPReply(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	controller.automatic = true
	started := time.Unix(1000, 0)
	connections := []clashConnection{
		{
			ID: "voice-gateway",
			Metadata: clashConnectionMetadata{
				Network:         "tcp",
				Host:            "sweetwater-123.discord.media",
				DestinationIP:   "203.0.113.10",
				DestinationPort: "32123",
				ProcessPath:     `C:\Users\client\AppData\Local\Discord\Discord.exe`,
			},
			Upload:   100,
			Download: 100,
		},
		{
			ID: "voice-udp",
			Metadata: clashConnectionMetadata{
				Network:         "udp",
				DestinationIP:   "203.0.113.20",
				DestinationPort: float64(45678),
				Process:         "Discord.exe",
			},
			Upload: 74,
		},
	}
	actions := controller.observeConnections(connections, started)
	if len(actions) != 1 || actions[0].learnedPort != 32123 || actions[0].failure != "" {
		t.Fatalf("initial actions = %#v", actions)
	}
	actions = controller.observeConnections(connections, started.Add(discordRealtimeDialDeadline))
	if len(actions) != 1 || actions[0].failure == "" {
		t.Fatalf("deadline actions = %#v, want UDP failure", actions)
	}
	// A reported flow must not repeatedly consume strategy attempts on every poll.
	if actions = controller.observeConnections(connections, started.Add(discordRealtimeDialDeadline+time.Second)); len(actions) != 0 {
		t.Fatalf("repeated actions = %#v", actions)
	}
}

func TestDiscordRealtimeAcceptsBidirectionalUDP(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2000, 0)
	connection := clashConnection{
		ID: "voice-udp",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.20",
			DestinationPort: "55000",
			Process:         "Discord.exe",
		},
		Upload:   74,
		Download: 74,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(2*discordRealtimeDialDeadline)); len(actions) != 0 {
		t.Fatalf("bidirectional UDP was treated as failed: %#v", actions)
	}
}

func TestDiscordRealtimeDetectsPreviouslyHealthyFlowThatStalls(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2250, 0)
	connection := clashConnection{
		ID: "voice-udp",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.20",
			DestinationPort: "55000",
			Process:         "Discord.exe",
		},
		Upload:   200,
		Download: 200,
	}
	controller.observeConnections([]clashConnection{connection}, started)

	connection.Upload = 300
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeDialDeadline-time.Second)); len(actions) != 0 {
		t.Fatalf("healthy flow failed before the deadline: %#v", actions)
	}
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeDialDeadline)); len(actions) != 1 || actions[0].failure == "" {
		t.Fatalf("stalled healthy flow actions = %#v, want failure", actions)
	}
}

func TestDiscordRealtimeDoesNotFailSilentFlow(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2350, 0)
	connection := clashConnection{
		ID: "voice-udp",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.20",
			DestinationPort: "55000",
			Process:         "Discord.exe",
		},
		Upload:   200,
		Download: 200,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(2*discordRealtimeDialDeadline)); len(actions) != 0 {
		t.Fatalf("silent flow was treated as failed: %#v", actions)
	}
}

func TestDiscordRealtimeDetectsVoiceGatewayTCPFailure(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2500, 0)
	connection := clashConnection{
		ID: "voice-gateway",
		Metadata: clashConnectionMetadata{
			Network:         "tcp",
			Host:            "rotterdam-42.discord.media",
			DestinationIP:   "203.0.113.30",
			DestinationPort: "443",
			Process:         "Discord.exe",
		},
		Upload: 512,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeDialDeadline))
	if len(actions) != 1 || !strings.Contains(actions[0].failure, "TCP") {
		t.Fatalf("voice gateway actions = %#v, want TCP failure", actions)
	}
}

func TestDiscordRealtimeOutboundsSeparateDirectAndVPNNodes(t *testing.T) {
	template := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{
				"type":      "urltest",
				"tag":       "auto-select",
				"outbounds": []string{"node-a", "node-b"},
			},
		},
	}
	builder := &ConfigBuilderForStorage{}
	builder.addFreeAccessOutbounds(template, GlobalAppSettings{})
	outbounds := template["outbounds"].([]interface{})
	vpn := findOutboundMap(outbounds, discordVPNGroupTag)
	if vpn == nil || !sameStringSet(interfaceStringSlice(vpn["outbounds"]), []string{"node-a", "node-b"}) {
		t.Fatalf("Discord VPN selector = %#v", vpn)
	}
	realtime := findOutboundMap(outbounds, discordRealtimeGroupTag)
	if realtime == nil || realtime["default"] != "direct" || !sameStringSet(interfaceStringSlice(realtime["outbounds"]), []string{"direct", discordVPNGroupTag}) {
		t.Fatalf("Discord realtime selector = %#v", realtime)
	}
}

func findOutboundMap(outbounds []interface{}, tag string) map[string]interface{} {
	for _, raw := range outbounds {
		outbound, ok := raw.(map[string]interface{})
		if ok && outbound["tag"] == tag {
			return outbound
		}
	}
	return nil
}
