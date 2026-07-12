$ErrorActionPreference = "Stop"
$Thumbprint = "C01E572C301010F06DC7F934A985691EE6C11096"

foreach ($storeName in @("Root", "TrustedPublisher")) {
    $store = [Security.Cryptography.X509Certificates.X509Store]::new(
        $storeName,
        [Security.Cryptography.X509Certificates.StoreLocation]::CurrentUser
    )
    try {
        $store.Open([Security.Cryptography.X509Certificates.OpenFlags]::ReadWrite)
        @($store.Certificates | Where-Object Thumbprint -eq $Thumbprint) |
            ForEach-Object { $store.Remove($_) }
    } finally {
        $store.Close()
    }
}

Write-Host "dropo pet-project certificate was removed from the current user's certificate stores." -ForegroundColor Green
