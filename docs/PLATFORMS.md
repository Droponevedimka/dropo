# Platform Architecture

dropo is currently production-ready on Windows. The codebase is now structured so
desktop Linux/macOS and future mobile clients can be added without mixing their
OS-specific runtime, updater, and dependency bundles into the Windows path.

## Current Targets

| Target | Status | App asset | Dependencies asset | Notes |
| --- | --- | --- | --- | --- |
| Windows x64 | Production | `dropo-Windows-Portable-x64.zip` | `dropo-Windows-Dependencies-x64.zip` | Full runtime: Flutter UI, Go-core bridge, sing-box, WireGuard, winws/WinDivert, Xray, tg-ws-proxy. |
| Linux x64 | Prepared | `dropo-Linux-x64.AppImage` | `dropo-Linux-Dependencies-x64.zip` | Compile contract exists; runtime/network engine still needs implementation and privilege model. |
| macOS arm64 | Prepared | `dropo-macOS-arm64.dmg` | `dropo-macOS-Dependencies-arm64.zip` | Compile contract exists; signing/notarization and network permissions still need implementation. |
| Android | Future mobile shell | `dropo-Android-universal.apk` | bundled/native | Requires Android `VpnService` or a dedicated mobile shell. |
| iOS | Future mobile shell | `dropo-iOS.ipa` | bundled/native | Requires Network Extension/TestFlight/App Store flow. |

## Single Core

There is exactly one Go core: the `app` package, shipped as `dropo-core.exe`
(the Flutter runner `dropo.exe` is built from `launcher/`). An earlier orphaned
`core/` module (`dropo/core`) that duplicated routing logic and was wired into
nothing has been removed to keep a single source of truth.

## Interception Engine

The transparent traffic-interception layer (the "Deep" engine that desyncs or
redirects packets without a full TUN) sits behind the `InterceptionEngine`
interface in `app/core_interception_engine.go`. The platform-agnostic core
(per-service strategy selection in `core_service_engine.go`, route probing,
network-mode resolution) talks only to this interface, so adding an OS means
providing one adapter file instead of scattering `runtime.GOOS` branches.

| OS | Engine kind | Adapter file | Status |
| --- | --- | --- | --- |
| Windows | winws + WinDivert | `core_interception_engine_windows.go` → `*TransparentBypassManager` | Production |
| Linux | nfqws/NFQUEUE | `core_interception_engine_linux.go` (`nfqwsEngine` scaffold) | Scaffold + roadmap |
| macOS | NetworkExtension/TUN | `core_interception_engine_darwin.go` (`darwinTunEngine` scaffold) | Scaffold + roadmap |
| other | unsupported | `core_interception_engine_other.go` | Falls back to Compatibility TUN |

Platform availability is exposed via `interceptionEngineSupported()` /
`interceptionEngineKind()`; `core_network_mode.go` reads these instead of
hardcoding Windows. The external-VPN-conflict detector
(`detectExternalVPNConflicts`) is the parallel **SystemInspector** seam, gated by
the per-platform `systemInspectorSupported` constant.

Mobile (Android/iOS) is **not** a `GOOS` variant of this binary: it is a native
VPN shell (Android `VpnService` / iOS `NEPacketTunnelProvider`) wrapping the same
Go core via `gomobile bind`. Packet desync is unavailable there, so free-bypass
degrades to a system VPN tunnel + tg-ws-proxy.

## Bridge Security

The local HTTP bridge generates a per-launch random token
(`app/core_bridge_auth.go`), written `0600` as `bridge-token` next to the
executable. State-changing endpoints (`/api/connect`, `/api/disconnect`,
`/api/call`, `/api/quit*`, `/api/dependencies/download`, `/api/tray/ensure`)
require the `X-Dropo-Token` header; read-only GETs stay open so reachability
probes and polling keep working. The co-located Flutter UI reads the token file
and attaches the header to POSTs. This defends the loopback bridge against other
local processes and browser DNS-rebinding to `127.0.0.1`.

## Boundaries

- Common app logic stays in the root `app` package: profiles, subscriptions,
  config building, update metadata, storage, traffic stats, dependency
  bootstrap, and Flutter bridge API contracts.
- Platform-specific code uses build tags:
  - `*_windows.go` for Windows registry, single-instance mutex, tray, process job objects, window activation, and WinDivert cleanup.
  - `*_other.go` for safe no-op placeholders until Linux/macOS adapters are
    implemented.
  - `core_interception_engine_*.go` for the per-OS desync/TUN engine adapter.
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

1. Linux desktop MVP: implement `nfqwsEngine` (the `InterceptionEngine` adapter)
   on nfqws/NFQUEUE — ship nfqws in the Linux dependency archive, acquire
   `CAP_NET_ADMIN`/`CAP_NET_RAW` (or a privileged helper), install NFQUEUE rules,
   and translate composed per-service winws args to nfqws (Flowseal-compatible
   multisplit+seqovl maps almost 1:1). Plus Flutter desktop build + AppImage/`.deb`.
2. macOS desktop MVP: implement `darwinTunEngine` as a sing-box TUN tunnel first
   (transparent desync degraded to VPN fallback), Flutter desktop build, `.dmg`,
   signing/notarization, network permission model. Kernel-level desync via a
   signed system extension is a later, separate R&D phase.
3. Mobile feasibility prototype: `gomobile bind` of the core, native VPN shell for
   Android (`VpnService`) / iOS (`NEPacketTunnelProvider`). Native mobile bridge,
   not a desktop packaging variant.

When implementing a new engine adapter, flip `interceptionEngineSupportedFlag`
in that platform file and (if it adds a conflict inspector) provide
`systemInspectorSupported = true` with a real `detectExternalVPNConflicts`.
