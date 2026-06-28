package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UserSettings хранит настройки пользователя
type UserSettings struct {
	SubscriptionURL  string                `json:"subscription_url"`  // URL подписки или прямая ссылка vless/trojan/etc
	LastUpdated      string                `json:"last_updated"`      // Время последнего обновления
	ProxyCount       int                   `json:"proxy_count"`       // Количество прокси из подписки
	WireGuardConfigs []UserWireGuardConfig `json:"wireguard_configs"` // WireGuard конфиги (до 20)
}

// ConfigBuilder генерирует config.json из template.json и подписки
type ConfigBuilder struct {
	templatePath    string
	basePath        string
	activeProfileID int
	routingMode     RoutingMode    // Current routing mode
	filterManager   *FilterManager // Filter manager for rule-sets
	fetcher         *SubscriptionFetcher
}

// NewConfigBuilder создаёт новый ConfigBuilder
func NewConfigBuilder(basePath string) *ConfigBuilder {
	cb := &ConfigBuilder{
		templatePath:    filepath.Join(basePath, "template.json"),
		basePath:        basePath,
		activeProfileID: DefaultProfileID,
		routingMode:     DefaultRoutingMode,
		filterManager:   NewFilterManager(filepath.Dir(basePath)), // bin/filters is sibling to resources
		fetcher:         NewSubscriptionFetcher(),
	}
	return cb
}

// getSettingsPathForProfile возвращает путь к настройкам для конкретного профиля
func (b *ConfigBuilder) getSettingsPathForProfile(profileID int) string {
	if profileID == DefaultProfileID {
		return filepath.Join(b.basePath, "user_settings.json")
	}
	return filepath.Join(b.basePath, fmt.Sprintf("user_settings_%d.json", profileID))
}

// getConfigPathForProfile возвращает путь к config.json для конкретного профиля
func (b *ConfigBuilder) getConfigPathForProfile(profileID int) string {
	if profileID == DefaultProfileID {
		return filepath.Join(b.basePath, "config.json")
	}
	return filepath.Join(b.basePath, fmt.Sprintf("config_%d.json", profileID))
}

// GetConfigPath возвращает путь к config.json для текущего профиля
func (b *ConfigBuilder) GetConfigPath() string {
	return b.getConfigPathForProfile(b.activeProfileID)
}

// SetActiveProfile переключает ConfigBuilder на указанный профиль
func (b *ConfigBuilder) SetActiveProfile(profileID int) {
	b.activeProfileID = profileID
}

// GetActiveProfileID возвращает ID активного профиля
func (b *ConfigBuilder) GetActiveProfileID() int {
	return b.activeProfileID
}

// SetRoutingMode sets the routing mode for config generation
func (b *ConfigBuilder) SetRoutingMode(mode RoutingMode) {
	b.routingMode = mode
}

// GetRoutingMode returns current routing mode
func (b *ConfigBuilder) GetRoutingMode() RoutingMode {
	return b.routingMode
}

// GetFilterManager returns the filter manager
func (b *ConfigBuilder) GetFilterManager() *FilterManager {
	return b.filterManager
}

// GetSettingsPath returns the path to settings file for current profile
func (b *ConfigBuilder) GetSettingsPath() string {
	return b.getSettingsPathForProfile(b.activeProfileID)
}

// LoadUserSettings загружает настройки пользователя для текущего профиля
func (b *ConfigBuilder) LoadUserSettings() (*UserSettings, error) {
	return b.LoadUserSettingsForProfile(b.activeProfileID)
}

// LoadUserSettingsForProfile загружает настройки для указанного профиля
func (b *ConfigBuilder) LoadUserSettingsForProfile(profileID int) (*UserSettings, error) {
	settingsPath := b.getSettingsPathForProfile(profileID)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &UserSettings{}, nil
		}
		return nil, err
	}

	var settings UserSettings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil, err
	}

	return &settings, nil
}

// SaveUserSettings сохраняет настройки пользователя для текущего профиля
func (b *ConfigBuilder) SaveUserSettings(settings *UserSettings) error {
	return b.SaveUserSettingsForProfile(b.activeProfileID, settings)
}

// SaveUserSettingsForProfile сохраняет настройки для указанного профиля
func (b *ConfigBuilder) SaveUserSettingsForProfile(profileID int, settings *UserSettings) error {
	settingsPath := b.getSettingsPathForProfile(profileID)
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(settingsPath, data, 0644)
}

