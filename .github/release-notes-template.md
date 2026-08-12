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

- Исправлена системная причина повышенного пинга и разрывов EA, Riot, Steam и других незаблокированных приложений на IP, совместно используемых с CDN или заблокированным сервисом.
- Windows Traffic Orchestrator больше не считает CIDR самостоятельным доказательством сервиса: named-service IP требует домен, процесс или media fingerprint, а общий blocked-IP каталог работает только без известного hostname.
- Service-specific правила sing-box теперь связывают IP с процессом; независимый IP больше не может отправить чужое приложение через бесплатную стратегию или VPN.
- Android не создаёт unscoped VPN-правила по IP сервиса: домены и package identity применяются раньше terminal known-domain `direct`, а весь неклассифицированный трафик остаётся прямым.
- Сохранённые Windows- и Android-конфигурации предыдущей версии автоматически распознаются по структурному/schema gate и пересобираются до подключения.
- Добавлена пакетная эмуляция общей IP-площадки: Discord SNI на тестовом адресе получает стратегию, а EA SNI на том же адресе возвращается одним побайтно неизменённым пакетом.
- Сохранены Discord voice/media и принудительный Telegram VPN: они используют подтверждённый media fingerprint либо строгую пару `IP + process/package`.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
