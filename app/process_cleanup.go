package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const managedSidecarCleanupTimeout = 8 * time.Second

const defaultDropoMixedProxyPort = 2088

func (a *App) cleanupManagedSidecarOrphans(reason string) {
	if runtime.GOOS != "windows" || a.basePath == "" {
		return
	}

	paths := a.managedSidecarPaths()
	roots := a.dropoPortableSidecarRoots()
	if len(paths) == 0 && len(roots) == 0 {
		return
	}

	killed, err := killWindowsDropoManagedSidecars(paths, roots)
	if err != nil {
		a.writeLog(fmt.Sprintf("[Cleanup] failed to scan managed sidecar processes (%s): %v", reason, err))
		return
	}
	if len(killed) > 0 {
		a.writeLog(fmt.Sprintf("[Cleanup] stopped orphaned managed sidecar process(es) (%s): %v", reason, killed))
	}
}

func (a *App) cleanupOwnedWinDivertService(reason string) {
	if a == nil || runtime.GOOS != "windows" || a.basePath == "" {
		return
	}
	roots := a.dropoPortableSidecarRoots()
	basePath, err := filepath.Abs(a.basePath)
	if err != nil {
		basePath = a.basePath
	}
	basePath = filepath.Clean(basePath)

	seen := map[string]bool{}
	for _, root := range roots {
		seen[strings.ToLower(filepath.Clean(root))] = true
	}
	if !seen[strings.ToLower(basePath)] {
		roots = append(roots, basePath)
	}
	if runtimeBase := a.runtimeBasePath(); runtimeBase != "" {
		if abs, err := filepath.Abs(runtimeBase); err == nil && !seen[strings.ToLower(filepath.Clean(abs))] {
			roots = append(roots, filepath.Clean(abs))
		}
	}
	cleanupWinDivertServiceIfOwned(roots, reason, func(message string) {
		a.writeLog("[Cleanup] " + message)
	})
}

func (a *App) cleanupDropoRuntimeResidue(reason string) {
	if a == nil {
		return
	}
	a.resetDropoSystemProxy(reason)
	a.cleanupManagedSidecarOrphans(reason)
	a.cleanupOwnedWinDivertService(reason)
}

func (a *App) resetDropoSystemProxy(reason string) {
	if a == nil || runtime.GOOS != "windows" {
		return
	}
	ports := a.dropoMixedProxyPorts()
	if len(ports) == 0 {
		ports = []int{defaultDropoMixedProxyPort}
	}
	changed, current, err := resetWindowsSystemProxyForPorts(ports)
	if err != nil {
		a.writeLog(fmt.Sprintf("[Cleanup] system proxy reset failed (%s): %v", reason, err))
		return
	}
	if changed {
		a.writeLog(fmt.Sprintf("[Cleanup] system proxy reset (%s): removed dropo proxy %q", reason, current))
	} else if current != "" {
		a.writeLog(fmt.Sprintf("[Cleanup] system proxy unchanged (%s): %q is not a dropo proxy", reason, current))
	}
}

func (a *App) dropoMixedProxyPorts() []int {
	ports := []int{defaultDropoMixedProxyPort}
	seen := map[int]bool{defaultDropoMixedProxyPort: true}

	add := func(port int) {
		if port <= 0 || seen[port] {
			return
		}
		seen[port] = true
		ports = append(ports, port)
	}

	for _, path := range a.dropoProxyConfigPaths() {
		for _, port := range mixedProxyPortsFromConfigFile(path) {
			add(port)
		}
	}
	return ports
}

func (a *App) dropoProxyConfigPaths() []string {
	paths := []string{}
	if a.storage != nil {
		resources := a.storage.GetResourcesPath()
		if resources != "" {
			paths = append(paths,
				filepath.Join(resources, "active_config.json"),
				filepath.Join(resources, "deep_windows_proxy_config.json"),
			)
		}
	}
	if a.basePath != "" {
		resources := filepath.Join(a.basePath, ResourcesFolder)
		paths = append(paths,
			filepath.Join(resources, "active_config.json"),
			filepath.Join(resources, "deep_windows_proxy_config.json"),
		)
	}
	return uniqueStrings(paths)
}

