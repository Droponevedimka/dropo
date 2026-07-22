//go:build darwin

package main

import traffic "dropo/trafficorchestrator"

// macOS interception engine: NetworkExtension/TUN. macOS has no NFQUEUE/WinDivert
// equivalent, so transparent packet desync requires a signed system extension and
// is treated as separate R&D. The pragmatic first target is a sing-box TUN tunnel
// with VPN fallback (transparent free-bypass degraded), not packet desync.
//
// Roadmap to implement:
//   - Ship a packet-tunnel provider (NetworkExtension) with signing/notarization.
//   - Drive routing through sing-box TUN; map validated TrafficPlan entries to outbounds.
//   - Investigate a divert-socket/pf-based desync as an optional later phase.
var (
	interceptionEngineSupportedFlag = false
	interceptionEngineKindLabel     = "NetworkExtension/TUN"
)

// darwinTunEngine is the macOS adapter scaffold. It implements InterceptionEngine
// so the runtime can be filled in from one place later.
type darwinTunEngine struct{}

var _ InterceptionEngine = (*darwinTunEngine)(nil)

func (*darwinTunEngine) IsInstalled() bool                                    { return false }
func (*darwinTunEngine) ActiveTag() string                                    { return "" }
func (*darwinTunEngine) AvailableStrategies() []TransparentFreeAccessStrategy { return nil }
func (*darwinTunEngine) StartPlan(traffic.TrafficPlan) error                  { return errEngineNotImplemented }
func (*darwinTunEngine) Stop()                                                {}
func (*darwinTunEngine) strategyPath(TransparentFreeAccessStrategy) string    { return "" }
func (*darwinTunEngine) prepareDebugLog(string) (string, error)               { return "", errEngineNotImplemented }
