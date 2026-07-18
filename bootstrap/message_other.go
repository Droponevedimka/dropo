//go:build !windows

package main

import (
	"fmt"
	"os"
)

func showBootstrapError(message string) {
	_, _ = fmt.Fprintln(os.Stderr, "dropo:", message)
}
