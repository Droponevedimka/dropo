package dropocore

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type proxyConfig struct {
	Type              string
	Raw               string
	Tag               string
	Server            string
	ServerPort        int
	UUID              string
	Password          string
	Method            string
	Flow              string
	Network           string
	Security          string
	SNI               string
	Fingerprint       string
	PublicKey         string
	ShortID           string
	Path              string
	Host              string
	Mode              string
	Extra             string
	ALPN              string
	HeaderType        string
	Name              string
	Obfs              string
	ObfsPassword      string
	UpMbps            int
	DownMbps          int
	CongestionControl string
	UDPRelayMode      string
}

type subscriptionFetcher struct {
	client *http.Client
}

func newSubscriptionFetcher() *subscriptionFetcher {
	return &subscriptionFetcher{
		client: &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if err := validateSubscriptionURL(req.URL.String()); err != nil {
					return err
				}
				if len(via) >= 10 {
					return fmt.Errorf("too many subscription redirects")
				}
				return nil
			},
		},
	}
}

func (f *subscriptionFetcher) fetchAndParse(subscriptionURL string) ([]proxyConfig, error) {
	if err := validateSubscriptionURL(subscriptionURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, subscriptionURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "dropo-android")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch subscription: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned HTTP %d", resp.StatusCode)
	}
	body, err := readHTTPBodyLimited(resp.Body, maxAndroidSubscriptionBytes)
	if err != nil {
		return nil, fmt.Errorf("read subscription: %w", err)
	}
	return f.parseSubscription(string(body))
}

func validateSubscriptionURL(rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid subscription URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || u.Hostname() == "" {
		return fmt.Errorf("subscription URL must use HTTPS")
	}
	if u.User != nil {
		return fmt.Errorf("subscription URL must not contain embedded credentials")
	}
	return nil
}

func (f *subscriptionFetcher) parseSubscription(content string) ([]proxyConfig, error) {
	body := strings.TrimSpace(content)
	if decoded, err := decodeBase64(body); err == nil && looksLikeSubscription(string(decoded)) {
		body = string(decoded)
	}

	var configs []proxyConfig
	for i, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cfg, err := f.parseSingleLink(line)
		if err != nil {
			continue
		}
		cfg.Raw = line
		if cfg.Tag == "" {
			cfg.Tag = generateProxyTag(cfg, i)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

func (f *subscriptionFetcher) parseSingleLink(link string) (proxyConfig, error) {
	link = strings.TrimSpace(link)
	switch {
	case strings.HasPrefix(link, "vless://"):
		return parseVLESS(link)
	case strings.HasPrefix(link, "trojan://"):
		return parseTrojan(link)
	case strings.HasPrefix(link, "ss://"):
		return parseShadowsocks(link)
	case strings.HasPrefix(link, "vmess://"):
		return parseVMess(link)
	case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):
		return parseHysteria2(link)
	case strings.HasPrefix(link, "tuic://"):
		return parseTUIC(link)
	default:
		return proxyConfig{}, fmt.Errorf("unsupported proxy link")
	}
}

func parseVLESS(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "vless"}
	u, err := parseProxyURL(link, "vless")
	if err != nil {
		return cfg, err
	}
	cfg.Name = fragmentName(u)
	cfg.UUID = u.User.Username()
	cfg.Server = u.Hostname()
	cfg.ServerPort = parsePort(u.Port())
	q := u.Query()
	cfg.Security = q.Get("security")
	cfg.Network = normalizeTransport(q.Get("type"))
	cfg.SNI = q.Get("sni")
	cfg.Fingerprint = q.Get("fp")
	cfg.Flow = q.Get("flow")
	cfg.PublicKey = q.Get("pbk")
	cfg.ShortID = q.Get("sid")
	cfg.Path = q.Get("path")
	cfg.Host = q.Get("host")
	cfg.Mode = q.Get("mode")
	cfg.Extra = q.Get("extra")
	cfg.ALPN = q.Get("alpn")
	cfg.HeaderType = q.Get("headerType")
	return cfg, validateProxy(cfg)
}

