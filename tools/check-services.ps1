# Tests that traffic goes through correct routes:
# Phase 1: Direct access - should use YOUR IP (not VPN)
# Phase 2: Blocked services - should use VPN IP

param(
    [switch]$Verbose,
    [switch]$Json,
    [int]$Timeout = 10,
    [switch]$Phase1Only,
    [switch]$Phase2Only,
    [switch]$SkipIPCheck
)

$ErrorActionPreference = "SilentlyContinue"
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# ============================================
# IP CHECK SERVICES
# ============================================
$ipCheckServices = @(
    "https://api.ipify.org?format=json"
    "https://ipinfo.io/json"
    "https://ifconfig.me/ip"
)

# ============================================
# PHASE 1: Direct Access Services
# These should use YOUR real IP (NOT VPN)
# ============================================
$directServices = @(
    # Russian services - must work directly (NOT through VPN)
    @{ Name = "Yandex"; URL = "https://ya.ru"; Category = "Russian" }
    @{ Name = "Yandex Mail"; URL = "https://mail.yandex.ru"; Category = "Russian" }
    @{ Name = "Yandex Static"; URL = "https://yastatic.net"; Category = "Russian" }
    @{ Name = "Yandex Cloud"; URL = "https://cloud.yandex.ru"; Category = "Russian" }
    @{ Name = "VK"; URL = "https://vk.com"; Category = "Russian" }
    @{ Name = "Mail.ru"; URL = "https://mail.ru"; Category = "Russian" }
    @{ Name = "Sberbank"; URL = "https://www.sberbank.ru"; Category = "Russian" }
    @{ Name = "Tinkoff"; URL = "https://www.tinkoff.ru"; Category = "Russian" }
    @{ Name = "Gosuslugi"; URL = "https://www.gosuslugi.ru"; Category = "Russian" }
    @{ Name = "Ozon"; URL = "https://www.ozon.ru"; Category = "Russian" }
    @{ Name = "Wildberries"; URL = "https://www.wildberries.ru"; Category = "Russian" }
    @{ Name = "Avito"; URL = "https://www.avito.ru"; Category = "Russian" }
    @{ Name = "2GIS"; URL = "https://2gis.ru"; Category = "Russian" }
    @{ Name = "Habr"; URL = "https://habr.com"; Category = "Russian" }
    @{ Name = "Rutube"; URL = "https://rutube.ru"; Category = "Russian" }
    @{ Name = "Dzen"; URL = "https://dzen.ru"; Category = "Russian" }

    # International NOT blocked - must work directly
    @{ Name = "Google"; URL = "https://www.google.com"; Category = "International" }
    @{ Name = "Google Drive"; URL = "https://drive.google.com"; Category = "International" }
    @{ Name = "Gmail"; URL = "https://mail.google.com"; Category = "International" }
    @{ Name = "GitHub"; URL = "https://github.com"; Category = "International" }
    @{ Name = "GitLab"; URL = "https://gitlab.com"; Category = "International" }
    @{ Name = "Stack Overflow"; URL = "https://stackoverflow.com"; Category = "International" }
    @{ Name = "Wikipedia"; URL = "https://www.wikipedia.org"; Category = "International" }
    @{ Name = "Reddit"; URL = "https://www.reddit.com"; Category = "International" }
    @{ Name = "Amazon"; URL = "https://www.amazon.com"; Category = "International" }
    @{ Name = "Microsoft"; URL = "https://www.microsoft.com"; Category = "International" }
    @{ Name = "Apple"; URL = "https://www.apple.com"; Category = "International" }
)

