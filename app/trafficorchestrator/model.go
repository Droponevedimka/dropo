package trafficorchestrator

import "time"

// Network identifies the transport protocol handled by a rule or probe.
type Network string

const (
	NetworkTCP Network = "tcp"
	NetworkUDP Network = "udp"
)

// PacketActionKind is a closed set of packet transformations supported by the
// Dropo engine. Keeping actions typed prevents runtime code injection and makes
// every strategy statically auditable.
type PacketActionKind string

const (
	ActionPass            PacketActionKind = "pass"
	ActionFake            PacketActionKind = "fake"
	ActionSplit           PacketActionKind = "split"
	ActionDisorder        PacketActionKind = "disorder"
	ActionTTL             PacketActionKind = "ttl"
	ActionSequenceOverlap PacketActionKind = "sequence_overlap"
	ActionRepeat          PacketActionKind = "repeat"
)

// PacketAction describes one bounded packet transformation. Fields unused by
// a particular Kind must remain at their zero value.
type PacketAction struct {
	Kind          PacketActionKind `json:"kind"`
	Position      int              `json:"position,omitempty"`
	SequenceDelta int              `json:"sequenceDelta,omitempty"`
	Overlap       int              `json:"overlap,omitempty"`
	TTL           int              `json:"ttl,omitempty"`
	Repeats       int              `json:"repeats,omitempty"`
	Payload       string           `json:"payload,omitempty"`
	InvalidSum    bool             `json:"invalidChecksum,omitempty"`
}

// StrategyConstraints limits where a strategy may be applied.
type StrategyConstraints struct {
	Networks    []Network `json:"networks,omitempty"`
	Payloads    []string  `json:"payloads,omitempty"`
	IPv4        bool      `json:"ipv4,omitempty"`
	IPv6        bool      `json:"ipv6,omitempty"`
	MaxFlowData int       `json:"maxFlowData,omitempty"`
}

// StrategyCost is used to choose the least invasive common strategy.
type StrategyCost struct {
	SyntheticPackets int `json:"syntheticPackets,omitempty"`
	BufferedBytes    int `json:"bufferedBytes,omitempty"`
	Risk             int `json:"risk,omitempty"`
}

// TrafficStrategy is a declarative strategy understood by the Dropo packet
// engine. It intentionally contains no executable snippets or command lines.
type TrafficStrategy struct {
	ID          string              `json:"id"`
	Revision    int                 `json:"revision"`
	Label       string              `json:"label"`
	TCP         []PacketAction      `json:"tcp,omitempty"`
	UDP         []PacketAction      `json:"udp,omitempty"`
	Constraints StrategyConstraints `json:"constraints,omitempty"`
	Cost        StrategyCost        `json:"cost,omitempty"`
}

// ProbeKind identifies the protocol-level success condition for a target.
type ProbeKind string

const (
	ProbeHTTP         ProbeKind = "http"
	ProbeTCPConnect   ProbeKind = "tcp_connect"
	ProbeUDPExchange  ProbeKind = "udp_exchange"
	ProbeSTUN         ProbeKind = "stun"
	ProbeDiscordMedia ProbeKind = "discord_media"
)

// ProbeTarget is one condition that a common strategy must satisfy. Optional
// targets are diagnostic only and do not block selection.
type ProbeTarget struct {
	ID        string        `json:"id"`
	Network   Network       `json:"network"`
	Kind      ProbeKind     `json:"kind"`
	URL       string        `json:"url,omitempty"`
	Host      string        `json:"host,omitempty"`
	Port      int           `json:"port"`
	Timeout   time.Duration `json:"-"`
	TimeoutMS int           `json:"timeoutMs,omitempty"`
	Optional  bool          `json:"optional,omitempty"`
}

// ServiceRule identifies one service across web, desktop and mobile traffic.
type ServiceRule struct {
	ID                   string        `json:"id"`
	DisplayName          string        `json:"displayName"`
	ExactHosts           []string      `json:"exactHosts,omitempty"`
	DomainSuffixes       []string      `json:"domainSuffixes,omitempty"`
	IPCIDRs              []string      `json:"ipCidrs,omitempty"`
	ProcessNames         []string      `json:"processNames,omitempty"`
	TCPPorts             []int         `json:"tcpPorts,omitempty"`
	UDPPorts             []int         `json:"udpPorts,omitempty"`
	Fingerprints         []string      `json:"fingerprints,omitempty"`
	ProbeTargets         []ProbeTarget `json:"probeTargets,omitempty"`
	CandidateStrategyIDs []string      `json:"candidateStrategyIds,omitempty"`
	AllowVPNFallback     bool          `json:"allowVpnFallback"`
	AllowDirectFallback  bool          `json:"allowDirectFallback"`
}

// ServiceSelection binds one service to the strategy currently selected for
// the active network fingerprint.
type ServiceSelection struct {
	ServiceID  string `json:"serviceId"`
	StrategyID string `json:"strategyId"`
}

// WorkNetworkRule reserves corporate/private destinations for the native
// WireGuard routing layer. It is evaluated before blocked-service rules.
type WorkNetworkRule struct {
	ID             string   `json:"id"`
	DomainSuffixes []string `json:"domainSuffixes,omitempty"`
	IPCIDRs        []string `json:"ipCidrs,omitempty"`
}

// TrafficPlan is an immutable configuration snapshot installed atomically in
// the packet engine.
type TrafficPlan struct {
	Revision        uint64             `json:"revision"`
	CatalogRevision string             `json:"catalogRevision"`
	Strategies      []TrafficStrategy  `json:"strategies"`
	Services        []ServiceRule      `json:"services"`
	Selections      []ServiceSelection `json:"selections,omitempty"`
	WorkNetworks    []WorkNetworkRule  `json:"workNetworks,omitempty"`
}

// FlowEvidence contains observable properties used to classify a flow.
type FlowEvidence struct {
	Network      Network
	Destination  string
	Port         int
	Host         string
	ProcessName  string
	Fingerprints []string
}

// Classification is the safe result of service classification. Matched=false
// means the packet must pass without a service strategy.
type Classification struct {
	Matched       bool
	ServiceID     string
	Score         int
	Evidence      []string
	WorkNetwork   bool
	WorkNetworkID string
}
