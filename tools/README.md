# dropo Tools

Утилиты для разработки и ручной проверки маршрутизации dropo.

## preflight-release.ps1

Главный релизный gate. Скрипт запускает Go- и Flutter-проверки, собирает релиз,
валидирует артефакты и при необходимости выполняет runtime-smoke.

Минимальный запуск перед билдом:

```powershell
.\tools\preflight-release.ps1 -Build
```

Полный запуск с сетью и подпиской выполняйте из PowerShell с правами администратора:

```powershell
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
.\tools\preflight-release.ps1 -WithNetwork -WithSubscription -Build
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

Полезные параметры:

- `-WithNetwork` запускает free-access runtime smoke на релизной папке.
- `-WithSubscription` запускает subscription/xHTTP runtime smoke через Xray bridge.
- `-WireGuardConfigPath <path>` проверяет парсинг WireGuard-конфига без записи секрета в репозиторий.
- `-ReleaseFolder <path>` позволяет валидировать уже собранную папку.

## publish-release-assets.ps1

Локально проверяет и загружает Windows ZIP и Android APK в уже созданную
GitHub Actions карточку релиза. Actions при этом не получает signing keys и не
собирает артефакты.

```powershell
.\publish-release-assets.ps1 -ReleaseFolder ..\release\dropo-<version>-<hash>
```

Аутентификация берётся из `GH_TOKEN` или из локального Git credential manager.
Без `-ReplaceExisting` скрипт откажется заменять уже загруженный файл.

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
**Настройки → `🩻 Снять отпечаток`** and send the file to the developer. The
local censor lab used to reproduce these reports lives in `testlab/`.

Manual quick client bundle script, for emergency support only:

Windows diagnostics target only zapret2 `winws2.exe`. Since dropo 2.1.55 the
old zapret1 `winws.exe` runtime is neither packaged nor started; its presence in
a portable folder indicates stale files from an older build.

```powershell
.\client-quick-check.ps1
```

Run it while dropo is connected. It writes a Desktop folder with service results, a redacted configuration summary, ports, adapters, routes and authenticated Clash API data. The raw `active_config.json` is deliberately excluded because it can contain VPN credentials and the process-local Clash API secret. If normal checks fail but proxy checks pass, the problem is likely TUN/default-route handling on the client machine.

For blocked-service failures, run the deeper method matrix:

```powershell
.\client-quick-check.ps1 -DeepMethodCheck
```

This adds `free-method-results.csv` (and the legacy `byedpi-method-results.csv` copy), testing failed blocked URLs through each live ByeDPI SOCKS5 method (`18091..18094`). Transparent zapret2/winws2 is selected by the app route-probe and appears in app logs plus the main route indicator rather than this SOCKS-only matrix.

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
`winws2.exe` and `xray.exe` whose executable path is inside the
detected Dropo app root. It does not target other VPN applications.

## Практические сценарии

1. Без VPN-ключа: включить `Бесплатный доступ`, оставить сервисы включенными, запустить приложение и проверить `check-services.ps1 -Phase2Only`.
2. С VPN-подпиской: добавить подписку в UI, подключиться, проверить `check-services.ps1`.
3. С WireGuard: добавить рабочую сеть, подключиться, затем проверить корпоративные домены и маршруты через `check-routes.ps1`.
4. С foreign-routing: включить `Открывать все иностранные сайты через VPN/обход`, подключиться и проверить, что RU-сервисы остаются direct, а зарубежный final уходит в `smart-bypass`.
5. AI services: без подписки `openai.com`, `api.openai.com`, Copilot/Cursor endpoints должны идти direct/pass-through; с подпиской должен появиться `bypass-openai` с единственным кандидатом `auto-select`.