// TestSubscription тестирует подписку и возвращает информацию о доступных прокси
func (b *ConfigBuilder) TestSubscription(subscriptionURL string) (*SubscriptionTestResult, error) {
	result := &SubscriptionTestResult{
		Success: false,
		Proxies: []ProxyInfo{},
	}

	// Определяем тип: это подписка URL или прямая ссылка
	isDirectLink := strings.HasPrefix(subscriptionURL, "vless://") ||
		strings.HasPrefix(subscriptionURL, "trojan://") ||
		strings.HasPrefix(subscriptionURL, "ss://") ||
		strings.HasPrefix(subscriptionURL, "vmess://")

	var proxies []ProxyConfig
	var err error

	if isDirectLink {
		// Парсим как одну ссылку
		proxy, err := b.fetcher.ParseSingleLink(subscriptionURL)
		if err != nil {
			result.Error = fmt.Sprintf("Ошибка парсинга ссылки: %v", err)
			return result, nil
		}
		proxies = []ProxyConfig{proxy}
	} else {
		// Парсим как подписку URL
		proxies, err = b.fetcher.FetchAndParse(subscriptionURL)
		if err != nil {
			result.Error = fmt.Sprintf("Ошибка загрузки подписки: %v", err)
			return result, nil
		}
	}

	// Filter unsupported transports (e.g., xhttp which is Xray-only)
	filterResult := FilterUnsupportedTransports(proxies)
	proxies = filterResult.Supported

	if len(proxies) == 0 {
		if filterResult.AllFiltered {
			result.Error = filterResult.Message
		} else {
			result.Error = "Подписка не содержит доступных прокси"
		}
		return result, nil
	}

	result.Success = true
	result.Count = len(proxies)
	result.IsDirectLink = isDirectLink

	// Add warning about filtered proxies
	if len(filterResult.Filtered) > 0 {
		result.Warning = filterResult.Message
		result.FilteredCount = len(filterResult.Filtered)
	}

	for _, p := range proxies {
		result.Proxies = append(result.Proxies, ProxyInfo{
			Type:   p.Type,
			Raw:    p.Raw,
			Name:   p.Name,
			Server: p.Server,
			Port:   p.ServerPort,
		})
	}

	return result, nil
}

// SubscriptionTestResult результат тестирования подписки
type SubscriptionTestResult struct {
	Success       bool        `json:"success"`
	Error         string      `json:"error,omitempty"`
	Warning       string      `json:"warning,omitempty"`
	Count         int         `json:"count"`
	FilteredCount int         `json:"filtered_count,omitempty"`
	IsDirectLink  bool        `json:"is_direct_link"`
	Proxies       []ProxyInfo `json:"proxies"`
}

// ProxyInfo информация о прокси для UI
type ProxyInfo struct {
	Type   string `json:"type"`
	Raw    string `json:"raw,omitempty"`
	Name   string `json:"name"`
	Server string `json:"server"`
	Port   int    `json:"port"`
}

// BuildConfig генерирует config.json из template и подписки
func (b *ConfigBuilder) BuildConfig(subscriptionURL string) error {
	// Загружаем текущие настройки для получения WireGuard конфигов
	settings, err := b.LoadUserSettings()
	if err != nil {
		settings = &UserSettings{}
	}

	return b.BuildConfigFull(subscriptionURL, settings.WireGuardConfigs)
}

