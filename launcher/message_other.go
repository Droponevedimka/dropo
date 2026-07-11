//go:build !windows

package main

import (
	"fmt"
	"os"
)

func showLauncherError(message string) error {
	_, err := fmt.Fprintln(os.Stderr, message)
	return err
}
