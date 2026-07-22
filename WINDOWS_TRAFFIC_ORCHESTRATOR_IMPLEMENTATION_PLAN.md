# План реализации Windows Traffic Orchestrator

Статус: Windows-реализация завершена в коде; локальные release gates пройдены 2026-07-21
Целевая платформа первого этапа: Windows x64
Область: локальный обход DPI, классификация сервисов, WireGuard overlay, VPN fallback, поставка и диагностика

## 0. Фактический результат реализации

Завершено:

- `app/trafficorchestrator` реализует типизированные планы, классификацию,
  bounded packet actions, checksum, атомарную смену ревизии и единственный
  in-process WinDivert owner;
- classifier различает HTTP Host, TLS ClientHello/SNI, QUIC v1/v2 Initial/SNI,
  STUN, Discord discovery и WireGuard fingerprints; неуверенный трафик проходит
  без изменения;
- selector принимает web/TCP/UDP targets и фиксирует кандидат только при успехе
  всех обязательных целей, сохраняя baseline regression guards;
- подписки и отдельные ключи представлены упорядоченными `VPNSource`; внутри
  подписки выбирается один стабильный node fingerprint, а fallback выполняется
  только между источниками с circuit breaker, cooldown и hysteresis;
- рабочие WireGuard rules входят в `TrafficPlan` раньше сервисных правил;
- Windows EXE полностью самодостаточен, не скачивает исполняемые зависимости,
  проверяет file-level manifest, содержит notices, SPDX SBOM и provenance;
- build запрещает внешний anti-DPI runtime/Lua/Cygwin, подписывает вложенные PE и
  проверяет Authenticode, self-extraction, bridge lifecycle, WinDivert open/close
  и отсутствие дочерних процессов после Stop.

Локальный release gate проверяет артефакт
`release/dropo-<version>-<build-hash>/dropo-Windows-x64.exe`: Go/Flutter tests,
analyze/vet, artifact validation, administrator lifecycle, реальное открытие
WinDivert, sing-box TUN, cleanup и Microsoft Defender custom scan должны пройти
без ошибок и детектов перед публикацией каждой версии.

Внешние acceptance gates, которые нельзя честно заменить локальным тестом:

- Discord voice/video/Go Live в реально ограниченной сети клиента;
- чистая Windows VM с Defender cloud protection и SmartScreen reputation;
- production Authenticode certificate вместо локального pet/self-signed
  сертификата. Эти пункты не меняют архитектуру, но обязательны перед публичным
  объявлением production-ready.

Осознанное fail-safe ограничение первой версии: SNI извлекается из полного TLS
ClientHello либо полного QUIC CRYPTO ClientHello в одном захваченном Initial.
Фрагментированный handshake без достаточного доказательства не буферизуется и
проходит неизменённым; bounded cross-packet reassembly остаётся следующим
hardening-этапом после VM packet fixtures, чтобы не создать зависание потока.

## 1. Цель

Dropo должен поставлять один собственный Windows-движок перехвата трафика, который владеет одним экземпляром WinDivert и применяет независимо подобранную стратегию к каждому распознанному заблокированному сервису.

В продукте не должно быть внешнего процесса `winws2`, Lua runtime-файлов или пользовательской сущности `zapret2`. Открытые anti-DPI проекты используются только как исследовательские источники алгоритмов. Заимствованный код допускается только после отдельного аудита, с сохранением обязательных лицензий и указанием происхождения в `THIRD_PARTY_NOTICES.md`.

Основной порядок маршрутизации:

1. Системные исключения и локальная сеть.
2. Рабочие сети и явно включённые WireGuard-туннели.
3. Direct + локальная сервисная стратегия Windows Traffic Orchestrator.
4. Первый рекомендуемый узел выбранного VPN-источника.
5. Следующий VPN-источник в пользовательском порядке при недоступности предыдущего источника.
6. Direct или явная ошибка согласно политике сервиса, если ни один fallback недоступен.

## 2. Неподвижные архитектурные правила

