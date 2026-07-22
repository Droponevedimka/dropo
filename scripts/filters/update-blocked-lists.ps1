param(
    [switch]$CheckOnly,
    [string]$RepositoryRoot,
    [string]$SingBoxPath
)

$ErrorActionPreference = "Stop"

if (-not $RepositoryRoot) {
    $RepositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
} else {
    $RepositoryRoot = (Resolve-Path $RepositoryRoot).Path
}

$FiltersDirectory = Join-Path $RepositoryRoot "dependencies\filters"
$VersionPath = Join-Path $FiltersDirectory "version.json"
$RequiredFiles = @(
    "domains_all.lst",
    "ipsum.lst",
    "refilter_domains.srs",
    "refilter_ips.srs",
    "community_domains.srs",
    "community_ips.srs",
    "discord_ips.srs"
)

function Invoke-Download {
    param([string]$Url, [string]$Destination)

    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($curl) {
        & $curl.Source -fsSL --retry 3 --connect-timeout 10 --max-time 90 -A "dropo-filter-updater" -o $Destination $Url
        if ($LASTEXITCODE -ne 0) {
            throw "curl failed with exit code $LASTEXITCODE for $Url"
        }
        return
    }
    Invoke-WebRequest -UseBasicParsing -Headers @{ "User-Agent" = "dropo-filter-updater" } -Uri $Url -OutFile $Destination -TimeoutSec 90
}

function Read-NormalizedList {
    param(
        [string]$Path,
        [ValidateSet("Domain", "IP")][string]$Kind
    )

    $items = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::OrdinalIgnoreCase)
    foreach ($line in [System.IO.File]::ReadLines($Path)) {
        $value = $line.Trim()
        if (-not $value -or $value.StartsWith("#") -or $value.StartsWith("//")) {
            continue
        }
        $comment = $value.IndexOf("#", [StringComparison]::Ordinal)
        if ($comment -ge 0) {
            $value = $value.Substring(0, $comment).Trim()
        }
        if ($Kind -eq "Domain") {
            $value = ($value -replace '^\|\|', '' -replace '\^$', '').TrimStart(".").TrimEnd(".").ToLowerInvariant()
            if ($value -notmatch '^[a-z0-9_*-]+(?:\.[a-z0-9_*-]+)+$' -or $value.Contains("*")) {
                continue
            }
        } else {
            $parts = @($value -split '/', 2)
            $parsed = $null
            $prefixLength = 0
            if ($parts.Count -ne 2 -or
                -not [System.Net.IPAddress]::TryParse($parts[0], [ref]$parsed) -or
                -not [int]::TryParse($parts[1], [ref]$prefixLength)) {
                continue
            }
            $maximumPrefixLength = if ($parsed.AddressFamily -eq [System.Net.Sockets.AddressFamily]::InterNetwork) { 32 } else { 128 }
            if ($prefixLength -lt 0 -or $prefixLength -gt $maximumPrefixLength) {
                continue
            }
            $value = $parsed.ToString().ToLowerInvariant() + "/" + $prefixLength
        }
        if ($value) {
            $null = $items.Add($value)
        }
    }
    return @($items | Sort-Object)
}

function Write-Utf8Lines {
    param([string]$Path, [string[]]$Lines)
    [System.IO.File]::WriteAllLines($Path, $Lines, [System.Text.UTF8Encoding]::new($false))
}

function Compile-RuleSet {
    param(
        [string[]]$Items,
        [ValidateSet("Domain", "IP")][string]$Kind,
        [string]$Destination,
        [string]$TemporaryDirectory
    )

    if ($Items.Count -eq 0) {
        throw "Cannot compile an empty $Kind rule-set."
    }
    $ruleKey = if ($Kind -eq "Domain") { "domain_suffix" } else { "ip_cidr" }
    $jsonPath = Join-Path $TemporaryDirectory (([IO.Path]::GetFileNameWithoutExtension($Destination)) + ".json")
    $payload = [ordered]@{ version = 2; rules = @([ordered]@{ $ruleKey = $Items }) }
    [System.IO.File]::WriteAllText($jsonPath, ($payload | ConvertTo-Json -Depth 10), [System.Text.UTF8Encoding]::new($false))
    & $SingBoxPath rule-set compile $jsonPath -o $Destination
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $Destination -PathType Leaf)) {
        throw "sing-box failed to compile $Destination"
    }
}

