# dropo 2.0.0 - процесс изменения

> Исторический снимок процесса для релиза 2.0.0. Актуальное архитектурное и
> тестовое описание — `README.md` и `docs/TESTING.md`. Текущая версия — 2.0.2
> (гибридная маршрутизация winws+TUN, Telegram-прокси с живой проверкой,
> отпечаток блокировки и лаба-цензор `testlab/`).

Дата актуализации: 2026-06-22.

## Изменения

- Приложение переименовано из Kampus VPN в `dropo`.
- Версия поднята до `2.0.0`.
- Wails metadata, build script, installer metadata и package metadata переведены на `dropo`.
- Основной знак заменён на `Dr` + поднятое `opo`.
- Сгенерированы новые `appicon.png`, `icon.ico` и статусные tray-иконки.
- Все бывшие модальные разделы визуально работают как левая drawer-панель до `90vw`.
- Toast-уведомления закреплены справа снизу и отображаются поверх панелей.
- В настройках удалена кнопка `Сохранить`; изменения применяются сразу.
- Исправлен вызов toast при копировании версии.
- Installer теперь копирует весь `bin`, включая WireGuard, ByeDPI и filters.
- Tools и документация обновлены под новый бренд и новые пути.
- Windows manifest требует `requireAdministrator`, UAC появляется до инициализации приложения.
- Локальные базы маршрутизации обновляются только при сборке; клиент использует проверенный bundled-набор из релиза.
- Запуск без VPN-подписки разрешён: бесплатные методы работают по умолчанию, а Deep Windows запускает zapret/winws без sing-box TUN. sing-box поднимается только как local-only proxy endpoint, если маршрутному плану нужны подписка/VLESS, AI/VPN-only, except-RU, all-traffic, RU-proxy или forced proxy/VPN сервисы.
- `mixed-in` получил безопасный дефолтный порт `2088` и runtime fallback на свободный порт.
- Из `direct` outbound убраны `tcp_fast_open/tcp_multi_path`, потому что они ломали TLS через Windows TUN.
- Добавлен релизный preflight: frontend build, Playwright visual/click E2E, Go tests, artifact validation, optional free-access/subscription/WireGuard runtime-smoke.
- Визуальный preflight выявил и исправил слой `confirmModal`: подтверждения теперь открываются поверх drawer-панелей, но ниже toast-уведомлений.

## Проверка UI

Проверить через локальный frontend:

1. Открыть главный экран.
2. Открыть `Настройки`, `Статистика`, `Логи`, `Профили`, `Рабочие сети`, `About`.
3. Убедиться, что панели появляются слева, не по центру.
4. Вызвать toast над открытой панелью и проверить, что он справа снизу поверх UI.
5. Переключить настройки и убедиться, что кнопки сохранения в настройках нет.

## Проверка без VPN-ключа

1. Открыть настройки.
2. Убедиться, что старых тумблеров `Бесплатный доступ` и `Предпочитать бесплатные методы` нет.
3. Переключить `Не использовать бесплатные методы`, убедиться, что backend возвращает success.
4. Вернуть `Не использовать бесплатные методы` в выключенное состояние.
5. Запустить подключение без добавленной VPN-подписки.
6. Запустить:

```powershell
cd tools
.\check-services.ps1 -Phase2Only
```

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

Routing diagnostic note: `smart-bypass` and every `bypass-<service>` group start as bypass/VPN-only groups. Saved/default strategies are applied on VPN start; the manual `Test` flow and settings refresh can run full route-probe and persist better free strategies. Proxy-style free methods are ByeDPI (`byedpi`, `byedpi-sni`, `byedpi-oob`, `byedpi-fake`) plus optional SpoofDPI (`spoofdpi-socks`). Deep Windows is the primary mode for every routing setting. Transparent zapret/winws runs natively through WinDivert; when a route needs proxy/VPN handling, sing-box runs local-only with TUN removed and `set_system_proxy=false`, and the Deep Windows route plan decides which flows must be redirected to it. Compatibility TUN is used only when Deep Windows cannot be activated. If `Не использовать бесплатные методы` is enabled and a subscription exists, service groups use `auto-select` only; if no subscription exists, startup requires a WireGuard-only scenario or fails with a clear error. Telegram is a blocked-service route: `Telegram.exe`, Telegram domains and Telegram DC IP ranges go through `bypass-telegram`, not direct. VK, Yandex, Ozon, Sber and Gosuslugi remain direct by default in `blocked_only`. Startup logs include `[Diag]`, `[DeepWindowsPlan]`, `[RouteProbe]` and `%TEMP%\dropo\dropo.log` lines with outbounds, route counts, candidate checks and selected methods.

## Проверка с подпиской

1. Добавить тестовую подписку через UI.
2. Проверить подписку.
3. Сохранить подписку.
4. Подключиться.
5. Проверить ping-панель, статистику и логи.
6. Запустить:

```powershell
cd tools
.\check-services.ps1
```

Без записи секретов в файлы можно проверить парсинг подписки:

```powershell
cd app
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
go test ./... -run TestManualSubscriptionURLFromEnv -count=1
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

## Проверка WireGuard

1. Добавить WireGuard-конфиг через `Рабочие сети`.
2. Проверить парсинг и сохранить.
3. Подключиться.
4. Проверить доступ к корпоративным доменам, если сеть доступна.
5. Запустить:

```powershell
cd tools
.\check-routes.ps1
```

Парсинг WireGuard-конфига проверяется env-driven тестом:

```powershell
cd app
$env:DROPO_TEST_WG_CONFIG = @"
<wireguard-config>
"@
go test ./... -run TestManualWireGuardConfigFromEnv -count=1
Remove-Item Env:\DROPO_TEST_WG_CONFIG
```

## Release

Команда:

```powershell
.\build.ps1 -All
```

Ожидаемые артефакты:

- `release/2.0.0-{hash}/dropo.exe`
- `release/dropo-2.0.0.zip`
- `release/dropo-2.0.0-setup.exe`, если установлен NSIS

Фактический прогон от 2026-06-21:

- `release/2.0.0-02dab5e/dropo.exe`
- `release/dropo-2.0.0.zip`
- setup installer не создан: NSIS не установлен на машине сборки.

Фактический прогон от 2026-06-22:

- `release/2.0.0-c25bec5/dropo.exe`
- `release/dropo-2.0.0.zip`
- setup installer не создан: NSIS не установлен на машине сборки.
- Проверено: сборка кладёт актуальные bundled-базы в релиз; free-access-only runtime smoke прошёл для Discord, YouTube и gstatic.

## Риски

- Полный VPN/WireGuard smoke-test требует прав администратора и может менять сетевые маршруты.
- Legacy-данные читаются из старых путей, но новые данные пишутся в `dropo`.
- Старые WireGuard-сервисы `kampus-wg-*` очищаются как legacy.
