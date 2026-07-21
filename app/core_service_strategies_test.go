//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestComposeServiceWinwsArgsPerServiceProfiles(t *testing.T) {
	selections := []serviceWinwsSelection{
		{ServiceTag: "discord", HostlistPath: "C:\\d\\discord.txt", Method: methodMultisplit("m1", "M1", 652, 2, googleQUICPayload)},
		{ServiceTag: "youtube", HostlistPath: "C:\\d\\youtube.txt", Method: methodFakedsplitTS("m2", "M2", googleQUICPayload)},
	}
	args := composeServiceWinwsArgs(selections, "C:\\dropo\\bin")
	joined := strings.Join(args, " ")

	if !strings.HasPrefix(joined, "--wf-tcp-out=80,443,2048,2053,2083,2087,2096,8443 --wf-raw-part=@C:\\dropo\\bin\\windivert_part.quic_initial_ietf.txt") {
		t.Fatalf("composed args must start with wf header: %s", joined)
	}
	for _, rawPart := range []string{
		"--wf-raw-part=@C:\\dropo\\bin\\windivert_part.discord_media.txt",
		"--wf-raw-part=@C:\\dropo\\bin\\windivert_part.stun.txt",
	} {
		if !strings.Contains(joined, rawPart) {
			t.Fatalf("missing Discord raw WinDivert filter %q: %s", rawPart, joined)
		}
	}
	// Each service contributes HTTP, TLS, and QUIC profiles scoped
	// to its own hostlist; Discord media profiles are intentionally scoped by
	// discord.media or l7 instead of the hostlist file.
	for _, host := range []string{"--hostlist=C:\\d\\discord.txt", "--hostlist=C:\\d\\youtube.txt"} {
		if strings.Count(joined, host) != 3 {
			t.Fatalf("expected hostlist %q applied to exactly 3 profiles (HTTP+TLS+QUIC): %s", host, joined)
		}
	}
	// ${BIN} must be resolved to the real bin dir.
	if strings.Contains(joined, "${BIN}") {
		t.Fatalf("unresolved ${BIN} in composed args: %s", joined)
	}
	if !strings.Contains(joined, "--blob=google_tls:@C:\\dropo\\bin\\tls_clienthello_www_google_com.bin") {
		t.Fatalf("bin path not resolved: %s", joined)
	}
	// Discord uses multisplit, YouTube uses fakedsplit — distinct per service.
	if !strings.Contains(joined, "--lua-desync=multisplit:pos=2:seqovl=652") || !strings.Contains(joined, "--lua-desync=fakedsplit:pos=2:pattern=0x00") {
		t.Fatalf("per-service methods not both present: %s", joined)
	}
	for _, expected := range []string{
		"--filter-tcp=2048,2053,2083,2087,2096,8443 --hostlist-domains=discord.media --payload=tls_client_hello --lua-desync=multisplit:pos=1:seqovl=681:seqovl_pattern=google_tls",
		"--filter-l7=discord,stun --payload=discord_ip_discovery,stun",
		"--lua-desync=fake:blob=0x00000000000000000000000000000000:repeats=2",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing Discord media profile %q: %s", expected, joined)
		}
	}
	for _, forbidden := range []string{"--payload=all", "--out-range=-d", "--ipset-ip=104.29."} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("encrypted Discord media must not use dynamic fake profile %q: %s", forbidden, joined)
		}
	}
	// Discord contributes 5 profiles and YouTube contributes 3 = 8 profiles.
	if got := strings.Count(joined, "--new"); got != 7 {
		t.Fatalf("expected 7 --new separators for 8 profiles, got %d: %s", got, joined)
	}
}

func TestComposeServiceWinwsArgsKeepsNarrowFiltersWithoutDiscord(t *testing.T) {
	selections := []serviceWinwsSelection{
		{ServiceTag: "youtube", HostlistPath: "C:\\d\\youtube.txt", Method: methodFakedsplitTS("m2", "M2", googleQUICPayload)},
	}
	args := composeServiceWinwsArgs(selections, "C:\\dropo\\bin")
	joined := strings.Join(args, " ")

	if !strings.HasPrefix(joined, "--wf-tcp-out=80,443 --wf-raw-part=@C:\\dropo\\bin\\windivert_part.quic_initial_ietf.txt") {
		t.Fatalf("non-Discord composed args must keep narrow wf header: %s", joined)
	}
	for _, unexpected := range []string{discordMediaTCPPorts, "--filter-l7=discord,stun", "--payload=discord_ip_discovery,stun", "--hostlist-domains=discord.media"} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("non-Discord composed args unexpectedly contain %q: %s", unexpected, joined)
		}
	}
}