function Get-Sha256 {
    param([string]$Path)

    # Git may check text catalogs out with LF or CRLF depending on the host.
    # Hash their canonical LF representation so the repository integrity gate
    # is deterministic on Windows, Linux and macOS. Binary rule-sets keep their
    # byte-for-byte digest.
    if ([IO.Path]::GetExtension($Path).Equals(".lst", [StringComparison]::OrdinalIgnoreCase)) {
        $text = [IO.File]::ReadAllText($Path, [Text.UTF8Encoding]::new($false, $true))
        $canonical = ($text -replace "`r`n", "`n" -replace "`r", "`n")
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($canonical)
        $algorithm = [Security.Cryptography.SHA256]::Create()
        try {
            return ([BitConverter]::ToString($algorithm.ComputeHash($bytes))).Replace("-", "").ToLowerInvariant()
        } finally {
            $algorithm.Dispose()
        }
    }
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Test-CurrentBundle {
    param([string]$LatestTag)

    if (-not (Test-Path -LiteralPath $VersionPath -PathType Leaf)) {
        return $false
    }
    try {
        $version = Get-Content -LiteralPath $VersionPath -Raw -Encoding UTF8 | ConvertFrom-Json
    } catch {
        return $false
    }
    if ([string]$version.filters_version -ne $LatestTag) {
        return $false
    }
    foreach ($name in $RequiredFiles) {
        $path = Join-Path $FiltersDirectory $name
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            return $false
        }
        $metadata = $version.files.$([IO.Path]::GetFileNameWithoutExtension($name))
        if (-not $metadata -or -not $metadata.sha256 -or (Get-Sha256 $path) -ne [string]$metadata.sha256) {
            return $false
        }
    }
    return $true
}

Write-Host "[FILTERS] Checking the latest Re-filter release..." -ForegroundColor Yellow
$latest = Invoke-RestMethod -Headers @{ "User-Agent" = "dropo-filter-updater" } -Uri "https://api.github.com/repos/1andrevich/Re-filter-lists/releases/latest" -TimeoutSec 30
$latestTag = [string]$latest.tag_name
if (-not $latestTag) {
    throw "The latest Re-filter release has no tag."
}

if (Test-CurrentBundle -LatestTag $latestTag) {
    Write-Host "[FILTERS] Bundled blocked lists are current ($latestTag)." -ForegroundColor Green
    exit 0
}
if ($CheckOnly) {
    throw "Bundled blocked lists are missing, damaged, or older than upstream release $latestTag. Run scripts/filters/update-blocked-lists.ps1 and commit dependencies/filters."
}

if (-not $SingBoxPath) {
    $SingBoxPath = Get-ChildItem (Join-Path $RepositoryRoot "dependencies") -Recurse -Filter "sing-box.exe" -File |
        Sort-Object FullName -Descending |
        Select-Object -First 1 -ExpandProperty FullName
}
if (-not $SingBoxPath -or -not (Test-Path -LiteralPath $SingBoxPath -PathType Leaf)) {
    throw "sing-box.exe is required to compile and validate bundled filters."
}

