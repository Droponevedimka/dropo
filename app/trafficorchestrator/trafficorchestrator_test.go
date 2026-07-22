package trafficorchestrator

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"sync"
	"testing"
	"time"
)

func testStrategy(id string, risk int) TrafficStrategy {
	return TrafficStrategy{
		ID:          id,
		Revision:    1,
		Label:       id,
		TCP:         []PacketAction{{Kind: ActionFake, Payload: "tls_client_hello", Repeats: 2}, {Kind: ActionSplit, Position: 2}},
		UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_initial", Repeats: 2}},
		Constraints: StrategyConstraints{Networks: []Network{NetworkTCP, NetworkUDP}, IPv4: true, IPv6: true, MaxFlowData: 64 * 1024},
		Cost:        StrategyCost{SyntheticPackets: 2, BufferedBytes: 4096, Risk: risk},
	}
}

func testPlan() TrafficPlan {
	return TrafficPlan{
		Revision:        1,
		CatalogRevision: "test-1",
		Strategies:      []TrafficStrategy{testStrategy("safe", 1), testStrategy("strong", 3)},
		Services: []ServiceRule{{
			ID:                   "discord",
			DisplayName:          "Discord",
			DomainSuffixes:       []string{"discord.com", "discord.media"},
			IPCIDRs:              []string{"66.22.192.0/18"},
			ProcessNames:         []string{"Discord.exe"},
			TCPPorts:             []int{443},
			UDPPorts:             []int{3478, 50000},
			Fingerprints:         []string{"discord-media", "stun"},
			CandidateStrategyIDs: []string{"safe", "strong"},
			ProbeTargets: []ProbeTarget{
				{ID: "web", Network: NetworkTCP, Kind: ProbeHTTP, URL: "https://discord.com/api/v10/gateway", Port: 443},
				{ID: "media", Network: NetworkUDP, Kind: ProbeDiscordMedia, Host: "66.22.200.1", Port: 50000},
			},
			AllowVPNFallback: true,
		}},
		Selections: []ServiceSelection{{ServiceID: "discord", StrategyID: "safe"}},
	}
}

func TestValidatePlanAndClassifier(t *testing.T) {
	plan := testPlan()
	if err := ValidatePlan(plan); err != nil {
		t.Fatalf("ValidatePlan() error = %v", err)
	}
	classifier, err := NewClassifier(plan)
	if err != nil {
		t.Fatalf("NewClassifier() error = %v", err)
	}

	web := classifier.Classify(FlowEvidence{Network: NetworkTCP, Destination: "1.1.1.1", Port: 443, Host: "cdn.discord.com", ProcessName: `C:\\Users\\u\\Discord.exe`})
	if !web.Matched || web.ServiceID != "discord" {
		t.Fatalf("web classification = %+v", web)
	}
	media := classifier.Classify(FlowEvidence{Network: NetworkUDP, Destination: "66.22.200.1", Port: 50000, Fingerprints: []string{"discord-media"}})
	if !media.Matched || media.ServiceID != "discord" {
		t.Fatalf("media classification = %+v", media)
	}
	processOnly := classifier.Classify(FlowEvidence{Network: NetworkTCP, Destination: "8.8.8.8", Port: 443, ProcessName: "Discord.exe"})
	if processOnly.Matched {
		t.Fatalf("process-only classification must fail safe: %+v", processOnly)
	}
	wrongPort := classifier.Classify(FlowEvidence{Network: NetworkTCP, Destination: "66.22.200.1", Port: 80, Host: "discord.com"})
	if wrongPort.Matched {
		t.Fatalf("wrong-port classification must fail safe: %+v", wrongPort)
	}
}

func TestProcessorSelectionOnlyRevisionReusesImmutableClassifier(t *testing.T) {
	plan := testPlan()
	processor, err := NewProcessor(plan)
	if err != nil {
		t.Fatal(err)
	}
	before := processor.snapshot.Load().classifier
	plan.Revision++
	plan.Selections[0].StrategyID = "strong"
	if err := processor.ApplyPlan(plan); err != nil {
		t.Fatal(err)
	}
	if after := processor.snapshot.Load().classifier; after != before {
		t.Fatal("selection-only revision rebuilt the immutable classifier")
	}

	plan.Revision++
	plan.Selections[0].StrategyID = "missing"
	if err := processor.ApplyPlan(plan); err == nil {
		t.Fatal("selection-only revision accepted an unknown strategy")
	}
}

