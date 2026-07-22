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

# Read current version
$config = Get-Content $versionFile | ConvertFrom-Json
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

# Update version.json
$config.version = $newVersion
$versionJson = $config | ConvertTo-Json -Depth 10
[IO.File]::WriteAllText(
    $versionFile,
    $versionJson + [Environment]::NewLine,
    [Text.UTF8Encoding]::new($false)
)

Write-Host ""
Write-Host "Updated version.json" -ForegroundColor Green

# Keep Flutter's versionName/versionCode synchronized with version.json. The
# build script rejects a mismatch, so version bumping must update both sources
# atomically from the release operator's perspective.
$pubspecPath = Join-Path $ScriptRoot "flutter_app\pubspec.yaml"
$pubspec = [IO.File]::ReadAllText($pubspecPath, [Text.UTF8Encoding]::new($false, $true))
$buildNumber = ($parsedVersion.Major * 10000) + ($parsedVersion.Minor * 100) + $parsedVersion.Build
$replacement = "version: $newVersion+$buildNumber"
$updatedPubspec = [regex]::Replace($pubspec, '(?m)^version:\s*[^\r\n]+$', $replacement, 1)
if ($updatedPubspec -eq $pubspec) {
    throw "Could not update the version line in flutter_app/pubspec.yaml."
}
[IO.File]::WriteAllText($pubspecPath, $updatedPubspec, [Text.UTF8Encoding]::new($false))
Write-Host "Updated flutter_app/pubspec.yaml ($replacement)" -ForegroundColor Green
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Run: .\scripts\build\build.ps1" -ForegroundColor White
Write-Host "  2. Commit: git add . && git commit -m 'Release v$newVersion'" -ForegroundColor White
Write-Host "  3. Tag: git tag v$newVersion && git push --tags" -ForegroundColor White
