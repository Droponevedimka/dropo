//go:build !windows

package main

import (
	"os/exec"
	"time"
)

func terminateManagedCmdAndWait(cmd *exec.Cmd, _ time.Duration) bool {
	if cmd == nil || cmd.Process == nil {
		return true
	}
	return cmd.Process.Kill() == nil
}

func ensureManagedProcessJob(logger func(string)) error {
	return nil
}

func attachManagedCmdToJob(cmd *exec.Cmd, label string, logger func(string)) {
}

func closeManagedProcessJob(logger func(string)) {
}
