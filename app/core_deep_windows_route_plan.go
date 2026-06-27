package main

import (
	"fmt"
	"strings"
)

type DeepWindowsTrafficAction string

const (
	DeepWindowsTrafficDirect      DeepWindowsTrafficAction = "direct"
	DeepWindowsTrafficTransparent DeepWindowsTrafficAction = "transparent"
	DeepWindowsTrafficProxy       DeepWindowsTrafficAction = "proxy"
	DeepWindowsTrafficBlock       DeepWindowsTrafficAction = "block"
)

type DeepWindowsRoutePlan struct {
	RoutingMode          RoutingMode
	FreeMethodsAllowed   bool
	HasVPNCandidates     bool
	RequiresSingBoxProxy bool
	RequiresRedirector   bool
	RUTraffic            DeepWindowsTrafficAction
	ForeignTraffic       DeepWindowsTrafficAction
	DefaultTraffic       DeepWindowsTrafficAction
	ProxyReasons         []string
	DirectServices       []string
	TransparentServices  []string
	ProxyServices        []string
	BlockedServices      []string
	Warnings             []string
}

func (a *App) buildDeepWindowsRoutePlan(configPath string) DeepWindowsRoutePlan {
	settings := GlobalAppSettings{}
	if a != nil && a.storage != nil {
		settings = a.storage.GetAppSettings()
	}

	hasVPNCandidates := false
	if configPath != "" {
		if hasCandidates, err := configHasVPNProbeCandidates(configPath); err != nil {
			plan := buildDeepWindowsRoutePlanForSettings(settings, false, false, false)
			plan.Warnings = append(plan.Warnings, fmt.Sprintf("failed to inspect VPN candidates: %v", err))
			return plan
		} else {
			hasVPNCandidates = hasCandidates
		}
	}

	hasTransparent := false
	if a != nil {
		hasTransparent = defaultZapretStrategyTag(a.availableTransparentStrategyTags()) != ""
	}
	return buildDeepWindowsRoutePlanForSettings(settings, hasVPNCandidates, hasTransparent, freeProxyMethodsAvailable())
}

func buildDeepWindowsRoutePlanForSettings(settings GlobalAppSettings, hasVPNCandidates, hasTransparent, hasFreeProxy bool) DeepWindowsRoutePlan {
	mode := settings.RoutingMode
	if mode == "" {
		mode = DefaultRoutingMode
	}

	plan := DeepWindowsRoutePlan{
		RoutingMode:        mode,
		FreeMethodsAllowed: FreeMethodsAllowed(settings),
		HasVPNCandidates:   hasVPNCandidates,
		RUTraffic:          DeepWindowsTrafficDirect,
		ForeignTraffic:     DeepWindowsTrafficDirect,
		DefaultTraffic:     DeepWindowsTrafficDirect,
	}

	if settings.HideRuTraffic {
		plan.RUTraffic = DeepWindowsTrafficProxy
		plan.requireProxy("RU traffic hiding is enabled")
	}

	switch mode {
	case RoutingModeExceptRussia:
		plan.ForeignTraffic = DeepWindowsTrafficProxy
		plan.DefaultTraffic = DeepWindowsTrafficProxy
		plan.requireProxy("foreign traffic routing is enabled")
	case RoutingModeAllTraffic:
		plan.RUTraffic = DeepWindowsTrafficProxy
		plan.ForeignTraffic = DeepWindowsTrafficProxy
		plan.DefaultTraffic = DeepWindowsTrafficProxy
		plan.requireProxy("all-traffic routing is enabled")
	default:
		plan.DefaultTraffic = DeepWindowsTrafficDirect
	}

	if hasVPNCandidates {
		plan.requireProxy("VPN/VLESS fallback candidates are available")
	}

	for _, svc := range DefaultFreeAccessServices {
		action, reason := deepWindowsServiceAction(settings, svc, hasVPNCandidates, hasTransparent, hasFreeProxy)
		switch action {
		case DeepWindowsTrafficDirect:
			plan.DirectServices = append(plan.DirectServices, svc.Tag)
		case DeepWindowsTrafficTransparent:
			plan.TransparentServices = append(plan.TransparentServices, svc.Tag)
		case DeepWindowsTrafficProxy:
			plan.ProxyServices = append(plan.ProxyServices, svc.Tag)
			if reason != "" {
				plan.requireProxy(reason)
			}
		case DeepWindowsTrafficBlock:
			plan.BlockedServices = append(plan.BlockedServices, svc.Tag)
		}
	}

	if plan.RequiresSingBoxProxy {
		plan.RequiresRedirector = true
	}
	plan.ProxyReasons = uniqueStrings(plan.ProxyReasons)
	return plan
}

