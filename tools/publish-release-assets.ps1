param(
    [string]$ReleaseFolder,
    [switch]$ReplaceExisting,
    [switch]$SkipPreflight
)

$ErrorActionPreference = "Stop"
$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptRoot
$Repository = "Droponevedimka/dropo"

function Get-GitHubToken {
    if (-not [string]::IsNullOrWhiteSpace($env:GH_TOKEN)) {
        return $env:GH_TOKEN
    }
    $credential = @{}
    ("protocol=https`nhost=github.com`n`n" | git credential fill) | ForEach-Object {
        $key, $value = $_ -split "=", 2
        if ($key) { $credential[$key] = $value }
    }
    if ([string]::IsNullOrWhiteSpace($credential.password)) {
        throw "GitHub token was not found. Set GH_TOKEN or authenticate Git for github.com."
    }
    return [string]$credential.password
}

function Invoke-GitHubApi {
    param(
        [ValidateSet("Get", "Post", "Patch", "Delete")][string]$Method,
        [string]$Uri,
        [object]$Body
    )

    $params = @{
        Method = $Method
        Uri = $Uri
        Headers = $script:GitHubHeaders
    }
    if ($null -ne $Body) {
        # Windows PowerShell 5.1 sends a string request body through the active
        # ANSI code page unless explicit UTF-8 bytes are provided. That replaced
        # every Cyrillic release-note character with '?'. Keep JSON Unicode-safe.
        $json = $Body | ConvertTo-Json -Depth 10
        $params.ContentType = "application/json; charset=utf-8"
        $params.Body = [System.Text.UTF8Encoding]::new($false).GetBytes($json)
    }
    return Invoke-RestMethod @params
}

function Get-AndroidApkSigner {
    $roots = @(
        $env:ANDROID_HOME,
        $env:ANDROID_SDK_ROOT,
        "E:\android-sdk",
        (Join-Path $env:LOCALAPPDATA "Android\Sdk")
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | Select-Object -Unique

    foreach ($root in $roots) {
        $buildTools = Join-Path $root "build-tools"
        if (-not (Test-Path -LiteralPath $buildTools -PathType Container)) { continue }
        $candidate = Get-ChildItem -LiteralPath $buildTools -Directory |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName "apksigner.bat" } |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } |
            Select-Object -First 1
        if ($candidate) { return $candidate }
    }
    throw "apksigner.bat was not found. Install Android build-tools before publishing."
}

function Initialize-AndroidSigningEnvironment {
    if (-not $env:JAVA_HOME) {
        $jdk = @(
            "E:\java\jdk-21.0.11+10",
            (Join-Path $env:USERPROFILE ".kampus-java\jdk-17.0.12+7")
        ) | Where-Object { Test-Path -LiteralPath (Join-Path $_ "bin\java.exe") } | Select-Object -First 1
        if ($jdk) { $env:JAVA_HOME = $jdk }
    }
    if (-not $env:JAVA_HOME -or -not (Test-Path -LiteralPath (Join-Path $env:JAVA_HOME "bin\java.exe"))) {
        throw "Java JDK was not found. Set JAVA_HOME before publishing Android assets."
    }
    if ($env:PATH -notlike "$env:JAVA_HOME\bin*") {
        $env:PATH = "$env:JAVA_HOME\bin;$env:PATH"
    }
}

function Upload-ReleaseAsset {
    param(
        [long]$ReleaseId,
        [string]$Path,
        [object[]]$ExistingAssets
    )

    $name = [IO.Path]::GetFileName($Path)
    $existing = @($ExistingAssets | Where-Object { $_.name -eq $name }) | Select-Object -First 1
    if ($existing) {
        if (-not $ReplaceExisting) {
            throw "Release already contains $name. Re-run with -ReplaceExisting only after verifying the replacement."
        }
        Invoke-GitHubApi -Method Delete -Uri "https://api.github.com/repos/$Repository/releases/assets/$($existing.id)"
    }

    $encodedName = [uri]::EscapeDataString($name)
    $uploadUri = "https://uploads.github.com/repos/$Repository/releases/$ReleaseId/assets?name=$encodedName"
    $uploaded = Invoke-RestMethod -Method Post -Uri $uploadUri -Headers $script:GitHubHeaders -InFile $Path -ContentType "application/octet-stream"
    $actual = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ([string]$uploaded.digest -ne "sha256:$actual") {
        throw "GitHub digest mismatch for ${name}: $($uploaded.digest)"
    }
    Write-Host "[OK] Uploaded $name ($actual)" -ForegroundColor Green
}

