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

- Маршрут по умолчанию подтверждён интеграционным тестом: только заблокированные сервисы и каталоги используют бесплатный обход, затем VPN-подписку; обычный и неизвестный трафик остаётся `direct`.
- Автоматический подбор ограничен тремя различными локальными стратегиями. После неудачи используется VPN-подписка, а при её отсутствии сохраняется безопасный `direct` fallback.
- Steam и CS2 теперь имеют приоритетный `direct`-маршрут во всех split-routing режимах, включая `except_russia`; устаревшие кэшированные Windows-конфигурации автоматически перестраиваются.
- DNS, домены и процессы Steam/CS2 исключены из общего VPN-маршрута на Windows. На Android те же исключения применяются к доменам и пакетам Steam, но явный режим `all_traffic` остаётся строгим.
- Принудительно выбранная пользователем политика сервиса не подменяется автоматическим бесплатным перебором.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
