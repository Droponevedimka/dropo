package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// bridgeAuthHeader is the header the Flutter frontend sends with mutating
// requests. Read-only GET endpoints (status/info/logs/events) are intentionally
// unauthenticated so reachability probing and polling keep working even if token
// provisioning hiccups; only state-changing calls (connect/disconnect/call/quit)
// are guarded.
const bridgeAuthHeader = "X-Dropo-Token"

// bridgeTokenFileName is written next to the dropo-core executable so the locally
// co-located Flutter UI can read it. It is NOT a secret against the local user —
// it defends the loopback bridge against other local processes and browser-based
// DNS-rebinding to 127.0.0.1 invoking Start/Stop/quit.
const bridgeTokenFileName = "bridge-token"

func bridgeTokenPath(dataPath string) string {
	return filepath.Join(dataPath, bridgeTokenFileName)
}

// ensureBridgeToken generates a fresh random token for this process, persists it
// (0600) next to the executable, and returns it. A new token per launch means a
// stale file from a previous run can never authorize a new process.
func ensureBridgeToken(dataPath string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	if err := os.MkdirAll(dataPath, 0700); err != nil {
		return "", err
	}
	path := bridgeTokenPath(dataPath)
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		return token, err
	}
	return token, nil
}

// removeBridgeToken deletes the token file on shutdown so a dangling secret does
// not linger after the bridge is gone.
func removeBridgeToken(dataPath string) {
	_ = os.Remove(bridgeTokenPath(dataPath))
}

// bridgeTokenAuthorized reports whether a request carries the expected token.
// Authentication is fail-closed: an empty expected token never authorizes a
// privileged request.
func bridgeTokenAuthorized(r *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	got := strings.TrimSpace(r.Header.Get(bridgeAuthHeader))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(expected)) == 1
}
