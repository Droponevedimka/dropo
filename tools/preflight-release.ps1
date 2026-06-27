# dropo release preflight.
# Runs frontend, backend, visual, artifact and optional runtime checks before publishing a build.

param(
    [switch]$WithVisual,
    [switch]$WithNetwork,
    [switch]$WithSubscription,
    [switch]$Build,
    [switch]$SkipInstall,
    [switch]$InstallBrowsers,
    [string]$SubscriptionUrl = $env:DROPO_TEST_SUBSCRIPTION_URL,
    [string]$WireGuardConfigPath,
    [string]$ReleaseFolder
)

$ErrorActionPreference = "Stop"
$ScriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$RepoRoot = Split-Path -Parent $ScriptRoot
$AppDir = Join-Path $RepoRoot "app"
$FrontendDir = Join-Path $AppDir "frontend"
$ReleaseRoot = Join-Path $RepoRoot "release"

function Invoke-Step {
    param(
        [string]$Name,
        [scriptblock]$Action
    )

    Write-Host ""
    Write-Host "==> $Name" -ForegroundColor Cyan
    $start = Get-Date
    try {
        & $Action
        $elapsed = [math]::Round(((Get-Date) - $start).TotalSeconds, 1)
        Write-Host "[OK] $Name (${elapsed}s)" -ForegroundColor Green
    } catch {
        Write-Host "[FAIL] $Name" -ForegroundColor Red
        throw
    }
}

function Invoke-External {
    param(
        [string]$FilePath,
        [string[]]$Arguments,
        [string]$WorkingDirectory
    )

    Push-Location $WorkingDirectory
    try {
        & $FilePath @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "$FilePath $($Arguments -join ' ') failed with exit code $LASTEXITCODE"
        }
    } finally {
        Pop-Location
    }
}

function Test-IsAdmin {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Get-VersionInfo {
    return Get-Content (Join-Path $RepoRoot "version.json") -Raw | ConvertFrom-Json
}

function Get-LatestReleaseFolder {
    param([string]$Version)

    if ($ReleaseFolder) {
        $resolved = Resolve-Path $ReleaseFolder -ErrorAction Stop
        return $resolved.Path
    }

    $candidate = Get-ChildItem -Path $ReleaseRoot -Directory -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -match "^dropo-$([regex]::Escape($Version))-[0-9a-f]+$" } |
        Sort-Object LastWriteTime -Descending |
        Select-Object -First 1

    if (-not $candidate) {
        throw "No release folder found for version $Version. Expected release/dropo-$Version-<hash>"
    }
    return $candidate.FullName
}

function Assert-FileExists {
    param([string]$Path)
    if (-not (Test-Path $Path -PathType Leaf)) {
        throw "Required file is missing: $Path"
    }
}

function Assert-NoRuntimeFiles {
    param([string]$Folder)

    $forbidden = @(
        "resources\active_config.json",
        "resources\settings.json",
        "resources\cache.db",
        "resources\xray_config.json"
    )

    foreach ($relative in $forbidden) {
        $path = Join-Path $Folder $relative
        if (Test-Path $path) {
            throw "Release contains runtime file: $relative"
        }
    }
}

