# dropo Build Script
# This script builds the application and outputs to release/dropo-{version}-{hash}/ folder.

param(
    [switch]$Build,
    [switch]$Portable,
    # Build every implemented release artifact in one pass:
    # Windows installer + portable ZIP + Android arm64 APK.
    [switch]$All,
    [switch]$Clean,
    [string]$Version,
    # CI mode: build the complete Windows installer and portable archive.
    [switch]$AppOnly,
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
    # Backward-compatible switch. Unsigned Windows output is now the normal
    # fallback when no publicly trusted signing identity is configured.
    [switch]$AllowUnsignedWindows,
    # Fail closed for publishers that configure a production signing gate.
    [switch]$RequireWindowsSigning,
    # Development-only escape hatch. Public/reproducible packages must always
    # be built from a clean commit.
    [switch]$AllowDirtySource
)

$ErrorActionPreference = "Stop"
$ScriptRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path

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

function Get-BuildHash {
    param([string]$Revision)
    if ($Revision -match '^[0-9a-fA-F]{7,}$') {
        return $Revision.Substring(0, 7).ToLowerInvariant()
    }
    $bytes = [Text.Encoding]::UTF8.GetBytes($Revision)
    $digest = [Security.Cryptography.SHA256]::Create().ComputeHash($bytes)
    return ([BitConverter]::ToString($digest).Replace("-", "").ToLowerInvariant()).Substring(0, 7)
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

$VersionInfo = Get-VersionInfo
$AppVersion = if ($Version) { $Version } else { $VersionInfo.version }
$PubspecPath = Join-Path $ScriptRoot "flutter_app\pubspec.yaml"
if (-not (Test-Path -LiteralPath $PubspecPath -PathType Leaf)) {
    throw "Flutter pubspec not found: $PubspecPath"
}
$PubspecVersionLine = Get-Content -LiteralPath $PubspecPath | Where-Object { $_ -match '^version:\s*' } | Select-Object -First 1
if ($PubspecVersionLine -notmatch '^version:\s*([0-9]+\.[0-9]+\.[0-9]+)(?:\+[0-9]+)?\s*$') {
    throw "flutter_app/pubspec.yaml has no valid semantic version."
}
$PubspecAppVersion = $Matches[1]
if ($PubspecAppVersion -ne [string]$VersionInfo.version) {
    throw "Version mismatch: version.json=$($VersionInfo.version), flutter_app/pubspec.yaml=$PubspecAppVersion. Keep version.json as the release source of truth."
}
$SingBoxVersion = $VersionInfo.singbox.version
$SingBoxArchiveSHA256 = ([string]$VersionInfo.singbox.archiveSha256).ToLowerInvariant()
$SingBoxExeSHA256 = ([string]$VersionInfo.singbox.executableSha256).ToLowerInvariant()
$WireGuardVersion = $VersionInfo.wireguard.version
$WireGuardExeSHA256 = ([string]$VersionInfo.wireguard.wireguardSha256).ToLowerInvariant()
$WireGuardWgSHA256 = ([string]$VersionInfo.wireguard.wgSha256).ToLowerInvariant()
$WireGuardWintunSHA256 = ([string]$VersionInfo.wireguard.wintunSha256).ToLowerInvariant()
$WinDivertVersion = $VersionInfo.windivert.version
$WinDivertArchiveSHA256 = ([string]$VersionInfo.windivert.archiveSha256).ToLowerInvariant()
$WinDivertDllSHA256 = ([string]$VersionInfo.windivert.dllSha256).ToLowerInvariant()
$WinDivertDriverSHA256 = ([string]$VersionInfo.windivert.driverSha256).ToLowerInvariant()
$WinDivertArchiveURL = [string]$VersionInfo.windivert.url
$XrayVersion = $VersionInfo.xray.version
$XrayArchiveSHA256 = ([string]$VersionInfo.xray.archiveSha256).ToLowerInvariant()
$XrayExeSHA256 = ([string]$VersionInfo.xray.executableSha256).ToLowerInvariant()
$TgWsProxyVersion = $VersionInfo.tgwsproxy.version
$TgWsProxyHeadlessSHA256 = ([string]$VersionInfo.tgwsproxy.headlessSha256).ToLowerInvariant()
$TgWsProxyOfficialSHA256 = ([string]$VersionInfo.tgwsproxy.officialWindowsSha256).ToLowerInvariant()
$SourceRevision = (& git -C $ScriptRoot rev-parse HEAD 2>$null | Select-Object -First 1)
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace([string]$SourceRevision)) {
    $SourceRevision = "unknown"
}
$SourceRevision = ([string]$SourceRevision).Trim()
$SourceDirty = @(& git -C $ScriptRoot status --porcelain 2>$null).Count -gt 0
$dirtyBuildAllowed = $AllowDirtySource -or $env:DROPO_ALLOW_DIRTY_BUILD -eq "1"
$willBuildWindows = $Build -or $Flutter -or $AppOnly -or $All -or (-not $Portable -and -not $Android -and -not $Clean)
if ($willBuildWindows -and $SourceDirty -and -not $dirtyBuildAllowed) {
    throw "Reproducible Windows packages require a clean Git worktree. Commit the intended source first, or use -AllowDirtySource for a development-only build."
}
$BuildHash = Get-BuildHash -Revision $SourceRevision
$sourceEpoch = 0L
if ([long]::TryParse([string]$env:SOURCE_DATE_EPOCH, [ref]$sourceEpoch) -and $sourceEpoch -gt 0) {
    $BuildDate = [DateTimeOffset]::FromUnixTimeSeconds($sourceEpoch).UtcDateTime
} elseif ($SourceRevision -ne "unknown") {
    $commitEpoch = (& git -C $ScriptRoot show -s --format=%ct $SourceRevision 2>$null | Select-Object -First 1)
    if (-not [long]::TryParse(([string]$commitEpoch).Trim(), [ref]$sourceEpoch) -or $sourceEpoch -le 0) {
        throw "Could not resolve the source commit timestamp."
    }
    $BuildDate = [DateTimeOffset]::FromUnixTimeSeconds($sourceEpoch).UtcDateTime
} else {
    $BuildDate = [DateTime]::UnixEpoch
}
$BuildTime = $BuildDate.ToString("yyyy-MM-dd HH:mm:ss")
$BuildTimestampISO = $BuildDate.ToString("yyyy-MM-ddTHH:mm:ssZ")

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
Write-Host "WinDivert: $WinDivertVersion" -ForegroundColor White
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
$WinDivertDir = Join-Path $DepsDir "WinDivert-$WinDivertVersion-A"
$WinDivertX64Dir = Join-Path $WinDivertDir "x64"
$ReleasePlatform = "windows"
$ReleaseArch = "x64"
$RequiredDepFiles = @("sing-box.exe", "xray.exe", "wireguard.exe", "wg.exe", "wintun.dll", "WinDivert.dll", "WinDivert64.sys")
$ForbiddenDepFiles = @("winws.exe", "winws2.exe", "cygwin1.dll", "zapret-lib.lua", "zapret-antidpi.lua")
$WindowsInstallerAsset = "dropo-Windows-Setup-x64.exe"
$WindowsPortableAsset = "dropo-Windows-Portable-x64.zip"
$AndroidReleaseArch = "arm64"
$AndroidFlutterTargetPlatform = "android-arm64"
$AndroidGoMobileTarget = "android/arm64"
$AndroidBridgeTags = "with_gvisor,with_quic,with_utls,with_clash_api,badlinkname,tfogo_checklinkname0"
$AndroidAppAsset = "dropo-Android-$AndroidReleaseArch.apk"

