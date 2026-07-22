# dropo

**dropo** — клиент для локального обхода блокировок и подключения к VPN. Windows
сначала использует собственные сервисные стратегии без удалённого сервера, а
при подтверждённой неработоспособности переводит только затронутый сервис в
упорядоченную цепочку VPN-источников.

> [!WARNING]
> Единственный официальный источник dropo — репозиторий
> [Droponevedimka/dropo](https://github.com/Droponevedimka/dropo). Не запускайте
> сборки из сторонних источников.

## Скачать

| Платформа | Файл |
| --- | --- |
| Windows 10/11 x64 | Последний `dropo-Windows-x64.exe` на [странице релизов](https://github.com/Droponevedimka/dropo/releases) |
| Android 11+ arm64 | [dropo-Android-arm64.apk](https://github.com/Droponevedimka/dropo/releases/latest/download/dropo-Android-arm64.apk) |

## Первый запуск Windows

1. Запустите единый `dropo-Windows-x64.exe`; вручную распаковывать архив не нужно.
2. Bootstrap проверит встроенный manifest и каждый файл, затем развернёт версию в `%LOCALAPPDATA%\dropo\app\<версия>`.
3. Подтвердите UAC-запрос привилегированного core. Он проверит подписанный runtime-manifest и локально подготовит защищённую копию native-компонентов в `%PROGRAMDATA%\dropo\runtime\<runtime-id>`.
4. Нажмите «Подключить». Исполняемые зависимости на первом запуске и при подключении из сети не скачиваются.

Повторный запуск проверяет уже установленный payload и сразу открывает приложение.
Обновление скачивает новый подписанный EXE, проверяет издателя, SHA-256 и версию,
после чего запускает обычную атомарную установку новой версии.

## Архитектура Windows

- sing-box TUN отвечает за системную маршрутизацию и VPN outbounds;
- встроенный Traffic Orchestrator владеет ровно одним WinDivert handle;
- immutable `TrafficPlan` меняется атомарно без запуска скриптов и внешнего anti-DPI-процесса;
- классификатор использует домены, CIDR, TCP/UDP-порты, protocol fingerprints
  и SNI из TLS либо QUIC v1/v2 Initial;
- непрозрачный или неуверенно распознанный трафик проходит без изменения;
- ошибка преобразования fail-safe возвращает исходный пакет;
- рабочие WireGuard-сети применяются раньше сервисного direct/VPN fallback.

Для каждого сервиса selector проверяет одну стратегию сразу на всех обязательных
web/TCP/UDP-целях. Если общей стратегии нет, permanent state не меняется, а
сервис использует direct либо следующий разрешённый VPN-источник.

Discord разделён на web/API, gateway signalling, STUN/discovery и динамические
voice/video/Go Live media endpoints. Успешная загрузка сайта сама по себе не
считается подтверждением работоспособности голоса или трансляций.

## VPN-источники

- одна подписка — один источник с N узлами;
- отдельный VLESS, VMess, Trojan, Shadowsocks, Hysteria2 или TUIC ключ — отдельный источник;
- по умолчанию выбирается первый поддерживаемый узел в порядке поставщика;
- пользователь может вручную закрепить другой узел внутри источника;
- автоматический fallback идёт на следующий добавленный источник, а не на соседний узел той же подписки;
- порядок, включение и выбор узла сохраняются в профиле;
- секретные URL и ключи не записываются в обычные журналы.

## WireGuard и рабочие сети

Нативные WireGuard-конфиги формируют overlay поверх сервисных правил. Внутренние
домены и AllowedIPs остаются в рабочем туннеле и не утекают в публичный VPN.
Опциональная защита handshake ограничена точным endpoint IP/UDP port и
автоматически откатывается на текущую сессию при нездоровом handshake.

## Диагностика и конфликты

Перед подключением Windows-проверка показывает активные VPN-адаптеры, системные
VPN-подключения, сторонние DPI-процессы и WinDivert services, которые могут
конфликтовать с единственным packet-filter owner. dropo завершает только свои
дочерние процессы и никогда не убивает найденные сторонние экземпляры.

Логи находятся в `%LOCALAPPDATA%\dropo\logs`. Для подробной packet-диагностики
можно задать `DROPO_TRAFFIC_PACKET_DEBUG=1` или создать `traffic-debug.txt` рядом
с установленным launcher.

## Поставка и безопасность

Windows EXE содержит UI, core, sing-box, Xray, WireGuard, Wintun, официальный
WinDivert и встроенные rule sets. Сборка закрепляет версии и SHA-256 бинарных
источников, подписывает вложенные PE-файлы, создаёт file-level runtime manifest,
SPDX 2.3 SBOM и provenance statement, а также запрещает попадание внешнего
anti-DPI runtime, Lua и Cygwin в release payload. Отключать Defender или добавлять
антивирусные исключения не требуется и не рекомендуется.

## Исследовательские источники и лицензии

Архитектура packet engine изучает открытые техники и изменения upstream, но не
поставляет их процессы, сценарии или runtime. Список источников вдохновения,
фактически включённых компонентов и обязательных лицензий находится в
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Android

Android-версия пока использует VPN-подписку для заблокированных сервисов.
Dropo Space создаёт отдельный рабочий профиль для приложений, которым нужен
прямой доступ вне VPN основного профиля.

## Разработка и проверка

Детальный Windows-first план и критерии готовности находятся в
[WINDOWS_TRAFFIC_ORCHESTRATOR_IMPLEMENTATION_PLAN.md](WINDOWS_TRAFFIC_ORCHESTRATOR_IMPLEMENTATION_PLAN.md).
Основные release gates: Go unit/cross-compile, Flutter analyze/tests, проверка
runtime manifest, self-extraction smoke, Authenticode и Windows lifecycle smoke.

## Лицензия

MIT License © 2026 dropo