function Invoke-ArtifactValidation {
    $versionInfo = Get-VersionInfo
    $version = $versionInfo.version
    $folder = Get-LatestReleaseFolder -Version $version
    $folderName = Split-Path $folder -Leaf

    if ($folderName -notmatch "^dropo-$([regex]::Escape($version))-[0-9a-f]+$") {
        throw "Release folder name must include app, version and build hash: dropo-$version-<hash>. Got: $folderName"
    }

    $zipPath = Join-Path $ReleaseRoot "$folderName.zip"

    Write-Host "Release folder: $folder" -ForegroundColor Gray
    Write-Host "Release zip:    $zipPath" -ForegroundColor Gray

    Assert-FileExists (Join-Path $folder "dropo.exe")
    Assert-FileExists (Join-Path $folder "bin\sing-box.exe")
    Assert-FileExists (Join-Path $folder "bin\xray.exe")
    Assert-FileExists (Join-Path $folder "bin\ciadpi.exe")
    Assert-FileExists (Join-Path $folder "bin\winws.exe")
    Assert-FileExists (Join-Path $folder "bin\cygwin1.dll")
    Assert-FileExists (Join-Path $folder "bin\WinDivert.dll")
    Assert-FileExists (Join-Path $folder "bin\WinDivert64.sys")
    Assert-FileExists (Join-Path $folder "bin\quic_initial_dbankcloud_ru.bin")
    Assert-FileExists (Join-Path $folder "bin\discord-ip-discovery-without-port.bin")
    Assert-FileExists (Join-Path $folder "bin\stun.bin")
    Assert-FileExists (Join-Path $folder "bin\windivert_part.discord_media.txt")
    Assert-FileExists (Join-Path $folder "bin\windivert_part.stun.txt")
    Assert-FileExists (Join-Path $folder "bin\wireguard.exe")
    Assert-FileExists (Join-Path $folder "bin\wg.exe")
    Assert-FileExists (Join-Path $folder "bin\wintun.dll")
    Assert-FileExists (Join-Path $folder "bin\filters\refilter_domains.srs")
    Assert-FileExists (Join-Path $folder "bin\filters\refilter_ips.srs")
    Assert-FileExists (Join-Path $folder "bin\filters\discord_ips.srs")
    Assert-FileExists (Join-Path $folder "bin\filters\version.json")
    Assert-FileExists (Join-Path $folder "resources\template.json")
    Assert-FileExists (Join-Path $folder "licenses\sing-box-LICENSE.txt")
    Assert-FileExists (Join-Path $folder "licenses\xray-LICENSE.txt")
    Assert-FileExists (Join-Path $folder "licenses\wireguard-windows-LICENSE.txt")
    Assert-FileExists (Join-Path $folder "licenses\byedpi-LICENSE.txt")
    Assert-FileExists (Join-Path $folder "licenses\zapret-LICENSE.txt")
    Assert-FileExists $zipPath
    Assert-NoRuntimeFiles $folder

    $manifestPath = Join-Path $AppDir "build\windows\wails.exe.manifest"
    $manifest = Get-Content $manifestPath -Raw
    if ($manifest -notmatch 'requestedExecutionLevel level="requireAdministrator"') {
        throw "Windows manifest does not request administrator privileges"
    }

    $wailsConfig = Get-Content (Join-Path $AppDir "wails.json") -Raw | ConvertFrom-Json
    if ($wailsConfig.name -ne "dropo" -or $wailsConfig.outputfilename -ne "dropo" -or $wailsConfig.info.productName -ne "dropo") {
        throw "Wails metadata is not branded as dropo"
    }

    $oldBrandHits = Select-String -Path @(
        (Join-Path $AppDir "frontend\index.html"),
        (Join-Path $AppDir "wails.json")
    ) -Pattern "Kampus VPN","kampus vpn" -SimpleMatch -ErrorAction SilentlyContinue
    if ($oldBrandHits) {
        throw "Old visible brand text remains: $($oldBrandHits[0].Path):$($oldBrandHits[0].LineNumber)"
    }

    $singBox = Join-Path $folder "bin\sing-box.exe"
    $xray = Join-Path $folder "bin\xray.exe"
    $singBoxOutput = & $singBox version 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "sing-box version check failed: $singBoxOutput"
    }
    Write-Host ($singBoxOutput | Select-Object -First 1) -ForegroundColor Gray

    $xrayOutput = & $xray version 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "xray version check failed: $xrayOutput"
    }
    Write-Host ($xrayOutput | Select-Object -First 1) -ForegroundColor Gray

    $winws = Join-Path $folder "bin\winws.exe"
    $winwsOutput = & $winws --version 2>&1
    if ($LASTEXITCODE -ne 0 -and $winwsOutput -notmatch "winws") {
        throw "winws version check failed: $winwsOutput"
    }
    Write-Host ($winwsOutput | Select-Object -First 1) -ForegroundColor Gray

    $tempDir = Join-Path $env:TEMP ("dropo-preflight-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $tempDir | Out-Null
    try {
        Expand-Archive -Path $zipPath -DestinationPath $tempDir -Force
        Assert-FileExists (Join-Path $tempDir "dropo.exe")
        Assert-FileExists (Join-Path $tempDir "bin\xray.exe")
        Assert-FileExists (Join-Path $tempDir "bin\winws.exe")
        Assert-NoRuntimeFiles $tempDir
    } finally {
        Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }

    return $folder
}