$temporaryDirectory = Join-Path ([IO.Path]::GetTempPath()) ("dropo-filter-update-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporaryDirectory | Out-Null
try {
    $assetByName = @{}
    foreach ($asset in $latest.assets) {
        $assetByName[[string]$asset.name] = $asset
    }
    foreach ($name in @("domains_all.lst", "ipsum.lst")) {
        if (-not $assetByName.ContainsKey($name)) {
            throw "The Re-filter release $latestTag does not contain $name."
        }
        Invoke-Download -Url ([string]$assetByName[$name].browser_download_url) -Destination (Join-Path $temporaryDirectory $name)
    }

    $communitySources = [ordered]@{
        "community_domains.lst" = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community.lst"
        "community_ips.lst"     = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community_ips.lst"
        "discord_ips.lst"       = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/discord_ips.lst"
    }
    foreach ($entry in $communitySources.GetEnumerator()) {
        Invoke-Download -Url $entry.Value -Destination (Join-Path $temporaryDirectory $entry.Key)
    }

    $lists = [ordered]@{
        refilter_domains  = @(Read-NormalizedList (Join-Path $temporaryDirectory "domains_all.lst") "Domain")
        refilter_ips      = @(Read-NormalizedList (Join-Path $temporaryDirectory "ipsum.lst") "IP")
        community_domains = @(Read-NormalizedList (Join-Path $temporaryDirectory "community_domains.lst") "Domain")
        community_ips     = @(Read-NormalizedList (Join-Path $temporaryDirectory "community_ips.lst") "IP")
        discord_ips       = @(Read-NormalizedList (Join-Path $temporaryDirectory "discord_ips.lst") "IP")
    }
    if ($lists.refilter_domains.Count -lt 1000 -or $lists.refilter_ips.Count -lt 1000) {
        throw "Upstream blocked lists are unexpectedly small."
    }

    Write-Utf8Lines (Join-Path $temporaryDirectory "domains_all.lst") $lists.refilter_domains
    Write-Utf8Lines (Join-Path $temporaryDirectory "ipsum.lst") $lists.refilter_ips
    Compile-RuleSet $lists.refilter_domains "Domain" (Join-Path $temporaryDirectory "refilter_domains.srs") $temporaryDirectory
    Compile-RuleSet $lists.refilter_ips "IP" (Join-Path $temporaryDirectory "refilter_ips.srs") $temporaryDirectory
    Compile-RuleSet $lists.community_domains "Domain" (Join-Path $temporaryDirectory "community_domains.srs") $temporaryDirectory
    Compile-RuleSet $lists.community_ips "IP" (Join-Path $temporaryDirectory "community_ips.srs") $temporaryDirectory
    Compile-RuleSet $lists.discord_ips "IP" (Join-Path $temporaryDirectory "discord_ips.srs") $temporaryDirectory

    $sourceUrls = [ordered]@{
        domains_all          = [string]$assetByName["domains_all.lst"].browser_download_url
        ipsum                = [string]$assetByName["ipsum.lst"].browser_download_url
        refilter_domains     = [string]$assetByName["domains_all.lst"].browser_download_url
        refilter_ips         = [string]$assetByName["ipsum.lst"].browser_download_url
        community_domains    = $communitySources["community_domains.lst"]
        community_ips        = $communitySources["community_ips.lst"]
        discord_ips          = $communitySources["discord_ips.lst"]
    }
    $fileMetadata = [ordered]@{}
    foreach ($name in $RequiredFiles) {
        $key = [IO.Path]::GetFileNameWithoutExtension($name)
        $path = Join-Path $temporaryDirectory $name
        $entry = [ordered]@{
            file       = $name
            source_url = $sourceUrls[$key]
            sha256     = Get-Sha256 $path
        }
        if ($lists.Contains($key)) {
            $entry.entries = $lists[$key].Count
        } elseif ($key -eq "domains_all") {
            $entry.entries = $lists.refilter_domains.Count
        } elseif ($key -eq "ipsum") {
            $entry.entries = $lists.refilter_ips.Count
        }
        $fileMetadata[$key] = $entry
    }
    $versionPayload = [ordered]@{
        schema_version  = 2
        filters_version = $latestTag
        updated_at      = ([DateTime]$latest.published_at).ToUniversalTime().ToString("o")
        source          = "https://github.com/1andrevich/Re-filter-lists"
        release_url     = [string]$latest.html_url
        max_age_days    = 30
        files           = $fileMetadata
    }
    [System.IO.File]::WriteAllText((Join-Path $temporaryDirectory "version.json"), ($versionPayload | ConvertTo-Json -Depth 10), [System.Text.UTF8Encoding]::new($false))

    New-Item -ItemType Directory -Path $FiltersDirectory -Force | Out-Null
    foreach ($name in @($RequiredFiles + "version.json")) {
        Copy-Item -LiteralPath (Join-Path $temporaryDirectory $name) -Destination (Join-Path $FiltersDirectory $name) -Force
    }
    Write-Host "[FILTERS] Updated bundled blocked lists to $latestTag ($($lists.refilter_domains.Count) domains, $($lists.refilter_ips.Count) networks)." -ForegroundColor Green
} finally {
    Remove-Item -LiteralPath $temporaryDirectory -Recurse -Force -ErrorAction SilentlyContinue
}
