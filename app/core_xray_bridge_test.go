package main

import (
	"fmt"
	"net"
	"testing"
)

func TestBuildXrayBridgeConfigSkipsBusyBridgePort(t *testing.T) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", XrayBridgePortStart))
	if err != nil {
		t.Skipf("cannot reserve test port %d: %v", XrayBridgePortStart, err)
	}
	defer listener.Close()

	result := BuildXrayBridgeConfig([]ProxyConfig{
		{
			Type:       "vless",
			Tag:        "xhttp-test",
			Name:       "xhttp-test",
			Server:     "example.com",
			ServerPort: 443,
			UUID:       "11111111-1111-1111-1111-111111111111",
			Network:    "xhttp",
			Security:   "tls",
		},
	})

	if len(result.SingBoxProxies) != 1 {
		t.Fatalf("sing-box bridge proxies = %#v, want one proxy", result.SingBoxProxies)
	}
	proxyPort := result.SingBoxProxies[0].ServerPort
	if proxyPort == XrayBridgePortStart {
		t.Fatalf("bridge reused busy port %d", XrayBridgePortStart)
	}

	inbounds, _ := result.XrayConfig["inbounds"].([]interface{})
	if len(inbounds) != 1 {
		t.Fatalf("xray inbounds = %#v, want one inbound", result.XrayConfig["inbounds"])
	}
	inbound, _ := inbounds[0].(map[string]interface{})
	if inbound["port"] != proxyPort {
		t.Fatalf("xray inbound port = %#v, want sing-box proxy port %d", inbound["port"], proxyPort)
	}
}
