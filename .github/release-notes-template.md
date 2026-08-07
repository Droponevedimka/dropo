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

- Discord voice/video/Go Live проверяется по устойчивому двустороннему медиапотоку; HTTP/API без работающего voice больше не кэшируется как успешный результат.
- При voice-stall Windows атомарно перебирает три действительно разные Discord-стратегии для всей политики сервиса, затем переводит весь Discord на проверенный VPN fallback.
- Steam и CS2 исключены из общего blocked-каталога и получают приоритетный direct-route; обычные игровые UDP-пакеты больше не проходят через WinDivert user mode.
- Перехват Windows ограничен распознаваемыми handshake-пакетами, буферы batch receive переиспользуются, а большой blocked-каталог кэшируется между ревизиями плана.
- Встроенные Re-filter списки обновлены до `01082026`; Android-маршрутизация Discord по умолчанию остаётся целиком через VPN, а Steam — напрямую в режиме «только заблокированные».

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.

### Совместимость автообновления

Для встроенного автообновления и клиентов старых версий публикуется byte-identical
alias `dropo-Windows-x64.exe`. Основные пользовательские файлы — Setup и Portable
из таблицы выше.
