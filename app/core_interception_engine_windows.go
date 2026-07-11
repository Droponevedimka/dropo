//go:build windows

package main

// Windows interception engine: winws + WinDivert ("Deep Windows"). This is the
// production engine; the concrete implementation lives in
// core_freeaccess_sidecars.go (*TransparentBypassManager).
var (
	interceptionEngineSupportedFlag = true
	interceptionEngineKindLabel     = "winws + WinDivert"
)
