# dropo - заметки для разработки и агентов

> Канонические документы: пользовательское/архитектурное описание — `README.md`;
> тестирование (все слои + лаба-цензор) — `docs/TESTING.md`; лаба — `testlab/`.
> Этот файл — рабочие заметки; при расхождении приоритет у README/TESTING.
> Платформа: реализован Windows; Android/macOS/Linux — тот же смысл, реализация позже.

## Что реализуем

dropo - Windows-first VPN-клиент на Wails/Go с HTML/CSS/JS UI.

Основные контуры:

- sing-box для подписок и маршрутизации.
- Нативный WireGuard для рабочих сетей.
- ByeDPI (`ciadpi.exe`), optional SpoofDPI and zapret/winws как бесплатный доступ без VPN-ключа.
- Единое хранилище `resources/settings.json`.
- UI 2.0: главный экран сохранён по структуре, разделы открываются как левая drawer-панель до `90vw`.

## Правила UI

- Настройки применяются сразу, кнопки `Сохранить` в настройках нет.
- Сохранение подписки, WireGuard-конфига, шаблона и профиля остаётся явным, потому что там пользователь подтверждает вводимые данные.
- Toast-уведомления показываются справа снизу и имеют `z-index` выше панелей.
- Основной логотип: `Dr` + поднятое `opo`; favicon и bitmap/ico-ассеты должны соответствовать этому знаку.
- Не возвращать центральные модалки для разделов приложения. Базовый слой `.modal-overlay/.modal` намеренно работает как drawer.
- Отключение VPN не должно открывать глобальный loader/modal. Центральная кнопка остаётся inline-loader, а статусы остановки sing-box, WireGuard, free-access и cleanup показываются под кнопкой.
- `Тест доступности сервисов` в приложении должен оставаться нативной Go-проверкой с событиями `client-check-*`: без запуска PowerShell, без мигающих окон и без записи результата в общий лог.
- В UI сетевого режима всегда различайте requested и active mode. Bottom-pill показывает active mode, fallback banner показывает причину, если active отличается от требуемого режима.
- Панель `Ping` не должна дублировать методы отдельным блоком. Во время активного VPN один список показывает сервис, текущий метод обхода и ping; `Показать все` открывает тот же набор полностью.

## Где менять

- Основной UI: `app/frontend/index.html`.
- Старые модульные файлы поддержки: `app/frontend/js/*`, `app/frontend/css/*`.
- Backend API настроек: `app/app_api_settings.go`.
- Storage и генерация active config: `app/core_storage.go`.
- WireGuard: `app/core_wireguard.go`, `app/core_wireguard_native.go`.
- Build/release: `version.json`, `app/wails.json`, `build.ps1`.

## Настройки

Приложение требует elevation через Windows manifest (`requireAdministrator`), поэтому UAC появляется до инициализации Wails/Go.

Общий autosave в UI:

- `scheduleAutoSaveSettings()`
- `autoSaveSettings()`
- `saveSettings()` оставлен как совместимый alias

Отдельные backend-методы с немедленным применением:

- `SetRoutingMode`
- `SetDisableFreeAccess`
- `SetHideRuTraffic`

RU proxy сохраняется с debounce, чтобы не валидировать незавершённый URL на каждый символ.

`Бесплатный доступ` должен работать без добавленной VPN-подписки. **Без подписки** dropo идёт в `Deep Windows`: native zapret/winws через WinDivert без sing-box TUN, чтобы не ломать поведение официального zapret-скрипта. **С подпиской** (или когда плану нужен proxy/VPN слой — VLESS, AI/VPN-only, except-RU, all-traffic, RU-proxy, forced VPN) dropo идёт в `Compatibility TUN`: маршрутизацией владеет sing-box TUN, а winws-движок desync запускается **рядом** (гибрид) — `bypass-<service>` становится `urltest[direct, VPN]`, где winws десинхронизирует `direct` (free), а недоступное уходит в VPN. Сервисы blockType `vpn`/`proxy` (Meta, WhatsApp, Telegram) идут в VPN, а не в «мёртвый» direct. `.srs` базы маршрутизации обновляются только на этапе сборки: `build.ps1` проверяет upstream Re-filter release, скачивает более свежие базы в `dependencies/filters`, пересобирает community/discord списки через bundled `sing-box` и копирует проверенный набор в релиз. На клиентской машине приложение только читает bundled `bin/filters` и не скачивает новые базы. Перед записью `active_config.json` storage выбирает доступный порт для `mixed-in`, удаляет risky `tcp_fast_open/tcp_multi_path` и stale `bind_interface`/bind-address поля из `direct` outbound, чтобы не ломать TLS и прямую маршрутизацию в fallback TUN.

