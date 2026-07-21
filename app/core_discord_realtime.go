package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	discordRealtimePollInterval   = 2 * time.Second
	discordRealtimeDialDeadline   = 10 * time.Second
	discordRealtimeMediaWarmup    = 6 * time.Second
	discordRealtimeSwitchCooldown = 5 * time.Second
	discordRealtimeFlowRetention  = 30 * time.Second
	discordRealtimeLearnedTTL     = 15 * time.Minute
	discordRealtimeDiagInterval   = 10 * time.Second
	discordRealtimeErrorInterval  = 30 * time.Second
	discordRealtimeMinMediaBytes  = 512
	discordRealtimeMinMediaPolls  = 3
	discordRealtimeMinUploadBytes = 64
	discordRealtimeStallBytes     = 64
)

type discordRealtimeController struct {
	mu sync.Mutex

	cancel          context.CancelFunc
	running         bool
	automatic       bool
	profileIndex    int
	attempt         int
	fallbackVPN     bool
	initialBusy     bool
	initialReady    bool
	initialIdle     time.Time
	vpnTried        map[string]bool
	lastSwitch      time.Time
	lastDiagnostics time.Time
	learnedPorts    map[int]time.Time
	learnedUDPPorts map[int]time.Time
	learnedUDPIPs   map[string]time.Time
	flows           map[string]*discordRealtimeFlow
}

type discordRealtimeFlow struct {
	ID               string
	Network          string
	Host             string
	DestinationIP    string
	DestinationPort  int
	Process          string
	Chains           []string
	FirstSeen        time.Time
	LastSeen         time.Time
	Upload           int64
	Download         int64
	WindowStarted    time.Time
	WindowUpload     int64
	WindowDownload   int64
	MediaUpload      int64
	MediaDownload    int64
	InboundPolls     int
	FirstInbound     time.Time
	Healthy          bool
	FailureReported  bool
	Announced        bool
	LastDiagUpload   int64
	LastDiagDownload int64
}

type clashConnectionsDocument struct {
	Connections []clashConnection `json:"connections"`
}

type clashConnection struct {
	ID       string                  `json:"id"`
	Metadata clashConnectionMetadata `json:"metadata"`
	Upload   int64                   `json:"upload"`
	Download int64                   `json:"download"`
	Chains   []string                `json:"chains"`
}

type clashConnectionMetadata struct {
	Network         string      `json:"network"`
	Host            string      `json:"host"`
	DestinationIP   string      `json:"destinationIP"`
	DestinationPort interface{} `json:"destinationPort"`
	Process         string      `json:"process"`
	ProcessPath     string      `json:"processPath"`
}

type discordRealtimeAction struct {
	learnedPort    int
	learnedUDPPort int
	learnedUDPIP   string
	failure        string
	connectionID   string
	started        bool
	healthy        bool
	cancelled      bool
	mediaUpload    int64
	mediaDownload  int64
	inboundPolls   int
}

type discordRealtimeFlowDiagnostic struct {
	ID              string
	Network         string
	Host            string
	DestinationIP   string
	DestinationPort int
	Process         string
	Chains          []string
	Age             time.Duration
	StalledFor      time.Duration
	Upload          int64
	Download        int64
	UploadDelta     int64
	DownloadDelta   int64
	MediaUpload     int64
	MediaDownload   int64
	InboundPolls    int
	Healthy         bool
	FailureReported bool
}

type discordRealtimeDiagnostic struct {
	Automatic    bool
	FallbackVPN  bool
	Attempt      int
	Profile      discordRealtimeProfile
	InitialBusy  bool
	InitialReady bool
	TCPPorts     []int
	UDPPorts     []int
	UDPIPs       []string
	NewFlows     []discordRealtimeFlowDiagnostic
	Flows        []discordRealtimeFlowDiagnostic
}

func newDiscordRealtimeController() *discordRealtimeController {
	return &discordRealtimeController{
		profileIndex:    0,
		attempt:         1,
		learnedPorts:    make(map[int]time.Time),
		learnedUDPPorts: make(map[int]time.Time),
		learnedUDPIPs:   make(map[string]time.Time),
		flows:           make(map[string]*discordRealtimeFlow),
	}
}

func (c *discordRealtimeController) resetLocked() {
	c.profileIndex = 0
	c.attempt = 1
	c.fallbackVPN = false
	c.initialBusy = false
	c.initialReady = false
	c.initialIdle = time.Time{}
	c.vpnTried = make(map[string]bool)
	c.lastSwitch = time.Time{}
	c.lastDiagnostics = time.Time{}
	c.learnedPorts = make(map[int]time.Time)
	c.learnedUDPPorts = make(map[int]time.Time)
	c.learnedUDPIPs = make(map[string]time.Time)
	c.flows = make(map[string]*discordRealtimeFlow)
}