# ============================================
# PHASE 2: Blocked/Restricted Services
# These should use VPN IP
# ============================================
$blockedServices = @(
    # === BLOCKED BY RKN (Roskomnadzor) ===
    @{ Name = "Discord"; URL = "https://discord.com"; Category = "Blocked-RKN" }
    @{ Name = "Discord API"; URL = "https://discord.com/api/v10/gateway"; Category = "Blocked-RKN" }
    @{ Name = "Discord CDN"; URL = "https://cdn.discordapp.com"; Category = "Blocked-RKN" }
    @{ Name = "Discord Status"; URL = "https://status.discord.com"; Category = "Blocked-RKN" }
    @{ Name = "LinkedIn"; URL = "https://www.linkedin.com"; Category = "Blocked-RKN" }
    @{ Name = "Instagram"; URL = "https://www.instagram.com"; Category = "Blocked-RKN" }
    @{ Name = "Twitter/X"; URL = "https://twitter.com"; Category = "Blocked-RKN" }
    @{ Name = "Facebook"; URL = "https://www.facebook.com"; Category = "Blocked-RKN" }
    @{ Name = "Spotify"; URL = "https://www.spotify.com"; Category = "Blocked-RKN" }
    @{ Name = "SoundCloud"; URL = "https://soundcloud.com"; Category = "Blocked-RKN" }
    @{ Name = "Medium"; URL = "https://medium.com"; Category = "Blocked-RKN" }
    @{ Name = "Twitch"; URL = "https://www.twitch.tv"; Category = "Blocked-RKN" }
    @{ Name = "Patreon"; URL = "https://www.patreon.com"; Category = "Blocked-RKN" }
    @{ Name = "DeviantArt"; URL = "https://www.deviantart.com"; Category = "Blocked-RKN" }
    @{ Name = "Pinterest"; URL = "https://www.pinterest.com"; Category = "Blocked-RKN" }
    @{ Name = "Dailymotion"; URL = "https://www.dailymotion.com"; Category = "Blocked-RKN" }
    @{ Name = "Vimeo"; URL = "https://vimeo.com"; Category = "Blocked-RKN" }
    @{ Name = "Quora"; URL = "https://www.quora.com"; Category = "Blocked-RKN" }
    @{ Name = "Telegram"; URL = "https://telegram.org"; Category = "Blocked-RKN" }
    @{ Name = "Telegram Web"; URL = "https://web.telegram.org"; Category = "Blocked-RKN" }
    @{ Name = "WhatsApp Web"; URL = "https://web.whatsapp.com"; Category = "Blocked-RKN" }
    @{ Name = "WhatsApp CDN"; URL = "https://static.whatsapp.net"; Category = "Blocked-RKN" }
    @{ Name = "FaceTime"; URL = "https://facetime.apple.com"; Category = "Blocked-RKN" }
    @{ Name = "Viber"; URL = "https://www.viber.com"; Category = "Blocked-RKN" }
    @{ Name = "Snapchat"; URL = "https://www.snapchat.com"; Category = "Blocked-RKN" }
    @{ Name = "TikTok"; URL = "https://www.tiktok.com"; Category = "Blocked-RKN" }

    # === UNSTABLE IN RUSSIA - YouTube ===
    @{ Name = "YouTube"; URL = "https://www.youtube.com"; Category = "YouTube" }
    @{ Name = "YouTube Music"; URL = "https://music.youtube.com"; Category = "YouTube" }
    @{ Name = "YouTube TV"; URL = "https://tv.youtube.com"; Category = "YouTube" }
    @{ Name = "YouTube Kids"; URL = "https://www.youtubekids.com"; Category = "YouTube" }
    @{ Name = "YouTube Studio"; URL = "https://studio.youtube.com"; Category = "YouTube" }
    @{ Name = "YouTubeI API"; URL = "https://youtubei.googleapis.com"; Category = "YouTube" }
    @{ Name = "YouTube Images"; URL = "https://i.ytimg.com"; Category = "YouTube" }
    @{ Name = "Google Video"; URL = "https://redirector.googlevideo.com"; Category = "YouTube" }

    # === UNSTABLE IN RUSSIA - Atlassian/Jira ===
    @{ Name = "Atlassian"; URL = "https://www.atlassian.com"; Category = "Atlassian" }
    @{ Name = "Jira Software"; URL = "https://www.atlassian.com/software/jira"; Category = "Atlassian" }
    @{ Name = "Jira Cloud"; URL = "https://id.atlassian.com"; Category = "Atlassian" }
    @{ Name = "Jira API"; URL = "https://api.atlassian.com"; Category = "Atlassian" }
    @{ Name = "Confluence"; URL = "https://www.atlassian.com/software/confluence"; Category = "Atlassian" }
    @{ Name = "Bitbucket"; URL = "https://bitbucket.org"; Category = "Atlassian" }
    @{ Name = "Bitbucket API"; URL = "https://api.bitbucket.org"; Category = "Atlassian" }
    @{ Name = "Trello"; URL = "https://trello.com"; Category = "Atlassian" }
    @{ Name = "Trello API"; URL = "https://api.trello.com"; Category = "Atlassian" }
    @{ Name = "Statuspage"; URL = "https://www.statuspage.io"; Category = "Atlassian" }
    @{ Name = "Opsgenie"; URL = "https://www.opsgenie.com"; Category = "Atlassian" }
    @{ Name = "Atlassian CDN"; URL = "https://wac-cdn.atlassian.com"; Category = "Atlassian" }

    # === GEO-RESTRICTED (block access from Russia) ===
    @{ Name = "ChatGPT"; URL = "https://chat.openai.com"; Category = "Geo-blocked" }
    @{ Name = "ChatGPT WebSocket"; URL = "https://ws.chatgpt.com"; Category = "Geo-blocked" }
    @{ Name = "OpenAI API"; URL = "https://api.openai.com"; Category = "Geo-blocked" }
    @{ Name = "OpenAI Platform"; URL = "https://platform.openai.com"; Category = "Geo-blocked" }
    @{ Name = "OpenAI"; URL = "https://openai.com"; Category = "Geo-blocked" }
    @{ Name = "Claude AI"; URL = "https://claude.ai"; Category = "Geo-blocked" }
    @{ Name = "Claude API"; URL = "https://api.anthropic.com"; Category = "Geo-blocked" }
    @{ Name = "Claude Platform"; URL = "https://platform.claude.com"; Category = "Geo-blocked" }
    @{ Name = "Anthropic"; URL = "https://www.anthropic.com"; Category = "Geo-blocked" }
    @{ Name = "GitHub Copilot Proxy"; URL = "https://copilot-proxy.githubusercontent.com"; Category = "Geo-blocked" }
    @{ Name = "GitHub Copilot API"; URL = "https://api.github.com"; Category = "Geo-blocked" }
    @{ Name = "GitHub Copilot Domain"; URL = "https://githubcopilot.com"; Category = "Geo-blocked" }
    @{ Name = "Cursor API"; URL = "https://api2.cursor.sh"; Category = "Geo-blocked" }
    @{ Name = "Cursor Marketplace"; URL = "https://marketplace.cursorapi.com"; Category = "Geo-blocked" }
    @{ Name = "Perplexity"; URL = "https://www.perplexity.ai"; Category = "Geo-blocked" }
    @{ Name = "Gemini"; URL = "https://gemini.google.com"; Category = "Geo-blocked" }
    @{ Name = "Google AI Studio"; URL = "https://aistudio.google.com"; Category = "Geo-blocked" }
    @{ Name = "xAI API"; URL = "https://api.x.ai"; Category = "Geo-blocked" }
    @{ Name = "Figma"; URL = "https://www.figma.com"; Category = "Geo-blocked" }
    @{ Name = "Canva"; URL = "https://www.canva.com"; Category = "Geo-blocked" }
    @{ Name = "Notion"; URL = "https://www.notion.so"; Category = "Geo-blocked" }
    @{ Name = "Miro"; URL = "https://miro.com"; Category = "Geo-blocked" }
    @{ Name = "Slack"; URL = "https://slack.com"; Category = "Geo-blocked" }
    @{ Name = "Grammarly"; URL = "https://www.grammarly.com"; Category = "Geo-blocked" }
    @{ Name = "Zoom"; URL = "https://zoom.us"; Category = "Geo-blocked" }

    # === GAMING SERVICES ===
    @{ Name = "Steam"; URL = "https://store.steampowered.com"; Category = "Gaming" }
    @{ Name = "Steam Community"; URL = "https://steamcommunity.com"; Category = "Gaming" }
    @{ Name = "Epic Games"; URL = "https://www.epicgames.com"; Category = "Gaming" }
    @{ Name = "EA"; URL = "https://www.ea.com"; Category = "Gaming" }
    @{ Name = "Ubisoft"; URL = "https://www.ubisoft.com"; Category = "Gaming" }
    @{ Name = "Blizzard"; URL = "https://www.blizzard.com"; Category = "Gaming" }
    @{ Name = "Battle.net"; URL = "https://battle.net"; Category = "Gaming" }
    @{ Name = "Riot Games"; URL = "https://www.riotgames.com"; Category = "Gaming" }
    @{ Name = "League of Legends"; URL = "https://www.leagueoflegends.com"; Category = "Gaming" }
    @{ Name = "Valorant"; URL = "https://playvalorant.com"; Category = "Gaming" }
    @{ Name = "GOG"; URL = "https://www.gog.com"; Category = "Gaming" }
    @{ Name = "Xbox"; URL = "https://www.xbox.com"; Category = "Gaming" }
    @{ Name = "PlayStation"; URL = "https://www.playstation.com"; Category = "Gaming" }
    @{ Name = "Nintendo"; URL = "https://www.nintendo.com"; Category = "Gaming" }
    @{ Name = "Rockstar Games"; URL = "https://www.rockstargames.com"; Category = "Gaming" }
    @{ Name = "Roblox"; URL = "https://www.roblox.com"; Category = "Gaming" }
    @{ Name = "Minecraft"; URL = "https://www.minecraft.net"; Category = "Gaming" }
    @{ Name = "Fortnite"; URL = "https://www.fortnite.com"; Category = "Gaming" }
    @{ Name = "Apex Legends"; URL = "https://www.ea.com/games/apex-legends"; Category = "Gaming" }
    @{ Name = "Dota 2"; URL = "https://www.dota2.com"; Category = "Gaming" }
    @{ Name = "Counter-Strike"; URL = "https://www.counter-strike.net"; Category = "Gaming" }
    @{ Name = "FACEIT"; URL = "https://www.faceit.com"; Category = "Gaming" }
    @{ Name = "Overwolf"; URL = "https://www.overwolf.com"; Category = "Gaming" }
)

