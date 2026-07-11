package main

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ServiceBypassMethod is one ranked DPI-bypass technique for a single service.
// It is composed into the shared winws instance as TCP and UDP profiles scoped
// to that service's own hostlist, so different services can run different
// methods simultaneously inside one WinDivert filter. Discord additionally gets
// raw media profiles because desktop voice uses direct UDP packets, not only
// hostname-scoped HTTPS/QUIC. This is the key to "no single method fits all
// services": each service keeps its own best method.
//
// Discord/YouTube methods mirror the Flowseal zapret-discord-youtube bundle
// (https://github.com/Flowseal/zapret-discord-youtube). Other services seed
// from the same broadly-effective zapret techniques, ranked most→least likely
// to work; the ranking is refined per service over time and at build via the
// service-strategy update check.
type ServiceBypassMethod struct {
	Tag      string
	Label    string
	TCPArgs  []string // desync args for the service's tcp 80,443 profile (${BIN} allowed)
	UDPArgs  []string // desync args for the service's udp 443 (QUIC) profile (${BIN} allowed)
	Required []string // bundled .bin payloads this method needs
}

const (
	googleTLSPayload       = "tls_clienthello_www_google_com.bin"
	googleQUICPayload      = "quic_initial_www_google_com.bin"
	facebookQUICPayload    = "quic_initial_facebook_com.bin"
	discordQUICFakePayload = "quic_initial_dbankcloud_ru.bin"
	discordFakePayload     = "discord-ip-discovery-without-port.bin"
	stunFakePayload        = "stun.bin"
	discordMediaRawFilter  = "windivert_part.discord_media.txt"
	discordSTUNRawFilter   = "windivert_part.stun.txt"
)

// quicPayloadFile maps a service's "quic" hint to a bundled QUIC-initial payload.
// Meta/WhatsApp use the Facebook QUIC initial; everything else uses Google's.
func quicPayloadFile(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "facebook", "meta":
		return facebookQUICPayload
	default:
		return googleQUICPayload
	}
}

func fakeQUICArgs(quicFile string) []string {
	return []string{
		"--dpi-desync=fake",
		"--dpi-desync-repeats=6",
		"--dpi-desync-fake-quic=${BIN}" + quicFile,
	}
}

