# dropo Build Script
# This script builds the application and outputs to release/dropo-{version}-{hash}/ folder.

param(
    [switch]$Build,
    [switch]$Installer,
    [switch]$Portable,
    [switch]$All,
    [switch]$Clean,
    [string]$Version,
    # CI mode: build only dropo.exe + app folder (no bundled bin/). The heavy
    # dependencies archive is hosted once and referenced via deps-lock.json /
    # the dependencies.json manifest. Used by .github/workflows/release.yml so a
    # clean runner can release the app without the dependencies/ cache.
    [switch]$AppOnly,
    # Full-build mode: record the freshly built dependencies archive as hosted
    # on the current app version, even when depsVersion did not change. Use this
    # when re-baselining dependencies onto a new release tag.
    [switch]$ForceDepsRelease,
    # Backward-compatible alias: Windows is now built with Flutter + Go core.
    [switch]$Flutter,
    # Opt-in Android artifact build for the Flutter migration path.
    [switch]$Android
)

$ErrorActionPreference = "Stop"
$ScriptRoot = $PSScriptRoot

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

    $versions = Get-ChildItem -Path $releaseDir -Directory |
        Where-Object { $_.Name -match '^(dropo-)?(\d+\.\d+\.\d+)-([0-9a-f]+)$' } |
        ForEach-Object {
            $null = $_.Name -match '^(dropo-)?(\d+\.\d+\.\d+)-([0-9a-f]+)$'
            [PSCustomObject]@{
                Name      = $_.Name
                Version   = [version]$Matches[2]
                WriteTime = $_.LastWriteTime
            }
        } |
        Sort-Object Version, WriteTime -Descending

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
$SpoofDPIVersion = $VersionInfo.spoofdpi.version
$ZapretVersion = $VersionInfo.zapret.version
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
Write-Host "SpoofDPI:  $SpoofDPIVersion" -ForegroundColor White
Write-Host "zapret:    $ZapretVersion" -ForegroundColor White
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
$SpoofDPIDir = Join-Path $DepsDir "spoofdpi-v$SpoofDPIVersion"
$SpoofDPIExe = Join-Path $SpoofDPIDir "spoofdpi.exe"
$ZapretRoot = Join-Path $DepsDir "zapret-v$ZapretVersion\zapret-v$ZapretVersion"
$ZapretWinDir = Join-Path $ZapretRoot "binaries\windows-x86_64"
$InstallerDir = Join-Path $ScriptRoot "installer"
$ReleasePlatform = "windows"
$ReleaseArch = "x64"
$RequiredDepFiles = @("sing-box.exe", "winws.exe", "WinDivert.dll")
$WindowsPortableAsset = "dropo-Windows-Portable-x64.zip"
$WindowsDepsAsset = "dropo-Windows-Dependencies-x64.zip"

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

    # Build Go core with ldflags (include build hash for dev identification)
    $ldflags = "-X 'main.Version=$AppVersion' -X 'main.BuildTime=$BuildTime' -X 'main.BuildHash=$BuildHash' -X 'main.SingBoxVersion=$SingBoxVersion' -X 'main.WireGuardVersion=$WireGuardVersion' -s -w -H=windowsgui"

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
        Write-Host "Building Flutter Windows UI..." -ForegroundColor Gray
        & $FlutterCmd build windows --release --build-name $AppVersion --build-number 1 --dart-define "DROPO_CORE_ENDPOINT=http://127.0.0.1:17890"
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] Flutter Windows build failed." -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }
    $FlutterOutput = Join-Path $FlutterDir "build\windows\x64\runner\Release"
    if (-not (Test-Path $FlutterOutput)) {
        Write-Host "[ERROR] Flutter output not found: $FlutterOutput" -ForegroundColor Red
        exit 1
    }
    Set-WindowsAdminManifest -ExePath (Join-Path $FlutterOutput "dropo.exe")
    Copy-Item -Path (Join-Path $FlutterOutput "*") -Destination $RuntimeFolder -Recurse -Force
    $uiExe = Join-Path $RuntimeFolder "dropo.exe"
    if (-not (Test-Path $uiExe)) {
        Write-Host "[ERROR] dropo.exe not found after Flutter build!" -ForegroundColor Red
        exit 1
    }
    Rename-Item -LiteralPath $uiExe -NewName "dropo-ui.exe" -Force
    Write-Host "[OK] Built Flutter dropo-ui.exe" -ForegroundColor Green

    Push-Location (Join-Path $ScriptRoot "launcher")
    try {
        Write-Host "Building dropo launcher..." -ForegroundColor Gray
        & go build -ldflags "-s -w -H=windowsgui" -o (Join-Path $AppFolder "dropo.exe") .
        if ($LASTEXITCODE -ne 0) {
            Write-Host "[ERROR] dropo launcher build failed!" -ForegroundColor Red
            exit 1
        }
    } finally {
        Pop-Location
    }
    Set-WindowsAdminManifest -ExePath (Join-Path $AppFolder "dropo.exe")
    Write-Host "[OK] Built launcher dropo.exe" -ForegroundColor Green

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
        $manifest = [ordered]@{
            depsVersion = [string]$lock.depsVersion
            platform    = if ($lock.platform) { [string]$lock.platform } else { $ReleasePlatform }
            arch        = if ($lock.arch) { [string]$lock.arch } else { $ReleaseArch }
            asset       = [string]$lock.asset
            url         = "https://github.com/Droponevedimka/dropo/releases/download/$lockTag/$([string]$lock.asset)"
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

    # Copy SpoofDPI if a Windows binary was provided manually.
    # Upstream publishes Linux/macOS assets; Windows support is optional until
    # a compatible spoofdpi.exe is available.
    $spoofDpiDst = Join-Path $binDir "spoofdpi.exe"
    if (Test-Path $SpoofDPIExe) {
        Copy-Item $SpoofDPIExe $spoofDpiDst -Force
        Write-Host "[OK] Copied bin/spoofdpi.exe (SpoofDPI v$SpoofDPIVersion)" -ForegroundColor Green
    } else {
        Write-Host "[INFO] Optional spoofdpi.exe not found at: $SpoofDPIExe" -ForegroundColor DarkGray
    }

    # Copy zapret/winws transparent DPI-bypass runtime to bin/ folder.
    $ZapretFiles = @("winws.exe", "cygwin1.dll", "WinDivert.dll", "WinDivert64.sys")
    foreach ($file in $ZapretFiles) {
        $src = Join-Path $ZapretWinDir $file
        $dst = Join-Path $binDir $file
        if (Test-Path $src) {
            Copy-Item $src $dst -Force
            Write-Host "[OK] Copied bin/$file (zapret v$ZapretVersion)" -ForegroundColor Green
        } else {
            Write-Host "[WARNING] zapret file not found at: $src" -ForegroundColor Yellow
        }
    }
    $ZapretFakeDir = Join-Path $ZapretRoot "files\fake"
    if (Test-Path $ZapretFakeDir) {
        $fakeFiles = Get-ChildItem -Path $ZapretFakeDir -File -Filter "*.bin"
        foreach ($fake in $fakeFiles) {
            Copy-Item $fake.FullName (Join-Path $binDir $fake.Name) -Force
        }
        Write-Host "[OK] Copied $($fakeFiles.Count) zapret fake payload file(s)" -ForegroundColor Green
    } else {
        Write-Host "[WARNING] zapret fake payload folder not found at: $ZapretFakeDir" -ForegroundColor Yellow
    }
    $ZapretWinDivertFilterDir = Join-Path $ZapretRoot "init.d\windivert.filter.examples"
    $ZapretWinDivertFilterFiles = @(
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

    # Download + bundle Flowseal/tg-ws-proxy (local MTProto-over-WebSocket proxy
    # for Telegram). MIT licensed. Cached under dependencies/ to avoid re-download.
    $TgWsProxyDir = Join-Path $DepsDir "tg-ws-proxy-v$TgWsProxyVersion"
    $TgWsProxyHeadlessSrc = Join-Path $TgWsProxyDir "TgWsProxy_headless_windows.exe"
    $TgWsProxyTraySrc = Join-Path $TgWsProxyDir "TgWsProxy_windows.exe"
    $tgWsProxyDst = Join-Path $binDir "tg-ws-proxy.exe"
    if (-not (Test-Path $TgWsProxyHeadlessSrc) -and -not (Test-Path $TgWsProxyTraySrc)) {
        try {
            $tgUrl = "https://github.com/Flowseal/tg-ws-proxy/releases/download/v$TgWsProxyVersion/TgWsProxy_windows.exe"
            Write-Host "[TG-WS] Downloading tg-ws-proxy v$TgWsProxyVersion..." -ForegroundColor Yellow
            Download-File -Url $tgUrl -Destination $TgWsProxyTraySrc
        } catch {
            Write-Host "[WARNING] Could not download tg-ws-proxy: $($_.Exception.Message)" -ForegroundColor Yellow
        }
    }
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
    # tg-ws-proxy is MIT — ship its license notice.
    $TgWsProxyLicense = Join-Path $TgWsProxyDir "LICENSE"
    if (-not (Test-Path $TgWsProxyLicense)) {
        try {
            Download-File -Url "https://raw.githubusercontent.com/Flowseal/tg-ws-proxy/main/LICENSE" -Destination $TgWsProxyLicense
        } catch {
            Write-Host "[WARNING] Could not download tg-ws-proxy LICENSE: $($_.Exception.Message)" -ForegroundColor Yellow
        }
    }
    if (Test-Path $TgWsProxyLicense -PathType Leaf) {
        Copy-LicenseFile $TgWsProxyLicense (Join-Path $RuntimeFolder "licenses") "tg-ws-proxy-LICENSE.txt"
    }

    # Copy third-party license notices required by bundled sidecar binaries.
    $licensesDir = Join-Path $RuntimeFolder "licenses"
    Copy-LicenseFile (Join-Path $SingBoxDir "windows-amd64\sing-box-$SingBoxVersion-windows-amd64\LICENSE") $licensesDir "sing-box-LICENSE.txt"
    Copy-LicenseFile (Join-Path $XrayDir "LICENSE") $licensesDir "xray-LICENSE.txt"
    Copy-LicenseFile (Join-Path $WireGuardDir "LICENSE") $licensesDir "wireguard-windows-LICENSE.txt"
    Copy-LicenseFile (Join-Path $ByeDPIDir "LICENSE") $licensesDir "byedpi-LICENSE.txt"
    Copy-LicenseFile (Join-Path $ZapretRoot "docs\LICENSE.txt") $licensesDir "zapret-LICENSE.txt"
    $spoofDpiLicense = Join-Path $SpoofDPIDir "LICENSE"
    if (Test-Path $spoofDpiLicense -PathType Leaf) {
        Copy-LicenseFile $spoofDpiLicense $licensesDir "spoofdpi-LICENSE.txt"
    }

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
    # it changes only when an actual binary changes (see docs/UPDATE.md).
    $depsKey = @(
        [string]$VersionInfo.singbox.version,
        [string]$VersionInfo.wireguard.version,
        [string]$VersionInfo.wireguard.wintunVersion,
        [string]$VersionInfo.byedpi.version,
        [string]$VersionInfo.spoofdpi.version,
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
    # url points straight at the hosting release tag (name-based search is the Go
    # fallback if the tag asset ever moves).
    $depsUrl = "https://github.com/Droponevedimka/dropo/releases/download/$depsTag/$DepsAsset"
    $manifest = [ordered]@{
        depsVersion = $DepsVersion
        platform    = $ReleasePlatform
        arch        = $ReleaseArch
        asset       = $DepsAsset
        url         = $depsUrl
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

function Get-AndroidBuildNumber {
    $v = [version]$AppVersion
    return ($v.Major * 10000) + ($v.Minor * 100) + $v.Build
}

function Build-AndroidApplication {
    Write-Host ""
    Write-Host "Building Android Flutter APK..." -ForegroundColor Yellow

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
        & $FlutterCmd build apk --release --build-name $AppVersion --build-number $buildNumber
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

    $assetName = "dropo-Android-universal.apk"
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

# Create installer
function Create-Installer {
    Write-Host ""
    Write-Host "Creating installer..." -ForegroundColor Yellow

    $sourceDir = $VersionDir
    if (-not (Test-Path $sourceDir)) {
        $latestVer = Get-LatestRelease
        if ($latestVer) {
            $sourceDir = Join-Path $ReleaseDir $latestVer
            Write-Host "Using latest release: $latestVer" -ForegroundColor Gray
        } else {
            Write-Host "[ERROR] No built version found. Run with -Build first." -ForegroundColor Red
            exit 1
        }
    }

    # Check for NSIS
    $nsisPath = $null
    $nsisLocations = @(
        "C:\Program Files (x86)\NSIS\makensis.exe",
        "C:\Program Files\NSIS\makensis.exe"
    )

    foreach ($path in $nsisLocations) {
        if (Test-Path $path) {
            $nsisPath = $path
            break
        }
    }

    if (-not $nsisPath) {
        $nsisCmd = Get-Command makensis -ErrorAction SilentlyContinue
        if ($nsisCmd) {
            $nsisPath = $nsisCmd.Source
        }
    }

    if (-not $nsisPath) {
        Write-Host "[WARNING] NSIS not found. Skipping installer creation." -ForegroundColor Yellow
        Write-Host "          Install NSIS from: https://nsis.sourceforge.io/Download" -ForegroundColor Yellow
        Write-Host "          Or run: winget install NSIS.NSIS" -ForegroundColor Yellow
        return
    }

    Write-Host "[OK] NSIS found: $nsisPath" -ForegroundColor Green

    $appFolder = Join-Path $sourceDir "dropo"
    if (-not (Test-Path (Join-Path $appFolder "dropo.exe") -PathType Leaf)) {
        Write-Host "[ERROR] dropo.exe not found in $appFolder" -ForegroundColor Red
        exit 1
    }

    $sourceName = Split-Path $sourceDir -Leaf
    if ($sourceName -notmatch '^dropo-') {
        $sourceName = "dropo-$sourceName"
    }
    $installerFileName = "$sourceName-setup.exe"

    # Generate NSIS script with current version and paths
    $nsiScript = @"
; dropo NSIS Installer (Auto-generated)
!define PRODUCT_VERSION "$AppVersion"
!define SOURCE_DIR "$appFolder"
!define OUTPUT_DIR "$ReleaseDir"

!include "MUI2.nsh"

Name "dropo `${PRODUCT_VERSION}"
OutFile "`${OUTPUT_DIR}\$installerFileName"
InstallDir "`$PROGRAMFILES64\dropo"
RequestExecutionLevel admin

!define MUI_ICON "$InstallerDir\assets\icon.ico"
!define MUI_UNICON "$InstallerDir\assets\icon.ico"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_LICENSE "$InstallerDir\assets\license.txt"
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN "`$INSTDIR\dropo.exe"
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "Russian"

Section "Install"
    SetOutPath "`$INSTDIR"
    File /r "`${SOURCE_DIR}\*.*"

    CreateDirectory "`$SMPROGRAMS\dropo"
    CreateShortCut "`$SMPROGRAMS\dropo\dropo.lnk" "`$INSTDIR\dropo.exe"
    CreateShortCut "`$SMPROGRAMS\dropo\Uninstall.lnk" "`$INSTDIR\uninst.exe"
    CreateShortCut "`$DESKTOP\dropo.lnk" "`$INSTDIR\dropo.exe"

    WriteUninstaller "`$INSTDIR\uninst.exe"

    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo" "DisplayName" "dropo"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo" "UninstallString" "`$INSTDIR\uninst.exe"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo" "DisplayVersion" "`${PRODUCT_VERSION}"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo" "Publisher" "dropo"
    WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo" "DisplayIcon" "`$INSTDIR\dropo.exe"
SectionEnd

Section "Uninstall"
    nsExec::ExecToLog 'taskkill /F /IM dropo.exe'
    nsExec::ExecToLog 'taskkill /F /IM dropo-ui.exe'
    nsExec::ExecToLog 'taskkill /F /IM dropo-core.exe'
    nsExec::ExecToLog 'taskkill /F /IM sing-box.exe'
    nsExec::ExecToLog 'taskkill /F /IM xray.exe'
    nsExec::ExecToLog 'taskkill /F /IM ciadpi.exe'
    nsExec::ExecToLog 'taskkill /F /IM spoofdpi.exe'
    nsExec::ExecToLog 'taskkill /F /IM winws.exe'
    nsExec::ExecToLog 'taskkill /F /IM winws2.exe'

    RMDir /r "`$INSTDIR"

    Delete "`$SMPROGRAMS\dropo\*.lnk"
    RMDir "`$SMPROGRAMS\dropo"
    Delete "`$DESKTOP\dropo.lnk"

    DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\dropo"
SectionEnd
"@

    $tempNsi = Join-Path $env:TEMP "dropo_installer.nsi"
    $nsiScript | Out-File -FilePath $tempNsi -Encoding UTF8

    # Build installer
    & $nsisPath $tempNsi

    if ($LASTEXITCODE -eq 0) {
        $installerFile = Join-Path $ReleaseDir $installerFileName
        if (Test-Path $installerFile) {
            $fileSize = (Get-Item $installerFile).Length / 1MB
            Write-Host "[OK] Created: $installerFileName ($([math]::Round($fileSize, 2)) MB)" -ForegroundColor Green
        }
    } else {
        Write-Host "[ERROR] NSIS build failed!" -ForegroundColor Red
    }

    Remove-Item $tempNsi -ErrorAction SilentlyContinue
}

# Main execution
if ($Flutter -or $Android) {
    if ($Build -or $All -or $Flutter) {
        Build-Application
        if ($All) {
            Create-Portable
            Create-Installer
        }
    }
    if ($Android) { Build-AndroidApplication }
} elseif ($AppOnly) {
    Build-Application
} elseif ($All -or (-not $Build -and -not $Installer -and -not $Portable)) {
    Build-Application
    Create-Portable
    Create-Installer
} else {
    if ($Build) { Build-Application }
    if ($Portable) { Create-Portable }
    if ($Installer) { Create-Installer }
}

Write-Host ""
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "   Done!" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Cyan
