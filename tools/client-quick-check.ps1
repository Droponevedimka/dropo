# dropo client quick connectivity diagnostics.
# Send this file to a client and ask them to run it while dropo VPN/free access is enabled.

param(
    [int]$Timeout = 8,
    [int]$MethodTimeout = 8,
    [string]$OutDir = "",
    [string]$AppDir = "",
    [switch]$SkipProxyCheck,
    [switch]$DeepMethodCheck,
    [switch]$CleanupDropoOrphans
)

$ErrorActionPreference = "SilentlyContinue"
$ProgressPreference = "SilentlyContinue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

if (-not $OutDir) {
    $stamp = Get-Date -Format "yyyyMMdd-HHmmss"
    $OutDir = Join-Path ([Environment]::GetFolderPath("Desktop")) "dropo-client-check-$stamp"
}
New-Item -ItemType Directory -Path $OutDir -Force | Out-Null

$directServices = @(
    @{ Name = "Yandex"; URL = "https://ya.ru"; Category = "Direct-RU" },
    @{ Name = "Yandex Mail"; URL = "https://mail.yandex.ru"; Category = "Direct-RU" },
    @{ Name = "VK"; URL = "https://vk.com"; Category = "Direct-RU" },
    @{ Name = "Ozon"; URL = "https://www.ozon.ru"; Category = "Direct-RU" },
    @{ Name = "Sber"; URL = "https://www.sberbank.ru"; Category = "Direct-RU" },
    @{ Name = "Gosuslugi"; URL = "https://www.gosuslugi.ru"; Category = "Direct-RU" },
    @{ Name = "Rutube"; URL = "https://rutube.ru"; Category = "Direct-RU" },
    @{ Name = "Habr"; URL = "https://habr.com"; Category = "Direct-RU" },
    @{ Name = "Google"; URL = "https://www.google.com"; Category = "Direct-Foreign" },
    @{ Name = "GitHub"; URL = "https://github.com"; Category = "Direct-Foreign" },
    @{ Name = "Wikipedia"; URL = "https://www.wikipedia.org"; Category = "Direct-Foreign" },
    @{ Name = "StackOverflow"; URL = "https://stackoverflow.com"; Category = "Direct-Foreign" }
)

$blockedServices = @(
    @{ Name = "Discord"; URL = "https://discord.com"; Category = "Blocked" },
    @{ Name = "Discord API"; URL = "https://discord.com/api/v10/gateway"; Category = "Blocked" },
    @{ Name = "Discord CDN"; URL = "https://cdn.discordapp.com"; Category = "Blocked" },
    @{ Name = "YouTube"; URL = "https://www.youtube.com"; Category = "Blocked" },
    @{ Name = "YouTube API"; URL = "https://youtubei.googleapis.com"; Category = "Blocked" },
    @{ Name = "YouTube Images"; URL = "https://i.ytimg.com/generate_204"; Category = "Blocked" },
    @{ Name = "YouTube video"; URL = "https://redirector.googlevideo.com"; Category = "Blocked" },
    @{ Name = "Instagram"; URL = "https://www.instagram.com"; Category = "Blocked" },
    @{ Name = "Facebook"; URL = "https://www.facebook.com"; Category = "Blocked" },
    @{ Name = "X"; URL = "https://x.com"; Category = "Blocked" },
    @{ Name = "LinkedIn"; URL = "https://www.linkedin.com"; Category = "Blocked" },
    @{ Name = "Spotify"; URL = "https://open.spotify.com"; Category = "Blocked" },
    @{ Name = "Twitch"; URL = "https://www.twitch.tv"; Category = "Blocked" },
    @{ Name = "Telegram"; URL = "https://telegram.org"; Category = "Blocked" },
    @{ Name = "Signal"; URL = "https://signal.org"; Category = "Blocked" },
    @{ Name = "WhatsApp Web"; URL = "https://web.whatsapp.com"; Category = "Blocked" },
    @{ Name = "WhatsApp CDN"; URL = "https://static.whatsapp.net"; Category = "Blocked" },
    @{ Name = "FaceTime"; URL = "https://facetime.apple.com"; Category = "Blocked" },
    @{ Name = "Viber"; URL = "https://www.viber.com"; Category = "Blocked" },
    @{ Name = "Snapchat"; URL = "https://www.snapchat.com"; Category = "Blocked" },
    @{ Name = "TikTok"; URL = "https://www.tiktok.com"; Category = "Blocked" },
    @{ Name = "ChatGPT"; URL = "https://chatgpt.com"; Category = "AI-VPNOnly" },
    @{ Name = "OpenAI API"; URL = "https://api.openai.com"; Category = "AI-VPNOnly" },
    @{ Name = "Copilot proxy"; URL = "https://copilot-proxy.githubusercontent.com"; Category = "AI-VPNOnly" },
    @{ Name = "Cursor API"; URL = "https://api2.cursor.sh"; Category = "AI-VPNOnly" }
)

