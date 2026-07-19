package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	XrayBridgeConfigFileName = "xray_config.json"
	XrayBridgePortStart      = 19081
	XrayBridgeStartupTimeout = 5 * time.Second
)

var XrayExeName = platformExecutableName("xray")

type XrayBridgeBuildResult struct {
	SingBoxProxies []ProxyConfig
	XrayConfig     map[string]interface{}
}

type XrayBridgeManager struct {
	exePath    string
	configPath string
	logger     func(string)

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopping bool
}

func NewXrayBridgeManager(basePath, resourcesPath string, logger func(string)) *XrayBridgeManager {
	return &XrayBridgeManager{
		exePath:    filepath.Join(basePath, "bin", XrayExeName),
		configPath: filepath.Join(resourcesPath, XrayBridgeConfigFileName),
		logger:     logger,
	}
}

func (m *XrayBridgeManager) log(message string) {
	if m.logger != nil {
		m.logger(fmt.Sprintf("[Xray] %s", message))
	}
}

func (m *XrayBridgeManager) IsInstalled() bool {
	return fileExists(m.exePath)
}

func (m *XrayBridgeManager) HasConfig() bool {
	if m.configPath == "" {
		return false
	}
	info, err := os.Stat(m.configPath)
	return err == nil && info.Size() > 0
}

func (m *XrayBridgeManager) Start() error {
	m.mu.Lock()

	if m.cmd != nil && m.cmd.Process != nil {
		m.mu.Unlock()
		return nil
	}
	m.stopping = false
	if !m.HasConfig() {
		m.mu.Unlock()
		return nil
	}
	if !m.IsInstalled() {
		m.mu.Unlock()
		return fmt.Errorf("%s not found: %s", XrayExeName, m.exePath)
	}
	if err := m.testConfig(); err != nil {
		m.mu.Unlock()
		return err
	}
	ports, err := m.socksPorts()
	if err != nil {
		m.mu.Unlock()
		return err
	}

	cmd := exec.Command(m.exePath, "run", "-config", m.configPath)
	cmd.Dir = filepath.Dir(m.configPath)
	configureBackgroundCommand(cmd)

	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		m.mu.Unlock()
		return err
	}
	attachManagedCmdToJob(cmd, "xray bridge", m.log)

	m.cmd = cmd
	m.log(fmt.Sprintf("started pid=%d config=%s", cmd.Process.Pid, m.configPath))
	go m.logOutput(stdout, "OUT")
	go m.logOutput(stderr, "ERR")
	go func() {
		err := cmd.Wait()
		m.mu.Lock()
		stopping := m.stopping || m.cmd != cmd
		if m.cmd == cmd {
			m.cmd = nil
		}
		if stopping {
			m.stopping = false
		}
		m.mu.Unlock()
		if stopping {
			m.log("stopped")
			return
		}
		if err != nil {
			m.log(fmt.Sprintf("exited with error: %v", err))
		} else {
			m.log("exited")
		}
	}()
	m.mu.Unlock()

	if err := m.waitForReady(cmd, ports, XrayBridgeStartupTimeout); err != nil {
		m.Stop()
		return err
	}
	return nil
}

func (m *XrayBridgeManager) Stop() {
	m.mu.Lock()
	cmd := m.cmd
	m.cmd = nil
	if cmd != nil {
		m.stopping = true
	}
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	if !terminateManagedCmdAndWait(cmd, 3*time.Second) {
		m.log("process termination timed out; orphan cleanup will retry it")
	}
}

func (m *XrayBridgeManager) logOutput(reader io.Reader, prefix string) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			m.log(fmt.Sprintf("[%s] %s", prefix, line))
		}
	}
	if err := scanner.Err(); err != nil {
		m.log(fmt.Sprintf("[%s] output read error: %v", prefix, err))
	}
}