// BuildConfigFull генерирует config.json с полным контролем над настройками
func (b *ConfigBuilder) BuildConfigFull(subscriptionURL string, wireGuardConfigs []UserWireGuardConfig) error {
	fmt.Printf("[BuildConfigFull] Called with %d WireGuard configs\n", len(wireGuardConfigs))
	for i, wg := range wireGuardConfigs {
		fmt.Printf("[BuildConfigFull] WireGuard[%d]: tag=%s, dns=%s, allowedIPs=%v\n", i, wg.Tag, wg.DNS, wg.AllowedIPs)
	}

	// Загружаем template
	templateData, err := os.ReadFile(b.templatePath)
	if err != nil {
		return fmt.Errorf("не удалось загрузить template.json: %w", err)
	}

	var template map[string]interface{}
	if err := json.Unmarshal(templateData, &template); err != nil {
		return fmt.Errorf("ошибка парсинга template.json: %w", err)
	}

	// Добавляем DNS серверы и правила для WireGuard сетей
	// (WireGuard работает нативно, DNS запросы к корпоративным доменам
	//  идут через direct, а WireGuard интерфейс их перехватывает)
	fmt.Printf("[BuildConfigFull] Calling addWireGuardDNS with %d configs...\n", len(wireGuardConfigs))
	b.addWireGuardDNS(template, wireGuardConfigs)

	// Обновляем route rules для WireGuard AllowedIPs
	fmt.Printf("[BuildConfigFull] Calling updateRouteRulesForWireGuard...\n")
	b.updateRouteRulesForWireGuard(template, wireGuardConfigs)

	// Получаем прокси из подписки
	var proxies []ProxyConfig

	if subscriptionURL != "" {
		isDirectLink := strings.HasPrefix(subscriptionURL, "vless://") ||
			strings.HasPrefix(subscriptionURL, "trojan://") ||
			strings.HasPrefix(subscriptionURL, "ss://") ||
			strings.HasPrefix(subscriptionURL, "vmess://")

		if isDirectLink {
			proxy, err := b.fetcher.ParseSingleLink(subscriptionURL)
			if err != nil {
				return fmt.Errorf("ошибка парсинга ссылки: %w", err)
			}
			proxy.Tag = generateTag(proxy, 0)
			proxies = []ProxyConfig{proxy}
		} else {
			proxies, err = b.fetcher.FetchAndParse(subscriptionURL)
			if err != nil {
				return fmt.Errorf("ошибка загрузки подписки: %w", err)
			}
			// Генерируем теги для прокси
			for i := range proxies {
				proxies[i].Tag = generateTag(proxies[i], i)
			}
		}

		// Filter unsupported transports (e.g., xhttp which is Xray-only)
		filterResult := FilterUnsupportedTransports(proxies)
		if filterResult.AllFiltered {
			return fmt.Errorf("%s", filterResult.Message)
		}
		if len(filterResult.Filtered) > 0 {
			fmt.Printf("[BuildConfigFull] Warning: %s\n", filterResult.Message)
		}
		proxies = filterResult.Supported
	}

	// Генерируем outbounds (WireGuard теперь управляется Native WireGuard Manager)
	outbounds := b.generateOutbounds(template, proxies)
	template["outbounds"] = outbounds

	// WireGuard управляется отдельно через Native WireGuard Manager
	// Удаляем endpoints секцию если она осталась от старого конфига
	delete(template, "endpoints")

	// Применяем режим маршрутизации (blocked_only, except_russia, all_traffic)
	b.applyRoutingMode(template)

	// Добавляем experimental секцию с clash_api для статистики трафика
	b.addExperimentalAPI(template)

	// Удаляем служебные поля из template
	delete(template, "outbounds_template")
	delete(template, "_comment_outbounds")
	delete(template, "_comment_outbounds")

	// Сохраняем config.json для текущего профиля
	configData, err := json.MarshalIndent(template, "", "  ")
	if err != nil {
		return fmt.Errorf("ошибка сериализации config: %w", err)
	}

	configPath := b.GetConfigPath()
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		return fmt.Errorf("ошибка сохранения config.json: %w", err)
	}

	// Сохраняем настройки пользователя
	settings := &UserSettings{
		SubscriptionURL:  subscriptionURL,
		LastUpdated:      time.Now().Format("2006-01-02 15:04:05"),
		ProxyCount:       len(proxies),
		WireGuardConfigs: wireGuardConfigs,
	}

	if err := b.SaveUserSettings(settings); err != nil {
		return fmt.Errorf("ошибка сохранения настроек: %w", err)
	}

	return nil
}