func deepWindowsServiceAction(settings GlobalAppSettings, svc FreeAccessService, hasVPNCandidates, hasTransparent, hasFreeProxy bool) (DeepWindowsTrafficAction, string) {
	method := FreeAccessServiceMethod(settings, svc.Tag)
	if method == FreeAccessMethodDirect {
		return DeepWindowsTrafficDirect, ""
	}
	if method == FreeAccessMethodVPN {
		if hasVPNCandidates {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s is forced to VPN", svc.DisplayName)
		}
		return DeepWindowsTrafficBlock, ""
	}
	if method != FreeAccessMethodAuto {
		if !FreeMethodsAllowed(settings) {
			return DeepWindowsTrafficBlock, ""
		}
		if IsFreeAccessTransparentMethod(method) && hasTransparent {
			return DeepWindowsTrafficTransparent, ""
		}
		if IsFreeAccessProxyMethod(method) && hasFreeProxy {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s uses a local free proxy method", svc.DisplayName)
		}
		return DeepWindowsTrafficBlock, ""
	}

	if svc.RequiresVPN {
		if hasVPNCandidates {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s requires VPN/subscription", svc.DisplayName)
		}
		return DeepWindowsTrafficDirect, ""
	}
	if !serviceHasFreeBypass(svc.Tag) {
		if hasVPNCandidates {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s has no working free bypass and uses VPN/subscription", svc.DisplayName)
		}
		return DeepWindowsTrafficDirect, ""
	}
	if !FreeAccessServiceEnabled(settings, svc.Tag) {
		if hasVPNCandidates {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s free access is disabled and VPN fallback is available", svc.DisplayName)
		}
		return DeepWindowsTrafficDirect, ""
	}
	if !FreeMethodsAllowed(settings) {
		if hasVPNCandidates {
			return DeepWindowsTrafficProxy, fmt.Sprintf("%s free methods are disabled and VPN fallback is available", svc.DisplayName)
		}
		return DeepWindowsTrafficDirect, ""
	}
	if hasTransparent {
		return DeepWindowsTrafficTransparent, ""
	}
	if hasFreeProxy {
		return DeepWindowsTrafficProxy, fmt.Sprintf("%s uses local free proxy because transparent zapret is unavailable", svc.DisplayName)
	}
	if hasVPNCandidates {
		return DeepWindowsTrafficProxy, fmt.Sprintf("%s falls back to VPN because free methods are unavailable", svc.DisplayName)
	}
	return DeepWindowsTrafficDirect, ""
}

func (p *DeepWindowsRoutePlan) requireProxy(reason string) {
	p.RequiresSingBoxProxy = true
	reason = strings.TrimSpace(reason)
	if reason != "" {
		p.ProxyReasons = append(p.ProxyReasons, reason)
	}
}

func freeProxyMethodsAvailable() bool {
	if len(DefaultByeDPIStrategies) > 0 {
		return true
	}
	for _, method := range DefaultSpoofDPIMethods {
		if methodSupportsCurrentPlatform(method.Platforms) {
			return true
		}
	}
	return false
}

func (a *App) logDeepWindowsRoutePlan(plan DeepWindowsRoutePlan) {
	if a == nil {
		return
	}
	a.writeLog(fmt.Sprintf("[DeepWindowsPlan] mode=%s free=%t vpn_candidates=%t sing_box_proxy=%t redirector=%t ru=%s foreign=%s default=%s",
		plan.RoutingMode, plan.FreeMethodsAllowed, plan.HasVPNCandidates, plan.RequiresSingBoxProxy, plan.RequiresRedirector,
		plan.RUTraffic, plan.ForeignTraffic, plan.DefaultTraffic))
	if len(plan.ProxyReasons) > 0 {
		a.writeLog(fmt.Sprintf("[DeepWindowsPlan] proxy reasons: %s", strings.Join(plan.ProxyReasons, "; ")))
	}
	a.writeLog(fmt.Sprintf("[DeepWindowsPlan] services transparent=%s proxy=%s direct=%s block=%s",
		strings.Join(plan.TransparentServices, ","),
		strings.Join(plan.ProxyServices, ","),
		strings.Join(plan.DirectServices, ","),
		strings.Join(plan.BlockedServices, ",")))
	for _, warning := range plan.Warnings {
		a.writeLog(fmt.Sprintf("[DeepWindowsPlan] warning: %s", warning))
	}
}
