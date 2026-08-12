# Direct-first routing contract

Этот документ фиксирует обязательную логику маршрутизации dropo для Windows и
Android. Заблокированный сервис получает стратегию или VPN, а любой остальной
сервис по умолчанию идёт напрямую без отдельного allowlist.

## Порядок решений

В режиме `blocked_only` правила применяются в следующем порядке:

1. Рабочие сети, private/LAN и WireGuard overlay.
2. Явная политика пользователя `Direct` или `VPN` для конкретного сервиса.
3. Положительная идентификация заблокированного сервиса: домен, приложение либо
   сервисный protocol/media fingerprint.
4. Любой другой известный HTTP Host, TLS SNI, QUIC server name или DNS reverse
   mapping получает terminal `direct`/packet `pass`.
5. IP разрешено использовать только по явно заданной политике:
   - `require_context` — CIDR подтверждает уже найденный домен, процесс или
     fingerprint;
   - `hostless_only` — только общий подписанный blocked-IP каталог и только
     когда hostname получить не удалось.
6. Неизвестный или неоднозначный трафик проходит без изменения.

IP и даже одиночный `/32` не являются идентификатором сервиса: CDN, anycast и
reverse proxy могут обслуживать несколько независимых доменов на одном адресе.
Добавление EA, Riot, Steam или другой незаблокированной программы в специальный
direct-список поэтому не является основным способом исправления.

## Реализация по платформам

- Windows sing-box: доменные и процессные правила сервиса идут раньше общей
  known-domain direct boundary. Сервисный `ip_cidr` допустим только совместно с
  `process_name`. Общий blocked-IP rule-set стоит после boundary.
- Windows Traffic Orchestrator: `ServiceRule.IPMatchPolicy` обязателен при
  наличии `IPCIDRs`. Сетевой WinDivert layer не предоставляет PID, поэтому
  production `TrafficPlan` не полагается на process-only исключения.
- Android: домен и `package_name` определяют VPN-политику. Независимые
  service-IP правила в VPN не генерируются; после сервисных правил установлена
  known-domain direct boundary, а `route.final` в `blocked_only` равен `direct`.

Режим `all_traffic` является явным выбором пользователя и намеренно не следует
этому split-routing контракту.

## Однозначная эмуляция общей IP-площадки

Основной тест создаёт настоящие IPv4/TCP пакеты с TLS ClientHello и отправляет
их через `trafficorchestrator.Processor`. Оба пакета имеют один destination
`66.22.200.1`, но разные SNI:

- `gateway.discord.com` — должен быть классифицирован как Discord и изменён
  выбранной тестовой стратегией;
- `accounts.ea.com` — не должен получить ServiceID, должен вернуться одним
  побайтно неизменённым пакетом.

Запуск:

```powershell
cd app
go test ./trafficorchestrator -run TestProcessorSharedAddressEmulationKeepsUnblockedTLSDirect -count=1 -v
go test . -run TestNamedServiceCIDRsNeverBecomeStandaloneSingBoxRoutes -count=1 -v
cd mobile\dropocore
go test . -run TestAndroidBlockedOnlyRoutesOnlyBlockedServicesThroughVPN -count=1 -v
```

Дополнительные unit-тесты проверяют, что hostless UDP на сервисном CIDR без
fingerprint проходит неизменённым, подтверждённый Discord media packet
классифицируется, а generic blocked-IP не может переопределить известный домен.

Перед релизом обязательны полный `go test ./...` для desktop/mobile модулей,
Flutter analyze/tests и `tools/preflight-release.ps1 -Build`.
