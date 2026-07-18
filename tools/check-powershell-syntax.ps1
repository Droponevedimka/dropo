$ErrorActionPreference = "Stop"

$root = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$paths = @(
    Get-ChildItem -LiteralPath (Join-Path $root "scripts") -Recurse -File -Filter "*.ps1"
    Get-ChildItem -LiteralPath (Join-Path $root "tools") -Recurse -File -Filter "*.ps1"
) | Sort-Object FullName -Unique

$failed = $false
foreach ($file in $paths) {
    $tokens = $null
    $parseErrors = $null
    [void][System.Management.Automation.Language.Parser]::ParseFile(
        $file.FullName,
        [ref]$tokens,
        [ref]$parseErrors
    )
    foreach ($parseError in @($parseErrors)) {
        $failed = $true
        Write-Error -ErrorAction Continue ("{0}:{1}:{2}: {3}" -f
            $file.FullName,
            $parseError.Extent.StartLineNumber,
            $parseError.Extent.StartColumnNumber,
            $parseError.Message)
    }
}

if ($failed) {
    exit 1
}
Write-Host "PowerShell syntax validation passed for $($paths.Count) script(s)."
