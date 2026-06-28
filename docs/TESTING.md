# dropo — тестирование

Детальное описание того, как тестируется dropo: от unit-тестов до эмуляции
DPI-блокировок РФ вне РФ. Документ формализован по слоям (от дешёвых и быстрых к
дорогим и реалистичным).

> Платформенная оговорка: всё ниже описывает **Windows-реализацию** (текущая
> единственная). Для Android / macOS / Linux смысл проверок тот же (тот же
> каталог сервисов, та же таксономия блокировок, та же лаба-цензор), но
> платформенная обвязка будет добавляться позже — см. README → «Платформы».

## Пирамида тестирования

| Слой | Что проверяет | Среда | Скорость | Где |
|---|---|---|---|---|
| L1 | Логика Go (маршрутизация, стратегии, конфиг, telegram-политика) | host | секунды | `go test ./...` |
| L2 | UI (Flutter dashboard, bridge calls, layout smoke) | host | minutes | `cd flutter_app; flutter analyze; flutter test` |
| L3 | Release gate (Go+Flutter+build+ZIP validation) | host | minutes | `tools/preflight-release.ps1` |
| L4 | Runtime собранного релиза (free / подписка / WireGuard) | Windows, admin | минуты | `go test -run TestManual…` |
| L5 | Снятие отпечатка блокировки у конечного клиента | клиент в РФ | секунды | кнопка в приложении |
| L6 | Эмуляция DPI/ТСПУ вне РФ и проверка обхода | Docker | минуты | `testlab/` |

L1–L4 проверяют, что приложение корректно собрано и логика маршрутизации верна.
L5–L6 — это контур разработки **методов обхода** без катания сборок в РФ.

---

## L1. Go unit-тесты

```powershell
cd app
go test ./...            # весь пакет (dropo)
go test -run TestResolveTelegramTransport -v
```

Покрытие (ключевое):
- **Маршрутизация free-access** — `free_access_config_test.go`: сборка outbounds
  и route-rules, `bypass-<service>` группы, smart-bypass, пиновка VPN/transparent,
  что сервисы blockType `vpn`/`proxy` уходят в VPN, а не в «мёртвый» direct.
- **Telegram-политика** — `core_tgwsproxy_test.go`: `resolveTelegramTransport`
  (auto/free/vpn), sidecar жив и в VPN-режиме, `tg://proxy` не дёргается лишний раз.
- **Сетевой режим** — `network_mode_test.go`: выбор Deep Windows ↔ Compatibility TUN.
- **Detection (Windows)** — синтаксис `core_tgwsproxy_detect_windows.go`
  компилируется; runtime-проверка TCP-таблицы/процессов — вручную (см. ниже).

Детектор не флейкует: чистые таблицы/функции, без сети.

## L2. Flutter UI smoke

```powershell
cd flutter_app
flutter analyze
flutter test
```

Checks that the Flutter Windows shell renders production controls and that bridge-facing widgets stay buildable. Network/runtime checks remain in L4.

## L3. Релизный gate — `preflight-release.ps1`

Единая точка перед сборкой: frontend build → `go test` → (опц.) Playwright →
сборка релиза → валидация ZIP → (опц.) runtime-smoke.

```powershell
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -Build
```

Полный прогон с сетью и подпиской — из **elevated** PowerShell (секреты только
через env, не в файлы):

```powershell
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
.\tools\preflight-release.ps1 -WithVisual -InstallBrowsers -WithNetwork -WithSubscription -Build
Remove-Item Env:\DROPO_TEST_SUBSCRIPTION_URL
```

## L4. Runtime собранного релиза (Windows, admin)

Прогон логики на реальном артефакте. Секреты — через env.

