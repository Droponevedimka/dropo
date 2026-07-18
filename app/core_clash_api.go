package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const clashAPIOrigin = "dropo://local"

// clashAPIAccess is created once per core process. The generated sing-box
// configuration and every in-process Clash API client share this endpoint and
// bearer secret. Keeping both values process-local avoids the well-known 9090
// collision and prevents browser pages or unrelated local processes from
// reading connections or changing selector state.
type clashAPIAccess struct {
	port   int
	secret string
	err    error
}

func newClashAPIAccess() *clashAPIAccess {
	access := &clashAPIAccess{}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		access.err = fmt.Errorf("generate Clash API secret: %w", err)
		return access
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		access.err = fmt.Errorf("allocate Clash API port: %w", err)
		return access
	}
	access.port = listener.Addr().(*net.TCPAddr).Port
	access.secret = hex.EncodeToString(raw)
	if err := listener.Close(); err != nil {
		access.err = fmt.Errorf("release Clash API port reservation: %w", err)
	}
	return access
}

func (c *clashAPIAccess) validate() error {
	if c == nil {
		return fmt.Errorf("Clash API access is not initialized")
	}
	if c.err != nil {
		return c.err
	}
	if c.port < 1 || c.port > 65535 || c.secret == "" {
		return fmt.Errorf("Clash API access is incomplete")
	}
	return nil
}

func (c *clashAPIAccess) apply(template map[string]interface{}) error {
	if err := c.validate(); err != nil {
		return err
	}
	experimental, ok := template["experimental"].(map[string]interface{})
	if !ok {
		experimental = map[string]interface{}{}
		template["experimental"] = experimental
	}
	experimental["clash_api"] = map[string]interface{}{
		"external_controller":         net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port)),
		"secret":                      c.secret,
		"access_control_allow_origin": []interface{}{clashAPIOrigin},
	}
	return nil
}

func (c *clashAPIAccess) baseURL() string {
	if c == nil || c.port <= 0 {
		return ""
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(c.port))
}

func (a *App) clashAPIAccessSnapshot() *clashAPIAccess {
	if a == nil || a.configBuilder == nil {
		return nil
	}
	return a.configBuilder.clashAPI
}

func (a *App) clashAPIURL(path string) (string, error) {
	access := a.clashAPIAccessSnapshot()
	if err := access.validate(); err != nil {
		return "", err
	}
	if path == "" {
		path = "/"
	} else if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return access.baseURL() + path, nil
}

func (a *App) newClashAPIRequest(method, path string, body io.Reader) (*http.Request, error) {
	endpoint, err := a.clashAPIURL(path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.clashAPIAccessSnapshot().secret)
	return req, nil
}

func (a *App) clashAPIGet(client *http.Client, path string) (*http.Response, error) {
	req, err := a.newClashAPIRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (a *App) clashAPIPortReady(timeout time.Duration) bool {
	access := a.clashAPIAccessSnapshot()
	if access == nil || access.port <= 0 {
		return false
	}
	return loopbackPortReady(access.port, timeout)
}

func clashProxyAPIPath(name string) string {
	return "/proxies/" + url.PathEscape(name)
}
