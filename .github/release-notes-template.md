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

- Проверка обновлений снова находит новые Windows-релизы при каждой успешной инициализации как установленной, так и portable-версии.
- Исправлен ложный результат `обновлений нет`: российский gateway может возвращать только зеркалируемый Android APK, поэтому отсутствующие Windows Setup/Portable теперь ищутся в канонических GitHub-метаданных.
- Оба источника проверяются параллельно; недоступный gateway или GitHub не задерживает результат второго источника на полный таймаут.
- Установленная версия выбирает `dropo-Windows-Setup-x64.exe`, portable — `dropo-Windows-Portable-x64.zip`; выбор обоих режимов покрыт регрессионными тестами.
- Прямые GitHub-файлы разрешены только для точного репозитория `Droponevedimka/dropo`. Размер и SHA-256 остаются обязательными, а посторонние репозитории и редиректы отклоняются.
- Android updater не изменён и продолжает использовать проверенный APK российского gateway.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
