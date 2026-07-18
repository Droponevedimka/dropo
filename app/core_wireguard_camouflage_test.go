package main

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestResolveWireGuardCamouflageTargetsScopesLiteralAndDNS(t *testing.T) {
	configs := []UserWireGuardConfig{
		{Tag: "literal", Endpoint: "203.0.113.10", EndpointPort: 51820, CamouflageEnabled: true},
		{Tag: "dns", Endpoint: "wg.example", EndpointPort: 443, CamouflageEnabled: true},
		{Tag: "off", Endpoint: "198.51.100.1", EndpointPort: 53},
	}
	lookup := func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host != "wg.example" {
			t.Fatalf("unexpected lookup for %q", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("2001:db8::2")}, {IP: net.ParseIP("198.51.100.20")}}, nil
	}
	targets, warnings := resolveWireGuardCamouflageTargets(context.Background(), configs, nil, lookup)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %#v, want 2", targets)
	}
	if got, want := targets[0].IPs, []string{"203.0.113.10"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("literal IPs = %v, want %v", got, want)
	}
	if got, want := targets[1].IPs, []string{"198.51.100.20", "2001:db8::2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("DNS IPs = %v, want %v", got, want)
	}
}

func TestResolveWireGuardCamouflageTargetsHonorsSessionRollback(t *testing.T) {
	configs := []UserWireGuardConfig{{Tag: "wg", Endpoint: "203.0.113.10", EndpointPort: 51820, CamouflageEnabled: true}}
	targets, _ := resolveWireGuardCamouflageTargets(context.Background(), configs, map[int]bool{0: true}, net.DefaultResolver.LookupIPAddr)
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want disabled target omitted", targets)
	}
}

func TestComposeWireGuardCamouflageIsStrictlyScoped(t *testing.T) {
	args := composeServiceAndWireGuardWinwsArgs(nil, []wireGuardCamouflageTarget{{
		ConfigID: 0,
		Tag:      "wg",
		Port:     51820,
		IPs:      []string{"203.0.113.10", "2001:db8::2"},
	}}, `C:\dropo\bin`)
	joined := strings.Join(args, "\n")
	for _, required := range []string{
		"--wf-udp-out=51820",
		"--filter-udp=51820",
		"--filter-l7=wireguard",
		"--ipset-ip=2001:db8::2,203.0.113.10",
		"--payload=wireguard_initiation,wireguard_cookie",
		"--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=2",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("composed args missing %q:\n%s", required, joined)
		}
	}
	if strings.Contains(joined, "wireguard_transport") || strings.Contains(joined, "wireguard_keepalive") {
		t.Fatalf("camouflage must not alter WireGuard data/keepalive packets:\n%s", joined)
	}
}

func TestParseWireGuardEndpointSupportsBracketedIPv6(t *testing.T) {
	host, port, err := parseWireGuardEndpoint("[2001:db8::5]:51820")
	if err != nil {
		t.Fatal(err)
	}
	if host != "2001:db8::5" || port != 51820 {
		t.Fatalf("got %s:%d", host, port)
	}
}
