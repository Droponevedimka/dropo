# Platform Architecture

dropo is currently production-ready on Windows. The codebase is now structured so
desktop Linux/macOS and future mobile clients can be added without mixing their
OS-specific runtime, updater, and dependency bundles into the Windows path.

## Current Targets

| Target | Status | App asset | Dependencies asset | Notes |
| --- | --- | --- | --- | --- |
| Windows x64 | Production | `dropo-Windows-Portable-x64.zip` | `dropo-Windows-Dependencies-x64.zip` | Full runtime: Wails, sing-box, WireGuard, winws/WinDivert, Xray, tg-ws-proxy. |
| Linux x64 | Prepared | `dropo-Linux-x64.AppImage` | `dropo-Linux-Dependencies-x64.zip` | Compile contract exists; runtime/network engine still needs implementation and privilege model. |
| macOS arm64 | Prepared | `dropo-macOS-arm64.dmg` | `dropo-macOS-Dependencies-arm64.zip` | Compile contract exists; signing/notarization and network permissions still need implementation. |
| Android | Future mobile shell | `dropo-Android-universal.apk` | bundled/native | Requires Android `VpnService` or a dedicated mobile shell. |
| iOS | Future mobile shell | `dropo-iOS.ipa` | bundled/native | Requires Network Extension/TestFlight/App Store flow. |

## Boundaries

- Common app logic stays in the root `app` package: profiles, subscriptions,
  config building, update metadata, storage, traffic stats, dependency
  bootstrap, and frontend API contracts.
- Platform-specific code uses build tags:
  - `*_windows.go` for Windows registry, single-instance mutex, tray, Wails
    Windows options, process job objects, and WinDivert cleanup.
  - `*_other.go` for safe no-op placeholders until Linux/macOS adapters are
    implemented.
- Dependency readiness is platform-driven via `PlatformTarget`:
  Windows requires `sing-box.exe`, `winws.exe`, and `WinDivert.dll`; Linux/macOS
  currently require `sing-box` as their first dependency-contract placeholder.
- Self-update is enabled only for Windows until Linux/macOS installers are
  implemented and tested. Mobile updates must go through platform store/update
  channels.

## Release Rules

1. Windows release remains the baseline and must keep passing:
   `go test ./...` and `./build.ps1 -AppOnly`.
2. Cross-platform compile gates must pass before adding a platform:
   `GOOS=linux GOARCH=amd64 go test -c ./...` and
   `GOOS=darwin GOARCH=arm64 go test -c ./...`.
3. A new platform must define:
   - app asset name;
   - dependency asset name or bundled/mobile strategy;
   - required dependency files;
   - updater strategy;
   - privilege model for TUN/VPN operations;
   - CI build job and release asset upload.
4. Do not reuse Windows dependencies on other platforms. Each platform owns its
   dependency archive and checksum.

## Next Implementation Order

1. Linux desktop MVP: Wails build, `sing-box` dependency archive, AppImage or
   `.deb`, privilege/capability strategy for TUN.
2. macOS desktop MVP: Wails build, `.dmg`, signing/notarization, app support
   directory, network permission model.
3. Mobile feasibility prototype: shared config/subscription core plus native
   VPN shell for Android/iOS. Treat this as a new app shell, not a Wails v2
   packaging variant.
