//go:build !windows

package main

import "os/exec"

func configurePlatformBackgroundCommand(cmd *exec.Cmd) {
}