func TestWorkNetworkWinsBeforeBlockedService(t *testing.T) {
	plan := testPlan()
	plan.WorkNetworks = []WorkNetworkRule{{ID: "corporate", DomainSuffixes: []string{"discord.com"}, IPCIDRs: []string{"66.22.192.0/18"}}}
	classifier, err := NewClassifier(plan)
	if err != nil {
		t.Fatal(err)
	}
	classification := classifier.Classify(FlowEvidence{Network: NetworkTCP, Destination: "66.22.200.1", Port: 443, Host: "discord.com"})
	if !classification.WorkNetwork || classification.Matched || classification.WorkNetworkID != "corporate" {
		t.Fatalf("classification = %+v", classification)
	}
}

func TestValidateStrategyRejectsUnboundedActions(t *testing.T) {
	strategy := testStrategy("bad", 1)
	strategy.TCP[0].Repeats = 1000
	if err := ValidateStrategy(strategy); err == nil {
		t.Fatal("expected unbounded repeats to be rejected")
	}
	strategy = testStrategy("bad-udp", 1)
	strategy.UDP = []PacketAction{{Kind: ActionSplit, Position: 2}}
	if err := ValidateStrategy(strategy); err == nil {
		t.Fatal("expected UDP split to be rejected")
	}
}

type fakeSelectorRuntime struct {
	active    string
	committed string
	results   map[string]map[string]bool
}

func (r *fakeSelectorRuntime) Probe(_ context.Context, target ProbeTarget) ProbeObservation {
	success := r.results[r.active][target.ID]
	if success {
		return ProbeObservation{Success: true, Latency: 5 * time.Millisecond}
	}
	return ProbeObservation{Failure: FailureTimeout}
}

func (r *fakeSelectorRuntime) BeginTrial(_ context.Context, _ string, strategy TrafficStrategy) (StrategyTrial, error) {
	previous := r.active
	r.active = strategy.ID
	return &fakeTrial{runtime: r, previous: previous, strategy: strategy.ID}, nil
}

type fakeTrial struct {
	runtime  *fakeSelectorRuntime
	previous string
	strategy string
}

func (t *fakeTrial) Commit() error {
	t.runtime.committed = t.strategy
	return nil
}

func (t *fakeTrial) Rollback() error {
	t.runtime.active = t.previous
	return nil
}

func selectorRequest() SelectionRequest {
	return SelectionRequest{
		ServiceID: "discord",
		Targets: []ProbeTarget{
			{ID: "web", Network: NetworkTCP, Kind: ProbeHTTP, URL: "https://discord.com/api/v10/gateway", Port: 443},
			{ID: "media", Network: NetworkUDP, Kind: ProbeDiscordMedia, Host: "66.22.200.1", Port: 50000},
		},
		Candidates:        []TrafficStrategy{testStrategy("partial", 1), testStrategy("common", 2)},
		Attempts:          3,
		RequiredSuccesses: 2,
	}
}

