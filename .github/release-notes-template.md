## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные файлы Windows и Android
загружаются локальным publisher после завершения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows Portable x64 | `dropo-Windows-Portable-x64.zip` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{TAG}}/dropo-Windows-Portable-x64.zip) | Распакуйте архив и запустите `dropo.exe`. Сертификат и ручные скрипты находятся в `resources/cert/`. |
| Windows Dependencies x64 | `{{DEPENDENCIES_ASSET}}` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{DEPENDENCIES_TAG}}/{{DEPENDENCIES_ASSET}}) | Движки VPN и обхода; приложение проверяет SHA-256 перед использованием. |
| Android arm64 | `dropo-Android-arm64.apk` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows SHA-256: `__WINDOWS_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Изменения

- WireGuard: добавлена опциональная Windows-маскировка handshake через zapret2 с точным ограничением по endpoint IP/UDP-порту и автоматическим безопасным откатом.
- Windows Unified: повышена устойчивость подбора стратегий, восстановления selector-ов и повторных проверок после временных сетевых ошибок.
- Безопасность: Clash API теперь использует случайные loopback-порт и секрет, все внутренние запросы проходят аутентификацию.
- Надёжность: исправлены очистка нативных процессов и WinDivert-сервисов, ложные рестарты idle WireGuard и восстановление после аварийного завершения.
- Сборка: усилены CI/preflight-проверки версий, PowerShell-скриптов, подписей Windows и Android-релиза.
