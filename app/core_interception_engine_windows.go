//go:build windows

package main

// Windows interception engine: zapret2/winws2 + WinDivert (Windows Unified). This is the
// production engine; the concrete implementation lives in
// core_freeaccess_sidecars.go (*TransparentBypassManager).
var (
	interceptionEngineSupportedFlag = true
	interceptionEngineKindLabel     = "winws2 + WinDivert"
)
