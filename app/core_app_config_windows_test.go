//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsAutoStartLauncherNextToResources(t *testing.T) {
	root := t.TempDir()
	resources := filepath.Join(root, ResourcesFolder)
	if err := os.MkdirAll(resources, 0755); err != nil {
		t.Fatalf("create resources: %v", err)
	}
	launcher := filepath.Join(root, AppName+".exe")
	if err := os.WriteFile(launcher, []byte("launcher"), 0644); err != nil {
		t.Fatalf("write launcher: %v", err)
	}

	core := filepath.Join(resources, AppName+"-core.exe")
	if got := resolveWindowsAutoStartLauncherPath(core); got != launcher {
		t.Fatalf("autostart launcher = %q, want %q", got, launcher)
	}
}

func TestWindowsAutoStartKeepsStandaloneExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), AppName+".exe")
	if got := resolveWindowsAutoStartLauncherPath(exe); got != exe {
		t.Fatalf("autostart launcher = %q, want %q", got, exe)
	}
}
