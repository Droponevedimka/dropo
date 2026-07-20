package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
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
	discordRealtimeSwitchCooldown = 5 * time.Second
	discordRealtimeFlowRetention  = 30 * time.Second
	discordDynamicFilterFileName  = "discord_media_dynamic.txt"
)

const discordDynamicMediaFilter = `outbound and
udp.PayloadLength=74 and
udp.Payload32[0]=0x00010046 and
udp.Payload32[2]=0 and
udp.Payload32[3]=0 and
udp.Payload32[4]=0 and
udp.Payload32[5]=0 and
udp.Payload32[6]=0 and
udp.Payload32[7]=0 and
udp.Payload32[8]=0 and
udp.Payload32[9]=0 and
udp.Payload32[10]=0 and
udp.Payload32[11]=0 and
udp.Payload32[12]=0 and
udp.Payload32[13]=0 and
udp.Payload32[14]=0 and
udp.Payload32[15]=0 and
udp.Payload32[16]=0 and
udp.Payload32[17]=0
`

type discordRealtimeController struct {
	mu sync.Mutex

	cancel       context.CancelFunc
	running      bool
	automatic    bool
	profileIndex int
	attempt      int
	fallbackVPN  bool
	vpnTried     map[string]bool
	lastSwitch   time.Time
	learnedPorts map[int]time.Time
	flows        map[string]*discordRealtimeFlow
}

type discordRealtimeFlow struct {
	ID              string
	Network         string
	Host            string
	DestinationIP   string
	DestinationPort int
	FirstSeen       time.Time
	LastSeen        time.Time
	Upload          int64
	Download        int64
	WindowStarted   time.Time
	WindowUpload    int64
	WindowDownload  int64
	FailureReported bool
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
	learnedPort  int
	failure      string
	connectionID string
}

func newDiscordRealtimeController() *discordRealtimeController {
	return &discordRealtimeController{
		profileIndex: 0,
		attempt:      1,
		learnedPorts: make(map[int]time.Time),
		flows:        make(map[string]*discordRealtimeFlow),
	}
}

func (c *discordRealtimeController) resetLocked() {
	c.profileIndex = 0
	c.attempt = 1
	c.fallbackVPN = false
	c.vpnTried = make(map[string]bool)
	c.lastSwitch = time.Time{}
	c.learnedPorts = make(map[int]time.Time)
	c.flows = make(map[string]*discordRealtimeFlow)
}

func (c *discordRealtimeController) snapshot() (discordRealtimeProfile, []int) {
	if c == nil {
		return defaultDiscordRealtimeProfile(), nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	profile, ok := discordRealtimeProfileAt(c.profileIndex)
	if !ok {
		profile = defaultDiscordRealtimeProfile()
	}
	ports := make([]int, 0, len(c.learnedPorts))
	for port := range c.learnedPorts {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return profile, ports
}

func (a *App) decorateDiscordRealtimeSelection(selection serviceWinwsSelection) serviceWinwsSelection {
	if !strings.EqualFold(selection.ServiceTag, "discord") {
		return selection
	}
	profile, ports := a.discordRealtime.snapshot()
	selection.DiscordRealtime = profile
	selection.DiscordMediaTCPPorts = ports
	if path, err := a.ensureDiscordDynamicMediaFilter(); err == nil {
		selection.DiscordMediaRawFilter = path
	} else {
		a.writeLog(fmt.Sprintf("[DiscordRealtime] dynamic media filter unavailable, bundled fallback is used: %v", err))
	}
	return selection
}

func (a *App) ensureDiscordDynamicMediaFilter() (string, error) {
	if a == nil || (a.storage == nil && strings.TrimSpace(a.basePath) == "") {
		return "", fmt.Errorf("service state directory is unavailable")
	}
	dir := a.serviceHostlistDir()
	if dir == "" {
		return "", fmt.Errorf("service state directory is unavailable")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, discordDynamicFilterFileName)
	wanted := []byte(discordDynamicMediaFilter)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, wanted) {
		return path, nil
	}
	if err := atomicWriteFile(path, wanted, 0o600); err != nil {
		return "", err
	}
	return path, nil
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
	controller.mu.Unlock()

	if method == FreeAccessMethodVPN || !FreeMethodsAllowed(settings) {
		if a.discordHasVPNFallback() {
			controller.mu.Lock()
			controller.fallbackVPN = true
			controller.mu.Unlock()
			a.switchOutboundSelector(discordRealtimeGroupTag, discordVPNGroupTag)
		} else {
			a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
		}
	} else {
		a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
	}

	a.writeLog(fmt.Sprintf("[DiscordRealtime] monitor started (automatic=%v, max local attempts=%d)", controller.automatic, discordRealtimeMaxTrials))
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
	controller.mu.Unlock()
}

func (a *App) runDiscordRealtimeMonitor(ctx context.Context, controller *discordRealtimeController) {
	ticker := time.NewTicker(discordRealtimePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.writeLog("[DiscordRealtime] monitor stopped")
			return
		case <-ticker.C:
			document, err := a.fetchClashConnections()
			if err != nil {
				continue
			}
			actions := controller.observeConnections(document.Connections, time.Now())
			for _, action := range actions {
				if action.learnedPort > 0 {
					a.handleDiscordLearnedTCPPort(action.learnedPort, action.connectionID)
				}
				if action.failure != "" {
					a.handleDiscordRealtimeFailure(action.failure)
				}
			}
		}
	}
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
				}
			}
			seen[connection.ID] = struct{}{}
			if failure := c.observeDiscordFlow(connection, network, host, port, now); failure != "" {
				actions = append(actions, discordRealtimeAction{failure: failure, connectionID: connection.ID})
			}
			continue
		}
		if network != "udp" || !isDiscordProcess(connection.Metadata.Process, connection.Metadata.ProcessPath) {
			continue
		}
		seen[connection.ID] = struct{}{}
		if failure := c.observeDiscordFlow(connection, network, host, port, now); failure != "" {
			actions = append(actions, discordRealtimeAction{failure: failure, connectionID: connection.ID})
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
	return actions
}