func mixedProxyPortsFromConfigFile(path string) []int {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil
	}
	inbounds, ok := config["inbounds"].([]interface{})
	if !ok {
		return nil
	}
	ports := []int{}
	for _, inbound := range inbounds {
		inboundMap, ok := inbound.(map[string]interface{})
		if !ok || inboundMap["type"] != "mixed" {
			continue
		}
		if port := mixedInboundPort(inboundMap["listen_port"]); port > 0 {
			ports = append(ports, port)
		}
	}
	return ports
}

func resetWindowsSystemProxyForPorts(ports []int) (bool, string, error) {
	if runtime.GOOS != "windows" || len(ports) == 0 {
		return false, "", nil
	}
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		if port > 0 {
			values = append(values, fmt.Sprintf("%d", port))
		}
	}
	if len(values) == 0 {
		return false, "", nil
	}
	quotedPorts := make([]string, 0, len(values))
	for _, value := range values {
		quotedPorts = append(quotedPorts, "'"+strings.ReplaceAll(value, "'", "''")+"'")
	}

	script := fmt.Sprintf(`
$key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
$connectionKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings\Connections'
$ports = @(%s)
$proxy = ''
$autoDetect = 0
try {
  $item = Get-ItemProperty -Path $key -ErrorAction Stop
  $proxy = [string]$item.ProxyServer
  if ($null -ne $item.AutoDetect) {
    $autoDetect = [int]$item.AutoDetect
  }
} catch {
  Write-Output 'ERROR|' + $_.Exception.Message
  exit 1
}
$isDropo = $false
foreach ($port in $ports) {
  if ($proxy -match "(^|[=;,\s])(?:https?=)?(?:https?://)?(?:127\.0\.0\.1|localhost):$port($|[;,\s])") {
    $isDropo = $true
    break
  }
}
$staleLoopbackProxy = $false
$loopbackProxyPort = 0
if ($proxy -match "(^|[=;,\s])(?:https?=)?(?:https?://)?(?:127\.0\.0\.1|localhost):(?<port>\d+)($|[;,\s])") {
  $loopbackProxyPort = [int]$Matches.port
  try {
    $listener = Get-NetTCPConnection -LocalAddress 127.0.0.1 -LocalPort $loopbackProxyPort -State Listen -ErrorAction SilentlyContinue
    if ($null -eq $listener) {
      $staleLoopbackProxy = $true
    }
  } catch {
    $socket = $null
    try {
      $socket = New-Object System.Net.Sockets.TcpClient
      $async = $socket.BeginConnect('127.0.0.1', $loopbackProxyPort, $null, $null)
      if (-not $async.AsyncWaitHandle.WaitOne(200)) {
        $staleLoopbackProxy = $true
      } else {
        $socket.EndConnect($async)
      }
    } catch {
      $staleLoopbackProxy = $true
    } finally {
      if ($null -ne $socket) { $socket.Close() }
    }
  }
}

$connectionNeedsReset = $false
try {
  $connectionItem = Get-ItemProperty -LiteralPath $connectionKey -ErrorAction Stop
  foreach ($name in @('DefaultConnectionSettings', 'SavedLegacySettings')) {
    $bytes = [byte[]]$connectionItem.$name
    if ($bytes -and $bytes.Length -gt 8) {
      $flag = [int]$bytes[8]
      $blobText = [System.Text.Encoding]::Unicode.GetString($bytes)
      $blobHasDropoProxy = $false
      foreach ($port in $ports) {
        if ($blobText -match "(?:127\.0\.0\.1|localhost):$port") {
          $blobHasDropoProxy = $true
          break
        }
      }
      if ($blobHasDropoProxy -or ($proxy -eq '' -and (($flag -band 2) -ne 0 -or ($flag -band 4) -ne 0 -or ($flag -band 8) -ne 0))) {
        $connectionNeedsReset = $true
      }
    }
  }
} catch {}

if ($isDropo -or $staleLoopbackProxy -or $connectionNeedsReset) {
  if (-not (Test-Path -LiteralPath $key)) {
    New-Item -Path $key -Force -ErrorAction SilentlyContinue | Out-Null
  }
  New-ItemProperty -Path $key -Name ProxyEnable -PropertyType DWord -Value 0 -Force -ErrorAction Stop | Out-Null
  New-ItemProperty -Path $key -Name AutoDetect -PropertyType DWord -Value 0 -Force -ErrorAction SilentlyContinue | Out-Null
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v ProxyEnable /t REG_DWORD /d 0 /f | Out-Null
  reg add "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings" /v AutoDetect /t REG_DWORD /d 0 /f | Out-Null
  Remove-ItemProperty -Path $key -Name ProxyServer -ErrorAction SilentlyContinue
  Remove-ItemProperty -Path $key -Name AutoConfigURL -ErrorAction SilentlyContinue
  try {
    $connectionItem = Get-ItemProperty -LiteralPath $connectionKey -ErrorAction Stop
    foreach ($name in @('DefaultConnectionSettings', 'SavedLegacySettings')) {
      $bytes = [byte[]]$connectionItem.$name
      if ($bytes -and $bytes.Length -gt 8) {
        $bytes[8] = 1
        Set-ItemProperty -Path $connectionKey -Name $name -Value $bytes -ErrorAction SilentlyContinue
      }
    }
  } catch {}
  $sig = @'
[DllImport("wininet.dll", SetLastError=true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
'@
  try {
    $type = Add-Type -MemberDefinition $sig -Name NativeMethods -Namespace WinInet -PassThru -ErrorAction Stop
    [void]$type::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
    [void]$type::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)
  } catch {}
  if ($staleLoopbackProxy) {
    Write-Output ('RESET|stale loopback proxy ' + $proxy)
  } else {
    Write-Output ('RESET|' + $proxy)
  }
} else {
  Write-Output ('UNCHANGED|' + $proxy)
}
`, strings.Join(quotedPorts, ","))

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if ctx.Err() == context.DeadlineExceeded {
		return false, "", fmt.Errorf("system proxy reset timed out")
	}
	if err != nil {
		return false, "", fmt.Errorf("%w: %s", err, text)
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "RESET|"):
			return true, strings.TrimPrefix(line, "RESET|"), nil
		case strings.HasPrefix(line, "UNCHANGED|"):
			return false, strings.TrimPrefix(line, "UNCHANGED|"), nil
		}
	}
	return false, "", nil
}

