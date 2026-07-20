## dropo {{TAG}}

Этот тег и описание созданы GitHub Actions. Проверенные файлы Windows и Android
загружаются локальным publisher после завершения release gate.

### Скачать

| Платформа | Файл | Ссылка | Примечание |
| --- | --- | --- | --- |
| Windows x64 | `dropo-Windows-x64.exe` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Windows-x64.exe) | Запустите один EXE: приложение проверит и развернёт подписанный пакет в `%LOCALAPPDATA%\dropo\app\<версия>`. Ручная распаковка не нужна. |
| Windows Dependencies x64 | `{{DEPENDENCIES_ASSET}}` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{DEPENDENCIES_TAG}}/{{DEPENDENCIES_ASSET}}) | Движки VPN и обхода; приложение проверяет SHA-256 перед использованием. |
| Android arm64 | `dropo-Android-arm64.apk` | [Скачать](https://downloads.droponevedimka.ru/releases/download/{{TAG}}/dropo-Android-arm64.apk) | Для Android 11+ на arm64. |

Windows SHA-256: `__WINDOWS_SHA256_PENDING_LOCAL_UPLOAD__`

Android SHA-256: `__ANDROID_SHA256_PENDING_LOCAL_UPLOAD__`

### Изменения

- Windows и Android корректно находят новейший совместимый релиз через российский сервер загрузок.
- При холодном запуске приложение показывает доступное обновление и кнопку установки.
- Windows загружает, проверяет и запускает подписанное обновление, после чего перезапускается.
- Android открывает APK именно с российского сервера, даже если самый новый GitHub-релиз ещё не содержит Android-сборку.
- Версия Windows сразу отображается из метаданных сборки; значение `dev` больше не появляется в релизе.

> Для перехода с 3.0.0 и более ранних Windows-версий новый EXE нужно скачать вручную один раз: старый updater распознаёт только ZIP-артефакты.
