# dropo Build Script
# This script builds the application and outputs to release/dropo-{version}-{hash}/ folder.

param(
    [switch]$Build,
    [switch]$Portable,
    # Build every implemented release artifact in one pass:
    # Windows portable app + Android arm64 APK.
    [switch]$All,
    [switch]$Clean,
    [string]$Version,
    # CI mode: build only dropo.exe + app folder (no bundled bin/). The heavy
    # dependencies archive is hosted once and referenced via deps-lock.json /
    # the dependencies.json manifest. Can be combined with -Android so CI can
    # publish Windows portable + Android APK without rebuilding Windows deps.
    [switch]$AppOnly,
    # Full-build mode: record the freshly built dependencies archive as hosted
    # on the current app version, even when depsVersion did not change. Use this
    # when re-baselining dependencies onto a new release tag.
    [switch]$ForceDepsRelease,
    # Backward-compatible alias: Windows is now built with Flutter + Go core.
    [switch]$Flutter,
    # Opt-in Android artifact build. Alone it builds only the APK; combine with
    # -Build/-Flutter for Windows app + Android, or use -All for all artifacts.
    [switch]$Android,
    # Local/offline rebuild aid: reuse an existing Flutter Windows Release
    # output while still rebuilding and signing the Go core and launcher.
    [switch]$ReuseFlutterWindowsOutput,
    # Local/offline signing aid. Production CI leaves this disabled and uses
    # the configured RFC3161 timestamp server.
    [switch]$SkipWindowsTimestamp,
    # Local/offline packaging aid. The lock identity is still embedded and
    # verified at runtime; only the network existence probe is skipped.
    [switch]$SkipHostedDependenciesCheck,
    # Development-only escape hatch. Production Windows artifacts must be
    # Authenticode-signed and the build fails closed when no certificate exists.
    [switch]$AllowUnsignedWindows,
    # Pet-project mode: require an exact certificate thumbprint and accept only
    # the expected "untrusted root" verification result before users install
    # the bundled public certificate. CI never enables this switch.
    [switch]$AllowUntrustedSelfSignedWindows
)

$ErrorActionPreference = "Stop"
$ScriptRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$CertificateSourceDir = Join-Path $ScriptRoot "scripts\signing\certificate"

# Read version from version.json
function Get-VersionInfo {
    $versionFile = Join-Path $ScriptRoot "version.json"
    if (-not (Test-Path $versionFile)) {
        Write-Host "[ERROR] version.json not found!" -ForegroundColor Red
        exit 1
    }
    return Get-Content $versionFile | ConvertFrom-Json
}

# Get latest version from release folder
function Get-LatestRelease {
    $releaseDir = Join-Path $ScriptRoot "release"
    if (-not (Test-Path $releaseDir)) {
        return $null
    }

    $versions = @(Get-ChildItem -Path $releaseDir -Directory |
        Where-Object { $_.Name -match '^(dropo-)?(\d+\.\d+\.\d+)-([0-9a-f]+)$' } |
        ForEach-Object {
            $null = $_.Name -match '^(dropo-)?(\d+\.\d+\.\d+)-([0-9a-f]+)$'
            [PSCustomObject]@{
                Name      = $_.Name
                Version   = [version]$Matches[2]
                WriteTime = $_.LastWriteTime
            }
        } |
        Sort-Object Version, WriteTime -Descending)

    if ($versions.Count -gt 0) {
        return $versions[0].Name
    }
    return $null
}

# Generate short random hash for build identification
function Get-BuildHash {
    $bytes = New-Object byte[] 4
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $rng.GetBytes($bytes)
    return [BitConverter]::ToString($bytes).Replace("-", "").ToLower().Substring(0, 7)
}

function Copy-LicenseFile {
    param(
        [string]$Source,
        [string]$DestinationDir,
        [string]$DestinationName
    )

    if (-not (Test-Path $Source -PathType Leaf)) {
        throw "License file not found: $Source"
    }

    if (-not (Test-Path $DestinationDir)) {
        New-Item -ItemType Directory -Path $DestinationDir | Out-Null
    }

    Copy-Item $Source (Join-Path $DestinationDir $DestinationName) -Force
    Write-Host "[OK] Copied licenses/$DestinationName" -ForegroundColor Green
}

function Download-File {
    param(
        [string]$Url,
        [string]$Destination
    )

    $parent = Split-Path $Destination -Parent
    if ($parent -and -not (Test-Path $parent)) {
        New-Item -ItemType Directory -Path $parent | Out-Null
    }

    $curl = Get-Command curl.exe -ErrorAction SilentlyContinue
    if ($curl) {
        & $curl.Source -fsSL --retry 3 --connect-timeout 10 --max-time 60 -A "dropo-build" -o $Destination $Url
        if ($LASTEXITCODE -ne 0) {
            throw "curl failed with exit code $LASTEXITCODE for $Url"
        }
        return
    }

    Invoke-WebRequest -UseBasicParsing -Headers @{ "User-Agent" = "dropo-build" } -Uri $Url -OutFile $Destination -TimeoutSec 60
}

function Test-ReleaseAssetAvailable {
    param(
        [string]$Url,
        [long]$ExpectedSize = 0
    )

    try {
        $request = [System.Net.HttpWebRequest]::Create($Url)
        $request.Method = "HEAD"
        $request.AllowAutoRedirect = $true
        $request.UserAgent = "dropo-build"
        $request.Timeout = 15000
        $response = $request.GetResponse()
        try {
            $status = [int]$response.StatusCode
            if ($status -lt 200 -or $status -ge 300) {
                return $false
            }
            if ($ExpectedSize -gt 0 -and $response.ContentLength -gt 0 -and $response.ContentLength -ne $ExpectedSize) {
                Write-Host "[Deps] Hosted asset size mismatch: expected $ExpectedSize, got $($response.ContentLength)" -ForegroundColor Yellow
                return $false
            }
            return $true
        } finally {
            $response.Close()
        }
    } catch {
        Write-Host "[Deps] Hosted asset check failed: $($_.Exception.Message)" -ForegroundColor Yellow
        return $false
    }
}

function Read-FilterList {
    param(
        [string]$Path,
        [ValidateSet("Domain", "IP")]
        [string]$Kind
    )

    $items = New-Object System.Collections.Generic.List[string]
    foreach ($line in Get-Content $Path -Encoding UTF8) {
        $value = $line.Trim()
        if (-not $value -or $value.StartsWith("#") -or $value.StartsWith("//")) {
            continue
        }
        if ($value.Contains("#")) {
            $value = $value.Split("#", 2)[0].Trim()
        }
        if (-not $value) {
            continue
        }

        if ($Kind -eq "Domain") {
            $value = $value -replace '^\|\|', ''
            $value = $value -replace '\^$', ''
            $value = $value.TrimStart(".")
        }

        if ($value) {
            $items.Add($value)
        }
    }

    return $items | Sort-Object -Unique
}

function Compile-RuleSetFromList {
    param(
        [string]$ListPath,
        [string]$OutputPath,
        [string]$Kind
    )

    $items = @(Read-FilterList -Path $ListPath -Kind $Kind)
    if ($items.Count -eq 0) {
        throw "Filter list is empty: $ListPath"
    }

    $ruleKey = if ($Kind -eq "Domain") { "domain_suffix" } else { "ip_cidr" }
    $jsonPath = [System.IO.Path]::ChangeExtension($OutputPath, ".json")
    $rule = [ordered]@{ $ruleKey = $items }
    $payload = [ordered]@{
        version = 2
        rules   = @($rule)
    }
    $json = $payload | ConvertTo-Json -Depth 20
    [System.IO.File]::WriteAllText($jsonPath, $json, (New-Object System.Text.UTF8Encoding($false)))

    & $SingBoxExe rule-set compile $jsonPath -o $OutputPath
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $OutputPath)) {
        throw "sing-box rule-set compile failed for $ListPath"
    }

    Remove-Item $jsonPath -Force -ErrorAction SilentlyContinue
    return $items.Count
}

