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

	if !strings.HasPrefix(joined, "--wf-tcp=80,443,2048,2053,2083,2087,2096,8443 --wf-udp=443,19294-19344,50000-50100") {
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
	// Each service still contributes its normal tcp and udp 443 profiles scoped
	// to its own hostlist; Discord media profiles are intentionally scoped by
	// discord.media or l7 instead of the hostlist file.
	for _, host := range []string{"--hostlist=C:\\d\\discord.txt", "--hostlist=C:\\d\\youtube.txt"} {
		if strings.Count(joined, host) != 2 {
			t.Fatalf("expected hostlist %q applied to exactly 2 profiles (tcp+udp): %s", host, joined)
		}
	}
	// ${BIN} must be resolved to the real bin dir.
	if strings.Contains(joined, "${BIN}") {
		t.Fatalf("unresolved ${BIN} in composed args: %s", joined)
	}
	if !strings.Contains(joined, "C:\\dropo\\bin\\tls_clienthello_www_google_com.bin") {
		t.Fatalf("bin path not resolved: %s", joined)
	}
	// Discord uses multisplit, YouTube uses fakedsplit — distinct per service.
	if !strings.Contains(joined, "--dpi-desync=multisplit") || !strings.Contains(joined, "--dpi-desync=fake,fakedsplit") {
		t.Fatalf("per-service methods not both present: %s", joined)
	}
	for _, expected := range []string{
		"--filter-tcp=2048,2053,2083,2087,2096,8443 --hostlist-domains=discord.media --dpi-desync=multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1",
		"--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake",
		"--dpi-desync-fake-discord=C:\\dropo\\bin\\quic_initial_dbankcloud_ru.bin",
		"--dpi-desync-fake-stun=C:\\dropo\\bin\\quic_initial_dbankcloud_ru.bin",
		"--dpi-desync-repeats=6",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing Discord media profile %q: %s", expected, joined)
		}
	}
	// Discord contributes 4 profiles, YouTube contributes 2 = 5 --new separators.
	if got := strings.Count(joined, "--new"); got != 5 {
		t.Fatalf("expected 5 --new separators for 6 profiles, got %d: %s", got, joined)
	}
}

func TestComposeServiceWinwsArgsKeepsNarrowFiltersWithoutDiscord(t *testing.T) {
	selections := []serviceWinwsSelection{
		{ServiceTag: "youtube", HostlistPath: "C:\\d\\youtube.txt", Method: methodFakedsplitTS("m2", "M2", googleQUICPayload)},
	}
	args := composeServiceWinwsArgs(selections, "C:\\dropo\\bin")
	joined := strings.Join(args, " ")

	if !strings.HasPrefix(joined, "--wf-tcp=80,443 --wf-udp=443") {
		t.Fatalf("non-Discord composed args must keep narrow wf header: %s", joined)
	}
	for _, unexpected := range []string{discordVoiceUDPPorts, discordMediaTCPPorts, "--filter-l7=discord,stun", "--hostlist-domains=discord.media"} {
		if strings.Contains(joined, unexpected) {
			t.Fatalf("non-Discord composed args unexpectedly contain %q: %s", unexpected, joined)
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
		// vpn-only and proxy-handled (tg-ws-proxy) services have no winws methods.
		if bt := serviceBlockType(svc.Tag); bt == "vpn" || bt == "proxy" {
			if serviceHasFreeBypass(svc.Tag) {
				t.Fatalf("%s (%s) must have no winws desync methods", svc.Tag, bt)
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
		for _, arg := range m.TCPArgs {
			if strings.Contains(arg, "--dpi-desync=fake,multisplit") {
				youtubeHasFakeMultisplit = true
			}
		}
	}
	if !youtubeHasFakeMultisplit {
		t.Fatal("youtube must include the fake,multisplit (fooling=ts) technique")
	}

	// Meta/WhatsApp are VPN-only; Telegram is proxy-handled (tg-ws-proxy) —
	// none of them are composed into the winws desync engine.
	for _, noDesync := range []string{"meta", "whatsapp", "telegram"} {
		if serviceHasFreeBypass(noDesync) {
			t.Fatalf("%s must not have winws desync methods", noDesync)
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