func (m *XrayBridgeManager) testConfig() error {
	cmd := exec.Command(m.exePath, "run", "-test", "-config", m.configPath)
	cmd.Dir = filepath.Dir(m.configPath)
	output, err := combinedOutputManagedCommand(cmd, "xray config test", m.log)
	if err != nil {
		return fmt.Errorf("xray config check failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (m *XrayBridgeManager) socksPorts() ([]int, error) {
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return nil, err
	}
	var config struct {
		Inbounds []struct {
			Listen   string `json:"listen"`
			Port     int    `json:"port"`
			Protocol string `json:"protocol"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	ports := make([]int, 0, len(config.Inbounds))
	for _, inbound := range config.Inbounds {
		if inbound.Port <= 0 || strings.ToLower(inbound.Protocol) != "socks" {
			continue
		}
		listen := strings.TrimSpace(inbound.Listen)
		if listen == "" || listen == "127.0.0.1" || listen == "localhost" {
			ports = append(ports, inbound.Port)
		}
	}
	return ports, nil
}

func (m *XrayBridgeManager) waitForReady(cmd *exec.Cmd, ports []int, timeout time.Duration) error {
	if len(ports) == 0 {
		return nil
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		running := m.cmd == cmd
		m.mu.Unlock()
		if !running {
			return fmt.Errorf("xray exited before SOCKS ports became ready")
		}

		allReady := true
		for _, port := range ports {
			if !isLoopbackPortReady(port) {
				allReady = false
				break
			}
		}
		if allReady {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("xray SOCKS ports did not become ready within %s: %v", timeout, ports)
}

func BuildXrayBridgeConfig(proxies []ProxyConfig) XrayBridgeBuildResult {
	if len(proxies) == 0 {
		return XrayBridgeBuildResult{}
	}

	inbounds := make([]interface{}, 0, len(proxies))
	outbounds := make([]interface{}, 0, len(proxies)+1)
	rules := make([]interface{}, 0, len(proxies))
	singBoxProxies := make([]ProxyConfig, 0, len(proxies))
	usedPorts := map[int]bool{}

	for i, proxy := range proxies {
		port := reserveLoopbackPort(XrayBridgePortStart, usedPorts)
		inboundTag := fmt.Sprintf("xray-in-%d", i)
		outboundTag := fmt.Sprintf("xray-out-%d", i)

		inbounds = append(inbounds, map[string]interface{}{
			"tag":      inboundTag,
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "socks",
			"settings": map[string]interface{}{
				"auth": "noauth",
				"udp":  true,
			},
			"sniffing": map[string]interface{}{
				"enabled":      true,
				"destOverride": []string{"http", "tls", "quic"},
			},
		})

		outbounds = append(outbounds, proxy.ToXrayOutbound(outboundTag))
		rules = append(rules, map[string]interface{}{
			"type":        "field",
			"inboundTag":  []string{inboundTag},
			"outboundTag": outboundTag,
		})

		singBoxProxies = append(singBoxProxies, ProxyConfig{
			Type:       "socks",
			Tag:        proxy.Tag,
			Server:     "127.0.0.1",
			ServerPort: port,
			Name:       proxy.Name,
		})
	}

	outbounds = append(outbounds, map[string]interface{}{
		"protocol": "freedom",
		"tag":      "direct",
	})

	return XrayBridgeBuildResult{
		SingBoxProxies: singBoxProxies,
		XrayConfig: map[string]interface{}{
			"log": map[string]interface{}{
				"loglevel": "info",
			},
			"inbounds":  inbounds,
			"outbounds": outbounds,
			"routing": map[string]interface{}{
				"domainStrategy": "AsIs",
				"rules":          rules,
			},
		},
	}
}

func reserveLoopbackPort(start int, used map[int]bool) int {
	for port := start; port < start+10000; port++ {
		if used[port] {
			continue
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = listener.Close()
			used[port] = true
			return port
		}
	}

	port := start + len(used)
	for used[port] {
		port++
	}
	used[port] = true
	return port
}

func isLoopbackPortReady(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func platformExecutableName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func (p ProxyConfig) ToXrayOutbound(tag string) map[string]interface{} {
	outbound := map[string]interface{}{
		"tag":      tag,
		"protocol": p.Type,
		"settings": map[string]interface{}{
			"vnext": []interface{}{
				map[string]interface{}{
					"address": p.Server,
					"port":    p.ServerPort,
					"users": []interface{}{
						map[string]interface{}{
							"id":         p.UUID,
							"encryption": "none",
						},
					},
				},
			},
		},
	}

	if p.Flow != "" {
		vnext := outbound["settings"].(map[string]interface{})["vnext"].([]interface{})
		users := vnext[0].(map[string]interface{})["users"].([]interface{})
		users[0].(map[string]interface{})["flow"] = p.Flow
	}

	network := NormalizeTransport(p.Network)
	stream := map[string]interface{}{
		"network": network,
	}
	if p.Security != "" && p.Security != "none" {
		stream["security"] = p.Security
	}

	switch p.Security {
	case "tls":
		tlsSettings := map[string]interface{}{}
		if p.SNI != "" {
			tlsSettings["serverName"] = p.SNI
		}
		if p.Fingerprint != "" {
			tlsSettings["fingerprint"] = p.Fingerprint
		}
		if alpn := splitALPN(p.ALPN); len(alpn) > 0 {
			tlsSettings["alpn"] = alpn
		}
		stream["tlsSettings"] = tlsSettings
	case "reality":
		realitySettings := map[string]interface{}{}
		if p.SNI != "" {
			realitySettings["serverName"] = p.SNI
		}
		if p.Fingerprint != "" {
			realitySettings["fingerprint"] = p.Fingerprint
		}
		if p.PublicKey != "" {
			realitySettings["publicKey"] = p.PublicKey
		}
		if p.ShortID != "" {
			realitySettings["shortId"] = p.ShortID
		}
		if alpn := splitALPN(p.ALPN); len(alpn) > 0 {
			realitySettings["alpn"] = alpn
		}
		stream["realitySettings"] = realitySettings
	}

	if network == "xhttp" {
		xhttpSettings := map[string]interface{}{}
		if p.Path != "" {
			xhttpSettings["path"] = p.Path
		}
		if p.Host != "" {
			xhttpSettings["host"] = p.Host
		}
		if p.Mode != "" {
			xhttpSettings["mode"] = p.Mode
		}
		if extra := parseXrayExtra(p.Extra); extra != nil {
			xhttpSettings["extra"] = extra
		}
		stream["xhttpSettings"] = xhttpSettings
	}

	outbound["streamSettings"] = stream
	return outbound
}

func splitALPN(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func parseXrayExtra(value string) map[string]interface{} {
	if value == "" {
		return nil
	}
	extra := map[string]interface{}{}
	if err := json.Unmarshal([]byte(value), &extra); err != nil {
		return nil
	}
	return extra
}