```powershell
# free-access без подписки
$env:DROPO_TEST_FREE_ACCESS_BASE = "<release-folder>"
go test ./... -run TestManualFreeAccessRuntimeFromEnv -count=1 -v

# путь «VPN включён» (подписка + xhttp через Xray bridge)
$env:DROPO_TEST_SUBSCRIPTION_URL = "<subscription-url>"
go test ./... -run TestManualSubscriptionRuntimeFromEnv -count=1 -v
Remove-Item Env:\DROPO_TEST_FREE_ACCESS_BASE, Env:\DROPO_TEST_SUBSCRIPTION_URL
```

Дополнительно — клиентские PowerShell-утилиты в `tools/` (`check-services.ps1`,
`check-routes.ps1`, `client-quick-check.ps1`). Предпочтительно — встроенная
кнопка **Настройки → «🔍 Проверить»** (нативная проверка доступности, без
PowerShell-окон).

Что НЕ проверяет L1–L4: реально ли обход открывает заблокированный сервис **под
DPI**. Это требует либо реального РФ-клиента, либо эмуляции — L5/L6.

---

## L5. Снятие отпечатка блокировки (на стороне клиента)

Конечный пользователь в РФ нажимает **Настройки → «🩻 Снять отпечаток»**
(`App.CaptureDPIFingerprint`, [app/app_api_fingerprint.go](../app/app_api_fingerprint.go)).

Что делает (с **выключенным** VPN, чтобы winws не маскировал поведение провайдера):
для каждого сервиса из каталога — послойная проба `DNS → TCP:443 → TLS(SNI)` и
классификация по таксономии rkn-block-checker / dpi-detector:

| поле | значения |
|---|---|
| `dns` | ok / poisoned / nxdomain / error |
| `tcp` | ok / timeout / refused / error |
| `tls` | ok / rst / drop / error |
| `verdict` | ok / dns-poison / ip-block / tls-rst / tls-drop / unknown |

Результат пишется в `resources/fingerprints/dpi-fingerprint-<время>.json`, папка
открывается, пользователь присылает файл разработчику. Прогресс идёт в кнопку
(событие `fingerprint-progress`).

Эти файлы — топливо для L6: они говорят, **как именно** блокирует конкретный
провайдер, чтобы лаба эмулировала ровно это.

---

## L6. Лаба-цензор: эмуляция DPI/ТСПУ вне РФ

Полное описание — [testlab/README.md](../testlab/README.md). Здесь — методология
и **что именно проверено**.

### Идея

Локальный Linux-шлюз-«цензор» воспроизводит блокировки провайдера; тест-машина
(в идеале Windows-VM с dropo+winws) маршрутизируется через него. winws переписывает
пакеты на NIC VM, цензор видит уже переписанные пакеты и реагирует как реальный DPI.

```
[ Windows VM: dropo + winws ]  →  [ Linux-цензор (testlab) ]  →  интернет
```

### Два режима цензора

**1. Naive** (`censor/apply.py`, iptables `xt_string`) — быстрый дым-тест.
Матчит подстроку SNI в одном пакете → RST; CIDR → drop; SNI → tc-throttle.
Ограничение: **любой split SNI обходит его даром** — split-only стратегия пройдёт
лабу, но провалится в РФ. Годен для проверки IP-блока и факта SNI-RST.

**2. Stateful / полное покрытие** (`censor/stateful.py`, NFQUEUE) — честная модель
ТСПУ. **Пересобирает TCP-поток до чтения SNI**, поэтому split/multisplit НЕ проходят
даром. Ручки моделируют каждый трюк desync:

| техника winws/zapret | ручка | смысл |
|---|---|---|
| `split` / `multisplit` | — | всегда блок (поток реассемблится) |
| `--dpi-desync-split-seqovl` | `REASSEMBLE_POLICY=first\|last` | обход работает, только если политика берёт фейковые байты overlap'а |
| `fooling=badsum` | `VALIDATE_CHECKSUM=0\|1` | 0 → фейк инспектируется (badsum обманывает); 1 → игнор (побеждён) |
| `fake-ttl` | `MIN_TTL=0..255` | низкий TTL фейка виден цензору или нет |
| `fake-quic` | `QUIC=1` | расшифровка QUIC Initial (RFC 9001) и матч по SNI |

