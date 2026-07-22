//go:build linux

package main

import traffic "dropo/trafficorchestrator"

// Linux interception engine: a future native NFQUEUE adapter. It is not wired
// into the runtime and does not share Windows command-line/process semantics.
//
// Roadmap to implement:
//   - Implement an audited in-process or privileged-helper NFQUEUE backend.
//   - Acquire CAP_NET_ADMIN/CAP_NET_RAW and install narrowly scoped nftables
//     rules without accepting generated shell commands.
//   - Compile the same validated TrafficPlan model used by Windows into bounded
//     Linux packet actions and preserve atomic plan replacement.
var (
	interceptionEngineSupportedFlag = false
	interceptionEngineKindLabel     = "nfqws/NFQUEUE"
)

// nfqwsEngine is the Linux adapter scaffold. It implements InterceptionEngine so
// the wiring point is a single file once the runtime is built; every method
// currently returns a typed not-implemented error rather than failing silently.
type nfqwsEngine struct{}

var _ InterceptionEngine = (*nfqwsEngine)(nil)

func (*nfqwsEngine) IsInstalled() bool                                    { return false }
func (*nfqwsEngine) ActiveTag() string                                    { return "" }
func (*nfqwsEngine) AvailableStrategies() []TransparentFreeAccessStrategy { return nil }
func (*nfqwsEngine) StartPlan(traffic.TrafficPlan) error                  { return errEngineNotImplemented }
func (*nfqwsEngine) Stop()                                                {}
func (*nfqwsEngine) strategyPath(TransparentFreeAccessStrategy) string    { return "" }
func (*nfqwsEngine) prepareDebugLog(string) (string, error)               { return "", errEngineNotImplemented }
