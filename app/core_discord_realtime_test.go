package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscordRealtimeProfilesAreBoundedAndUnique(t *testing.T) {
	if err := validateDiscordRealtimeProfiles(); err != nil {
		t.Fatal(err)
	}
	profiles := discordRealtimeProfiles()
	if got := len(profiles); got != discordRealtimeProfileCount {
		t.Fatalf("profile count = %d, want %d", got, discordRealtimeProfileCount)
	}
	if !strings.Contains(strings.Join(profiles[0].VoiceUDPArgs, " "), "repeats=2") {
		t.Fatalf("first profile must be the upstream Discord UDP baseline: %#v", profiles[0])
	}
	for _, profile := range profiles {
		joined := strings.Join(append(append([]string(nil), profile.VoiceTCPArgs...), profile.VoiceUDPArgs...), " ")
		for _, forbidden := range []string{"--payload=all", "--out-range=-d"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("encrypted Discord RTP must not be modified with %q: %#v", forbidden, profile)
			}
		}
	}
}

func TestDiscordRealtimeReusesBoundedVPNFallbackAfterLocalExhaustion(t *testing.T) {
	cachedFallback := serviceStrategyCacheEntry{
		MethodTag: FreeAccessMethodVPN,
		Source:    "fallback-vpn",
	}
	if !discordRealtimeShouldPreferVPN(FreeAccessMethodAuto, true, true, cachedFallback, true) {
		t.Fatal("automatic Discord ignored the cached VPN fallback after local strategies were exhausted")
	}
	transparent := serviceStrategyCacheEntry{MethodTag: rankedMethodsForService("discord")[0].Tag}
	if discordRealtimeShouldPreferVPN(FreeAccessMethodAuto, true, true, transparent, true) {
		t.Fatal("proven local Discord strategy unexpectedly started on VPN")
	}
	if discordRealtimeShouldPreferVPN(FreeAccessMethodVPN, true, false, cachedFallback, false) {
		t.Fatal("Discord selected VPN without an available VPN source")
	}
}

func TestDiscordSelectionKeepsStableUpstreamFiltersAfterLearningEndpoints(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.learnedPorts[32123] = time.Now()
	controller.learnedUDPPorts[19328] = time.Now()
	controller.learnedUDPIPs["203.0.113.20"] = time.Now()
	app := &App{discordRealtime: controller}
	selection := app.decorateDiscordRealtimeSelection(serviceWinwsSelection{
		ServiceTag:   "discord",
		HostlistPath: `C:\state\discord.txt`,
		Method:       methodMultisplit("m1", "M1", 652, 2, googleQUICPayload),
	})
	joined := strings.Join(composeServiceWinwsArgs([]serviceWinwsSelection{selection}, `C:\dropo\bin`), " ")
	expectedFilter := "--wf-raw-part=@" + filepath.Join(`C:\dropo\bin`, discordMediaRawFilter)
	if !strings.Contains(joined, expectedFilter) {
		t.Fatalf("bundled upstream Discord filter is not used: %s", joined)
	}
	for _, forbidden := range []string{"32123", "--filter-udp=19328", "--ipset-ip=203.0.113.20", "--payload=all", "--out-range=-d"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("learned endpoint triggered unstable encrypted-media interception %q: %s", forbidden, joined)
		}
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
	if len(actions) != 3 || actions[0].learnedPort != 32123 || actions[1].learnedUDPPort != 45678 || actions[1].learnedUDPIP != "203.0.113.20" || !actions[2].started || actions[0].failure != "" {
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
	initial := controller.observeConnections([]clashConnection{connection}, started)
	if len(initial) != 2 || initial[0].learnedUDPPort != 55000 || !initial[1].started {
		t.Fatalf("initial bidirectional actions = %#v, want port learning and loading start", initial)
	}
	for i := 1; i <= 4; i++ {
		connection.Upload += 100
		connection.Download += 200
		actions := controller.observeConnections([]clashConnection{connection}, started.Add(time.Duration(i)*2*time.Second))
		if i < 4 && len(actions) != 0 {
			t.Fatalf("media became healthy too early at poll %d: %#v", i, actions)
		}
		if i == 4 && (len(actions) != 1 || !actions[0].healthy) {
			t.Fatalf("sustained media actions = %#v, want healthy completion", actions)
		}
	}
}

func TestDiscordRealtimeDiagnosticsCaptureRouteAndCounterDeltas(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	controller.automatic = true
	controller.fallbackVPN = true
	started := time.Unix(2025, 0)
	connection := clashConnection{
		ID: "voice-udp-diagnostic",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "104.29.136.221",
			DestinationPort: "19314",
			Process:         "Discord.exe",
		},
		Chains:   []string{"node-a", discordVPNGroupTag, discordRealtimeGroupTag},
		Upload:   74,
		Download: 74,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	diagnostic, due := controller.collectDiagnostics(started)
	if !due || !diagnostic.FallbackVPN || len(diagnostic.NewFlows) != 1 || len(diagnostic.Flows) != 1 {
		t.Fatalf("initial diagnostic = %#v, due=%v", diagnostic, due)
	}
	line := formatDiscordFlowDiagnostic("opened", diagnostic.NewFlows[0])
	for _, expected := range []string{"process=Discord.exe", "destination=104.29.136.221:19314", "node-a -> discord-vpn -> discord-realtime", "total_up=74", "total_down=74"} {
		if !strings.Contains(line, expected) {
			t.Fatalf("diagnostic line is missing %q: %s", expected, line)
		}
	}

	connection.Upload += 100
	connection.Download += 200
	controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeDiagInterval))
	diagnostic, due = controller.collectDiagnostics(started.Add(discordRealtimeDiagInterval))
	if !due || len(diagnostic.NewFlows) != 0 || len(diagnostic.Flows) != 1 {
		t.Fatalf("follow-up diagnostic = %#v, due=%v", diagnostic, due)
	}
	flow := diagnostic.Flows[0]
	if flow.UploadDelta != 100 || flow.DownloadDelta != 200 || flow.InboundPolls != 1 {
		t.Fatalf("follow-up flow counters = %#v", flow)
	}
}

