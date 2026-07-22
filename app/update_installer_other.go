//go:build !windows

package main

import "fmt"

func stageInstalledUpdate(string, string, int64, string) (string, error) {
	return "", fmt.Errorf("installer-managed update is available only on Windows")
}

func startInstalledUpdate(string, int64, string) error {
	return fmt.Errorf("installer-managed update is available only on Windows")
}
