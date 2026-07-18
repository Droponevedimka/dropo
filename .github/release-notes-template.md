## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные файлы Windows и Android
загружаются локальным publisher после завершения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows x64 | `dropo-Windows-x64.exe` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{TAG}}/dropo-Windows-x64.exe) | Запустите один EXE: приложение проверит и развернёт подписанный пакет в `%LOCALAPPDATA%\dropo\app\<версия>`. Ручная распаковка не нужна. |
| Windows Dependencies x64 | `{{DEPENDENCIES_ASSET}}` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{DEPENDENCIES_TAG}}/{{DEPENDENCIES_ASSET}}) | Движки VPN и обхода; приложение проверяет SHA-256 перед использованием. |
| Android arm64 | `dropo-Android-arm64.apk` | [Скачать](https://github.com/{{REPOSITORY}}/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows SHA-256: `__WINDOWS_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Изменения

- Windows поставляется одним `dropo-Windows-x64.exe`: ручная распаковка больше не требуется, файлы разворачиваются в версионный каталог AppData с проверкой SHA-256.
- Discord: голосовой UDP, просмотр и публикация стримов автоматически используют VPN-подписку, когда прозрачного обхода недостаточно; явный Direct продолжает учитываться.
- zapret2: исправлен конфликт перекомпозиции `winws2 --dry-run` с уже работающим WinDivert-фильтром и восстановление предыдущей рабочей стратегии при ошибке.
- Первый запуск: автовосстановление VPN теперь дожидается защищённой загрузки компонентов; параллельные загрузки сериализованы.
- Обновление и release pipeline переведены с ZIP на подписанный self-extracting EXE, включая встроенный manifest smoke-test и поддержку EXE в автообновлении.

> Для перехода с 3.0.0 и более ранних Windows-версий новый EXE нужно скачать вручную один раз: старый updater распознаёт только ZIP-артефакты.