// generateOutbounds генерирует список outbounds
// WireGuard теперь управляется через Native WireGuard Manager, не sing-box
func (b *ConfigBuilder) generateOutbounds(template map[string]interface{}, proxies []ProxyConfig) []interface{} {
	outbounds := []interface{}{}
	proxyTags := []string{}

	// WireGuard управляется через Native WireGuard Manager
	// Не добавляем WireGuard outbounds в sing-box

	// Добавляем прокси из подписки
	for _, p := range proxies {
		outbounds = append(outbounds, p.ToSingboxOutbound())
		proxyTags = append(proxyTags, p.Tag)
	}

	// Получаем шаблоны outbounds
	outboundsTemplate, ok := template["outbounds_template"].(map[string]interface{})
	if !ok {
		outboundsTemplate = map[string]interface{}{}
	}

	// Если есть прокси, добавляем selector и urltest
	if len(proxyTags) > 0 {
		// URLTest для автовыбора
		outbounds = append(outbounds, buildAutoSelectOutbound(proxyTags))

		// Selector для ручного выбора
		selectorOutbounds := append([]string{"auto-select"}, proxyTags...)
		selectorOutbounds = append(selectorOutbounds, "direct")

		if selector, ok := outboundsTemplate["selector"].(map[string]interface{}); ok {
			selector = copyMap(selector)
			selector["outbounds"] = selectorOutbounds
			outbounds = append(outbounds, selector)
		} else {
			outbounds = append(outbounds, map[string]interface{}{
				"type":      "selector",
				"tag":       "proxy",
				"outbounds": selectorOutbounds,
				"default":   "auto-select",
			})
		}
	} else {
		// Если нет прокси, создаём простой selector с direct
		outbounds = append(outbounds, map[string]interface{}{
			"type":      "selector",
			"tag":       "proxy",
			"outbounds": []string{"direct"},
			"default":   "direct",
		})
	}

	// Добавляем direct
	if direct, ok := outboundsTemplate["direct"].(map[string]interface{}); ok {
		outbounds = append(outbounds, copyMap(direct))
	} else {
		outbounds = append(outbounds, map[string]interface{}{
			"type": "direct",
			"tag":  "direct",
		})
	}

	// Примечание: block и dns-out удалены - в sing-box 1.11+ используются rule actions
	// action: "reject" вместо outbound: "block"
	// action: "hijack-dns" вместо outbound: "dns-out"

	return outbounds
}

// addWireGuardDNS настраивает DNS для WireGuard конфигов
//
// ВАЖНО: Внутренние домены (.local, .internal, .corp, etc.) теперь резолвятся
// через dns-local (системный резолвер) в template.json, который автоматически
// использует DNS из WireGuard интерфейса на основе системных маршрутов.
//
// Эта функция добавляет ДОПОЛНИТЕЛЬНЫЕ правила для внутренних доменов,
// которые должны резолвиться через системный DNS (WireGuard DNS)
func (b *ConfigBuilder) addWireGuardDNS(config map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	dns, ok := config["dns"].(map[string]interface{})
	if !ok {
		return
	}

	// Получаем существующие DNS rules
	dnsRules, _ := dns["rules"].([]interface{})
	if dnsRules == nil {
		dnsRules = []interface{}{}
	}

	// Собираем все внутренние домены из всех WireGuard конфигов
	collectedDomains := CollectAllInternalDomains(wireGuardConfigs)

	// Фильтруем старые WireGuard DNS правила (по domain_suffix совпадению)
	filteredRules := []interface{}{}
	for _, rule := range dnsRules {
		if ruleMap, ok := rule.(map[string]interface{}); ok {
			// Проверяем является ли это WG правилом по содержимому domain_suffix
			if domainSuffix, hasDomains := ruleMap["domain_suffix"].([]interface{}); hasDomains {
				server, _ := ruleMap["server"].(string)
				if server == "dns-local" && len(domainSuffix) > 0 {
					// Проверяем совпадение с нашими доменами
					isWgRule := false
					for _, d := range domainSuffix {
						domStr, _ := d.(string)
						for _, wgDomain := range collectedDomains {
							if domStr == wgDomain {
								isWgRule = true
								break
							}
						}
						if isWgRule {
							break
						}
					}
					if isWgRule {
						continue // Пропускаем старое WG правило
					}
				}
			}
		}
		filteredRules = append(filteredRules, rule)
	}
	dnsRules = filteredRules

	// Если есть внутренние домены - добавляем DNS правило (БЕЗ _comment!)
	if len(collectedDomains) > 0 {
		dnsRule := map[string]interface{}{
			"domain_suffix": collectedDomains,
			"action":        "route",
			"server":        "dns-local", // Системный DNS (использует WireGuard DNS)
		}

		// Добавляем в начало правил (высший приоритет, до hijack-dns)
		dnsRules = append([]interface{}{dnsRule}, dnsRules...)

		fmt.Printf("[addWireGuardDNS] Added DNS rule for internal domains: %v\n", collectedDomains)
	}

	dns["rules"] = dnsRules
}

