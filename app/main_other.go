//go:build !windows

package main

func acquireSingleInstance() (func(), bool) {
	return nil, false
}

func startPlatformTray() {
	// Linux/macOS tray support will be implemented as a dedicated platform
	// adapter. Until then close-to-tray is disabled on non-Windows builds.
}

func ensurePlatformTray() {
}

func showPlatformWindow() {
}

func hidePlatformWindow() {
}

func UpdateTrayIcon(status string) {
}