func (c *discordRealtimeController) snapshot() (discordRealtimeProfile, []int, []int, []string) {
	if c == nil {
		return defaultDiscordRealtimeProfile(), nil, nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	profile, ok := discordRealtimeProfileAt(c.profileIndex)
	if !ok {
		profile = defaultDiscordRealtimeProfile()
	}
	cutoff := time.Now().Add(-discordRealtimeLearnedTTL)
	ports := make([]int, 0, len(c.learnedPorts))
	for port, seen := range c.learnedPorts {
		if seen.Before(cutoff) {
			delete(c.learnedPorts, port)
			continue
		}
		ports = append(ports, port)
	}
	sort.Ints(ports)
	udpPorts := make([]int, 0, len(c.learnedUDPPorts))
	for port, seen := range c.learnedUDPPorts {
		if seen.Before(cutoff) {
			delete(c.learnedUDPPorts, port)
			continue
		}
		udpPorts = append(udpPorts, port)
	}
	sort.Ints(udpPorts)
	udpIPs := make([]string, 0, len(c.learnedUDPIPs))
	for ip, seen := range c.learnedUDPIPs {
		if seen.Before(cutoff) {
			delete(c.learnedUDPIPs, ip)
			continue
		}
		udpIPs = append(udpIPs, ip)
	}
	sort.Strings(udpIPs)
	return profile, ports, udpPorts, udpIPs
}

func (a *App) decorateDiscordRealtimeSelection(selection serviceWinwsSelection) serviceWinwsSelection {
	if !strings.EqualFold(selection.ServiceTag, "discord") {
		return selection
	}
	profile, _, _, _ := a.discordRealtime.snapshot()
	selection.DiscordRealtime = profile
	return selection
}

func (a *App) startDiscordRealtimeMonitor() {
	if runtime.GOOS != "windows" || a.discordRealtime == nil || a.storage == nil {
		return
	}
	controller := a.discordRealtime
	controller.mu.Lock()
	if controller.cancel != nil {
		controller.cancel()
	}
	settings := a.storage.GetAppSettings()
	method := FreeAccessServiceMethod(settings, "discord")
	ctx, cancel := context.WithCancel(context.Background())
	controller.cancel = cancel
	controller.running = true
	automatic := controller.automatic
	controller.mu.Unlock()

	hasVPN := a.discordHasVPNFallback()
	preferVPN := hasVPN && (automatic || method == FreeAccessMethodVPN || !FreeMethodsAllowed(settings))
	target := "direct"
	if preferVPN {
		target = discordVPNGroupTag
	}
	selected := a.switchOutboundSelector(discordRealtimeGroupTag, target)
	if !selected && target == discordVPNGroupTag {
		target = "direct"
		selected = a.switchOutboundSelector(discordRealtimeGroupTag, target)
	}
	controller.mu.Lock()
	controller.fallbackVPN = selected && target == discordVPNGroupTag
	controller.mu.Unlock()

	realtimeCandidates, realtimeCurrent := a.selectorCandidates(discordRealtimeGroupTag)
	vpnCandidates, vpnCurrent := a.selectorCandidates(discordVPNGroupTag)
	profile := defaultDiscordRealtimeProfile()
	a.writeLog(fmt.Sprintf("[DiscordRealtime] monitor started: method=%s automatic=%v preferred=%s selected=%s switch_ok=%v profile=%s", method, automatic, map[bool]string{true: "vpn-first", false: "direct"}[preferVPN], realtimeCurrent, selected, profile.Tag))
	a.writeLog(fmt.Sprintf("[DiscordRealtime][Route] realtime candidates=%v current=%s; vpn candidates=%v current=%s; web/API remains on the Discord service route", realtimeCandidates, realtimeCurrent, vpnCandidates, vpnCurrent))
	go a.runDiscordRealtimeMonitor(ctx, controller)
}

func (a *App) prepareDiscordRealtimeSession() {
	if a.discordRealtime == nil || a.storage == nil {
		return
	}
	controller := a.discordRealtime
	controller.mu.Lock()
	controller.resetLocked()
	settings := a.storage.GetAppSettings()
	controller.automatic = FreeAccessServiceMethod(settings, "discord") == FreeAccessMethodAuto && FreeMethodsAllowed(settings)
	controller.mu.Unlock()
}

func (a *App) stopDiscordRealtimeMonitor() {
	controller := a.discordRealtime
	if controller == nil {
		return
	}
	controller.mu.Lock()
	if controller.cancel != nil {
		controller.cancel()
		controller.cancel = nil
	}
	controller.running = false
	controller.initialBusy = false
	controller.initialIdle = time.Time{}
	controller.mu.Unlock()
	a.endBusy(discordRealtimeBusyID)
}

func (a *App) runDiscordRealtimeMonitor(ctx context.Context, controller *discordRealtimeController) {
	ticker := time.NewTicker(discordRealtimePollInterval)
	defer ticker.Stop()
	var fetchFailures int
	var lastFetchErrorLog time.Time
	for {
		select {
		case <-ctx.Done():
			a.writeLog("[DiscordRealtime] monitor stopped")
			return
		case <-ticker.C:
			now := time.Now()
			document, err := a.fetchClashConnections()
			if err != nil {
				fetchFailures++
				if lastFetchErrorLog.IsZero() || now.Sub(lastFetchErrorLog) >= discordRealtimeErrorInterval {
					a.writeLog(fmt.Sprintf("[DiscordRealtime][Diagnostics] cannot read Clash connections (consecutive=%d): %v", fetchFailures, err))
					lastFetchErrorLog = now
				}
				continue
			}
			if fetchFailures > 0 {
				a.writeLog(fmt.Sprintf("[DiscordRealtime][Diagnostics] Clash connection polling recovered after %d failure(s)", fetchFailures))
				fetchFailures = 0
				lastFetchErrorLog = time.Time{}
			}
			actions := controller.observeConnections(document.Connections, now)
			learnedTCP := make(map[int]struct{})
			learnedUDP := make(map[int]struct{})
			learnedIPs := make(map[string]struct{})
			for _, action := range actions {
				if action.started {
					if controller.usingVPN() {
						a.updateBusy(discordRealtimeBusyID, "Проверяем Discord voice/video/Go Live через VPN...")
					} else {
						a.updateBusy(discordRealtimeBusyID, "Проверяем Discord voice/video/Go Live через прямой zapret2...")
					}
				}
				if action.learnedPort > 0 {
					learnedTCP[action.learnedPort] = struct{}{}
				}
				if action.learnedUDPPort > 0 {
					learnedUDP[action.learnedUDPPort] = struct{}{}
				}
				if action.learnedUDPIP != "" {
					learnedIPs[action.learnedUDPIP] = struct{}{}
				}
			}
			if len(learnedTCP) > 0 || len(learnedUDP) > 0 || len(learnedIPs) > 0 {
				a.handleDiscordLearnedMedia(learnedTCP, learnedUDP, learnedIPs)
			}
			for _, action := range actions {
				if action.healthy {
					a.writeLog(fmt.Sprintf("[DiscordRealtime] sustained bidirectional Discord media confirmed (upload=%d, download=%d, inbound_polls=%d); keeping the selected strategy", action.mediaUpload, action.mediaDownload, action.inboundPolls))
					a.endBusy(discordRealtimeBusyID)
				}
				if action.cancelled {
					a.writeLog("[DiscordRealtime] initial voice check ended because Discord no longer has an active UDP flow")
					a.endBusy(discordRealtimeBusyID)
				}
				if action.failure != "" {
					a.handleDiscordRealtimeFailure(action.failure)
				}
			}
			diagnostic, summaryDue := controller.collectDiagnostics(now)
			for _, flow := range diagnostic.NewFlows {
				a.writeLog(formatDiscordFlowDiagnostic("opened", flow))
			}
			if summaryDue {
				a.logDiscordRealtimeDiagnostic(diagnostic)
			}
		}
	}
}

func (c *discordRealtimeController) collectDiagnostics(now time.Time) (discordRealtimeDiagnostic, bool) {
	if c == nil {
		return discordRealtimeDiagnostic{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	profile, ok := discordRealtimeProfileAt(c.profileIndex)
	if !ok {
		profile = defaultDiscordRealtimeProfile()
	}
	result := discordRealtimeDiagnostic{
		Automatic:    c.automatic,
		FallbackVPN:  c.fallbackVPN,
		Attempt:      c.attempt,
		Profile:      profile,
		InitialBusy:  c.initialBusy,
		InitialReady: c.initialReady,
	}
	cutoff := now.Add(-discordRealtimeLearnedTTL)
	for port, seen := range c.learnedPorts {
		if seen.Before(cutoff) {
			delete(c.learnedPorts, port)
			continue
		}
		result.TCPPorts = append(result.TCPPorts, port)
	}
	for port, seen := range c.learnedUDPPorts {
		if seen.Before(cutoff) {
			delete(c.learnedUDPPorts, port)
			continue
		}
		result.UDPPorts = append(result.UDPPorts, port)
	}
	for ip, seen := range c.learnedUDPIPs {
		if seen.Before(cutoff) {
			delete(c.learnedUDPIPs, ip)
			continue
		}
		result.UDPIPs = append(result.UDPIPs, ip)
	}
	sort.Ints(result.TCPPorts)
	sort.Ints(result.UDPPorts)
	sort.Strings(result.UDPIPs)
	summaryDue := c.lastDiagnostics.IsZero() || now.Sub(c.lastDiagnostics) >= discordRealtimeDiagInterval
	flowIDs := make([]string, 0, len(c.flows))
	for id := range c.flows {
		flowIDs = append(flowIDs, id)
	}
	sort.Strings(flowIDs)
	for _, id := range flowIDs {
		flow := c.flows[id]
		if flow == nil {
			continue
		}
		diagnostic := discordRealtimeFlowDiagnostic{
			ID:              flow.ID,
			Network:         flow.Network,
			Host:            flow.Host,
			DestinationIP:   flow.DestinationIP,
			DestinationPort: flow.DestinationPort,
			Process:         flow.Process,
			Chains:          append([]string(nil), flow.Chains...),
			Age:             now.Sub(flow.FirstSeen),
			StalledFor:      now.Sub(flow.WindowStarted),
			Upload:          flow.Upload,
			Download:        flow.Download,
			UploadDelta:     discordCounterDelta(flow.Upload, flow.LastDiagUpload),
			DownloadDelta:   discordCounterDelta(flow.Download, flow.LastDiagDownload),
			MediaUpload:     flow.MediaUpload,
			MediaDownload:   flow.MediaDownload,
			InboundPolls:    flow.InboundPolls,
			Healthy:         flow.Healthy,
			FailureReported: flow.FailureReported,
		}
		if !flow.Announced {
			result.NewFlows = append(result.NewFlows, diagnostic)
			flow.Announced = true
		}
		if summaryDue {
			result.Flows = append(result.Flows, diagnostic)
			flow.LastDiagUpload = flow.Upload
			flow.LastDiagDownload = flow.Download
		}
	}
	if summaryDue {
		c.lastDiagnostics = now
	}
	return result, summaryDue
}

func discordCounterDelta(current, previous int64) int64 {
	if current < previous {
		return current
	}
	return current - previous
}

func formatDiscordFlowDiagnostic(event string, flow discordRealtimeFlowDiagnostic) string {
	host := flow.Host
	if host == "" {
		host = "-"
	}
	process := flow.Process
	if process == "" {
		process = "-"
	}
	chains := "direct/unknown"
	if len(flow.Chains) > 0 {
		chains = strings.Join(flow.Chains, " -> ")
	}
	destination := net.JoinHostPort(flow.DestinationIP, strconv.Itoa(flow.DestinationPort))
	return fmt.Sprintf("[DiscordRealtime][Flow] %s id=%s process=%s network=%s host=%s destination=%s chains=%s total_up=%d total_down=%d delta_up=%d delta_down=%d media_up=%d media_down=%d inbound_polls=%d age=%s stalled=%s healthy=%v failure_reported=%v", event, flow.ID, process, flow.Network, host, destination, chains, flow.Upload, flow.Download, flow.UploadDelta, flow.DownloadDelta, flow.MediaUpload, flow.MediaDownload, flow.InboundPolls, flow.Age.Round(time.Second), flow.StalledFor.Round(time.Second), flow.Healthy, flow.FailureReported)
}

func (a *App) logDiscordRealtimeDiagnostic(diagnostic discordRealtimeDiagnostic) {
	realtimeCandidates, realtimeCurrent := a.selectorCandidates(discordRealtimeGroupTag)
	vpnCandidates, vpnCurrent := a.selectorCandidates(discordVPNGroupTag)
	state := "idle"
	if diagnostic.InitialBusy {
		state = "checking"
	} else if diagnostic.InitialReady {
		state = "ready"
	}
	a.writeLog(fmt.Sprintf("[DiscordRealtime][Status] route=%s vpn_node=%s automatic=%v vpn_mode=%v state=%s attempt=%d/%d profile=%s active_flows=%d learned_tcp=%v learned_udp=%v learned_ips=%v realtime_candidates=%v vpn_candidates=%v", realtimeCurrent, vpnCurrent, diagnostic.Automatic, diagnostic.FallbackVPN, state, diagnostic.Attempt, discordRealtimeMaxTrials, diagnostic.Profile.Tag, len(diagnostic.Flows), diagnostic.TCPPorts, diagnostic.UDPPorts, diagnostic.UDPIPs, realtimeCandidates, vpnCandidates))
	if len(diagnostic.Flows) == 0 {
		a.writeLog("[DiscordRealtime][Flow] snapshot: no active Discord realtime flows")
		return
	}
	for _, flow := range diagnostic.Flows {
		a.writeLog(formatDiscordFlowDiagnostic("snapshot", flow))
	}
}

func (c *discordRealtimeController) currentAttempt() int {
	if c == nil {
		return 1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.attempt < 1 {
		return 1
	}
	return c.attempt
}

func (c *discordRealtimeController) usingVPN() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fallbackVPN
}

func (a *App) fetchClashConnections() (clashConnectionsDocument, error) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := a.clashAPIGet(client, "/connections")
	if err != nil {
		return clashConnectionsDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return clashConnectionsDocument{}, fmt.Errorf("Clash connections returned HTTP %d", resp.StatusCode)
	}
	body, err := readHTTPBodyLimited(resp.Body, defaultMaxHTTPResponseBytes)
	if err != nil {
		return clashConnectionsDocument{}, err
	}
	var document clashConnectionsDocument
	if err := json.Unmarshal(body, &document); err != nil {
		return clashConnectionsDocument{}, err
	}
	return document, nil
}

func (c *discordRealtimeController) observeConnections(connections []clashConnection, now time.Time) []discordRealtimeAction {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return nil
	}
	actions := make([]discordRealtimeAction, 0, 2)
	seen := make(map[string]struct{}, len(connections))
	activeDiscordUDP := false
	for _, connection := range connections {
		if connection.ID == "" || !isDiscordConnection(connection) {
			continue
		}
		network := strings.ToLower(strings.TrimSpace(connection.Metadata.Network))
		port := clashPort(connection.Metadata.DestinationPort)
		host := normalizeDiscordHost(connection.Metadata.Host)
		if network == "tcp" && isDiscordVoiceGateway(connection, host, port) {
			if port > 0 && port != 80 && port != 443 && !isDefaultDiscordTCPPort(port) {
				if _, exists := c.learnedPorts[port]; !exists {
					c.learnedPorts[port] = now
					actions = append(actions, discordRealtimeAction{learnedPort: port, connectionID: connection.ID})
				} else {
					c.learnedPorts[port] = now
				}
			}
			seen[connection.ID] = struct{}{}
			if failure, _ := c.observeDiscordFlow(connection, network, host, port, now); failure != "" {
				actions = append(actions, discordRealtimeAction{failure: failure, connectionID: connection.ID})
			}
			continue
		}
		if network != "udp" || !isDiscordMediaUDPConnection(connection, port) {
			continue
		}
		activeDiscordUDP = true
		c.initialIdle = time.Time{}
		if port > 0 {
			if _, exists := c.learnedUDPPorts[port]; !exists {
				c.learnedUDPPorts[port] = now
				actions = append(actions, discordRealtimeAction{learnedUDPPort: port, learnedUDPIP: connection.Metadata.DestinationIP, connectionID: connection.ID})
			} else {
				c.learnedUDPPorts[port] = now
			}
		}
		if ip := net.ParseIP(strings.TrimSpace(connection.Metadata.DestinationIP)); ip != nil {
			normalizedIP := ip.String()
			if _, exists := c.learnedUDPIPs[normalizedIP]; !exists {
				c.learnedUDPIPs[normalizedIP] = now
				if len(actions) == 0 || actions[len(actions)-1].learnedUDPPort != port {
					actions = append(actions, discordRealtimeAction{learnedUDPIP: normalizedIP, connectionID: connection.ID})
				} else {
					actions[len(actions)-1].learnedUDPIP = normalizedIP
				}
			} else {
				c.learnedUDPIPs[normalizedIP] = now
			}
		}
		if !c.initialReady && !c.initialBusy && connection.Upload >= 64 {
			c.initialBusy = true
			actions = append(actions, discordRealtimeAction{started: true, connectionID: connection.ID})
		}
		seen[connection.ID] = struct{}{}
		failure, healthy := c.observeDiscordFlow(connection, network, host, port, now)
		if failure != "" {
			actions = append(actions, discordRealtimeAction{failure: failure, connectionID: connection.ID})
		}
		if !c.initialReady && healthy {
			c.initialReady = true
			c.initialBusy = false
			c.initialIdle = time.Time{}
			flow := c.flows[connection.ID]
			action := discordRealtimeAction{healthy: true, connectionID: connection.ID}
			if flow != nil {
				action.mediaUpload = flow.MediaUpload
				action.mediaDownload = flow.MediaDownload
				action.inboundPolls = flow.InboundPolls
			}
			actions = append(actions, action)
		}
	}
	for id, flow := range c.flows {
		if _, ok := seen[id]; ok {
			continue
		}
		if now.Sub(flow.LastSeen) >= discordRealtimeFlowRetention {
			delete(c.flows, id)
		}
	}
	// Recomposition deliberately closes Discord connections between attempts.
	// Allow the client time to retry, but never leave the UI blocked forever if
	// the user leaves the voice channel while the initial check is in progress.
	if c.initialBusy && !activeDiscordUDP {
		if c.initialIdle.IsZero() {
			c.initialIdle = now
		} else if now.Sub(c.initialIdle) >= discordRealtimeFlowRetention {
			c.initialBusy = false
			c.initialIdle = time.Time{}
			actions = append(actions, discordRealtimeAction{cancelled: true})
		}
	}
	return actions
}

func (c *discordRealtimeController) observeDiscordFlow(connection clashConnection, network, host string, port int, now time.Time) (string, bool) {
	flow := c.flows[connection.ID]
	if flow == nil {
		flow = &discordRealtimeFlow{
			ID:              connection.ID,
			Network:         network,
			Host:            host,
			DestinationIP:   connection.Metadata.DestinationIP,
			DestinationPort: port,
			Process:         discordProcessLabel(connection.Metadata),
			Chains:          append([]string(nil), connection.Chains...),
			FirstSeen:       now,
			LastSeen:        now,
			Upload:          connection.Upload,
			Download:        connection.Download,
			WindowStarted:   now,
			WindowUpload:    connection.Upload,
			WindowDownload:  connection.Download,
		}
		if network == "tcp" && connection.Download == 0 {
			flow.WindowUpload = 0
		}
		c.flows[connection.ID] = flow
		return "", false
	}
	flow.Process = discordProcessLabel(connection.Metadata)
	flow.Chains = append(flow.Chains[:0], connection.Chains...)
	// Clash exposes cumulative byte counters. The first inbound Discord UDP
	// packet is commonly the 74-byte IP-discovery response, so it must never be
	// accepted as proof that encrypted RTP media works. Only repeated inbound
	// progress after the first observation contributes to media health.
	if connection.Upload < flow.Upload || connection.Download < flow.Download {
		flow.WindowStarted = now
		flow.WindowUpload = connection.Upload
		flow.WindowDownload = connection.Download
		flow.MediaUpload = 0
		flow.MediaDownload = 0
		flow.InboundPolls = 0
		flow.FirstInbound = time.Time{}
		flow.Healthy = false
		flow.FailureReported = false
	} else {
		uploadDelta := connection.Upload - flow.Upload
		downloadDelta := connection.Download - flow.Download
		if uploadDelta > 0 {
			flow.MediaUpload += uploadDelta
		}
		if downloadDelta > 0 {
			flow.MediaDownload += downloadDelta
			flow.InboundPolls++
			if flow.FirstInbound.IsZero() {
				flow.FirstInbound = now
			}
		}
	}
	if connection.Download > flow.WindowDownload {
		flow.WindowStarted = now
		flow.WindowUpload = connection.Upload
		flow.WindowDownload = connection.Download
		flow.FailureReported = false
	}
	flow.LastSeen = now
	flow.Upload = connection.Upload
	flow.Download = connection.Download
	if !flow.Healthy && flow.MediaUpload >= discordRealtimeMinUploadBytes &&
		flow.MediaDownload >= discordRealtimeMinMediaBytes &&
		flow.InboundPolls >= discordRealtimeMinMediaPolls &&
		!flow.FirstInbound.IsZero() && now.Sub(flow.FirstInbound) >= discordRealtimeMediaWarmup {
		flow.Healthy = true
	}
	sentWithoutReply := connection.Upload - flow.WindowUpload
	if !flow.FailureReported && network == "udp" && connection.Download == 0 && connection.Upload >= 64 && now.Sub(flow.FirstSeen) >= discordRealtimeDialDeadline {
		flow.FailureReported = true
		return fmt.Sprintf("UDP %s:%d did not receive the Discord discovery response within %s", flow.DestinationIP, flow.DestinationPort, discordRealtimeDialDeadline), flow.Healthy
	}
	if flow.FailureReported || sentWithoutReply < discordRealtimeStallBytes || now.Sub(flow.WindowStarted) < discordRealtimeDialDeadline {
		return "", flow.Healthy
	}
	flow.FailureReported = true
	return fmt.Sprintf("%s %s:%d sent %d media bytes without inbound progress for %s", strings.ToUpper(network), flow.DestinationIP, flow.DestinationPort, sentWithoutReply, discordRealtimeDialDeadline), flow.Healthy
}

func discordProcessLabel(metadata clashConnectionMetadata) string {
	if process := strings.TrimSpace(metadata.Process); process != "" {
		return process
	}
	return filepath.Base(strings.TrimSpace(metadata.ProcessPath))
}

func isDiscordConnection(connection clashConnection) bool {
	if isDiscordProcess(connection.Metadata.Process, connection.Metadata.ProcessPath) {
		return true
	}
	host := normalizeDiscordHost(connection.Metadata.Host)
	return host == "discord.media" || strings.HasSuffix(host, ".discord.media")
}

func isDiscordProcess(process, processPath string) bool {
	for _, value := range []string{process, filepath.Base(processPath)} {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "discord.exe", "discordcanary.exe", "discordptb.exe":
			return true
		}
	}
	return false
}