func TestDiscordRealtimeDoesNotTreatDiscoveryResponseAsMedia(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2050, 0)
	connection := clashConnection{
		ID: "voice-udp",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.20",
			DestinationPort: "19328",
			Process:         "Discord.exe",
		},
		Upload:   74,
		Download: 74,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeProvenDeadline-time.Second)); len(actions) != 0 {
		t.Fatalf("discovery-only flow produced a terminal action: %#v", actions)
	}
	if controller.initialReady {
		t.Fatal("a single discovery response was accepted as healthy media")
	}
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeProvenDeadline)); len(actions) != 1 || actions[0].failure == "" {
		t.Fatalf("discovery-only flow was not rejected after the proof deadline: %#v", actions)
	}
}

func TestDiscordRealtimeControlDeltasDoNotSuppressVoiceFailure(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2060, 0)
	failed := clashConnection{
		ID:       "voice-no-reply",
		Metadata: clashConnectionMetadata{Network: "udp", DestinationIP: "203.0.113.20", DestinationPort: "19310", Process: "Discord.exe"},
		Upload:   74,
	}
	control := clashConnection{
		ID:       "voice-control-only",
		Metadata: clashConnectionMetadata{Network: "udp", DestinationIP: "203.0.113.21", DestinationPort: "19311", Process: "Discord.exe"},
		Upload:   74, Download: 74,
	}
	controller.observeConnections([]clashConnection{failed, control}, started)
	for i := 1; i < 5; i++ {
		control.Upload += 50
		control.Download += 50
		if actions := controller.observeConnections([]clashConnection{failed, control}, started.Add(time.Duration(i)*2*time.Second)); len(actions) != 0 {
			t.Fatalf("unexpected early action at poll %d: %#v", i, actions)
		}
	}
	control.Upload += 50
	control.Download += 50
	actions := controller.observeConnections([]clashConnection{failed, control}, started.Add(discordRealtimeDialDeadline))
	if len(actions) != 1 || actions[0].failure == "" || actions[0].suppressed != "" {
		t.Fatalf("control-only inbound traffic suppressed a real failure: %#v", actions)
	}
}

func TestDiscordRealtimeIgnoresDiscordWebQUIC(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	connection := clashConnection{
		ID: "discord-quic",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.40",
			DestinationPort: "443",
			Process:         "Discord.exe",
		},
		Upload:   4096,
		Download: 8192,
	}
	if actions := controller.observeConnections([]clashConnection{connection}, time.Unix(2075, 0)); len(actions) != 0 {
		t.Fatalf("Discord web QUIC was treated as realtime media: %#v", actions)
	}
	if len(controller.learnedUDPPorts) != 0 {
		t.Fatalf("Discord web QUIC port was learned as media: %#v", controller.learnedUDPPorts)
	}
	if len(controller.learnedUDPIPs) != 0 {
		t.Fatalf("Discord web QUIC IP was learned as media: %#v", controller.learnedUDPIPs)
	}
}

func TestDiscordProcessRecognizesWindowsPathOnEveryHost(t *testing.T) {
	path := `C:\Users\client\AppData\Local\Discord\Discord.exe`
	if !isDiscordProcess("", path) {
		t.Fatalf("Windows Discord process path %q was not recognized", path)
	}
	if got := discordProcessLabel(clashConnectionMetadata{ProcessPath: path}); got != "Discord.exe" {
		t.Fatalf("Discord process label = %q, want Discord.exe", got)
	}
}