func parseTrojan(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "trojan"}
	u, err := parseProxyURL(link, "trojan")
	if err != nil {
		return cfg, err
	}
	cfg.Name = fragmentName(u)
	cfg.Password = u.User.Username()
	cfg.Server = u.Hostname()
	cfg.ServerPort = parsePort(u.Port())
	q := u.Query()
	cfg.Security = q.Get("security")
	if cfg.Security == "" {
		cfg.Security = "tls"
	}
	cfg.Network = normalizeTransport(q.Get("type"))
	cfg.SNI = q.Get("sni")
	cfg.Fingerprint = q.Get("fp")
	cfg.Path = q.Get("path")
	cfg.Host = q.Get("host")
	cfg.ALPN = q.Get("alpn")
	cfg.HeaderType = q.Get("headerType")
	return cfg, validateProxy(cfg)
}

func parseShadowsocks(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "shadowsocks"}
	raw := strings.TrimPrefix(link, "ss://")
	parts := strings.SplitN(raw, "#", 2)
	if len(parts) == 2 {
		cfg.Name, _ = url.QueryUnescape(parts[1])
	}
	raw = parts[0]
	raw = strings.SplitN(raw, "?", 2)[0]

	if at := strings.LastIndex(raw, "@"); at >= 0 {
		userInfo := raw[:at]
		serverInfo := raw[at+1:]
		decoded, err := decodeBase64OrRaw(userInfo)
		if err != nil {
			return cfg, err
		}
		methodPassword := strings.SplitN(decoded, ":", 2)
		if len(methodPassword) != 2 {
			return cfg, fmt.Errorf("invalid shadowsocks credentials")
		}
		cfg.Method = methodPassword[0]
		cfg.Password = methodPassword[1]
		if err := fillHostPort(serverInfo, &cfg); err != nil {
			return cfg, err
		}
		return cfg, validateProxy(cfg)
	}

	decoded, err := decodeBase64(raw)
	if err != nil {
		return cfg, err
	}
	compound := strings.SplitN(string(decoded), "@", 2)
	if len(compound) != 2 {
		return cfg, fmt.Errorf("invalid shadowsocks link")
	}
	methodPassword := strings.SplitN(compound[0], ":", 2)
	if len(methodPassword) != 2 {
		return cfg, fmt.Errorf("invalid shadowsocks credentials")
	}
	cfg.Method = methodPassword[0]
	cfg.Password = methodPassword[1]
	if err := fillHostPort(compound[1], &cfg); err != nil {
		return cfg, err
	}
	return cfg, validateProxy(cfg)
}

func parseVMess(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "vmess"}
	decoded, err := decodeBase64(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		return cfg, err
	}
	var vmess struct {
		PS   string `json:"ps"`
		Add  string `json:"add"`
		Port any    `json:"port"`
		ID   string `json:"id"`
		Net  string `json:"net"`
		Type string `json:"type"`
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
	}
	if err := json.Unmarshal(decoded, &vmess); err != nil {
		return cfg, err
	}
	cfg.Name = vmess.PS
	cfg.Server = vmess.Add
	cfg.UUID = vmess.ID
	cfg.Network = normalizeTransport(vmess.Net)
	cfg.HeaderType = vmess.Type
	cfg.Host = vmess.Host
	cfg.Path = vmess.Path
	cfg.SNI = vmess.SNI
	if vmess.TLS == "tls" {
		cfg.Security = "tls"
	}
	switch port := vmess.Port.(type) {
	case float64:
		cfg.ServerPort = int(port)
	case string:
		cfg.ServerPort = parsePort(port)
	}
	return cfg, validateProxy(cfg)
}

func parseHysteria2(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "hysteria2"}
	normalized := strings.TrimPrefix(strings.TrimPrefix(link, "hysteria2://"), "hy2://")
	u, err := url.Parse("hysteria2://" + normalized)
	if err != nil {
		return cfg, err
	}
	cfg.Name = fragmentName(u)
	cfg.Password = u.User.Username()
	cfg.Server = u.Hostname()
	cfg.ServerPort = parsePort(u.Port())
	q := u.Query()
	cfg.SNI = q.Get("sni")
	if cfg.SNI == "" {
		cfg.SNI = cfg.Server
	}
	cfg.Fingerprint = q.Get("pinSHA256")
	cfg.Obfs = q.Get("obfs")
	cfg.ObfsPassword = firstNonEmpty(q.Get("obfs-password"), q.Get("obfs_password"))
	cfg.UpMbps = parsePort(q.Get("up"))
	cfg.DownMbps = parsePort(q.Get("down"))
	return cfg, validateProxy(cfg)
}