// updateRouteRulesForWireGuard обновляет правила маршрутизации для WireGuard
// Порядок маршрутизации:
// 1. sniff
// 2. DNS bypass для WireGuard сетей (исключаем hijack-dns для корп. DNS)
// 3. hijack-dns для остального трафика
// 4. WireGuard внутренние сети (по AllowedIPs каждого WireGuard в порядке добавления)
// 5. Прямой доступ к RU зоне (ip_is_private, geosite-ru, etc.)
// 6. Через proxy (final)
//
// ВАЖНО: WireGuard работает нативно через Windows, поэтому маршруты должны
// указывать на "direct", а не на WireGuard outbound. Нативный WireGuard
// сам перехватит трафик на основе AllowedIPs.
func (b *ConfigBuilder) updateRouteRulesForWireGuard(template map[string]interface{}, wireGuardConfigs []UserWireGuardConfig) {
	if len(wireGuardConfigs) == 0 {
		return
	}

	route, ok := template["route"].(map[string]interface{})
	if !ok {
		return
	}

	rules, ok := route["rules"].([]interface{})
	if !ok {
		rules = []interface{}{}
	}

	// Собираем все AllowedIPs и DNS серверы из WireGuard конфигов
	allWireGuardCIDRs := []string{}
	allWireGuardDNS := []string{}
	allInternalDomains := CollectAllInternalDomains(wireGuardConfigs)

	for _, wg := range wireGuardConfigs {
		networks := ExtractNetworksFromAllowedIPs(wg.AllowedIPs)
		allWireGuardCIDRs = append(allWireGuardCIDRs, networks...)
		if wg.DNS != "" {
			allWireGuardDNS = append(allWireGuardDNS, wg.DNS)
		}
	}

	// Фильтруем существующие WireGuard правила (удаляем старые)
	filteredRules := []interface{}{}
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			filteredRules = append(filteredRules, rule)
			continue
		}

		// Пропускаем правила с ip_cidr, совпадающими с WireGuard AllowedIPs
		if ipCidr, ok := ruleMap["ip_cidr"].([]interface{}); ok {
			isWireGuardRule := false
			for _, cidr := range ipCidr {
				cidrStr, _ := cidr.(string)
				for _, wgCidr := range allWireGuardCIDRs {
					if cidrStr == wgCidr {
						isWireGuardRule = true
						break
					}
				}
				if isWireGuardRule {
					break
				}
			}
			if isWireGuardRule {
				continue // Удаляем старые WireGuard правила
			}
		}

		// Пропускаем правила с domain_suffix, совпадающими с внутренними доменами
		if domainSuffix, ok := ruleMap["domain_suffix"].([]interface{}); ok {
			outbound, _ := ruleMap["outbound"].(string)
			if outbound == "direct" && len(domainSuffix) > 0 {
				isWgDomainRule := false
				for _, d := range domainSuffix {
					domStr, _ := d.(string)
					for _, wgDomain := range allInternalDomains {
						if domStr == wgDomain {
							isWgDomainRule = true
							break
						}
					}
					if isWgDomainRule {
						break
					}
				}
				if isWgDomainRule {
					continue // Удаляем старые WireGuard domain правила
				}
			}
		}

		filteredRules = append(filteredRules, rule)
	}

	// Находим позицию после sniff (перед hijack-dns)
	sniffIdx := -1
	for i, rule := range filteredRules {
		if ruleMap, ok := rule.(map[string]interface{}); ok {
			action, _ := ruleMap["action"].(string)
			if action == "sniff" {
				sniffIdx = i
			}
		}
	}

	// Создаём новые правила для WireGuard (БЕЗ _comment - sing-box не поддерживает!)
	// ВСЕ правила должны быть ДО hijack-dns, чтобы трафик сразу шёл в direct
	// без попыток DNS резолвинга через sing-box
	newRules := []interface{}{}

	// 1. DNS bypass: DNS запросы к WireGuard DNS серверам идут через direct БЕЗ hijack
	// Это предотвращает DNS leak - запросы пойдут через WireGuard интерфейс
	if len(allWireGuardDNS) > 0 {
		dnsRule := map[string]interface{}{
			"protocol": "dns",
			"ip_cidr":  allWireGuardDNS,
			"action":   "route",
			"outbound": "direct",
		}
		newRules = append(newRules, dnsRule)
	}

	// 2. Route правило для внутренних доменов WireGuard
	if len(allInternalDomains) > 0 {
		domainRule := map[string]interface{}{
			"domain_suffix": allInternalDomains,
			"action":        "route",
			"outbound":      "direct",
		}
		newRules = append(newRules, domainRule)
	}

	// 3. Правило для IP сетей из AllowedIPs - для мгновенной маршрутизации
	if len(allWireGuardCIDRs) > 0 {
		wgRule := map[string]interface{}{
			"ip_cidr":  allWireGuardCIDRs,
			"action":   "route",
			"outbound": "direct",
		}
		newRules = append(newRules, wgRule)
	}

	// Вставляем ВСЕ правила сразу после sniff, ПЕРЕД hijack-dns
	// Это обеспечивает быстрый доступ к внутренним ресурсам без задержек
	if len(newRules) > 0 {
		finalRules := []interface{}{}

		// Добавляем sniff если есть
		if sniffIdx >= 0 {
			finalRules = append(finalRules, filteredRules[:sniffIdx+1]...)
		}

		// Добавляем ВСЕ WireGuard правила сразу после sniff
		finalRules = append(finalRules, newRules...)

		// Добавляем остальные правила (включая hijack-dns и всё после)
		if sniffIdx >= 0 && sniffIdx+1 < len(filteredRules) {
			finalRules = append(finalRules, filteredRules[sniffIdx+1:]...)
		} else if sniffIdx < 0 {
			// Если нет sniff, добавляем WG правила в начало
			finalRules = append(newRules, filteredRules...)
		}

		filteredRules = finalRules
	}

	route["rules"] = filteredRules
	fmt.Printf("[updateRouteRulesForWireGuard] Added DNS bypass for %d DNS servers, %d internal domains, route for %d CIDRs\n",
		len(allWireGuardDNS), len(allInternalDomains), len(allWireGuardCIDRs))
}