func (c *discordRealtimeController) observeDiscordFlow(connection clashConnection, network, host string, port int, now time.Time) string {
	flow := c.flows[connection.ID]
	if flow == nil {
		flow = &discordRealtimeFlow{
			ID:              connection.ID,
			Network:         network,
			Host:            host,
			DestinationIP:   connection.Metadata.DestinationIP,
			DestinationPort: port,
			FirstSeen:       now,
			WindowStarted:   now,
		}
		c.flows[connection.ID] = flow
	}
	// Clash exposes cumulative counters. Reset the observation window whenever
	// inbound traffic progresses, or if the connection counters themselves were
	// reset. This detects both an initial one-way connection and a previously
	// healthy voice/stream flow that later stalls, without treating silence (no
	// outbound progress) as a failure.
	if connection.Upload < flow.Upload || connection.Download < flow.Download {
		flow.WindowStarted = now
		flow.WindowUpload = connection.Upload
		flow.WindowDownload = connection.Download
		flow.FailureReported = false
	} else if connection.Download > flow.WindowDownload {
		flow.WindowStarted = now
		flow.WindowUpload = connection.Upload
		flow.WindowDownload = connection.Download
		flow.FailureReported = false
	}
	flow.LastSeen = now
	flow.Upload = connection.Upload
	flow.Download = connection.Download
	sentWithoutReply := connection.Upload - flow.WindowUpload
	if flow.FailureReported || sentWithoutReply < 64 || now.Sub(flow.WindowStarted) < discordRealtimeDialDeadline {
		return ""
	}
	flow.FailureReported = true
	return fmt.Sprintf("%s %s:%d sent %d bytes without inbound progress for %s", strings.ToUpper(network), flow.DestinationIP, flow.DestinationPort, sentWithoutReply, discordRealtimeDialDeadline)
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

func (a *App) handleDiscordLearnedTCPPort(port int, connectionID string) {
	controller := a.discordRealtime
	if controller == nil {
		return
	}
	controller.mu.Lock()
	fallback := controller.fallbackVPN
	controller.mu.Unlock()
	a.writeLog(fmt.Sprintf("[DiscordRealtime] learned dynamic voice gateway TCP port %d", port))
	if fallback {
		return
	}
	if err := a.recomposeDiscordRealtimeEngine("learned voice TCP port " + strconv.Itoa(port)); err != nil {
		a.writeLog(fmt.Sprintf("[DiscordRealtime] failed to apply dynamic TCP port %d: %v", port, err))
		return
	}
	if connectionID != "" {
		a.closeClashConnection(connectionID)
	}
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
		controller.mu.Unlock()
		a.rotateDiscordVPNNode(reason)
		return
	}
	if !controller.automatic {
		controller.mu.Unlock()
		a.writeLog("[DiscordRealtime] failure detected but automatic routing is disabled: " + reason)
		return
	}
	if controller.attempt < discordRealtimeMaxTrials {
		controller.attempt++
		controller.profileIndex = controller.attempt - 1
		controller.lastSwitch = time.Now()
		attempt := controller.attempt
		profile, _ := discordRealtimeProfileAt(controller.profileIndex)
		controller.flows = make(map[string]*discordRealtimeFlow)
		controller.mu.Unlock()
		a.writeLog(fmt.Sprintf("[DiscordRealtime] attempt %d/%d: %s; previous failure: %s", attempt, discordRealtimeMaxTrials, profile.Label, reason))
		if err := a.recomposeDiscordRealtimeEngine(fmt.Sprintf("attempt %d", attempt)); err != nil {
			a.writeLog(fmt.Sprintf("[DiscordRealtime] attempt %d failed to start: %v", attempt, err))
		}
		a.closeDiscordRealtimeConnections()
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
		controller.mu.Unlock()
		if a.switchOutboundSelector(discordRealtimeGroupTag, discordVPNGroupTag) {
			a.writeLog(fmt.Sprintf("[DiscordRealtime] all %d local attempts failed; switched voice/video/Go Live to VPN: %s", discordRealtimeMaxTrials, reason))
			a.closeDiscordRealtimeConnections()
			return
		}
	}
	a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
	controller.mu.Lock()
	controller.automatic = false
	controller.fallbackVPN = false
	controller.mu.Unlock()
	a.writeLog(fmt.Sprintf("[DiscordRealtime] all %d local attempts failed and no usable subscription exists; degraded direct fallback selected", discordRealtimeMaxTrials))
}