$version = (Get-Content (Join-Path $RepoRoot "version.json") -Raw | ConvertFrom-Json).version
if ([string]::IsNullOrWhiteSpace($ReleaseFolder)) {
    $ReleaseFolder = Get-ChildItem (Join-Path $RepoRoot "release") -Directory |
        Where-Object { $_.Name -like "dropo-$version-*" } |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1 -ExpandProperty FullName
}
if (-not $ReleaseFolder -or -not (Test-Path -LiteralPath $ReleaseFolder -PathType Container)) {
    throw "Release folder for $version was not found. Build it locally first."
}

$windowsInstaller = Join-Path $ReleaseFolder "dropo-Windows-Setup-x64.exe"
$windowsPortable = Join-Path $ReleaseFolder "dropo-Windows-Portable-x64.zip"
$androidApk = Join-Path $ReleaseFolder "dropo-Android-arm64.apk"
$tag = "v$version"
$releaseAssets = @($windowsInstaller, $windowsPortable, $androidApk)
foreach ($path in $releaseAssets) {
	if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
		throw "Required release asset was not found: $path"
    }
}

if (-not $SkipPreflight) {
    & (Join-Path $ScriptRoot "preflight-release.ps1") -SkipInstall -SkipFlutterChecks -SkipLifecycleSmoke -ReleaseFolder $ReleaseFolder
    if ($LASTEXITCODE -ne 0) { throw "Release preflight failed." }
}

Initialize-AndroidSigningEnvironment
$apkSigner = Get-AndroidApkSigner
$apkVerification = (& $apkSigner verify --verbose --print-certs $androidApk) -join "`n"
if ($LASTEXITCODE -ne 0) { throw "apksigner verification failed." }
$apkDigest = [regex]::Match($apkVerification, 'certificate SHA-256 digest:\s*([0-9a-fA-F]+)')
if (-not $apkDigest.Success) { throw "APK certificate digest was not reported." }
$expectedAndroidDigest = (Get-Content (Join-Path $RepoRoot "signing\android-release-cert.sha256") -Raw).Trim().ToLowerInvariant()
if ($apkDigest.Groups[1].Value.ToLowerInvariant() -ne $expectedAndroidDigest) {
    throw "APK is signed by an unexpected certificate."
}

$token = Get-GitHubToken
$script:GitHubHeaders = @{
    Accept = "application/vnd.github+json"
    Authorization = "Bearer $token"
    "X-GitHub-Api-Version" = "2022-11-28"
    "User-Agent" = "dropo-local-release-publisher"
}
$release = Invoke-GitHubApi -Method Get -Uri "https://api.github.com/repos/$Repository/releases/tags/$tag"
if ($release.draft) { throw "Release $tag is still a draft." }

foreach ($assetPath in $releaseAssets) {
	Upload-ReleaseAsset -ReleaseId $release.id -Path $assetPath -ExistingAssets $release.assets
}

$windowsInstallerSha = (Get-FileHash -LiteralPath $windowsInstaller -Algorithm SHA256).Hash.ToLowerInvariant()
$windowsPortableSha = (Get-FileHash -LiteralPath $windowsPortable -Algorithm SHA256).Hash.ToLowerInvariant()
$androidSha = (Get-FileHash -LiteralPath $androidApk -Algorithm SHA256).Hash.ToLowerInvariant()
$body = [string]$release.body
$body = $body.Replace("__WINDOWS_INSTALLER_SHA256_PENDING_LOCAL_UPLOAD__", $windowsInstallerSha)
$body = $body.Replace("__WINDOWS_PORTABLE_SHA256_PENDING_LOCAL_UPLOAD__", $windowsPortableSha)
$body = $body.Replace("__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__", $androidSha)
if (-not $body.Contains($windowsInstallerSha) -or -not $body.Contains($windowsPortableSha) -or -not $body.Contains($androidSha)) {
    $body += "`n`n### Local artifact integrity`n`nWindows installer SHA-256: $windowsInstallerSha`n`nWindows portable SHA-256: $windowsPortableSha`n`nAndroid SHA-256: $androidSha`n"
}
Invoke-GitHubApi -Method Patch -Uri "https://api.github.com/repos/$Repository/releases/$($release.id)" -Body @{ body = $body } | Out-Null

Write-Host "[SUCCESS] Published locally verified assets to $tag" -ForegroundColor Green