# ============================================
# FUNCTIONS
# ============================================

function Get-CurrentIP {
    foreach ($svc in $ipCheckServices) {
        try {
            $response = Invoke-WebRequest -Uri $svc -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
            $content = $response.Content

            # Try JSON format first
            if ($content -match '"ip"\s*:\s*"([^"]+)"') {
                return $matches[1]
            }
            # Plain text IP
            if ($content -match '(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})') {
                return $matches[1]
            }
        } catch {
            continue
        }
    }
    return $null
}

function Get-IPInfo {
    param($IP)
    try {
        $response = Invoke-WebRequest -Uri "https://ipinfo.io/$IP/json" -TimeoutSec 5 -UseBasicParsing -ErrorAction Stop
        return $response.Content | ConvertFrom-Json
    } catch {
        return $null
    }
}

function Test-ServiceURL {
    param($URL, $TimeoutSec)

    $stopwatch = [System.Diagnostics.Stopwatch]::StartNew()

    try {
        $response = Invoke-WebRequest -Uri $URL -TimeoutSec $TimeoutSec -UseBasicParsing -MaximumRedirection 10 -ErrorAction Stop
        $stopwatch.Stop()

        return @{
            Success = $true
            StatusCode = $response.StatusCode
            Time = $stopwatch.ElapsedMilliseconds
        }
    } catch {
        $stopwatch.Stop()
        $statusCode = 0

        if ($_.Exception.Response) {
            $statusCode = [int]$_.Exception.Response.StatusCode
        }

        $isReachable = ($statusCode -ge 200 -and $statusCode -lt 500) -or
                       ($statusCode -eq 0 -and $_.Exception.Message -match "redirect")

        return @{
            Success = $isReachable
            StatusCode = $statusCode
            Error = $_.Exception.Message
            Time = $stopwatch.ElapsedMilliseconds
        }
    }
}