IP-блок (Telegram/Meta) — iptables drop CIDR; для устойчивости к CDN-IP
`build_profile.py` для ip-block-сервисов добавляет и CIDR, и SNI-RST (ТСПУ делает оба).

### Конвейер фингерпринтов

```
fingerprints/*.json  +  services.json  --build_profile.py-->  profiles/censor-profile.json
```
Агрегация: по каждому сервису берётся **самый агрессивный** вердикт среди всех
фингерпринтов; пустая база → разумный RU-baseline. Добавить отчёт клиента:

```bash
python testlab/tools/add_fingerprint.py <file> --isp mts --country RU
```

### Запуск

```bash
cd testlab
python tools/build_profile.py

# naive:
docker compose up -d --build censor
docker compose run --rm tester
docker compose down

# stateful (полное покрытие):
docker compose -f docker-compose.yml -f docker-compose.stateful.yml up -d --build censor
docker compose -f docker-compose.yml -f docker-compose.stateful.yml run --rm tester

# юнит-тесты движка цензора (без ядра/NFQUEUE):
docker run --rm --entrypoint python3 testlab-censor-stateful /stateful.py --selftest
#   (в git-bash префиксуйте: MSYS_NO_PATHCONV=1 docker run … /stateful.py --selftest)
```

### Проверенные результаты (на этой машине, WSL2 kernel 6.6)

Движок stateful-цензора — **9/9** unit:
```
PASS parse plain SNI / plain youtube blocked / plain example allowed
PASS split incomplete before 2nd seg
PASS TCP split still BLOCKED (reassembled)        # split не проходит даром
PASS multisplit BLOCKED (reassembled)
PASS seqovl bypass works under policy=first
PASS seqovl blocked under policy=last
PASS QUIC Initial SNI extracted                    # QUIC-расшифровка работает
```

E2E через NFQUEUE — **5/5** (stateful) и **5/5** (naive):
```
PASS control example.com -> http 200
PASS youtube (SNI rst) -> blocked
PASS discord (SNI rst) -> blocked
PASS telegram DC (ip drop) -> blocked
PASS instagram (ip drop) -> blocked
```

Требование к ядру: stateful-режиму нужен `nfnetlink_queue` (есть в Docker
Desktop/WSL2 6.6 — подтверждено; naive-режиму — `xt_string`).

### Тугой цикл без пересборки приложения

Бутылочное горлышко — winws-стратегии, не само приложение. Бери строку
`[Zapret] starting Per-service bypass: winws.exe …` из лога клиента и гоняй **тот
же winws** на Windows-VM за цензором. Пересобирай dropo только под доказанные
стратегии.

### Граница достоверности (честно)

- **naive** даёт ложную зелёнку для split/fake — используй только как дым-тест.
- **stateful** достаточен для desync-сервисов (YouTube/Discord/QUIC), если выставить
  ручки под фингерпринт конкретного провайдера. Стратегия, прошедшая stateful с
  RF-ручками, — это **реальное** свидетельство работы в РФ.
- Чего лаба не заменяет: точные тайминги throttle, проприетарные особенности
  конкретного железа ТСПУ. Финальное подтверждение спорных кейсов — реальный клиент.

---

## Опорные проекты (методология/таксономия)

- [bol-van/zapret](https://github.com/bol-van/zapret) — nfqws/tpws, `blockcheck.sh`
- [Runnin4ik/dpi-detector](https://github.com/Runnin4ik/dpi-detector),
  [MayersScott/rkn-block-checker](https://github.com/MayersScott/rkn-block-checker) — таксономия блокировок
- [Kkevsterrr/geneva](https://github.com/Kkevsterrr/geneva) — testbed «цензор против обхода»
