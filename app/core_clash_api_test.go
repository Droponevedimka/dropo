package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestClashAPIAccessAppliesRandomAuthenticatedEndpoint(t *testing.T) {
	access := newClashAPIAccess()
	if err := access.validate(); err != nil {
		t.Fatalf("newClashAPIAccess() failed: %v", err)
	}
	if len(access.secret) != 64 {
		t.Fatalf("secret length = %d, want 64 hex chars", len(access.secret))
	}
	template := map[string]interface{}{}
	if err := access.apply(template); err != nil {
		t.Fatalf("apply() failed: %v", err)
	}
	experimental := template["experimental"].(map[string]interface{})
	clash := experimental["clash_api"].(map[string]interface{})
	if clash["external_controller"] != net.JoinHostPort("127.0.0.1", strconv.Itoa(access.port)) {
		t.Fatalf("external_controller = %v", clash["external_controller"])
	}
	if clash["secret"] != access.secret {
		t.Fatal("generated config did not receive the process-local secret")
	}
	origins := clash["access_control_allow_origin"].([]interface{})
	if len(origins) != 1 || origins[0] != clashAPIOrigin {
		t.Fatalf("allowed origins = %v, want only %q", origins, clashAPIOrigin)
	}
}

func TestClashAPIClientSendsBearerSecret(t *testing.T) {
	const secret = "unit-test-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{configBuilder: &ConfigBuilderForStorage{clashAPI: &clashAPIAccess{port: port, secret: secret}}}
	resp, err := app.clashAPIGet(server.Client(), "/proxies")
	if err != nil {
		t.Fatalf("clashAPIGet() failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
}
