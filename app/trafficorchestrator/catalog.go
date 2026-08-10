package trafficorchestrator

// BuiltinCatalogRevision changes whenever packet semantics or ordering changes.
const BuiltinCatalogRevision = "dropo-native-windows-3"

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
	sniMiddle := PacketPosition{Anchor: "tls-sni-middle"}
	firstByte := PacketPosition{Absolute: 1}
	strategies := []TrafficStrategy{
		{
			ID: "native-zapret2-fake-multidisorder", Revision: 1, Label: "TLS fake and SNI multidisorder",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, Repeats: 11, InvalidSum: true},
				{Kind: ActionDisorder, Positions: []PacketPosition{firstByte, sniMiddle}},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_decoy", Repeats: 11}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 14, BufferedBytes: 681, Risk: 26},
		},
		{
			ID: "native-zapret2-fake-sni-split", Revision: 1, Label: "TLS fake and SNI split",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, SequenceDelta: -10000, Repeats: 6},
				{Kind: ActionSplit, Positions: []PacketPosition{sniMiddle}},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_decoy", Repeats: 5}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 8, BufferedBytes: 681, Risk: 18},
		},
		{
			ID: "native-zapret2-sni-disorder", Revision: 1, Label: "SNI multidisorder",
			TCP:         []PacketAction{{Kind: ActionDisorder, Positions: []PacketPosition{firstByte, sniMiddle}}},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_decoy", Repeats: 10}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 13, BufferedBytes: 681, Risk: 20},
		},
		{
			ID: "native-zapret2-overlap-sni", Revision: 1, Label: "Sequence overlap and SNI split",
			TCP: []PacketAction{
				{Kind: ActionSequenceOverlap, Overlap: 8},
				{Kind: ActionSplit, Positions: []PacketPosition{firstByte, sniMiddle}},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_decoy", Repeats: 5}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 9, BufferedBytes: 689, Risk: 23},
		},
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
	// Discord's active discovery/STUN candidates deliberately differ on UDP.
	// The old catalog attached the same UDP action to every TCP strategy, so a
	// "next strategy" was often a no-op for voice. Protocol decoys are generated
	// in-process from the observed handshake and remain bounded to three packets.
	strategies = append(strategies,
		TrafficStrategy{
			ID: "native-discord-active-v2", Revision: 1, Label: "Discord active QUIC and SNI decoys",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, SequenceDelta: -10000, Repeats: 8},
				{Kind: ActionDisorder, Positions: []PacketPosition{firstByte, sniMiddle}},
			},
			UDP: []PacketAction{
				{Kind: ActionFake, Payload: "quic_decoy", Repeats: 3},
				{Kind: ActionFake, Payload: "protocol_decoy", Repeats: 3},
			},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 17, BufferedBytes: 1881, Risk: 18},
		},
		TrafficStrategy{
			ID: "native-discord-zero-v2", Revision: 1, Label: "Discord zero and TLS decoys",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, SequenceDelta: -10000, Repeats: 6},
				{Kind: ActionSplit, Positions: []PacketPosition{sniMiddle}},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "zero", PadTo: 32, Repeats: 2}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 10, BufferedBytes: 713, Risk: 20},
		},
		TrafficStrategy{
			ID: "native-discord-invalidsum-v2", Revision: 1, Label: "Discord invalid-checksum QUIC decoys",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, SequenceDelta: -10000, Repeats: 6, InvalidSum: true},
				{Kind: ActionSplit, Positions: []PacketPosition{sniMiddle}},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "quic_decoy", Repeats: 4, InvalidSum: true}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 12, BufferedBytes: 1881, Risk: 25},
		},
		TrafficStrategy{
			ID: "native-discord-low-ttl-v2", Revision: 1, Label: "Discord low-TTL QUIC decoys",
			TCP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "tls_client_hello", PadTo: 681, SequenceDelta: -10000, Repeats: 4},
				{Kind: ActionSplit, Positions: []PacketPosition{sniMiddle}},
			},
			UDP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "quic_decoy", Repeats: 3},
			},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 9, BufferedBytes: 1881, Risk: 34},
		},
		TrafficStrategy{
			ID: "native-discord-active", Revision: 1, Label: "Discord active protocol decoys",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", SequenceDelta: -10000, Repeats: 4, InvalidSum: true},
				{Kind: ActionSplit, Position: 2},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "protocol_decoy", Repeats: 3}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 7, BufferedBytes: 1024, Risk: 16},
		},
		TrafficStrategy{
			ID: "native-discord-invalidsum", Revision: 1, Label: "Discord invalid-checksum protocol decoys",
			TCP: []PacketAction{
				{Kind: ActionFake, Payload: "tls_client_hello", SequenceDelta: -10000, Repeats: 4, InvalidSum: true},
				{Kind: ActionSplit, Position: 2},
			},
			UDP:         []PacketAction{{Kind: ActionFake, Payload: "protocol_decoy", Repeats: 3, InvalidSum: true}},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 7, BufferedBytes: 1024, Risk: 24},
		},
		TrafficStrategy{
			ID: "native-discord-low-ttl", Revision: 1, Label: "Discord low-TTL protocol decoys",
			TCP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "tls_client_hello", SequenceDelta: -10000, Repeats: 4},
				{Kind: ActionSplit, Position: 2},
			},
			UDP: []PacketAction{
				{Kind: ActionTTL, TTL: 3},
				{Kind: ActionFake, Payload: "protocol_decoy", Repeats: 3},
			},
			Constraints: common,
			Cost:        StrategyCost{SyntheticPackets: 7, BufferedBytes: 1024, Risk: 36},
		},
	)
	return strategies
}
