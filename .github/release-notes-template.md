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

- Восстановлён жёсткий split-routing контракт: рабочие сети имеют приоритетный WireGuard-маршрут, локальные/private-сети идут напрямую, обход/VLESS получают только положительно классифицированные заблокированные цели, весь остальной трафик имеет `final=direct`.
- Сохранённый в старых версиях режим `except_russia` автоматически мигрирует в `blocked_only`; из настроек убран широкий foreign-traffic режим. Остаётся только явный пользовательский `all_traffic`.
- Bundled IP-каталог больше не маршрутизирует по слишком широким сетям общих CDN/хостингов. Автоматически применяются только точные диапазоны до 16 адресов; надёжные CIDR именованных сервисов остаются отдельными правилами.
- Исправлена Windows-маршрутизация Riot/League: доменный, DNS, process-aware и IP-only игровой трафик остаётся в direct-контуре и не попадает в VLESS fallback.
- Сохранённые Windows-конфигурации предыдущих версий автоматически пересобираются при первом подключении; миграция политики не блокирует connection-ready gate.
- Android получил ту же миграцию широкого режима в `blocked_only`, а схема кэша маршрутов обновлена, чтобы новые правила гарантированно применились после обновления.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