- Ровно один процесс Dropo владеет WinDivert во время активной сессии.
- Ровно один WinDivert handle используется для общего фильтра исходящего payload-трафика; изменение сервисных стратегий выполняется атомарной заменой snapshot без перезапуска драйвера.
- Пакет классифицируется до применения стратегии. Стратегия одного сервиса не должна воздействовать на другой сервис или на произвольный HTTPS-трафик.
- Ни один probe не изменяет постоянную маршрутизацию до успешного завершения всех обязательных целей входного набора.
- Подбор считается успешным только если одна стратегия одновременно проходит все TCP/UDP/URL/port-проверки сервиса с заданным порогом повторов.
- Узлы внутри одной подписки не являются разными уровнями fallback. По умолчанию используется первый поддерживаемый узел в порядке поставщика; пользователь может выбрать другой узел вручную.
- Fallback происходит между отдельными VPN-источниками: подписками либо отдельными ключами.
- WireGuard overlay имеет приоритет над сервисным direct/VPN fallback и не должен быть незаметно заменён подпиской.
- Ошибка локального движка не должна оставлять пользователя без связи: сервис переводится на разрешённый VPN fallback, а UI и журнал показывают ограниченный режим.
- Никаких рекомендаций отключать Defender, скрывать payload или добавлять антивирусные исключения.

## 3. Целевая схема компонентов

### 3.1 `TrafficOrchestrator`

Go-компонент основного core, отвечающий за жизненный цикл и конфигурацию собственного Windows packet engine.

Обязанности:

- построение неизменяемого `TrafficPlan`;
- запуск и остановка одного Windows engine;
- атомарное применение новой ревизии плана;
- контроль heartbeat, generation и состояния WinDivert;
- получение статистики совпадений, пропусков, инъекций и ошибок по сервисам;
- fail-safe возврат оригинального пакета при неизвестном сервисе или внутренней ошибке.

### 3.2 `WindowsPacketEngine`

Windows-only runtime встроен в `dropo-core.exe` и динамически загружает официальный `WinDivert.dll` через типизированную обёртку. Внешний packet-engine процесс и control-script отсутствуют.

Engine не использует Lua. Все поддерживаемые действия представлены типизированными структурами:

- `FakePacketAction`;
- `SplitAction`;
- `DisorderAction`;
- `TTLAction`;
- `SequenceOverlapAction`;
- `RepeatAction`;
- `PassAction`.

Engine должен:

- разбирать IPv4/IPv6, TCP, UDP;
- распознавать HTTP Host, TLS ClientHello/SNI, QUIC Initial там, где расшифровка заголовка безопасно поддерживается;
- распознавать Discord/STUN/WireGuard по валидируемым сигнатурам, а не только по порту;
- пересчитывать checksum через WinDivert helper API;
- ограничивать очереди, объём reassembly и число синтетических пакетов;
- не иметь функций загрузки кода, shell-команд, сетевого скачивания или persistence;
- применять строгие лимиты памяти и времени на flow.

### 3.3 `ServiceClassifier`

Классификатор объединяет несколько независимых доказательств:

- домены и суффиксы из встроенного каталога сервисов;
- IP/CIDR-наборы с версией и временем обновления;
- TLS SNI/HTTP Host/QUIC SNI;
- protocol fingerprint для Discord media, STUN, WireGuard и иных поддержанных протоколов;
- endpoint learning из sing-box connection API для приложений, где адрес назначается динамически;
- process metadata только как вспомогательный сигнал, никогда как единственное основание для перехвата чужого трафика;
- TCP/UDP port constraints.

Результат классификации содержит `service_id`, `confidence`, `evidence`, `flow_key` и revision каталога. При конфликте правил применяется наиболее специфичное правило; недостаточная уверенность означает `pass`.

### 3.4 `StrategyCatalog`

Каталог хранит собственные декларативные стратегии Dropo. В runtime и пользовательской документации нет CLI-фрагментов или названий стороннего движка.

Пример модели:

