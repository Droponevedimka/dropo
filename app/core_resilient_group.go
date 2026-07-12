package main

// resilientGroupTestURL is a neutral health-check target used for choosing
// between real bypass/VPN outbounds. Do not include direct in bypass-first
// groups for blocked services: direct can win this check while still being
// unable to open the blocked target.
const resilientGroupTestURL = "https://www.gstatic.com/generate_204"

// BuildResilientGroup builds a sing-box outbound group for shared fallback.
// With a single candidate it returns a selector so sing-box does not spend
// health-check cycles on a one-member urltest group.
func BuildResilientGroup(tag string, candidates []string) map[string]interface{} {
	return BuildResilientGroupWithURL(tag, candidates, resilientGroupTestURL)
}

func BuildResilientGroupWithURL(tag string, candidates []string, testURL string) map[string]interface{} {
	if testURL == "" {
		testURL = resilientGroupTestURL
	}
	if len(candidates) == 1 {
		return map[string]interface{}{
			"type":      "selector",
			"tag":       tag,
			"outbounds": candidates,
			"default":   candidates[0],
		}
	}

	return map[string]interface{}{
		"type":                        "urltest",
		"tag":                         tag,
		"outbounds":                   candidates,
		"url":                         testURL,
		"interval":                    "90s",
		"tolerance":                   0,
		"interrupt_exist_connections": false,
	}
}

func buildAutoSelectOutbound(proxyTags []string) map[string]interface{} {
	if len(proxyTags) == 1 {
		return map[string]interface{}{
			"type":      "selector",
			"tag":       "auto-select",
			"outbounds": proxyTags,
			"default":   proxyTags[0],
		}
	}
	return map[string]interface{}{
		"type":                        "urltest",
		"tag":                         "auto-select",
		"outbounds":                   proxyTags,
		"url":                         resilientGroupTestURL,
		"interval":                    "5m",
		"tolerance":                   50,
		"interrupt_exist_connections": false,
	}
}
