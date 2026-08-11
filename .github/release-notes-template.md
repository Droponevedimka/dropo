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

- Исправлена маршрутизация Riot Games и League of Legends на Windows: игровые и клиентские соединения больше не попадают в VLESS/общий foreign-traffic fallback в split-routing режимах.
- Домены `riotgames.com`, `riotcdn.net`, `pvp.net` и `leagueoflegends.com` получили высокоприоритетный direct-маршрут и прямой DNS до правил заблокированного каталога.
- Реальные процессы Riot/League, включая `League of Legends.exe`, `LeagueClient*`, `RiotClient*`, `Riot Client.exe`, `vgc.exe` и `vgm.exe`, направляются напрямую. Это защищает IP-only игровой трафик, для которого доменное имя не видно.
- Сохранённые конфигурации предыдущих версий автоматически пересобираются при первом подключении, если в них ещё нет полного Riot direct-контура.
- Подтверждён базовый контракт `blocked_only`: рабочие сети имеют приоритетный WireGuard-маршрут, локальные/private-сети идут напрямую, только именованные заблокированные сервисы и bundled списки доменов/IP используют обход/VPN fallback, весь остальной трафик имеет `final=direct`.
- Явный пользовательский режим `all_traffic` остаётся полностью через VPN. Сохранённый `except_russia` продолжает проксировать остальной иностранный трафик, но latency-sensitive Steam/CS2 и Riot/League остаются напрямую.
- На Android добавлен эквивалентный direct-контур Riot для доменов и пакетов Wild Rift/TFT; версия схемы конфигурационного кэша повышена, чтобы новые правила гарантированно применились после обновления.

> При конфликте с другим VPN или WinDivert-приложением dropo показывает найденные процессы, адаптеры и packet-filter services до подключения.
