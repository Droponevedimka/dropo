//go:build linux

package main

// Linux interception engine: nfqws/NFQUEUE (the zapret family's Linux desync
// engine, the direct analogue of Windows winws). Not yet wired into the runtime.
//
// Roadmap to implement:
//   - Ship nfqws (+ optional tpws) in the Linux dependency archive.
//   - Acquire CAP_NET_ADMIN/CAP_NET_RAW (or a privileged helper) and install the
//     NFQUEUE iptables/nftables rules the composed profiles need.
//   - Translate the per-service composed winws args into nfqws args. The
//     Flowseal-compatible multisplit+seqovl strategies map almost 1:1.
//   - Implement StartComposedStrategy to launch one nfqws instance from the
//     composed per-service profiles, mirroring the Windows hot path.
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
func (*nfqwsEngine) StartComposedStrategy(string, []string) error         { return errEngineNotImplemented }
func (*nfqwsEngine) Stop()                                                {}
func (*nfqwsEngine) strategyPath(TransparentFreeAccessStrategy) string    { return "" }
func (*nfqwsEngine) prepareDebugLog(string) (string, error)               { return "", errEngineNotImplemented }
