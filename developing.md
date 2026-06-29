# dropo - Flutter + Go-core development plan

> Live engineering plan. User-facing docs live in `README.md`; release/update
> details live in `docs/UPDATE.md`; test details live in `docs/TESTING.md`.

## Current State

- Windows production UI is Flutter (`flutter_app/`).
- Windows runtime is the existing Go logic under `app/`, now running as a
  headless `dropo-core.exe` with a local HTTP bridge.
- The Flutter runner (`dropo.exe`) starts bundled `dropo-core.exe` on Windows.
- The previous HTML desktop shell has been removed from the repository.
- Windows dependency split is unchanged:
  `dropo-Windows-Portable-x64.zip` plus
  `dropo-Windows-Dependencies-x64.zip` referenced through `deps-lock.json`.

## Non-Negotiable Behavior

- Version is still read from `version.json`.
- Windows release artifact names stay stable.
- Normal traffic stays direct by default.
- RU services and Gosuslugi stay direct.
- Generic Google traffic stays direct; YouTube is handled as its own service.
- AI/dev services blocked by remote-side policy for Russian users are
  VPN-forced: ChatGPT/OpenAI/Codex, Claude/Anthropic, Copilot, Cursor,
  Perplexity, Gemini, xAI/Grok, Meta AI and similar tools.
- DPI-blocked services try free bypass first, then VPN fallback where possible.
- Android must use `VpnService`; no root-only path for the public APK.

## Architecture

```text
flutter_app/
  lib/                      # Flutter Windows/Android UI
  test/                     # Flutter widget tests

app/
  main.go                   # headless Go-core process
  http_bridge.go            # local HTTP bridge for Flutter (token-guarded POSTs)
  core_bridge_auth.go       # per-launch bridge token (X-Dropo-Token)
  core_interception_engine.go            # InterceptionEngine interface (multi-OS seam)
  core_interception_engine_<os>.go       # per-OS desync/TUN engine adapters
  app_api_*.go              # VPN, profiles, subscription, update APIs
  core_*.go                 # routing, storage, filters, WireGuard, sidecars
```

There is a single Go core. The previously orphaned `core/` (`dropo/core`)
module has been removed; do not reintroduce a parallel routing core.

Windows release layout:

```text
release/dropo-<version>-<hash>/
  dropo/
    dropo.exe               # Flutter runner
    dropo-core.exe          # Go-core bridge/runtime
    data/                   # Flutter assets
    dependencies.json       # deps manifest
  dropo-Windows-Portable-x64.zip
```

## Development Commands

Hot reload:

```powershell
# terminal 1
cd app
go run . --no-tray --listen 127.0.0.1:17890

# terminal 2
cd flutter_app
flutter run -d windows --dart-define=DROPO_CORE_ENDPOINT=http://127.0.0.1:17890
```

Release build:

```powershell
.\build.ps1 -Build
.\build.ps1 -AppOnly
.\build.ps1 -Android
```

## Test Gate

```powershell
cd app
go test ./...

cd ..\flutter_app
flutter analyze
flutter test

cd ..
.\build.ps1 -Build
```

Runtime smoke after build:

1. Start `release/dropo-<version>-<hash>/dropo/dropo.exe`.
2. Confirm it starts the bundled `dropo-core.exe`.
3. Confirm `http://127.0.0.1:17890/api/status` responds with the same version.
4. Confirm dependencies status is reported through the bridge.

## Completed Migration Work

- [x] Added Flutter project for Windows and Android.
- [x] Added Kotlin Android `DropoVpnService` stub and manifest wiring.
- [x] Added local HTTP bridge in Go-core.
- [x] Replaced legacy desktop event calls with an internal event hub.
- [x] Moved file-dialog dependent profile import/export to path-based APIs.
- [x] Replaced Windows build path with Flutter runner + `dropo-core.exe`.
- [x] Updated GitHub release workflow to install Flutter instead of the removed
  desktop shell toolchain.
- [x] Removed old HTML desktop frontend, old shell config, and old build output.
- [x] Removed obsolete shell dependencies from `app/go.mod`.
- [x] Built Windows artifact successfully:
  `release/dropo-2.0.8-6a4d675/dropo-Windows-Portable-x64.zip`.
- [x] Verified clean-port Flutter auto-start:
  `dropo.exe` started bundled `dropo-core.exe`, `/api/status` returned
  `2.0.8-6a4d675`, dependencies were ready.

## Remaining Work

- [ ] Expand Flutter UI beyond the production smoke dashboard:
  profiles, WireGuard editor, detailed settings, import/export file pickers,
  update install flow, fingerprint capture flow and richer route diagnostics.
- [ ] Add Flutter integration tests for the local bridge.
- [ ] Add release-smoke automation for launching portable `dropo.exe` and
  verifying bundled core startup.
- [ ] Implement Android VPN permission flow with `VpnService.prepare()`.
- [ ] Implement Android foreground service notification.
- [ ] Implement Android TUN via `VpnService.Builder.establish()`.
- [ ] Protect outbound helper sockets with `VpnService.protect()`.
- [ ] Add Android-supported user-space bypass path.
- [ ] Add Android release publishing as an extra asset without changing Windows
  dependency archive rules.

## Safety Rules

- Never commit VPN subscription URLs, WireGuard private keys or user logs.
- Keep Windows build passing before expanding Android/macOS/Linux work.
- Do not route all traffic through VPN by default.
