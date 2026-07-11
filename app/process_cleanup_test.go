package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDropoMixedProxyPortsIncludesActiveAndProxyFallbackConfigs(t *testing.T) {
	basePath := t.TempDir()
	resourcesPath := filepath.Join(basePath, ResourcesFolder)
	if err := os.MkdirAll(resourcesPath, 0755); err != nil {
		t.Fatalf("create resources dir failed: %v", err)
	}

	activeConfig := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen_port": 2091},
		},
	}
	proxyConfig := map[string]interface{}{
		"inbounds": []interface{}{
			map[string]interface{}{"type": "tun", "tag": "tun-in"},
			map[string]interface{}{"type": "mixed", "tag": "mixed-in", "listen_port": 2092},
		},
	}
	if err := writeJSONConfig(filepath.Join(resourcesPath, "active_config.json"), activeConfig); err != nil {
		t.Fatalf("write active config failed: %v", err)
	}
	if err := writeJSONConfig(filepath.Join(resourcesPath, "deep_windows_proxy_config.json"), proxyConfig); err != nil {
		t.Fatalf("write proxy fallback config failed: %v", err)
	}

	app := &App{basePath: basePath}
	ports := app.dropoMixedProxyPorts()
	for _, want := range []int{defaultDropoMixedProxyPort, 2091, 2092} {
		if !containsInt(ports, want) {
			t.Fatalf("dropo mixed proxy ports = %v, want %d", ports, want)
		}
	}
}

func TestManagedSidecarPathsIncludesTelegramProxy(t *testing.T) {
	basePath := t.TempDir()
	binPath := filepath.Join(basePath, "bin")
	if err := os.MkdirAll(binPath, 0755); err != nil {
		t.Fatalf("create bin dir failed: %v", err)
	}
	tgProxyPath := filepath.Join(binPath, TgWsProxyProcessName)
	if err := os.WriteFile(tgProxyPath, []byte("test"), 0644); err != nil {
		t.Fatalf("write tg-ws-proxy placeholder failed: %v", err)
	}

	app := &App{basePath: basePath}
	paths := app.managedSidecarPaths()
	if !containsStringFold(paths, tgProxyPath) {
		t.Fatalf("managed sidecar paths = %v, want %s", paths, tgProxyPath)
	}
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsStringFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(filepath.Clean(value), filepath.Clean(want)) {
			return true
		}
	}
	return false
}