```go
type TrafficStrategy struct {
    ID          string
    Revision    int
    TCP         []PacketAction
    UDP         []PacketAction
    Constraints StrategyConstraints
    Cost        StrategyCost
}
```

Каждая стратегия проходит:

- статическую валидацию;
- property/fuzz-тесты парсеров;
- packet fixture tests;
- проверку максимального amplification;
- проверку совместимости с IPv4/IPv6;
- интеграционный тест на Windows VM.

### 3.5 `StrategySelector`

Утилита принимает набор обязательных целей:

```json
{
  "serviceId": "discord",
  "targets": [
    {"network":"tcp","url":"https://discord.com/api/v10/gateway","port":443},
    {"network":"udp","host":"162.159.0.1","port":50000,"probe":"discord-media"},
    {"network":"udp","host":"stun.discord.com","port":3478,"probe":"stun"}
  ],
  "candidates":["strategy-a","strategy-b"],
  "attempts":3,
  "requiredSuccesses":2
}
```

Алгоритм:

1. Проверить и нормализовать все цели без запуска engine.
2. Снять baseline direct, сохранив тип сбоя: DNS, connect, reset, timeout, TLS, protocol, inbound-media.
3. Пропустить уже работающие direct-цели, но оставить их regression guard для кандидата.
4. Для каждого кандидата атомарно создать trial snapshot только для данного сервиса.
5. Проверить все цели, сохраняя несколько попыток и двусторонние признаки для UDP.
6. Немедленно отклонить кандидат, если он ломает baseline-working цель.
7. Принять только кандидат, прошедший порог для всех обязательных целей одновременно.
8. Выбрать минимальную стоимость при нескольких успешных кандидатах.
9. Сохранить результат по `network_fingerprint + service_catalog_revision + strategy_revision`.
10. При отсутствии общей стратегии вернуть типизированную ошибку `ErrNoCommonStrategy`; permanent state не менять.

Selector должен иметь библиотечный API, core API для UI/диагностики и CLI-режим для воспроизводимых инженерных тестов.

## 4. VPN-источники и fallback

### 4.1 Новая модель хранения

Текущая строка `ProfileData.SubscriptionURL` заменяется упорядоченным массивом:

```go
type VPNSource struct {
    ID             string
    Name           string
    Kind           string // subscription | direct_link
    URL            string
    Enabled        bool
    Priority       int
    SelectedNodeID string
    Nodes          []VPNNode
    Health         VPNSourceHealth
}
```

Правила:

- одна URL-подписка = один `VPNSource` с N узлами;
- один VLESS/Trojan/SS/VMess/Hysteria/TUIC ключ = отдельный `VPNSource` с одним узлом;
- порядок источников задаёт fallback chain;
- внутри источника автоматически берётся первый поддерживаемый узел провайдера;
- ручной выбор закрепляет `SelectedNodeID` и не меняет порядок источников;
- обновление подписки сохраняет выбор по стабильному node fingerprint, если узел ещё существует;
- секретные URL не выводятся в журналы и диагностические bundle.

### 4.2 Health и переключение

- Стартует первый enabled-источник.
- Проверяются DNS, TCP/TLS, UDP capability и контрольная внешняя цель.
- Ошибка отдельного узла не запускает перебор остальных узлов той же подписки автоматически.
- Неработоспособный источник переводит сервисную fallback-группу на следующий enabled-источник.
- Переключение имеет cooldown, circuit breaker и hysteresis, чтобы не флапать.
- Возврат к более приоритетному источнику выполняется только после нескольких успешных фоновых проверок.
- Пользовательское ручное переключение имеет приоритет над автоматическим восстановлением до конца сессии либо до явного сброса.

## 5. WireGuard и рабочие сети