function Update-BundledFilters {
    $filtersDir = Join-Path $DepsDir "filters"
    if (-not (Test-Path $filtersDir)) {
        New-Item -ItemType Directory -Path $filtersDir | Out-Null
    }

    $requiredFiles = @(
        "refilter_domains.srs",
        "refilter_ips.srs",
        "community_domains.srs",
        "community_ips.srs",
        "discord_ips.srs"
    )

    if (-not (Test-Path $SingBoxExe)) {
        throw "Cannot update bundled filters: sing-box.exe not found at $SingBoxExe"
    }

    Write-Host "[FILTERS] Checking upstream Re-filter release..." -ForegroundColor Yellow
    try {
        $latest = Invoke-RestMethod -Headers @{ "User-Agent" = "dropo-build" } -Uri "https://api.github.com/repos/1andrevich/Re-filter-lists/releases/latest" -TimeoutSec 30
    } catch {
        $missingRequired = $false
        foreach ($file in $requiredFiles) {
            if (-not (Test-Path (Join-Path $filtersDir $file))) {
                $missingRequired = $true
                break
            }
        }
        if ($missingRequired) {
            throw "Cannot check upstream filters and bundled filters are incomplete: $($_.Exception.Message)"
        }
        Write-Host "[FILTERS] Upstream check failed, using bundled filters: $($_.Exception.Message)" -ForegroundColor Yellow
        return
    }
    $publishedAt = ([DateTime]$latest.published_at).ToUniversalTime()

    $versionFile = Join-Path $filtersDir "version.json"
    $localUpdatedAt = [DateTime]::MinValue
    if (Test-Path $versionFile) {
        try {
            $localVersion = Get-Content $versionFile -Raw -Encoding UTF8 | ConvertFrom-Json
            if ($localVersion.updated_at) {
                $localUpdatedAt = ([DateTime]$localVersion.updated_at).ToUniversalTime()
            }
        } catch {
            Write-Host "[FILTERS] Could not parse local version.json: $($_.Exception.Message)" -ForegroundColor Yellow
        }
    }

    $missingRequired = $false
    foreach ($file in $requiredFiles) {
        if (-not (Test-Path (Join-Path $filtersDir $file))) {
            $missingRequired = $true
            break
        }
    }

    if (-not $missingRequired -and $publishedAt -le $localUpdatedAt) {
        Write-Host "[FILTERS] Bundled filters are current ($($localUpdatedAt.ToString("yyyy-MM-dd")) >= $($publishedAt.ToString("yyyy-MM-dd")))" -ForegroundColor Green
        return
    }

    Write-Host "[FILTERS] Updating bundled filters to $($latest.tag_name) ($($publishedAt.ToString("yyyy-MM-dd")))" -ForegroundColor Yellow
    $tmpDir = Join-Path $env:TEMP ("dropo-filters-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tmpDir | Out-Null

    try {
        $releaseAssets = @{
            "refilter_domains.srs" = "ruleset-domain-refilter_domains.srs"
            "refilter_ips.srs"     = "ruleset-ip-refilter_ipsum.srs"
        }
        foreach ($target in $releaseAssets.Keys) {
            $assetName = $releaseAssets[$target]
            $asset = $latest.assets | Where-Object { $_.name -eq $assetName } | Select-Object -First 1
            if (-not $asset) {
                throw "Release asset not found: $assetName"
            }
            $tmpFile = Join-Path $tmpDir $target
            Download-File -Url $asset.browser_download_url -Destination $tmpFile
            if ((Get-Item $tmpFile).Length -lt 1024) {
                throw "Downloaded filter is unexpectedly small: $target"
            }
            Copy-Item $tmpFile (Join-Path $filtersDir $target) -Force
            Write-Host "[FILTERS] Updated $target" -ForegroundColor Green
        }

        $rawLists = @(
            @{ Name = "community_domains"; Url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community.lst"; Kind = "Domain"; Output = "community_domains.srs" },
            @{ Name = "community_ips"; Url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community_ips.lst"; Kind = "IP"; Output = "community_ips.srs" },
            @{ Name = "discord_ips"; Url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/discord_ips.lst"; Kind = "IP"; Output = "discord_ips.srs" }
        )
        $entryCounts = @{}
        foreach ($list in $rawLists) {
            $listPath = Join-Path $tmpDir ($list.Name + ".lst")
            $outPath = Join-Path $filtersDir $list.Output
            Download-File -Url $list.Url -Destination $listPath
            $entryCounts[$list.Name] = Compile-RuleSetFromList -ListPath $listPath -OutputPath $outPath -Kind $list.Kind
            Write-Host "[FILTERS] Compiled $($list.Output) ($($entryCounts[$list.Name]) entries)" -ForegroundColor Green
        }

        $versionPayload = [ordered]@{
            filters_version = [string]$latest.tag_name
            updated_at      = $publishedAt.ToString("o")
            source          = "https://github.com/1andrevich/Re-filter-lists"
            release_url     = [string]$latest.html_url
            max_age_days    = 30
            files           = [ordered]@{
                refilter_domains = [ordered]@{
                    file       = "refilter_domains.srs"
                    source_url = ($latest.assets | Where-Object { $_.name -eq "ruleset-domain-refilter_domains.srs" } | Select-Object -First 1).browser_download_url
                }
                refilter_ips = [ordered]@{
                    file       = "refilter_ips.srs"
                    source_url = ($latest.assets | Where-Object { $_.name -eq "ruleset-ip-refilter_ipsum.srs" } | Select-Object -First 1).browser_download_url
                }
                community_domains = [ordered]@{
                    file       = "community_domains.srs"
                    source_url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community.lst"
                    entries    = $entryCounts["community_domains"]
                }
                community_ips = [ordered]@{
                    file       = "community_ips.srs"
                    source_url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/community_ips.lst"
                    entries    = $entryCounts["community_ips"]
                }
                discord_ips = [ordered]@{
                    file       = "discord_ips.srs"
                    source_url = "https://raw.githubusercontent.com/1andrevich/Re-filter-lists/main/discord_ips.lst"
                    entries    = $entryCounts["discord_ips"]
                }
            }
        }
        $versionJson = $versionPayload | ConvertTo-Json -Depth 20
        [System.IO.File]::WriteAllText($versionFile, $versionJson, (New-Object System.Text.UTF8Encoding($false)))
        Write-Host "[FILTERS] version.json updated" -ForegroundColor Green
    } finally {
        Remove-Item $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

$VersionInfo = Get-VersionInfo
$AppVersion = if ($Version) { $Version } else { $VersionInfo.version }
$SingBoxVersion = $VersionInfo.singbox.version
$WireGuardVersion = $VersionInfo.wireguard.version
$ByeDPIVersion = $VersionInfo.byedpi.version
$ZapretVersion = $VersionInfo.zapret.version
$ZapretArchiveSHA256 = ([string]$VersionInfo.zapret.archiveSha256).ToLowerInvariant()
$XrayVersion = $VersionInfo.xray.version
$TgWsProxyVersion = $VersionInfo.tgwsproxy.version
$BuildTime = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
$BuildHash = Get-BuildHash

# Build folder and archive names include the app name, version and hash for unique test environments.
$ArtifactBaseName = "dropo-$AppVersion-$BuildHash"
$BuildFolderName = $ArtifactBaseName

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "   dropo Build System" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Version:   $AppVersion" -ForegroundColor White
Write-Host "Build:     $BuildHash" -ForegroundColor Gray
Write-Host "sing-box:  $SingBoxVersion" -ForegroundColor White
Write-Host "WireGuard: $WireGuardVersion" -ForegroundColor White
Write-Host "ByeDPI:    $ByeDPIVersion" -ForegroundColor White
Write-Host "zapret2:   $ZapretVersion" -ForegroundColor White
Write-Host "Xray:      $XrayVersion" -ForegroundColor White
Write-Host ""

# Paths
$AppDir = Join-Path $ScriptRoot "app"
$ReleaseDir = Join-Path $ScriptRoot "release"
$VersionDir = Join-Path $ReleaseDir $BuildFolderName
$DepsDir = Join-Path $ScriptRoot "dependencies"
$SingBoxDir = Join-Path $DepsDir "sing-box-v$SingBoxVersion"
$SingBoxExe = Join-Path $SingBoxDir "windows-amd64\sing-box-$SingBoxVersion-windows-amd64\sing-box.exe"
$XrayDir = Join-Path $DepsDir "xray-v$XrayVersion"
$XrayExe = Join-Path $XrayDir "xray.exe"
$ZapretDir = Join-Path $DepsDir "zapret2-v$ZapretVersion"
$ZapretRoot = Join-Path $ZapretDir "zapret2-v$ZapretVersion"
$ZapretWinDir = Join-Path $ZapretRoot "binaries\windows-x86_64"
$ReleasePlatform = "windows"
$ReleaseArch = "x64"
$RequiredDepFiles = @("sing-box.exe", "winws2.exe", "WinDivert.dll", "zapret-lib.lua", "zapret-antidpi.lua")
$ForbiddenDepFiles = @("winws.exe")
$WindowsPortableAsset = "dropo-Windows-Portable-x64.zip"
$WindowsDepsAsset = "dropo-Windows-Dependencies-x64.zip"
$AndroidReleaseArch = "arm64"
$AndroidFlutterTargetPlatform = "android-arm64"
$AndroidGoMobileTarget = "android/arm64"
$AndroidBridgeTags = "with_gvisor,with_quic,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0"
$AndroidAppAsset = "dropo-Android-$AndroidReleaseArch.apk"

function Ensure-SingBoxWindowsDependency {
    if (Test-Path $SingBoxExe) {
        return
    }

    Write-Host "[SING-BOX] Downloading sing-box v$SingBoxVersion for Windows..." -ForegroundColor Yellow
    $archiveUrl = "https://github.com/SagerNet/sing-box/releases/download/v$SingBoxVersion/sing-box-$SingBoxVersion-windows-amd64.zip"
    $tmpDir = Join-Path $env:TEMP ("dropo-sing-box-" + [Guid]::NewGuid().ToString("N"))
    $zipPath = Join-Path $tmpDir "sing-box.zip"
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        Download-File -Url $archiveUrl -Destination $zipPath
        if (-not (Test-Path $SingBoxDir)) {
            New-Item -ItemType Directory -Path $SingBoxDir | Out-Null
        }
        $targetRoot = Join-Path $SingBoxDir "windows-amd64"
        if (Test-Path $targetRoot) {
            Remove-Item -LiteralPath $targetRoot -Recurse -Force
        }
        New-Item -ItemType Directory -Path $targetRoot | Out-Null
        Expand-Archive -Path $zipPath -DestinationPath $targetRoot -Force
        if (-not (Test-Path $SingBoxExe)) {
            throw "Downloaded archive did not contain expected file: $SingBoxExe"
        }
        Write-Host "[OK] Downloaded sing-box.exe v$SingBoxVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Ensure-Zapret2WindowsDependency {
    $expected = Join-Path $ZapretWinDir "winws2.exe"
    if (Test-Path $expected) {
        return
    }
    if ($ZapretArchiveSHA256 -notmatch '^[0-9a-f]{64}$') {
        throw "version.json must pin zapret.archiveSha256 for zapret2 v$ZapretVersion."
    }

    Write-Host "[ZAPRET2] Downloading zapret2 v$ZapretVersion for Windows..." -ForegroundColor Yellow
    $archiveUrl = "https://github.com/bol-van/zapret2/releases/download/v$ZapretVersion/zapret2-v$ZapretVersion.zip"
    $tmpDir = Join-Path $env:TEMP ("dropo-zapret2-" + [Guid]::NewGuid().ToString("N"))
    $zipPath = Join-Path $tmpDir "zapret2.zip"
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        Download-File -Url $archiveUrl -Destination $zipPath
        $actualHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $ZapretArchiveSHA256) {
            throw "zapret2 archive hash mismatch: expected $ZapretArchiveSHA256, got $actualHash"
        }
        if (Test-Path $ZapretDir) {
            Remove-Item -LiteralPath $ZapretDir -Recurse -Force
        }
        New-Item -ItemType Directory -Path $ZapretDir | Out-Null
        Expand-Archive -Path $zipPath -DestinationPath $ZapretDir -Force
        if (-not (Test-Path $expected)) {
            throw "Downloaded archive did not contain expected file: $expected"
        }
        Write-Host "[OK] Downloaded and verified zapret2 v$ZapretVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

# Clean build
if ($Clean) {
    Write-Host "Cleaning..." -ForegroundColor Yellow
    if (Test-Path $VersionDir) {
        Remove-Item -Recurse -Force $VersionDir
    }
    Write-Host "[OK] Cleaned release/$BuildFolderName" -ForegroundColor Green
    if (-not $Build -and -not $All) {
        exit 0
    }
}

function New-AppOnlyArchive {
    param(
        [string]$SourceAppFolder,
        [string]$DestinationZip
    )

    $stageDir = Join-Path $env:TEMP ("dropo-appzip-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $stageDir | Out-Null
    try {
        Copy-Item -Path (Join-Path $SourceAppFolder "*") -Destination $stageDir -Recurse -Force
        foreach ($relativeBin in @("resources\bin", "resources\app\bin")) {
            $nestedBin = Join-Path $stageDir $relativeBin
            if (Test-Path $nestedBin) {
                Remove-Item -LiteralPath $nestedBin -Recurse -Force
            }
        }
        if (Test-Path $DestinationZip) { Remove-Item $DestinationZip -Force }
        Compress-Archive -Path (Get-ChildItem -Path $stageDir).FullName -DestinationPath $DestinationZip -CompressionLevel Optimal
    } finally {
        Remove-Item -LiteralPath $stageDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Get-SignToolCommand {
    $fromPath = Get-Command signtool.exe -ErrorAction SilentlyContinue
    if ($fromPath) {
        return $fromPath.Source
    }

    $kitsRoot = Join-Path ${env:ProgramFiles(x86)} "Windows Kits\10\bin"
    if (Test-Path $kitsRoot) {
        return Get-ChildItem -Path $kitsRoot -Filter signtool.exe -Recurse -File -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match '\\x64\\signtool\.exe$' } |
            Sort-Object FullName -Descending |
            Select-Object -First 1 -ExpandProperty FullName
    }
    return $null
}

function Invoke-WindowsCodeSigning {
    param([string[]]$Paths)

    $pfxPath = [string]$env:DROPO_WINDOWS_PFX_PATH
    $pfxPassword = [string]$env:DROPO_WINDOWS_PFX_PASSWORD
    $certificateSha1 = ([string]$env:DROPO_WINDOWS_CERT_SHA1) -replace '\s', ''
    if ($AllowUntrustedSelfSignedWindows -and [string]::IsNullOrWhiteSpace($certificateSha1)) {
        $bundledCertificatePath = Join-Path $CertificateSourceDir "dropo-pet-code-signing.cer"
        if (Test-Path -LiteralPath $bundledCertificatePath -PathType Leaf) {
            $bundledCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($bundledCertificatePath)
            $certificateSha1 = $bundledCertificate.Thumbprint
        }
    }
    $hasPfx = -not [string]::IsNullOrWhiteSpace($pfxPath)
    $hasCertificateSha1 = -not [string]::IsNullOrWhiteSpace($certificateSha1)

    if (-not $hasPfx -and -not $hasCertificateSha1) {
        if ($AllowUnsignedWindows) {
            Write-Host "[WARNING] Windows binaries are unsigned (-AllowUnsignedWindows). Do not publish this build." -ForegroundColor Yellow
            return
        }
        throw "Windows release signing is required. Set DROPO_WINDOWS_CERT_SHA1, or DROPO_WINDOWS_PFX_PATH and DROPO_WINDOWS_PFX_PASSWORD. Use -AllowUnsignedWindows only for local development."
    }
    if ($hasPfx -and (-not (Test-Path -LiteralPath $pfxPath -PathType Leaf))) {
        throw "Windows signing certificate not found: $pfxPath"
    }
    if ($hasPfx -and [string]::IsNullOrWhiteSpace($pfxPassword)) {
        throw "DROPO_WINDOWS_PFX_PASSWORD is required with DROPO_WINDOWS_PFX_PATH."
    }
    if ($AllowUntrustedSelfSignedWindows -and -not $hasCertificateSha1) {
        throw "-AllowUntrustedSelfSignedWindows requires an exact certificate thumbprint or dropo-pet-code-signing.cer."
    }

    $signTool = Get-SignToolCommand
    if (-not $signTool) {
        throw "signtool.exe was not found. Install the Windows SDK signing tools."
    }
    $timestampUrl = [string]$env:DROPO_WINDOWS_TIMESTAMP_URL
    if ([string]::IsNullOrWhiteSpace($timestampUrl)) {
        $timestampUrl = "http://timestamp.digicert.com"
    }

    foreach ($path in $Paths) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            throw "Windows binary to sign was not found: $path"
        }
        $arguments = @("sign", "/fd", "SHA256")
        if (-not $SkipWindowsTimestamp) {
            $arguments += @("/td", "SHA256", "/tr", $timestampUrl)
        }
        if ($hasPfx) {
            $arguments += @("/f", $pfxPath, "/p", $pfxPassword)
        } else {
            $arguments += @("/sha1", $certificateSha1)
        }
        $arguments += $path
        & $signTool @arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Authenticode signing failed for $path (exit code $LASTEXITCODE)."
        }
        & $signTool verify /pa /all $path | Out-Null
        if ($LASTEXITCODE -ne 0) {
            if (-not $AllowUntrustedSelfSignedWindows) {
                throw "Authenticode verification failed for $path (exit code $LASTEXITCODE)."
            }
            $signature = Get-AuthenticodeSignature -FilePath $path
            $actualThumbprint = ([string]$signature.SignerCertificate.Thumbprint) -replace '\s', ''
            $chain = [Security.Cryptography.X509Certificates.X509Chain]::new()
            try {
                $chain.ChainPolicy.RevocationMode = [Security.Cryptography.X509Certificates.X509RevocationMode]::NoCheck
                $null = $chain.Build($signature.SignerCertificate)
                $chainStatuses = @($chain.ChainStatus | ForEach-Object Status)
                $untrustedRoot = $signature.Status -eq [System.Management.Automation.SignatureStatus]::UnknownError -and
                    $chainStatuses.Count -gt 0 -and
                    @($chainStatuses | Where-Object { $_ -ne [Security.Cryptography.X509Certificates.X509ChainStatusFlags]::UntrustedRoot }).Count -eq 0
            } finally {
                $chain.Dispose()
            }
            if (-not $untrustedRoot -or -not $actualThumbprint.Equals($certificateSha1, [StringComparison]::OrdinalIgnoreCase)) {
                throw "Self-signed Authenticode verification failed for $path (status: $($signature.Status), thumbprint: $actualThumbprint)."
            }
            Write-Host "[WARNING] Signature integrity and thumbprint verified; root is not trusted until the bundled certificate is installed." -ForegroundColor Yellow
        }
        Write-Host "[OK] Signed $([IO.Path]::GetFileName($path))" -ForegroundColor Green
    }
}

function Copy-PetCertificateBundle {
    param([string]$Destination)

    $certificateDestination = Join-Path $Destination "resources\cert"
    New-Item -ItemType Directory -Path $certificateDestination -Force | Out-Null
    foreach ($name in @(
        "dropo-pet-code-signing.cer",
        "install-dropo-pet-certificate.cmd",
        "install-dropo-pet-certificate.ps1",
        "remove-dropo-pet-certificate.cmd",
        "remove-dropo-pet-certificate.ps1"
    )) {
        $source = Join-Path $CertificateSourceDir $name
        if (Test-Path -LiteralPath $source -PathType Leaf) {
            Copy-Item -LiteralPath $source -Destination (Join-Path $certificateDestination $name) -Force
        }
    }
}

# Build application
function Build-Application {
    Write-Host ""
    Write-Host "Building application..." -ForegroundColor Yellow

    $FlutterCmd = Get-FlutterCommand
    if (-not $FlutterCmd) {
        Write-Host "[ERROR] Flutter SDK not found. Install Flutter or use E:\flutter-sdk\flutter\bin\flutter.bat" -ForegroundColor Red
        exit 1
    }
    $FlutterDir = Join-Path $ScriptRoot "flutter_app"
    if (-not (Test-Path $FlutterDir)) {
        Write-Host "[ERROR] flutter_app/ not found." -ForegroundColor Red
        exit 1
    }

    # Update bundled routing filters at build time only. Runtime builds never
    # download route databases on client machines. (Skipped in -AppOnly: filters
    # are part of bin/, shipped in the dependencies archive.)
    if (-not $AppOnly) {
        Ensure-SingBoxWindowsDependency
        Ensure-Zapret2WindowsDependency
        Update-BundledFilters
    }

    # Keep release/ clean: every new build removes ALL previous build containers
    # (any version/hash) and any stray archives, so only the current build remains.
    if (Test-Path $ReleaseDir) {
        $oldBuilds = Get-ChildItem -Path $ReleaseDir -Directory | Where-Object { $_.Name -match "^dropo-.+-[0-9a-f]+$" }
        foreach ($oldBuild in $oldBuilds) {
            Write-Host "[CLEAN] Removing old build: $($oldBuild.Name)" -ForegroundColor Yellow
            try {
                Remove-Item -Path $oldBuild.FullName -Recurse -Force
            } catch {
                Write-Host "[WARNING] Could not remove old build $($oldBuild.Name): $($_.Exception.Message)" -ForegroundColor Yellow
            }
        }
        # Remove any stray archives left directly in release/ root.
        $oldZips = Get-ChildItem -Path $ReleaseDir -File -Filter "*.zip"
        foreach ($oldZip in $oldZips) {
            Write-Host "[CLEAN] Removing old ZIP: $($oldZip.Name)" -ForegroundColor Yellow
            Remove-Item -Path $oldZip.FullName -Force
        }
    }

    # Create the release container (identified by version+hash) and the runnable
    # app subfolder inside it (named plain "dropo", no version/hash). Split
    # release archives are written next to it, inside the container.
    if (-not (Test-Path $VersionDir)) {
        New-Item -ItemType Directory -Path $VersionDir | Out-Null
    }
    $AppFolder = Join-Path $VersionDir "dropo"
    if (-not (Test-Path $AppFolder)) {
        New-Item -ItemType Directory -Path $AppFolder | Out-Null
    }

    # Root contains only the launcher and resources/. The actual Flutter UI,
    # Go core, manifests and runtime files live directly under resources/.
    $RuntimeFolder = Join-Path $AppFolder "resources"
    if (-not (Test-Path $RuntimeFolder)) {
        New-Item -ItemType Directory -Path $RuntimeFolder | Out-Null
    }
    $resourcesDir = Join-Path $RuntimeFolder "resources"
    if (-not (Test-Path $resourcesDir)) {
        New-Item -ItemType Directory -Path $resourcesDir | Out-Null
    }

    # Build Go core with ldflags (include build hash for dev identification).
    # Split releases also bind the signed core to the exact dependency archive;
    # the adjacent JSON manifest can no longer redirect elevated execution.
    $ldflags = "-X 'main.Version=$AppVersion' -X 'main.BuildTime=$BuildTime' -X 'main.BuildHash=$BuildHash' -X 'main.SingBoxVersion=$SingBoxVersion' -X 'main.WireGuardVersion=$WireGuardVersion' -s -w -H=windowsgui"
    # Every Windows core, including a full local bundle, uses the hosted lock as
    # its authority and executes only from the protected ProgramData runtime.
    # A bundled user-writable bin/ may exist for diagnostics, but is never the
    # elevated execution source.
    if ($true) {
        $trustedLockPath = Join-Path $ScriptRoot "deps-lock.json"
        if (-not (Test-Path -LiteralPath $trustedLockPath -PathType Leaf)) {
            throw "deps-lock.json is required for an AppOnly build."
        }
        $trustedLock = Get-Content -LiteralPath $trustedLockPath -Raw | ConvertFrom-Json
        $trustedVersion = [string]$trustedLock.depsVersion
        $trustedAsset = [string]$trustedLock.asset
        $trustedSHA256 = ([string]$trustedLock.sha256).ToLowerInvariant()
        $trustedSize = [long]$trustedLock.size
        $trustedTag = [string]$trustedLock.tag
        $trustedRequired = @($trustedLock.requiredFiles) -join ','
        if ($trustedVersion -notmatch '^[0-9a-f]{12}$' -or
            $trustedAsset -notmatch '^dropo-Windows-Dependencies-x64\.zip$' -or
            $trustedSHA256 -notmatch '^[0-9a-f]{64}$' -or $trustedSize -le 0 -or
            $trustedTag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$' -or
            [string]::IsNullOrWhiteSpace($trustedRequired)) {
            throw "deps-lock.json contains invalid or incomplete trusted dependency identity."
        }
        $ldflags = "-X 'main.trustedDepsVersion=$trustedVersion' -X 'main.trustedDepsAsset=$trustedAsset' -X 'main.trustedDepsSHA256=$trustedSHA256' -X 'main.trustedDepsSize=$trustedSize' -X 'main.trustedDepsRequired=$trustedRequired' $ldflags"
    }

    Push-Location $AppDir
    try {
        Write-Host "Building dropo-core.exe $AppVersion (hash: $BuildHash)..." -ForegroundColor Gray
        & go build -ldflags $ldflags -o (Join-Path $RuntimeFolder "dropo-core.exe") .

        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] Go core build failed!" -ForegroundColor Red
            exit 1
        }
        Write-Host "[OK] Built dropo-core.exe" -ForegroundColor Green
    } finally {
        Pop-Location
    }

    Push-Location $FlutterDir
    try {
        if ($ReuseFlutterWindowsOutput) {
            Write-Host "Reusing existing Flutter Windows Release output..." -ForegroundColor Yellow
        } else {
            Write-Host "Building Flutter Windows UI..." -ForegroundColor Gray
            & $FlutterCmd build windows --release --build-name $AppVersion --build-number 1 --dart-define "DROPO_CORE_ENDPOINT=http://127.0.0.1:17890"
            if ($LASTEXITCODE -ne 0) {
                Write-Host "[ERROR] Flutter Windows build failed." -ForegroundColor Red
                exit 1
            }
        }
    } finally {
        Pop-Location
    }
    $FlutterOutput = Join-Path $FlutterDir "build\windows\x64\runner\Release"
    if (-not (Test-Path $FlutterOutput)) {
        Write-Host "[ERROR] Flutter output not found: $FlutterOutput" -ForegroundColor Red
        exit 1
    }
    Copy-Item -Path (Join-Path $FlutterOutput "*") -Destination $RuntimeFolder -Recurse -Force
    $uiExe = Join-Path $RuntimeFolder "dropo.exe"
    if (-not (Test-Path $uiExe)) {
        Write-Host "[ERROR] dropo.exe not found after Flutter build!" -ForegroundColor Red
        exit 1
    }
    Rename-Item -LiteralPath $uiExe -NewName "dropo-ui.exe" -Force
    Write-Host "[OK] Built Flutter dropo-ui.exe" -ForegroundColor Green

    $coreExe = Join-Path $RuntimeFolder "dropo-core.exe"
    $uiExe = Join-Path $RuntimeFolder "dropo-ui.exe"
    # Sign first: Authenticode changes PE bytes, so the launcher must pin the
    # final on-disk hashes that it will verify immediately before execution.
    Invoke-WindowsCodeSigning -Paths @($coreExe, $uiExe)
    $coreSHA256 = (Get-FileHash -LiteralPath $coreExe -Algorithm SHA256).Hash.ToLowerInvariant()
    $uiSHA256 = (Get-FileHash -LiteralPath $uiExe -Algorithm SHA256).Hash.ToLowerInvariant()

    Push-Location (Join-Path $ScriptRoot "launcher")
    try {
        Write-Host "Building dropo launcher..." -ForegroundColor Gray
        $launcherLdflags = "-X 'main.expectedCoreSHA256=$coreSHA256' -X 'main.expectedUISHA256=$uiSHA256' -s -w -H=windowsgui"
        & go build -ldflags $launcherLdflags -o (Join-Path $AppFolder "dropo.exe") .
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] dropo launcher build failed!" -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }
    Write-Host "[OK] Built launcher dropo.exe" -ForegroundColor Green

    Invoke-WindowsCodeSigning -Paths @((Join-Path $AppFolder "dropo.exe"))
    if ($AllowUntrustedSelfSignedWindows) {
        Copy-PetCertificateBundle -Destination $AppFolder
    }

    # ---- AppOnly (CI): package app without bundled bin/, deps from deps-lock ----
    if ($AppOnly) {
        Write-Host "[AppOnly] CI build - skipping bundled bin/; deps referenced via deps-lock.json" -ForegroundColor Yellow
        $templateSrc = Join-Path $AppDir "config\template.json"
        if (Test-Path $templateSrc) { Copy-Item $templateSrc $resourcesDir -Force }
        $repair = Join-Path $ScriptRoot "tools\repair-browser-proxy.ps1"
        if (Test-Path $repair) { Copy-Item $repair (Join-Path $RuntimeFolder "repair-browser-proxy.ps1") -Force }

        $lockPath = Join-Path $ScriptRoot "deps-lock.json"
        if (-not (Test-Path $lockPath)) {
            Write-Host "[ERROR] deps-lock.json not found. Run a full local build once to generate it (it records the dependencies archive depsVersion/sha256/size)." -ForegroundColor Red
            exit 1
        }
        $lock = Get-Content $lockPath -Raw | ConvertFrom-Json
        $lockTag = [string]$lock.tag
        if (-not $lockTag) { $lockTag = "v$AppVersion" }
        $depsUrl = "https://github.com/Droponevedimka/dropo/releases/download/$lockTag/$([string]$lock.asset)"
        if (-not $SkipHostedDependenciesCheck -and -not (Test-ReleaseAssetAvailable -Url $depsUrl -ExpectedSize ([long]$lock.size))) {
            Write-Host "[ERROR] deps-lock.json points to a missing dependencies asset:" -ForegroundColor Red
            Write-Host "        $depsUrl" -ForegroundColor Red
            Write-Host "        Run a full build with -ForceDepsRelease and publish that dependencies asset." -ForegroundColor Red
            exit 1
        }
        if ($SkipHostedDependenciesCheck) {
            Write-Host "[WARNING] Skipped hosted dependencies network check for offline packaging." -ForegroundColor Yellow
        }
        $manifest = [ordered]@{
            depsVersion = [string]$lock.depsVersion
            platform    = if ($lock.platform) { [string]$lock.platform } else { $ReleasePlatform }
            arch        = if ($lock.arch) { [string]$lock.arch } else { $ReleaseArch }
            asset       = [string]$lock.asset
            sha256      = [string]$lock.sha256
            size        = [long]$lock.size
            appVersion  = $AppVersion
            repo        = "Droponevedimka/dropo"
            requiredFiles = if ($lock.requiredFiles) { @($lock.requiredFiles) } else { $RequiredDepFiles }
        }
        [System.IO.File]::WriteAllText((Join-Path $RuntimeFolder "dependencies.json"), ($manifest | ConvertTo-Json), (New-Object System.Text.UTF8Encoding($false)))

        $AppAsset = $WindowsPortableAsset
        $zipFile = Join-Path $VersionDir $AppAsset
        New-AppOnlyArchive -SourceAppFolder $AppFolder -DestinationZip $zipFile
        $zipMB = [math]::Round((Get-Item $zipFile).Length / 1MB, 2)
        Write-Host "[OK] Created $AppAsset ($zipMB MB, AppOnly; deps=$($lock.asset))" -ForegroundColor Green
        Write-Host "[SUCCESS] App-only build: release/$BuildFolderName/" -ForegroundColor Green
        return
    }

    # Create bin directory for sing-box
    $binDir = Join-Path $RuntimeFolder "bin"
    if (-not (Test-Path $binDir)) {
        New-Item -ItemType Directory -Path $binDir | Out-Null
    }

    # Copy sing-box.exe to bin/ folder
    $singBoxDest = Join-Path $binDir "sing-box.exe"
    if (Test-Path $SingBoxExe) {
        Copy-Item $SingBoxExe $singBoxDest -Force
        Write-Host "[OK] Copied bin/sing-box.exe (v$SingBoxVersion)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] sing-box.exe not found at: $SingBoxExe" -ForegroundColor Yellow
        Write-Host "          Run download-singbox.ps1 to download it" -ForegroundColor Yellow
    }

    # Copy WireGuard dependencies to bin/ folder
    $WireGuardDir = Join-Path $DepsDir "wireguard-windows-v$WireGuardVersion"
    $WireGuardFiles = @("wireguard.exe", "wg.exe", "wintun.dll")

    foreach ($file in $WireGuardFiles) {
        $src = Join-Path $WireGuardDir $file
        $dst = Join-Path $binDir $file
        if (Test-Path $src) {
            Copy-Item $src $dst -Force
            Write-Host "[OK] Copied bin/$file" -ForegroundColor Green
        } else {
            Write-Host "[WARNING] $file not found at: $src" -ForegroundColor Yellow
        }
    }

    # Copy ByeDPI (free access bypass without VPN key) to bin/ folder
    $ByeDPIDir = Join-Path $DepsDir "byedpi-v$ByeDPIVersion"
    $ByeDPIExe = Join-Path $ByeDPIDir "ciadpi.exe"
    $byeDpiDst = Join-Path $binDir "ciadpi.exe"
    if (Test-Path $ByeDPIExe) {
        Copy-Item $ByeDPIExe $byeDpiDst -Force
        Write-Host "[OK] Copied bin/ciadpi.exe (ByeDPI v$ByeDPIVersion)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] ciadpi.exe not found at: $ByeDPIExe" -ForegroundColor Yellow
    }

    # Copy the zapret2/winws2 transparent DPI-bypass runtime to bin/.
    $ZapretFiles = @("winws2.exe", "cygwin1.dll", "WinDivert.dll", "WinDivert64.sys")
    foreach ($file in $ZapretFiles) {
        $src = Join-Path $ZapretWinDir $file
        $dst = Join-Path $binDir $file
        if (Test-Path $src) {
            Copy-Item $src $dst -Force
            Write-Host "[OK] Copied bin/$file (zapret2 v$ZapretVersion)" -ForegroundColor Green
        } else {
            Write-Host "[WARNING] zapret file not found at: $src" -ForegroundColor Yellow
        }
    }
    $ZapretFakeDir = Join-Path $ZapretRoot "files\fake"
    foreach ($fakeFile in @("tls_clienthello_www_google_com.bin", "quic_initial_www_google_com.bin", "quic_initial_facebook_com.bin")) {
        $src = Join-Path $ZapretFakeDir $fakeFile
        if (-not (Test-Path $src -PathType Leaf)) {
            throw "Required zapret2 fake payload not found: $src"
        }
        Copy-Item $src (Join-Path $binDir $fakeFile) -Force
        Write-Host "[OK] Copied bin/$fakeFile (zapret2 blob)" -ForegroundColor Green
    }
    foreach ($luaFile in @("zapret-lib.lua", "zapret-antidpi.lua")) {
        $src = Join-Path (Join-Path $ZapretRoot "lua") $luaFile
        if (-not (Test-Path $src -PathType Leaf)) {
            throw "Required zapret2 Lua module not found: $src"
        }
        Copy-Item $src (Join-Path $binDir $luaFile) -Force
        Write-Host "[OK] Copied bin/$luaFile" -ForegroundColor Green
    }
    $ZapretWinDivertFilterDir = Join-Path $ZapretRoot "init.d\windivert.filter.examples"
    $ZapretWinDivertFilterFiles = @(
        "windivert_part.quic_initial_ietf.txt",
        "windivert_part.discord_media.txt",
        "windivert_part.stun.txt"
    )
    foreach ($filterFile in $ZapretWinDivertFilterFiles) {
        $src = Join-Path $ZapretWinDivertFilterDir $filterFile
        if (Test-Path $src) {
            Copy-Item $src (Join-Path $binDir $filterFile) -Force
            Write-Host "[OK] Copied bin/$filterFile (zapret WinDivert filter)" -ForegroundColor Green
        } else {
            Write-Host "[WARNING] zapret WinDivert filter not found at: $src" -ForegroundColor Yellow
        }
    }

    # Copy Xray-core for VLESS xhttp bridge outbounds
    $xrayDst = Join-Path $binDir "xray.exe"
    if (Test-Path $XrayExe) {
        Copy-Item $XrayExe $xrayDst -Force
        Write-Host "[OK] Copied bin/xray.exe (Xray-core v$XrayVersion)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] xray.exe not found at: $XrayExe" -ForegroundColor Yellow
    }

    # Bundle the locally cached tg-ws-proxy dependency (local
    # MTProto-over-WebSocket proxy for Telegram). The upstream repository is no
    # longer publicly available, so builds must not depend on its URLs.
    $TgWsProxyDir = Join-Path $DepsDir "tg-ws-proxy-v$TgWsProxyVersion"
    $TgWsProxyHeadlessSrc = Join-Path $TgWsProxyDir "TgWsProxy_headless_windows.exe"
    $TgWsProxyTraySrc = Join-Path $TgWsProxyDir "TgWsProxy_windows.exe"
    $tgWsProxyDst = Join-Path $binDir "tg-ws-proxy.exe"
    $TgWsProxySrc = $null
    $TgWsProxyMode = "headless"
    if (Test-Path $TgWsProxyHeadlessSrc) {
        $TgWsProxySrc = $TgWsProxyHeadlessSrc
    } elseif (Test-Path $TgWsProxyTraySrc) {
        $TgWsProxySrc = $TgWsProxyTraySrc
        $TgWsProxyMode = "tray fallback"
        Write-Host "[WARNING] Headless tg-ws-proxy not found; bundling tray fallback" -ForegroundColor Yellow
    }
    if ($TgWsProxySrc -and (Test-Path $TgWsProxySrc)) {
        Copy-Item $TgWsProxySrc $tgWsProxyDst -Force
        Write-Host "[OK] Copied bin/tg-ws-proxy.exe (tg-ws-proxy v$TgWsProxyVersion, $TgWsProxyMode)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] tg-ws-proxy.exe not bundled; Telegram MTProto proxy will be unavailable" -ForegroundColor Yellow
    }
    # tg-ws-proxy is MIT licensed; ship the locally cached license notice.
    $TgWsProxyLicense = Join-Path $TgWsProxyDir "LICENSE"
    if (Test-Path $TgWsProxyLicense -PathType Leaf) {
        Copy-LicenseFile $TgWsProxyLicense (Join-Path $RuntimeFolder "licenses") "tg-ws-proxy-LICENSE.txt"
    } else {
        Write-Host "[WARNING] Local tg-ws-proxy LICENSE not found at: $TgWsProxyLicense" -ForegroundColor Yellow
    }

    # Copy third-party license notices required by bundled sidecar binaries.
    $licensesDir = Join-Path $RuntimeFolder "licenses"
    Copy-LicenseFile (Join-Path $SingBoxDir "windows-amd64\sing-box-$SingBoxVersion-windows-amd64\LICENSE") $licensesDir "sing-box-LICENSE.txt"
    Copy-LicenseFile (Join-Path $XrayDir "LICENSE") $licensesDir "xray-LICENSE.txt"
    Copy-LicenseFile (Join-Path $WireGuardDir "LICENSE") $licensesDir "wireguard-windows-LICENSE.txt"
    Copy-LicenseFile (Join-Path $ByeDPIDir "LICENSE") $licensesDir "byedpi-LICENSE.txt"
    Copy-LicenseFile (Join-Path $ZapretRoot "docs\LICENSE.txt") $licensesDir "zapret2-LICENSE.txt"
    # Copy template.json
    $templateSrc = Join-Path $AppDir "config\template.json"
    if (Test-Path $templateSrc) {
        Copy-Item $templateSrc $resourcesDir -Force
        Write-Host "[OK] Copied template.json" -ForegroundColor Green
    }

    # Copy filters (rule-sets for routing)
    $filtersDir = Join-Path $DepsDir "filters"
    $filtersDest = Join-Path $binDir "filters"
    if (Test-Path $filtersDir) {
        if (-not (Test-Path $filtersDest)) {
            New-Item -ItemType Directory -Path $filtersDest | Out-Null
        }
        # Copy all .srs files and version.json
        Get-ChildItem -Path $filtersDir -Filter "*.srs" | ForEach-Object {
            Copy-Item $_.FullName $filtersDest -Force
        }
        $filtersVersion = Join-Path $filtersDir "version.json"
        if (Test-Path $filtersVersion) {
            Copy-Item $filtersVersion $filtersDest -Force
        }
        $filterCount = (Get-ChildItem -Path $filtersDest -Filter "*.srs").Count
        Write-Host "[OK] Copied bin/filters/ ($filterCount rule-sets)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] Filters not found at: $filtersDir" -ForegroundColor Yellow
    }

    # Copy support scripts for client-side diagnostics and recovery.
    $supportScripts = @(
        "repair-browser-proxy.ps1"
    )
    foreach ($scriptName in $supportScripts) {
        $scriptPath = Join-Path $ScriptRoot "tools\$scriptName"
        if (Test-Path $scriptPath -PathType Leaf) {
            Copy-Item $scriptPath (Join-Path $RuntimeFolder $scriptName) -Force
            Write-Host "[OK] Copied $scriptName" -ForegroundColor Green
        }
    }

    # Keep release artifacts clean even if a local smoke test previously wrote
    # runtime state into the resources folder.
    $runtimeResourceEntries = @(
        "active_config.json",
        "settings.json",
        "cache.db",
        "xray_config.json",
        "tg-ws-proxy.json",
        "service_strategy_cache.json",
        "service-hostlists"
    )
    foreach ($entry in $runtimeResourceEntries) {
        $runtimePath = Join-Path $resourcesDir $entry
        if (Test-Path $runtimePath) {
            Remove-Item -LiteralPath $runtimePath -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    # ---- Split packaging: app archive (no bin) + dependencies archive ----
    # depsVersion is a deterministic short hash of the bundled tool versions, so
    # it changes only when an actual bundled binary changes.
    $depsKey = @(
        [string]$VersionInfo.singbox.version,
        [string]$VersionInfo.wireguard.version,
        [string]$VersionInfo.wireguard.wintunVersion,
        [string]$VersionInfo.byedpi.version,
        [string]$VersionInfo.zapret.version,
        [string]$VersionInfo.tgwsproxy.version,
        [string]$VersionInfo.xray.version
    ) -join '|'
    $depsSha256 = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes($depsKey))
    $DepsVersion = ([System.BitConverter]::ToString($depsSha256).Replace('-', '').ToLower()).Substring(0, 12)

    # Marker so the app (and the extracted deps) know which depsVersion is installed.
    Set-Content -Path (Join-Path $binDir ".deps-version") -Value $DepsVersion -NoNewline -Encoding ascii

    # Platform-tagged dependencies archive = the whole bin/ (heavy, rarely
    # changes). The filename carries NO hash — the archive is referenced by the
    # release TAG that hosts it (deps update => new tag => new url). depsVersion
    # stays as the internal change-detection key + extracted marker. The platform
    # tag keeps room for future dropo-<platform>-* assets (Android/macOS/Linux).
    $DepsAsset = $WindowsDepsAsset
    $depsArchiveForUpload = $false
    # Archives live INSIDE the version container (release/dropo-<v>-<hash>/), not
    # in release/ root. Names carry no version/hash (the container already does).
    $depsZip = Join-Path $VersionDir $DepsAsset
    if (Test-Path $depsZip) { Remove-Item $depsZip -Force }
    foreach ($requiredFile in $RequiredDepFiles) {
        if (-not (Test-Path (Join-Path $binDir $requiredFile) -PathType Leaf)) {
            throw "Dependency staging is missing required zapret2 runtime file: $requiredFile"
        }
    }
    foreach ($forbiddenFile in $ForbiddenDepFiles) {
        if (Test-Path (Join-Path $binDir $forbiddenFile)) {
            throw "Obsolete zapret1 runtime must not be packaged: $forbiddenFile"
        }
    }
    Compress-Archive -Path "$binDir\*" -DestinationPath $depsZip -CompressionLevel Optimal
    $depsZipSize = (Get-Item $depsZip).Length
    $depsZipSha = (Get-FileHash -Algorithm SHA256 $depsZip).Hash.ToLower()
    Write-Host "[OK] Created $DepsAsset ($([math]::Round($depsZipSize / 1MB, 2)) MB, depsVersion=$DepsVersion)" -ForegroundColor Green

    # Decide which release tag hosts the deps archive. If the bundled tool
    # versions did not change since the last build (same depsVersion) AND a tag is
    # already recorded, reuse that hosted archive (its tag + sha). Only when deps
    # actually change do we re-host: the archive must be uploaded to the current
    # version's release and the freshly-built sha recorded.
    $lockPath = Join-Path $ScriptRoot "deps-lock.json"
    $prevLock = $null
    if (Test-Path $lockPath) { $prevLock = Get-Content $lockPath -Raw | ConvertFrom-Json }
    if ($ForceDepsRelease) {
        $depsTag  = "v$AppVersion"
        $depsSha  = $depsZipSha
        $depsSize = $depsZipSize
        $depsArchiveForUpload = $true
        Write-Host "[Deps] FORCE -> host $DepsAsset on release $depsTag (sha $depsSha)" -ForegroundColor Yellow
    } elseif ($prevLock -and ([string]$prevLock.depsVersion -eq $DepsVersion) -and $prevLock.tag) {
        $depsTag  = [string]$prevLock.tag
        $depsSha  = [string]$prevLock.sha256
        $depsSize = [long]$prevLock.size
        if (Test-Path $depsZip) { Remove-Item $depsZip -Force }
        Write-Host "[Deps] Unchanged (depsVersion=$DepsVersion) - reusing archive hosted at $depsTag" -ForegroundColor Cyan
        Write-Host "[Deps] Removed freshly-built local deps zip; upload/use the hosted archive recorded in deps-lock.json" -ForegroundColor Gray
    } else {
        $depsTag  = "v$AppVersion"
        $depsSha  = $depsZipSha
        $depsSize = $depsZipSize
        $depsArchiveForUpload = $true
        Write-Host "[Deps] CHANGED -> host $DepsAsset on release $depsTag (sha $depsSha)" -ForegroundColor Yellow
    }

    # Record the dependencies-archive identity so CI (-AppOnly) writes the app
    # manifest without rebuilding bin/. Commit deps-lock.json when deps change,
    # and host the archive on the recorded tag's release once.
    $lock = [ordered]@{ platform = $ReleasePlatform; arch = $ReleaseArch; depsVersion = $DepsVersion; tag = $depsTag; asset = $DepsAsset; sha256 = $depsSha; size = $depsSize; requiredFiles = $RequiredDepFiles }
    [System.IO.File]::WriteAllText($lockPath, ($lock | ConvertTo-Json), (New-Object System.Text.UTF8Encoding($false)))

    # dependencies.json manifest ships inside resources/ next to dropo-ui.exe
    # and dropo-core.exe.
    # Runtime resolves the freshest compatible asset across all GitHub releases;
    # no fixed release-download URL is embedded in the client.
    $manifest = [ordered]@{
        depsVersion = $DepsVersion
        platform    = $ReleasePlatform
        arch        = $ReleaseArch
        asset       = $DepsAsset
        sha256      = $depsSha
        size        = $depsSize
        appVersion  = $AppVersion
        repo        = "Droponevedimka/dropo"
        requiredFiles = $RequiredDepFiles
    }
    # Write UTF-8 WITHOUT BOM (Set-Content -Encoding utf8 adds a BOM that Go's
    # json.Unmarshal rejects).
    [System.IO.File]::WriteAllText((Join-Path $RuntimeFolder "dependencies.json"), ($manifest | ConvertTo-Json), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "[OK] Wrote dependencies.json (depsVersion=$DepsVersion, tag=$depsTag)" -ForegroundColor Green

    # The dependency archive identity is known only after bin/ has been staged
    # and compressed. Rebuild the core and launcher once with that final lock so
    # a one-pass full build cannot ship executables pinned to the previous
    # dependency archive.
    $finalRequired = $RequiredDepFiles -join ','
    $finalLdflags = "-X 'main.trustedDepsVersion=$DepsVersion' -X 'main.trustedDepsAsset=$DepsAsset' -X 'main.trustedDepsSHA256=$depsSha' -X 'main.trustedDepsSize=$depsSize' -X 'main.trustedDepsRequired=$finalRequired' -X 'main.Version=$AppVersion' -X 'main.BuildTime=$BuildTime' -X 'main.BuildHash=$BuildHash' -X 'main.SingBoxVersion=$SingBoxVersion' -X 'main.WireGuardVersion=$WireGuardVersion' -s -w -H=windowsgui"
    Push-Location $AppDir
    try {
        & go build -ldflags $finalLdflags -o $coreExe .
        if ($LASTEXITCODE -ne 0) {
            throw "Go core rebuild with final dependency lock failed."
        }
    } finally {
        Pop-Location
    }
    Invoke-WindowsCodeSigning -Paths @($coreExe)
    $coreSHA256 = (Get-FileHash -LiteralPath $coreExe -Algorithm SHA256).Hash.ToLowerInvariant()

    Push-Location (Join-Path $ScriptRoot "launcher")
    try {
        $launcherLdflags = "-X 'main.expectedCoreSHA256=$coreSHA256' -X 'main.expectedUISHA256=$uiSHA256' -s -w -H=windowsgui"
        & go build -ldflags $launcherLdflags -o (Join-Path $AppFolder "dropo.exe") .
        if ($LASTEXITCODE -ne 0) {
            throw "Launcher rebuild after dependency lock binding failed."
        }
    } finally {
        Pop-Location
    }
    Invoke-WindowsCodeSigning -Paths @((Join-Path $AppFolder "dropo.exe"))
    Write-Host "[OK] Rebuilt core and launcher with final dependency lock" -ForegroundColor Green

    # app archive = the dropo/ app folder WITHOUT bin/ (small, ships every release)
    $AppAsset = $WindowsPortableAsset
    $zipFile = Join-Path $VersionDir $AppAsset
    if (Test-Path $zipFile) { Remove-Item $zipFile -Force }
    New-AppOnlyArchive -SourceAppFolder $AppFolder -DestinationZip $zipFile
    $zipSize = (Get-Item $zipFile).Length / 1MB
    Write-Host "[OK] Created $AppAsset ($([math]::Round($zipSize, 2)) MB, app only - bin/ ships as $DepsAsset)" -ForegroundColor Green

    Write-Host ""
    Write-Host "[SUCCESS] Build completed: release/$BuildFolderName/" -ForegroundColor Green
    Write-Host "  app folder:  dropo/  (run dropo/dropo.exe)" -ForegroundColor Gray
    if ($depsArchiveForUpload) {
        Write-Host "  release zips: $AppAsset + $DepsAsset" -ForegroundColor Gray
    } else {
        Write-Host "  release zips: $AppAsset (deps reused from $depsTag)" -ForegroundColor Gray
    }

    # Show files
    Write-Host ""
    Write-Host "Output files (release/$BuildFolderName/):" -ForegroundColor Cyan
    Get-ChildItem $VersionDir -Recurse | ForEach-Object {
        $size = if ($_.PSIsContainer) { "" } else { " ({0:N2} MB)" -f ($_.Length / 1MB) }
        $relativePath = $_.FullName.Replace($VersionDir, "").TrimStart("\")
        Write-Host "  $relativePath$size" -ForegroundColor White
    }
}

function Get-FlutterCommand {
    $flutter = Get-Command flutter -ErrorAction SilentlyContinue
    if ($flutter) {
        return $flutter.Source
    }

    $localFlutter = "E:\flutter-sdk\flutter\bin\flutter.bat"
    if (Test-Path $localFlutter) {
        return $localFlutter
    }

    return $null
}

function Get-GoMobileCommand {
    $gomobile = Get-Command gomobile -ErrorAction SilentlyContinue
    if ($gomobile) {
        return $gomobile.Source
    }

    $localGoMobile = Join-Path $env:USERPROFILE "go\bin\gomobile.exe"
    if (Test-Path $localGoMobile) {
        return $localGoMobile
    }

    return $null
}

function Initialize-AndroidBuildEnvironment {
    if (-not $env:ANDROID_HOME -and -not $env:ANDROID_SDK_ROOT) {
        $localAndroidSdk = "E:\android-sdk"
        if (Test-Path $localAndroidSdk) {
            $env:ANDROID_HOME = $localAndroidSdk
            $env:ANDROID_SDK_ROOT = $localAndroidSdk
        }
    }

    if (-not $env:JAVA_HOME) {
        $localJdk = "E:\java\jdk-21.0.11+10"
        if (Test-Path $localJdk) {
            $env:JAVA_HOME = $localJdk
        }
    }

    if ($env:JAVA_HOME -and ($env:PATH -notlike "$env:JAVA_HOME\bin*")) {
        $env:PATH = "$env:JAVA_HOME\bin;$env:PATH"
    }
}

function Get-MtCommand {
    $mt = Get-Command mt.exe -ErrorAction SilentlyContinue
    if ($mt) {
        return $mt.Source
    }

    $kitsDir = "C:\Program Files (x86)\Windows Kits\10\bin"
    if (Test-Path $kitsDir) {
        $candidate = Get-ChildItem $kitsDir -Recurse -Filter mt.exe -ErrorAction SilentlyContinue |
            Where-Object { $_.FullName -match "\\x64\\mt\.exe$" } |
            Sort-Object FullName -Descending |
            Select-Object -First 1
        if ($candidate) {
            return $candidate.FullName
        }
    }

    return $null
}

function Set-WindowsAdminManifest {
    param([string]$ExePath)

    $manifestPath = Join-Path $ScriptRoot "flutter_app\windows\runner\dropo_admin.manifest"
    if (-not (Test-Path $manifestPath)) {
        Write-Host "[ERROR] Admin manifest not found: $manifestPath" -ForegroundColor Red
        exit 1
    }

    $mt = Get-MtCommand
    if (-not $mt) {
        Write-Host "[ERROR] mt.exe not found. Install Windows SDK / Visual Studio Build Tools." -ForegroundColor Red
        exit 1
    }

    & $mt -manifest $manifestPath "-outputresource:$ExePath;#1"
    if ($LASTEXITCODE -ne 0) {
        Write-Host "[ERROR] Failed to embed administrator manifest into $ExePath" -ForegroundColor Red
        exit 1
    }
    Write-Host "[OK] Embedded administrator manifest" -ForegroundColor Green
}

function Get-AndroidBridgeFingerprint {
    param(
        [string]$BridgeDir,
        [string]$CoreDir
    )

    $files = @()
    foreach ($dir in @($BridgeDir, $CoreDir)) {
        if (Test-Path $dir) {
            $files += Get-ChildItem -Path $dir -Recurse -File -Include "*.go", "go.mod", "go.sum" |
                Sort-Object FullName
        }
    }

    $builder = New-Object System.Text.StringBuilder
    [void]$builder.AppendLine("sing-box=$SingBoxVersion")
    [void]$builder.AppendLine("gomobileTarget=$AndroidGoMobileTarget")
    [void]$builder.AppendLine("tags=$AndroidBridgeTags")
    foreach ($file in $files) {
        $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $file.FullName).Hash.ToLower()
        [void]$builder.AppendLine("$($file.FullName)|$hash")
    }

    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [System.Text.Encoding]::UTF8.GetBytes($builder.ToString())
        return [BitConverter]::ToString($sha.ComputeHash($bytes)).Replace("-", "").ToLower()
    } finally {
        $sha.Dispose()
    }
}

function Build-AndroidBridgeAAR {
    Write-Host ""
    Write-Host "Building Android gomobile bridge..." -ForegroundColor Yellow

    $GoMobileCmd = Get-GoMobileCommand
    if (-not $GoMobileCmd) {
        Write-Host "[ERROR] gomobile not found. Install it with: go install golang.org/x/mobile/cmd/gomobile@latest" -ForegroundColor Red
        exit 1
    }

    Initialize-AndroidBuildEnvironment

    $MobileBridgeDir = Join-Path $ScriptRoot "app\mobile\dropoandroid"
    $MobileCoreDir = Join-Path $ScriptRoot "app\mobile\dropocore"
    $AARDir = Join-Path $ScriptRoot "flutter_app\android\app\libs"
    $AARPath = Join-Path $AARDir "dropoandroid.aar"
    $VersionMarker = Join-Path $AARDir "dropoandroid.version"
    $GoModPath = Join-Path $MobileBridgeDir "go.mod"
    $GoSumPath = Join-Path $MobileBridgeDir "go.sum"
    $OriginalGoMod = if (Test-Path $GoModPath) { [System.IO.File]::ReadAllBytes($GoModPath) } else { $null }
    $OriginalGoSum = if (Test-Path $GoSumPath) { [System.IO.File]::ReadAllBytes($GoSumPath) } else { $null }

    if (-not (Test-Path $MobileBridgeDir)) {
        Write-Host "[ERROR] Android mobile bridge not found: $MobileBridgeDir" -ForegroundColor Red
        exit 1
    }
    if (-not (Test-Path $MobileCoreDir)) {
        Write-Host "[ERROR] Android mobile core not found: $MobileCoreDir" -ForegroundColor Red
        exit 1
    }

    New-Item -ItemType Directory -Force -Path $AARDir | Out-Null
    $fingerprint = Get-AndroidBridgeFingerprint -BridgeDir $MobileBridgeDir -CoreDir $MobileCoreDir
    if ((Test-Path $AARPath) -and (Test-Path $VersionMarker) -and ((Get-Content $VersionMarker -Raw).Trim() -eq $fingerprint)) {
        $aarMB = [math]::Round((Get-Item $AARPath).Length / 1MB, 2)
        Write-Host "[OK] Reusing Android bridge AAR: $AARPath ($aarMB MB, v$SingBoxVersion)" -ForegroundColor Green
        return
    }

    foreach ($staleName in @(
            "dropocore.aar",
            "dropocore-sources.jar",
            "dropocore.version",
            "libbox.aar",
            "libbox-sources.jar",
            "libbox.version"
        )) {
        $stalePath = Join-Path $AARDir $staleName
        if (Test-Path $stalePath) {
            Remove-Item -LiteralPath $stalePath -Force
        }
    }

    Push-Location $MobileBridgeDir
    try {
        & go mod tidy
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] Failed to prepare Android bridge Go module." -ForegroundColor Red
            exit 1
        }

        & go list -mod=mod golang.org/x/mobile/cmd/gobind | Out-Null
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] Failed to prepare gomobile gobind tool for Android bridge." -ForegroundColor Red
            exit 1
        }

        $ldflags = "-X github.com/sagernet/sing-box/constant.Version=$SingBoxVersion -X internal/godebug.defaultGODEBUG=multipathtcp=0 -s -w -buildid= -checklinkname=0"
        & $GoMobileCmd bind `
            "-target=$AndroidGoMobileTarget" `
            -androidapi=23 `
            -trimpath `
            -ldflags $ldflags `
            -tags $AndroidBridgeTags `
            -o $AARPath `
            .
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] gomobile bind failed for Android bridge." -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
        if ($null -ne $OriginalGoMod) {
            [System.IO.File]::WriteAllBytes($GoModPath, $OriginalGoMod)
        }
        if ($null -ne $OriginalGoSum) {
            [System.IO.File]::WriteAllBytes($GoSumPath, $OriginalGoSum)
        }
    }

    [System.IO.File]::WriteAllText($VersionMarker, $fingerprint, (New-Object System.Text.UTF8Encoding($false)))
    $aarMB = [math]::Round((Get-Item $AARPath).Length / 1MB, 2)
    Write-Host "[OK] Created Android bridge AAR: $AARPath ($aarMB MB, v$SingBoxVersion)" -ForegroundColor Green
}

function Get-AndroidBuildNumber {
    $v = [version]$AppVersion
    return ($v.Major * 10000) + ($v.Minor * 100) + $v.Build
}

function Build-AndroidApplication {
    Write-Host ""
    Write-Host "Building Android Flutter APK..." -ForegroundColor Yellow

    Build-AndroidBridgeAAR

    $FlutterCmd = Get-FlutterCommand
    if (-not $FlutterCmd) {
        Write-Host "[ERROR] Flutter SDK not found. Install Flutter or use E:\flutter-sdk\flutter\bin\flutter.bat" -ForegroundColor Red
        exit 1
    }

    $FlutterDir = Join-Path $ScriptRoot "flutter_app"
    if (-not (Test-Path $FlutterDir)) {
        Write-Host "[ERROR] flutter_app/ not found." -ForegroundColor Red
        exit 1
    }

    if (-not (Test-Path $VersionDir)) {
        New-Item -ItemType Directory -Path $VersionDir | Out-Null
    }

    $buildNumber = Get-AndroidBuildNumber
    Push-Location $FlutterDir
    try {
        & $FlutterCmd build apk --release --target-platform $AndroidFlutterTargetPlatform --build-name $AppVersion --build-number $buildNumber
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] Flutter Android build failed. Run 'flutter doctor -v' and check Android SDK/JDK." -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }

    $sourceApk = Join-Path $FlutterDir "build\app\outputs\flutter-apk\app-release.apk"
    if (-not (Test-Path $sourceApk)) {
        Write-Host "[ERROR] Android APK output not found: $sourceApk" -ForegroundColor Red
        exit 1
    }

    $assetName = $AndroidAppAsset
    $destApk = Join-Path $VersionDir $assetName
    Copy-Item $sourceApk $destApk -Force

    $sha = (Get-FileHash -Algorithm SHA256 $destApk).Hash.ToLower()
    [System.IO.File]::WriteAllText((Join-Path $VersionDir "$assetName.sha256"), "$sha  $assetName`n", (New-Object System.Text.UTF8Encoding($false)))
    $apkMB = [math]::Round((Get-Item $destApk).Length / 1MB, 2)

    Write-Host "[OK] Created Android APK: $assetName ($apkMB MB)" -ForegroundColor Green
    Write-Host "[OK] SHA256: $sha" -ForegroundColor Green
}

# Create portable ZIP (standalone, for manual use)
function Create-Portable {
    Write-Host ""
    Write-Host "Creating portable ZIP..." -ForegroundColor Yellow

    $sourceDir = $VersionDir
    if (-not (Test-Path $sourceDir)) {
        # Try to find latest version
        $latestVer = Get-LatestRelease
        if ($latestVer) {
            $sourceDir = Join-Path $ReleaseDir $latestVer
            Write-Host "Using latest release: $latestVer" -ForegroundColor Gray
        } else {
            Write-Host "[ERROR] No built version found. Run with -Build first." -ForegroundColor Red
            exit 1
        }
    }

    # The runnable app lives in <container>/dropo/ (no version/hash in its name).
    $appFolder = Join-Path $sourceDir "dropo"
    $appExe = Join-Path $appFolder "dropo.exe"
    if (-not (Test-Path $appExe)) {
        Write-Host "[ERROR] dropo.exe not found in $appFolder" -ForegroundColor Red
        exit 1
    }
    if ($AllowUntrustedSelfSignedWindows) {
        Copy-PetCertificateBundle -Destination $appFolder
    }

    # App-only ZIP (exclude bin/ — heavy deps ship as the separate
    # dependencies archive). Written INSIDE the container, name without
    # version/hash. Idempotent with Build-Application's packaging.
    $zipFile = Join-Path $sourceDir $WindowsPortableAsset
    if (Test-Path $zipFile) {
        Remove-Item $zipFile
    }
    New-AppOnlyArchive -SourceAppFolder $appFolder -DestinationZip $zipFile

    $fileSize = (Get-Item $zipFile).Length / 1MB
    Write-Host "[OK] Created: $WindowsPortableAsset ($([math]::Round($fileSize, 2)) MB, app only - bin/ ships separately)" -ForegroundColor Green
}

# Main execution
# Windows ships as a portable archive only (no installer). Launch-at-logon is
# handled by a per-user Scheduled Task the app registers at runtime, so no
# installer is required.
if ($AppOnly) {
    Build-Application
    if ($Android) {
        Build-AndroidApplication
    }
} elseif ($All) {
    Build-Application
    Create-Portable
    Build-AndroidApplication
} else {
    $didWork = $false
    if ($Flutter -or $Build) {
        Build-Application
        $didWork = $true
    }
    if ($Portable) {
        Create-Portable
        $didWork = $true
    }
    if ($Android) {
        Build-AndroidApplication
        $didWork = $true
    }
    if (-not $didWork) {
        Build-Application
        Create-Portable
    }
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "   Done!" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Cyan