func isDiscordMediaUDPConnection(connection clashConnection, port int) bool {
	if !isDiscordProcess(connection.Metadata.Process, connection.Metadata.ProcessPath) || port <= 0 || port > 65535 {
		return false
	}
	// Discord web QUIC on UDP/443 is not a voice media flow. Treating it as one
	// would both produce false health results and broaden WinDivert capture for
	// unrelated HTTPS/3 traffic on the machine.
	switch port {
	case 53, 80, 123, 443:
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(connection.Metadata.DestinationIP))
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified()
}

func normalizeDiscordHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isDiscordVoiceGateway(connection clashConnection, host string, port int) bool {
	if host == "discord.media" || strings.HasSuffix(host, ".discord.media") {
		return true
	}
	if !isDiscordProcess(connection.Metadata.Process, connection.Metadata.ProcessPath) || port <= 0 || port == 80 || port == 443 {
		return false
	}
	ip := net.ParseIP(strings.TrimSpace(connection.Metadata.DestinationIP))
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback()
}

func clashPort(value interface{}) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		port, _ := strconv.Atoi(typed.String())
		return port
	case string:
		port, _ := strconv.Atoi(strings.TrimSpace(typed))
		return port
	case int:
		return typed
	default:
		return 0
	}
}

func isDefaultDiscordTCPPort(port int) bool {
	for _, candidate := range discordDefaultMediaTCPPorts {
		if candidate == port {
			return true
		}
	}
	return false
}