func TestSelectorRequiresOneStrategyForEveryTarget(t *testing.T) {
	runtime := &fakeSelectorRuntime{results: map[string]map[string]bool{
		"":        {"web": false, "media": false},
		"partial": {"web": true, "media": false},
		"common":  {"web": true, "media": true},
	}}
	selector, err := NewSelector(runtime, runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := selector.Select(context.Background(), selectorRequest())
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if result.Strategy.ID != "common" || runtime.committed != "common" {
		t.Fatalf("selected=%q committed=%q", result.Strategy.ID, runtime.committed)
	}
	if len(result.Candidates) != 2 || result.Candidates[0].Passed || !result.Candidates[1].Passed {
		t.Fatalf("candidate results = %+v", result.Candidates)
	}
}

func TestSelectorReturnsTypedErrorAndRestoresDirect(t *testing.T) {
	runtime := &fakeSelectorRuntime{results: map[string]map[string]bool{
		"":        {"web": true, "media": false},
		"partial": {"web": true, "media": false},
		"common":  {"web": false, "media": true},
	}}
	selector, _ := NewSelector(runtime, runtime)
	_, err := selector.Select(context.Background(), selectorRequest())
	if !errors.Is(err, ErrNoCommonStrategy) {
		t.Fatalf("Select() error = %v, want ErrNoCommonStrategy", err)
	}
	if runtime.active != "" || runtime.committed != "" {
		t.Fatalf("failed selection leaked trial state: active=%q committed=%q", runtime.active, runtime.committed)
	}
}

func TestSelectorRejectsRegressionOfBaselineWorkingOptionalTarget(t *testing.T) {
	request := selectorRequest()
	request.Targets = append(request.Targets, ProbeTarget{
		ID: "already-working", Network: NetworkTCP, Kind: ProbeTCPConnect,
		Host: "example.com", Port: 443, Optional: true,
	})
	request.Candidates = []TrafficStrategy{testStrategy("regresses", 1)}
	runtime := &fakeSelectorRuntime{results: map[string]map[string]bool{
		"":          {"web": false, "media": false, "already-working": true},
		"regresses": {"web": true, "media": true, "already-working": false},
	}}
	selector, _ := NewSelector(runtime, runtime)
	_, err := selector.Select(context.Background(), request)
	if !errors.Is(err, ErrNoCommonStrategy) {
		t.Fatalf("Select() error = %v, want ErrNoCommonStrategy", err)
	}
	if runtime.active != "" || runtime.committed != "" {
		t.Fatalf("regressing trial leaked state: active=%q committed=%q", runtime.active, runtime.committed)
	}
}

func TestPacketProcessorClassifiesAndSplitsTLS(t *testing.T) {
	plan := testPlan()
	processor, err := NewProcessor(plan)
	if err != nil {
		t.Fatal(err)
	}
	packet := testIPv4TCPPacket(t, "discord.com")
	decision := processor.Process(packet)
	if decision.ServiceID != "discord" || decision.StrategyID != "safe" || !decision.Transformed {
		t.Fatalf("decision = %+v", decision)
	}
	if len(decision.Packets) < 3 { // fake packets + two original segments
		t.Fatalf("packet count = %d, want at least 3", len(decision.Packets))
	}
	for index, output := range decision.Packets {
		if _, err := parsePacket(output); err != nil {
			t.Fatalf("output %d is malformed: %v", index, err)
		}
	}
	counters := processor.Counters()["discord"]
	if counters.Matched != 1 || counters.Transformed != 1 || counters.Errors != 0 {
		t.Fatalf("counters = %+v", counters)
	}
}

func TestPacketProcessorFailsSafeForMalformedAndUnclassified(t *testing.T) {
	processor, err := NewProcessor(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	malformed := []byte{0x45, 0, 0}
	decision := processor.Process(malformed)
	if decision.Transformed || len(decision.Packets) != 1 {
		t.Fatalf("malformed decision = %+v", decision)
	}
	packet := testIPv4TCPPacket(t, "example.com")
	decision = processor.Process(packet)
	if decision.Transformed || decision.ServiceID != "" {
		t.Fatalf("unclassified decision = %+v", decision)
	}
}

func TestInvalidChecksumDecoyRemainsInvalidAfterProcessing(t *testing.T) {
	strategies := BuiltinStrategies()
	var selected TrafficStrategy
	for _, strategy := range strategies {
		if strategy.ID == "native-decoy-split" {
			selected = strategy
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("native decoy strategy is missing")
	}
	plan := testPlan()
	plan.Strategies = []TrafficStrategy{selected}
	plan.Services[0].CandidateStrategyIDs = []string{selected.ID}
	plan.Selections[0].StrategyID = selected.ID
	processor, err := NewProcessor(plan)
	if err != nil {
		t.Fatal(err)
	}
	decision := processor.Process(testIPv4TCPPacket(t, "discord.com"))
	if !decision.Transformed || len(decision.Packets) < 6 {
		t.Fatalf("unexpected decision: %+v", decision)
	}
	for index := 0; index < 4; index++ {
		decoy := decision.Packets[index]
		valid := append([]byte(nil), decoy...)
		calculateChecksums(valid)
		if string(valid) == string(decoy) {
			t.Fatalf("decoy %d checksum was accidentally repaired", index)
		}
	}
	for index := len(decision.Packets) - 2; index < len(decision.Packets); index++ {
		segment := decision.Packets[index]
		valid := append([]byte(nil), segment...)
		calculateChecksums(valid)
		if string(valid) != string(segment) {
			t.Fatalf("real segment %d has an invalid checksum", index)
		}
	}
}

func TestProcessorDoesNotTransformUnrecognizedEncryptedMedia(t *testing.T) {
	strategies := BuiltinStrategies()
	plan := TrafficPlan{
		Revision: 1, CatalogRevision: BuiltinCatalogRevision,
		Strategies: strategies,
		Services: []ServiceRule{{
			ID: "discord", DisplayName: "Discord", IPCIDRs: []string{"66.22.192.0/18"},
			UDPPorts: []int{50000}, CandidateStrategyIDs: []string{strategies[0].ID},
		}},
		Selections: []ServiceSelection{{ServiceID: "discord", StrategyID: strategies[0].ID}},
	}
	processor, err := NewProcessor(plan)
	if err != nil {
		t.Fatal(err)
	}
	packet := testIPv4UDPPacket("66.22.200.1", 50000, []byte("opaque encrypted media"))
	decision := processor.Process(packet)
	if decision.Transformed || len(decision.Packets) != 1 {
		t.Fatalf("opaque media must pass unchanged: %+v", decision)
	}
}

func TestProcessorRejectsNonMonotonicPlanRevision(t *testing.T) {
	processor, err := NewProcessor(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.ApplyPlan(testPlan()); err == nil {
		t.Fatal("expected equal plan revision to be rejected")
	}
	next := testPlan()
	next.Revision = 2
	if err := processor.ApplyPlan(next); err != nil {
		t.Fatalf("ApplyPlan(next) error = %v", err)
	}
	if processor.Revision() != 2 {
		t.Fatalf("revision = %d", processor.Revision())
	}
}

func TestUDPProtocolFingerprints(t *testing.T) {
	stun := make([]byte, 20)
	stun[4], stun[5], stun[6], stun[7] = 0x21, 0x12, 0xa4, 0x42
	if !isSTUN(stun) {
		t.Fatal("valid STUN header was not recognized")
	}
	discord := make([]byte, 74)
	discord[1], discord[3] = 1, 70
	if !isDiscordDiscovery(discord) {
		t.Fatal("valid Discord discovery request was not recognized")
	}
	wireguard := make([]byte, 148)
	wireguard[0] = 1
	if got := wireGuardFingerprint(wireguard); got != "wireguard-initiation" {
		t.Fatalf("wireguard fingerprint = %q", got)
	}
}

type backendPacket struct {
	data    []byte
	address PacketAddress
}

type fakePacketBackend struct {
	mu       sync.Mutex
	input    []backendPacket
	output   [][]byte
	closed   bool
	received chan struct{}
}

func (b *fakePacketBackend) Receive(buffer []byte) (int, PacketAddress, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, PacketAddress{}, ErrBackendClosed
	}
	if len(b.input) == 0 {
		return 0, PacketAddress{}, io.EOF
	}
	packet := b.input[0]
	b.input = b.input[1:]
	copy(buffer, packet.data)
	if b.received != nil {
		close(b.received)
		b.received = nil
	}
	return len(packet.data), packet.address, nil
}

func (b *fakePacketBackend) Send(packet []byte, _ *PacketAddress) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.output = append(b.output, append([]byte(nil), packet...))
	return nil
}

func (b *fakePacketBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func TestEngineOwnsOneBackendLoopAndReinjectsDecision(t *testing.T) {
	processor, err := NewProcessor(testPlan())
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakePacketBackend{
		input:    []backendPacket{{data: testIPv4TCPPacket(t, "discord.com")}},
		received: make(chan struct{}),
	}
	engine, err := NewEngine(backend, processor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(); err != nil {
		t.Fatal(err)
	}
	<-backend.received
	err = engine.Wait()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Wait() error = %v", err)
	}
	backend.mu.Lock()
	outputs := len(backend.output)
	backend.mu.Unlock()
	if outputs < 3 {
		t.Fatalf("backend outputs = %d", outputs)
	}
}

func testIPv4TCPPacket(t *testing.T, host string) []byte {
	t.Helper()
	payload := fakeTLSClientHelloForServerName(host)
	packet := make([]byte, 20+20+len(payload))
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = 6
	packet[12], packet[13], packet[14], packet[15] = 10, 0, 0, 1
	packet[16], packet[17], packet[18], packet[19] = 1, 1, 1, 1
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[20:22], 50000)
	binary.BigEndian.PutUint16(packet[22:24], 443)
	binary.BigEndian.PutUint32(packet[24:28], 1000)
	packet[32] = 5 << 4
	packet[33] = 0x18
	copy(packet[40:], payload)
	calculateChecksums(packet)
	return packet
}

func testIPv4UDPPacket(destination string, port int, payload []byte) []byte {
	packet := make([]byte, 20+8+len(payload))
	packet[0] = 0x45
	packet[8] = 64
	packet[9] = 17
	packet[12], packet[13], packet[14], packet[15] = 192, 0, 2, 1
	destinationBytes := netip.MustParseAddr(destination).As4()
	copy(packet[16:20], destinationBytes[:])
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	binary.BigEndian.PutUint16(packet[20:22], 40000)
	binary.BigEndian.PutUint16(packet[22:24], uint16(port))
	binary.BigEndian.PutUint16(packet[24:26], uint16(8+len(payload)))
	copy(packet[28:], payload)
	calculateChecksums(packet)
	return packet
}