func (a *App) discordHasVPNFallback() bool {
	if a.storage == nil {
		return false
	}
	configPath := a.storage.ActiveConfigFilePath()
	hasVPN, err := configHasVPNProbeCandidates(configPath)
	return err == nil && hasVPN
}

func (a *App) recomposeDiscordRealtimeEngine(reason string) error {
	if a.zapret == nil || a.zapret.ActiveTag() != composedStrategyTag || a.storage == nil {
		return fmt.Errorf("composed winws2 engine is not active")
	}
	dir := a.serviceHostlistDir()
	cache := a.loadServiceStrategyCache()
	if entry, ok := cache["discord"]; ok && isFreeAccessFallbackTag(entry.MethodTag) {
		delete(cache, "discord")
	}
	selections, _ := a.resolveServiceSelections(dir, cache)
	if _, ok := selections["discord"]; !ok {
		service, found := findFreeAccessService("discord")
		ranked := rankedMethodsForService("discord")
		if !found || len(ranked) == 0 {
			return fmt.Errorf("Discord transparent method is unavailable")
		}
		hostlist, err := ensureServiceHostlist(dir, service)
		if err != nil {
			return err
		}
		selections["discord"] = serviceWinwsSelection{ServiceTag: "discord", HostlistPath: hostlist, Method: ranked[0]}
	}
	if err := a.composeAndStartServiceEngine(selections); err != nil {
		return err
	}
	a.writeLog("[DiscordRealtime] winws2 realtime profile applied: " + reason)
	return nil
}

func (a *App) closeClashConnection(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := a.newClashAPIRequest(http.MethodDelete, "/connections/"+id, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func (a *App) closeDiscordRealtimeConnections() {
	document, err := a.fetchClashConnections()
	if err != nil {
		return
	}
	for _, connection := range document.Connections {
		if !isDiscordConnection(connection) {
			continue
		}
		network := strings.ToLower(strings.TrimSpace(connection.Metadata.Network))
		if network == "udp" || isDiscordVoiceGateway(connection, normalizeDiscordHost(connection.Metadata.Host), clashPort(connection.Metadata.DestinationPort)) {
			a.closeClashConnection(connection.ID)
		}
	}
}

func (a *App) rotateDiscordVPNNode(reason string) {
	candidates, current := a.selectorCandidates(discordVPNGroupTag)
	if len(candidates) == 0 {
		a.switchOutboundSelector(discordRealtimeGroupTag, "direct")
		a.writeLog("[DiscordRealtime] VPN UDP failed and no alternative subscription node exists; switched to direct")
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
		controller.mu.Unlock()
		a.writeLog("[DiscordRealtime] every available VPN node failed realtime UDP; switched to direct")
		return
	}
	a.writeLog(fmt.Sprintf("[DiscordRealtime] VPN realtime failure (%s); rotated subscription node %s -> %s", reason, current, next))
	controller.mu.Lock()
	controller.flows = make(map[string]*discordRealtimeFlow)
	controller.mu.Unlock()
	a.closeDiscordRealtimeConnections()
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
