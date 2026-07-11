//go:build windows

package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
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

if ($items.Count -eq 0) {
  '[]'
} else {
  $items | ConvertTo-Json -Compress -Depth 4
}
`

func detectExternalVPNConflicts() ([]ExternalVPNConflict, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", windowsExternalVPNProbeScript)
	configureBackgroundCommand(cmd)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("external VPN check timed out")
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, string(output))
	}
	candidates, err := parseExternalVPNCandidates(output)
	if err != nil {
		return nil, fmt.Errorf("parse external VPN check: %w", err)
	}
	return filterExternalVPNConflicts(candidates), nil
}
