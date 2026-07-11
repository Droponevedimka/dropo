param([switch]$Force)

$ErrorActionPreference = "Stop"
$ExpectedThumbprint = "C01E572C301010F06DC7F934A985691EE6C11096"
$CertificatePath = Join-Path $PSScriptRoot "dropo-pet-code-signing.cer"

if (-not (Test-Path -LiteralPath $CertificatePath -PathType Leaf)) {
    throw "Certificate not found next to this script: $CertificatePath"
}

$certificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($CertificatePath)
if (-not $certificate.Thumbprint.Equals($ExpectedThumbprint, [StringComparison]::OrdinalIgnoreCase)) {
    throw "Certificate fingerprint mismatch. Expected $ExpectedThumbprint, got $($certificate.Thumbprint)."
}

Write-Host "WARNING: this self-signed certificate trusts every executable signed by its private key." -ForegroundColor Yellow
Write-Host "Subject:    $($certificate.Subject)"
Write-Host "Thumbprint: $($certificate.Thumbprint)"
Write-Host "Scope:      current Windows user only"
if (-not $Force) {
    $answer = Read-Host "Type INSTALL to trust this pet-project certificate"
    if ($answer -cne "INSTALL") {
        Write-Host "Cancelled."
        exit 1
    }
}

foreach ($storeName in @("Root", "TrustedPublisher")) {
    $store = [Security.Cryptography.X509Certificates.X509Store]::new(
        $storeName,
        [Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser
    )
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
        if (-not @($store.Certificates | Where-Object Thumbprint -eq $ExpectedThumbprint).Count) {
            $store.Add($certificate)
        }
    } finally {
        $store.Close()
    }
}

Write-Host "dropo pet-project certificate is now trusted for the current user." -ForegroundColor Green