function Test-TcpPort {
    param([string]$HostName, [int]$Port)
    try {
        $client = New-Object System.Net.Sockets.TcpClient
        $async = $client.BeginConnect($HostName, $Port, $null, $null)
        $ok = $async.AsyncWaitHandle.WaitOne(1000, $false)
        if ($ok) { $client.EndConnect($async) }
        $client.Close()
        return $ok
    } catch {
        return $false
    }
}

function Invoke-CheckUrl {
    param([string]$Url, [string]$ProxyUrl)
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        $params = @{
            Uri = $Url
            TimeoutSec = $Timeout
            UseBasicParsing = $true
            MaximumRedirection = 5
            ErrorAction = "Stop"
            Headers = @{ "User-Agent" = "dropo-client-quick-check/1.0" }
        }
        if ($ProxyUrl) {
            $params.Proxy = $ProxyUrl
        }
        $response = Invoke-WebRequest @params
        $sw.Stop()
        return [PSCustomObject]@{
            Success = $true
            Status = [int]$response.StatusCode
            TimeMs = [int]$sw.ElapsedMilliseconds
            Error = ""
        }
    } catch {
        $sw.Stop()
        $status = 0
        if ($_.Exception.Response) {
            try { $status = [int]$_.Exception.Response.StatusCode } catch {}
        }
        $reachable = ($status -ge 200 -and $status -lt 500)
        return [PSCustomObject]@{
            Success = $reachable
            Status = $status
            TimeMs = [int]$sw.ElapsedMilliseconds
            Error = $_.Exception.Message
        }
    }
}

