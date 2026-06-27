package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ProxyConfig represents a parsed proxy configuration.
type ProxyConfig struct {
	Type        string `json:"type"`
	Tag         string `json:"tag"`
	Server      string `json:"server"`
	ServerPort  int    `json:"server_port"`
	UUID        string `json:"uuid,omitempty"`     // VLESS/VMess/TUIC
	Password    string `json:"password,omitempty"` // Trojan/SS/Hysteria2
	Method      string `json:"method,omitempty"`   // Shadowsocks
	Flow        string `json:"flow,omitempty"`     // VLESS
	Network     string `json:"network,omitempty"`  // tcp/ws/grpc
	Security    string `json:"security,omitempty"` // tls/reality
	SNI         string `json:"sni,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	PublicKey   string `json:"public_key,omitempty"` // Reality
	ShortID     string `json:"short_id,omitempty"`   // Reality
	Path        string `json:"path,omitempty"`       // WebSocket/XHTTP
	Host        string `json:"host,omitempty"`       // WebSocket/XHTTP
	Mode        string `json:"mode,omitempty"`       // XHTTP
	Extra       string `json:"extra,omitempty"`      // XHTTP JSON options
	ALPN        string `json:"alpn,omitempty"`       // TLS ALPN list
	HeaderType  string `json:"header_type,omitempty"`
	Name        string `json:"name"` // Display name
	// Hysteria2/TUIC specific
	Obfs              string `json:"obfs,omitempty"`               // Hysteria2 obfs type
	ObfsPassword      string `json:"obfs_password,omitempty"`      // Hysteria2 obfs password
	UpMbps            int    `json:"up_mbps,omitempty"`            // Hysteria2 upload speed
	DownMbps          int    `json:"down_mbps,omitempty"`          // Hysteria2 download speed
	CongestionControl string `json:"congestion_control,omitempty"` // TUIC
	UDPRelayMode      string `json:"udp_relay_mode,omitempty"`     // TUIC
}

// SubscriptionFetcher handles subscription URL fetching and parsing.
type SubscriptionFetcher struct {
	client *http.Client
}

// NewSubscriptionFetcher creates a new fetcher with default timeout.
func NewSubscriptionFetcher() *SubscriptionFetcher {
	return &SubscriptionFetcher{
		client: HTTPClient, // Use shared client with DefaultHTTPTimeout
	}
}

// FetchAndParse fetches subscription URL and parses proxy configs.
func (f *SubscriptionFetcher) FetchAndParse(subscriptionURL string) ([]ProxyConfig, error) {
	// Fetch subscription
	resp, err := f.client.Get(subscriptionURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("subscription returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	return f.ParseSubscription(string(body))
}

// ParseSubscription parses subscription content (base64 or plain text)
func (f *SubscriptionFetcher) ParseSubscription(content string) ([]ProxyConfig, error) {
	// Try base64 decode
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
	if err != nil {
		// Not base64, try as plain text
		decoded = []byte(content)
	}

	// Split by newlines
	lines := strings.Split(string(decoded), "\n")
	var configs []ProxyConfig

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var cfg ProxyConfig
		var parseErr error

		switch {
		case strings.HasPrefix(line, "vless://"):
			cfg, parseErr = parseVLESS(line)
		case strings.HasPrefix(line, "trojan://"):
			cfg, parseErr = parseTrojan(line)
		case strings.HasPrefix(line, "ss://"):
			cfg, parseErr = parseShadowsocks(line)
		case strings.HasPrefix(line, "vmess://"):
			cfg, parseErr = parseVMess(line)
		case strings.HasPrefix(line, "hysteria2://"), strings.HasPrefix(line, "hy2://"):
			cfg, parseErr = parseHysteria2(line)
		case strings.HasPrefix(line, "tuic://"):
			cfg, parseErr = parseTUIC(line)
		default:
			continue // Skip unknown protocols
		}

		if parseErr != nil {
			// Log error but continue
			fmt.Printf("Warning: failed to parse line %d: %v\n", i, parseErr)
			continue
		}

		// Generate tag if not set
		if cfg.Tag == "" {
			cfg.Tag = fmt.Sprintf("%s-%d", cfg.Type, i)
		}

		configs = append(configs, cfg)
	}

	return configs, nil
}

// ParseSingleLink parses a single proxy link
func (f *SubscriptionFetcher) ParseSingleLink(link string) (ProxyConfig, error) {
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
		return ProxyConfig{}, fmt.Errorf("unknown protocol: %s", link[:min(20, len(link))])
	}
}

// parseVLESS parses vless:// link
// Format: vless://uuid@server:port?params#name
func parseVLESS(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "vless"}

	// Remove prefix and parse URL
	link = strings.TrimPrefix(link, "vless://")

	// Split name (after #)
	parts := strings.SplitN(link, "#", 2)
	if len(parts) == 2 {
		name, _ := url.QueryUnescape(parts[1])
		cfg.Name = name
	}
	link = parts[0]

	// Parse as URL
	u, err := url.Parse("vless://" + link)
	if err != nil {
		return cfg, fmt.Errorf("invalid vless URL: %w", err)
	}

	// Extract UUID
	cfg.UUID = u.User.Username()

	// Extract server and port
	cfg.Server = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	cfg.ServerPort = port

	// Parse query params
	q := u.Query()
	cfg.Security = q.Get("security")
	cfg.Network = q.Get("type")
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}
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

	return cfg, nil
}

// parseTrojan parses trojan:// link
// Format: trojan://password@server:port?params#name
func parseTrojan(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "trojan"}

	// Remove prefix
	link = strings.TrimPrefix(link, "trojan://")

	// Split name (after #)
	parts := strings.SplitN(link, "#", 2)
	if len(parts) == 2 {
		name, _ := url.QueryUnescape(parts[1])
		cfg.Name = name
	}
	link = parts[0]

	// Parse as URL
	u, err := url.Parse("trojan://" + link)
	if err != nil {
		return cfg, fmt.Errorf("invalid trojan URL: %w", err)
	}

	// Extract password
	cfg.Password = u.User.Username()

	// Extract server and port
	cfg.Server = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	cfg.ServerPort = port

	// Parse query params
	q := u.Query()
	cfg.Security = q.Get("security")
	if cfg.Security == "" {
		cfg.Security = "tls"
	}
	cfg.Network = q.Get("type")
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}
	cfg.SNI = q.Get("sni")
	cfg.Fingerprint = q.Get("fp")
	cfg.Path = q.Get("path")
	cfg.Host = q.Get("host")
	cfg.ALPN = q.Get("alpn")
	cfg.HeaderType = q.Get("headerType")

	return cfg, nil
}

// parseShadowsocks parses ss:// link
// Format: ss://base64(method:password)@server:port#name
// or: ss://base64(method:password@server:port)#name
func parseShadowsocks(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "shadowsocks"}

	// Remove prefix
	link = strings.TrimPrefix(link, "ss://")

	// Split name (after #)
	parts := strings.SplitN(link, "#", 2)
	if len(parts) == 2 {
		name, _ := url.QueryUnescape(parts[1])
		cfg.Name = name
	}
	link = parts[0]

	// Try to find @ separator
	if atIdx := strings.LastIndex(link, "@"); atIdx != -1 {
		// Format: base64(method:password)@server:port
		userInfo := link[:atIdx]
		serverInfo := link[atIdx+1:]

		// Decode userInfo
		decoded, err := base64.RawURLEncoding.DecodeString(userInfo)
		if err != nil {
			decoded, err = base64.StdEncoding.DecodeString(userInfo)
			if err != nil {
				return cfg, fmt.Errorf("failed to decode ss userinfo: %w", err)
			}
		}

		// Parse method:password
		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			return cfg, fmt.Errorf("invalid ss userinfo format")
		}
		cfg.Method = parts[0]
		cfg.Password = parts[1]

		// Parse server:port
		hostPort := strings.Split(serverInfo, ":")
		if len(hostPort) != 2 {
			return cfg, fmt.Errorf("invalid ss server:port format")
		}
		cfg.Server = hostPort[0]
		port, _ := strconv.Atoi(hostPort[1])
		cfg.ServerPort = port
	} else {
		// Format: base64(method:password@server:port)
		decoded, err := base64.RawURLEncoding.DecodeString(link)
		if err != nil {
			decoded, err = base64.StdEncoding.DecodeString(link)
			if err != nil {
				return cfg, fmt.Errorf("failed to decode ss link: %w", err)
			}
		}

		// Parse method:password@server:port
		parts := strings.SplitN(string(decoded), "@", 2)
		if len(parts) != 2 {
			return cfg, fmt.Errorf("invalid ss format")
		}

		userParts := strings.SplitN(parts[0], ":", 2)
		if len(userParts) != 2 {
			return cfg, fmt.Errorf("invalid ss method:password format")
		}
		cfg.Method = userParts[0]
		cfg.Password = userParts[1]

		hostPort := strings.Split(parts[1], ":")
		if len(hostPort) != 2 {
			return cfg, fmt.Errorf("invalid ss server:port format")
		}
		cfg.Server = hostPort[0]
		port, _ := strconv.Atoi(hostPort[1])
		cfg.ServerPort = port
	}

	return cfg, nil
}

// parseVMess parses vmess:// link (base64 JSON format)
func parseVMess(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "vmess"}

	// Remove prefix
	link = strings.TrimPrefix(link, "vmess://")

	// Decode base64
	decoded, err := base64.StdEncoding.DecodeString(link)
	if err != nil {
		decoded, err = base64.RawURLEncoding.DecodeString(link)
		if err != nil {
			return cfg, fmt.Errorf("failed to decode vmess: %w", err)
		}
	}

	// Parse JSON
	var vmessConfig struct {
		V    string `json:"v"`
		PS   string `json:"ps"`   // Name
		Add  string `json:"add"`  // Server
		Port any    `json:"port"` // Port (can be string or int)
		ID   string `json:"id"`   // UUID
		Aid  any    `json:"aid"`  // Alter ID
		Net  string `json:"net"`  // Network
		Type string `json:"type"` // Header type
		Host string `json:"host"`
		Path string `json:"path"`
		TLS  string `json:"tls"`
		SNI  string `json:"sni"`
	}

	if err := json.Unmarshal(decoded, &vmessConfig); err != nil {
		return cfg, fmt.Errorf("failed to parse vmess JSON: %w", err)
	}

	cfg.Name = vmessConfig.PS
	cfg.Server = vmessConfig.Add
	cfg.UUID = vmessConfig.ID
	cfg.Network = vmessConfig.Net
	if cfg.Network == "" {
		cfg.Network = "tcp"
	}
	cfg.Host = vmessConfig.Host
	cfg.Path = vmessConfig.Path
	cfg.SNI = vmessConfig.SNI
	if vmessConfig.TLS == "tls" {
		cfg.Security = "tls"
	}

	// Handle port as string or int
	switch p := vmessConfig.Port.(type) {
	case float64:
		cfg.ServerPort = int(p)
	case string:
		cfg.ServerPort, _ = strconv.Atoi(p)
	}

	return cfg, nil
}

// ToSingboxOutbound converts ProxyConfig to sing-box outbound format
func (p *ProxyConfig) ToSingboxOutbound() map[string]interface{} {
	out := map[string]interface{}{
		"type":        p.Type,
		"tag":         p.Tag,
		"server":      p.Server,
		"server_port": p.ServerPort,
	}

	switch p.Type {
	case "vless":
		out["uuid"] = p.UUID
		if p.Flow != "" {
			out["flow"] = p.Flow
		}

		// TLS settings
		if p.Security == "tls" || p.Security == "reality" {
			tls := map[string]interface{}{
				"enabled": true,
			}
			if p.SNI != "" {
				tls["server_name"] = p.SNI
			}
			if p.Fingerprint != "" {
				tls["utls"] = map[string]interface{}{
					"enabled":     true,
					"fingerprint": p.Fingerprint,
				}
			}
			if p.Security == "reality" {
				tls["reality"] = map[string]interface{}{
					"enabled":    true,
					"public_key": p.PublicKey,
					"short_id":   p.ShortID,
				}
			}
			out["tls"] = tls
		}

		// Transport
		if p.Network != "" && p.Network != "tcp" {
			out["transport"] = buildTransport(p)
		}

	case "trojan":
		out["password"] = p.Password

		// TLS settings
		tls := map[string]interface{}{
			"enabled": true,
		}
		if p.SNI != "" {
			tls["server_name"] = p.SNI
		}
		if p.Fingerprint != "" {
			tls["utls"] = map[string]interface{}{
				"enabled":     true,
				"fingerprint": p.Fingerprint,
			}
		}
		out["tls"] = tls

		// Transport
		if p.Network != "" && p.Network != "tcp" {
			out["transport"] = buildTransport(p)
		}

	case "shadowsocks":
		out["method"] = p.Method
		out["password"] = p.Password

	case "socks":
		out["version"] = "5"

	case "vmess":
		out["uuid"] = p.UUID
		out["security"] = "auto"

		// TLS settings
		if p.Security == "tls" {
			tls := map[string]interface{}{
				"enabled": true,
			}
			if p.SNI != "" {
				tls["server_name"] = p.SNI
			}
			out["tls"] = tls
		}

		// Transport
		if p.Network != "" && p.Network != "tcp" {
			out["transport"] = buildTransport(p)
		}

	case "hysteria2":
		out["password"] = p.Password

		// TLS (обязательно для hysteria2)
		tls := map[string]interface{}{
			"enabled": true,
		}
		if p.SNI != "" {
			tls["server_name"] = p.SNI
		}
		out["tls"] = tls

		// Obfuscation
		if p.Obfs != "" && p.ObfsPassword != "" {
			out["obfs"] = map[string]interface{}{
				"type":     p.Obfs,
				"password": p.ObfsPassword,
			}
		}

		// Speed limits
		if p.UpMbps > 0 {
			out["up_mbps"] = p.UpMbps
		}
		if p.DownMbps > 0 {
			out["down_mbps"] = p.DownMbps
		}

	case "tuic":
		out["uuid"] = p.UUID
		out["password"] = p.Password
		out["congestion_control"] = p.CongestionControl
		out["udp_relay_mode"] = p.UDPRelayMode

		// TLS (обязательно для TUIC)
		tls := map[string]interface{}{
			"enabled": true,
		}
		if p.SNI != "" {
			tls["server_name"] = p.SNI
		}
		if p.Fingerprint != "" {
			tls["alpn"] = []string{p.Fingerprint}
		}
		out["tls"] = tls
	}

	return out
}

func buildTransport(p *ProxyConfig) map[string]interface{} {
	transport := map[string]interface{}{
		"type": p.Network,
	}

	switch p.Network {
	case "ws":
		if p.Path != "" {
			transport["path"] = p.Path
		}
		if p.Host != "" {
			transport["headers"] = map[string]interface{}{
				"Host": p.Host,
			}
		}
	case "grpc":
		if p.Path != "" {
			transport["service_name"] = p.Path
		}
	case "http":
		if p.Path != "" {
			transport["path"] = p.Path
		}
		if p.Host != "" {
			transport["host"] = []string{p.Host}
		}
	}

	return transport
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseHysteria2 parses hysteria2:// or hy2:// link
// Format: hysteria2://password@server:port?params#name
func parseHysteria2(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "hysteria2"}

	// Remove prefix
	link = strings.TrimPrefix(link, "hysteria2://")
	link = strings.TrimPrefix(link, "hy2://")

	// Split name (after #)
	parts := strings.SplitN(link, "#", 2)
	if len(parts) == 2 {
		name, _ := url.QueryUnescape(parts[1])
		cfg.Name = name
	}
	link = parts[0]

	// Parse as URL
	u, err := url.Parse("hysteria2://" + link)
	if err != nil {
		return cfg, fmt.Errorf("invalid hysteria2 URL: %w", err)
	}

	// Extract password
	cfg.Password = u.User.Username()

	// Extract server and port
	cfg.Server = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	cfg.ServerPort = port

	// Parse query params
	q := u.Query()
	cfg.SNI = q.Get("sni")
	if cfg.SNI == "" {
		cfg.SNI = cfg.Server
	}
	cfg.Fingerprint = q.Get("pinSHA256")
	cfg.Obfs = q.Get("obfs")
	cfg.ObfsPassword = q.Get("obfs-password")

	// Parse speeds
	if up := q.Get("up"); up != "" {
		fmt.Sscanf(up, "%d", &cfg.UpMbps)
	}
	if down := q.Get("down"); down != "" {
		fmt.Sscanf(down, "%d", &cfg.DownMbps)
	}

	return cfg, nil
}

// parseTUIC parses tuic:// link
// Format: tuic://uuid:password@server:port?params#name
func parseTUIC(link string) (ProxyConfig, error) {
	cfg := ProxyConfig{Type: "tuic"}

	// Remove prefix
	link = strings.TrimPrefix(link, "tuic://")

	// Split name (after #)
	parts := strings.SplitN(link, "#", 2)
	if len(parts) == 2 {
		name, _ := url.QueryUnescape(parts[1])
		cfg.Name = name
	}
	link = parts[0]

	// Parse as URL
	u, err := url.Parse("tuic://" + link)
	if err != nil {
		return cfg, fmt.Errorf("invalid tuic URL: %w", err)
	}

	// Extract UUID and password
	cfg.UUID = u.User.Username()
	cfg.Password, _ = u.User.Password()

	// Extract server and port
	cfg.Server = u.Hostname()
	port, _ := strconv.Atoi(u.Port())
	cfg.ServerPort = port

	// Parse query params
	q := u.Query()
	cfg.SNI = q.Get("sni")
	if cfg.SNI == "" {
		cfg.SNI = cfg.Server
	}
	cfg.CongestionControl = q.Get("congestion_control")
	if cfg.CongestionControl == "" {
		cfg.CongestionControl = "cubic"
	}
	cfg.UDPRelayMode = q.Get("udp_relay_mode")
	if cfg.UDPRelayMode == "" {
		cfg.UDPRelayMode = "native"
	}
	cfg.Fingerprint = q.Get("alpn")

	return cfg, nil
}
