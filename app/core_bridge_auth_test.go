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
		{"empty expected allows (degraded)", "", "", true},
		{"empty expected allows even with header", "", "whatever", true},
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

// TestBridgeMuxGuardsMutating verifies the token gate short-circuits mutating
// endpoints before any App method runs, while leaving read-only endpoints open.
func TestBridgeMuxGuardsMutating(t *testing.T) {
	const token = "s3cr3t-token"
	mux := newBridgeMux(&App{}, token)

	// Mutating endpoint without token -> 401, App.Start never invoked.
	noTok := httptest.NewRecorder()
	mux.ServeHTTP(noTok, httptest.NewRequest(http.MethodPost, "/api/call", bytes.NewBufferString(`{"method":"Nope"}`)))
	if noTok.Code != http.StatusUnauthorized {
		t.Fatalf("guarded endpoint without token: status = %d, want 401", noTok.Code)
	}

	// Mutating endpoint with token -> auth passes (bogus method yields 400, not 401).
	withTok := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/call", bytes.NewBufferString(`{"method":"Nope"}`))
	req.Header.Set(bridgeAuthHeader, token)
	mux.ServeHTTP(withTok, req)
	if withTok.Code == http.StatusUnauthorized {
		t.Fatalf("guarded endpoint with valid token still returned 401")
	}

	// Read-only OPTIONS preflight is never gated.
	opt := httptest.NewRecorder()
	mux.ServeHTTP(opt, httptest.NewRequest(http.MethodOptions, "/api/connect", nil))
	if opt.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS preflight: status = %d, want 204", opt.Code)
	}
}
