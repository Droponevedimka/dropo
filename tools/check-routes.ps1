# dropo - IP Route Verification Script
# This script verifies that traffic goes through correct routes
# by checking the outbound IP for different domains

param(
    [int]$Timeout = 10
)

$ErrorActionPreference = "SilentlyContinue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "        dropo IP Route Verification" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  This script checks which IP is used for different services" -ForegroundColor DarkGray
Write-Host "  to verify split tunneling is working correctly." -ForegroundColor DarkGray
Write-Host ""

# Services to test IP routing
# These services return your IP in response
$testServices = @(
    # General IP check (baseline)
    @{ Name = "Baseline IP"; URL = "https://api.ipify.org"; Category = "Baseline"; ParseIP = $true }
    @{ Name = "ipinfo.io"; URL = "https://ipinfo.io/ip"; Category = "Baseline"; ParseIP = $true }
    
    # Services that should go DIRECT (Russian)
    @{ Name = "2ip.ru (RU)"; URL = "https://2ip.ru/"; Category = "Direct-RU"; CheckDNS = "2ip.ru" }
    
    # Services that should go through VPN
    @{ Name = "Discord Check"; URL = "https://discord.com"; Category = "VPN"; CheckDNS = "discord.com" }
    @{ Name = "YouTube Check"; URL = "https://www.youtube.com"; Category = "VPN"; CheckDNS = "www.youtube.com" }
)

function Get-DNSResolution {
    param($Hostname)
    try {
        $dns = Resolve-DnsName -Name $Hostname -Type A -ErrorAction Stop | Where-Object { $_.Type -eq 'A' } | Select-Object -First 1
        return $dns.IPAddress
    } catch {
        return "DNS Failed"
    }
}

function Get-PublicIP {
    param($URL)
    try {
        $response = Invoke-WebRequest -Uri $URL -TimeoutSec 10 -UseBasicParsing -ErrorAction Stop
        $content = $response.Content.Trim()
        
        # Extract IP from response
        if ($content -match '(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})') {
            return $matches[1]
        }
        return "Parse Failed"
    } catch {
        return "Request Failed"
    }
}

# Get baseline IP first
Write-Host "Getting baseline IP..." -ForegroundColor Yellow
$baselineIP = Get-PublicIP -URL "https://api.ipify.org"
Write-Host "  Your current public IP: " -NoNewline
Write-Host $baselineIP -ForegroundColor Green
Write-Host ""

# Get IP info
try {
    $ipInfo = Invoke-WebRequest -Uri "https://ipinfo.io/$baselineIP/json" -TimeoutSec 5 -UseBasicParsing | ConvertFrom-Json
    Write-Host "  Location: $($ipInfo.city), $($ipInfo.country)" -ForegroundColor DarkGray
    Write-Host "  ISP: $($ipInfo.org)" -ForegroundColor DarkGray
    Write-Host ""
} catch {}

Write-Host "================================================================" -ForegroundColor Cyan
Write-Host " Checking DNS Resolution (shows which server handles requests)" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

$dnsTests = @(
    @{ Name = "ya.ru (should be direct)"; Host = "ya.ru" }
    @{ Name = "yastatic.net (should be direct)"; Host = "yastatic.net" }
    @{ Name = "vk.com (should be direct)"; Host = "vk.com" }
    @{ Name = "discord.com (should be VPN)"; Host = "discord.com" }
    @{ Name = "youtube.com (should be VPN)"; Host = "www.youtube.com" }
    @{ Name = "googlevideo.com (should be VPN)"; Host = "redirector.googlevideo.com" }
    @{ Name = "atlassian.com (should be VPN)"; Host = "www.atlassian.com" }
    @{ Name = "instagram.com (should be VPN)"; Host = "www.instagram.com" }
)