function Ensure-SingBoxWindowsDependency {
    if (Test-Path $SingBoxExe) {
        Assert-FileSHA256 -Path $SingBoxExe -ExpectedSHA256 $SingBoxExeSHA256 -Label "sing-box executable"
        return
    }

    Write-Host "[SING-BOX] Downloading sing-box v$SingBoxVersion for Windows..." -ForegroundColor Yellow
    $archiveUrl = "https://github.com/SagerNet/sing-box/releases/download/v$SingBoxVersion/sing-box-$SingBoxVersion-windows-amd64.zip"
    $tmpDir = Join-Path $env:TEMP ("dropo-sing-box-" + [Guid]::NewGuid().ToString("N"))
    $zipPath = Join-Path $tmpDir "sing-box.zip"
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        Download-File -Url $archiveUrl -Destination $zipPath
        Assert-FileSHA256 -Path $zipPath -ExpectedSHA256 $SingBoxArchiveSHA256 -Label "sing-box archive"
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
        Assert-FileSHA256 -Path $SingBoxExe -ExpectedSHA256 $SingBoxExeSHA256 -Label "sing-box executable"
        Write-Host "[OK] Downloaded sing-box.exe v$SingBoxVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Ensure-WinDivertWindowsDependency {
    $expected = Join-Path $WinDivertX64Dir "WinDivert.dll"
    if (Test-Path $expected) {
        Assert-FileSHA256 -Path $expected -ExpectedSHA256 $WinDivertDllSHA256 -Label "WinDivert DLL"
        Assert-FileSHA256 -Path (Join-Path $WinDivertX64Dir "WinDivert64.sys") -ExpectedSHA256 $WinDivertDriverSHA256 -Label "WinDivert driver"
        return
    }
    if ($WinDivertArchiveSHA256 -notmatch '^[0-9a-f]{64}$') {
        throw "version.json must pin windivert.archiveSha256 for WinDivert v$WinDivertVersion."
    }

    Write-Host "[WINDIVERT] Downloading official WinDivert v$WinDivertVersion..." -ForegroundColor Yellow
    $tmpDir = Join-Path $env:TEMP ("dropo-windivert-" + [Guid]::NewGuid().ToString("N"))
    $zipPath = Join-Path $tmpDir "windivert.zip"
    New-Item -ItemType Directory -Path $tmpDir | Out-Null
    try {
        Download-File -Url $WinDivertArchiveURL -Destination $zipPath
        $actualHash = (Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $WinDivertArchiveSHA256) {
            throw "WinDivert archive hash mismatch: expected $WinDivertArchiveSHA256, got $actualHash"
        }
        $extractRoot = Join-Path $tmpDir "extract"
        Expand-Archive -Path $zipPath -DestinationPath $extractRoot -Force
        $extracted = Join-Path $extractRoot "WinDivert-$WinDivertVersion-A"
        if (-not (Test-Path (Join-Path $extracted "x64\WinDivert64.sys") -PathType Leaf)) {
            throw "Official archive did not contain the expected x64 WinDivert files."
        }
        if (Test-Path $WinDivertDir) {
            Remove-Item -LiteralPath $WinDivertDir -Recurse -Force
        }
        Copy-Item -LiteralPath $extracted -Destination $WinDivertDir -Recurse -Force
        if (-not (Test-Path $expected)) {
            throw "Downloaded archive did not contain expected file: $expected"
        }
        Assert-FileSHA256 -Path $expected -ExpectedSHA256 $WinDivertDllSHA256 -Label "WinDivert DLL"
        Assert-FileSHA256 -Path (Join-Path $WinDivertX64Dir "WinDivert64.sys") -ExpectedSHA256 $WinDivertDriverSHA256 -Label "WinDivert driver"
        Write-Host "[OK] Downloaded and SHA-256 verified official WinDivert v$WinDivertVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Assert-FileSHA256 {
    param(
        [string]$Path,
        [string]$ExpectedSHA256,
        [string]$Label
    )
    if ($ExpectedSHA256 -notmatch '^[0-9a-f]{64}$') {
        throw "$Label has no pinned SHA-256 in version.json."
    }
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $ExpectedSHA256) {
        throw "$Label SHA-256 mismatch: expected $ExpectedSHA256, got $actual"
    }
}

