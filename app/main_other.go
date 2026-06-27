//go:build !windows

package main

import "github.com/wailsapp/wails/v2/pkg/options"

func acquireSingleInstance() (func(), bool) {
	return nil, false
}

func startPlatformTray() {
	// Linux/macOS tray support will be implemented as a dedicated platform
	// adapter. Until then close-to-tray is disabled on non-Windows builds.
}

func applyPlatformWailsOptions(appOptions *options.App) {
}

func UpdateTrayIcon(status string) {
}
