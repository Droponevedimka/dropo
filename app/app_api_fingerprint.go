package main

// DPI fingerprint capture. Runs on the END USER's machine (in the censored
// network) to classify HOW each blocked service is being blocked — RST on SNI,
// silent drop, IP-block, DNS poisoning — and writes a JSON the user sends back.
// We merge those into the local censor lab (see testlab/) so DPI bypass can be
// developed/regression-tested outside RF without round-tripping builds.
//
// Taxonomy follows the community tools rkn-block-checker / dpi-detector:
//   dns:  ok | poisoned | nxdomain | error
//   tcp:  ok | timeout | refused | error
//   tls:  ok | rst | drop | error
//   verdict: ok | dns-poison | ip-block | tls-rst | tls-drop | unknown
//
// IMPORTANT: must run with the bypass OFF (VPN disconnected) so winws/WinDivert
// does not mask the provider's real behaviour.

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsRuntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

type FingerprintServiceResult struct {
	Tag     string   `json:"tag"`
	Name    string   `json:"name"`
	Host    string   `json:"host"`
	DNS     string   `json:"dns"`
	IPs     []string `json:"ips,omitempty"`
	TCP     string   `json:"tcp"`
	TLS     string   `json:"tls"`
	Verdict string   `json:"verdict"`
	Detail  string   `json:"detail,omitempty"`
}

type DPIFingerprint struct {
	Schema     int                        `json:"schema"`
	CapturedAt string                     `json:"capturedAt"`
	App        string                     `json:"app"`
	AppVersion string                     `json:"appVersion"`
	Country    string                     `json:"country"`
	Services   []FingerprintServiceResult `json:"services"`
}

type FingerprintResult struct {
	Success      bool           `json:"success"`
	Error        string         `json:"error,omitempty"`
	Path         string         `json:"path,omitempty"`
	BlockedCount int            `json:"blockedCount"`
	Total        int            `json:"total"`
	Fingerprint  DPIFingerprint `json:"fingerprint"`
}

// CaptureDPIFingerprint probes every catalogued blocked service directly (no
// bypass) and classifies the censorship, then writes the fingerprint to
// resources/fingerprints/ and returns it. The user sends the file to the dev.
func (a *App) CaptureDPIFingerprint() FingerprintResult {
	res := FingerprintResult{}
	if a.isInitialized() && a.isRunning {
		res.Error = "Сначала отключите VPN — при включённом обходе отпечаток будет недостоверным."
		return res
	}

	fp := DPIFingerprint{
		Schema:     1,
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		App:        "dropo",
		AppVersion: Version,
		Country:    detectCountryBestEffort(),
	}

	services := DefaultFreeAccessServices
	res.Total = len(services)
	for i, svc := range services {
		host := fingerprintProbeHost(svc)
		if host == "" {
			continue
		}
		a.emitFingerprintProgress(i+1, len(services), svc.DisplayName)
		r := classifyHost(svc.Tag, svc.DisplayName, host)
		if r.Verdict != "ok" {
			res.BlockedCount++
		}
		fp.Services = append(fp.Services, r)
	}

	res.Fingerprint = fp
	path, err := a.writeFingerprintFile(fp)
	if err != nil {
		res.Error = fmt.Sprintf("Отпечаток собран, но не удалось сохранить файл: %v", err)
		return res
	}
	res.Path = path
	res.Success = true
	a.writeLog(fmt.Sprintf("[Fingerprint] captured: %d/%d services blocked, saved to %s", res.BlockedCount, res.Total, path))
	return res
}

// OpenFingerprintFolder opens the folder containing saved fingerprints so the
// user can attach the file to a message.
func (a *App) OpenFingerprintFolder() {
	dir := a.fingerprintDir()
	_ = os.MkdirAll(dir, 0755)
	if err := openExternalURL(dir); err != nil {
		a.writeLog(fmt.Sprintf("[Fingerprint] could not open folder: %v", err))
	}
}