func (a *App) managedSidecarPaths() []string {
	basePath, err := filepath.Abs(a.runtimeBasePath())
	if err != nil {
		basePath = a.runtimeBasePath()
	}

	candidates := []string{
		a.singBoxPathSnapshot(),
		filepath.Join(a.binDir(), ByeDPIProcessName),
		filepath.Join(a.binDir(), SpoofDPIExeName),
		filepath.Join(a.binDir(), ZapretProcessName),
		filepath.Join(a.binDir(), XrayExeName),
		filepath.Join(a.binDir(), TgWsProxyProcessName),
	}

	seen := map[string]bool{}
	paths := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		if !pathIsInside(abs, basePath) || seen[strings.ToLower(abs)] || !fileExists(abs) {
			continue
		}
		seen[strings.ToLower(abs)] = true
		paths = append(paths, abs)
	}
	return paths
}

func (a *App) dropoPortableSidecarRoots() []string {
	basePath, err := filepath.Abs(a.basePath)
	if err != nil {
		basePath = a.basePath
	}
	basePath = filepath.Clean(basePath)

	seen := map[string]bool{}
	addRoot := func(path string, roots *[]string) {
		if path == "" {
			return
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return
		}
		abs = filepath.Clean(abs)
		key := strings.ToLower(abs)
		if seen[key] || !looksLikeDropoPortableRoot(abs) {
			return
		}
		if !hasManagedSidecarBin(abs) {
			return
		}
		seen[key] = true
		*roots = append(*roots, abs)
	}

	roots := []string{}
	addRoot(basePath, &roots)

	parent := filepath.Dir(basePath)
	entries, err := os.ReadDir(parent)
	if err != nil {
		return roots
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasPrefix(name, "dropo-") {
			continue
		}
		addRoot(filepath.Join(parent, entry.Name()), &roots)
	}
	return roots
}

