## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные Windows- и Android-файлы
загружаются локальным publisher после прохождения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows 10/11 x64 | `dropo-Windows-x64.exe` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Windows-x64.exe) | Один самораспаковывающийся EXE уже содержит UI, core, sing-box, Xray, WireGuard и официальный WinDivert. Сетевой runtime при запуске не скачивается. |
| Android 11+ arm64 | `dropo-Android-arm64.apk` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows SHA-256: `__WINDOWS_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Основные изменения

- Windows переведён на собственный in-process Traffic Orchestrator с одним владельцем WinDivert и атомарными стратегиями по сервисам.
- Стратегия принимается только после одновременного успеха обязательных TCP, UDP и web-проверок; частичный результат не применяется.
- TLS, HTTP Host и QUIC v1/v2 Initial SNI классифицируются внутри ограниченного и отказоустойчивого packet pipeline; неуверенный трафик проходит без изменения.
- Discord web, signalling, STUN и voice/video/Go Live media классифицируются и диагностируются раздельно; зашифрованный media payload не изменяется.
- Несколько VPN-подписок и отдельных ключей образуют упорядоченный fallback между источниками. Внутри подписки используется первый рекомендуемый узел либо ручной выбор пользователя.
- Рабочие WireGuard-сети имеют приоритет над direct, локальными стратегиями и VPN fallback.
- Windows EXE содержит полный runtime. На первом запуске payload проверяется и разворачивается в AppData, а повышенный core переносит проверенные native-компоненты в защищённый ProgramData runtime без сетевой загрузки.
- Из поставки удалены внешний anti-DPI-процесс, Lua runtime и Cygwin; официальный WinDivert 2.2.2 закреплён по SHA-256.
- Вложенные PE-файлы проверяются по Authenticode; пакет содержит file manifest, SPDX SBOM и provenance, связанный с ревизией исходников и составом runtime.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
