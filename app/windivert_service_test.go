package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseWinDivertBinaryPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dropo-2.0.0-test")
	want := filepath.Join(root, "bin", "WinDivert64.sys")
	output := strings.Join([]string{
		"SERVICE_NAME: WinDivert",
		"        TYPE               : 1  KERNEL_DRIVER",
		"        BINARY_PATH_NAME   : \\??\\" + want,
	}, "\r\n")

	got := parseWinDivertBinaryPath(output)
	if got != filepath.Clean(want) {
		t.Fatalf("parseWinDivertBinaryPath() = %q, want %q", got, filepath.Clean(want))
	}
}

func TestWinDivertBinaryOwnedByRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dropo-2.0.0-test")
	path := "\\??\\" + filepath.Join(root, "bin", "WinDivert64.sys")

	if !winDivertBinaryOwnedByRoots(path, []string{root}) {
		t.Fatalf("expected %q to be owned by root %q", path, root)
	}
}

func TestWinDivertBinaryOwnedByRootsRejectsExternalService(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dropo-2.0.0-test")
	otherRoot := filepath.Join(t.TempDir(), "other-vpn")
	path := "\\??\\" + filepath.Join(otherRoot, "bin", "WinDivert64.sys")

	if winDivertBinaryOwnedByRoots(path, []string{root}) {
		t.Fatalf("external WinDivert service must not be treated as dropo-owned")
	}
}

func TestParseRegistryStringValue(t *testing.T) {
	want := filepath.Join(`E:\kampus-vpn`, `release`, `dropo-2.0.0-test`, `bin`, `WinDivert64.sys`)
	output := strings.Join([]string{
		`HKEY_LOCAL_MACHINE\SYSTEM\CurrentControlSet\Services\EventLog\System\WinDivert`,
		`    EventMessageFile    REG_SZ    ` + want,
	}, "\r\n")

	if got := parseRegistryStringValue(output, "EventMessageFile"); got != want {
		t.Fatalf("parseRegistryStringValue() = %q, want %q", got, want)
	}
}
