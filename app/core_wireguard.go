package main

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// UserWireGuardConfig представляет пользовательскую конфигурацию WireGuard (из стандартного WG конфига)
type UserWireGuardConfig struct {
	Tag                 string   `json:"tag"`                            // Уникальный тег (латиница, без пробелов)
	Name                string   `json:"name"`                           // Отображаемое имя
	PrivateKey          string   `json:"private_key"`                    // [Interface] PrivateKey
	LocalAddress        []string `json:"local_address"`                  // [Interface] Address
	DNS                 string   `json:"dns,omitempty"`                  // [Interface] DNS (опционально)
	MTU                 int      `json:"mtu,omitempty"`                  // [Interface] MTU (опционально)
	PublicKey           string   `json:"public_key"`                     // [Peer] PublicKey
	PresharedKey        string   `json:"preshared_key,omitempty"`        // [Peer] PresharedKey (опционально)
	AllowedIPs          []string `json:"allowed_ips"`                    // [Peer] AllowedIPs
	Endpoint            string   `json:"endpoint"`                       // [Peer] Endpoint (host без порта)
	EndpointPort        int      `json:"endpoint_port"`                  // Порт из Endpoint
	PersistentKeepalive int      `json:"persistent_keepalive,omitempty"` // [Peer] PersistentKeepalive
	// CamouflageEnabled enables the Windows-only zapret2 handshake camouflage
	// sidecar. Native WireGuard remains the tunnel implementation.
	CamouflageEnabled bool `json:"camouflage_enabled,omitempty"`

	// Внутренние домены для этого VPN (опционально, пользователь может добавить вручную)
	// Примеры: [".company.local", ".internal.corp", ".test-test.com"]
	// Если пусто - автоматически извлекаются из Endpoint
	InternalDomains []string `json:"internal_domains,omitempty"`
}

// ParseWireGuardConfig парсит стандартный WireGuard конфиг
func ParseWireGuardConfig(config string) (*UserWireGuardConfig, error) {
	wg := &UserWireGuardConfig{
		LocalAddress: []string{},
		AllowedIPs:   []string{},
		MTU:          1280, // Default MTU
	}

	lines := strings.Split(config, "\n")
	currentSection := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Пропускаем пустые строки и комментарии
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Определяем секцию
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}

		// Парсим key = value
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(strings.ToLower(parts[0]))
		value := strings.TrimSpace(parts[1])

		switch currentSection {
		case "interface":
			switch key {
			case "privatekey":
				wg.PrivateKey = value
			case "address":
				// Может быть несколько адресов через запятую
				addresses := strings.Split(value, ",")
				for _, addr := range addresses {
					addr = strings.TrimSpace(addr)
					if addr != "" {
						wg.LocalAddress = append(wg.LocalAddress, addr)
					}
				}
			case "dns":
				// Берём только первый DNS сервер
				dnsServers := strings.Split(value, ",")
				if len(dnsServers) > 0 {
					wg.DNS = strings.TrimSpace(dnsServers[0])
				}
			case "mtu":
				if mtu, err := strconv.Atoi(value); err == nil {
					wg.MTU = mtu
				}
			}

		case "peer":
			switch key {
			case "publickey":
				wg.PublicKey = value
			case "presharedkey":
				wg.PresharedKey = value
			case "allowedips":
				// Может быть несколько IP через запятую
				ips := strings.Split(value, ",")
				for _, ip := range ips {
					ip = strings.TrimSpace(ip)
					if ip != "" {
						wg.AllowedIPs = append(wg.AllowedIPs, ip)
					}
				}
			case "endpoint":
				host, port, err := parseWireGuardEndpoint(value)
				if err == nil {
					wg.Endpoint = host
					wg.EndpointPort = port
				} else {
					wg.Endpoint = value
				}
			case "persistentkeepalive":
				if keepalive, err := strconv.Atoi(value); err == nil {
					wg.PersistentKeepalive = keepalive
				}
			}
		}
	}

	// Валидация обязательных полей
	if wg.PrivateKey == "" {
		return nil, fmt.Errorf("отсутствует PrivateKey")
	}
	if len(wg.LocalAddress) == 0 {
		return nil, fmt.Errorf("отсутствует Address")
	}
	if wg.PublicKey == "" {
		return nil, fmt.Errorf("отсутствует PublicKey")
	}
	if len(wg.AllowedIPs) == 0 {
		return nil, fmt.Errorf("отсутствует AllowedIPs")
	}
	if wg.Endpoint == "" {
		return nil, fmt.Errorf("отсутствует Endpoint")
	}
	if wg.EndpointPort <= 0 {
		return nil, fmt.Errorf("Endpoint должен содержать корректный порт")
	}

	return wg, nil
}

