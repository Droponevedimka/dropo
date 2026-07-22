package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	traffic "dropo/trafficorchestrator"
)

// TrafficProbeEndpoint is one TCP or UDP input for the strategy utility.
type TrafficProbeEndpoint struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Kind     string `json:"kind,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

// TrafficStrategyUtilityRequest requires one strategy to satisfy every web,
// TCP and UDP target. It never accepts scripts or command-line fragments.
type TrafficStrategyUtilityRequest struct {
	ServiceID         string                 `json:"serviceId"`
	Web               []string               `json:"web,omitempty"`
	TCP               []TrafficProbeEndpoint `json:"tcp,omitempty"`
	UDP               []TrafficProbeEndpoint `json:"udp,omitempty"`
	CandidateIDs      []string               `json:"candidateIds,omitempty"`
	Attempts          int                    `json:"attempts,omitempty"`
	RequiredSuccesses int                    `json:"requiredSuccesses,omitempty"`
}

// SelectTrafficStrategy tries bounded native strategies and commits only one
// that passes all required inputs. On failure the previous plan is restored and
// the caller can keep direct routing or use the VPN-source fallback chain.
func (a *App) SelectTrafficStrategy(input TrafficStrategyUtilityRequest) map[string]interface{} {
	if a == nil || a.trafficEngine == nil || a.trafficEngine.ActiveTag() == "" {
		return map[string]interface{}{"success": false, "error": "native traffic engine is not active", "fallback": "direct-or-vpn"}
	}
	service, ok := findFreeAccessService(strings.TrimSpace(input.ServiceID))
	if !ok {
		return map[string]interface{}{"success": false, "error": "unknown service", "fallback": "direct-or-vpn"}
	}
	targets, err := buildTrafficProbeTargets(input)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error(), "fallback": "direct-or-vpn"}
	}
	candidates, err := selectCandidateStrategies(input.CandidateIDs)
	if err != nil {
		return map[string]interface{}{"success": false, "error": err.Error(), "fallback": "direct-or-vpn"}
	}
	attempts := input.Attempts
	if attempts == 0 {
		attempts = 2
	}
	required := input.RequiredSuccesses
	if required == 0 {
		required = attempts
	}
	if !a.tryBeginRouteProbeDiscovery() {
		return map[string]interface{}{"success": false, "error": "another strategy selection is already running"}
	}
	defer a.finishRouteProbeDiscovery()

	previousRoute := a.currentServiceRoute(service.Tag)
	if previousRoute != "" && previousRoute != "direct" && !a.switchServiceRoute(service.Tag, "direct") {
		return map[string]interface{}{"success": false, "error": "failed to select direct route for validation"}
	}
	runner := nativeProbeRunner{}
	controller := nativeTrialController{manager: a.trafficEngine}
	selector, _ := traffic.NewSelector(runner, controller)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(candidates)*len(targets)*attempts+2)*7*time.Second)
	defer cancel()
	result, selectionErr := selector.Select(ctx, traffic.SelectionRequest{
		ServiceID: service.Tag, Targets: targets, Candidates: candidates,
		Attempts: attempts, RequiredSuccesses: required,
	})
	if selectionErr != nil {
		if previousRoute != "" && previousRoute != "direct" {
			_ = a.switchServiceRoute(service.Tag, previousRoute)
		}
		a.writeLog(fmt.Sprintf("[StrategySelector] %s: no common strategy for %d targets: %v", service.Tag, len(targets), selectionErr))
		return map[string]interface{}{
			"success": false, "error": selectionErr.Error(), "fallback": "direct-or-vpn",
			"service": service.Tag, "baseline": result.Baseline, "candidates": result.Candidates,
		}
	}
	methodTag := result.Strategy.ID
	for _, method := range rankedMethodsForService(service.Tag) {
		if method.NativeStrategyID == result.Strategy.ID {
			methodTag = method.Tag
			break
		}
	}
	a.cacheServiceMethod(service.Tag, methodTag, "native-multi-target-selector")
	a.writeLog(fmt.Sprintf("[StrategySelector] %s: committed %s for all %d target(s)", service.Tag, result.Strategy.ID, len(targets)))
	return map[string]interface{}{
		"success": true, "service": service.Tag, "strategy": result.Strategy.ID,
		"baseline": result.Baseline, "candidates": result.Candidates,
	}
}

func buildTrafficProbeTargets(input TrafficStrategyUtilityRequest) ([]traffic.ProbeTarget, error) {
	targets := make([]traffic.ProbeTarget, 0, len(input.Web)+len(input.TCP)+len(input.UDP))
	for index, rawURL := range input.Web {
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, fmt.Errorf("web target %d is not a valid HTTP(S) URL", index+1)
		}
		port := 443
		if parsed.Scheme == "http" {
			port = 80
		}
		if parsed.Port() != "" {
			if parsedPort, parseErr := net.LookupPort("tcp", parsed.Port()); parseErr == nil {
				port = parsedPort
			}
		}
		targets = append(targets, traffic.ProbeTarget{
			ID: fmt.Sprintf("web-%d", index+1), Network: traffic.NetworkTCP,
			Kind: traffic.ProbeHTTP, URL: parsed.String(), Port: port, TimeoutMS: 6000,
		})
	}
	for index, endpoint := range input.TCP {
		id := normalizedProbeID(endpoint.ID, "tcp", index)
		targets = append(targets, traffic.ProbeTarget{
			ID: id, Network: traffic.NetworkTCP, Kind: traffic.ProbeTCPConnect,
			Host: endpoint.Address, Port: endpoint.Port, TimeoutMS: 4000, Optional: endpoint.Optional,
		})
	}
	for index, endpoint := range input.UDP {
		kind := traffic.ProbeUDPExchange
		switch strings.ToLower(strings.TrimSpace(endpoint.Kind)) {
		case "", "exchange", "udp":
		case "stun":
			kind = traffic.ProbeSTUN
		case "discord", "discord-media":
			kind = traffic.ProbeDiscordMedia
		default:
			return nil, fmt.Errorf("UDP target %d has unsupported kind %q", index+1, endpoint.Kind)
		}
		targets = append(targets, traffic.ProbeTarget{
			ID: normalizedProbeID(endpoint.ID, "udp", index), Network: traffic.NetworkUDP,
			Kind: kind, Host: endpoint.Address, Port: endpoint.Port, TimeoutMS: 4000, Optional: endpoint.Optional,
		})
	}
	if len(targets) == 0 {
		return nil, errors.New("at least one web, TCP or UDP target is required")
	}
	for _, target := range targets {
		if err := traffic.ValidateProbeTarget(target); err != nil {
			return nil, fmt.Errorf("target %s: %w", target.ID, err)
		}
	}
	return targets, nil
}

func normalizedProbeID(value, prefix string, index int) string {
	value = safeFileComponent(value)
	if value == "traffic" {
		return fmt.Sprintf("%s-%d", prefix, index+1)
	}
	return value
}

func selectCandidateStrategies(ids []string) ([]traffic.TrafficStrategy, error) {
	available := traffic.BuiltinStrategies()
	if len(ids) == 0 {
		return available, nil
	}
	byID := make(map[string]traffic.TrafficStrategy, len(available))
	for _, strategy := range available {
		byID[strategy.ID] = strategy
	}
	selected := make([]traffic.TrafficStrategy, 0, len(ids))
	seen := map[string]struct{}{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		strategy, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown native strategy %q", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		selected = append(selected, strategy)
	}
	return selected, nil
}

type nativeTrialController struct{ manager *NativeTrafficManager }

func (controller nativeTrialController) BeginTrial(_ context.Context, serviceID string, strategy traffic.TrafficStrategy) (traffic.StrategyTrial, error) {
	if controller.manager == nil {
		return nil, errors.New("traffic engine is not initialized")
	}
	previous := controller.manager.CurrentPlan()
	trial := cloneTrafficPlan(previous)
	trial.Revision++
	found := false
	for index := range trial.Selections {
		if trial.Selections[index].ServiceID == serviceID {
			trial.Selections[index].StrategyID = strategy.ID
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("service %q is not present in the active traffic plan", serviceID)
	}
	if err := controller.manager.StartPlan(trial); err != nil {
		return nil, err
	}
	return &nativePlanTrial{manager: controller.manager, previous: previous, trialRevision: trial.Revision}, nil
}

type nativePlanTrial struct {
	manager       *NativeTrafficManager
	previous      traffic.TrafficPlan
	trialRevision uint64
	committed     bool
}

func (trial *nativePlanTrial) Commit() error {
	trial.committed = true
	return nil
}

func (trial *nativePlanTrial) Rollback() error {
	if trial.committed {
		return nil
	}
	trial.previous.Revision = trial.trialRevision + 1
	return trial.manager.StartPlan(trial.previous)
}

type nativeProbeRunner struct{}

func (nativeProbeRunner) Probe(ctx context.Context, target traffic.ProbeTarget) traffic.ProbeObservation {
	started := time.Now()
	timeout := time.Duration(target.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var err error
	switch target.Kind {
	case traffic.ProbeHTTP:
		err = probeHTTP(probeCtx, target.URL)
	case traffic.ProbeTCPConnect:
		err = probeTCP(probeCtx, target.Host, target.Port)
	case traffic.ProbeUDPExchange, traffic.ProbeSTUN, traffic.ProbeDiscordMedia:
		err = probeUDP(probeCtx, target)
	default:
		err = errors.New("unsupported probe kind")
	}
	if err == nil {
		return traffic.ProbeObservation{Success: true, Latency: time.Since(started)}
	}
	return traffic.ProbeObservation{Failure: classifyProbeFailure(err), Detail: err.Error(), Latency: time.Since(started)}
}

func probeHTTP(ctx context.Context, rawURL string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	client := *HTTPClient
	client.Timeout = 0
	client.CheckRedirect = func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode >= 500 {
		return fmt.Errorf("HTTP status %d", response.StatusCode)
	}
	return nil
}

func probeTCP(ctx context.Context, host string, port int) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return err
	}
	return connection.Close()
}

func probeUDP(ctx context.Context, target traffic.ProbeTarget) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "udp", net.JoinHostPort(target.Host, fmt.Sprint(target.Port)))
	if err != nil {
		return err
	}
	defer connection.Close()
	deadline, ok := ctx.Deadline()
	if ok {
		_ = connection.SetDeadline(deadline)
	}
	payload, transaction := udpProbePayload(target.Kind)
	if _, err := connection.Write(payload); err != nil {
		return err
	}
	response := make([]byte, 2048)
	read, err := connection.Read(response)
	if err != nil {
		return err
	}
	response = response[:read]
	switch target.Kind {
	case traffic.ProbeSTUN:
		if len(response) < 20 || binary.BigEndian.Uint32(response[4:8]) != 0x2112a442 || !strings.EqualFold(fmt.Sprintf("%x", response[8:20]), fmt.Sprintf("%x", transaction)) {
			return errors.New("invalid STUN response")
		}
	case traffic.ProbeDiscordMedia:
		if len(response) < 8 || binary.BigEndian.Uint16(response[:2]) != 2 {
			return errors.New("invalid Discord discovery response")
		}
	case traffic.ProbeUDPExchange:
		if len(response) == 0 {
			return errors.New("empty UDP response")
		}
	}
	return nil
}

func udpProbePayload(kind traffic.ProbeKind) ([]byte, []byte) {
	switch kind {
	case traffic.ProbeSTUN:
		payload := make([]byte, 20)
		binary.BigEndian.PutUint16(payload[:2], 1)
		binary.BigEndian.PutUint32(payload[4:8], 0x2112a442)
		_, _ = rand.Read(payload[8:20])
		return payload, append([]byte(nil), payload[8:20]...)
	case traffic.ProbeDiscordMedia:
		payload := make([]byte, 74)
		binary.BigEndian.PutUint16(payload[:2], 1)
		binary.BigEndian.PutUint16(payload[2:4], 70)
		_, _ = rand.Read(payload[4:8])
		return payload, nil
	default:
		payload := make([]byte, 16)
		_, _ = rand.Read(payload)
		return payload, nil
	}
}

func classifyProbeFailure(err error) traffic.FailureClass {
	if err == nil {
		return traffic.FailureNone
	}
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		return traffic.FailureDNS
	}
	var tlsError tls.RecordHeaderError
	if errors.As(err, &tlsError) || strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return traffic.FailureTLS
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return traffic.FailureTimeout
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return traffic.FailureTimeout
		}
		return traffic.FailureConnect
	}
	if strings.Contains(strings.ToLower(err.Error()), "reset") {
		return traffic.FailureReset
	}
	return traffic.FailureProtocol
}
