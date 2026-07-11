//go:build !windows

package main

import "fmt"

func startElevatedCore(corePath string) error {
	return fmt.Errorf("elevated core launch is only supported on Windows: %s", corePath)
}