function Invoke-RuntimeSmoke {
    param([string]$Folder)

    $runtimeFolder = New-RuntimeReleaseCopy -Folder $Folder
    $env:DROPO_TEST_FREE_ACCESS_BASE = $runtimeFolder
    try {
        Invoke-External "go" @("test", "./...", "-run", "TestManualFreeAccessRuntimeFromEnv", "-count=1", "-v") $AppDir
    } finally {
        Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE -ErrorAction SilentlyContinue
        Remove-Item -Path $runtimeFolder -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-SubscriptionSmoke {
    param([string]$Folder)

    if (-not $SubscriptionUrl) {
        throw "SubscriptionUrl is required when -WithSubscription is set"
    }

    $runtimeFolder = New-RuntimeReleaseCopy -Folder $Folder
    $env:DROPO_TEST_FREE_ACCESS_BASE = $runtimeFolder
    $env:DROPO_TEST_SUBSCRIPTION_URL = $SubscriptionUrl
    try {
        Invoke-External "go" @("test", "./...", "-run", "TestManualSubscriptionRuntimeFromEnv", "-count=1", "-v") $AppDir
    } finally {
        Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE -ErrorAction SilentlyContinue
        Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL -ErrorAction SilentlyContinue
        Remove-Item -Path $runtimeFolder -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function New-RuntimeReleaseCopy {
    param([string]$Folder)

    $runtimeFolder = Join-Path $env:TEMP ("dropo-runtime-" + [guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $runtimeFolder | Out-Null
    Copy-Item -Path (Join-Path $Folder "*") -Destination $runtimeFolder -Recurse -Force
    return $runtimeFolder
}

function Wait-TextInFile {
    param(
        [string]$Path,
        [string[]]$RequiredPatterns,
        [int]$TimeoutSeconds = 180
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $seen = @{}
    foreach ($pattern in $RequiredPatterns) {
        $seen[$pattern] = $false
    }

    do {
        if (Test-Path $Path -PathType Leaf) {
            $text = Get-Content $Path -Raw -ErrorAction SilentlyContinue
            foreach ($pattern in $RequiredPatterns) {
                if (-not $seen[$pattern] -and $text -match $pattern) {
                    $seen[$pattern] = $true
                    Write-Host "  log marker: $pattern" -ForegroundColor DarkGray
                }
            }

            $missingCount = @($seen.GetEnumerator() | Where-Object { -not $_.Value }).Count
            if ($missingCount -eq 0) {
                return
            }
        }

        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    $missing = @($seen.GetEnumerator() | Where-Object { -not $_.Value } | ForEach-Object { $_.Key })
    throw "Timed out waiting for application initialization log markers in $Path. Missing: $($missing -join ', ')"
}

function Wait-TextInLatestFile {
    param(
        [string]$Directory,
        [string]$Filter,
        [string[]]$RequiredPatterns,
        [int]$TimeoutSeconds = 180
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $latest = Get-ChildItem -Path $Directory -Filter $Filter -File -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending |
            Select-Object -First 1
        if ($latest) {
            $remaining = [int][Math]::Max(1, ($deadline - (Get-Date)).TotalSeconds)
            Wait-TextInFile -Path $latest.FullName -RequiredPatterns $RequiredPatterns -TimeoutSeconds $remaining
            return
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    throw "Timed out waiting for log file $Filter in $Directory"
}

function Wait-ProcessMainWindow {
    param(
        [System.Diagnostics.Process]$Process,
        [int]$TimeoutSeconds = 30
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $Process.Refresh()
        if ($Process.HasExited) {
            throw "dropo exited before its main window appeared with code $($Process.ExitCode)"
        }
        if (-not $Process.HasExited -and $Process.MainWindowHandle -ne 0) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)

    throw "dropo main window did not appear"
}

function Wait-ProcessExit {
    param(
        [System.Diagnostics.Process]$Process,
        [int]$TimeoutSeconds = 15
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $Process.Refresh()
        if ($Process.HasExited) {
            return
        }
        Start-Sleep -Milliseconds 250
    } while ((Get-Date) -lt $deadline)

    throw "dropo did not exit after tray menu command"
}

function Assert-NoRuntimeProcesses {
    param(
        [string]$RuntimeFolder,
        [int]$TimeoutSeconds = 8
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    $normalized = (Resolve-Path $RuntimeFolder -ErrorAction SilentlyContinue).Path
    if (-not $normalized) {
        $normalized = $RuntimeFolder
    }
    $prefix = ($normalized.TrimEnd('\') + '\').ToLowerInvariant()

    do {
        $leftovers = @(
            Get-CimInstance Win32_Process -ErrorAction SilentlyContinue |
                Where-Object {
                    $_.ExecutablePath -and $_.ExecutablePath.ToLowerInvariant().StartsWith($prefix)
                } |
                Select-Object ProcessId, Name, ExecutablePath
        )
        if ($leftovers.Count -eq 0) {
            return
        }
        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    $summary = $leftovers | ForEach-Object { "$($_.Name)#$($_.ProcessId) $($_.ExecutablePath)" }
    throw "Runtime process leftovers after app exit: $($summary -join '; ')"
}

function Assert-DropoProcessAlive {
    param(
        [System.Diagnostics.Process]$Process,
        [string]$ExpectedPath,
        [string]$Stage
    )

    if (-not $Process) {
        throw "dropo process handle is missing at stage: $Stage"
    }

    $Process.Refresh()
    if ($Process.HasExited) {
        throw "dropo exited unexpectedly at stage '$Stage' with code $($Process.ExitCode). Expected path: $ExpectedPath"
    }

    try {
        if ($ExpectedPath -and $Process.Path -and ((Resolve-Path $Process.Path).Path -ne (Resolve-Path $ExpectedPath).Path)) {
            throw "dropo PID $($Process.Id) points to another executable at stage '$Stage': $($Process.Path)"
        }
    } catch {
        if ($_.Exception.Message -like "dropo PID*") {
            throw
        }
    }
}

function Initialize-UiAutomation {
    Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
    Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
    Add-Type -AssemblyName WindowsBase -ErrorAction Stop
    Add-Type -AssemblyName System.Windows.Forms -ErrorAction Stop
    Add-Type -AssemblyName System.Drawing -ErrorAction Stop

    if (-not ("NativeMouse" -as [type])) {
        Add-Type @"
using System;
using System.Runtime.InteropServices;

public static class NativeMouse {
    [DllImport("user32.dll")]
    public static extern void mouse_event(uint dwFlags, uint dx, uint dy, uint dwData, UIntPtr dwExtraInfo);
}
"@
    }
}

function Get-AutomationElementClickPoint {
    param([System.Windows.Automation.AutomationElement]$Element)

    try {
        $rect = $Element.Current.BoundingRectangle
        if ($rect.Width -gt 0 -and $rect.Height -gt 0) {
            return New-Object System.Drawing.Point(
                [int]($rect.X + ($rect.Width / 2)),
                [int]($rect.Y + ($rect.Height / 2))
            )
        }
    } catch {
    }

    try {
        $point = New-Object System.Windows.Point
        if ($Element.TryGetClickablePoint([ref]$point)) {
            return New-Object System.Drawing.Point([int]$point.X, [int]$point.Y)
        }
    } catch {
    }

    return $null
}

function Get-ShellAutomationRoots {
    $root = [System.Windows.Automation.AutomationElement]::RootElement
    $roots = New-Object System.Collections.Generic.List[System.Windows.Automation.AutomationElement]
    $roots.Add($root)

    $children = $root.FindAll(
        [System.Windows.Automation.TreeScope]::Children,
        [System.Windows.Automation.Condition]::TrueCondition
    )

    for ($i = 0; $i -lt $children.Count; $i++) {
        try {
            $element = $children.Item($i)
            $className = $element.Current.ClassName
            if ($className -match '^(Shell_TrayWnd|NotifyIconOverflowWindow|Shell_SecondaryTrayWnd)$') {
                $roots.Add($element)
            }
        } catch {
        }
    }

    return $roots
}

function Find-AutomationElementByName {
    param(
        [string]$NamePattern,
        [switch]$SearchAll
    )

    if ($SearchAll) {
        $roots = @([System.Windows.Automation.AutomationElement]::RootElement)
    } else {
        $roots = Get-ShellAutomationRoots
    }

    foreach ($root in $roots) {
        try {
            $elements = $root.FindAll(
                [System.Windows.Automation.TreeScope]::Subtree,
                [System.Windows.Automation.Condition]::TrueCondition
            )
        } catch {
            continue
        }
        for ($i = 0; $i -lt $elements.Count; $i++) {
            try {
                $element = $elements.Item($i)
                $name = $element.Current.Name
                if ($name -and $name -match $NamePattern) {
                    return $element
                }
            } catch {
            }
        }
    }

    return $null
}

function Invoke-AutomationElement {
    param([System.Windows.Automation.AutomationElement]$Element)

    $pattern = $null
    if ($Element.TryGetCurrentPattern([System.Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        ([System.Windows.Automation.InvokePattern]$pattern).Invoke()
        return $true
    }

    return $false
}

function Invoke-HiddenTrayIconsFlyout {
    $button = Find-AutomationElementByName -NamePattern '(Show hidden icons|Hidden icons)'
    if ($button) {
        [void](Invoke-AutomationElement -Element $button)
        Start-Sleep -Milliseconds 500
    }
}

function Find-DropoTrayIcon {
    param(
        [string]$NamePattern = '(?i)^dropo(\b|\s|-)',
        [int]$TimeoutSeconds = 30
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    do {
        $element = Find-AutomationElementByName -NamePattern $NamePattern
        if ($element -and (Get-AutomationElementClickPoint -Element $element)) {
            return $element
        }

        Invoke-HiddenTrayIconsFlyout
        $element = Find-AutomationElementByName -NamePattern $NamePattern
        if ($element -and (Get-AutomationElementClickPoint -Element $element)) {
            return $element
        }

        Start-Sleep -Milliseconds 500
    } while ((Get-Date) -lt $deadline)

    throw "dropo tray icon was not found in the notification area"
}

function Invoke-RightClick {
    param([System.Windows.Automation.AutomationElement]$Element)

    $point = Get-AutomationElementClickPoint -Element $Element
    if (-not $point) {
        throw "Tray icon has no clickable point"
    }

    [System.Windows.Forms.Cursor]::Position = $point
    Start-Sleep -Milliseconds 100
    [NativeMouse]::mouse_event(0x0008, 0, 0, 0, [UIntPtr]::Zero)
    [NativeMouse]::mouse_event(0x0010, 0, 0, 0, [UIntPtr]::Zero)
}

function Find-LastVisibleContextMenuItem {
    $root = [System.Windows.Automation.AutomationElement]::RootElement
    $elements = $root.FindAll(
        [System.Windows.Automation.TreeScope]::Subtree,
        [System.Windows.Automation.Condition]::TrueCondition
    )

    $items = @()
    for ($i = 0; $i -lt $elements.Count; $i++) {
        try {
            $element = $elements.Item($i)
            $typeName = $element.Current.ControlType.ProgrammaticName
            $rect = $element.Current.BoundingRectangle
            if ($typeName -eq 'ControlType.MenuItem' -and $rect.Width -gt 0 -and $rect.Height -gt 0) {
                $items += [PSCustomObject]@{
                    Element = $element
                    Bottom  = $rect.Y + $rect.Height
                }
            }
        } catch {
        }
    }

    return $items | Sort-Object Bottom -Descending | Select-Object -ExpandProperty Element -First 1
}

function Invoke-DropoTrayExit {
    param([string]$TrayNamePattern = '(?i)^dropo(\b|\s|-)')

    Initialize-UiAutomation

    $trayIcon = Find-DropoTrayIcon -NamePattern $TrayNamePattern
    Write-Host "  tray icon: $($trayIcon.Current.Name)" -ForegroundColor DarkGray
    Invoke-RightClick -Element $trayIcon
    Start-Sleep -Milliseconds 500

    # The tray menu is localized and some Windows builds expose systray menu
    # items poorly through UI Automation. Keyboard selection is more stable:
    # after opening the menu, Up selects the last item ("Exit"), Enter invokes it.
    [System.Windows.Forms.SendKeys]::SendWait("{UP}")
    Start-Sleep -Milliseconds 100
    [System.Windows.Forms.SendKeys]::SendWait("{ENTER}")
}

function Invoke-TrayLifecycleSmoke {
    param([string]$Folder)

    $existing = @(Get-Process -Name "dropo" -ErrorAction SilentlyContinue)
    if ($existing.Count -gt 0) {
        throw "Close existing dropo processes before running tray lifecycle smoke: $($existing.Id -join ', ')"
    }

    $runtimeFolder = New-RuntimeReleaseCopy -Folder $Folder
    $exePath = Join-Path $runtimeFolder "dropo.exe"
    $tempLogDir = Join-Path $env:TEMP "dropo"
    $process = $null
    $trayLabel = "dropo-smoke-" + [guid]::NewGuid().ToString("N").Substring(0, 8)
    $trayNamePattern = [regex]::Escape($trayLabel)
    $previousTrayLabel = $env:DROPO_TRAY_LABEL

    Remove-Item -Path (Join-Path $tempLogDir "dropo-*.log") -Force -ErrorAction SilentlyContinue

    try {
        $env:DROPO_TRAY_LABEL = $trayLabel
        $process = Start-Process -FilePath $exePath -WorkingDirectory $runtimeFolder -PassThru
        Assert-DropoProcessAlive -Process $process -ExpectedPath $exePath -Stage "after Start-Process"
        Wait-ProcessMainWindow -Process $process
        Assert-DropoProcessAlive -Process $process -ExpectedPath $exePath -Stage "after main window"
        Wait-TextInLatestFile -Directory $tempLogDir -Filter "dropo-*.log" -RequiredPatterns @(
            'Systray ready',
            [regex]::Escape($runtimeFolder),
            'Application initialized',
            'Routing filters (bundled|background check (finished|skipped))'
        ) -TimeoutSeconds 180
        Assert-DropoProcessAlive -Process $process -ExpectedPath $exePath -Stage "after initialization markers"

        $process.Refresh()
        if (-not $process.CloseMainWindow()) {
            throw "CloseMainWindow returned false"
        }

        Start-Sleep -Seconds 2
        Assert-DropoProcessAlive -Process $process -ExpectedPath $exePath -Stage "after window close"

        Invoke-DropoTrayExit -TrayNamePattern $trayNamePattern
        Wait-ProcessExit -Process $process
        Assert-NoRuntimeProcesses -RuntimeFolder $runtimeFolder
    } finally {
        if ($null -eq $previousTrayLabel) {
            Remove-Item Env:\DROPO_TRAY_LABEL -ErrorAction SilentlyContinue
        } else {
            $env:DROPO_TRAY_LABEL = $previousTrayLabel
        }
        if ($process -and -not $process.HasExited) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        }
        Remove-Item -Path $runtimeFolder -Recurse -Force -ErrorAction SilentlyContinue
    }
}

function Invoke-WireGuardSmoke {
    if (-not $WireGuardConfigPath) {
        return
    }
    $resolved = Resolve-Path $WireGuardConfigPath -ErrorAction Stop
    $env:DROPO_TEST_WG_CONFIG = Get-Content $resolved.Path -Raw
    try {
        Invoke-External "go" @("test", "./...", "-run", "TestManualWireGuardConfigFromEnv", "-count=1", "-v") $AppDir
    } finally {
        Remove-Item Env:\DROPO_TEST_WG_CONFIG -ErrorAction SilentlyContinue
    }
}

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "   dropo Release Preflight" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

if (-not $SkipInstall) {
    Invoke-Step "Install frontend dependencies" {
        $packageLock = Join-Path $FrontendDir "package-lock.json"
        if (Test-Path $packageLock -PathType Leaf) {
            Invoke-External "npm" @("ci") $FrontendDir
        } else {
            Invoke-External "npm" @("install") $FrontendDir
        }
    }
}

Invoke-Step "Build frontend" {
    Invoke-External "npm" @("run", "build") $FrontendDir
}

if ($WithVisual -and $InstallBrowsers) {
    Invoke-Step "Install Playwright Chromium" {
        Invoke-External "npx" @("playwright", "install", "chromium") $FrontendDir
    }
}

if ($WithVisual) {
    Invoke-Step "Run visual click tests" {
        Invoke-External "npm" @("run", "test:e2e") $FrontendDir
    }
}

Invoke-Step "Run Go tests" {
    $savedFreeAccessBase = $env:DROPO_TEST_FREE_ACCESS_BASE
    $savedSubscriptionUrl = $env:DROPO_TEST_SUBSCRIPTION_URL
    $savedWireGuardConfig = $env:DROPO_TEST_WG_CONFIG
    Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE -ErrorAction SilentlyContinue
    Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL -ErrorAction SilentlyContinue
    Remove-Item Env:\DROPO_TEST_WG_CONFIG -ErrorAction SilentlyContinue
    try {
        Invoke-External "go" @("test", "./...") $AppDir
    } finally {
        if ($null -ne $savedFreeAccessBase) { $env:DROPO_TEST_FREE_ACCESS_BASE = $savedFreeAccessBase }
        if ($null -ne $savedSubscriptionUrl) { $env:DROPO_TEST_SUBSCRIPTION_URL = $savedSubscriptionUrl }
        if ($null -ne $savedWireGuardConfig) { $env:DROPO_TEST_WG_CONFIG = $savedWireGuardConfig }
    }
}

Invoke-Step "Optional WireGuard config smoke" {
    Invoke-WireGuardSmoke
}

if ($Build) {
    Invoke-Step "Build release" {
        Push-Location $RepoRoot
        try {
            & (Join-Path $RepoRoot "build.ps1") -All
            if ($LASTEXITCODE -ne 0) {
                throw "build.ps1 -All failed with exit code $LASTEXITCODE"
            }
        } finally {
            Pop-Location
        }
    }
}

$releaseFolderForRuntime = $null
Invoke-Step "Validate release artifact" {
    $script:releaseFolderForRuntime = Invoke-ArtifactValidation
}

Invoke-Step "Check administrator privileges for app lifecycle smoke" {
    if (-not (Test-IsAdmin)) {
        throw "App lifecycle smoke requires an elevated PowerShell session"
    }
}

Invoke-Step "Run app tray lifecycle smoke" {
    Invoke-TrayLifecycleSmoke -Folder $releaseFolderForRuntime
}

if ($WithNetwork) {
    Invoke-Step "Run free-access runtime smoke" {
        Invoke-RuntimeSmoke -Folder $releaseFolderForRuntime
    }
}

if ($WithSubscription) {
    Invoke-Step "Run subscription/xHTTP runtime smoke" {
        Invoke-SubscriptionSmoke -Folder $releaseFolderForRuntime
    }
}

Write-Host ""
Write-Host "[SUCCESS] dropo preflight passed" -ForegroundColor Green
Write-Host "Release folder: $releaseFolderForRuntime" -ForegroundColor White
$global:LASTEXITCODE = 0