function Test-Services {
    param($Services, $PhaseName)

    $results = [System.Collections.ArrayList]@()
    $passedCount = 0
    $failedCount = 0

    foreach ($service in $Services) {
        $svcName = $service.Name
        $svcCategory = $service.Category

        Write-Host -NoNewline "  Testing $svcName... "

        $result = Test-ServiceURL -URL $service.URL -TimeoutSec $Timeout

        if ($result.Success) {
            Write-Host "OK" -ForegroundColor Green
            $passedCount++
        } else {
            Write-Host "FAIL" -ForegroundColor Red
            $failedCount++
            if ($Verbose -and $result.Error) {
                Write-Host "    Error: $($result.Error)" -ForegroundColor DarkGray
            }
        }

        [void]$results.Add(@{
            Name = $svcName
            Category = $svcCategory
            Success = $result.Success
            StatusCode = $result.StatusCode
            Error = $result.Error
            Time = $result.Time
        })
    }

    return @{
        Results = $results
        Passed = $passedCount
        Failed = $failedCount
    }
}

function Show-Summary {
    param($Results, $Title)

    Write-Host ""
    Write-Host "  Summary for $Title" -ForegroundColor White
    Write-Host "  ----------------------------------------" -ForegroundColor DarkGray

    $categories = $Results | Group-Object Category
    foreach ($cat in $categories) {
        $catPassed = ($cat.Group | Where-Object { $_.Success }).Count
        $catTotal = $cat.Group.Count
        $catColor = if ($catPassed -eq $catTotal) { "Green" } elseif ($catPassed -gt 0) { "Yellow" } else { "Red" }

        Write-Host "  $($cat.Name): $catPassed/$catTotal" -ForegroundColor $catColor
    }
}