func TestDiscordRealtimeMissingVoiceFlowAdvancesAutomaticStrategy(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	controller.automatic = true
	started := time.Unix(2080, 0)
	connection := clashConnection{
		ID: "discord-public-control",
		Metadata: clashConnectionMetadata{
			Network:         "tcp",
			DestinationIP:   "203.0.113.40",
			DestinationPort: "443",
			ProcessPath:     `C:\Users\client\AppData\Local\Discord\Discord.exe`,
		},
		Upload: 256,
	}
	actions := controller.observeConnections([]clashConnection{connection}, started)
	if len(actions) != 1 || !actions[0].started {
		t.Fatalf("initial app activity actions = %#v, want bounded verification start", actions)
	}
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeNoFlowDeadline-time.Second)); len(actions) != 0 {
		t.Fatalf("missing-flow watchdog fired early: %#v", actions)
	}
	actions = controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeNoFlowDeadline))
	if len(actions) != 1 || !strings.Contains(actions[0].failure, "no voice flow") {
		t.Fatalf("missing-flow deadline actions = %#v, want strategy failure", actions)
	}
}

func TestDiscordRealtimeMissingVoiceFlowDoesNotRunWithoutAppActivity(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	controller.automatic = true
	started := time.Unix(2090, 0)
	if actions := controller.observeConnections(nil, started); len(actions) != 0 {
		t.Fatalf("idle monitor started strategy churn: %#v", actions)
	}
	if actions := controller.observeConnections(nil, started.Add(10*discordRealtimeNoFlowDeadline)); len(actions) != 0 {
		t.Fatalf("idle monitor failed a strategy without Discord activity: %#v", actions)
	}
}

func TestDiscordRealtimeLocalAttemptsStayBounded(t *testing.T) {
	if got := discordLocalStrategyCount(); got < 1 || got > discordRealtimeMaxLocalAttempts {
		t.Fatalf("local Discord strategy count = %d, want 1..%d", got, discordRealtimeMaxLocalAttempts)
	}
}

func TestDiscordRealtimeEndpointObservationDoesNotResetControllerState(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.learnedPorts[32123] = time.Now()
	controller.learnedUDPPorts[19328] = time.Now()
	controller.learnedUDPIPs["203.0.113.20"] = time.Now()
	app := &App{discordRealtime: controller}
	app.handleDiscordLearnedMedia(
		map[int]struct{}{32123: {}},
		map[int]struct{}{19328: {}},
		map[string]struct{}{"203.0.113.20": {}},
	)
	if len(controller.learnedPorts) != 1 || len(controller.learnedUDPPorts) != 1 || len(controller.learnedUDPIPs) != 1 {
		t.Fatalf("diagnostic endpoints were unexpectedly reset: tcp=%v udp=%v ips=%v", controller.learnedPorts, controller.learnedUDPPorts, controller.learnedUDPIPs)
	}
}

func TestDiscordRealtimePrunesStaleLearnedEndpoints(t *testing.T) {
	controller := newDiscordRealtimeController()
	stale := time.Now().Add(-discordRealtimeLearnedTTL - time.Minute)
	controller.learnedPorts[32123] = stale
	controller.learnedUDPPorts[19328] = stale
	controller.learnedUDPIPs["203.0.113.20"] = stale
	_, tcpPorts, udpPorts, udpIPs := controller.snapshot()
	if len(tcpPorts) != 0 || len(udpPorts) != 0 || len(udpIPs) != 0 {
		t.Fatalf("stale endpoints survived snapshot: tcp=%v udp=%v ips=%v", tcpPorts, udpPorts, udpIPs)
	}
}

