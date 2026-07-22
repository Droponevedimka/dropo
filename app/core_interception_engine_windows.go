//go:build windows

package main

// Windows interception engine: Dropo's native typed packet processor with one
// WinDivert owner. No external strategy process or interpreter is involved.
var (
	interceptionEngineSupportedFlag = true
	interceptionEngineKindLabel     = "Dropo native + WinDivert"
)
