//go:build windows

package main

import (
	"os/exec"
	"testing"
	"time"
)

func TestConfigureBackgroundCommandSetsNoWindowFlags(t *testing.T) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", "exit 0")
	configureBackgroundCommand(cmd)
	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr was not initialized")
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow must be enabled for background commands")
	}
	if cmd.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatal("CREATE_NO_WINDOW must be enabled for background commands")
	}
}

func TestManagedProcessJobKillsAssignedChildOnClose(t *testing.T) {
	closeManagedProcessJob(func(msg string) { t.Log(msg) })

	cmd := exec.Command("powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 60")
	if err := startManagedCommand(cmd, "test sleep", func(msg string) { t.Log(msg) }); err != nil {
		t.Fatalf("start managed child failed: %v", err)
	}
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.HideWindow || cmd.SysProcAttr.CreationFlags&windowsCreateNoWindow == 0 {
		t.Fatal("managed start did not configure the child as a hidden background command")
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}()

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	closeManagedProcessJob(func(msg string) { t.Log(msg) })

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("managed child survived after job object close")
	}
}