func (a *App) emitFingerprintProgress(done, total int, name string) {
	if a.ctx == nil {
		return
	}
	wailsRuntime.EventsEmit(a.ctx, "fingerprint-progress", map[string]interface{}{
		"done": done, "total": total, "name": name,
	})
}

func (a *App) fingerprintDir() string {
	base := a.basePath
	if a.storage != nil && a.storage.resourcesPath != "" {
		base = a.storage.resourcesPath
	} else {
		base = filepath.Join(base, "resources")
	}
	return filepath.Join(base, "fingerprints")
}

func (a *App) writeFingerprintFile(fp DPIFingerprint) (string, error) {
	dir := a.fingerprintDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("dpi-fingerprint-%s.json", time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(fp, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// fingerprintProbeHost picks a real hostname to probe for a service.
func fingerprintProbeHost(svc FreeAccessService) string {
	if h := hostFromURL(svc.HealthURL); h != "" {
		return h
	}
	for _, u := range svc.ProbeURLs {
		if h := hostFromURL(u); h != "" {
			return h
		}
	}
	if len(svc.DomainSuffixes) > 0 {
		d := svc.DomainSuffixes[0]
		if strings.Count(d, ".") < 2 {
			return "www." + d
		}
		return d
	}
	return ""
}

func hostFromURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Hostname()
}

// classifyHost does the layered probe: DNS -> TCP -> TLS(SNI).
func classifyHost(tag, name, host string) FingerprintServiceResult {
	r := FingerprintServiceResult{Tag: tag, Name: name, Host: host}

	ips, err := net.LookupHost(host)
	switch {
	case err != nil && strings.Contains(strings.ToLower(err.Error()), "no such host"):
		r.DNS, r.Verdict, r.Detail = "nxdomain", "dns-poison", err.Error()
		return r
	case err != nil:
		r.DNS, r.Verdict, r.Detail = "error", "unknown", err.Error()
		return r
	}
	r.IPs = ips
	if dnsLooksPoisoned(ips) {
		r.DNS, r.Verdict = "poisoned", "dns-poison"
		return r
	}
	r.DNS = "ok"

	ip := ips[0]
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip, "443"), 4*time.Second)
	if err != nil {
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "refused") {
			r.TCP, r.Verdict, r.Detail = "refused", "ip-block", err.Error()
		} else {
			r.TCP, r.Verdict, r.Detail = "timeout", "ip-block", err.Error()
		}
		return r
	}
	r.TCP = "ok"
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	tlsConn := tls.Client(conn, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
	err = tlsConn.Handshake()
	if err == nil {
		r.TLS, r.Verdict = "ok", "ok"
		return r
	}
	low := strings.ToLower(err.Error())
	switch {
	case strings.Contains(low, "reset") || strings.Contains(low, "forcibly closed") || strings.Contains(low, "aborted") || strings.Contains(low, "broken pipe"):
		r.TLS, r.Verdict, r.Detail = "rst", "tls-rst", err.Error()
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline") || strings.Contains(low, "i/o timeout"):
		r.TLS, r.Verdict, r.Detail = "drop", "tls-drop", err.Error()
	case strings.Contains(low, "eof"):
		// connection closed right after ClientHello — treat as reset-like.
		r.TLS, r.Verdict, r.Detail = "rst", "tls-rst", err.Error()
	default:
		// Server responded (e.g. cert/alert) -> not censored by SNI.
		r.TLS, r.Verdict, r.Detail = "ok", "ok", err.Error()
	}
	return r
}

// dnsLooksPoisoned flags obvious sinkhole answers (private/loopback/zero) RKN
// resolvers sometimes return for blocked domains.
func dnsLooksPoisoned(ips []string) bool {
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() {
			return true
		}
	}
	return false
}

func detectCountryBestEffort() string {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("https://ipinfo.io/country")
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
	c := strings.TrimSpace(string(b))
	if c == "" {
		return "unknown"
	}
	return c
}
