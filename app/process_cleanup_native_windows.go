//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const internetSettingsRegistryPath = `Software\Microsoft\Windows\CurrentVersion\Internet Settings`
const internetConnectionsRegistryPath = internetSettingsRegistryPath + `\Connections`

var internetSetOption = windows.NewLazySystemDLL("wininet.dll").NewProc("InternetSetOptionW")

func resetWindowsSystemProxyNativeForPorts(ports []int) (bool, string, error) {
	if len(ports) == 0 {
		return false, "", nil
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, internetSettingsRegistryPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return false, "", err
	}
	defer key.Close()

	proxy, _, _ := key.GetStringValue("ProxyServer")
	proxyPort := loopbackProxyPort(proxy)
	ownedPort := cleanupContainsInt(ports, proxyPort)
	staleLoopback := proxyPort > 0 && !loopbackPortListening(proxyPort)
	connectionNeedsReset := connectionSettingsNeedReset(ports, strings.TrimSpace(proxy) == "")
	if !ownedPort && !staleLoopback && !connectionNeedsReset {
		return false, proxy, nil
	}

	if err := key.SetDWordValue("ProxyEnable", 0); err != nil {
		return false, proxy, err
	}
	_ = key.SetDWordValue("AutoDetect", 0)
	_ = key.DeleteValue("ProxyServer")
	_ = key.DeleteValue("AutoConfigURL")
	resetConnectionSettingsFlags()

	// INTERNET_OPTION_SETTINGS_CHANGED and INTERNET_OPTION_REFRESH notify
	// WinINet consumers directly; no PowerShell Add-Type or reg.exe is needed.
	_, _, _ = internetSetOption.Call(0, 39, 0, 0)
	_, _, _ = internetSetOption.Call(0, 37, 0, 0)
	return true, proxy, nil
}

func cleanupContainsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func loopbackProxyPort(proxy string) int {
	for _, token := range strings.FieldsFunc(proxy, func(r rune) bool {
		return r == ';' || r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		if _, value, ok := strings.Cut(token, "="); ok {
			token = value
		}
		token = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(token), "http://"), "https://")
		host, portText, err := net.SplitHostPort(token)
		if err != nil {
			continue
		}
		if !strings.EqualFold(host, "localhost") && host != "127.0.0.1" && host != "::1" {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err == nil && port > 0 && port <= 65535 {
			return port
		}
	}
	return 0
}

func loopbackPortListening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func connectionSettingsNeedReset(ports []int, proxyEmpty bool) bool {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetConnectionsRegistryPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer key.Close()
	for _, name := range []string{"DefaultConnectionSettings", "SavedLegacySettings"} {
		value, _, err := key.GetBinaryValue(name)
		if err != nil || len(value) <= 8 {
			continue
		}
		if connectionBlobReferencesPorts(value, ports) || (proxyEmpty && value[8]&(2|4|8) != 0) {
			return true
		}
	}
	return false
}

func connectionBlobReferencesPorts(value []byte, ports []int) bool {
	words := make([]uint16, 0, len(value)/2)
	for index := 0; index+1 < len(value); index += 2 {
		words = append(words, binary.LittleEndian.Uint16(value[index:index+2]))
	}
	text := strings.ToLower(string(utf16.Decode(words)))
	for _, port := range ports {
		needle := ":" + strconv.Itoa(port)
		if strings.Contains(text, "127.0.0.1"+needle) || strings.Contains(text, "localhost"+needle) {
			return true
		}
	}
	return false
}

func resetConnectionSettingsFlags() {
	key, err := registry.OpenKey(registry.CURRENT_USER, internetConnectionsRegistryPath, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer key.Close()
	for _, name := range []string{"DefaultConnectionSettings", "SavedLegacySettings"} {
		value, _, err := key.GetBinaryValue(name)
		if err != nil || len(value) <= 8 {
			continue
		}
		value[8] = 1
		_ = key.SetBinaryValue(name, value)
	}
}

func killWindowsDropoManagedSidecarsNative(paths []string, roots []string) ([]int, error) {
	exact := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if abs, err := filepath.Abs(path); err == nil {
			exact[strings.ToLower(filepath.Clean(abs))] = struct{}{}
		}
	}
	normalizedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if abs, err := filepath.Abs(root); err == nil {
			normalizedRoots = append(normalizedRoots, filepath.Join(filepath.Clean(abs), "bin"))
		}
	}

	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	ownedNames := map[string]bool{
		"sing-box.exe":    true,
		"ciadpi.exe":      true,
		"winws2.exe":      true,
		"xray.exe":        true,
		"tg-ws-proxy.exe": true,
	}
	entry := windows.ProcessEntry32{}
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	var result []int
	var resultErr error
	currentPID := uint32(os.Getpid())
	for {
		if entry.ProcessID != 0 && entry.ProcessID != currentPID && ownedNames[strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))] {
			process, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, entry.ProcessID)
			if openErr == nil {
				imagePath, pathErr := processImagePath(process)
				owned := pathErr == nil && processPathOwned(imagePath, exact, normalizedRoots)
				if owned {
					if terminateErr := windows.TerminateProcess(process, 1); terminateErr != nil {
						resultErr = errors.Join(resultErr, fmt.Errorf("terminate pid %d: %w", entry.ProcessID, terminateErr))
					} else {
						result = append(result, int(entry.ProcessID))
					}
				}
				_ = windows.CloseHandle(process)
			}
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return result, errors.Join(resultErr, err)
		}
	}
	return result, resultErr
}

func processImagePath(process windows.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	size := uint32(len(buffer))
	if err := windows.QueryFullProcessImageName(process, 0, &buffer[0], &size); err != nil {
		return "", err
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:size])), nil
}

func processPathOwned(imagePath string, exact map[string]struct{}, roots []string) bool {
	abs, err := filepath.Abs(imagePath)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	if _, ok := exact[strings.ToLower(abs)]; ok {
		return true
	}
	for _, root := range roots {
		if pathIsInside(abs, root) {
			return true
		}
	}
	return false
}
