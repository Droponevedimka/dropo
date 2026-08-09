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

- Принудительный маршрут сервиса теперь является строгой политикой: `VPN` не включает бесплатный перебор, а неизвестные значения API отклоняются.
- На Windows маршрут Discord или другого сервиса можно менять при активном VPN: dropo транзакционно сохраняет политику, перестраивает конфигурацию и безопасно переподключается; при ошибке возвращает прежний маршрут.
- Хранилище настроек больше не отдаёт изменяемые map по ссылке и атомарно откатывает память при ошибке записи; откат после неудачной перестройки конфигурации покрыт регрессиями.
- Discord voice/video/Go Live подтверждается только устойчивым двусторонним медиапотоком. Автоматический режим быстро проверяет не более трёх разных локальных стратегий и затем использует VLESS/VPN-подписку, если она доступна.
- Steam и CS2 сохраняют приоритетный direct-route; Android-маршрутизация и безопасный запрет смены политики во время активного VpnService проверены отдельно.
- Элементы настроек получили контрастные цвета и корректно отражают сохранённый маршрут после обновления данных.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
