# Bump version script
# Usage: .\bump-version.ps1 -Version "1.0.3"
#    or: .\bump-version.ps1 -Patch    # 1.0.2 -> 1.0.3
#    or: .\bump-version.ps1 -Minor    # 1.0.2 -> 1.1.0
#    or: .\bump-version.ps1 -Major    # 1.0.2 -> 2.0.0

param(
    [string]$Version,
    [switch]$Patch,
    [switch]$Minor,
    [switch]$Major
)

$ScriptRoot = $PSScriptRoot
$versionFile = Join-Path $ScriptRoot "version.json"

# Read current version
$config = Get-Content $versionFile | ConvertFrom-Json
$currentVersion = $config.version

Write-Host "Current version: $currentVersion" -ForegroundColor Cyan

# Parse current version
$parts = $currentVersion.Split('.')
$major = [int]$parts[0]
$minor = [int]$parts[1]
$patch = [int]$parts[2]

# Calculate new version
if ($Version) {
    $newVersion = $Version
} elseif ($Major) {
    $newVersion = "$($major + 1).0.0"
} elseif ($Minor) {
    $newVersion = "$major.$($minor + 1).0"
} elseif ($Patch) {
    $newVersion = "$major.$minor.$($patch + 1)"
} else {
    Write-Host "Usage:" -ForegroundColor Yellow
    Write-Host "  .\bump-version.ps1 -Version '1.0.3'" -ForegroundColor White
    Write-Host "  .\bump-version.ps1 -Patch    # Increment patch version" -ForegroundColor White
    Write-Host "  .\bump-version.ps1 -Minor    # Increment minor version" -ForegroundColor White
    Write-Host "  .\bump-version.ps1 -Major    # Increment major version" -ForegroundColor White
    exit 0
}

Write-Host "New version: $newVersion" -ForegroundColor Green

# Update version.json
$config.version = $newVersion
$config | ConvertTo-Json -Depth 10 | Set-Content $versionFile

Write-Host ""
Write-Host "Updated version.json" -ForegroundColor Green
Write-Host ""
Write-Host "Next steps:" -ForegroundColor Yellow
Write-Host "  1. Run: .\build.ps1" -ForegroundColor White
Write-Host "  2. Commit: git add . && git commit -m 'Release v$newVersion'" -ForegroundColor White
Write-Host "  3. Tag: git tag v$newVersion && git push --tags" -ForegroundColor White