func methodMultisplit(tag, label string, seqovl, pos int, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=multisplit",
			fmt.Sprintf("--dpi-desync-split-seqovl=%d", seqovl),
			fmt.Sprintf("--dpi-desync-split-pos=%d", pos),
			"--dpi-desync-split-seqovl-pattern=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

// methodFakeMultisplit is the post-May-2026 Flowseal technique (ALT11 / FAKE TLS
// AUTO): fake,multisplit with seqovl + fooling=ts + repeats + a fake TLS payload.
// This is what currently defeats the updated ТСПУ DPI on Google/YouTube where
// plain multisplit stopped working.
func methodFakeMultisplit(tag, label string, seqovl, pos, repeats int, fakeTLSMod bool, quicFile string) ServiceBypassMethod {
	if repeats <= 0 {
		repeats = 8
	}
	tcp := []string{
		"--dpi-desync=fake,multisplit",
		fmt.Sprintf("--dpi-desync-split-seqovl=%d", seqovl),
		fmt.Sprintf("--dpi-desync-split-pos=%d", pos),
		"--dpi-desync-fooling=ts",
		fmt.Sprintf("--dpi-desync-repeats=%d", repeats),
		"--dpi-desync-split-seqovl-pattern=${BIN}" + googleTLSPayload,
	}
	if fakeTLSMod {
		tcp = append(tcp, "--dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com")
	} else {
		tcp = append(tcp, "--dpi-desync-fake-tls=${BIN}"+googleTLSPayload)
	}
	return ServiceBypassMethod{
		Tag:      tag,
		Label:    label,
		TCPArgs:  tcp,
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

func methodFakedsplitTS(tag, label, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=fake,fakedsplit",
			"--dpi-desync-repeats=6",
			"--dpi-desync-fooling=ts",
			"--dpi-desync-fakedsplit-pattern=0x00",
			"--dpi-desync-fake-tls=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

func methodMultidisorder(tag, label, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=fake,multidisorder",
			"--dpi-desync-split-pos=1,midsld",
			"--dpi-desync-repeats=6",
			"--dpi-desync-fooling=badseq",
			"--dpi-desync-fake-tls=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

func methodSplitAutottl(tag, label, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=fake,split",
			"--dpi-desync-autottl=5",
			"--dpi-desync-repeats=6",
			"--dpi-desync-fooling=badseq",
			"--dpi-desync-fake-tls=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

// methodFakeTTL is a TTL-based fake (a different mechanism class from TLS-split):
// a fake ClientHello with a low TTL + md5sig so it dies before the server but is
// seen by the DPI. Useful where stream-splitting is defeated.
func methodFakeTTL(tag, label string, ttl, repeats int, quicFile string) ServiceBypassMethod {
	if ttl <= 0 {
		ttl = 2
	}
	if repeats <= 0 {
		repeats = 6
	}
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=fake",
			fmt.Sprintf("--dpi-desync-ttl=%d", ttl),
			"--dpi-desync-fooling=md5sig",
			fmt.Sprintf("--dpi-desync-repeats=%d", repeats),
			"--dpi-desync-fake-tls=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

func methodFakeSplitMd5(tag, label, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:   tag,
		Label: label,
		TCPArgs: []string{
			"--dpi-desync=fake,split",
			"--dpi-desync-autottl=2",
			"--dpi-desync-fooling=md5sig",
			"--dpi-desync-repeats=6",
			"--dpi-desync-fake-tls=${BIN}" + googleTLSPayload,
		},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{googleTLSPayload, quicFile},
	}
}

// methodSyndata injects fake data into the TCP SYN so the DPI classifier locks
// onto the decoy. Distinct from ClientHello splitting.
func methodSyndata(tag, label, quicFile string) ServiceBypassMethod {
	return ServiceBypassMethod{
		Tag:      tag,
		Label:    label,
		TCPArgs:  []string{"--dpi-desync=syndata"},
		UDPArgs:  fakeQUICArgs(quicFile),
		Required: []string{quicFile},
	}
}

// baseRankedMethods is the compiled-in fallback ladder used only if the embedded
// per-service strategy file cannot be parsed.
func baseRankedMethods() []ServiceBypassMethod {
	return []ServiceBypassMethod{
		methodMultisplit("multisplit-652-2", "Multisplit seqovl=652 pos=2", 652, 2, googleQUICPayload),
		methodMultisplit("multisplit-568-1", "Multisplit seqovl=568 pos=1", 568, 1, googleQUICPayload),
		methodFakedsplitTS("fakedsplit-ts", "Fake+fakedsplit (ts)", googleQUICPayload),
		methodMultidisorder("multidisorder", "Fake+multidisorder", googleQUICPayload),
	}
}

// service_strategies.json is the per-service method database: each service maps
// to ONLY the bypass methods suitable for it, ranked most→least likely to work.
// It is the single source of truth for the search ladder and can be refreshed at
// build time without touching code.
//
//go:embed service_strategies.json
var serviceStrategiesJSON []byte

type serviceStrategyDoc struct {
	Version  int                           `json:"version"`
	Services map[string]serviceStrategyDef `json:"services"`
}

type serviceStrategyDef struct {
	Source string `json:"source"`
	// BlockType classifies HOW the service is blocked, which determines whether
	// desync (winws) can help at all: "dpi" = SNI/DPI block (desync works),
	// "ip" = IP-address block, "protocol" = protocol block (MTProto). Only "dpi"
	// is solvable by winws; "ip"/"protocol" lean on the VPN/direct fallback.
	BlockType string                      `json:"blockType"`
	Methods   []serviceStrategyMethodSpec `json:"methods"`
}

type serviceStrategyMethodSpec struct {
	Tag        string `json:"tag"`
	Label      string `json:"label"`
	Technique  string `json:"technique"`
	Quic       string `json:"quic"`
	Seqovl     int    `json:"seqovl"`
	Pos        int    `json:"pos"`
	Repeats    int    `json:"repeats"`
	Ttl        int    `json:"ttl"`
	FakeTLSMod bool   `json:"fakeTlsMod"` // use --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com
	IPIDZero   bool   `json:"ipIdZero"`   // prepend --ip-id=zero (Flowseal Google/YouTube profile)
}

var (
	serviceStrategiesOnce   sync.Once
	loadedServiceMethods    map[string][]ServiceBypassMethod
	loadedServiceVPNPref    map[string]bool
	loadedServiceBlockType  map[string]string
	loadedStrategiesVersion int
)

// serviceBlockType returns the classified blocking type for a service
// ("dpi"|"ip"|"protocol"), defaulting to "dpi" when unspecified.
func serviceBlockType(serviceTag string) string {
	serviceStrategiesOnce.Do(loadServiceStrategies)
	if bt := loadedServiceBlockType[serviceTag]; bt != "" {
		return bt
	}
	return "dpi"
}

// serviceStrategiesVersion is the version of the embedded service_strategies.json.
// The per-service cache is keyed to it so shipping a new ladder forces clients to
// re-search with the improved methods instead of reusing a stale cached choice.
func serviceStrategiesVersion() int {
	serviceStrategiesOnce.Do(loadServiceStrategies)
	return loadedStrategiesVersion
}

func buildMethodFromSpec(spec serviceStrategyMethodSpec) (ServiceBypassMethod, bool) {
	label := spec.Label
	if label == "" {
		label = spec.Tag
	}
	quic := quicPayloadFile(spec.Quic)
	var method ServiceBypassMethod
	switch spec.Technique {
	case "fake-multisplit":
		method = methodFakeMultisplit(spec.Tag, label, spec.Seqovl, spec.Pos, spec.Repeats, spec.FakeTLSMod, quic)
	case "multisplit":
		method = methodMultisplit(spec.Tag, label, spec.Seqovl, spec.Pos, quic)
	case "fakedsplit-ts":
		method = methodFakedsplitTS(spec.Tag, label, quic)
	case "multidisorder":
		method = methodMultidisorder(spec.Tag, label, quic)
	case "split-autottl":
		method = methodSplitAutottl(spec.Tag, label, quic)
	case "fake-ttl":
		method = methodFakeTTL(spec.Tag, label, spec.Ttl, spec.Repeats, quic)
	case "fake-split-md5sig":
		method = methodFakeSplitMd5(spec.Tag, label, quic)
	case "syndata":
		method = methodSyndata(spec.Tag, label, quic)
	default:
		return ServiceBypassMethod{}, false
	}
	if spec.IPIDZero {
		// --ip-id=zero must precede --dpi-desync in the profile.
		method.TCPArgs = append([]string{"--ip-id=zero"}, method.TCPArgs...)
	}
	return method, true
}

func loadServiceStrategies() {
	loadedServiceMethods = map[string][]ServiceBypassMethod{}
	loadedServiceVPNPref = map[string]bool{}
	loadedServiceBlockType = map[string]string{}
	var doc serviceStrategyDoc
	if err := json.Unmarshal(serviceStrategiesJSON, &doc); err != nil {
		return
	}
	loadedStrategiesVersion = doc.Version
	for tag, def := range doc.Services {
		methods := make([]ServiceBypassMethod, 0, len(def.Methods))
		for _, spec := range def.Methods {
			if m, ok := buildMethodFromSpec(spec); ok {
				methods = append(methods, m)
			}
		}
		if len(methods) > 0 {
			loadedServiceMethods[tag] = methods
		}
		blockType := def.BlockType
		if blockType == "" {
			blockType = "dpi"
		}
		loadedServiceBlockType[tag] = blockType
		// IP/protocol blocking cannot be fixed by desync → prefer VPN fallback.
		// "proxy" services are handled by a dedicated sidecar (tg-ws-proxy) and
		// "dpi" by winws, so neither prefers the VPN fallback.
		loadedServiceVPNPref[tag] = blockType == "ip" || blockType == "protocol" || blockType == "vpn"
	}
}

// DefaultServiceBypassMethods returns the ranked method ladder per service tag,
// loaded from the embedded service_strategies.json. Each service lists ONLY the
// methods suitable for it. Falls back to the compiled-in ladder if the file
// cannot be parsed.
func DefaultServiceBypassMethods() map[string][]ServiceBypassMethod {
	serviceStrategiesOnce.Do(loadServiceStrategies)
	if len(loadedServiceMethods) > 0 {
		out := make(map[string][]ServiceBypassMethod, len(loadedServiceMethods))
		for k, v := range loadedServiceMethods {
			out[k] = v
		}
		return out
	}
	fallback := map[string][]ServiceBypassMethod{}
	for _, svc := range DefaultFreeAccessServices {
		if svc.RequiresVPN {
			continue
		}
		fallback[svc.Tag] = baseRankedMethods()
	}
	return fallback
}

// serviceVpnPreferred reports whether a service is known to be IP-blocked
// (Telegram/Meta/X) so desync is unlikely to fully fix it — used to bias toward
// the VPN/direct fallback and to keep its desync ladder short.
func serviceVpnPreferred(serviceTag string) bool {
	serviceStrategiesOnce.Do(loadServiceStrategies)
	return loadedServiceVPNPref[serviceTag]
}

// rankedMethodsForService returns the ranked methods for a service. Services
// classified blockType:"vpn" have NO free desync (e.g. Meta/WhatsApp — IP-blocked)
// and return nil so they are never searched; they rely on the VPN/direct route.
func rankedMethodsForService(serviceTag string) []ServiceBypassMethod {
	// "vpn" (no free bypass) and "proxy" (handled by the tg-ws-proxy sidecar)
	// services have no winws methods and are never composed into the engine.
	if bt := serviceBlockType(serviceTag); bt == "vpn" || bt == "proxy" {
		return nil
	}
	if m, ok := DefaultServiceBypassMethods()[serviceTag]; ok && len(m) > 0 {
		return m
	}
	// Non-DPI services with no configured methods don't fall back to the base
	// desync ladder; only DPI services do.
	if serviceBlockType(serviceTag) != "dpi" {
		return nil
	}
	return baseRankedMethods()
}

// serviceHasFreeBypass reports whether the service has any free desync method to
// try. False means it needs a VPN/proxy subscription (or stays direct).
func serviceHasFreeBypass(serviceTag string) bool {
	return len(rankedMethodsForService(serviceTag)) > 0
}

func findServiceBypassMethod(serviceTag, methodTag string) (ServiceBypassMethod, bool) {
	for _, m := range rankedMethodsForService(serviceTag) {
		if m.Tag == methodTag {
			return m, true
		}
	}
	return ServiceBypassMethod{}, false
}

// serviceWinwsSelection binds a service's hostlist file to the method that will
// handle its traffic in the composed winws instance.
type serviceWinwsSelection struct {
	ServiceTag   string
	HostlistPath string
	Method       ServiceBypassMethod
}

const (
	// Discord voice gateway endpoints are commonly returned as
	// *.discord.media:2048; keep it with the alternate media TCP ports so voice
	// channel join handshakes do not bypass winws while regular Discord works.
	discordMediaTCPPorts = "2048,2053,2083,2087,2096,8443"
	discordVoiceUDPPorts = "19294-19344,50000-50100"
)

func hasDiscordSelection(selections []serviceWinwsSelection) bool {
	for _, sel := range selections {
		if strings.EqualFold(sel.ServiceTag, "discord") {
			return true
		}
	}
	return false
}

// composeServiceWinwsArgs builds a single winws argument list where each service
// is its own pair of profiles (tcp 80,443 and udp 443) scoped to its hostlist.
// Discord also contributes voice/media profiles for raw UDP and discord.media
// alternate TCP ports, matching the standalone zapret Discord preset. winws
// matches a packet against the first profile whose filter+scope match, and the
// per-service scopes are disjoint, so every service is handled by its own method
// without conflicting with the others.
func composeServiceWinwsArgs(selections []serviceWinwsSelection, binDir string) []string {
	binPrefix := binDir
	if binPrefix != "" && !strings.HasSuffix(binPrefix, string(os.PathSeparator)) {
		binPrefix += string(os.PathSeparator)
	}
	resolve := func(args []string) []string {
		out := make([]string, 0, len(args))
		for _, a := range args {
			out = append(out, strings.ReplaceAll(a, "${BIN}", binPrefix))
		}
		return out
	}

	discordSelected := hasDiscordSelection(selections)
	tcpPorts := "80,443"
	udpPorts := "443"
	if discordSelected {
		tcpPorts += "," + discordMediaTCPPorts
		udpPorts += "," + discordVoiceUDPPorts
	}

	profiles := make([][]string, 0, len(selections)*2+2)
	for _, sel := range selections {
		if sel.HostlistPath == "" {
			continue
		}
		tcp := append([]string{"--filter-tcp=80,443", "--hostlist=" + sel.HostlistPath}, resolve(sel.Method.TCPArgs)...)
		udp := append([]string{"--filter-udp=443", "--hostlist=" + sel.HostlistPath}, resolve(sel.Method.UDPArgs)...)
		profiles = append(profiles, tcp, udp)
		if strings.EqualFold(sel.ServiceTag, "discord") {
			mediaTCP := []string{
				"--filter-tcp=" + discordMediaTCPPorts,
				"--hostlist-domains=discord.media",
				"--dpi-desync=multisplit",
				"--dpi-desync-split-seqovl=681",
				"--dpi-desync-split-pos=1",
				"--dpi-desync-split-seqovl-pattern=" + binPrefix + googleTLSPayload,
			}
			voiceUDP := []string{
				"--filter-udp=" + discordVoiceUDPPorts,
				"--filter-l7=discord,stun",
				"--dpi-desync=fake",
				"--dpi-desync-fake-discord=" + binPrefix + discordQUICFakePayload,
				"--dpi-desync-fake-stun=" + binPrefix + discordQUICFakePayload,
				"--dpi-desync-repeats=6",
			}
			profiles = append(profiles, mediaTCP, voiceUDP)
		}
	}
	if len(profiles) == 0 {
		return nil
	}

	args := []string{"--wf-tcp=" + tcpPorts, "--wf-udp=" + udpPorts}
	if discordSelected {
		args = append(args,
			"--wf-raw-part=@"+binPrefix+discordMediaRawFilter,
			"--wf-raw-part=@"+binPrefix+discordSTUNRawFilter,
		)
	}
	for i, profile := range profiles {
		if i > 0 {
			args = append(args, "--new")
		}
		args = append(args, profile...)
	}
	return args
}

// serviceHostlistPath is the per-service hostlist file location.
func serviceHostlistPath(dir, serviceTag string) string {
	return filepath.Join(dir, "zapret-host-"+safeLogFilePart(serviceTag)+".txt")
}

// ensureServiceHostlist writes (and returns) the hostlist file for one service,
// containing only that service's domain suffixes.
func ensureServiceHostlist(dir string, svc FreeAccessService) (string, error) {
	domains := make([]string, 0, len(svc.DomainSuffixes))
	for _, suffix := range svc.DomainSuffixes {
		normalized := strings.TrimSpace(strings.TrimPrefix(suffix, "."))
		if normalized != "" {
			domains = append(domains, normalized)
		}
	}
	domains = uniqueStrings(domains)
	if len(domains) == 0 {
		return "", fmt.Errorf("service %q has no domains for a hostlist", svc.Tag)
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := serviceHostlistPath(dir, svc.Tag)
	if err := os.WriteFile(path, []byte(strings.Join(domains, "\n")+"\n"), 0644); err != nil {
		return "", err
	}
	return path, nil
}
