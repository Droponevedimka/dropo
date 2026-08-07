//go:build windows

package trafficorchestrator

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestDivertedPacketLength(t *testing.T) {
	ipv4 := make([]byte, 60)
	ipv4[0] = 0x45
	ipv4[2], ipv4[3] = 0, 60
	if got, err := divertedPacketLength(ipv4); err != nil || got != 60 {
		t.Fatalf("IPv4 length = %d, %v", got, err)
	}
	ipv6 := make([]byte, 80)
	ipv6[0] = 0x60
	ipv6[4], ipv6[5] = 0, 40
	if got, err := divertedPacketLength(ipv6); err != nil || got != 80 {
		t.Fatalf("IPv6 length = %d, %v", got, err)
	}
	if _, err := divertedPacketLength([]byte{0x45}); err == nil {
		t.Fatal("truncated IPv4 packet accepted")
	}
}

func TestWinDivertFilterDoesNotCaptureBroadGameTraffic(t *testing.T) {
	for _, forbidden := range []string{
		"tcp.PayloadLength > 0) or (udp",
		"udp.PayloadLength > 0",
		"udp.DstPort == 27015",
		"udp.DstPort == 27036",
	} {
		if strings.Contains(winDivertCaptureFilter, forbidden) {
			t.Fatalf("capture filter contains broad/game match %q", forbidden)
		}
	}
	for _, required := range []string{
		"udp.Payload32[1] == 0x2112a442",
		"udp.PayloadLength == 74",
		"udp.PayloadLength == 148",
		"udp.DstPort == 443",
		"tcp.Payload[0] == 0x16",
	} {
		if !strings.Contains(winDivertCaptureFilter, required) {
			t.Fatalf("capture filter is missing protocol guard %q", required)
		}
	}
}

func TestWinDivertCaptureFilterCompilesWithBundledRuntime(t *testing.T) {
	dllPath, err := filepath.Abs(filepath.Join("..", "..", "dependencies", "WinDivert-2.2.2-A", "x64", "WinDivert.dll"))
	if err != nil {
		t.Fatal(err)
	}
	dll := windows.NewLazyDLL(dllPath)
	if err := dll.Load(); err != nil {
		t.Fatalf("load bundled WinDivert: %v", err)
	}
	compile := dll.NewProc("WinDivertHelperCompileFilter")
	if err := compile.Find(); err != nil {
		t.Fatal(err)
	}
	filter, err := syscall.BytePtrFromString(winDivertCaptureFilter)
	if err != nil {
		t.Fatal(err)
	}
	object := make([]byte, 16*1024)
	var errorString uintptr
	var errorPosition uint32
	result, _, callErr := compile.Call(
		uintptr(unsafe.Pointer(filter)), winDivertNetworkLayer,
		uintptr(unsafe.Pointer(&object[0])), uintptr(len(object)),
		uintptr(unsafe.Pointer(&errorString)), uintptr(unsafe.Pointer(&errorPosition)),
	)
	if result == 0 {
		t.Fatalf("WinDivert rejected capture filter at position %d: %v", errorPosition, callErr)
	}
}
