//go:build !windows

package main

// SetAutoStart is intentionally a no-op until per-platform launch agents are
// implemented and tested for Linux/macOS/mobile shells.
func SetAutoStart(enable bool) error {
	return nil
}

func IsAutoStartEnabled() bool {
	return false
}
