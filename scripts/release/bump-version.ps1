# Bump version script
# Usage: .\scripts\release\bump-version.ps1 -Version "1.0.3"
#    or: .\scripts\release\bump-version.ps1 -Patch

param(
    [string]$Version,
    [switch]$Patch,
    [switch]$Minor,
    [switch]$Major
)

$ScriptRoot = (Resolve-Path (Join-Path $PSScriptRoot "..\..")).Path
$versionFile = Join-Path $ScriptRoot "version.json"

# Read the source of truth as strict UTF-8. Windows PowerShell 5.1 otherwise
# decodes UTF-8 without a BOM through the active ANSI code page and can corrupt
# non-ASCII metadata such as the copyright symbol.
$strictUtf8 = [Text.UTF8Encoding]::new($false, $true)
$versionJson = [IO.File]::ReadAllText($versionFile, $strictUtf8)
$config = $versionJson | ConvertFrom-Json
$currentVersion = $config.version

Write-Host "Current version: $currentVersion" -ForegroundColor Cyan

# Parse current version. Local names must NOT collide with the -Major/-Minor/
# -Patch switch parameters: PowerShell variables are case-insensitive, so
# `$major = ...` would clobber the [switch]$Major parameter and corrupt the
# computed version (the "False.False." bug).
$parts = $currentVersion.Split('.')
$verMajor = [int]$parts[0]
$verMinor = [int]$parts[1]
$verPatch = [int]$parts[2]

# Calculate new version
if ($Version) {
    $newVersion = $Version
} elseif ($Major) {
    $newVersion = "$($verMajor + 1).0.0"
} elseif ($Minor) {
    $newVersion = "$verMajor.$($verMinor + 1).0"
} elseif ($Patch) {
    $newVersion = "$verMajor.$verMinor.$($verPatch + 1)"
} else {
    Write-Host "Usage:" -ForegroundColor Yellow
    Write-Host "  .\scripts\release\bump-version.ps1 -Version '1.0.3'" -ForegroundColor White
    Write-Host "  .\scripts\release\bump-version.ps1 -Patch" -ForegroundColor White
    Write-Host "  .\scripts\release\bump-version.ps1 -Minor" -ForegroundColor White
    Write-Host "  .\scripts\release\bump-version.ps1 -Major" -ForegroundColor White
    exit 0
}

Write-Host "New version: $newVersion" -ForegroundColor Green

$parsedVersion = $null
if (-not [version]::TryParse($newVersion, [ref]$parsedVersion) -or
    $parsedVersion.Major -lt 0 -or $parsedVersion.Minor -lt 0 -or $parsedVersion.Build -lt 0 -or
    $parsedVersion.Revision -ge 0) {
    throw "Version must contain exactly three non-negative numeric components (for example 3.0.11)."
}

# Update only the version token so formatting and unrelated metadata remain
# byte-for-byte stable.
$topLevelVersionPattern = '(?m)^(  "version"\s*:\s*")[^"]+("\s*,\s*)$'
$versionMatches = [regex]::Matches($versionJson, $topLevelVersionPattern)
if ($versionMatches.Count -ne 1) {
    throw "version.json must contain exactly one top-level version field."
}
$updatedVersionJson = [regex]::Replace(
    $versionJson,
    $topLevelVersionPattern,
    ('${1}' + $newVersion + '${2}')
)
if ($updatedVersionJson -eq $versionJson) {
    throw "Could not update the version field in version.json."
}
[IO.File]::WriteAllText($versionFile, $updatedVersionJson, [Text.UTF8Encoding]::new($false))

Write-Host ""
Write-Host "Updated version.json" -ForegroundColor Green

# Keep Flutter's versionName/versionCode synchronized with version.json. The
# build script rejects a mismatch, so version bumping must update both sources
# atomically from the release operator's perspective.
$pubspecPath = Join-Path $ScriptRoot "flutter_app\pubspec.yaml"
$pubspec = [IO.File]::ReadAllText($pubspecPath, [Text.UTF8Encoding]::new($false, $true))
$buildNumber = ($parsedVersion.Major * 10000) + ($parsedVersion.Minor * 100) + $parsedVersion.Build
$replacement = "version: $newVersion+$buildNumber"
$pubspecVersionPattern = '(?m)^version:\s*[^\r\n]+$'
$pubspecVersionMatches = [regex]::Matches($pubspec, $pubspecVersionPattern)
if ($pubspecVersionMatches.Count -ne 1) {
    throw "flutter_app/pubspec.yaml must contain exactly one version field."
}
$updatedPubspec = [regex]::Replace($pubspec, $pubspecVersionPattern, $replacement)
if ($updatedPubspec -eq $pubspec) {
    throw "Could not update the version line in flutter_app/pubspec.yaml."
}
[IO.File]::WriteAllText($pubspecPath, $updatedPubspec, [Text.UTF8Encoding]::new($false))
Write-Host "Updated flutter_app/pubspec.yaml ($replacement)" -ForegroundColor Green
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Run the source checks and commit the synchronized version." -ForegroundColor White
Write-Host "  2. Build and validate the clean release commit locally." -ForegroundColor White
Write-Host "  3. Push main once; GitHub Actions creates the immutable v$newVersion tag after the Windows gate." -ForegroundColor White
Write-Host "  4. Publish the verified Setup, Portable and Android assets with tools\publish-release-assets.ps1." -ForegroundColor White
