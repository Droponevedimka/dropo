package routing

import "testing"

func TestDefaultPolicyDecisions(t *testing.T) {
	policy := DefaultPolicy()
	tests := []struct {
		name         string
		host         string
		process      string
		subscription bool
		want         Route
		wantTag      string
	}{
		{name: "gosuslugi direct", host: "www.gosuslugi.ru", subscription: true, want: RouteDirect, wantTag: "ru-direct"},
		{name: "yandex direct", host: "mail.yandex.ru", subscription: true, want: RouteDirect, wantTag: "ru-direct"},
		{name: "google direct", host: "www.google.com", subscription: true, want: RouteDirect, wantTag: "google-direct"},
		{name: "youtube specific before google", host: "youtubei.googleapis.com", subscription: true, want: RouteFreeBypass, wantTag: "youtube"},
		{name: "openai vpn forced", host: "chatgpt.com", subscription: true, want: RouteVPNForced, wantTag: "openai"},
		{name: "claude vpn forced", host: "api.anthropic.com", subscription: true, want: RouteVPNForced, wantTag: "openai"},
		{name: "cursor process vpn forced", process: "cursor.exe", subscription: true, want: RouteVPNForced, wantTag: "openai"},
		{name: "vpn forced without subscription", host: "claude.ai", subscription: false, want: RouteBlockedWithoutVPN, wantTag: "openai"},
		{name: "discord free bypass", host: "cdn.discordapp.com", subscription: true, want: RouteFreeBypass, wantTag: "discord"},
		{name: "unknown direct", host: "example.org", subscription: true, want: RouteDirect, wantTag: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := policy.Decide(tt.host, tt.process, tt.subscription)
			if got.Route != tt.want {
				t.Fatalf("route = %q, want %q: %+v", got.Route, tt.want, got)
			}
			if got.ServiceTag != tt.wantTag {
				t.Fatalf("service tag = %q, want %q: %+v", got.ServiceTag, tt.wantTag, got)
			}
		})
	}
}