func looksLikeDropoPortableRoot(path string) bool {
	name := strings.ToLower(filepath.Base(filepath.Clean(path)))
	return name == "dropo" || strings.HasPrefix(name, "dropo-")
}

func hasManagedSidecarBin(root string) bool {
	for _, name := range []string{ByeDPIProcessName, SpoofDPIExeName, ZapretProcessName, XrayExeName, TgWsProxyProcessName, "sing-box.exe"} {
		if fileExists(filepath.Join(root, "bin", name)) {
			return true
		}
	}
	return false
}

func pathIsInside(path, basePath string) bool {
	path = filepath.Clean(path)
	basePath = filepath.Clean(basePath)
	if strings.EqualFold(path, basePath) {
		return true
	}
	baseWithSeparator := basePath
	if !strings.HasSuffix(baseWithSeparator, string(filepath.Separator)) {
		baseWithSeparator += string(filepath.Separator)
	}
	return strings.HasPrefix(strings.ToLower(path), strings.ToLower(baseWithSeparator))
}

func killWindowsDropoManagedSidecars(paths []string, roots []string) ([]int, error) {
	if len(paths) == 0 && len(roots) == 0 {
		return nil, nil
	}

	quoted := make([]string, 0, len(paths))
	for _, path := range paths {
		quoted = append(quoted, "'"+strings.ReplaceAll(path, "'", "''")+"'")
	}
	quotedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		quotedRoots = append(quotedRoots, "'"+strings.ReplaceAll(root, "'", "''")+"'")
	}

	script := fmt.Sprintf(`
$paths = @(%s)
$roots = @(%s)
$names = @('sing-box', 'ciadpi', 'spoofdpi', 'winws', 'xray', 'tg-ws-proxy')
function Test-InsidePath($path, $root) {
  if (-not $path -or -not $root) { return $false }
  try {
    $fullPath = [System.IO.Path]::GetFullPath($path)
    $fullRoot = [System.IO.Path]::GetFullPath($root)
  } catch {
    return $false
  }
  if ($fullPath.Equals($fullRoot, [System.StringComparison]::OrdinalIgnoreCase)) { return $true }
  if (-not $fullRoot.EndsWith([System.IO.Path]::DirectorySeparatorChar)) {
    $fullRoot += [System.IO.Path]::DirectorySeparatorChar
  }
  return $fullPath.StartsWith($fullRoot, [System.StringComparison]::OrdinalIgnoreCase)
}
$procs = Get-Process -ErrorAction SilentlyContinue | Where-Object {
  if (-not ($names -contains $_.ProcessName)) { return $false }
  $exePath = $_.Path
  if (-not $exePath) { return $false }
  if ($paths -contains $exePath) { return $true }
  foreach ($root in $roots) {
    if (Test-InsidePath $exePath (Join-Path $root 'bin')) { return $true }
  }
  return $false
}
foreach ($p in $procs) {
  Write-Output $p.Id
  & taskkill.exe /F /T /PID $p.Id | Out-Null
}
`, strings.Join(quoted, ","), strings.Join(quotedRoots, ","))

	ctx, cancel := context.WithTimeout(context.Background(), managedSidecarCleanupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", script)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("cleanup timed out after %s", managedSidecarCleanupTimeout)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}

	pids := make([]int, 0)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func waitForProcessDone(done <-chan error, timeout time.Duration) bool {
	if done == nil {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func runBackgroundCommandWithTimeout(timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	configureBackgroundCommand(cmd)
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s timed out after %s", name, timeout)
	}
	return err
}
