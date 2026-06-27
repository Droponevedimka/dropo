package main

import "testing"

func TestDeepWindowsRoutePlanBlockedOnlyPrefersTransparentZapret(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()

	plan := buildDeepWindowsRoutePlanForSettings(settings, false, true, true)

	if plan.RoutingMode != RoutingModeBlockedOnly {
		t.Fatalf("routing mode = %q, want blocked_only", plan.RoutingMode)
	}
	if plan.RequiresSingBoxProxy {
		t.Fatalf("blocked-only free strategy must not require sing-box proxy: %+v", plan)
	}
	if plan.RequiresRedirector {
		t.Fatalf("blocked-only free strategy must not require proxy redirector: %+v", plan)
	}
	if !planContainsString(plan.TransparentServices, "youtube") {
		t.Fatalf("transparent services = %v, want youtube through zapret", plan.TransparentServices)
	}
	if !planContainsString(plan.DirectServices, "telegram") {
		t.Fatalf("direct services = %v, want telegram direct until tg-proxy/subscription is available", plan.DirectServices)
	}
	if plan.ForeignTraffic != DeepWindowsTrafficDirect || plan.RUTraffic != DeepWindowsTrafficDirect {
		t.Fatalf("foreign/ru = %s/%s, want direct/direct", plan.ForeignTraffic, plan.RUTraffic)
	}
}

func TestDeepWindowsRoutePlanStartsLocalProxyForSubscriptionFallback(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("subscription fallback must require local proxy endpoint and redirector: %+v", plan)
	}
	if !planContainsString(plan.ProxyReasons, "VPN/VLESS fallback candidates are available") {
		t.Fatalf("proxy reasons = %v, want VPN fallback reason", plan.ProxyReasons)
	}
	if !planContainsString(plan.TransparentServices, "discord") {
		t.Fatalf("discord should still prefer transparent free strategy, got %v", plan.TransparentServices)
	}
	for _, serviceTag := range []string{"openai", "meta", "whatsapp", "telegram"} {
		if !planContainsString(plan.ProxyServices, serviceTag) {
			t.Fatalf("%s should use subscription proxy, got %v", serviceTag, plan.ProxyServices)
		}
	}
}

func TestDeepWindowsRoutePlanExceptRussiaDoesNotSwitchEngine(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.RoutingMode = RoutingModeExceptRussia

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if plan.ForeignTraffic != DeepWindowsTrafficProxy || plan.DefaultTraffic != DeepWindowsTrafficProxy {
		t.Fatalf("foreign/default = %s/%s, want proxy/proxy", plan.ForeignTraffic, plan.DefaultTraffic)
	}
	if plan.RUTraffic != DeepWindowsTrafficDirect {
		t.Fatalf("RU traffic = %s, want direct", plan.RUTraffic)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("except-russia must use local proxy endpoint under Deep Windows, not TUN: %+v", plan)
	}
	if !planContainsString(plan.TransparentServices, "youtube") {
		t.Fatalf("blocked services should still prefer zapret before proxy fallback, got %v", plan.TransparentServices)
	}
}