// generateTag генерирует уникальный тег для прокси
func generateTag(p ProxyConfig, index int) string {
	// Используем имя если есть, иначе генерируем
	if p.Name != "" {
		// Очищаем имя от спецсимволов
		name := sanitizeTagName(p.Name)
		if name != "" {
			return name
		}
	}

	// Генерируем имя из типа и индекса
	return fmt.Sprintf("%s-%d", p.Type, index+1)
}

// sanitizeTagName очищает имя от спецсимволов
func sanitizeTagName(name string) string {
	result := strings.Builder{}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' ||
			(r >= 0x0400 && r <= 0x04FF) { // Кириллица
			result.WriteRune(r)
		} else if r == ' ' {
			result.WriteRune('-')
		}
	}
	return strings.TrimSpace(result.String())
}

// copyMap создаёт копию map
func copyMap(m map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}

// addExperimentalAPI добавляет clash_api в experimental секцию для статистики трафика
func (b *ConfigBuilder) addExperimentalAPI(template map[string]interface{}) {
	// Clash API для получения статистики трафика и пинга
	clashAPI := map[string]interface{}{
		"external_controller": "127.0.0.1:9090",
		"secret":              "",
	}

	// Получаем существующую experimental секцию или создаём новую
	var experimental map[string]interface{}
	if existing, ok := template["experimental"].(map[string]interface{}); ok {
		experimental = existing
	} else {
		experimental = make(map[string]interface{})
	}

	// Добавляем clash_api
	experimental["clash_api"] = clashAPI

	template["experimental"] = experimental
}

// applyRoutingMode applies routing rules based on the selected routing mode.
// This modifies the route section of the config.
func (b *ConfigBuilder) applyRoutingMode(template map[string]interface{}) {
	route, ok := template["route"].(map[string]interface{})
	if !ok {
		route = map[string]interface{}{}
		template["route"] = route
	}

	// Clean up DNS rules that reference remote rule_sets (geosite-*)
	b.cleanupDNSRuleSets(template)

	// Get existing rules and rule_set
	existingRules, _ := route["rules"].([]interface{})
	existingRuleSets, _ := route["rule_set"].([]interface{})

	switch b.routingMode {
	case RoutingModeBlockedOnly:
		// Only blocked sites through VPN - use Re:filter + community rule-sets
		b.applyBlockedOnlyMode(route, existingRules, existingRuleSets)

	case RoutingModeExceptRussia:
		// All except Russia through VPN - use built-in domain list
		b.applyExceptRussiaMode(route)

	case RoutingModeAllTraffic:
		// All traffic through VPN - remove direct rules for Russia
		b.applyAllTrafficMode(route, existingRules, existingRuleSets)

	default:
		// Unknown mode, use blocked_only as safest default
		fmt.Printf("[applyRoutingMode] Unknown mode %s, using blocked_only\n", b.routingMode)
		b.applyBlockedOnlyMode(route, existingRules, existingRuleSets)
	}
}

