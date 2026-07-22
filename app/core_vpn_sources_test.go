package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestNormalizeProfileVPNSourcesMigratesLegacySubscription(t *testing.T) {
	profile := ProfileData{SubscriptionURL: "https://example.com/sub", ProxyCount: 3}
	normalizeProfileVPNSources(&profile)
	if len(profile.VPNSources) != 1 || profile.VPNSources[0].SelectedNode != 0 || profile.VPNSources[0].NodeCount != 3 {
		t.Fatalf("profile = %+v", profile)
	}
}

func TestNormalizeProfileVPNSourcesRepairsRepeatedIDsWithoutLooping(t *testing.T) {
	profile := &ProfileData{VPNSources: []VPNSource{
		{ID: "same", Name: "First", Kind: VPNSourceDirect, URI: "vless://one@example.com:443"},
		{ID: "same", Name: "Second", Kind: VPNSourceDirect, URI: "vless://two@example.com:443"},
		{ID: "same", Name: "Third", Kind: VPNSourceDirect, URI: "vless://three@example.com:443"},
	}}
	normalizeProfileVPNSources(profile)
	got := []string{profile.VPNSources[0].ID, profile.VPNSources[1].ID, profile.VPNSources[2].ID}
	want := []string{"same", "same-2", "same-3"}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("source IDs = %v, want %v", got, want)
		}
	}
}

func TestBuildVPNSourceFallbackOutboundKeepsSourceOrder(t *testing.T) {
	outbound := buildVPNSourceFallbackOutbound([]string{"vpn-source-first", "vpn-source-second"})
	if outbound["type"] != "selector" || outbound["default"] != "vpn-source-first" {
		t.Fatalf("outbound = %#v", outbound)
	}
	values := outbound["outbounds"].([]string)
	if len(values) != 2 || values[1] != "vpn-source-second" {
		t.Fatalf("outbounds = %#v", values)
	}
}

func TestNewVPNSourceRecognizesEveryDirectProtocol(t *testing.T) {
	for _, uri := range []string{
		"vless://id@example.com:443", "trojan://pass@example.com:443",
		"hysteria2://pass@example.com:443", "tuic://id:pass@example.com:443",
	} {
		source, err := newVPNSource("source-1", "", uri)
		if err != nil || source.Kind != VPNSourceDirect {
			t.Fatalf("newVPNSource(%q) = %+v, %v", uri, source, err)
		}
	}
}

func TestMarkVPNSourceUpdatedKeepsManualNodeAcrossProviderReorder(t *testing.T) {
	first := ProxyConfig{Name: "first", Raw: "vless://first@example.com:443"}
	second := ProxyConfig{Name: "second", Raw: "vless://second@example.com:443"}
	source := VPNSource{SelectedNode: 1}
	markVPNSourceUpdated(&source, []ProxyConfig{first, second}, nil)
	selectedID := source.SelectedNodeID
	markVPNSourceUpdated(&source, []ProxyConfig{second, first}, nil)
	if source.SelectedNode != 0 || source.SelectedNodeID != selectedID {
		t.Fatalf("manual selection was not preserved after reorder: %+v", source)
	}
}

func TestPublicVPNSourcesDoNotExposeCredentials(t *testing.T) {
	views := publicVPNSources([]VPNSource{{
		ID: "source-1", Name: "Primary", URI: "vless://secret@example.com",
		CachedNodes: []string{"trojan://secret@example.com"}, NodeNames: []string{"Node"},
	}})
	text := fmt.Sprint(views)
	if strings.Contains(text, "secret") || strings.Contains(strings.ToLower(text), "cached") {
		t.Fatalf("public VPN source view leaked credentials: %s", text)
	}
}