- Активные пользовательские WireGuard-конфиги создают overlay-правила раньше правил заблокированных сервисов.
- Домены/CIDR рабочей сети всегда направляются в соответствующий WireGuard outbound/tunnel.
- После исключения рабочих адресов остальные сервисы проверяются classifier-ом и идут через direct + local strategy.
- Собственный WireGuard transport endpoint исключается из TUN/WinDivert рекурсии.
- Опциональная маскировка WireGuard использует отдельную scoped-стратегию только для handshake endpoint и не захватывает произвольный UDP.
- При нездоровом WireGuard туннеле его рабочие адреса не должны автоматически утекать в публичный VPN без отдельной пользовательской политики; по умолчанию fail closed для private destinations.

## 6. Каталог заблокированных сервисов

Для каждого сервиса хранятся:

- стабильный `service_id` и отображаемое имя;
- web/app/mobile variants;
- domain suffixes и точные hostnames;
- IP/CIDR с источником и сроком годности;
- TCP/UDP порты;
- protocol fingerprints;
- обязательные probe targets;
- допустимые fallback-политики;
- список стратегий в порядке стоимости;
- чувствительные адреса, которые нельзя писать в обычный лог.

Discord сохраняет отдельные классы:

- web/API/gateway TLS;
- voice signalling;
- voice/video/Go Live UDP media;
- STUN/IP discovery;
- dynamically learned media endpoints.

Веб-сайт и приложение могут быть работоспособны независимо, поэтому успешная HTTP-проверка не считается доказательством работоспособности voice/video.

## 7. Поставка и безопасность

- Один подписанный Windows installer EXE содержит UI, core, packet engine, WinDivert и VPN binaries.
- Клиент не скачивает runtime-зависимости при первом запуске.
- Установка выполняется атомарно после UAC; повторный запуск только проверяет manifest и открывает приложение.
- Обновление скачивает новый подписанный installer EXE, проверяет publisher, SHA-256 и anti-rollback version, затем атомарно переключает установленную версию.
- В build pipeline закрепляются SHA сторонних Actions, toolchain и бинарных зависимостей.
- Формируются SBOM, third-party notices, dependency manifest и provenance.
- Подписываются installer, launcher, core и packet engine.
- Перед публикацией выполняются Defender cloud scan, SmartScreen check и отправка ложноположительных файлов Microsoft при необходимости.

## 8. Миграция кода

### Этап A — контракты и чистые модели

- Ввести `TrafficPlan`, `ServiceRule`, `TrafficStrategy`, `ProbeTarget`, `StrategySelectionResult`.
- Заменить строковые CLI-аргументы в сервисном каталоге на типизированные действия.
- Создать валидатор плана и deterministic JSON representation.
- Добавить unit/property/fuzz tests.

### Этап B — собственный Windows engine

- Добавить минимальную WinDivert binding-обёртку.
- Реализовать безопасные packet parsers и flow table.
- Реализовать `pass`, fake TTL/checksum, TCP split/overlap/disorder и UDP fake ограниченным набором.
- Добавить атомарный control protocol и heartbeat.
- Подключить Job Object и гарантированное завершение.

### Этап C — сервисная композиция

- Перевести hostlists/ipsets/Discord endpoint learning в `ServiceClassifier` snapshots.
- Заменить restart-based recomposition на atomic plan update.
- Оставить один WinDivert handle на всю сессию.
- Добавить per-service counters и диагностические причины match/miss.

### Этап D — общий selector

- Реализовать multi-target evaluator.
- Перевести first-run search и retune на один кодовый путь.
- Запретить частичный commit стратегии.
- Добавить cache versioning и network-change invalidation.

### Этап E — VPN sources

- Поднять schema version и мигрировать старую подписку в один `VPNSource`.
- Поддержать несколько подписок и одиночных ключей.
- Генерировать отдельные outbound/selectors для каждого источника.
- Реализовать inter-source fallback и ручной node selection.

### Этап F — WireGuard overlay

- Формализовать приоритет рабочих маршрутов.
- Добавить leak-prevention tests.
- Перевести WireGuard camouflage на scoped traffic strategy.

### Этап G — удаление старого runtime

