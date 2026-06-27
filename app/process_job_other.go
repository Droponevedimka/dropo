//go:build !windows

package main

import "os/exec"

func ensureManagedProcessJob(logger func(string)) error {
	return nil
}

func attachManagedCmdToJob(cmd *exec.Cmd, label string, logger func(string)) {
}

func closeManagedProcessJob(logger func(string)) {
}
