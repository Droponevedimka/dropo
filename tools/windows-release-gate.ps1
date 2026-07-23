param(
    [Parameter(Mandatory = $true)][string]$InstallerPath,
    [Parameter(Mandatory = $true)][string]$PortablePath,
    [switch]$RequireDefender,
    [switch]$RequireSignature,
    [switch]$InstallSmoke
)

$ErrorActionPreference = "Stop"

function Resolve-MpCmdRun {
    $candidates = @(
        (Join-Path $env:ProgramFiles "Windows Defender\MpCmdRun.exe"),
        (Get-ChildItem "$env:ProgramData\Microsoft\Windows Defender\Platform" -Directory -ErrorAction SilentlyContinue |
            Sort-Object Name -Descending |
            ForEach-Object { Join-Path $_.FullName "MpCmdRun.exe" })
    )
    return $candidates | Where-Object { $_ -and (Test-Path -LiteralPath $_ -PathType Leaf) } | Select-Object -First 1
}

function Assert-PublicationSignaturePolicy {
    param([string]$Path)
    $signature = Get-AuthenticodeSignature -LiteralPath $Path
    if ($signature.SignerCertificate -and $signature.SignerCertificate.Subject -eq $signature.SignerCertificate.Issuer) {
        throw "Self-signed release artifact is forbidden: $Path"
    }
    if ($signature.Status -eq [System.Management.Automation.SignatureStatus]::Valid) {
        Write-Host "[GATE] Trusted signature: $($signature.SignerCertificate.Subject)" -ForegroundColor Green
        return
    }
    if ($RequireSignature) {
        throw "A publicly trusted Authenticode signature is required: $Path ($($signature.Status))"
    }
    if ($signature.Status -ne [System.Management.Automation.SignatureStatus]::NotSigned) {
        throw "Invalid Authenticode state: $Path ($($signature.Status))"
    }
    Write-Host "[GATE] Artifact is unsigned by publication policy." -ForegroundColor Yellow
}

function Add-MarkOfTheWeb {
    param([string]$Path, [string]$SourceUrl)
    $zone = "[ZoneTransfer]`r`nZoneId=3`r`nHostUrl=$SourceUrl`r`n"
    Set-Content -LiteralPath "$Path`:Zone.Identifier" -Value $zone -Encoding ASCII
    if (-not (Get-Item -LiteralPath $Path -Stream Zone.Identifier -ErrorAction SilentlyContinue)) {
        throw "Failed to apply Mark-of-the-Web: $Path"
    }
}

function Invoke-DefenderScan {
    param([string]$MpCmdRun, [string]$Path)
    & $MpCmdRun -Scan -ScanType 3 -File $Path -DisableRemediation
    if ($LASTEXITCODE -ne 0) {
        throw "Microsoft Defender rejected $Path (MpCmdRun exit $LASTEXITCODE)."
    }
}