func parseWireGuardEndpoint(value string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return "", 0, fmt.Errorf("некорректный Endpoint: %w", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("некорректный порт Endpoint")
	}
	if host == "" {
		return "", 0, fmt.Errorf("пустой хост Endpoint")
	}
	return strings.Trim(host, "[]"), port, nil
}

// ValidateTag проверяет корректность тега (латиница, без пробелов)
func ValidateTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("тег не может быть пустым")
	}
	if len(tag) > 32 {
		return fmt.Errorf("тег слишком длинный (макс. 32 символа)")
	}

	// Только латиница, цифры, дефис и подчёркивание
	validTag := regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
	if !validTag.MatchString(tag) {
		return fmt.Errorf("тег должен начинаться с буквы и содержать только латинские буквы, цифры, дефис или подчёркивание")
	}

	return nil
}

// ValidateAllowedIPs проверяет AllowedIPs на конфликты с sing-box TUN
// Возвращает ошибку если AllowedIPs содержат 0.0.0.0/0 или ::/0 (конфликт с sing-box)
func ValidateAllowedIPs(allowedIPs []string) error {
	conflictingCIDRs := []string{
		"0.0.0.0/0",
		"::/0",
		"0.0.0.0/1",
		"128.0.0.0/1",
	}

	for _, ip := range allowedIPs {
		ip = strings.TrimSpace(ip)
		for _, conflict := range conflictingCIDRs {
			if ip == conflict {
				return fmt.Errorf("AllowedIPs содержит %s, что конфликтует с sing-box TUN. "+
					"Используйте конкретные подсети вместо полного перенаправления трафика", ip)
			}
		}
	}
	return nil
}

// ExtractNetworksFromAllowedIPs извлекает сетевые адреса из AllowedIPs для DNS bypass
// Возвращает список CIDR, которые относятся к WireGuard сетям
func ExtractNetworksFromAllowedIPs(allowedIPs []string) []string {
	var networks []string
	for _, cidr := range allowedIPs {
		cidr = strings.TrimSpace(cidr)
		// Проверяем что это валидный CIDR
		_, _, err := net.ParseCIDR(cidr)
		if err != nil {
			// Может быть одиночный IP, добавляем /32
			if ip := net.ParseIP(cidr); ip != nil {
				if ip.To4() != nil {
					cidr = cidr + "/32"
				} else {
					cidr = cidr + "/128"
				}
			} else {
				continue
			}
		}
		networks = append(networks, cidr)
	}
	return networks
}

// GenerateRouteRulesForWireGuard генерирует правила маршрутизации для WireGuard
func GenerateRouteRulesForWireGuard(configs []UserWireGuardConfig) []map[string]interface{} {
	rules := []map[string]interface{}{}

	for _, wg := range configs {
		// Правило для AllowedIPs -> этот WireGuard
		if len(wg.AllowedIPs) > 0 {
			rules = append(rules, map[string]interface{}{
				"ip_cidr":  wg.AllowedIPs,
				"outbound": wg.Tag,
			})
		}
	}

	return rules
}

// WireGuardInfo информация для UI
type WireGuardInfo struct {
	Tag               string   `json:"tag"`
	Name              string   `json:"name"`
	Endpoint          string   `json:"endpoint"`
	AllowedIPs        []string `json:"allowed_ips"`
	InternalDomains   []string `json:"internal_domains,omitempty"`
	CamouflageEnabled bool     `json:"camouflage_enabled,omitempty"`
}

// ToInfo конвертирует в структуру для UI
func (wg *UserWireGuardConfig) ToInfo() WireGuardInfo {
	endpoint := wg.Endpoint
	if wg.EndpointPort > 0 {
		endpoint = fmt.Sprintf("%s:%d", wg.Endpoint, wg.EndpointPort)
	}

	return WireGuardInfo{
		Tag:               wg.Tag,
		Name:              wg.Name,
		Endpoint:          endpoint,
		AllowedIPs:        wg.AllowedIPs,
		InternalDomains:   wg.InternalDomains,
		CamouflageEnabled: wg.CamouflageEnabled,
	}
}

