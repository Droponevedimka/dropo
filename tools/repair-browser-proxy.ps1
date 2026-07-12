param(
    [switch]$KillBrowsers,
    [switch]$ResetBrowserPolicies,
    [switch]$FixFirefoxProfiles
)

$ErrorActionPreference = 'Continue'

function Write-Step($Text) {
    Write-Host ""
    Write-Host "== $Text ==" -ForegroundColor Cyan
}

function Reset-WinInetProxy {
    Write-Step "Reset Windows proxy"
    $key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
    $connectionKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings\Connections'

    if (-not (Test-Path -LiteralPath $key)) {
        New-Item -Path $key -Force -ErrorAction SilentlyContinue | Out-Null
    }
    New-ItemProperty -Path $key -Name ProxyEnable -PropertyType DWord -Value 0 -Force -ErrorAction SilentlyContinue | Out-Null
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
                Write-Host "$name -> direct only"
            }
        }
    } catch {
        Write-Warning "Cannot update WinINET connection blob: $($_.Exception.Message)"
    }

    try {
        Add-Type -Namespace WinINet -Name Native -MemberDefinition @'
[DllImport("wininet.dll", SetLastError=true)]
public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
'@ -ErrorAction SilentlyContinue
        [WinINet.Native]::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0) | Out-Null
        [WinINet.Native]::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0) | Out-Null
    } catch {}

    netsh winhttp reset proxy | Out-Host
    ipconfig /flushdns | Out-Host
}

function Reset-BrowserPolicyProxy {
    Write-Step "Set browser proxy policies to direct"
    foreach ($path in @(
        'HKCU:\Software\Policies\Google\Chrome',
        'HKCU:\Software\Policies\Microsoft\Edge'
    )) {
        New-Item -Path $path -Force -ErrorAction SilentlyContinue | Out-Null
        New-ItemProperty -Path $path -Name ProxyMode -PropertyType String -Value 'direct' -Force -ErrorAction SilentlyContinue | Out-Null
    }

    $firefoxPolicy = 'HKCU:\Software\Policies\Mozilla\Firefox\Proxy'
    New-Item -Path $firefoxPolicy -Force -ErrorAction SilentlyContinue | Out-Null
    New-ItemProperty -Path $firefoxPolicy -Name Mode -PropertyType String -Value 'none' -Force -ErrorAction SilentlyContinue | Out-Null
}

function Fix-FirefoxProfileProxy {
    Write-Step "Fix Firefox profile proxy prefs"
    $root = Join-Path $env:APPDATA 'Mozilla\Firefox'
    if (!(Test-Path $root)) {
        Write-Host "Firefox profile folder not found: $root"
        return
    }

    Get-ChildItem -Path $root -Recurse -Filter prefs.js -ErrorAction SilentlyContinue | ForEach-Object {
        $path = $_.FullName
        $content = Get-Content -LiteralPath $path -Raw -ErrorAction SilentlyContinue
        if ($null -eq $content) { return }

        if ($content -notmatch 'network\.proxy') {
            return
        }

        $backup = "$path.dropo-proxy-backup-$(Get-Date -Format yyyyMMdd-HHmmss)"
        Copy-Item -LiteralPath $path -Destination $backup -Force

        $lines = $content -split "`r?`n"
        $filtered = $lines | Where-Object { $_ -notmatch 'user_pref\("network\.proxy\.' }
        $filtered += 'user_pref("network.proxy.type", 0);'
        Set-Content -LiteralPath $path -Value ($filtered -join "`r`n") -Encoding ASCII
        Write-Host "Fixed $path"
        Write-Host "Backup $backup"
    }
}

function Show-ProxyState {
    Write-Step "Current proxy state"
    $key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
    $item = Get-ItemProperty -Path $key -ErrorAction SilentlyContinue
    foreach ($name in @('ProxyEnable', 'ProxyServer', 'AutoConfigURL', 'AutoDetect')) {
        $value = $null
        if ($null -ne $item) {
            $value = $item.$name
        }
        if ($null -eq $value -or ($value -is [string] -and $value -eq '')) {
            $value = '<not set>'
        }
        Write-Host ("{0}: {1}" -f $name, $value)
    }
    reg query "HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings\Connections" 2>$null
    netsh winhttp show proxy
}

function Show-LoopbackProxyAttempts {
    Write-Step "Loopback proxy attempts"
    $lines = netstat -ano | Select-String '127\.0\.0\.1:54846|127\.0\.0\.1:2088|127\.0\.0\.1:7301'
    if (!$lines) {
        Write-Host "No Dropo/old loopback proxy attempts found." -ForegroundColor Green
        return
    }

    $lines | ForEach-Object { $_.Line }
    $processIds = $lines | ForEach-Object { ($_ -split '\s+')[-1] } | Sort-Object -Unique
    foreach ($processId in $processIds) {
        try {
            Get-Process -Id ([int]$processId) -ErrorAction Stop | Select-Object Id, ProcessName, Path, StartTime | Format-List
        } catch {
            Write-Host "PID ${processId}: cannot inspect"
        }
    }
}

function Kill-BrowserProcesses {
    Write-Step "Close browsers"
    foreach ($name in @('chrome', 'firefox', 'msedge')) {
        Get-Process -Name $name -ErrorAction SilentlyContinue | ForEach-Object {
            Write-Host "Stopping $($_.ProcessName) pid=$($_.Id)"
            Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
        }
    }
}

Reset-WinInetProxy
if ($ResetBrowserPolicies) {
    Reset-BrowserPolicyProxy
}
if ($FixFirefoxProfiles) {
    Fix-FirefoxProfileProxy
}
if ($KillBrowsers) {
    Kill-BrowserProcesses
}
Show-ProxyState
Show-LoopbackProxyAttempts

Write-Host ""
Write-Host "Done. Fully reopen Chrome/Firefox after this script finishes." -ForegroundColor Green
