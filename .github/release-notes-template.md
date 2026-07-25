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

- Windows больше не показывает устаревшее жёлтое уведомление «Нужны компоненты» для installer и portable: оба пакета явно определяются как self-contained.
- Временная подготовка или ошибка целостности встроенного runtime больше не трактуется как необходимость сетевой загрузки. Локальная проверка и восстановление остаются доступны в настройках.
- Экран настроек получил собственный ScrollController: устранено исключение Flutter при переходе с главной страницы и проверена компактная компоновка окна 960×640.
- Русская навигация и диагностика используют согласованные подписи «О приложении» и «официальный пакет» для installer и portable.
- Оба Windows-пакета по-прежнему содержат полный runtime, file manifest, SPDX SBOM и provenance; исполняемые зависимости при запуске не скачиваются.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.

### Совместимость автообновления

Для встроенного автообновления и клиентов старых версий публикуется byte-identical
alias `dropo-Windows-x64.exe`. Основные пользовательские файлы — Setup и Portable
из таблицы выше.