## Брендинг

Новое имя: `dropo`.

Новые пользовательские артефакты:

- `dropo.exe`
- `dropo-{version}-{hash}.zip`
- `dropo-{version}-setup.exe`
- `%LOCALAPPDATA%\dropo\logs\dropo-YYYYMMDD-HHMMSS.log`
- `%TEMP%\dropo\dropo-YYYYMMDD-HHMMSS.log`

Legacy-имена `KampusVPN` и `kampus-wg-` оставлены только для миграции/очистки старых данных.

## Проверка без VPN-ключа

1. Запустить приложение.
2. Открыть настройки.
3. Проверить, что панель выезжает слева.
4. Убедиться, что тумблера `Включить бесплатный доступ` больше нет: бесплатные методы работают по умолчанию.
5. Переключить `Не использовать бесплатные методы` и убедиться, что изменение применяется без кнопки сохранения.
6. Вернуть `Не использовать бесплатные методы` в выключенное состояние.
7. Нажать основную кнопку подключения без добавления VPN-подписки.
8. Проверить `tools/check-services.ps1 -Phase2Only`.

Runtime smoke без подписки:

```powershell
cd app
$env:DROPO_TEST_FREE_ACCESS_BASE = "<release-folder>"
go test ./... -run TestManualFreeAccessRuntimeFromEnv -count=1 -v
Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE
```

Runtime smoke with a subscription:

```powershell
cd app
$env:DROPO_TEST_FREE_ACCESS_BASE = "<release-folder>"
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
go test ./... -run TestManualSubscriptionRuntimeFromEnv -count=1 -v
Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE, Env:\DROPO_TEST_SUBSCRIPTION_URL
```

Routing diagnostic note: `smart-bypass` and every `bypass-<service>` group start as bypass/VPN-only groups. Startup uses saved/default free strategies for speed. The old home-screen `Test` route probe is removed; the user-facing checks now live in Settings: `🔍 Проверить` (native availability test, streamed `client-check-*` events) and `🩻 Снять отпечаток` (provider DPI fingerprint — see `docs/TESTING.md`). Failed blocked services enqueue a per-service maintenance job. With a subscription, only the failed service group is switched to `auto-select` as a temporary VPN fallback; without a subscription, the service goes straight to the maintenance queue. The listener processes one service at a time and keeps the first working free strategy it finds. Proxy-style free methods are ByeDPI (`byedpi`, `byedpi-sni`, `byedpi-oob`, `byedpi-fake`) plus optional SpoofDPI (`spoofdpi-socks`). Engine selection: **without a subscription** Deep Windows is primary (winws via WinDivert, no TUN); **with a subscription** dropo uses Compatibility TUN (sing-box owns routing) and runs winws desync **alongside** it (hybrid), so each `bypass-<service>` is `urltest[direct, VPN]` — winws desyncs the `direct` path (free), unreachable falls to VPN. Compatibility TUN **without** winws is only the emergency fallback if the Deep Windows engine cannot start. Services with blockType `vpn`/`proxy` (Meta, WhatsApp, Telegram) route to the VPN group, not a dead `direct` path. Subscription `auto-select` is a selector with the first stored proxy as default; the UI sorts proxies by measured ping for manual selection. Helper processes (`ciadpi.exe`, `spoofdpi.exe`, `winws.exe`, `winws2.exe`) are routed `direct` early when sing-box is active to avoid loops. Telegram is a blocked-service route: `Telegram.exe`, Telegram domains and Telegram DC IP ranges go through `bypass-telegram`, not direct. VK, Yandex, Ozon, Sber and Gosuslugi remain direct by default in `blocked_only`. The "Открывать все иностранные сайты через VPN/обход" toggle maps to `except_russia`: RU services stay direct, final foreign traffic is marked for local proxy routing. DNS uses `ipv4_only` plus reverse mapping so direct sites do not fail on clients without IPv6. Session logs include `[Diag]`, `[DeepWindowsPlan]`, `[RouteProbe]`, `[FreeAccess]`, `[Zapret]` and WinDivert service-status lines.

