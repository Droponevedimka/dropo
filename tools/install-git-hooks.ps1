$ErrorActionPreference = "Stop"

$repositoryRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repositoryRoot
try {
    & git config --local core.hooksPath .githooks
    & git config --local user.name "Droponevedimka"
    & git config --local user.email "34841931+Droponevedimka@users.noreply.github.com"
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to configure repository Git identity"
    }

    & (Join-Path $PSScriptRoot "check-clean-contributors.ps1")
    if ($LASTEXITCODE -ne 0) {
        throw "Contributor metadata validation failed"
    }
} finally {
    Pop-Location
}

Write-Host "Git hooks and repository identity configured."
