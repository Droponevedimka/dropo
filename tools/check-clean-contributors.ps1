param(
    [string]$RevisionRange = ""
)

$ErrorActionPreference = "Stop"

$logArgs = @(
    "log",
    "--format=%H%nAuthor: %an <%ae>%nCommitter: %cn <%ce>%n%B%n---END---"
)

if ($RevisionRange.Trim()) {
    $logArgs += $RevisionRange.Trim()
} else {
    $logArgs += "HEAD"
}

$payload = & git @logArgs
if ($LASTEXITCODE -ne 0) {
    throw "git log failed while checking contributor metadata"
}

$text = ($payload -join "`n")
$patterns = @(
    '(?im)^\s*(Co-Authored-By|Authored-By|Committed-By|Generated-By|Reviewed-by|Acked-by):'
)

$matches = New-Object System.Collections.Generic.List[string]
foreach ($pattern in $patterns) {
    foreach ($match in [regex]::Matches($text, $pattern)) {
        $matches.Add($match.Value.Trim())
    }
}

if ($matches.Count -gt 0) {
    Write-Error ("Generated contributor metadata is not allowed:`n" + (($matches | Select-Object -Unique) -join "`n"))
    exit 1
}

Write-Host "Contributor metadata guard passed."