- Удалить `winws2.exe`, `cygwin1.dll`, Lua и старые fake/filter-файлы из required dependencies.
- Удалить CLI composer, dry-run запуска внешнего процесса и связанные retry/cleanup ветки.
- Добавить безопасную очистку только старых app-owned runtime-каталогов и процессов при миграции.
- Не трогать сторонние экземпляры WinDivert/других приложений; показывать конфликт до запуска.

### Этап H — UI, документация и инструкции

- UI: список VPN-источников с reorder/enable/health и выбором узла внутри источника.
- UI: состояние local strategy / VPN fallback по каждому сервису.
- UI: запуск selector с отображением матрицы целей без чувствительных данных.
- README и tools: использовать только термины Dropo Traffic Orchestrator.
- Ссылки на исследованные проекты оставить только в разделе `Вдохновение и лицензии`.
- Обновить `AGENTS.md`: запретить возвращение внешнего anti-DPI runtime, Lua/CLI-стратегий и непроверенных dependency downloads.

## 9. Тестовая матрица

### Unit

- packet parsing bounds и malformed inputs;
- classifier specificity/conflicts;
- strategy validation и amplification limits;
- atomic plan revisions;
- multi-target all-or-nothing selection;
- subscription migration/order/manual node selection;
- inter-source fallback circuit breaker;
- WireGuard route precedence.

### Fuzz/property

- IPv4/IPv6/TCP/UDP/TLS/QUIC parsers;
- checksum and split reconstruction;
- service catalog decoder;
- control protocol decoder;
- subscription parsing and stable node fingerprint.

### Integration без драйвера

- packet fixture replay через in-memory engine;
- synthetic DPI testlab для каждой стратегии;
- mixed-service flows через один snapshot;
- selector с TCP+UDP targets;
- simulated source failures and recovery.

### Windows integration

- один WinDivert handle/process;
- Discord web, voice, video, stream receive и Go Live;
- одновременные Discord + YouTube + обычный HTTPS;
- active WireGuard overlay + blocked service;
- Defender enabled, clean install/update/uninstall;
- process/service cleanup после connect, disconnect, crash и update;
- конфликт с внешним WinDivert приложением;
- IPv4/IPv6 и смена сети.

### Release gates

- `go test ./...`;
- `go vet ./...`;
- Flutter analyze/tests;
- Windows full build через Bash entrypoint;
- dependency/SBOM/provenance validation;
- Authenticode verification всех EXE/DLL/MSI/CAB, где применимо;
- clean Windows VM smoke test;
- отсутствие runtime downloads;
- отсутствие запрещённых файлов/терминов в release payload.

## 10. Критерии завершения

- В release нет `winws2.exe`, Lua anti-DPI runtime или Cygwin.
- В активной сессии ровно один Dropo-owned WinDivert engine и один driver service instance.
- Каждая включённая сервисная стратегия изолирована правилами классификатора.
- Selector принимает стратегию только при успехе всех обязательных целей.
- Discord web/voice/video/streams проверяются раздельно и совместно.
- Несколько VPN-источников работают как упорядоченный fallback, а узлы одной подписки не смешиваются с ним.
- Рабочие WireGuard-маршруты имеют более высокий приоритет и защищены от утечки.
- При отказе local strategy сервис автоматически использует разрешённый VPN fallback.
- Первый запуск и обновление не скачивают зависимости отдельно от подписанного installer.
- Документация, UI и логи описывают только собственную архитектуру Dropo; исследовательские источники перечислены отдельно вместе с лицензиями.

## 11. Порядок текущей реализации

1. Добавить чистые модели и валидатор плана.
2. Перевести сервисный каталог с CLI-строк на типизированные стратегии.
3. Добавить in-memory packet engine и selector, покрыть тестами.
4. Добавить Windows WinDivert adapter и один engine lifecycle.
5. Подключить classifier/Discord learning и убрать restart-based composition.
6. Мигрировать VPN storage/config/UI на `VPNSource[]`.
7. Реализовать inter-source fallback и WireGuard precedence.
8. Удалить старый runtime из приложения и build pipeline.
9. Обновить документацию и `AGENTS.md`.
10. Выполнить полный build/security/release audit.
