package dropocore

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type wireGuardConfig struct {
	Tag                 string   `json:"tag"`
	Name                string   `json:"name"`
	Endpoint            string   `json:"endpoint"`
	AllowedIPs          []string `json:"allowed_ips"`
	Config              string   `json:"config"`
	PrivateKey          string   `json:"private_key"`
	LocalAddress        []string `json:"local_address"`
	DNS                 []string `json:"dns"`
	MTU                 int      `json:"mtu"`
	PublicKey           string   `json:"public_key"`
	PresharedKey        string   `json:"preshared_key"`
	PersistentKeepalive int      `json:"persistent_keepalive"`
	CamouflageEnabled   bool     `json:"camouflage_enabled,omitempty"`
}

var wireGuardTagPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,31}$`)

func wireGuardListPayloadLocked() []interface{} {
	items := make([]interface{}, 0, len(current.WireGuards))
	for _, wg := range current.WireGuards {
		items = append(items, wireGuardPayload(wg))
	}
	return items
}

func addWireGuardLocked(args []interface{}) string {
	tag := normalizeWireGuardTag(stringArg(args, 0, ""))
	name := strings.TrimSpace(stringArg(args, 1, ""))
	configText := stringArg(args, 2, "")
	camouflageEnabled := boolArg(args, 3, false)
	wg, err := parseWireGuardConfigText(configText)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	if tag == "" {
		tag = generateWireGuardTag(wg)
	}
	if err := validateWireGuardTag(tag); err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	for _, existing := range current.WireGuards {
		if existing.Tag == tag {
			return encode(map[string]interface{}{"success": false, "error": "WireGuard tag already exists"})
		}
	}
	wg.Tag = tag
	wg.Name = firstNonEmpty(name, tag)
	wg.CamouflageEnabled = camouflageEnabled
	current.WireGuards = append(current.WireGuards, wg)
	appendLogLocked("android WireGuard saved: " + wg.Tag)
	_ = saveLocked()
	return encode(map[string]interface{}{"success": true, "tag": wg.Tag, "count": len(current.WireGuards)})
}

func updateWireGuardLocked(args []interface{}) string {
	oldTag := stringArg(args, 0, "")
	tag := normalizeWireGuardTag(stringArg(args, 1, oldTag))
	name := strings.TrimSpace(stringArg(args, 2, ""))
	configText := stringArg(args, 3, "")
	camouflageEnabled := boolArg(args, 4, false)
	wg, err := parseWireGuardConfigText(configText)
	if err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	if tag == "" {
		tag = oldTag
	}
	if err := validateWireGuardTag(tag); err != nil {
		return encode(map[string]interface{}{"success": false, "error": err.Error()})
	}
	found := -1
	for i, existing := range current.WireGuards {
		if existing.Tag == oldTag {
			found = i
			continue
		}
		if existing.Tag == tag {
			return encode(map[string]interface{}{"success": false, "error": "WireGuard tag already exists"})
		}
	}
	if found < 0 {
		return encode(map[string]interface{}{"success": false, "error": "WireGuard config not found"})
	}
	wg.Tag = tag
	wg.Name = firstNonEmpty(name, tag)
	wg.CamouflageEnabled = camouflageEnabled
	current.WireGuards[found] = wg
	appendLogLocked("android WireGuard updated: " + wg.Tag)
	_ = saveLocked()
	return encode(map[string]interface{}{"success": true, "tag": wg.Tag, "count": len(current.WireGuards)})
}

func deleteWireGuardLocked(args []interface{}) string {
	tag := stringArg(args, 0, "")
	next := current.WireGuards[:0]
	removed := false
	for _, existing := range current.WireGuards {
		if existing.Tag == tag {
			removed = true
			continue
		}
		next = append(next, existing)
	}
	if !removed {
		return encode(map[string]interface{}{"success": false, "error": "WireGuard config not found"})
	}
	current.WireGuards = next
	appendLogLocked("android WireGuard removed: " + tag)
	_ = saveLocked()
	return encode(map[string]interface{}{"success": true, "count": len(current.WireGuards)})
}

func parseWireGuardConfigText(configText string) (wireGuardConfig, error) {
	configText = strings.TrimSpace(configText)
	if configText == "" {
		return wireGuardConfig{}, fmt.Errorf("WireGuard config is empty")
	}
	wg := wireGuardConfig{
		Config:       configText,
		LocalAddress: []string{},
		AllowedIPs:   []string{},
		DNS:          []string{},
		MTU:          1280,
	}
	section := ""
	for _, rawLine := range strings.Split(configText, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[] \t"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				wg.PrivateKey = value
			case "address":
				wg.LocalAddress = splitWireGuardCSV(value)
			case "dns":
				wg.DNS = splitWireGuardCSV(value)
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
				wg.AllowedIPs = splitWireGuardCSV(value)
			case "endpoint":
				wg.Endpoint = value
			case "persistentkeepalive":
				if keepalive, err := strconv.Atoi(value); err == nil {
					wg.PersistentKeepalive = keepalive
				}
			}
		}
	}
	if wg.PrivateKey == "" {
		return wg, fmt.Errorf("missing PrivateKey")
	}
	if len(wg.LocalAddress) == 0 {
		return wg, fmt.Errorf("missing Address")
	}
	if wg.PublicKey == "" {
		return wg, fmt.Errorf("missing PublicKey")
	}
	if len(wg.AllowedIPs) == 0 {
		return wg, fmt.Errorf("missing AllowedIPs")
	}
	if wg.Endpoint == "" {
		return wg, fmt.Errorf("missing Endpoint")
	}
	return wg, nil
}

func wireGuardPayload(wg wireGuardConfig) map[string]interface{} {
	return map[string]interface{}{
		"success":              true,
		"tag":                  wg.Tag,
		"name":                 wg.Name,
		"endpoint":             wg.Endpoint,
		"allowed_ips":          append([]string(nil), wg.AllowedIPs...),
		"config":               wg.Config,
		"private_key":          wg.PrivateKey,
		"local_address":        strings.Join(wg.LocalAddress, ", "),
		"dns":                  append([]string(nil), wg.DNS...),
		"mtu":                  wg.MTU,
		"public_key":           wg.PublicKey,
		"preshared_key":        wg.PresharedKey,
		"persistent_keepalive": wg.PersistentKeepalive,
		"camouflage_enabled":   wg.CamouflageEnabled,
	}
}

func splitWireGuardCSV(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func validateWireGuardTag(tag string) error {
	if tag == "" {
		return fmt.Errorf("WireGuard tag is empty")
	}
	if !wireGuardTagPattern.MatchString(tag) {
		return fmt.Errorf("WireGuard tag must start with a letter and contain only latin letters, digits, '-' or '_'")
	}
	return nil
}

func normalizeWireGuardTag(tag string) string {
	tag = strings.TrimSpace(tag)
	tag = strings.ReplaceAll(tag, " ", "-")
	return tag
}

func generateWireGuardTag(wg wireGuardConfig) string {
	base := wg.Endpoint
	if base == "" {
		base = "android-wg"
	}
	base = strings.ToLower(safeTagChars.ReplaceAllString(base, "-"))
	base = strings.Trim(base, "-")
	if base == "" || !((base[0] >= 'a' && base[0] <= 'z') || (base[0] >= 'A' && base[0] <= 'Z')) {
		base = "wg-" + base
	}
	if len(base) > 24 {
		base = base[:24]
	}
	tag := base
	for i := 2; wireGuardTagExists(tag); i++ {
		tag = fmt.Sprintf("%s-%d", base, i)
		if len(tag) > 32 {
			tag = fmt.Sprintf("wg-%d", i)
		}
	}
	return tag
}

func wireGuardTagExists(tag string) bool {
	for _, existing := range current.WireGuards {
		if existing.Tag == tag {
			return true
		}
	}
	return false
}