// GetInternalDomains возвращает все внутренние домены для этого WireGuard конфига
// Если InternalDomains задан явно - возвращает его
// Иначе пытается автоматически извлечь из Endpoint
func (wg *UserWireGuardConfig) GetInternalDomains() []string {
	// Если пользователь явно указал домены - используем их
	if len(wg.InternalDomains) > 0 {
		return wg.InternalDomains
	}

	// Автоматическое извлечение из Endpoint
	domains := []string{}

	if wg.Endpoint != "" {
		// Извлекаем домен из endpoint (например, vpn.company.local -> .company.local)
		parts := strings.Split(wg.Endpoint, ".")
		if len(parts) >= 2 {
			// Берём последние 2+ части как возможные внутренние домены
			// vpn.internal.company.local -> [.company.local, .internal.company.local]
			for i := len(parts) - 2; i >= 0 && i >= len(parts)-3; i-- {
				domain := "." + strings.Join(parts[i:], ".")
				if !isStandardInternalDomain(domain) && !isPublicDomain(domain) {
					domains = append(domains, domain)
				}
			}
		}
	}

	return domains
}

// CollectAllInternalDomains собирает все внутренние домены из всех WireGuard конфигов
// Возвращает уникальный список доменов для DNS rules
func CollectAllInternalDomains(configs []UserWireGuardConfig) []string {
	seen := make(map[string]bool)
	var domains []string

	for _, wg := range configs {
		for _, domain := range wg.GetInternalDomains() {
			domain = strings.ToLower(strings.TrimSpace(domain))
			if domain == "" {
				continue
			}
			// Добавляем точку в начало если нет
			if !strings.HasPrefix(domain, ".") {
				domain = "." + domain
			}
			if !seen[domain] {
				seen[domain] = true
				domains = append(domains, domain)
			}
		}
	}

	return domains
}

// isStandardInternalDomain проверяет является ли домен стандартным внутренним
// Эти домены уже есть в template.json, не нужно дублировать
func isStandardInternalDomain(domain string) bool {
	standardDomains := []string{
		".local", ".internal", ".corp", ".lan", ".home", ".intranet", ".private",
	}
	domain = strings.ToLower(domain)
	for _, std := range standardDomains {
		if domain == std {
			return true
		}
	}
	return false
}

// isPublicDomain проверяет является ли домен публичным
func isPublicDomain(domain string) bool {
	publicTLDs := []string{
		".com", ".net", ".org", ".io", ".dev", ".app", ".co", ".me", ".info",
		".ru", ".su", ".рф", ".ua", ".by", ".kz", ".uz",
		".de", ".fr", ".uk", ".eu", ".us", ".cn", ".jp", ".kr",
		".edu", ".gov", ".mil",
	}
	domain = strings.ToLower(domain)
	for _, tld := range publicTLDs {
		if strings.HasSuffix(domain, tld) {
			return true
		}
	}
	return false
}

// WireGuardConfig is the format used by NativeWireGuardManager
type WireGuardConfig struct {
	PrivateKey string
	Address    []string
	DNS        string
	MTU        int
	Peers      []WireGuardPeer
}

// WireGuardPeer represents a WireGuard peer configuration
type WireGuardPeer struct {
	PublicKey           string
	PresharedKey        string
	Endpoint            string
	Port                int
	AllowedIPs          []string
	PersistentKeepalive int
}

// ToWireGuardConfig converts UserWireGuardConfig to WireGuardConfig for native manager
func (wg *UserWireGuardConfig) ToWireGuardConfig() *WireGuardConfig {
	return &WireGuardConfig{
		PrivateKey: wg.PrivateKey,
		Address:    wg.LocalAddress,
		DNS:        wg.DNS,
		MTU:        wg.MTU,
		Peers: []WireGuardPeer{
			{
				PublicKey:           wg.PublicKey,
				PresharedKey:        wg.PresharedKey,
				Endpoint:            wg.Endpoint,
				Port:                wg.EndpointPort,
				AllowedIPs:          wg.AllowedIPs,
				PersistentKeepalive: wg.PersistentKeepalive,
			},
		},
	}
}
