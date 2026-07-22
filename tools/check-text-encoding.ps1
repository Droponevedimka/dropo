# Reject invalid UTF-8 and common irreversible encoding damage in tracked text.

$ErrorActionPreference = "Stop"
$RepoRoot = Split-Path -Parent $PSScriptRoot
$strictUtf8 = [System.Text.UTF8Encoding]::new($false, $true)
$textExtensions = @(
    ".cmd", ".css", ".dart", ".go", ".gradle", ".html", ".js", ".json",
    ".kt", ".kts", ".md", ".ps1", ".sh", ".svg", ".toml", ".txt",
    ".xml", ".yaml", ".yml"
)
$explicitTextFiles = @(".editorconfig", ".gitattributes", ".gitignore")
$failures = [System.Collections.Generic.List[string]]::new()
$mojibakePattern = "(?:$([char]0x0420).|$([char]0x0421).){3,}"

Push-Location $RepoRoot
try {
    $trackedFiles = @(git ls-files)
    if ($LASTEXITCODE -ne 0) { throw "git ls-files failed" }

    foreach ($relativePath in $trackedFiles) {
        $extension = [IO.Path]::GetExtension($relativePath).ToLowerInvariant()
        if ($extension -notin $textExtensions -and $relativePath -notin $explicitTextFiles) {
            continue
        }

        $path = Join-Path $RepoRoot $relativePath
        # git ls-files also lists tracked files staged/deleted in the current
        # worktree. Deletion is not an encoding failure.
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
            continue
        }
        try {
            $text = $strictUtf8.GetString([IO.File]::ReadAllBytes($path))
        } catch {
            $failures.Add("${relativePath}: invalid UTF-8")
            continue
        }

        if ($text -match '\?{3,}') {
            $failures.Add("${relativePath}: contains a run of replacement question marks")
        }
        if ($text -cmatch $mojibakePattern) {
            $failures.Add("${relativePath}: contains likely UTF-8/Windows-1251 mojibake")
        }
    }

    # The local release publisher runs under Windows PowerShell 5.1. Passing a
    # JSON string to Invoke-RestMethod there uses the ANSI code page and turns
    # Cyrillic GitHub release notes into question marks. Keep this regression
    # guard next to the general encoding validation used by CI.
    $publisherPath = Join-Path $RepoRoot "tools\publish-release-assets.ps1"
    $publisherText = $strictUtf8.GetString([IO.File]::ReadAllBytes($publisherPath))
    if ($publisherText -notmatch 'application/json; charset=utf-8' -or
        $publisherText -notmatch 'UTF8Encoding\]::new\(\$false\)\.GetBytes') {
        $failures.Add("tools/publish-release-assets.ps1: GitHub JSON PATCH must use explicit UTF-8 bytes")
    }
} finally {
    Pop-Location
}

if ($failures.Count -gt 0) {
    $failures | ForEach-Object { Write-Host "[ENCODING] $_" -ForegroundColor Red }
    throw "Text encoding validation failed for $($failures.Count) file(s)."
}

Write-Host "[OK] Tracked text is valid UTF-8 and contains no known encoding damage." -ForegroundColor Green
