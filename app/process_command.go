package main

import (
	"bytes"
	"os/exec"
)

func startManagedCommand(cmd *exec.Cmd, label string, logger func(string)) error {
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	attachManagedCmdToJob(cmd, label, logger)
	return nil
}

func runManagedCommand(cmd *exec.Cmd, label string, logger func(string)) error {
	if err := startManagedCommand(cmd, label, logger); err != nil {
		return err
	}
	return cmd.Wait()
}

func combinedOutputManagedCommand(cmd *exec.Cmd, label string, logger func(string)) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := startManagedCommand(cmd, label, logger)
	if err == nil {
		err = cmd.Wait()
	}

	output := stdout.Bytes()
	if stderr.Len() > 0 {
		output = append(output, stderr.Bytes()...)
	}
	return output, err
}

func newBackgroundCommand(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	configureBackgroundCommand(cmd)
	return cmd
}

func configureBackgroundCommand(cmd *exec.Cmd) {
	configurePlatformBackgroundCommand(cmd)
}