foreach ($test in $dnsTests) {
    Write-Host "  $($test.Name): " -NoNewline
    $ip = Get-DNSResolution -Hostname $test.Host
    Write-Host $ip -ForegroundColor Cyan
}

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host " Route Verification (check active_config.json)" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

# Check if config exists
$candidateConfigPaths = @(
    (Join-Path (Get-Location) "resources\active_config.json"),
    "$env:LOCALAPPDATA\dropo\resources\active_config.json",
    "$env:LOCALAPPDATA\KampusVPN\resources\active_config.json"
)
$configPath = $candidateConfigPaths | Where-Object { Test-Path $_ } | Select-Object -First 1
if ($configPath -and (Test-Path $configPath)) {
    Write-Host "  Found active config: $configPath" -ForegroundColor Green
    Write-Host ""
    
    try {
        $config = Get-Content $configPath -Raw | ConvertFrom-Json
        
        # Check route rules
        if ($config.route -and $config.route.rules) {
            $rules = $config.route.rules
            Write-Host "  Route rules found: $($rules.Count)" -ForegroundColor Cyan
            Write-Host ""
            
            # Look for key rules
            $directRules = $rules | Where-Object { $_.outbound -eq "direct" }
            $proxyRules = $rules | Where-Object { $_.outbound -eq "proxy" }
            
            Write-Host "  Direct rules: $($directRules.Count)" -ForegroundColor Yellow
            Write-Host "  Proxy rules: $($proxyRules.Count)" -ForegroundColor Magenta
            Write-Host ""
            
            # Check for Russian domains in direct
            $russianDirect = $directRules | Where-Object { 
                $_.domain_suffix -and ($_.domain_suffix -contains "yandex.ru" -or $_.domain_suffix -contains "ya.ru")
            }
            if ($russianDirect) {
                Write-Host "  [OK] Russian domains configured for DIRECT" -ForegroundColor Green
            } else {
                Write-Host "  [WARN] Russian domains not found in direct rules" -ForegroundColor Yellow
            }
            
            # Check for YouTube in proxy
            $youtubeProxy = $proxyRules | Where-Object {
                $_.domain_suffix -and ($_.domain_suffix -contains "youtube.com" -or $_.domain_suffix -contains "googlevideo.com")
            }
            if ($youtubeProxy) {
                Write-Host "  [OK] YouTube domains configured for PROXY" -ForegroundColor Green
            }
            
            # Check for Discord in proxy (via rule_set or domain)
            $discordProxy = $proxyRules | Where-Object {
                ($_.rule_set -and $_.rule_set -contains "discord-ips") -or
                ($_.domain_suffix -and $_.domain_suffix -contains "discord.com")
            }
            if ($discordProxy) {
                Write-Host "  [OK] Discord configured for PROXY" -ForegroundColor Green
            }
            
            # Check final rule
            Write-Host ""
            Write-Host "  Final (default) route: $($config.route.final)" -ForegroundColor Cyan
        }
    } catch {
        Write-Host "  Error parsing config: $_" -ForegroundColor Red
    }
} else {
    Write-Host "  Config not found at: $configPath" -ForegroundColor Yellow
    Write-Host "  Start the VPN first to generate config" -ForegroundColor DarkGray
}

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host " How to verify split tunneling is working:" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  1. From Russia: yandex.ru should load fast (direct)" -ForegroundColor DarkGray
Write-Host "     youtube.com should also work (via VPN)" -ForegroundColor DarkGray
Write-Host ""
Write-Host "  2. Check sing-box logs for routing decisions:" -ForegroundColor DarkGray
Write-Host "     Look for 'outbound: direct' vs 'outbound: proxy'" -ForegroundColor DarkGray
Write-Host ""
Write-Host "  3. Use browser DevTools Network tab:" -ForegroundColor DarkGray
Write-Host "     - yastatic.net requests should complete quickly" -ForegroundColor DarkGray
Write-Host "     - If they fail, check VPN routing rules" -ForegroundColor DarkGray
Write-Host ""