// applyBlockedOnlyMode configures routing for blocked sites only.
// Uses Re:filter and community rule-sets to route only blocked traffic through VPN.
func (b *ConfigBuilder) applyBlockedOnlyMode(route map[string]interface{}, existingRules, existingRuleSets []interface{}) {
	fmt.Printf("[applyRoutingMode] Using blocked_only mode with local filters\n")

	// Get local filter rule_sets
	filterRuleSets := b.filterManager.GetRuleSetConfigs()
	if len(filterRuleSets) == 0 {
		fmt.Printf("[applyRoutingMode] WARNING: No filter files found, falling back to except_russia\n")
		return
	}

	// Build new rule_set array with only local filters (remove geosite-ru etc.)
	newRuleSets := make([]interface{}, 0, len(filterRuleSets))
	for _, rs := range filterRuleSets {
		newRuleSets = append(newRuleSets, rs)
	}
	route["rule_set"] = newRuleSets

	// Build new rules for blocked_only mode
	// Order: sniff → dns → private → wireguard → blocked → throttled → final:direct
	newRules := []interface{}{
		// 1. Sniff for protocol detection
		map[string]interface{}{
			"action": "sniff",
		},
		// 2. Hijack DNS (early for faster resolution)
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		// 3. Private IPs direct (covers .local, LAN, etc.)
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
		// 4. Local/internal domain suffixes direct
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
	}
	newRules = append(newRules, buildDirectServiceRules()...)

	newRules = append(newRules,
		map[string]interface{}{
			"domain_suffix": RuDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		map[string]interface{}{
			"domain_keyword": RuDomainKeywords,
			"action":         "route",
			"outbound":       "direct",
		},
	)

	// 5. WireGuard routing for internal networks (if configured)
	// This is handled separately by WireGuard integration, not here
	// Internal networks go through WireGuard tunnel directly

	// 6. Blocked + throttled sites via proxy (combined rule_sets)
	// Re:filter includes: RKN-blocked domains/IPs + community list (OpenAI, Claude, YouTube, etc.)
	// Discord IPs added separately as they're not in Re:filter
	newRules = append(newRules, map[string]interface{}{
		"rule_set": []string{
			"refilter-domains",  // RKN blocked domains
			"refilter-ips",      // RKN blocked IPs
			"community-domains", // Throttled/geo-blocked: OpenAI, Claude, YouTube, etc.
			"community-ips",     // Community IPs
			"discord-ips",       // Discord voice/media IPs
		},
		"action":   "route",
		"outbound": "proxy",
	})

	// 7. Additional throttled services NOT in community.lst (YouTube video CDN)
	// YouTube is in community.lst but googlevideo.com CDN sometimes missing
	newRules = append(newRules, map[string]interface{}{
		"domain_suffix": []string{
			"googlevideo.com",         // YouTube video CDN
			"youtube-ui.l.google.com", // YouTube UI
		},
		"action":   "route",
		"outbound": "proxy",
	})

	route["rules"] = newRules

	// Change final to direct (everything not blocked goes direct)
	route["final"] = "direct"

	fmt.Printf("[applyRoutingMode] Applied blocked_only: %d rule_sets, %d rules, final=direct\n",
		len(newRuleSets), len(newRules))
}

// applyAllTrafficMode configures routing for all traffic through VPN.
// Removes all direct rules for Russia, everything goes through proxy.
func (b *ConfigBuilder) applyAllTrafficMode(route map[string]interface{}, existingRules, existingRuleSets []interface{}) {
	fmt.Printf("[applyRoutingMode] Using all_traffic mode\n")

	// Remove geosite/geoip rule_sets (not needed for all traffic mode)
	route["rule_set"] = []interface{}{}

	// Minimal rules: sniff, private, everything else proxy
	newRules := []interface{}{
		// 1. Sniff
		map[string]interface{}{
			"action": "sniff",
		},
		// 2. Local domains direct
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		// 3. Hijack DNS
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		// 4. Private IPs direct
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
	}

	route["rules"] = newRules
	route["final"] = "proxy"

	fmt.Printf("[applyRoutingMode] Applied all_traffic: minimal rules, final=proxy\n")
}

// applyExceptRussiaMode configures routing for all traffic except Russia through VPN.
// Uses built-in domain list instead of remote geosite to avoid download issues.
func (b *ConfigBuilder) applyExceptRussiaMode(route map[string]interface{}) {
	fmt.Printf("[applyRoutingMode] Using except_russia mode with built-in domain list\n")

	// No remote rule_sets needed
	route["rule_set"] = []interface{}{}

	// Russian domain suffixes for direct routing
	ruDomainSuffixes := RuDomainSuffixes
	_ = []string{
		".ru", ".su", ".рф",
		".yandex.com", ".yandex.net", ".yandex.ru", ".ya.ru", ".yandex.by", ".yandex.kz",
		".vk.com", ".vkontakte.ru", ".vk.me", ".userapi.com",
		".mail.ru", ".mailru.com", ".mycdn.me", ".imgsmail.ru",
		".ok.ru", ".odnoklassniki.ru",
		".sberbank.ru", ".sber.ru", ".tinkoff.ru", ".tinkoff.com", ".vtb.ru", ".alfabank.ru",
		".raiffeisen.ru", ".gazprombank.ru", ".open.ru", ".rosbank.ru",
		".gosuslugi.ru", ".mos.ru", ".nalog.ru", ".government.ru", ".kremlin.ru",
		".duma.gov.ru", ".cbr.ru", ".pfrf.ru", ".fss.ru",
		".ria.ru", ".rbc.ru", ".interfax.ru", ".tass.ru", ".kommersant.ru",
		".lenta.ru", ".gazeta.ru", ".kp.ru", ".mk.ru", ".iz.ru", ".rt.com",
		".ozon.ru", ".wildberries.ru", ".lamoda.ru", ".dns-shop.ru", ".mvideo.ru",
		".eldorado.ru", ".citilink.ru", ".avito.ru", ".youla.ru",
		".perekrestok.ru", ".magnit.ru", ".5ka.ru", ".dixy.ru", ".lenta.com",
		".sbermarket.ru", ".delivery-club.ru",
		".rzd.ru", ".aeroflot.ru", ".s7.ru", ".utair.ru", ".pobeda.aero",
		".pochta.ru", ".cdek.ru", ".boxberry.ru", ".dpd.ru",
		".mts.ru", ".megafon.ru", ".beeline.ru", ".tele2.ru",
		".rostelecom.ru", ".rt.ru",
		".vgtrk.ru", ".1tv.ru", ".ntv.ru", ".ren.tv", ".ctc.ru",
		".rutube.ru", ".ivi.ru", ".okko.tv", ".more.tv", ".kinopoisk.ru",
		".dzen.ru", ".zen.yandex.ru",
		".2gis.ru", ".2gis.com",
		".sports.ru", ".championat.com", ".sport-express.ru",
		".hh.ru", ".superjob.ru", ".rabota.ru",
		".cian.ru", ".domclick.ru",
		".pikabu.ru", ".habr.com", ".vc.ru", ".dtf.ru",
	}

	ruDomainKeywords := RuDomainKeywords
	_ = []string{
		"yandex", "sber", "tinkoff", "gosuslugi", "rutube",
		"vkontakte", "mailru", "rambler", "wildberries", "ozon",
	}

	newRules := []interface{}{
		map[string]interface{}{"action": "sniff"},
		map[string]interface{}{
			"domain_suffix": LocalDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		map[string]interface{}{
			"protocol": "dns",
			"action":   "hijack-dns",
		},
		map[string]interface{}{
			"ip_is_private": true,
			"action":        "route",
			"outbound":      "direct",
		},
		map[string]interface{}{
			"domain_suffix": ruDomainSuffixes,
			"action":        "route",
			"outbound":      "direct",
		},
		map[string]interface{}{
			"domain_keyword": ruDomainKeywords,
			"action":         "route",
			"outbound":       "direct",
		},
	}

	route["rules"] = newRules
	route["final"] = "proxy"

	fmt.Printf("[applyRoutingMode] Applied except_russia: %d domain suffixes, final=proxy\n", len(ruDomainSuffixes))
}

// cleanupDNSRuleSets removes DNS rules that reference remote rule_sets (geosite-*).
func (b *ConfigBuilder) cleanupDNSRuleSets(template map[string]interface{}) {
	dns, ok := template["dns"].(map[string]interface{})
	if !ok {
		return
	}

	rules, ok := dns["rules"].([]interface{})
	if !ok {
		return
	}

	newRules := make([]interface{}, 0, len(rules))
	for _, rule := range rules {
		ruleMap, ok := rule.(map[string]interface{})
		if !ok {
			newRules = append(newRules, rule)
			continue
		}

		if ruleSet, hasRuleSet := ruleMap["rule_set"]; hasRuleSet {
			if ruleSetArr, ok := ruleSet.([]interface{}); ok {
				hasGeosite := false
				for _, rs := range ruleSetArr {
					if rsStr, ok := rs.(string); ok {
						if strings.HasPrefix(rsStr, "geosite-") || strings.HasPrefix(rsStr, "geoip-") {
							hasGeosite = true
							break
						}
					}
				}
				if hasGeosite {
					fmt.Printf("[cleanupDNSRuleSets] Removed DNS rule with remote rule_set: %v\n", ruleSet)
					continue
				}
			}
		}

		newRules = append(newRules, rule)
	}

	dns["rules"] = newRules
}
