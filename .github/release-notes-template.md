## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные Windows- и Android-файлы
загружаются локальным publisher после прохождения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows 10/11 x64 | `dropo-Windows-Setup-x64.exe` | [Установщик](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Windows-Setup-x64.exe) | Рекомендуемый автономный установщик: защищённый каталог, автозапуск по выбору и автоматические обновления. |
| Windows 10/11 x64 | `dropo-Windows-Portable-x64.zip` | [Portable](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Windows-Portable-x64.zip) | Не требует установки. При обновлении скачайте новый архив; профили и настройки сохраняются в AppData. |
| Android 11+ arm64 | `dropo-Android-arm64.apk` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows installer SHA-256: `__WINDOWS_INSTALLER_SHA256_PENDING_LOCAL_UPLOAD__`

Windows portable SHA-256: `__WINDOWS_PORTABLE_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Основные изменения

- Актуальный каталог Re-filter хранится в репозитории, проверяется при каждой сборке и входит в runtime, защищённый file-level manifest; при запуске приложение больше не скачивает списки блокировок.
- Для известных сервисов сохраняются отдельные типизированные DPI-стратегии, а для остальных доменов и IP из каталога выбирается одна общая in-process стратегия.
- Общая стратегия принимается только после успешной параллельной проверки четырёх случайных заблокированных доменов. При неудаче используется VPN-подписка, а при её отсутствии — direct.
- Именованные сервисы исключаются из общего правила, рабочие сети и WireGuard имеют высший приоритет, неуверенный трафик проходит без изменения.
- Большой каталог индексирован по доменным и IP-префиксам и не перебирается линейно для каждого пакета.
- Исправлены настройки Windows и Android: тема применяется сразу, значения строго валидируются, ошибки сохранения откатываются, индивидуальное отключение сервисов реально меняет маршрут.
- Удалены неработающие элементы интерфейса для английского языка и системных уведомлений; изменение логирования во время активного VPN безопасно блокируется.
- Оба Windows-пакета содержат полный runtime, file manifest, SPDX SBOM и provenance; внешний anti-DPI-процесс не используется.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
