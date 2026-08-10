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

- Фоновый подбор стратегий теперь виден в интерфейсе: проверяемые сервисы поднимаются вверх списка, показывают spinner, текущую стратегию и номер попытки.
- Если очередная стратегия не прошла полную проверку, активное окно показывает верхнее предупреждение о переходе к следующей стратегии.
- При наличии VLESS выполняется быстрый ограниченный цикл и немедленный fallback на подписку. Без подписки запускается отменяемая фоновая кампания до 12 циклов и не более одного часа.
- Discord не помечается рабочим по HTTP/API или discovery-ответу: окончательный успех требует устойчивого двустороннего voice-медиапотока; дальнейшие попытки согласованы с общей атомарной политикой сервиса.
- Устранена гонка между Discord realtime-monitor и общей перекомпозицией Windows `TrafficPlan`; остановка VPN отменяет оставшиеся фоновые попытки текущей сессии.
- Общий Flutter-интерфейс сохраняет совместимость с Android-событиями; Android core, UI-тесты и arm64-сборка проходят без изменения Android-маршрутизации.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
