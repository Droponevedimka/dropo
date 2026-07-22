package trafficorchestrator

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
)

type processorSnapshot struct {
	plan       TrafficPlan
	classifier *Classifier
	strategies map[string]TrafficStrategy
	selected   map[string]string
}

// PacketDecision is the complete result for one captured packet. Packets are
// already checksummed and ready for reinjection in listed order.
type PacketDecision struct {
	PlanRevision uint64
	ServiceID    string
	StrategyID   string
	Transformed  bool
	Packets      [][]byte
	Reason       string
}

type ServiceCounters struct {
	Matched     uint64
	Transformed uint64
	Passed      uint64
	Errors      uint64
}

// Processor owns the immutable plan snapshot but no driver handle. It is used
// by both the real Windows engine and deterministic packet replay tests.
type Processor struct {
	snapshot atomic.Pointer[processorSnapshot]
	statsMu  sync.Mutex
	stats    map[string]ServiceCounters
}

func NewProcessor(plan TrafficPlan) (*Processor, error) {
	processor := &Processor{stats: make(map[string]ServiceCounters)}
	if err := processor.ApplyPlan(plan); err != nil {
		return nil, err
	}
	return processor, nil
}

// ApplyPlan compiles the complete snapshot before one atomic pointer swap.
func (p *Processor) ApplyPlan(plan TrafficPlan) error {
	if p == nil {
		return errors.New("processor is nil")
	}
	current := p.snapshot.Load()
	if current != nil && plan.Revision <= current.plan.Revision {
		return fmt.Errorf("plan revision %d is not newer than active revision %d", plan.Revision, current.plan.Revision)
	}
	var classifier *Classifier
	if current != nil && sameClassificationPlan(current.plan, plan) {
		if err := validateSelectionRevision(plan, current); err != nil {
			return err
		}
		classifier = current.classifier
	} else {
		var err error
		classifier, err = NewClassifier(plan)
		if err != nil {
			return err
		}
	}
	snapshot := &processorSnapshot{
		plan:       plan,
		classifier: classifier,
		strategies: make(map[string]TrafficStrategy, len(plan.Strategies)),
		selected:   make(map[string]string, len(plan.Selections)),
	}
	for _, strategy := range plan.Strategies {
		snapshot.strategies[strategy.ID] = strategy
	}
	for _, selection := range plan.Selections {
		snapshot.selected[selection.ServiceID] = selection.StrategyID
	}
	p.snapshot.Store(snapshot)
	return nil
}

func sameClassificationPlan(previous, next TrafficPlan) bool {
	return previous.CatalogRevision == next.CatalogRevision &&
		reflect.DeepEqual(previous.Strategies, next.Strategies) &&
		reflect.DeepEqual(previous.Services, next.Services) &&
		reflect.DeepEqual(previous.WorkNetworks, next.WorkNetworks)
}

func validateSelectionRevision(plan TrafficPlan, current *processorSnapshot) error {
	seen := make(map[string]struct{}, len(plan.Selections))
	for _, selection := range plan.Selections {
		if _, exists := current.strategies[selection.StrategyID]; !exists {
			return fmt.Errorf("selection for %q references unknown strategy %q", selection.ServiceID, selection.StrategyID)
		}
		serviceExists := false
		for _, service := range plan.Services {
			if service.ID == selection.ServiceID {
				serviceExists = true
				break
			}
		}
		if !serviceExists {
			return fmt.Errorf("selection references unknown service %q", selection.ServiceID)
		}
		if _, duplicate := seen[selection.ServiceID]; duplicate {
			return fmt.Errorf("duplicate selection for service %q", selection.ServiceID)
		}
		seen[selection.ServiceID] = struct{}{}
	}
	return nil
}

func (p *Processor) Revision() uint64 {
	if p == nil || p.snapshot.Load() == nil {
		return 0
	}
	return p.snapshot.Load().plan.Revision
}