func TestDeepWindowsRoutePlanAllTrafficKeepsLocalDirectExclusions(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.RoutingMode = RoutingModeAllTraffic

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if plan.RUTraffic != DeepWindowsTrafficProxy || plan.ForeignTraffic != DeepWindowsTrafficProxy || plan.DefaultTraffic != DeepWindowsTrafficProxy {
		t.Fatalf("ru/foreign/default = %s/%s/%s, want proxy/proxy/proxy", plan.RUTraffic, plan.ForeignTraffic, plan.DefaultTraffic)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("all-traffic must require local proxy endpoint and redirector under Deep Windows: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanHideRuTrafficRequiresProxyEndpoint(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.HideRuTraffic = true
	settings.RuProxyAddress = "vless://00000000-0000-0000-0000-000000000000@example.com:443?security=tls#ru"

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if plan.RUTraffic != DeepWindowsTrafficProxy {
		t.Fatalf("RU traffic = %s, want proxy", plan.RUTraffic)
	}
	if !plan.RequiresSingBoxProxy || !planContainsString(plan.ProxyReasons, "RU traffic hiding is enabled") {
		t.Fatalf("hide-RU plan = %+v, want proxy endpoint reason", plan)
	}
}

func TestDeepWindowsRoutePlanForcedVPNWithoutSubscriptionBlocksService(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.FreeAccessMethods["telegram"] = FreeAccessMethodVPN

	plan := buildDeepWindowsRoutePlanForSettings(settings, false, true, true)

	if !planContainsString(plan.BlockedServices, "telegram") {
		t.Fatalf("blocked services = %v, want telegram when forced VPN has no candidate", plan.BlockedServices)
	}
	if planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("telegram must not be proxied without VPN candidate: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanForcedVPNWithSubscriptionUsesProxyEndpoint(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.FreeAccessMethods["telegram"] = FreeAccessMethodVPN

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if !planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("proxy services = %v, want telegram", plan.ProxyServices)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("forced VPN should require local proxy endpoint and redirector: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanDisableFreeMethodsUsesSubscriptionWhenAvailable(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.DisableFreeAccess = true

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if plan.FreeMethodsAllowed {
		t.Fatalf("free methods allowed = true, want false")
	}
	if !planContainsString(plan.ProxyServices, "youtube") || !planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("proxy services = %v, want ordinary blocked services through subscription fallback", plan.ProxyServices)
	}
	if len(plan.TransparentServices) != 0 {
		t.Fatalf("transparent services = %v, want none while free methods are disabled", plan.TransparentServices)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("disabled free methods with VPN candidates should require local proxy endpoint: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanDisableFreeMethodsWithoutSubscriptionGoesDirectForNonVPNServices(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.DisableFreeAccess = true

	plan := buildDeepWindowsRoutePlanForSettings(settings, false, true, true)

	if !planContainsString(plan.DirectServices, "youtube") || !planContainsString(plan.DirectServices, "telegram") {
		t.Fatalf("direct services = %v, want non-VPN services direct when free methods are disabled and no VPN exists", plan.DirectServices)
	}
	if plan.RequiresSingBoxProxy || plan.RequiresRedirector {
		t.Fatalf("plan must not require local proxy without VPN candidate: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanManualDirectOverridesSubscriptionFallback(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.FreeAccessMethods["telegram"] = FreeAccessMethodDirect

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if !planContainsString(plan.DirectServices, "telegram") {
		t.Fatalf("direct services = %v, want manual direct telegram", plan.DirectServices)
	}
	if planContainsString(plan.ProxyServices, "telegram") || planContainsString(plan.TransparentServices, "telegram") {
		t.Fatalf("telegram should only be direct, got plan %+v", plan)
	}
}

func TestDeepWindowsRoutePlanManualTransparentMethodUsesZapret(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.FreeAccessMethods["telegram"] = DefaultZapretTransparentStrategies[0].Tag

	plan := buildDeepWindowsRoutePlanForSettings(settings, true, true, true)

	if !planContainsString(plan.TransparentServices, "telegram") {
		t.Fatalf("transparent services = %v, want telegram through manual zapret strategy", plan.TransparentServices)
	}
	if planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("telegram should not use proxy while manual zapret strategy is available: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanManualProxyMethodRequiresLocalProxyEndpoint(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()
	settings.FreeAccessMethods["telegram"] = DefaultByeDPIStrategies[0].Tag

	plan := buildDeepWindowsRoutePlanForSettings(settings, false, true, true)

	if !planContainsString(plan.ProxyServices, "telegram") {
		t.Fatalf("proxy services = %v, want telegram through manual local proxy method", plan.ProxyServices)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("manual local proxy method should require local proxy endpoint and redirector: %+v", plan)
	}
}

func TestDeepWindowsRoutePlanFallsBackToFreeProxyWhenTransparentUnavailable(t *testing.T) {
	settings := defaultDeepWindowsPlanSettings()

	plan := buildDeepWindowsRoutePlanForSettings(settings, false, false, true)

	if !planContainsString(plan.ProxyServices, "youtube") || !planContainsString(plan.ProxyServices, "discord") {
		t.Fatalf("proxy services = %v, want free proxy methods when transparent zapret is unavailable", plan.ProxyServices)
	}
	if len(plan.TransparentServices) != 0 {
		t.Fatalf("transparent services = %v, want none without transparent strategies", plan.TransparentServices)
	}
	if !plan.RequiresSingBoxProxy || !plan.RequiresRedirector {
		t.Fatalf("free proxy methods should require local proxy endpoint and redirector: %+v", plan)
	}
}

func defaultDeepWindowsPlanSettings() GlobalAppSettings {
	return GlobalAppSettings{
		RoutingMode:        RoutingModeBlockedOnly,
		NetworkMode:        DefaultNetworkMode,
		FreeAccessEnabled:  true,
		FreeAccessServices: DefaultFreeAccessServiceState(),
		FreeAccessMethods:  DefaultFreeAccessServiceMethodState(),
		DisableFreeAccess:  false,
	}
}

func planContainsString(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
