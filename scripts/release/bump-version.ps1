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
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Run: .\scripts\build\build.ps1" -ForegroundColor White
Write-Host "  2. Commit: git add . && git commit -m 'Release v$newVersion'" -ForegroundColor White
Write-Host "  3. Tag: git tag v$newVersion && git push --tags" -ForegroundColor White
