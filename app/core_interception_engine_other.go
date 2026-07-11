//go:build !windows && !linux && !darwin

package main

// Fallback for platforms without a transparent interception engine adapter.
// Mobile (Android/iOS) is intentionally NOT handled here: those are native VPN
// shells wrapping the Go core via gomobile, not GOOS variants of this binary.
var (
	interceptionEngineSupportedFlag = false
	interceptionEngineKindLabel     = "unsupported"
)
