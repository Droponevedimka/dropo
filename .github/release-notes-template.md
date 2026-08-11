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

- Исправлена ложная маршрутизация обычного трафика через VLESS: service-specific IP-источники больше не участвуют в глобальном blocked catch-all.
- В `blocked_only` доменная классификация теперь отделена от IP-классификации. Подтверждённо заблокированные домены идут через обход, любой другой известный домен — напрямую, и только IP-only соединения проверяются по точному blocked-IP каталогу.
- Из bundled IP-списков всех автоматически компилируемых источников удаляются широкие сети общих CDN/хостингов. Допускаются только диапазоны до 16 адресов.
- Сохранённые конфигурации v3.0.22 автоматически распознаются и пересобираются при первом подключении, поэтому ошибочный catch-all не сохраняется после обновления.
- Discord desktop и voice продолжают использовать собственную process/domain/media политику и не зависят от глобального IP-списка.
- Android проверен отдельно: он не использует Windows `.srs` catch-all, а его `blocked_only` по-прежнему оставляет весь неклассифицированный трафик напрямую.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
