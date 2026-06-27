//go:build !windows

package main

// Telegram proxy presence detection is Windows-only (the app ships for Windows).
// Non-Windows builds (tests on CI) get safe no-op stubs.

func telegramProxyHasActiveConnection(port uint16) bool { return false }

func isProcessRunningByName(name string) bool { return false }