func TestZapret2RuntimeDoesNotUseZapret1Interface(t *testing.T) {
	if ZapretProcessName != "winws2.exe" {
		t.Fatalf("Windows runtime executable = %q, want winws2.exe", ZapretProcessName)
	}
	for _, file := range zapret2RequiredFiles {
		if strings.EqualFold(file, "winws.exe") {
			t.Fatal("zapret1 winws.exe must not be a required runtime file")
		}
	}
	for service, methods := range DefaultServiceBypassMethods() {
		for _, method := range methods {
			joined := strings.Join(append(append([]string{}, method.TCPArgs...), method.UDPArgs...), " ")
			if strings.Contains(joined, "--dpi-desync") {
				t.Fatalf("service %s method %s still uses the zapret1 CLI: %s", service, method.Tag, joined)
			}
		}
	}
}

func TestServiceBypassMethodsOnlyReferenceBundledPayloads(t *testing.T) {
	bundled := map[string]bool{
		googleTLSPayload:    true,
		googleQUICPayload:   true,
		facebookQUICPayload: true,
	}
	for tag, methods := range DefaultServiceBypassMethods() {
		if len(methods) == 0 {
			t.Fatalf("service %s has no ranked methods", tag)
		}
		for _, m := range methods {
			for _, arg := range append(append([]string{}, m.TCPArgs...), m.UDPArgs...) {
				idx := strings.Index(arg, "${BIN}")
				if idx < 0 {
					continue
				}
				if bin := arg[idx+len("${BIN}"):]; !bundled[bin] {
					t.Fatalf("service %s method %s references non-bundled payload %q", tag, m.Tag, bin)
				}
			}
		}
	}
}

func TestServiceStrategiesAreCuratedPerServiceFromFile(t *testing.T) {
	methods := DefaultServiceBypassMethods()

	// Every DPI-solvable service must have a curated ladder; VPN-only services
	// (Meta/WhatsApp) must have NONE so they are never searched.
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			continue
		}
		// vpn-only and proxy-handled (tg-ws-proxy) services have no winws2 methods.
		if bt := serviceBlockType(svc.Tag); bt == "vpn" || bt == "proxy" {
			if serviceHasFreeBypass(svc.Tag) {
				t.Fatalf("%s (%s) must have no winws2 desync methods", svc.Tag, bt)
			}
			continue
		}
		if len(methods[svc.Tag]) == 0 {
			t.Fatalf("service %s missing curated methods in service_strategies.json", svc.Tag)
		}
	}

	// YouTube must carry the post-May-2026 fake,multisplit technique.
	youtubeHasFakeMultisplit := false
	for _, m := range methods["youtube"] {
		joinedMethod := strings.Join(m.TCPArgs, " ")
		if strings.Contains(joinedMethod, "--lua-desync=fake:blob=google_tls") && strings.Contains(joinedMethod, "--lua-desync=multisplit") {
			youtubeHasFakeMultisplit = true
		}
	}
	if !youtubeHasFakeMultisplit {
		t.Fatal("youtube must include the fake,multisplit (fooling=ts) technique")
	}

	// Meta/WhatsApp are VPN-only; Telegram is proxy-handled (tg-ws-proxy) —
	// none of them are composed into the winws2 desync engine.
	for _, noDesync := range []string{"meta", "whatsapp", "telegram"} {
		if serviceHasFreeBypass(noDesync) {
			t.Fatalf("%s must not have winws2 desync methods", noDesync)
		}
	}
	if serviceBlockType("telegram") != "proxy" {
		t.Fatalf("telegram must be proxy-handled, got %q", serviceBlockType("telegram"))
	}
	// Empirically desync-solvable services must NOT be pre-routed to VPN.
	for _, dpi := range []string{"discord", "youtube", "twitter", "viber", "tiktok"} {
		if serviceVpnPreferred(dpi) {
			t.Fatalf("%s is desync-solvable and must not prefer VPN fallback", dpi)
		}
	}
}

func TestEveryDpiServiceHasRankedMethods(t *testing.T) {
	for _, svc := range DefaultFreeAccessServices {
		if bt := serviceBlockType(svc.Tag); svc.RequiresVPN || bt == "vpn" || bt == "proxy" {
			continue
		}
		if len(rankedMethodsForService(svc.Tag)) == 0 {
			t.Fatalf("desync-solvable service %s has no ranked bypass methods", svc.Tag)
		}
	}
}