func (p *Processor) Process(packet []byte) PacketDecision {
	snapshot := p.snapshot.Load()
	if snapshot == nil {
		return passDecision(0, packet, "no active plan")
	}
	parsed, err := parsePacket(packet)
	if err != nil {
		return passDecision(snapshot.plan.Revision, packet, "unsupported or malformed packet")
	}
	classification := snapshot.classifier.Classify(parsed.flowEvidence())
	if classification.WorkNetwork {
		return passDecision(snapshot.plan.Revision, packet, "reserved for work network "+classification.WorkNetworkID)
	}
	if !classification.Matched {
		return passDecision(snapshot.plan.Revision, packet, "service not classified")
	}
	strategyID := snapshot.selected[classification.ServiceID]
	strategy, ok := snapshot.strategies[strategyID]
	if !ok {
		p.bump(classification.ServiceID, func(value *ServiceCounters) { value.Matched++; value.Passed++ })
		decision := passDecision(snapshot.plan.Revision, packet, "no selected strategy")
		decision.ServiceID = classification.ServiceID
		return decision
	}
	packets, transformed, err := applyStrategy(parsed, strategy)
	if err != nil {
		p.bump(classification.ServiceID, func(value *ServiceCounters) { value.Matched++; value.Errors++; value.Passed++ })
		decision := passDecision(snapshot.plan.Revision, packet, "strategy failed safe: "+err.Error())
		decision.ServiceID = classification.ServiceID
		decision.StrategyID = strategy.ID
		return decision
	}
	p.bump(classification.ServiceID, func(value *ServiceCounters) {
		value.Matched++
		if transformed {
			value.Transformed++
		} else {
			value.Passed++
		}
	})
	return PacketDecision{
		PlanRevision: snapshot.plan.Revision,
		ServiceID:    classification.ServiceID,
		StrategyID:   strategy.ID,
		Transformed:  transformed,
		Packets:      packets,
	}
}

func (p *Processor) Counters() map[string]ServiceCounters {
	if p == nil {
		return nil
	}
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	result := make(map[string]ServiceCounters, len(p.stats))
	for service, counters := range p.stats {
		result[service] = counters
	}
	return result
}

func (p *Processor) bump(service string, update func(*ServiceCounters)) {
	p.statsMu.Lock()
	defer p.statsMu.Unlock()
	value := p.stats[service]
	update(&value)
	p.stats[service] = value
}

func passDecision(revision uint64, packet []byte, reason string) PacketDecision {
	copyPacket := append([]byte(nil), packet...)
	return PacketDecision{PlanRevision: revision, Packets: [][]byte{copyPacket}, Reason: reason}
}

func applyStrategy(parsed parsedPacket, strategy TrafficStrategy) ([][]byte, bool, error) {
	if !strategyApplies(parsed, strategy.Constraints) {
		return [][]byte{append([]byte(nil), parsed.bytes...)}, false, nil
	}
	actions := strategy.TCP
	if parsed.network == NetworkUDP {
		actions = strategy.UDP
	}
	if len(actions) == 0 || len(parsed.payload()) == 0 {
		return [][]byte{append([]byte(nil), parsed.bytes...)}, false, nil
	}
	outputs := make([][]byte, 0, 8)
	originals := [][]byte{append([]byte(nil), parsed.bytes...)}
	transformed := false
	ttl := 0
	for _, action := range actions {
		switch action.Kind {
		case ActionPass:
			continue
		case ActionTTL:
			ttl = action.TTL
		case ActionFake:
			fake, err := makeFakePacket(parsed, action, ttl)
			if err != nil {
				return nil, false, err
			}
			for repeat := 0; repeat < action.Repeats; repeat++ {
				outputs = append(outputs, append([]byte(nil), fake...))
			}
			transformed = true
		case ActionSplit, ActionDisorder:
			if parsed.network != NetworkTCP {
				return nil, false, fmt.Errorf("%s applied to non-TCP packet", action.Kind)
			}
			segments, err := splitTCPPacket(parsed, action.Position)
			if err != nil {
				return nil, false, err
			}
			if action.Kind == ActionDisorder {
				for left, right := 0, len(segments)-1; left < right; left, right = left+1, right-1 {
					segments[left], segments[right] = segments[right], segments[left]
				}
			}
			originals = segments
			transformed = true
		case ActionSequenceOverlap:
			if parsed.network != NetworkTCP {
				return nil, false, errors.New("sequence overlap applied to non-TCP packet")
			}
			overlap, err := makeOverlapPacket(parsed, action.Overlap)
			if err != nil {
				return nil, false, err
			}
			outputs = append(outputs, overlap)
			transformed = true
		case ActionRepeat:
			if len(outputs) == 0 {
				return nil, false, errors.New("repeat has no preceding synthetic packet")
			}
			last := outputs[len(outputs)-1]
			for repeat := 1; repeat < action.Repeats; repeat++ {
				outputs = append(outputs, append([]byte(nil), last...))
			}
			transformed = true
		default:
			return nil, false, fmt.Errorf("unsupported action %q", action.Kind)
		}
	}
	outputs = append(outputs, originals...)
	return outputs, transformed, nil
}

