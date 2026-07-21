package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestQuickCheckTreatsRedirectLimitAsReachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := invokeQuickCheckURL(ctx, newQuickCheckHTTPClient(nil), server.URL)
	if !result.Success {
		t.Fatalf("redirecting service should be considered reachable: status=%d err=%q", result.Status, result.Error)
	}
	if result.Status != http.StatusFound {
		t.Fatalf("expected final redirect response, got status %d", result.Status)
	}
}

func TestQuickCheckTreatsRegionalServiceFailureAsExpected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client := &http.Client{Transport: failingRoundTripper{}}
	result := runSingleClientQuickCheck(ctx, clientQuickCheckService{
		Name:     "Gosuslugi",
		URL:      "https://www.gosuslugi.ru",
		Category: "Direct-RU",
		Regional: true,
	}, client, nil)

	if !result.Success || result.StatusText != "REGION_LIMIT" {
		t.Fatalf("regional service failure = success:%v status:%s, want REGION_LIMIT success", result.Success, result.StatusText)
	}
	if result.NormalSuccess {
		t.Fatal("regional failure should keep NormalSuccess=false for diagnostics")
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("region restricted")
}

func TestQuickCheckFailuresQueueUniqueBlockedServiceMaintenance(t *testing.T) {
	app := &App{routeStrategyJobs: make(chan string, 4), isRunning: true}
	app.handleClientQuickCheckFailures([]clientQuickCheckResult{
		{Name: "Discord", Category: "Blocked", Success: false, NormalError: "timeout"},
		{Name: "Discord API", Category: "Blocked", Success: false, NormalError: "timeout"},
		{Name: "Yandex", Category: "Direct-RU", Success: false, NormalError: "timeout"},
	})

	select {
	case reason := <-app.routeStrategyJobs:
		if !strings.HasPrefix(reason, "service:discord ") {
			t.Fatalf("queued reason = %q, want discord service maintenance", reason)
		}
	default:
		t.Fatal("expected blocked service maintenance to be queued")
	}

	select {
	case reason := <-app.routeStrategyJobs:
		t.Fatalf("unexpected duplicate maintenance job: %q", reason)
	default:
	}
}

func TestQuickCheckProxyRescueQueuesBlockedServiceMaintenance(t *testing.T) {
	app := &App{routeStrategyJobs: make(chan string, 2), isRunning: true}
	app.handleClientQuickCheckFailures([]clientQuickCheckResult{
		{Name: "YouTube", Category: "Blocked", Success: true, ProxyRescued: true, StatusText: "TUN_FAIL_PROXY_OK"},
	})

	select {
	case reason := <-app.routeStrategyJobs:
		if !strings.HasPrefix(reason, "service:youtube ") {
			t.Fatalf("queued reason = %q, want youtube service maintenance", reason)
		}
	default:
		t.Fatal("expected proxy-rescued blocked service to queue maintenance")
	}
}

func TestQuickCheckAIVPNOnlyProxyFallbackDoesNotQueueFreeMethodMaintenance(t *testing.T) {
	app := &App{routeStrategyJobs: make(chan string, 2), isRunning: true}
	app.handleClientQuickCheckFailures([]clientQuickCheckResult{
		{Name: "OpenAI API", Category: "AI-VPNOnly", Success: true, ProxyRescued: true, StatusText: "VPN_PROXY_OK"},
	})

	select {
	case reason := <-app.routeStrategyJobs:
		t.Fatalf("AI/VPN-only proxy fallback must not queue free-method maintenance, got %q", reason)
	default:
	}
}

func TestRouteStrategyMaintenanceSkippedWhileStopping(t *testing.T) {
	app := &App{routeStrategyJobs: make(chan string, 2), isRunning: true, stoppedManually: true}
	app.requestRouteStrategyMaintenance("service:discord stop race")

	select {
	case reason := <-app.routeStrategyJobs:
		t.Fatalf("maintenance must not be queued while VPN is stopping, got %q", reason)
	default:
	}
}

func TestRouteStrategyMaintenanceCoalescesByService(t *testing.T) {
	app := NewApp()
	defer close(app.routeStrategyJobs)

	uniqueTags := map[string]bool{}
	for _, svc := range clientQuickCheckServices {
		if svc.Category != "Blocked" {
			continue
		}
		tag := clientQuickCheckServiceTag(svc.Name)
		if tag != "" {
			uniqueTags[tag] = true
		}
		// Multiple endpoints map to the same service tag (Discord/Discord API/...)
		// and must collapse into a single search.
		app.requestRouteStrategyMaintenance("service:" + tag + " test failure")
	}

	queued := 0
	for {
		select {
		case <-app.routeStrategyJobs:
			queued++
		default:
			if queued != len(uniqueTags) {
				t.Fatalf("queued %d jobs, want one per unique blocked service (%d)", queued, len(uniqueTags))
			}
			return
		}
	}
}

func TestRouteStrategyMaintenanceAllowsLaterRetryAfterCooldown(t *testing.T) {
	app := NewApp()
	defer close(app.routeStrategyJobs)

	app.requestRouteStrategyMaintenance("service:discord first failure")
	// Dequeue and mark as searched, mimicking the maintenance listener.
	<-app.routeStrategyJobs
	app.finishRouteStrategyService("discord")

	app.requestRouteStrategyMaintenance("service:discord second failure")
	select {
	case reason := <-app.routeStrategyJobs:
		t.Fatalf("service must not be searched during cooldown, got %q", reason)
	default:
	}

	app.routeStrategyMu.Lock()
	app.routeStrategyLastAttempt["discord"] = time.Now().Add(-routeStrategyRetryCooldown - time.Second)
	app.routeStrategyMu.Unlock()
	app.requestRouteStrategyMaintenance("service:discord later failure")
	select {
	case <-app.routeStrategyJobs:
	default:
		t.Fatal("later failure must allow another search after cooldown")
	}

	// A new session also resets the cooldown immediately.
	app.resetRouteStrategySession()
	app.requestRouteStrategyMaintenance("service:discord new session")
	select {
	case <-app.routeStrategyJobs:
	default:
		t.Fatal("new session must allow the service to be searched again")
	}
}

func TestTransparentReselectionRunsOncePerSession(t *testing.T) {
	app := NewApp()

	if !app.beginTransparentReselectionOncePerSession() {
		t.Fatal("first reselection in a session must be allowed")
	}
	if app.beginTransparentReselectionOncePerSession() {
		t.Fatal("second reselection in the same session must be suppressed")
	}

	app.resetRouteStrategySession()
	if !app.beginTransparentReselectionOncePerSession() {
		t.Fatal("a new session must allow reselection again")
	}
}

func TestClientQuickCheckServiceTag(t *testing.T) {
	cases := map[string]string{
		"YouTube API":  "youtube",
		"WhatsApp CDN": "whatsapp",
		"Instagram":    "meta",
		"X":            "twitter",
		"Gosuslugi":    "",
	}
	for name, want := range cases {
		if got := clientQuickCheckServiceTag(name); got != want {
			t.Fatalf("clientQuickCheckServiceTag(%q) = %q, want %q", name, got, want)
		}
	}
}
