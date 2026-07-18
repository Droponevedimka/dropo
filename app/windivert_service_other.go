//go:build !windows

package main

func nativeWinDivertServiceStatus() (string, error) {
	return "not supported", nil
}

func cleanupWinDivertServiceNative(_ []string, _ string, _ func(string)) {}
