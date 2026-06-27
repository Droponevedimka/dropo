# dropo Tools

Утилиты для разработки и ручной проверки маршрутизации dropo.

## preflight-release.ps1

Главный релизный gate. Скрипт собирает frontend, запускает Go-тесты, опционально гоняет визуальные Playwright-сценарии, собирает релиз, валидирует ZIP и может запустить runtime-smoke на собранном артефакте.

Минимальный запуск перед билдом:

```powershell
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -Build
```

Полный запуск с сетью и подпиской выполняйте из PowerShell с правами администратора:

```powershell
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -WithNetwork -WithSubscription -Build
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

Полезные параметры:

- `-WithVisual` запускает Playwright-клики и визуальные проверки.
- `-WithNetwork` запускает free-access runtime smoke на релизной папке.
- `-WithSubscription` запускает subscription/xHTTP runtime smoke через Xray bridge.
- `-WireGuardConfigPath <path>` проверяет парсинг WireGuard-конфига без записи секрета в репозиторий.
- `-ReleaseFolder <path>` позволяет валидировать уже собранную папку.

## check-services.ps1

Проверяет доступность набора сайтов в двух группах:

- `Phase 1`: сервисы, которые должны открываться напрямую.
- `Phase 2`: сервисы, которые должны открываться через VPN/free-access маршрут.

Запуск:

```powershell
cd tools
.\check-services.ps1
.\check-services.ps1 -SkipIPCheck
.\check-services.ps1 -Verbose
.\check-services.ps1 -Phase1Only
.\check-services.ps1 -Phase2Only
.\check-services.ps1 -Json
```

## check-routes.ps1

Проверяет DNS и активный `active_config.json`. Скрипт ищет конфиг в:

- `.\resources\active_config.json`
- `%LOCALAPPDATA%\dropo\resources\active_config.json`
- legacy `%LOCALAPPDATA%\KampusVPN\resources\active_config.json`

Запуск:

```powershell
cd tools
.\check-routes.ps1
```

Preferred client flow: **Настройки → `🔍 Проверить`** (the in-app availability
test, moved from the home screen into Settings). It runs a native concurrent
check inside dropo, updates the result table dynamically, does not open
PowerShell windows, and does not write the quick-check output into the main app
logs. For diagnosing *how* a provider blocks (RST/timeout/IP/DNS), use
**Настройки → `🩻 Снять отпечаток`** and send the file to the developer — see
`docs/TESTING.md` (L5) and the censor lab in `testlab/`.

Manual quick client bundle script, for emergency support only:

```powershell
.\client-quick-check.ps1
```

Run it while dropo is connected. It writes a Desktop folder with service-results, active_config, ports, adapters, routes and Clash API data. If normal checks fail but proxy checks pass, the problem is likely TUN/default-route handling on the client machine.

For blocked-service failures, run the deeper method matrix:

```powershell
.\client-quick-check.ps1 -DeepMethodCheck
```

This adds `free-method-results.csv` (and the legacy `byedpi-method-results.csv` copy), testing failed blocked URLs through each live local free proxy method: ByeDPI SOCKS5 ports (`18091..18094`) and optional SpoofDPI SOCKS5 (`18095`). Transparent zapret/winws is selected by the app route-probe and appears in app logs plus the main route indicator rather than this SOCKS-only matrix.

Dropo now cleans bundled sidecar processes automatically on startup, before VPN
start, on failed starts, on stop, and on quit. It also scans sibling portable
folders named `dropo-*`, so a newly unpacked build can clean sidecars left by an
older unpacked build. If the app itself cannot be launched, use the manual
cleanup command as an emergency fallback:

```powershell
.\client-quick-check.ps1 -DeepMethodCheck
.\client-quick-check.ps1 -CleanupDropoOrphans
```

`-CleanupDropoOrphans` kills only managed `sing-box.exe`, `ciadpi.exe`,
`spoofdpi.exe`, `winws.exe`, and `xray.exe` whose executable path is inside the
detected Dropo app root. It does not target other VPN applications.

## Практические сценарии

1. Без VPN-ключа: включить `Бесплатный доступ`, оставить сервисы включенными, запустить приложение и проверить `check-services.ps1 -Phase2Only`.
2. С VPN-подпиской: добавить подписку в UI, подключиться, проверить `check-services.ps1`.
3. С WireGuard: добавить рабочую сеть, подключиться, затем проверить корпоративные домены и маршруты через `check-routes.ps1`.
4. С foreign-routing: включить `Открывать все иностранные сайты через VPN/обход`, подключиться и проверить, что RU-сервисы остаются direct, а зарубежный final уходит в `smart-bypass`.
5. AI services: без подписки `openai.com`, `api.openai.com`, `claude.ai`, Copilot/Cursor endpoints должны идти direct/pass-through; с подпиской должен появиться `bypass-openai` с единственным кандидатом `auto-select`.
