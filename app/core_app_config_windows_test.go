//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsAutoStartCommandUsesLauncherNextToResources(t *testing.T) {
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
	got := windowsAutoStartCommandForExecutable(core)
	want := `"` + launcher + `"`
	if got != want {
		t.Fatalf("autostart command = %q, want %q", got, want)
	}
}

func TestWindowsAutoStartCommandKeepsStandaloneExecutable(t *testing.T) {
	exe := filepath.Join(t.TempDir(), AppName+".exe")
	got := windowsAutoStartCommandForExecutable(exe)
	want := `"` + exe + `"`
	if got != want {
		t.Fatalf("autostart command = %q, want %q", got, want)
	}
}