function Invoke-WindowsInstallSmoke {
    param([string]$SetupPath, [string]$GateRoot)
    $installRoot = Join-Path $GateRoot "installed"
    $setupArgs = @(
        "/VERYSILENT",
        "/SUPPRESSMSGBOXES",
        "/NORESTART",
        "/CURRENTUSER",
        "/TASKS=autostart,backgroundcore",
        "/DIR=$installRoot"
    )
    foreach ($pass in 1..2) {
        $process = Start-Process -FilePath $SetupPath -ArgumentList $setupArgs -Wait -PassThru
        if ($process.ExitCode -ne 0) {
            throw "Installer smoke pass $pass failed with exit code $($process.ExitCode)."
        }
        foreach ($required in @(
            "dropo.exe",
            "install-mode.json",
            "resources\dropo-ui.exe",
            "resources\dropo-core.exe"
        )) {
            if (-not (Test-Path -LiteralPath (Join-Path $installRoot $required) -PathType Leaf)) {
                throw "Installed application is missing $required after pass $pass."
            }
        }
    }

    $runCommand = (Get-ItemProperty -LiteralPath "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "dropo" -ErrorAction SilentlyContinue).dropo
    if ([string]::IsNullOrWhiteSpace([string]$runCommand) -or $runCommand -notlike "*$installRoot*") {
        throw "Installer did not configure the selected per-user UI autostart entry."
    }
    & schtasks.exe /Query /TN "dropo-background-core" | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Installer did not configure the selected background-core task."
    }

    $uninstaller = Join-Path $installRoot "unins000.exe"
    if (-not (Test-Path -LiteralPath $uninstaller -PathType Leaf)) {
        throw "Installer did not create an uninstaller."
    }
    $process = Start-Process -FilePath $uninstaller -ArgumentList @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART") -Wait -PassThru
    if ($process.ExitCode -ne 0) {
        throw "Uninstaller smoke failed with exit code $($process.ExitCode)."
    }
    $runCommand = (Get-ItemProperty -LiteralPath "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "dropo" -ErrorAction SilentlyContinue).dropo
    if (-not [string]::IsNullOrWhiteSpace([string]$runCommand)) {
        throw "Uninstaller left the dropo UI autostart entry behind."
    }
    & schtasks.exe /Query /TN "dropo-background-core" 2>$null | Out-Null
    if ($LASTEXITCODE -eq 0) {
        throw "Uninstaller left the background-core task behind."
    }
    $global:LASTEXITCODE = 0
}

$installer = (Resolve-Path -LiteralPath $InstallerPath).Path
$portable = (Resolve-Path -LiteralPath $PortablePath).Path
Assert-PublicationSignaturePolicy -Path $installer

$mpCmdRun = Resolve-MpCmdRun
if (-not $mpCmdRun) {
    if ($RequireDefender) {
        throw "Microsoft Defender command-line scanner was not found."
    }
    Write-Host "[GATE] Defender is unavailable; MOTW/Defender scan skipped." -ForegroundColor Yellow
    exit 0
}

$status = Get-MpComputerStatus -ErrorAction SilentlyContinue
if ($RequireDefender -and (-not $status -or -not $status.AntivirusEnabled)) {
    throw "Microsoft Defender Antivirus is not enabled on this release-gate host."
}

$gateRoot = Join-Path $env:TEMP ("dropo-release-gate-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $gateRoot | Out-Null
try {
    $installerCopy = Join-Path $gateRoot (Split-Path -Leaf $installer)
    $portableCopy = Join-Path $gateRoot (Split-Path -Leaf $portable)
    Copy-Item -LiteralPath $installer -Destination $installerCopy
    Copy-Item -LiteralPath $portable -Destination $portableCopy
    Add-MarkOfTheWeb -Path $installerCopy -SourceUrl "https://downloads.droponevedimka.ru/$([IO.Path]::GetFileName($installerCopy))"
    Add-MarkOfTheWeb -Path $portableCopy -SourceUrl "https://downloads.droponevedimka.ru/$([IO.Path]::GetFileName($portableCopy))"

    Write-Host "[GATE] Scanning Internet-marked installer..." -ForegroundColor Cyan
    Invoke-DefenderScan -MpCmdRun $mpCmdRun -Path $installerCopy
    Write-Host "[GATE] Scanning Internet-marked portable archive..." -ForegroundColor Cyan
    Invoke-DefenderScan -MpCmdRun $mpCmdRun -Path $portableCopy

    $extract = Join-Path $gateRoot "portable"
    Expand-Archive -LiteralPath $portableCopy -DestinationPath $extract -Force
    if (Test-Path -LiteralPath (Join-Path $extract "install-mode.json")) {
        throw "Portable archive contains the installer mode marker."
    }
    foreach ($required in @(
        "dropo.exe",
        "resources\dropo-ui.exe",
        "resources\dropo-core.exe",
        "resources\runtime-manifest.json",
        "resources\bin\WinDivert64.sys"
    )) {
        if (-not (Test-Path -LiteralPath (Join-Path $extract $required) -PathType Leaf)) {
            throw "Portable archive is missing $required"
        }
    }
    Write-Host "[GATE] Scanning extracted portable payload..." -ForegroundColor Cyan
    Invoke-DefenderScan -MpCmdRun $mpCmdRun -Path $extract
    if ($InstallSmoke) {
        Write-Host "[GATE] Exercising fresh install, in-place upgrade and uninstall..." -ForegroundColor Cyan
        Invoke-WindowsInstallSmoke -SetupPath $installerCopy -GateRoot $gateRoot
    }
} finally {
    Remove-Item -LiteralPath $gateRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host "[GATE] Windows MOTW and Defender release gate passed." -ForegroundColor Green
