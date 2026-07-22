//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// systemInspectorSupported marks that this platform has a real external-VPN
// inspector. It is the single source of truth for the "supported" flag the UI
// reads, replacing inline runtime.GOOS checks in vpn_conflict.go.
const systemInspectorSupported = true

const windowsExternalVPNProbeScript = `
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
$ProgressPreference = 'SilentlyContinue'
$items = @()
function Add-DropoVpnCandidate([string]$name, [string]$detail, [string]$source, [string]$status) {
  if ([string]::IsNullOrWhiteSpace($name) -and [string]::IsNullOrWhiteSpace($detail)) {
    return
  }
  $script:items += [pscustomobject]@{
    name = $name
    detail = $detail
    source = $source
    status = $status
  }
}

foreach ($allUser in @($false, $true)) {
  try {
    if ($allUser) {
      $connections = Get-VpnConnection -AllUserConnection -ErrorAction SilentlyContinue
    } else {
      $connections = Get-VpnConnection -ErrorAction SilentlyContinue
    }
    foreach ($connection in @($connections)) {
      if ($null -ne $connection -and [string]$connection.ConnectionStatus -eq 'Connected') {
        Add-DropoVpnCandidate ([string]$connection.Name) ([string]$connection.ServerAddress) 'vpn-connection' ([string]$connection.ConnectionStatus)
      }
    }
  } catch {}
}

try {
  $adapters = Get-NetAdapter -ErrorAction SilentlyContinue | Where-Object { [string]$_.Status -eq 'Up' }
  foreach ($adapter in @($adapters)) {
    Add-DropoVpnCandidate ([string]$adapter.Name) ([string]$adapter.InterfaceDescription) 'netadapter' ([string]$adapter.Status)
  }
} catch {
  try {
    $adapters = Get-CimInstance Win32_NetworkAdapter -Filter "NetEnabled = TRUE" -ErrorAction SilentlyContinue
    foreach ($adapter in @($adapters)) {
      $name = [string]$adapter.NetConnectionID
      if ([string]::IsNullOrWhiteSpace($name)) {
        $name = [string]$adapter.Name
      }
      Add-DropoVpnCandidate $name ([string]$adapter.Description) 'netadapter' 'Up'
    }
  } catch {}
}

# WinDivert-based DPI bypass tools compete for packet capture filters even
# though they do not expose a Windows VPN adapter. Surface them explicitly so
# the user can close the other zapret/GoodbyeDPI instance before starting dropo.
$dpiProcessNames = @('winws.exe', 'winws2.exe', 'nfqws.exe', 'nfqws2.exe', 'goodbyedpi.exe')
try {
  $processes = Get-CimInstance Win32_Process -ErrorAction SilentlyContinue | Where-Object {
    $dpiProcessNames -contains ([string]$_.Name).ToLowerInvariant()
  }
  foreach ($process in @($processes)) {
    $detail = 'PID ' + [string]$process.ProcessId
    if (-not [string]::IsNullOrWhiteSpace([string]$process.ExecutablePath)) {
      $detail += ' — ' + [string]$process.ExecutablePath
    }
    Add-DropoVpnCandidate ([string]$process.Name) $detail 'dpi-process' 'Running'
  }
} catch {
  try {
    $processes = Get-Process -ErrorAction SilentlyContinue | Where-Object {
      $dpiProcessNames -contains ($_.ProcessName.ToLowerInvariant() + '.exe')
    }
    foreach ($process in @($processes)) {
      Add-DropoVpnCandidate ($process.ProcessName + '.exe') ('PID ' + [string]$process.Id) 'dpi-process' 'Running'
    }
  } catch {}
}

try {
  $drivers = Get-CimInstance Win32_SystemDriver -ErrorAction SilentlyContinue | Where-Object {
    ([string]$_.Name -like 'WinDivert*') -and ([string]$_.State -eq 'Running')
  }
  foreach ($driver in @($drivers)) {
    Add-DropoVpnCandidate ([string]$driver.Name) ([string]$driver.PathName) 'packet-filter-service' 'Running'
  }
} catch {}

if ($items.Count -eq 0) {
  '[]'
} else {
  $items | ConvertTo-Json -Compress -Depth 4
}
`

func detectExternalVPNConflicts() ([]ExternalVPNConflict, error) {
	// Packet-filter competitors are the most important conflict and can be
	// enumerated without spawning PowerShell or waiting for WMI. Keep the slower
	// adapter/VPN inspection as an enrichment step.
	nativeCandidates, nativeErr := detectExternalPacketFilterProcesses()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", windowsExternalVPNProbeScript)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		if len(nativeCandidates) > 0 {
			return filterExternalVPNConflicts(nativeCandidates), nil
		}
		return nil, fmt.Errorf("external VPN check timed out")
	}
	if err != nil {
		if len(nativeCandidates) > 0 {
			return filterExternalVPNConflicts(nativeCandidates), nil
		}
		return nil, fmt.Errorf("%w: %s", err, string(output))
	}
	candidates, err := parseExternalVPNCandidates(output)
	if err != nil {
		return nil, fmt.Errorf("parse external VPN check: %w", err)
	}
	candidates = append(nativeCandidates, candidates...)
	if nativeErr != nil && len(candidates) == 0 {
		return nil, nativeErr
	}
	return filterExternalVPNConflicts(candidates), nil
}

func detectExternalPacketFilterProcesses() ([]externalVPNCandidate, error) {
	known := map[string]struct{}{
		"winws.exe": {}, "winws2.exe": {}, "nfqws.exe": {}, "nfqws2.exe": {}, "goodbyedpi.exe": {},
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	result := make([]externalVPNCandidate, 0)
	for {
		name := strings.ToLower(windows.UTF16ToString(entry.ExeFile[:]))
		if _, ok := known[name]; ok {
			result = append(result, externalVPNCandidate{
				Name: name, Detail: fmt.Sprintf("PID %d", entry.ProcessID), Source: "dpi-process", Status: "Running",
			})
		}
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
				break
			}
			return result, err
		}
	}
	return result, nil
}
