package trafficorchestrator

// BuiltinCatalogRevision changes whenever packet semantics or ordering changes.
const BuiltinCatalogRevision = "dropo-native-windows-1"

// BuiltinStrategies returns the bounded strategy ladder implemented by the
// Dropo packet engine. It contains data only: no shell arguments, Lua or
// external payload files are accepted by the runtime.
func BuiltinStrategies() []TrafficStrategy {
	common := StrategyConstraints{
		Networks: []Network{NetworkTCP, NetworkUDP},
		Payloads: []string{
			"http-request", "tls-client-hello", "quic-initial", "stun",
			"discord-media", "wireguard-initiation", "wireguard-cookie",
		},
		IPv4:        true,
		IPv6:        true,
		MaxFlowData: 64 * 1024,
	}
	udpDecoy := []PacketAction{{Kind: ActionFake, Payload: "original", Repeats: 2, InvalidSum: true}}
	return []TrafficStrategy{
		{
			ID: "native-split-1", Revision: 1, Label: "Segment at byte 1",
			TCP: []PacketAction{{Kind: ActionSplit, Position: 1}}, UDP: udpDecoy,
			Constraints: common, Cost: StrategyCost{SyntheticPackets: 2, BufferedBytes: 1, Risk: 5},
		},
		{
			ID: "native-split-2", Revision: 1, Label: "Segment at byte 2",
			TCP: []PacketAction{{Kind: ActionSplit, Position: 2}}, UDP: udpDecoy,
			Constraints: common, Cost: StrategyCost{SyntheticPackets: 2, BufferedBytes: 2, Risk: 8},
		},
		{
			ID: "native-overlap-split", Revision: 1, Label: "Sequence overlap and segment",
			TCP: []PacketAction{
				{Kind: ActionSequenceOverlap, Overlap: 8},
				{Kind: ActionSplit, Position: 2},
			},
			UDP: udpDecoy, Constraints: common,
			Cost: StrategyCost{SyntheticPackets: 3, BufferedBytes: 10, Risk: 18},
		},
		{
			ID: "native-disorder-2", Revision: 1, Label: "Out-of-order segments",
			TCP: []PacketAction{{Kind: ActionDisorder, Position: 2}}, UDP: udpDecoy,
			Constraints: common, Cost: StrategyCost{SyntheticPackets: 2, BufferedBytes: 2, Risk: 24},
		},
		{
			ID: "native-decoy-split", Revision: 1, Label: "Invalid-checksum decoy and segment",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", SequenceDelta: -10000, Repeats: 4, InvalidSum: true},
				{Kind: ActionSplit, Position: 2},
			},
			UDP: udpDecoy, Constraints: common,
			Cost: StrategyCost{SyntheticPackets: 6, BufferedBytes: 1024, Risk: 30},
		},
		{
			ID: "native-low-ttl-decoy", Revision: 1, Label: "Low-TTL decoy and segment",
			TCP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "tls_client_hello", SequenceDelta: -10000, Repeats: 4},
				{Kind: ActionSplit, Position: 2},
			},
			UDP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "original", Repeats: 2},
			},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 6, BufferedBytes: 1024, Risk: 42},
		},
	}
}