func (a *App) handleDiscordLearnedMedia(tcpPorts, udpPorts map[int]struct{}, udpIPs map[string]struct{}) {
	tcpValues := sortedIntSet(tcpPorts)
	udpValues := sortedIntSet(udpPorts)
	ipValues := sortedStringSet(udpIPs)
	classes := make([]string, 0, len(udpValues))
	for _, port := range udpValues {
		class := "observed-only"
		if port >= 19294 && port <= 19344 {
			class = "official-19294-19344"
		} else if port >= 50000 && port <= 50099 {
			class = "official-50000-50099"
		}
		classes = append(classes, fmt.Sprintf("%d:%s", port, class))
	}
	a.writeLog(fmt.Sprintf("[DiscordRealtime][Endpoint] learned tcp=%v udp=%v udp_class=%v ips=%v; observation only, winws2 is intentionally not restarted and encrypted RTP is never modified", tcpValues, udpValues, classes, ipValues))
}

func sortedIntSet(values map[int]struct{}) []int {
	result := make([]int, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Ints(result)
	return result
}

func sortedStringSet(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (a *App) handleDiscordRealtimeFailure(reason string) {
	controller := a.discordRealtime
	if controller == nil {
		return
	}
	controller.mu.Lock()
	if !controller.running || time.Since(controller.lastSwitch) < discordRealtimeSwitchCooldown {
		controller.mu.Unlock()
		return
	}
	if controller.fallbackVPN {
		controller.lastSwitch = time.Now()
		initialBusy := controller.initialBusy
		controller.mu.Unlock()
		if initialBusy {
			a.updateBusy(discordRealtimeBusyID, "Проверяем Discord voice через VPN...")
		}
		a.rotateDiscordVPNNode(reason)
		return
	}
	if !controller.automatic {
		controller.initialBusy = false
		controller.initialReady = true
		controller.initialIdle = time.Time{}
		controller.mu.Unlock()
		a.endBusy(discordRealtimeBusyID)
		a.writeLog("[DiscordRealtime] failure detected but automatic routing is disabled: " + reason)
		return
	}
	controller.lastSwitch = time.Now()
	controller.mu.Unlock()
	a.activateDiscordRealtimeFallback(reason)
}

func (a *App) activateDiscordRealtimeFallback(reason string) {
	controller := a.discordRealtime
	if controller == nil {
		return
	}
	if a.discordHasVPNFallback() {
		controller.mu.Lock()
		controller.fallbackVPN = true
		controller.vpnTried = make(map[string]bool)
		controller.flows = make(map[string]*discordRealtimeFlow)
		initialBusy := controller.initialBusy
		controller.mu.Unlock()
		if a.switchOutboundSelector(discordRealtimeGroupTag, discordVPNGroupTag) {
			if initialBusy {
				a.updateBusy(discordRealtimeBusyID, "Локальные методы не подошли, проверяем Discord voice через VPN...")
			}
			a.writeLog(fmt.Sprintf("[DiscordRealtime] all %d local attempts failed; switched voice/video/Go Live to VPN: %s", discordRealtimeMaxTrials, reason))
			a.closeDiscordRealtimeConnections()
			return
		}
	}
	a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
	controller.mu.Lock()
	controller.automatic = false
	controller.fallbackVPN = false
	controller.initialBusy = false
	controller.initialReady = true
	controller.initialIdle = time.Time{}
	controller.mu.Unlock()
	a.endBusy(discordRealtimeBusyID)
	a.writeLog(fmt.Sprintf("[DiscordRealtime] all %d local attempts failed and no usable subscription exists; degraded direct fallback selected", discordRealtimeMaxTrials))
}

func (a *App) discordHasVPNFallback() bool {
	if a.storage == nil {
		return false
	}
	config, err := readJSONConfig(a.storage.ActiveConfigFilePath())
	if err != nil {
		return false
	}
	outbounds, ok := config["outbounds"].([]interface{})
	return ok && outboundTagExists(outbounds, discordVPNGroupTag)
}

func (a *App) closeClashConnection(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("connection id is empty")
	}
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := a.newClashAPIRequest(http.MethodDelete, "/connections/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func (a *App) closeDiscordRealtimeConnections() {
	document, err := a.fetchClashConnections()
	if err != nil {
		a.writeLog(fmt.Sprintf("[DiscordRealtime][Reconnect] cannot enumerate connections: %v", err))
		return
	}
	attempted := 0
	closed := 0
	for _, connection := range document.Connections {
		if !isDiscordConnection(connection) {
			continue
		}
		network := strings.ToLower(strings.TrimSpace(connection.Metadata.Network))
		if network == "udp" || isDiscordVoiceGateway(connection, normalizeDiscordHost(connection.Metadata.Host), clashPort(connection.Metadata.DestinationPort)) {
			attempted++
			port := clashPort(connection.Metadata.DestinationPort)
			destination := net.JoinHostPort(connection.Metadata.DestinationIP, strconv.Itoa(port))
			if err := a.closeClashConnection(connection.ID); err != nil {
				a.writeLog(fmt.Sprintf("[DiscordRealtime][Reconnect] close failed id=%s network=%s destination=%s chains=%s: %v", connection.ID, network, destination, strings.Join(connection.Chains, " -> "), err))
				continue
			}
			closed++
			a.writeLog(fmt.Sprintf("[DiscordRealtime][Reconnect] closed id=%s network=%s destination=%s chains=%s", connection.ID, network, destination, strings.Join(connection.Chains, " -> ")))
		}
	}
	a.writeLog(fmt.Sprintf("[DiscordRealtime][Reconnect] completed: eligible=%d closed=%d failed=%d", attempted, closed, attempted-closed))
}

func (a *App) rotateDiscordVPNNode(reason string) {
	candidates, current := a.selectorCandidates(discordVPNGroupTag)
	if len(candidates) == 0 {
		a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
		a.finishDiscordRealtimeInitialGate()
		a.writeLog("[DiscordRealtime] VPN UDP failed and no alternative subscription node exists; switched to direct")
		a.closeDiscordRealtimeConnections()
		return
	}
	controller := a.discordRealtime
	controller.mu.Lock()
	if controller.vpnTried == nil {
		controller.vpnTried = make(map[string]bool)
	}
	if current != "" {
		controller.vpnTried[current] = true
	}
	next := ""
	for _, candidate := range candidates {
		if candidate != current && !controller.vpnTried[candidate] {
			next = candidate
			break
		}
	}
	controller.mu.Unlock()
	if next == "" || !a.switchOutboundSelector(discordVPNGroupTag, next) {
		a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
		controller.mu.Lock()
		controller.fallbackVPN = false
		controller.automatic = false
		controller.initialBusy = false
		controller.initialReady = true
		controller.initialIdle = time.Time{}
		controller.mu.Unlock()
		a.endBusy(discordRealtimeBusyID)
		a.writeLog("[DiscordRealtime] every available VPN node failed realtime UDP; switched to direct")
		a.closeDiscordRealtimeConnections()
		return
	}
	a.writeLog(fmt.Sprintf("[DiscordRealtime] VPN realtime failure (%s); rotated subscription node %s -> %s", reason, current, next))
	controller.mu.Lock()
	initialBusy := controller.initialBusy
	controller.flows = make(map[string]*discordRealtimeFlow)
	controller.mu.Unlock()
	if initialBusy {
		a.updateBusy(discordRealtimeBusyID, fmt.Sprintf("Проверяем следующий VPN-узел для Discord voice: %s", next))
	}
	a.closeDiscordRealtimeConnections()
}

func (a *App) finishDiscordRealtimeInitialGate() {
	controller := a.discordRealtime
	if controller != nil {
		controller.mu.Lock()
		controller.initialBusy = false
		controller.initialReady = true
		controller.initialIdle = time.Time{}
		controller.fallbackVPN = false
		controller.automatic = false
		controller.mu.Unlock()
	}
	a.endBusy(discordRealtimeBusyID)
}

func (a *App) selectorCandidates(groupTag string) ([]string, string) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := a.clashAPIGet(client, clashProxyAPIPath(groupTag))
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ""
	}
	body, err := readHTTPBodyLimited(resp.Body, defaultMaxHTTPResponseBytes)
	if err != nil {
		return nil, ""
	}
	var selector struct {
		All []string `json:"all"`
		Now string   `json:"now"`
	}
	if json.Unmarshal(body, &selector) != nil {
		return nil, ""
	}
	return selector.All, selector.Now
}