func parseTUIC(link string) (proxyConfig, error) {
	cfg := proxyConfig{Type: "tuic"}
	u, err := parseProxyURL(link, "tuic")
	if err != nil {
		return cfg, err
	}
	cfg.Name = fragmentName(u)
	cfg.UUID = u.User.Username()
	cfg.Password, _ = u.User.Password()
	cfg.Server = u.Hostname()
	cfg.ServerPort = parsePort(u.Port())
	q := u.Query()
	cfg.SNI = q.Get("sni")
	if cfg.SNI == "" {
		cfg.SNI = cfg.Server
	}
	cfg.CongestionControl = firstNonEmpty(q.Get("congestion_control"), "cubic")
	cfg.UDPRelayMode = firstNonEmpty(q.Get("udp_relay_mode"), "native")
	cfg.ALPN = q.Get("alpn")
	return cfg, validateProxy(cfg)
}

func parseProxyURL(link, scheme string) (*url.URL, error) {
	raw := strings.TrimPrefix(link, scheme+"://")
	return url.Parse(scheme + "://" + raw)
}

func fillHostPort(value string, cfg *proxyConfig) error {
	if host, port, err := net.SplitHostPort(value); err == nil {
		cfg.Server = strings.Trim(host, "[]")
		cfg.ServerPort = parsePort(port)
		return nil
	}
	lastColon := strings.LastIndex(value, ":")
	if lastColon < 0 {
		return fmt.Errorf("missing server port")
	}
	cfg.Server = strings.Trim(value[:lastColon], "[]")
	cfg.ServerPort = parsePort(value[lastColon+1:])
	return nil
}

func validateProxy(cfg proxyConfig) error {
	if cfg.Server == "" || cfg.ServerPort <= 0 {
		return fmt.Errorf("missing server or port")
	}
	switch cfg.Type {
	case "vless", "vmess", "tuic":
		if cfg.UUID == "" {
			return fmt.Errorf("missing uuid")
		}
	case "trojan", "hysteria2":
		if cfg.Password == "" {
			return fmt.Errorf("missing password")
		}
	case "shadowsocks":
		if cfg.Method == "" || cfg.Password == "" {
			return fmt.Errorf("missing method or password")
		}
	}
	return nil
}

func decodeBase64OrRaw(value string) (string, error) {
	if decoded, err := decodeBase64(value); err == nil {
		return string(decoded), nil
	}
	return url.QueryUnescape(value)
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if decoded, err := enc.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	if rem := len(value) % 4; rem != 0 {
		padded := value + strings.Repeat("=", 4-rem)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding} {
			if decoded, err := enc.DecodeString(padded); err == nil {
				return decoded, nil
			}
		}
	}
	return nil, fmt.Errorf("invalid base64")
}

func looksLikeSubscription(value string) bool {
	return strings.Contains(value, "://") || strings.Contains(value, "\n")
}

func isDirectProxyLink(value string) bool {
	value = strings.TrimSpace(value)
	return strings.HasPrefix(value, "vless://") ||
		strings.HasPrefix(value, "trojan://") ||
		strings.HasPrefix(value, "ss://") ||
		strings.HasPrefix(value, "vmess://") ||
		strings.HasPrefix(value, "hysteria2://") ||
		strings.HasPrefix(value, "hy2://") ||
		strings.HasPrefix(value, "tuic://")
}

func normalizeTransport(transport string) string {
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "" {
		return "tcp"
	}
	if transport == "splithttp" {
		return "xhttp"
	}
	return transport
}

func isTransportSupported(transport string) bool {
	switch normalizeTransport(transport) {
	case "kcp", "mkcp", "xhttp":
		return false
	default:
		return true
	}
}

func parsePort(value string) int {
	port, _ := strconv.Atoi(strings.TrimSpace(value))
	return port
}

func fragmentName(u *url.URL) string {
	if u == nil || u.Fragment == "" {
		return ""
	}
	name, err := url.QueryUnescape(u.Fragment)
	if err != nil {
		return u.Fragment
	}
	return name
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
