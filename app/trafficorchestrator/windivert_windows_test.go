//go:build windows

package trafficorchestrator

import "testing"

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