func TestDiscordRealtimeInitialLoaderEndsWhenVoiceFlowDisappears(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2100, 0)
	connection := clashConnection{
		ID: "voice-udp",
		Metadata: clashConnectionMetadata{
			Network:         "udp",
			DestinationIP:   "203.0.113.20",
			DestinationPort: "55000",
			Process:         "Discord.exe",
		},
		Upload: 74,
	}
	if actions := controller.observeConnections([]clashConnection{connection}, started); len(actions) != 2 || actions[0].learnedUDPPort != 55000 || !actions[1].started {
		t.Fatalf("initial actions = %#v, want loader start", actions)
	}
	if actions := controller.observeConnections(nil, started.Add(time.Second)); len(actions) != 0 {
		t.Fatalf("loader ended before retry grace period: %#v", actions)
	}
	if actions := controller.observeConnections(nil, started.Add(time.Second+discordRealtimeFlowRetention)); len(actions) != 1 || !actions[0].cancelled {
		t.Fatalf("idle actions = %#v, want loader cancellation", actions)
	}
	// Cancellation is not success: a later voice attempt must start a fresh
	// initial check instead of silently trusting an unverified strategy.
	connection.ID = "voice-udp-retry"
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(2*discordRealtimeFlowRetention)); len(actions) != 1 || !actions[0].started {
		t.Fatalf("retry actions = %#v, want a new loader start", actions)
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
	for i := 1; i <= 4; i++ {
		connection.Upload += 200
		connection.Download += 300
		controller.observeConnections([]clashConnection{connection}, started.Add(time.Duration(i)*2*time.Second))
	}

	lastProgress := started.Add(8 * time.Second)
	connection.Upload += 1500
	if actions := controller.observeConnections([]clashConnection{connection}, lastProgress.Add(discordRealtimeProvenDeadline-time.Second)); len(actions) != 0 {
		t.Fatalf("healthy flow failed before the deadline: %#v", actions)
	}
	if actions := controller.observeConnections([]clashConnection{connection}, lastProgress.Add(discordRealtimeProvenDeadline)); len(actions) != 1 || actions[0].failure == "" {
		t.Fatalf("stalled healthy flow actions = %#v, want failure", actions)
	}
}

func TestDiscordRealtimeDoesNotFailIdleVoiceGatewayAfterHandshake(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2300, 0)
	connection := clashConnection{
		ID: "voice-gateway",
		Metadata: clashConnectionMetadata{
			Network:         "tcp",
			Host:            "arn-voice.discord.media",
			DestinationIP:   "203.0.113.30",
			DestinationPort: "2053",
			Process:         "Discord.exe",
		},
		Upload:   512,
		Download: 1024,
	}
	controller.observeConnections([]clashConnection{connection}, started)
	connection.Upload += 4096 // a heartbeat/control write; the gateway may stay quiet
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(2*discordRealtimeProvenDeadline)); len(actions) != 0 {
		t.Fatalf("idle voice WebSocket evicted the realtime route: %#v", actions)
	}
}

func TestDiscordRealtimeSuppressesIsolatedUDPFailureWhileMediaProgresses(t *testing.T) {
	controller := newDiscordRealtimeController()
	controller.running = true
	started := time.Unix(2325, 0)
	stale := clashConnection{
		ID:       "discovery-without-reply",
		Metadata: clashConnectionMetadata{Network: "udp", DestinationIP: "203.0.113.20", DestinationPort: "19304", Process: "Discord.exe"},
		Upload:   74,
	}
	media := clashConnection{
		ID:       "active-media",
		Metadata: clashConnectionMetadata{Network: "udp", DestinationIP: "203.0.113.20", DestinationPort: "19304", Process: "Discord.exe"},
		Upload:   74, Download: 74,
	}
	controller.observeConnections([]clashConnection{stale, media}, started)
	for i := 1; i <= 5; i++ {
		media.Upload += 200
		media.Download += 300
		actions := controller.observeConnections([]clashConnection{stale, media}, started.Add(time.Duration(i)*2*time.Second))
		if i == 5 {
			if len(actions) != 1 || actions[0].failure != "" || actions[0].suppressed == "" {
				t.Fatalf("isolated stale flow was not suppressed: %#v", actions)
			}
		}
	}
	lateStale := stale
	lateStale.ID = "late-discovery-without-reply"
	if actions := controller.observeConnections([]clashConnection{media, lateStale}, started.Add(30*time.Second)); len(actions) != 0 {
		t.Fatalf("late discovery setup produced actions: %#v", actions)
	}
	if actions := controller.observeConnections([]clashConnection{media, lateStale}, started.Add(40*time.Second)); len(actions) != 1 || actions[0].suppressed == "" {
		t.Fatalf("established media sibling did not suppress late isolated failure: %#v", actions)
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
	if actions := controller.observeConnections([]clashConnection{connection}, started.Add(discordRealtimeProvenDeadline-time.Second)); len(actions) != 0 {
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

func TestDiscordRealtimeOutboundsPreferNativeDirectBeforeVPNSources(t *testing.T) {
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

	manualTemplate := map[string]interface{}{
		"outbounds": []interface{}{
			map[string]interface{}{"type": "direct", "tag": "direct"},
			map[string]interface{}{"type": "urltest", "tag": "auto-select", "outbounds": []string{"node-a"}},
		},
	}
	settings := GlobalAppSettings{FreeAccessMethods: map[string]string{"discord": FreeAccessMethodDirect}}
	builder.addFreeAccessOutbounds(manualTemplate, settings)
	manualRealtime := findOutboundMap(manualTemplate["outbounds"].([]interface{}), discordRealtimeGroupTag)
	if manualRealtime == nil || manualRealtime["default"] != "direct" {
		t.Fatalf("manual direct Discord realtime selector = %#v", manualRealtime)
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