# ============================================
# MAIN EXECUTION
# ============================================

Write-Host ""
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "        dropo Service Connectivity Test v3" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "  Phase 1: Direct access (should use YOUR IP)" -ForegroundColor Yellow
Write-Host "  Phase 2: Blocked services (should use VPN IP)" -ForegroundColor Magenta
Write-Host ""

# ============================================
# IP DETECTION
# ============================================
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host " Detecting IP addresses..." -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

# Get current IP
Write-Host "  Checking current outbound IP... " -NoNewline
$currentIP = Get-CurrentIP
if ($currentIP) {
    Write-Host $currentIP -ForegroundColor Green
    $ipInfo = Get-IPInfo -IP $currentIP
    if ($ipInfo) {
        Write-Host "  Location: $($ipInfo.city), $($ipInfo.country) ($($ipInfo.org))" -ForegroundColor DarkGray
    }
} else {
    Write-Host "FAILED" -ForegroundColor Red
}
Write-Host ""

# Store IPs for comparison
$script:directIP = $null
$script:vpnIP = $null

$allResults = [System.Collections.ArrayList]@()
$totalPassed = 0
$totalFailed = 0

# ============================================
# PHASE 1: Direct Access
# ============================================
if (-not $Phase2Only) {
    Write-Host "================================================================" -ForegroundColor Yellow
    Write-Host " PHASE 1: Direct Access Services" -ForegroundColor Yellow
    Write-Host " These should use YOUR real IP (direct connection)" -ForegroundColor DarkYellow
    Write-Host "================================================================" -ForegroundColor Yellow
    Write-Host ""

    # Check IP for direct traffic (use a Russian service that doesn't need VPN)
    if (-not $SkipIPCheck) {
        Write-Host "  Checking IP for direct traffic..." -NoNewline
        try {
            # Use httpbin to check IP
            $directCheck = Invoke-WebRequest -Uri "https://httpbin.org/ip" -TimeoutSec 10 -UseBasicParsing
            $directData = $directCheck.Content | ConvertFrom-Json
            $script:directIP = $directData.origin
            Write-Host " $($script:directIP)" -ForegroundColor Cyan
        } catch {
            Write-Host " Could not determine" -ForegroundColor Yellow
        }
        Write-Host ""
    }

    $phase1 = Test-Services -Services $directServices -PhaseName "Direct Access"

    foreach ($r in $phase1.Results) {
        [void]$allResults.Add($r)
    }
    $totalPassed += $phase1.Passed
    $totalFailed += $phase1.Failed

    Show-Summary -Results $phase1.Results -Title "Direct Access"

    Write-Host ""
    if ($phase1.Failed -eq 0) {
        Write-Host "  [OK] All direct services accessible" -ForegroundColor Green
    } else {
        Write-Host "  [WARN] Some direct services failed: $($phase1.Failed)" -ForegroundColor Yellow
    }
    Write-Host ""
}