func strategyApplies(parsed parsedPacket, constraints StrategyConstraints) bool {
	if len(constraints.Networks) > 0 {
		matched := false
		for _, network := range constraints.Networks {
			if network == parsed.network {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if (constraints.IPv4 || constraints.IPv6) && ((parsed.ipVersion == 4 && !constraints.IPv4) || (parsed.ipVersion == 6 && !constraints.IPv6)) {
		return false
	}
	if constraints.MaxFlowData > 0 && len(parsed.payload()) > constraints.MaxFlowData {
		return false
	}
	if len(constraints.Payloads) == 0 {
		return true
	}
	evidence := parsed.flowEvidence()
	fingerprints := make(map[string]struct{}, len(evidence.Fingerprints))
	for _, fingerprint := range evidence.Fingerprints {
		fingerprints[normalizeToken(fingerprint)] = struct{}{}
	}
	for _, required := range constraints.Payloads {
		if _, ok := fingerprints[normalizeToken(required)]; ok {
			return true
		}
	}
	return false
}

func makeFakePacket(parsed parsedPacket, action PacketAction, ttl int) ([]byte, error) {
	var payload []byte
	switch action.Payload {
	case "tls_client_hello":
		payload = fakeTLSClientHello()
	case "quic_initial":
		payload = fakeQUICInitial(parsed.payload())
	case "original":
		payload = append([]byte(nil), parsed.payload()...)
	default:
		return nil, fmt.Errorf("unknown fake payload %q", action.Payload)
	}
	packet, updated, err := resizePacketPayload(parsed, payload)
	if err != nil {
		return nil, err
	}
	if ttl > 0 {
		setPacketTTL(packet, updated, ttl)
	}
	if action.SequenceDelta != 0 && updated.network == NetworkTCP {
		sequence := uint32(int64(updated.tcpSequence) + int64(action.SequenceDelta))
		binary.BigEndian.PutUint32(packet[updated.transportOffset+4:updated.transportOffset+8], sequence)
	}
	calculateChecksums(packet)
	if action.InvalidSum {
		corruptTransportChecksum(packet, updated)
	}
	return packet, nil
}

func splitTCPPacket(parsed parsedPacket, position int) ([][]byte, error) {
	payload := parsed.payload()
	if position < 1 || position >= len(payload) {
		return nil, fmt.Errorf("split position %d is outside payload length %d", position, len(payload))
	}
	parts := [][]byte{payload[:position], payload[position:]}
	segments := make([][]byte, 0, 2)
	offset := 0
	for index, part := range parts {
		packet, updated, err := resizePacketPayload(parsed, part)
		if err != nil {
			return nil, err
		}
		binary.BigEndian.PutUint32(packet[updated.transportOffset+4:updated.transportOffset+8], parsed.tcpSequence+uint32(offset))
		if index < len(parts)-1 {
			packet[updated.tcpFlagsOffset] &^= 0x09 // FIN + PSH are kept only on the last segment.
		}
		calculateChecksums(packet)
		segments = append(segments, packet)
		offset += len(part)
	}
	return segments, nil
}

func makeOverlapPacket(parsed parsedPacket, overlap int) ([]byte, error) {
	payload := parsed.payload()
	if overlap < 1 || overlap > len(payload) {
		return nil, fmt.Errorf("overlap %d is outside payload length %d", overlap, len(payload))
	}
	fake := make([]byte, overlap)
	for index := range fake {
		fake[index] = byte(0xa5 ^ index)
	}
	packet, updated, err := resizePacketPayload(parsed, fake)
	if err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(packet[updated.transportOffset+4:updated.transportOffset+8], parsed.tcpSequence-uint32(overlap))
	packet[updated.tcpFlagsOffset] &^= 0x09
	calculateChecksums(packet)
	return packet, nil
}

func setPacketTTL(packet []byte, parsed parsedPacket, ttl int) {
	if parsed.ipVersion == 4 {
		packet[8] = byte(ttl)
	} else {
		packet[7] = byte(ttl)
	}
}

func corruptTransportChecksum(packet []byte, parsed parsedPacket) {
	offset := parsed.transportOffset + 16
	if parsed.network == NetworkUDP {
		offset = parsed.transportOffset + 6
	}
	if offset+2 <= len(packet) {
		checksum := binary.BigEndian.Uint16(packet[offset : offset+2])
		binary.BigEndian.PutUint16(packet[offset:offset+2], checksum^0xffff)
	}
}

func fakeTLSClientHello() []byte {
	return fakeTLSClientHelloForServerName("www.google.com")
}

func fakeTLSClientHelloForServerName(host string) []byte {
	serverName := []byte(normalizeHost(host))
	serverNameListLength := 3 + len(serverName)
	serverNameExtensionLength := 2 + serverNameListLength
	extensionsLength := 4 + serverNameExtensionLength
	bodyLength := 2 + 32 + 1 + 2 + 2 + 1 + 1 + 2 + extensionsLength
	handshakeLength := 4 + bodyLength
	payload := make([]byte, 5+handshakeLength)
	payload[0] = 0x16
	payload[1] = 0x03
	payload[2] = 0x01
	binary.BigEndian.PutUint16(payload[3:5], uint16(handshakeLength))
	payload[5] = 0x01
	payload[6] = byte(bodyLength >> 16)
	payload[7] = byte(bodyLength >> 8)
	payload[8] = byte(bodyLength)
	cursor := 9
	payload[cursor], payload[cursor+1] = 0x03, 0x03
	cursor += 2
	for index := 0; index < 32; index++ {
		payload[cursor+index] = byte(index*17 + 3)
	}
	cursor += 32
	payload[cursor] = 0
	cursor++
	binary.BigEndian.PutUint16(payload[cursor:cursor+2], 2)
	cursor += 2
	payload[cursor], payload[cursor+1] = 0x13, 0x01
	cursor += 2
	payload[cursor], payload[cursor+1] = 1, 0
	cursor += 2
	binary.BigEndian.PutUint16(payload[cursor:cursor+2], uint16(extensionsLength))
	cursor += 2
	binary.BigEndian.PutUint16(payload[cursor:cursor+2], 0)
	binary.BigEndian.PutUint16(payload[cursor+2:cursor+4], uint16(serverNameExtensionLength))
	cursor += 4
	binary.BigEndian.PutUint16(payload[cursor:cursor+2], uint16(serverNameListLength))
	cursor += 2
	payload[cursor] = 0
	binary.BigEndian.PutUint16(payload[cursor+1:cursor+3], uint16(len(serverName)))
	cursor += 3
	copy(payload[cursor:], serverName)
	return payload
}

func fakeQUICInitial(original []byte) []byte {
	// QUIC Initial payloads are encrypted. Reusing a bounded prefix with a
	// deterministic tail preserves the public long-header shape without loading
	// external binary blobs or parsing secret application data.
	length := len(original)
	if length < 64 {
		length = 64
	}
	if length > 1200 {
		length = 1200
	}
	payload := make([]byte, length)
	copy(payload, original)
	for index := len(original); index < len(payload); index++ {
		payload[index] = byte(index*29 + 11)
	}
	if len(payload) >= 5 {
		payload[0] |= 0xc0
		if binary.BigEndian.Uint32(payload[1:5]) == 0 {
			binary.BigEndian.PutUint32(payload[1:5], 1)
		}
	}
	return payload
}
