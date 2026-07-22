//go:build !windows

package main

import "fmt"

func prepareProtectedRuntime(version string) (string, error) {
	return "", fmt.Errorf("protected dependency runtime is only supported on Windows")
}

func cleanupStaleProtectedRuntimes(string) (int, error) { return 0, nil }