function Invoke-CurlSocksCheck {
    param([string]$Url, [int]$Port)

    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if (-not $curl) {
        return [PSCustomObject]@{
            Success = $false
            Status = 0
            TimeMs = 0
            Error = "curl.exe not found"
        }
    }

    $errFile = Join-Path $OutDir ("curl-{0}-{1}.err" -f $Port, ([Guid]::NewGuid().ToString("N")))
    $sw = [System.Diagnostics.Stopwatch]::StartNew()
    try {
        $statusText = & $curl.Source `
            --location `
            --head `
            --silent `
            --show-error `
            --output NUL `
            --write-out "%{http_code}" `
            --max-time $MethodTimeout `
            --connect-timeout $MethodTimeout `
            --socks5-hostname "127.0.0.1:$Port" `
            --user-agent "dropo-client-quick-check/1.0" `
            $Url 2>$errFile
        $exitCode = $LASTEXITCODE
        $sw.Stop()
        $status = 0
        [int]::TryParse(($statusText | Select-Object -Last 1), [ref]$status) | Out-Null
        $errText = Get-Content $errFile -Raw -ErrorAction SilentlyContinue
        Remove-Item $errFile -ErrorAction SilentlyContinue
        return [PSCustomObject]@{
            Success = ($exitCode -eq 0 -and $status -ge 200 -and $status -lt 500)
            Status = $status
            TimeMs = [int]$sw.ElapsedMilliseconds
            Error = $errText
        }
    } catch {
        $sw.Stop()
        Remove-Item $errFile -ErrorAction SilentlyContinue
        return [PSCustomObject]@{
            Success = $false
            Status = 0
            TimeMs = [int]$sw.ElapsedMilliseconds
            Error = $_.Exception.Message
        }
    }
}

function Find-ActiveConfig {
    $candidates = @()
    if ($AppDir) {
        $candidates += (Join-Path $AppDir "resources\active_config.json")
        $candidates += (Join-Path $AppDir "resources\settings.json")
    }
    $candidates += (Join-Path $PSScriptRoot "resources\active_config.json")
    $candidates += (Join-Path (Split-Path $PSScriptRoot -Parent) "resources\active_config.json")
    $candidates += (Join-Path (Get-Location) "resources\active_config.json")
    $candidates += (Join-Path $env:ProgramFiles "dropo\resources\active_config.json")
    $candidates += (Join-Path $env:LOCALAPPDATA "dropo\resources\active_config.json")

    foreach ($path in $candidates | Select-Object -Unique) {
        if ($path -and (Test-Path $path -PathType Leaf)) {
            return $path
        }
    }
    return ""
}

function Get-ConfigSummary {
    param([string]$Path)
    if (-not $Path) { return $null }
    try {
        $config = Get-Content $Path -Raw | ConvertFrom-Json
        $mixed = $config.inbounds | Where-Object { $_.type -eq "mixed" } | Select-Object -First 1
        $tun = $config.inbounds | Where-Object { $_.type -eq "tun" } | Select-Object -First 1
        $tunAddress = @($tun.address) | Where-Object { $_ }
        $direct = $config.outbounds | Where-Object { $_.tag -eq "direct" } | Select-Object -First 1
        $autoSelect = $config.outbounds | Where-Object { $_.tag -eq "auto-select" } | Select-Object -First 1
        $proxyOutbounds = @($config.outbounds | Where-Object {
            $_.tag -like "proxy-*" -or
            $_.type -in @("vless", "vmess", "trojan", "shadowsocks", "hysteria2", "tuic")
        })
        $groups = $config.outbounds |
            Where-Object { $_.tag -like "bypass-*" -or $_.tag -in @("smart-bypass", "vpn-or-direct") } |
            ForEach-Object {
                [PSCustomObject]@{
                    Tag = $_.tag
                    Type = $_.type
                    Now = $_.default
                    Candidates = ($_.outbounds -join ",")
                    Url = $_.url
                }
            }
        return [PSCustomObject]@{
            Path = $Path
            MixedPort = $mixed.listen_port
            TunInterface = $tun.interface_name
            TunAutoRoute = $tun.auto_route
            TunStrictRoute = $tun.strict_route
            TunAddress = ($tunAddress -join ",")
            TunHasIPv6 = [bool](@($tunAddress | Where-Object { "$_" -match ":" }).Count -gt 0)
            DirectBindInterface = $direct.bind_interface
            RouteFinal = $config.route.final
            RouteAutoDetectInterface = $config.route.auto_detect_interface
            RouteDefaultInterface = $config.route.default_interface
            DnsFinal = $config.dns.final
            HasVpnCandidate = [bool]$autoSelect
            VpnCandidateCount = $proxyOutbounds.Count
            AutoSelectCandidates = if ($autoSelect) { ($autoSelect.outbounds -join ",") } else { "" }
            Groups = $groups
        }
    } catch {
        return [PSCustomObject]@{ Path = $Path; Error = $_.Exception.Message }
    }
}

function Get-ClashProxies {
    try {
        return Invoke-RestMethod -Uri "http://127.0.0.1:9090/proxies" -TimeoutSec 2
    } catch {
        return $null
    }
}

function Get-SingBoxListeners {
    $pids = @(Get-Process sing-box -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Id)
    if (-not $pids -or $pids.Count -eq 0) {
        return @()
    }

    return @(Get-NetTCPConnection -State Listen -ErrorAction SilentlyContinue |
        Where-Object { $pids -contains $_.OwningProcess } |
        Select-Object LocalAddress, LocalPort, OwningProcess)
}

function Get-LiveMixedPort {
    param([int]$ConfiguredPort, [object[]]$Listeners)

    if ($ConfiguredPort -and (Test-TcpPort "127.0.0.1" $ConfiguredPort)) {
        return $ConfiguredPort
    }

    $excluded = @(9090, 18091, 18092, 18093, 18094, 18095)
    $excluded += 19081..19120
    $loopback = @("127.0.0.1", "0.0.0.0", "::1", "::")
    $candidate = $Listeners |
        Where-Object { $loopback -contains $_.LocalAddress -and ($excluded -notcontains [int]$_.LocalPort) } |
        Sort-Object LocalPort |
        Select-Object -First 1

    if ($candidate) {
        return [int]$candidate.LocalPort
    }
    return 0
}

function Find-AppRoot {
    $candidates = @()
    if ($AppDir) { $candidates += $AppDir }
    $candidates += $PSScriptRoot
    $parent = Split-Path $PSScriptRoot -Parent
    if ($parent) { $candidates += $parent }
    $cwd = (Get-Location).Path
    if ($cwd) { $candidates += $cwd }

    foreach ($candidate in $candidates | Where-Object { $_ } | Select-Object -Unique) {
        $resolved = Resolve-Path $candidate -ErrorAction SilentlyContinue
        if (-not $resolved) { continue }
        $root = $resolved.Path
        if ((Test-Path (Join-Path $root "resources\active_config.json")) -or
            (Test-Path (Join-Path $root "bin\sing-box.exe")) -or
            (Test-Path (Join-Path $root "dropo.exe"))) {
            return $root
        }
    }
    return ""
}

function Test-PathInside {
    param([string]$Path, [string]$Root)
    if (-not $Path -or -not $Root) { return $false }
    try {
        $fullPath = [System.IO.Path]::GetFullPath($Path)
        $fullRoot = [System.IO.Path]::GetFullPath($Root)
        if (-not $fullRoot.EndsWith([System.IO.Path]::DirectorySeparatorChar)) {
            $fullRoot += [System.IO.Path]::DirectorySeparatorChar
        }
        return $fullPath.StartsWith($fullRoot, [System.StringComparison]::OrdinalIgnoreCase)
    } catch {
        return $false
    }
}

function Get-DropoProcessDetails {
    param([string]$Root)
    $names = @("dropo.exe", "sing-box.exe", "ciadpi.exe", "spoofdpi.exe", "winws.exe", "xray.exe")
    $managedNames = @("sing-box.exe", "ciadpi.exe", "spoofdpi.exe", "winws.exe", "xray.exe")
    Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
        Where-Object { $names -contains $_.Name } |
        ForEach-Object {
            $inside = Test-PathInside -Path $_.ExecutablePath -Root $Root
            [PSCustomObject]@{
                Name = $_.Name
                ProcessId = $_.ProcessId
                ParentProcessId = $_.ParentProcessId
                ExecutablePath = $_.ExecutablePath
                CommandLine = $_.CommandLine
                CreationDate = $_.CreationDate
                InsideAppRoot = $inside
                ManagedSidecar = ($inside -and ($managedNames -contains $_.Name))
            }
        }
}

function Stop-DropoManagedSidecars {
    param([object[]]$Processes)
    $killed = @()
    foreach ($proc in $Processes | Where-Object { $_.ManagedSidecar }) {
        try {
            & taskkill.exe /F /T /PID $proc.ProcessId | Out-Null
            $killed += $proc.ProcessId
        } catch {}
    }
    return $killed
}

Write-Host ""
Write-Host "dropo client quick check" -ForegroundColor Cyan
Write-Host "Output: $OutDir" -ForegroundColor DarkGray
Write-Host ""

$appRoot = Find-AppRoot
if ($appRoot -and -not $AppDir) {
    $AppDir = $appRoot
}
$processInfo = @(Get-DropoProcessDetails -Root $appRoot)
if ($CleanupDropoOrphans) {
    $killed = Stop-DropoManagedSidecars -Processes $processInfo
    if ($killed.Count -gt 0) {
        Start-Sleep -Milliseconds 800
        $processInfo = @(Get-DropoProcessDetails -Root $appRoot)
    }
    $killed | Set-Content (Join-Path $OutDir "cleanup-killed-pids.txt") -Encoding UTF8
}
$processInfo | Format-Table | Out-String | Set-Content (Join-Path $OutDir "processes.txt") -Encoding UTF8
$processInfo | Export-Csv (Join-Path $OutDir "processes.csv") -NoTypeInformation -Encoding UTF8
$processInfo | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $OutDir "processes.json") -Encoding UTF8
$singBoxListeners = Get-SingBoxListeners
$singBoxListeners | Export-Csv (Join-Path $OutDir "singbox-listeners.csv") -NoTypeInformation -Encoding UTF8

$activeConfigPath = Find-ActiveConfig
$configSummary = Get-ConfigSummary -Path $activeConfigPath
$configSummary | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $OutDir "config-summary.json") -Encoding UTF8

if ($activeConfigPath) {
    Copy-Item $activeConfigPath (Join-Path $OutDir "active_config.json") -Force
}

$mixedPort = $null
if ($configSummary -and $configSummary.MixedPort) {
    $mixedPort = [int]$configSummary.MixedPort
}
$liveMixedPort = Get-LiveMixedPort -ConfiguredPort $mixedPort -Listeners $singBoxListeners
$portList = @(9090, 18091, 18092, 18093, 18094, 18095)
if ($mixedPort -and ($portList -notcontains $mixedPort)) {
    $portList += $mixedPort
}
if ($liveMixedPort -and ($portList -notcontains $liveMixedPort)) {
    $portList += $liveMixedPort
}
$ports = foreach ($port in $portList) {
    [PSCustomObject]@{ Host = "127.0.0.1"; Port = $port; Open = (Test-TcpPort "127.0.0.1" $port) }
}
$ports | Export-Csv (Join-Path $OutDir "ports.csv") -NoTypeInformation -Encoding UTF8

Get-NetAdapter -ErrorAction SilentlyContinue |
    Select-Object Name, InterfaceDescription, Status, LinkSpeed, ifIndex |
    Export-Csv (Join-Path $OutDir "net-adapters.csv") -NoTypeInformation -Encoding UTF8
Get-NetRoute -DestinationPrefix "0.0.0.0/0" -ErrorAction SilentlyContinue |
    Sort-Object RouteMetric, InterfaceMetric |
    Select-Object DestinationPrefix, NextHop, InterfaceAlias, RouteMetric, InterfaceMetric |
    Export-Csv (Join-Path $OutDir "default-routes.csv") -NoTypeInformation -Encoding UTF8
Get-DnsClientServerAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
    Select-Object InterfaceAlias, ServerAddresses |
    Export-Csv (Join-Path $OutDir "dns-servers.csv") -NoTypeInformation -Encoding UTF8

netsh winhttp show proxy | Out-File (Join-Path $OutDir "winhttp-proxy.txt") -Encoding UTF8
Get-ItemProperty "HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings" -ErrorAction SilentlyContinue |
    Select-Object ProxyEnable, ProxyServer, AutoConfigURL |
    ConvertTo-Json -Depth 3 |
    Set-Content (Join-Path $OutDir "user-proxy.json") -Encoding UTF8

$clash = Get-ClashProxies
if ($clash) {
    $clash | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $OutDir "clash-proxies.json") -Encoding UTF8
}

$proxyUrl = ""
if (-not $SkipProxyCheck -and $liveMixedPort -and (Test-TcpPort "127.0.0.1" $liveMixedPort)) {
    $proxyUrl = "http://127.0.0.1:$liveMixedPort"
}

$allServices = @()
$allServices += $directServices
$allServices += $blockedServices

$results = foreach ($svc in $allServices) {
    Write-Host ("Testing {0,-18} " -f $svc.Name) -NoNewline
    $normal = Invoke-CheckUrl -Url $svc.URL -ProxyUrl ""
    $proxy = $null
    if ($proxyUrl) {
        $proxy = Invoke-CheckUrl -Url $svc.URL -ProxyUrl $proxyUrl
    }

    $color = if ($normal.Success) { "Green" } elseif ($proxy -and $proxy.Success) { "Yellow" } else { "Red" }
    $statusText = if ($normal.Success) {
        "OK"
    } elseif ($proxy -and $proxy.Success -and $svc.Category -eq "AI-VPNOnly") {
        "VPN_PROXY_OK"
    } elseif ($proxy -and $proxy.Success) {
        "TUN_FAIL_PROXY_OK"
    } else {
        "FAIL"
    }
    Write-Host $statusText -ForegroundColor $color

    [PSCustomObject]@{
        Name = $svc.Name
        Category = $svc.Category
        Url = $svc.URL
        NormalSuccess = $normal.Success
        NormalStatus = $normal.Status
        NormalTimeMs = $normal.TimeMs
        NormalError = $normal.Error
        ProxyChecked = [bool]$proxyUrl
        ProxySuccess = if ($proxy) { $proxy.Success } else { $null }
        ProxyStatus = if ($proxy) { $proxy.Status } else { $null }
        ProxyTimeMs = if ($proxy) { $proxy.TimeMs } else { $null }
        ProxyError = if ($proxy) { $proxy.Error } else { "" }
    }
}

$results | Export-Csv (Join-Path $OutDir "service-results.csv") -NoTypeInformation -Encoding UTF8
$results | ConvertTo-Json -Depth 5 | Set-Content (Join-Path $OutDir "service-results.json") -Encoding UTF8
$results |
    Where-Object { -not $_.NormalSuccess } |
    Select-Object Name, Category, Url, NormalStatus, NormalTimeMs, NormalError, ProxyChecked, ProxySuccess, ProxyError |
    Format-List |
    Out-String |
    Set-Content (Join-Path $OutDir "failures.txt") -Encoding UTF8

$methodResults = @()
if ($DeepMethodCheck) {
    $freeProxyMethods = @(
        @{ Tag = "byedpi"; Port = 18091; Type = "socks" },
        @{ Tag = "byedpi-sni"; Port = 18092; Type = "socks" },
        @{ Tag = "byedpi-oob"; Port = 18093; Type = "socks" },
        @{ Tag = "byedpi-fake"; Port = 18094; Type = "socks" },
        @{ Tag = "spoofdpi-socks"; Port = 18095; Type = "socks" }
    )
    $openFreeProxyMethods = $freeProxyMethods | Where-Object { Test-TcpPort "127.0.0.1" $_.Port }
    $failedBlocked = $results | Where-Object { $_.Category -notlike "Direct*" -and -not $_.NormalSuccess }

    if ($failedBlocked -and $openFreeProxyMethods) {
        Write-Host ""
        Write-Host "Deep free proxy method check" -ForegroundColor Cyan
        foreach ($svc in $failedBlocked) {
            foreach ($method in $openFreeProxyMethods) {
                Write-Host ("  {0,-18} via {1,-12} " -f $svc.Name, $method.Tag) -NoNewline
                $check = Invoke-CurlSocksCheck -Url $svc.Url -Port $method.Port
                $color = if ($check.Success) { "Green" } else { "Red" }
                $text = if ($check.Success) { "OK" } else { "FAIL" }
                Write-Host $text -ForegroundColor $color
                $methodResults += [PSCustomObject]@{
                    Name = $svc.Name
                    Category = $svc.Category
                    Url = $svc.Url
                    Method = $method.Tag
                    Port = $method.Port
                    Success = $check.Success
                    Status = $check.Status
                    TimeMs = $check.TimeMs
                    Error = $check.Error
                }
            }
        }
    }
}
$methodResults | Export-Csv (Join-Path $OutDir "free-method-results.csv") -NoTypeInformation -Encoding UTF8
$methodResults | Export-Csv (Join-Path $OutDir "byedpi-method-results.csv") -NoTypeInformation -Encoding UTF8

$noRouteGroups = @()
if ($configSummary -and $configSummary.Groups) {
    $noRouteGroups = @($configSummary.Groups |
        Where-Object {
            $_.Now -eq "dropo-block" -or
            $_.Candidates -match '(^|,)dropo-block(,|$)'
        } |
        Select-Object -ExpandProperty Tag)
}

$summary = [PSCustomObject]@{
    CreatedAt = (Get-Date).ToString("s")
    AppRoot = $appRoot
    ActiveConfig = $activeConfigPath
    MixedProxy = $proxyUrl
    ConfiguredMixedPort = $mixedPort
    LiveMixedPort = $liveMixedPort
    Processes = $processInfo
    Ports = $ports
    SingBoxListeners = $singBoxListeners
    Config = $configSummary
    Total = $results.Count
    NormalFailed = ($results | Where-Object { -not $_.NormalSuccess }).Count
    ProxyRecovered = ($results | Where-Object { -not $_.NormalSuccess -and $_.ProxySuccess }).Count
    MethodRecovered = ($methodResults | Where-Object { $_.Success } | Select-Object -ExpandProperty Name -Unique).Count
    DirectFailed = ($results | Where-Object { $_.Category -like "Direct*" -and -not $_.NormalSuccess }).Count
    BlockedFailed = ($results | Where-Object { $_.Category -notlike "Direct*" -and -not $_.NormalSuccess }).Count
    NoRouteGroupCount = $noRouteGroups.Count
    NoRouteGroups = ($noRouteGroups -join ",")
    ManagedSidecarProcesses = ($processInfo | Where-Object { $_.ManagedSidecar }).Count
}
$summary | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $OutDir "summary.json") -Encoding UTF8

Write-Host ""
Write-Host "Summary" -ForegroundColor Cyan
Write-Host "  App root:      $appRoot"
Write-Host "  Active config: $activeConfigPath"
Write-Host "  Mixed proxy:   $proxyUrl"
if ($summary.ManagedSidecarProcesses -gt 0) {
    Write-Host "  Managed sidecars still running: $($summary.ManagedSidecarProcesses)" -ForegroundColor Yellow
    Write-Host "  To clean only Dropo bundled sidecars, rerun with -CleanupDropoOrphans" -ForegroundColor Yellow
}
if ($configSummary) {
    Write-Host "  VPN candidate: $($configSummary.HasVpnCandidate) ($($configSummary.VpnCandidateCount) proxy outbound(s))"
    if ($configSummary.TunAddress) {
        Write-Host "  TUN address:   $($configSummary.TunAddress)"
    }
    if ($configSummary.TunHasIPv6) {
        Write-Host "  TUN IPv6:      enabled (can break IPv4-only client networks)" -ForegroundColor Yellow
    }
}
if ($noRouteGroups.Count -gt 0) {
    Write-Host "  No-route groups: $($noRouteGroups -join ', ')" -ForegroundColor Yellow
}
if ($mixedPort -and $mixedPort -ne $liveMixedPort) {
    Write-Host "  Mixed port:    $mixedPort in config, live $liveMixedPort" -ForegroundColor Yellow
} elseif ($mixedPort -and -not $proxyUrl) {
    Write-Host "  Mixed port:    $mixedPort (not listening)" -ForegroundColor Yellow
}
Write-Host "  Normal failed: $($summary.NormalFailed)/$($summary.Total)"
Write-Host "  Proxy rescued: $($summary.ProxyRecovered)"
if ($DeepMethodCheck) {
    Write-Host "  Method rescued:$($summary.MethodRecovered)"
}
Write-Host "  Direct failed: $($summary.DirectFailed)"
Write-Host "  Blocked failed:$($summary.BlockedFailed)"
if ($summary.BlockedFailed -gt 0 -and $DeepMethodCheck -and $summary.MethodRecovered -eq 0 -and $configSummary -and -not $configSummary.HasVpnCandidate) {
    Write-Host ""
    Write-Host "Blocked services failed through every live free proxy method and active config has no VPN/VLESS candidate." -ForegroundColor Yellow
    Write-Host "If winws/zapret is running, send the full output folder; transparent-method results are visible in route-probe logs." -ForegroundColor Yellow
}
Write-Host ""
Write-Host "Send this folder back for analysis:" -ForegroundColor Yellow
Write-Host "  $OutDir"

$firstFailures = $results | Where-Object { -not $_.NormalSuccess } | Select-Object -First 5
if ($firstFailures) {
    Write-Host ""
    Write-Host "First errors:" -ForegroundColor Red
    foreach ($failure in $firstFailures) {
        Write-Host "  $($failure.Name): $($failure.NormalError)" -ForegroundColor DarkRed
    }
}

if ($summary.NormalFailed -gt 0) {
    exit 1
}
exit 0