function Ensure-XrayWindowsDependency {
    if (Test-Path -LiteralPath $XrayExe -PathType Leaf) {
        Assert-FileSHA256 -Path $XrayExe -ExpectedSHA256 $XrayExeSHA256 -Label "Xray executable"
        return
    }
    $tmpDir = Join-Path $env:TEMP ("dropo-xray-" + [Guid]::NewGuid().ToString("N"))
    $zipPath = Join-Path $tmpDir "Xray-windows-64.zip"
    try {
        New-Item -ItemType Directory -Path $tmpDir | Out-Null
        Download-File -Url "https://github.com/XTLS/Xray-core/releases/download/v$XrayVersion/Xray-windows-64.zip" -Destination $zipPath
        Assert-FileSHA256 -Path $zipPath -ExpectedSHA256 $XrayArchiveSHA256 -Label "Xray archive"
        if (Test-Path -LiteralPath $XrayDir) {
            Remove-Item -LiteralPath $XrayDir -Recurse -Force
        }
        New-Item -ItemType Directory -Path $XrayDir | Out-Null
        Expand-Archive -LiteralPath $zipPath -DestinationPath $XrayDir -Force
        if (-not (Test-Path -LiteralPath $XrayExe -PathType Leaf)) {
            throw "Official Xray archive does not contain xray.exe."
        }
        Assert-FileSHA256 -Path $XrayExe -ExpectedSHA256 $XrayExeSHA256 -Label "Xray executable"
        Write-Host "[OK] Downloaded and verified Xray-core v$XrayVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Ensure-WireGuardWindowsDependency {
    $wireGuardDir = Join-Path $DepsDir "wireguard-windows-v$WireGuardVersion"
    $expectedFiles = [ordered]@{
        "wireguard.exe" = $WireGuardExeSHA256
        "wg.exe"        = $WireGuardWgSHA256
        "wintun.dll"    = $WireGuardWintunSHA256
    }
    $complete = $true
    foreach ($entry in $expectedFiles.GetEnumerator()) {
        $candidate = Join-Path $wireGuardDir $entry.Key
        if (-not (Test-Path -LiteralPath $candidate -PathType Leaf)) {
            $complete = $false
            break
        }
        Assert-FileSHA256 -Path $candidate -ExpectedSHA256 $entry.Value -Label "WireGuard $($entry.Key)"
    }
    if ($complete) {
        return
    }

    $tmpDir = Join-Path $env:TEMP ("dropo-wireguard-" + [Guid]::NewGuid().ToString("N"))
    $msiPath = Join-Path $tmpDir "wireguard-amd64-$WireGuardVersion.msi"
    $extractDir = Join-Path $tmpDir "extract"
    try {
        New-Item -ItemType Directory -Path $tmpDir | Out-Null
        Download-File -Url "https://download.wireguard.com/windows-client/wireguard-amd64-$WireGuardVersion.msi" -Destination $msiPath
        $signature = Get-AuthenticodeSignature -LiteralPath $msiPath
        if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid -or
            -not $signature.SignerCertificate -or
            $signature.SignerCertificate.Subject -notmatch 'WireGuard') {
            throw "Official WireGuard MSI has no valid WireGuard Authenticode signature."
        }
        New-Item -ItemType Directory -Path $extractDir | Out-Null
        $process = Start-Process msiexec.exe -ArgumentList @('/a', ('"' + $msiPath + '"'), '/qn', ('TARGETDIR="' + $extractDir + '"')) -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw "WireGuard administrative MSI extraction failed with exit code $($process.ExitCode)."
        }
        if (Test-Path -LiteralPath $wireGuardDir) {
            Remove-Item -LiteralPath $wireGuardDir -Recurse -Force
        }
        New-Item -ItemType Directory -Path $wireGuardDir | Out-Null
        foreach ($entry in $expectedFiles.GetEnumerator()) {
            $matches = @(Get-ChildItem -LiteralPath $extractDir -Recurse -File -Filter $entry.Key)
            if ($matches.Count -ne 1) {
                throw "WireGuard MSI must contain exactly one $($entry.Key); found $($matches.Count)."
            }
            Assert-FileSHA256 -Path $matches[0].FullName -ExpectedSHA256 $entry.Value -Label "WireGuard $($entry.Key)"
            Copy-Item -LiteralPath $matches[0].FullName -Destination (Join-Path $wireGuardDir $entry.Key)
        }
        Download-File -Url "https://raw.githubusercontent.com/WireGuard/wireguard-windows/v$WireGuardVersion/LICENSE" -Destination (Join-Path $wireGuardDir "LICENSE")
        Write-Host "[OK] Downloaded and verified WireGuard for Windows v$WireGuardVersion" -ForegroundColor Green
    } finally {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Ensure-TgWsProxyWindowsDependency {
    $tgDir = Join-Path $DepsDir "tg-ws-proxy-v$TgWsProxyVersion"
    $headless = Join-Path $tgDir "TgWsProxy_headless_windows.exe"
    $official = Join-Path $tgDir "TgWsProxy_windows.exe"
    if (Test-Path -LiteralPath $headless -PathType Leaf) {
        Assert-FileSHA256 -Path $headless -ExpectedSHA256 $TgWsProxyHeadlessSHA256 -Label "tg-ws-proxy headless executable"
        return
    }
    if (Test-Path -LiteralPath $official -PathType Leaf) {
        Assert-FileSHA256 -Path $official -ExpectedSHA256 $TgWsProxyOfficialSHA256 -Label "tg-ws-proxy official executable"
        return
    }
    New-Item -ItemType Directory -Path $tgDir -Force | Out-Null
    Download-File -Url "https://github.com/Flowseal/tg-ws-proxy/releases/download/v$TgWsProxyVersion/TgWsProxy_windows.exe" -Destination $official
    Assert-FileSHA256 -Path $official -ExpectedSHA256 $TgWsProxyOfficialSHA256 -Label "tg-ws-proxy official executable"
    Download-File -Url "https://raw.githubusercontent.com/Flowseal/tg-ws-proxy/v$TgWsProxyVersion/LICENSE" -Destination (Join-Path $tgDir "LICENSE")
    Write-Host "[OK] Downloaded and verified tg-ws-proxy v$TgWsProxyVersion" -ForegroundColor Green
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

    New-DeterministicZip -SourceDirectory $SourceAppFolder -DestinationZip $DestinationZip
}

function New-DeterministicZip {
    param(
        [string]$SourceDirectory,
        [string]$DestinationZip
    )

    Add-Type -AssemblyName System.IO.Compression
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $sourceRoot = (Resolve-Path -LiteralPath $SourceDirectory).Path.TrimEnd('\')
    if (Test-Path -LiteralPath $DestinationZip) {
        Remove-Item -LiteralPath $DestinationZip -Force
    }
    $zipParent = Split-Path -Parent $DestinationZip
    if ($zipParent -and -not (Test-Path -LiteralPath $zipParent)) {
        New-Item -ItemType Directory -Path $zipParent -Force | Out-Null
    }
    $fileStream = [IO.File]::Open($DestinationZip, [IO.FileMode]::CreateNew, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
    $archive = [IO.Compression.ZipArchive]::new($fileStream, [IO.Compression.ZipArchiveMode]::Create, $false)
    try {
        $entryTime = [DateTimeOffset]::new($BuildDate)
        if ($entryTime.Year -lt 1980) {
            $entryTime = [DateTimeOffset]::new([DateTime]::new(1980, 1, 1, 0, 0, 0, [DateTimeKind]::Utc))
        }
        foreach ($file in @(Get-ChildItem -LiteralPath $sourceRoot -Recurse -File | Sort-Object FullName)) {
            $relative = $file.FullName.Substring($sourceRoot.Length).TrimStart([char[]]"\/").Replace('\', '/')
            $entry = $archive.CreateEntry($relative, [IO.Compression.CompressionLevel]::Optimal)
            $entry.LastWriteTime = $entryTime
            $input = [IO.File]::OpenRead($file.FullName)
            $output = $entry.Open()
            try {
                $input.CopyTo($output)
            } finally {
                $output.Dispose()
                $input.Dispose()
            }
        }
    } finally {
        $archive.Dispose()
        $fileStream.Dispose()
    }
}

function Get-InnoSetupCommand {
    $fromPath = Get-Command ISCC.exe -ErrorAction SilentlyContinue
    if ($fromPath) {
        return $fromPath.Source
    }
    foreach ($candidate in @(
        (Join-Path $env:LOCALAPPDATA "Programs\Inno Setup 6\ISCC.exe"),
        (Join-Path ${env:ProgramFiles(x86)} "Inno Setup 6\ISCC.exe"),
        (Join-Path $env:ProgramFiles "Inno Setup 6\ISCC.exe")
    )) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return $candidate
        }
    }
    return $null
}

function New-WindowsInstaller {
    param(
        [string]$SourceAppFolder,
        [string]$DestinationExe,
        [string]$PackageVersion
    )

    $iscc = Get-InnoSetupCommand
    if (-not $iscc) {
        throw "Inno Setup 6 was not found. Install it with: winget install --id JRSoftware.InnoSetup -e"
    }
    $script = Join-Path $ScriptRoot "packaging\windows\dropo.iss"
    $setupIcon = Join-Path $ScriptRoot "flutter_app\windows\runner\resources\app_icon.ico"
    if (-not (Test-Path -LiteralPath $setupIcon -PathType Leaf)) {
        throw "Windows installer icon was not found: $setupIcon"
    }
    $outputDir = Split-Path -Parent $DestinationExe
    $baseName = [IO.Path]::GetFileNameWithoutExtension($DestinationExe)
    & $iscc "/DSourceDir=$SourceAppFolder" "/DOutputDir=$outputDir" "/DAppVersion=$PackageVersion" "/DSetupBaseName=$baseName" "/DSetupIconFile=$setupIcon" $script
    if ($LASTEXITCODE -ne 0) {
        throw "Inno Setup compilation failed with exit code $LASTEXITCODE."
    }
    if (-not (Test-Path -LiteralPath $DestinationExe -PathType Leaf)) {
        throw "Inno Setup did not create the expected installer: $DestinationExe"
    }
    Invoke-WindowsCodeSigning -Paths @($DestinationExe)
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
    $hasPfx = -not [string]::IsNullOrWhiteSpace($pfxPath)
    $hasCertificateSha1 = -not [string]::IsNullOrWhiteSpace($certificateSha1)

    if (-not $hasPfx -and -not $hasCertificateSha1) {
        if ($RequireWindowsSigning -or $env:DROPO_REQUIRE_WINDOWS_SIGNING -eq "1") {
            throw "Windows signing is required, but no public certificate is configured."
        }
        if (-not $script:UnsignedWindowsNoticeShown) {
            Write-Host "[WARNING] No public Windows signing identity configured; Windows artifacts will remain unsigned." -ForegroundColor Yellow
            $script:UnsignedWindowsNoticeShown = $true
        }
        return
    }
    if ($hasPfx -and (-not (Test-Path -LiteralPath $pfxPath -PathType Leaf))) {
        throw "Windows signing certificate not found: $pfxPath"
    }
    if ($hasPfx -and [string]::IsNullOrWhiteSpace($pfxPassword)) {
        throw "DROPO_WINDOWS_PFX_PASSWORD is required with DROPO_WINDOWS_PFX_PATH."
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
            throw "Authenticode verification failed for $path (exit code $LASTEXITCODE)."
        }
        Write-Host "[OK] Signed $([IO.Path]::GetFileName($path))" -ForegroundColor Green
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

    # Every Windows artifact is self-contained, including CI/AppOnly requests.
    Ensure-SingBoxWindowsDependency
    Ensure-WinDivertWindowsDependency
    Ensure-XrayWindowsDependency
    Ensure-WireGuardWindowsDependency
    Ensure-TgWsProxyWindowsDependency
    # Every build, including AppOnly/CI builds, verifies the repository-owned
    # blocked catalog and refreshes it when upstream published a new release.
    # End-user startup never downloads routing data.
    & (Join-Path $ScriptRoot "scripts\filters\update-blocked-lists.ps1") -RepositoryRoot $ScriptRoot -SingBoxPath $SingBoxExe
    if ($LASTEXITCODE -ne 0) {
        throw "Bundled blocked-list update failed with exit code $LASTEXITCODE"
    }
    $postCatalogDirty = @(& git -C $ScriptRoot status --porcelain 2>$null).Count -gt 0
    if ($postCatalogDirty -and -not $dirtyBuildAllowed) {
        throw "The blocked catalog changed during the build. Review and commit the refreshed catalog, then rebuild from that clean commit."
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

    # Build an initial core. It is rebuilt after native files are staged, with
    # the exact runtime-manifest hash embedded into the signed executable.
    $ldflags = "-X 'main.Version=$AppVersion' -X 'main.BuildTime=$BuildTime' -X 'main.BuildHash=$BuildHash' -X 'main.SingBoxVersion=$SingBoxVersion' -X 'main.WireGuardVersion=$WireGuardVersion' -s -w -H=windowsgui"

    Push-Location $AppDir
    try {
        Write-Host "Building dropo-core.exe $AppVersion (hash: $BuildHash)..." -ForegroundColor Gray
        & go build -trimpath -buildvcs=false -ldflags $ldflags -o (Join-Path $RuntimeFolder "dropo-core.exe") .

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
            & $FlutterCmd build windows --release --build-name $AppVersion --build-number 1 --dart-define "DROPO_CORE_ENDPOINT=http://127.0.0.1:17890" --dart-define "DROPO_APP_VERSION=$AppVersion"
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
        & go build -trimpath -buildvcs=false -ldflags $launcherLdflags -o (Join-Path $AppFolder "dropo.exe") .
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] dropo launcher build failed!" -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }
    Write-Host "[OK] Built launcher dropo.exe" -ForegroundColor Green

    Invoke-WindowsCodeSigning -Paths @((Join-Path $AppFolder "dropo.exe"))

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

    # Copy the official WinDivert runtime used by Dropo's in-process engine.
    foreach ($file in @("WinDivert.dll", "WinDivert64.sys")) {
        $src = Join-Path $WinDivertX64Dir $file
        $dst = Join-Path $binDir $file
        if (-not (Test-Path $src -PathType Leaf)) {
            throw "Required official WinDivert file not found: $src"
        }
        Copy-Item $src $dst -Force
        Write-Host "[OK] Copied bin/$file (official WinDivert v$WinDivertVersion)" -ForegroundColor Green
    }

    # Copy Xray-core for VLESS xhttp bridge outbounds
    $xrayDst = Join-Path $binDir "xray.exe"
    if (Test-Path $XrayExe) {
        Copy-Item $XrayExe $xrayDst -Force
        Write-Host "[OK] Copied bin/xray.exe (Xray-core v$XrayVersion)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] xray.exe not found at: $XrayExe" -ForegroundColor Yellow
    }

    # Bundle the pinned tg-ws-proxy dependency (local MTProto-over-WebSocket
    # proxy for Telegram). Prefer the verified headless build when present;
    # clean builders use the verified official upstream Windows release.
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

    # Defender evaluates extracted child PE files independently from the outer
    # installer. Preserve upstream signatures and report unsigned dependencies,
    # but never re-sign third-party files as though dropo authored them.
    $unsignedNestedPE = @(Get-ChildItem -LiteralPath $binDir -File | Where-Object {
        $_.Extension -in @(".exe", ".dll") -and
        (Get-AuthenticodeSignature -FilePath $_.FullName).Status -eq [System.Management.Automation.SignatureStatus]::NotSigned
    } | Select-Object -ExpandProperty FullName)
    if ($unsignedNestedPE.Count -gt 0) {
        Write-Host "[INFO] $($unsignedNestedPE.Count) upstream PE runtime file(s) are unsigned and remain attributed to upstream." -ForegroundColor DarkYellow
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
    Copy-LicenseFile (Join-Path $WinDivertDir "LICENSE") $licensesDir "WinDivert-LICENSE.txt"
    Copy-LicenseFile (Join-Path $DepsDir "filters\LICENSE.Re-filter-lists.txt") $licensesDir "Re-filter-lists-LICENSE.txt"
    Copy-LicenseFile (Join-Path $ScriptRoot "THIRD_PARTY_NOTICES.md") $licensesDir "THIRD_PARTY_NOTICES.md"
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
        Get-ChildItem -Path $filtersDir -File | Where-Object { $_.Extension -in @(".srs", ".lst") } | ForEach-Object {
            Copy-Item $_.FullName $filtersDest -Force
        }
        $filtersVersion = Join-Path $filtersDir "version.json"
        if (Test-Path $filtersVersion) {
            Copy-Item $filtersVersion $filtersDest -Force
        }
        $filterCount = (Get-ChildItem -Path $filtersDest -Filter "*.srs").Count
        $catalogCount = (Get-ChildItem -Path $filtersDest -Filter "*.lst").Count
        Write-Host "[OK] Copied bin/filters/ ($filterCount rule-sets, $catalogCount native catalogs)" -ForegroundColor Green
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

    # The native runtime is content-addressed and described file-by-file. The
    # signed core trusts only the SHA-256 of this local manifest; no executable
    # dependency URL or archive identity is accepted by a current Windows build.
    foreach ($requiredFile in $RequiredDepFiles) {
        if (-not (Test-Path (Join-Path $binDir $requiredFile) -PathType Leaf)) {
			throw "Core dependency staging is missing required file: $requiredFile"
        }
    }
    foreach ($forbiddenFile in $ForbiddenDepFiles) {
        if (Test-Path (Join-Path $binDir $forbiddenFile)) {
            throw "Obsolete external packet runtime must not be packaged: $forbiddenFile"
        }
    }

    $runtimeFiles = @(Get-ChildItem -LiteralPath $binDir -Recurse -File |
        Where-Object { $_.Name -ne ".deps-version" } |
        Sort-Object FullName)
    if ($runtimeFiles.Count -eq 0) {
        throw "Bundled runtime is empty."
    }
    $runtimeIdentity = New-Object System.Text.StringBuilder
    foreach ($file in $runtimeFiles) {
        $relative = "bin/" + $file.FullName.Substring($binDir.Length).TrimStart([char[]]"\/").Replace('\', '/')
        $sha = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        [void]$runtimeIdentity.Append($relative).Append('|').Append([long]$file.Length).Append('|').Append($sha).Append("`n")
    }
    $runtimeIdentityHash = [System.Security.Cryptography.SHA256]::Create().ComputeHash([System.Text.Encoding]::UTF8.GetBytes($runtimeIdentity.ToString()))
    $RuntimeVersion = ([System.BitConverter]::ToString($runtimeIdentityHash).Replace('-', '').ToLowerInvariant()).Substring(0, 12)
    Set-Content -Path (Join-Path $binDir ".deps-version") -Value $RuntimeVersion -NoNewline -Encoding ascii

    $runtimeManifestFiles = @()
    foreach ($file in @(Get-ChildItem -LiteralPath $binDir -Recurse -File | Sort-Object FullName)) {
        $relative = "bin/" + $file.FullName.Substring($binDir.Length).TrimStart([char[]]"\/").Replace('\', '/')
        $runtimeManifestFiles += [ordered]@{
            path   = $relative
            size   = [long]$file.Length
            sha256 = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    }
    $runtimeManifest = [ordered]@{ version = $RuntimeVersion; files = $runtimeManifestFiles }
    $runtimeManifestPath = Join-Path $RuntimeFolder "runtime-manifest.json"
    [System.IO.File]::WriteAllText($runtimeManifestPath, ($runtimeManifest | ConvertTo-Json -Depth 10), (New-Object System.Text.UTF8Encoding($false)))
    $runtimeManifestSHA256 = (Get-FileHash -LiteralPath $runtimeManifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Host "[OK] Wrote trusted runtime manifest (version=$RuntimeVersion, files=$($runtimeManifestFiles.Count))" -ForegroundColor Green

    # A compact SPDX 2.3 software bill of materials travels inside the signed
    # Windows packages. It describes every native runtime file by SHA-256 and
    # the independently versioned components that produced the bundle.
    $spdxSHA1Values = @(Get-ChildItem -LiteralPath $binDir -Recurse -File | Sort-Object FullName | ForEach-Object {
        (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA1).Hash.ToLowerInvariant()
    })
    $spdxVerificationBytes = [System.Text.Encoding]::ASCII.GetBytes(($spdxSHA1Values -join ""))
    $spdxVerificationHash = [System.Security.Cryptography.SHA1]::Create().ComputeHash($spdxVerificationBytes)
    $spdxVerificationCode = [System.BitConverter]::ToString($spdxVerificationHash).Replace('-', '').ToLowerInvariant()
    $spdxPackages = @(
        [ordered]@{ name = "dropo"; SPDXID = "SPDXRef-Package-dropo"; versionInfo = $AppVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $true; packageVerificationCode = [ordered]@{ packageVerificationCodeValue = $spdxVerificationCode }; licenseConcluded = "MIT"; licenseDeclared = "MIT" },
        [ordered]@{ name = "sing-box"; SPDXID = "SPDXRef-Package-sing-box"; versionInfo = $SingBoxVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $false; licenseConcluded = "NOASSERTION"; licenseDeclared = "NOASSERTION" },
        [ordered]@{ name = "Xray-core"; SPDXID = "SPDXRef-Package-xray"; versionInfo = $XrayVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $false; licenseConcluded = "MPL-2.0"; licenseDeclared = "MPL-2.0" },
        [ordered]@{ name = "WireGuard for Windows"; SPDXID = "SPDXRef-Package-wireguard"; versionInfo = $WireGuardVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $false; licenseConcluded = "NOASSERTION"; licenseDeclared = "NOASSERTION" },
        [ordered]@{ name = "WinDivert"; SPDXID = "SPDXRef-Package-windivert"; versionInfo = $WinDivertVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $false; licenseConcluded = "LGPL-3.0-only OR GPL-2.0-only"; licenseDeclared = "LGPL-3.0-only OR GPL-2.0-only" },
        [ordered]@{ name = "tg-ws-proxy"; SPDXID = "SPDXRef-Package-tg-ws-proxy"; versionInfo = $TgWsProxyVersion; downloadLocation = "NOASSERTION"; filesAnalyzed = $false; licenseConcluded = "MIT"; licenseDeclared = "MIT" }
    )
    $spdxFiles = @()
    $spdxRelationships = @([ordered]@{ spdxElementId = "SPDXRef-DOCUMENT"; relationshipType = "DESCRIBES"; relatedSpdxElement = "SPDXRef-Package-dropo" })
    for ($index = 0; $index -lt $runtimeManifestFiles.Count; $index++) {
        $item = $runtimeManifestFiles[$index]
        $fileID = "SPDXRef-File-$($index + 1)"
        $spdxFiles += [ordered]@{
            fileName = "./$($item.path)"
            SPDXID = $fileID
            checksums = @([ordered]@{ algorithm = "SHA256"; checksumValue = $item.sha256 })
            licenseConcluded = "NOASSERTION"
            copyrightText = "NOASSERTION"
        }
        $spdxRelationships += [ordered]@{ spdxElementId = "SPDXRef-Package-dropo"; relationshipType = "CONTAINS"; relatedSpdxElement = $fileID }
    }
    $spdxDocument = [ordered]@{
        spdxVersion = "SPDX-2.3"
        dataLicense = "CC0-1.0"
        SPDXID = "SPDXRef-DOCUMENT"
        name = "dropo-$AppVersion-windows-x64"
        documentNamespace = "https://k-ampus.dev/spdx/dropo/$AppVersion/$BuildHash"
        creationInfo = [ordered]@{ created = $BuildTimestampISO; creators = @("Organization: Droponevedimka") }
        packages = $spdxPackages
        files = $spdxFiles
        relationships = $spdxRelationships
    }
    $spdxPath = Join-Path $RuntimeFolder "dropo-sbom.spdx.json"
    [System.IO.File]::WriteAllText($spdxPath, ($spdxDocument | ConvertTo-Json -Depth 12), (New-Object System.Text.UTF8Encoding($false)))

    # This provenance statement is intentionally local and self-contained. It
    # records the exact source revision and runtime-manifest subject; a public
    # Authenticode identity strengthens origin when one is configured.
    $provenance = [ordered]@{
        _type = "https://in-toto.io/Statement/v1"
        subject = @([ordered]@{ name = "runtime-manifest.json"; digest = [ordered]@{ sha256 = $runtimeManifestSHA256 } })
        predicateType = "https://slsa.dev/provenance/v1"
        predicate = [ordered]@{
            buildDefinition = [ordered]@{
                buildType = "https://k-ampus.dev/build/windows-installer-portable/v1"
                externalParameters = [ordered]@{ version = $AppVersion; platform = $ReleasePlatform; architecture = $ReleaseArch }
                internalParameters = [ordered]@{ buildHash = $BuildHash; sourceRevision = $SourceRevision; sourceDirty = $SourceDirty }
                resolvedDependencies = @(
                    [ordered]@{ uri = "pkg:generic/sing-box@$SingBoxVersion" },
                    [ordered]@{ uri = "pkg:generic/xray-core@$XrayVersion" },
                    [ordered]@{ uri = "pkg:generic/wireguard-windows@$WireGuardVersion" },
                    [ordered]@{ uri = "pkg:generic/windivert@$WinDivertVersion" },
                    [ordered]@{ uri = "pkg:generic/tg-ws-proxy@$TgWsProxyVersion" }
                )
            }
            runDetails = [ordered]@{ builder = [ordered]@{ id = "dropo-local-windows-builder" }; metadata = [ordered]@{ invocationId = $BuildHash; startedOn = $BuildTimestampISO } }
        }
    }
    $provenancePath = Join-Path $RuntimeFolder "dropo-build-provenance.json"
    [System.IO.File]::WriteAllText($provenancePath, ($provenance | ConvertTo-Json -Depth 12), (New-Object System.Text.UTF8Encoding($false)))
    Write-Host "[OK] Wrote SPDX SBOM and package provenance metadata" -ForegroundColor Green

    $finalLdflags = "-X 'main.trustedRuntimeVersion=$RuntimeVersion' -X 'main.trustedRuntimeManifestSHA256=$runtimeManifestSHA256' -X 'main.Version=$AppVersion' -X 'main.BuildTime=$BuildTime' -X 'main.BuildHash=$BuildHash' -X 'main.SingBoxVersion=$SingBoxVersion' -X 'main.WireGuardVersion=$WireGuardVersion' -s -w -H=windowsgui"
    Push-Location $AppDir
    try {
        & go build -trimpath -buildvcs=false -ldflags $finalLdflags -o $coreExe .
        if ($LASTEXITCODE -ne 0) {
            throw "Go core rebuild with final bundled-runtime identity failed."
        }
    } finally {
        Pop-Location
    }
    Invoke-WindowsCodeSigning -Paths @($coreExe)
    $coreSHA256 = (Get-FileHash -LiteralPath $coreExe -Algorithm SHA256).Hash.ToLowerInvariant()

    Push-Location (Join-Path $ScriptRoot "launcher")
    try {
        $launcherLdflags = "-X 'main.expectedCoreSHA256=$coreSHA256' -X 'main.expectedUISHA256=$uiSHA256' -s -w -H=windowsgui"
        & go build -trimpath -buildvcs=false -ldflags $launcherLdflags -o (Join-Path $AppFolder "dropo.exe") .
        if ($LASTEXITCODE -ne 0) {
            throw "Launcher rebuild after bundled-runtime binding failed."
        }
    } finally {
        Pop-Location
    }
    Invoke-WindowsCodeSigning -Paths @((Join-Path $AppFolder "dropo.exe"))
    Write-Host "[OK] Rebuilt core and launcher with the final bundled-runtime identity" -ForegroundColor Green

    # Publish the same complete offline payload in two explicit forms. The
    # installer owns updates and protected Program Files placement; the ZIP is
    # genuinely portable and never attempts to replace itself while running.
    $portablePath = Join-Path $VersionDir $WindowsPortableAsset
    New-AppOnlyArchive -SourceAppFolder $AppFolder -DestinationZip $portablePath
    $installerPath = Join-Path $VersionDir $WindowsInstallerAsset
    New-WindowsInstaller -SourceAppFolder $AppFolder -DestinationExe $installerPath -PackageVersion $AppVersion
    foreach ($assetPath in @($installerPath, $portablePath)) {
        $assetName = Split-Path -Leaf $assetPath
        $assetSHA = (Get-FileHash -LiteralPath $assetPath -Algorithm SHA256).Hash.ToLowerInvariant()
        [IO.File]::WriteAllText("$assetPath.sha256", "$assetSHA  $assetName`n", [Text.UTF8Encoding]::new($false))
        $assetSize = [math]::Round((Get-Item -LiteralPath $assetPath).Length / 1MB, 2)
        Write-Host "[OK] Created $assetName ($assetSize MB, SHA-256 $assetSHA)" -ForegroundColor Green
    }
    if ($env:DROPO_SKIP_WINDOWS_RELEASE_GATE -ne "1") {
        & (Join-Path $ScriptRoot "tools\windows-release-gate.ps1") -InstallerPath $installerPath -PortablePath $portablePath
        if ($LASTEXITCODE -ne 0) {
            throw "Windows MOTW/Defender release gate failed with exit code $LASTEXITCODE"
        }
    }

    Write-Host ""
    Write-Host "[SUCCESS] Build completed: release/$BuildFolderName/" -ForegroundColor Green
    Write-Host "  app folder:  dropo/  (run dropo/dropo.exe)" -ForegroundColor Gray
	$releaseFiles = @($WindowsInstallerAsset, $WindowsPortableAsset)
	Write-Host "  release files: $($releaseFiles -join ' + ')" -ForegroundColor Gray

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
        & $FlutterCmd build apk --release --target-platform $AndroidFlutterTargetPlatform --build-name $AppVersion --build-number $buildNumber --dart-define "DROPO_APP_VERSION=$AppVersion"
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

# Create the deterministic portable ZIP from an existing build.
function Create-Portable {
    Write-Host ""
    Write-Host "Creating Windows portable archive..." -ForegroundColor Yellow

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
    $appAssetPath = Join-Path $sourceDir $WindowsPortableAsset
    New-AppOnlyArchive -SourceAppFolder $appFolder -DestinationZip $appAssetPath

    $fileSize = (Get-Item $appAssetPath).Length / 1MB
    Write-Host "[OK] Created: $WindowsPortableAsset ($([math]::Round($fileSize, 2)) MB, complete offline runtime)" -ForegroundColor Green
}

# Main execution
# Windows ships as an offline installer plus a deterministic portable ZIP.
if ($AppOnly) {
    Build-Application
    if ($Android) {
        Build-AndroidApplication
    }
} elseif ($All) {
    Build-Application
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
