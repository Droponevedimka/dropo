package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBridgeTokenAuthorized(t *testing.T) {
	const token = "deadbeef"
	cases := []struct {
		name     string
		expected string
		header   string
		want     bool
	}{
		{"empty expected denies", "", "", false},
		{"empty expected denies even with header", "", "whatever", false},
		{"correct token", token, token, true},
		{"wrong token", token, "nope", false},
		{"missing token", token, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/call", nil)
			if tc.header != "" {
				r.Header.Set(bridgeAuthHeader, tc.header)
			}
			if got := bridgeTokenAuthorized(r, tc.expected); got != tc.want {
				t.Fatalf("bridgeTokenAuthorized = %v, want %v", got, tc.want)
			}
		})
	}
}

// loopbackRequest builds a request whose Host is loopback so it passes the
// DNS-rebinding guard, isolating the auth behavior under test.
func loopbackRequest(method, path string, body *bytes.Buffer) *http.Request {
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, body)
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "127.0.0.1:17890"
	return r
}

// TestBridgeMuxGuardsMutating verifies the token gate short-circuits mutating
// endpoints before any App method runs, while leaving read-only endpoints open.
func TestBridgeMuxGuardsMutating(t *testing.T) {
	const token = "s3cr3t-token"
	mux := newBridgeMux(&App{}, token)

	// Mutating endpoint without token -> 401, App.Start never invoked.
	noTok := httptest.NewRecorder()
	mux.ServeHTTP(noTok, loopbackRequest(http.MethodPost, "/api/call", bytes.NewBufferString(`{"method":"Nope"}`)))
	if noTok.Code != http.StatusUnauthorized {
		t.Fatalf("guarded endpoint without token: status = %d, want 401", noTok.Code)
	}

	// Mutating endpoint with token -> auth passes (bogus method yields 400, not 401).
	withTok := httptest.NewRecorder()
	req := loopbackRequest(http.MethodPost, "/api/call", bytes.NewBufferString(`{"method":"Nope"}`))
	req.Header.Set(bridgeAuthHeader, token)
	mux.ServeHTTP(withTok, req)
	if withTok.Code == http.StatusUnauthorized {
		t.Fatalf("guarded endpoint with valid token still returned 401")
	}

	// Read-only OPTIONS preflight is never gated.
	opt := httptest.NewRecorder()
	mux.ServeHTTP(opt, loopbackRequest(http.MethodOptions, "/api/connect", nil))
	if opt.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight: status = %d, want 204", opt.Code)
	}

	// /api/logs is now guarded (it can expose sensitive detail): no token -> 401.
	logsNoTok := httptest.NewRecorder()
	mux.ServeHTTP(logsNoTok, loopbackRequest(http.MethodGet, "/api/logs", nil))
	if logsNoTok.Code != http.StatusUnauthorized {
		t.Fatalf("/api/logs without token: status = %d, want 401", logsNoTok.Code)
	}
}

// TestBridgeMuxRejectsNonLoopbackHost verifies the DNS-rebinding guard: any
// request whose Host is not loopback is refused with 403 before auth/handler.
func TestBridgeMuxRejectsNonLoopbackHost(t *testing.T) {
	const token = "s3cr3t-token"
	mux := newBridgeMux(&App{}, token)

	rebind := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rebind.Host = "attacker.example" // resolves to 127.0.0.1 in a rebinding attack
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, rebind)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback Host: status = %d, want 403", rec.Code)
	}

	// A loopback Host on the same open endpoint is served normally.
	ok := httptest.NewRecorder()
	mux.ServeHTTP(ok, loopbackRequest(http.MethodGet, "/api/status", nil))
	if ok.Code != http.StatusOK {
		t.Fatalf("loopback Host on /api/status: status = %d, want 200", ok.Code)
	}
}

// TestBridgeDropsCORSWildcard guards against reintroducing a wildcard CORS grant
// that would let arbitrary web pages read bridge responses.
func TestBridgeDropsCORSWildcard(t *testing.T) {
	mux := newBridgeMux(&App{}, "tok")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, loopbackRequest(http.MethodGet, "/api/status", nil))
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty (no CORS grant)", got)
	}
}

func TestHostHeaderLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"", true},
		{"127.0.0.1:17890", true},
		{"127.0.0.1", true},
		{"localhost:17890", true},
		{"localhost", true},
		{"[::1]:17890", true},
		{"::1", true},
		{"attacker.example", false},
		{"attacker.example:17890", false},
		{"192.168.1.10:17890", false},
		{"10.0.0.5", false},
	}
	for _, tc := range cases {
		if got := hostHeaderLoopback(tc.host); got != tc.want {
			t.Errorf("hostHeaderLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}

func TestBridgeCallAllowlistRejectsExportedMaintenanceMethods(t *testing.T) {
	_, err := callAppMethod(&App{}, bridgeCallRequest{Method: "RebuildActiveProfileConfig"})
	if err == nil {
		t.Fatal("RebuildActiveProfileConfig unexpectedly exposed through /api/call")
	}
	if _, ok := bridgeCallableMethods["GetAppConfig"]; !ok {
		t.Fatal("GetAppConfig must remain available to the Flutter UI")
	}
	if _, ok := bridgeCallableMethods["DownloadAndInstallUpdate"]; !ok {
		t.Fatal("DownloadAndInstallUpdate must be available to the trusted Flutter UI")
	}
}
