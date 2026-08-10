## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные Windows- и Android-файлы
загружаются локальным publisher после прохождения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows 10/11 x64 | `dropo-Windows-Setup-x64.exe` | [Установщик](https://github.com/Droponevedimka/dropo/releases/download/{{TAG}}/dropo-Windows-Setup-x64.exe) | Рекомендуемый автономный установщик: защищённый каталог, автозапуск по выбору и автоматические обновления. |
| Windows 10/11 x64 | `dropo-Windows-Portable-x64.zip` | [Portable](https://github.com/Droponevedimka/dropo/releases/download/{{TAG}}/dropo-Windows-Portable-x64.zip) | Не требует установки. При обновлении скачайте новый архив; профили и настройки сохраняются в AppData. |
| Android 11+ arm64 | `dropo-Android-arm64.apk` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows installer SHA-256: `__WINDOWS_INSTALLER_SHA256_PENDING_LOCAL_UPLOAD__`

Windows portable SHA-256: `__WINDOWS_PORTABLE_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Основные изменения

- Для YouTube добавлен новый нативный набор из пяти действительно разных стратегий. Приоритетный план повторяет актуальный класс zapret2: изменённый TLS fake ×11, копирование session ID, SNI `www.google.com`, SNI-aware multidisorder и отдельный QUIC decoy ×11.
- Исправлена QUIC-приманка, которая для типичного 1200-байтового Initial могла совпадать с исходным пакетом. Discord теперь перебирает разные bounded-комбинации QUIC/protocol, zero, invalid-checksum и low-TTL decoy-пакетов.
- Современный крупный TLS ClientHello распознаётся, когда полная SNI-секция уже доступна, даже если объявленная TLS-запись продолжается в следующем TCP-сегменте. Позиции разбиения вычисляются по реальной середине SNI.
- Без VLESS первый фоновый цикл проверяет до пяти разных стратегий; с подпиской сохранён быстрый лимит из трёх кандидатов и немедленный VPN fallback.
- Проверки всех целей сервиса выполняются параллельно с ограничением конкуренции и читают до 64 КиБ ответа. Оборванная после первых килобайт загрузка больше не помечает стратегию рабочей.
- Discord по-прежнему получает окончательный статус «работает» только после устойчивого двустороннего voice-медиапотока. Steam/CS2 остаются в узком direct-first пути и не попадают под широкий WinDivert-перехват.
- Изменения относятся к Windows traffic orchestrator. Android-маршрутизация не менялась; Android core, Flutter UI и arm64-сборка проверены отдельно.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