AI routing note: the `openai` service tag now covers OpenAI/ChatGPT/Codex, Claude/Anthropic, GitHub Copilot, Cursor, Perplexity, Gemini, xAI/Grok and Meta AI endpoints. This service is VPN-only because ByeDPI does not change the public hosting IP and cannot fix foreign-side geo/API restrictions. When a subscription/VLESS key exists, `bypass-openai` is created with `auto-select` as its only candidate. When no subscription exists, AI domains and IDE processes are routed direct/pass-through instead of trying ByeDPI.

Windows routing note: without a subscription, Deep Windows (native `winws.exe`/WinDivert, no TUN) is the traffic layer. With a subscription dropo runs Compatibility TUN (sing-box TUN) **plus** the winws desync engine in parallel (`startComposedTransparentEngine`), so free desync still applies on the `direct` path while the subscription handles per-service VPN fallback. Helper egress (`winws.exe`, `tg-ws-proxy.exe`, ByeDPI, Xray) is routed `direct` early to avoid TUN loops; `tg-ws-proxy.exe` is omitted from the direct rule only when Telegram is forced to VPN. Telegram presence is verified live via the OS TCP table (re-injects `tg://proxy` if missing), not a one-time flag.

## Проверка с подпиской

1. Добавить URL подписки через `Добавить VPN`.
2. Нажать `Проверить`, затем сохранить подписку.
3. Подключиться.
4. Проверить статус, ping-панель, список серверов, статистику и логи.
5. Запустить `tools/check-services.ps1`.

Для быстрой проверки парсинга без записи секретов в проект можно использовать env-driven тест:

```powershell
cd app
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
go test ./... -run TestManualSubscriptionURLFromEnv -count=1
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

## Проверка WireGuard

1. Открыть `Рабочие сети`.
2. Добавить WireGuard-конфиг.
3. Проверить парсинг, сохранить.
4. Подключиться и проверить корпоративные домены, например `kampus.internal`, если тестовая сеть доступна.
5. Запустить `tools/check-routes.ps1`.

Парсинг WireGuard-конфига можно проверить без сохранения ключей:

```powershell
cd app
$env:DROPO_TEST_WG_CONFIG = @"
<wireguard-config>
"@
go test ./... -run TestManualWireGuardConfigFromEnv -count=1
Remove-Item Env:\DROPO_TEST_WG_CONFIG
```

## Release

Версия задаётся в `version.json`.

Перед публичным билдом запускайте preflight. Минимальный gate:

```powershell
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -Build
```

Полный gate с сетевыми runtime-smoke требует elevated PowerShell:

```powershell
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -WithNetwork -WithSubscription -Build
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

Для повторных прогонов на уже подготовленной машине можно добавить `-SkipInstall`, чтобы не переустанавливать npm-зависимости.

Что проверяет preflight:

- frontend build и Playwright visual/click E2E;
- `go test ./...`;
- наличие `dropo.exe`, `sing-box.exe`, `xray.exe`, WireGuard, ByeDPI, zapret/winws, `.srs` баз и license-файлов в релизе;
- отсутствие runtime-файлов в ZIP;
- `requireAdministrator` в Windows manifest;
- отсутствие старого видимого бренда в пользовательском UI и metadata;
- версии `sing-box` и `xray`;
- optional free-access runtime smoke;
- optional subscription/xHTTP runtime smoke;
- optional WireGuard config parse smoke через `-WireGuardConfigPath`.

```powershell
.\build.ps1 -All
```

Ожидаемые артефакты:

- `release/dropo-{version}-{hash}/dropo.exe`
- `release/dropo-{version}-{hash}.zip`
- `release/dropo-{version}-setup.exe`, если установлен NSIS

## Осторожность

- Не коммитить VPN private key, subscription URL или WireGuard secrets в документацию/fixtures.
- Не удалять legacy-константы без отдельной миграционной задачи.
- Не запускать destructive git-команды для очистки чужих изменений.