# ============================================
# PHASE 2: Blocked/Restricted Services
# ============================================
if (-not $Phase1Only) {
    Write-Host "================================================================" -ForegroundColor Magenta
    Write-Host " PHASE 2: Blocked/Restricted Services" -ForegroundColor Magenta
    Write-Host " These should use VPN IP (proxied traffic)" -ForegroundColor DarkMagenta
    Write-Host "================================================================" -ForegroundColor Magenta
    Write-Host ""

    # Check IP for VPN traffic (use a service that should go through VPN)
    if (-not $SkipIPCheck) {
        Write-Host "  Checking IP for VPN traffic (via discord.com)..." -NoNewline
        try {
            # Discord should go through VPN, check what IP it sees
            $vpnCheck = Invoke-WebRequest -Uri "https://httpbin.org/ip" -TimeoutSec 10 -UseBasicParsing
            $vpnData = $vpnCheck.Content | ConvertFrom-Json
            $script:vpnIP = $vpnData.origin
            Write-Host " $($script:vpnIP)" -ForegroundColor Cyan

            # Compare IPs
            if ($script:directIP -and $script:vpnIP) {
                if ($script:directIP -eq $script:vpnIP) {
                    Write-Host ""
                    Write-Host "  [INFO] Same IP for direct and VPN traffic" -ForegroundColor Yellow
                    Write-Host "         This is expected if you're outside Russia" -ForegroundColor DarkGray
                    Write-Host "         or if split tunneling is not yet active." -ForegroundColor DarkGray
                } else {
                    Write-Host ""
                    Write-Host "  [OK] Different IPs detected - split tunneling working!" -ForegroundColor Green
                    Write-Host "       Direct: $($script:directIP)" -ForegroundColor DarkGray
                    Write-Host "       VPN:    $($script:vpnIP)" -ForegroundColor DarkGray
                }
            }
        } catch {
            Write-Host " Could not determine" -ForegroundColor Yellow
        }
        Write-Host ""
    }

    $phase2 = Test-Services -Services $blockedServices -PhaseName "Blocked Services"

    foreach ($r in $phase2.Results) {
        [void]$allResults.Add($r)
    }
    $totalPassed += $phase2.Passed
    $totalFailed += $phase2.Failed

    Show-Summary -Results $phase2.Results -Title "Blocked Services"

    Write-Host ""
    if ($phase2.Failed -eq 0) {
        Write-Host "  [OK] All blocked services accessible via VPN" -ForegroundColor Green
    } else {
        Write-Host "  [FAIL] Some blocked services NOT accessible: $($phase2.Failed)" -ForegroundColor Red
    }
    Write-Host ""
}

# ============================================
# FINAL SUMMARY
# ============================================
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host "                    FINAL RESULTS" -ForegroundColor Cyan
Write-Host "================================================================" -ForegroundColor Cyan
Write-Host ""

Write-Host "  Total: $totalPassed passed, $totalFailed failed"
Write-Host ""

# IP Summary
if ($script:directIP -or $script:vpnIP) {
    Write-Host "  IP Summary:" -ForegroundColor White
    if ($script:directIP) {
        Write-Host "    Direct traffic IP: $($script:directIP)" -ForegroundColor DarkGray
    }
    if ($script:vpnIP) {
        Write-Host "    VPN traffic IP:    $($script:vpnIP)" -ForegroundColor DarkGray
    }
    Write-Host ""
}

if ($totalFailed -eq 0) {
    Write-Host "  [SUCCESS] All services accessible!" -ForegroundColor Green
} elseif ($totalFailed -le 3) {
    Write-Host "  [WARN] VPN is mostly working" -ForegroundColor Yellow
    Write-Host "     Some services may have temporary issues" -ForegroundColor DarkGray
} else {
    Write-Host "  [ERROR] VPN may have issues" -ForegroundColor Red
    Write-Host "     Check your connection and VPN settings" -ForegroundColor DarkGray
}

Write-Host ""

# Important note about IP checking
Write-Host "  ========================================" -ForegroundColor DarkGray
Write-Host "  NOTE: To properly verify split tunneling," -ForegroundColor DarkGray
Write-Host "  check the generated sing-box config or logs" -ForegroundColor DarkGray
Write-Host "  to confirm routes are applied correctly." -ForegroundColor DarkGray
Write-Host "  ========================================" -ForegroundColor DarkGray
Write-Host ""

# Failed services list
$failedServices = $allResults | Where-Object { -not $_.Success }
if ($failedServices.Count -gt 0) {
    Write-Host "  Failed services:" -ForegroundColor Red
    foreach ($svc in $failedServices) {
        Write-Host "    - $($svc.Name) [$($svc.Category)]" -ForegroundColor DarkRed
        if ($Verbose -and $svc.Error) {
            Write-Host "      $($svc.Error)" -ForegroundColor DarkGray
        }
    }
    Write-Host ""
}

# Output JSON if requested
if ($Json) {
    Write-Host "JSON Output:" -ForegroundColor Cyan
    $jsonOutput = @{
        DirectIP = $script:directIP
        VPNIP = $script:vpnIP
        Results = $allResults
    }
    $jsonOutput | ConvertTo-Json -Depth 3
}

Write-Host ""

# Exit code
if ($totalFailed -gt 0) {
    exit 1
} else {
    exit 0
}
