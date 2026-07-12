package main

import "testing"

func TestParseExternalVPNCandidatesAcceptsSingleObject(t *testing.T) {
	candidates, err := parseExternalVPNCandidates([]byte(`{"name":"Office VPN","detail":"vpn.example","source":"vpn-connection","status":"Connected"}`))
	if err != nil {
		t.Fatalf("parseExternalVPNCandidates failed: %v", err)
	}
	if len(candidates) != 1 {
		t.Fatalf("len(candidates) = %d, want 1", len(candidates))
	}
	if candidates[0].Name != "Office VPN" || candidates[0].Source != "vpn-connection" {
		t.Fatalf("candidate = %+v, want Office VPN profile", candidates[0])
	}
}

func TestFilterExternalVPNConflictsKeepsVPNAndIgnoresInfrastructure(t *testing.T) {
	conflicts := filterExternalVPNConflicts([]externalVPNCandidate{
		{Name: "Wi-Fi", Detail: "Intel(R) Wi-Fi 6", Source: "netadapter", Status: "Up"},
		{Name: "vEthernet (Default Switch)", Detail: "Hyper-V Virtual Ethernet Adapter", Source: "netadapter", Status: "Up"},
		{Name: "DockerNAT", Detail: "Docker virtual adapter", Source: "netadapter", Status: "Up"},
		{Name: "dropo-wg-0", Detail: "WireGuard Tunnel", Source: "netadapter", Status: "Up"},
		{Name: "NordLynx", Detail: "WireGuard Tunnel", Source: "netadapter", Status: "Up"},
		{Name: "Office VPN", Detail: "vpn.example", Source: "vpn-connection", Status: "Connected"},
		{Name: "Tailscale", Detail: "Tailscale Tunnel", Source: "netadapter", Status: "Down"},
	})

	if len(conflicts) != 2 {
		t.Fatalf("len(conflicts) = %d, want 2: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Name != "NordLynx" {
		t.Fatalf("first conflict = %+v, want NordLynx", conflicts[0])
	}
	if conflicts[1].Name != "Office VPN" || conflicts[1].Kind != "VPN profile" {
		t.Fatalf("second conflict = %+v, want Office VPN profile", conflicts[1])
	}
}

func TestFilterExternalVPNConflictsDeduplicatesCandidates(t *testing.T) {
	conflicts := filterExternalVPNConflicts([]externalVPNCandidate{
		{Name: "ProtonVPN", Detail: "WireGuard Tunnel", Source: "netadapter", Status: "Up"},
		{Name: "ProtonVPN", Detail: "WireGuard Tunnel", Source: "netadapter", Status: "Up"},
	})
	if len(conflicts) != 1 {
		t.Fatalf("len(conflicts) = %d, want 1", len(conflicts))
	}
}

func TestFilterExternalVPNConflictsKeepsLegacyKampusAdapter(t *testing.T) {
	conflicts := filterExternalVPNConflicts([]externalVPNCandidate{
		{Name: "kampus", Detail: "WireGuard Tunnel", Source: "netadapter", Status: "Up"},
	})
	if len(conflicts) != 1 {
		t.Fatalf("len(conflicts) = %d, want 1", len(conflicts))
	}
	if conflicts[0].Name != "kampus" {
		t.Fatalf("conflict = %+v, want kampus", conflicts[0])
	}
}
