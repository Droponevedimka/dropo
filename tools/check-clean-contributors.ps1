param(
    [string]$RevisionRange = "",
    [string]$AllowedName = "Droponevedimka",
    [string]$AllowedEmail = "34841931+Droponevedimka@users.noreply.github.com"
)

$ErrorActionPreference = "Stop"

$revision = if ($RevisionRange.Trim()) { $RevisionRange.Trim() } else { "HEAD" }
$commitIds = @(& git rev-list $revision)
if ($LASTEXITCODE -ne 0 -or $commitIds.Count -eq 0) {
    throw "git rev-list failed while checking contributor metadata: $revision"
}

$trailerPattern = '(?im)^\s*(Co-Authored-By|Authored-By|Committed-By|Generated-By|Assisted-By|AI-Generated-By|Reviewed-By|Acked-By|Signed-off-by|Tested-By|Suggested-By|Claude-Session):'
$identityFailures = New-Object System.Collections.Generic.List[string]
$trailerFailures = New-Object System.Collections.Generic.List[string]

foreach ($commitId in $commitIds) {
    $metadata = @(& git show -s --format="%an%n%ae%n%cn%n%ce%n%B" $commitId)
    if ($LASTEXITCODE -ne 0 -or $metadata.Count -lt 4) {
        throw "git show failed while checking contributor metadata: $commitId"
    }

    $authorName = $metadata[0]
    $authorEmail = $metadata[1]
    $committerName = $metadata[2]
    $committerEmail = $metadata[3]
    if ($authorName -cne $AllowedName -or $authorEmail -cne $AllowedEmail -or
        $committerName -cne $AllowedName -or $committerEmail -cne $AllowedEmail) {
        $identityFailures.Add(
            "$commitId author=$authorName <$authorEmail>; committer=$committerName <$committerEmail>"
        )
    }

    $message = ($metadata | Select-Object -Skip 4) -join "`n"
    foreach ($match in [regex]::Matches($message, $trailerPattern)) {
        $trailerFailures.Add("$commitId $($match.Value.Trim())")
    }
}

if ($identityFailures.Count -gt 0) {
    Write-Error (
        "Only $AllowedName <$AllowedEmail> may author and commit repository history:`n" +
        ($identityFailures -join "`n")
    )
    exit 1
}

if ($trailerFailures.Count -gt 0) {
    Write-Error (
        "Contributor and AI attribution metadata is not allowed:`n" +
        (($trailerFailures | Select-Object -Unique) -join "`n")
    )
    exit 1
}

Write-Host "Contributor metadata guard passed for $($commitIds.Count) commit(s)."
