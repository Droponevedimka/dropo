package main

import (
	"errors"

	traffic "dropo/trafficorchestrator"
)

var errEngineNotImplemented = errors.New("interception engine is not implemented on this platform")

// InterceptionEngine is the platform-agnostic contract for the transparent
// traffic-interception layer (the "Deep" engine that desyncs/redirects packets
// without a full TUN). The platform-agnostic contract accepts only a validated,
// immutable TrafficPlan; command lines and external strategy processes are not
// part of the runtime boundary.
//
// Per-OS adapters:
//   - Windows: native Go packet processor + WinDivert, implemented by
//     *NativeTrafficManager.
//   - Linux:   nfqws/NFQUEUE (scaffold in core_interception_engine_linux.go).
//   - macOS:   NetworkExtension/TUN (scaffold in core_interception_engine_darwin.go).
//   - other:   unsupported (core_interception_engine_other.go).
//
// The shipped Windows engine (*NativeTrafficManager) satisfies this contract;
// the compile-time assertion below keeps the two from drifting.
type InterceptionEngine interface {
	// IsInstalled reports whether the engine's binaries/driver are present.
	IsInstalled() bool
	// ActiveTag returns the tag of the currently running strategy (or "").
	ActiveTag() string
	// AvailableStrategies lists the transparent strategies this engine can run.
	AvailableStrategies() []TransparentFreeAccessStrategy
	// StartPlan validates and atomically installs one complete routing plan.
	StartPlan(plan traffic.TrafficPlan) error
	// Stop tears down any running engine instance.
	Stop()
	// strategyPath resolves the on-disk path backing a strategy.
	strategyPath(strategy TransparentFreeAccessStrategy) string
	// prepareDebugLog provisions a packet-debug log file for verbose diagnostics.
	prepareDebugLog(tag string) (string, error)
}

// Compile-time guarantee that the Windows engine implements the contract. If a
// signature drifts, the build fails here instead of at a call site.
var _ InterceptionEngine = (*NativeTrafficManager)(nil)

// interceptionEngineSupported reports whether this build has a working transparent
// interception engine. It is set by the platform adapter file at package init and
// replaces ad-hoc runtime.GOOS == "windows" gating in the engine/network-mode
// logic, so non-Windows builds report the truth from a single source.
func interceptionEngineSupported() bool { return interceptionEngineSupportedFlag }

// interceptionEngineKind returns a short human label for the active platform
// engine (used in network-mode descriptions and diagnostics).
func interceptionEngineKind() string { return interceptionEngineKindLabel }
