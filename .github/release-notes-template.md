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

- Discord Go Live: просмотр и публикация трансляций больше не переключаются с подобранного zapret2-маршрута в VLESS. В автоматическом режиме UDP Discord закреплён за `direct + winws2`; VPN используется только при явном выборе.
- Discord media: профиль приведён к актуальной схеме zapret2 `discord,stun`, поддерживает динамический порт медиасервера и не отправляет лишние fake-пакеты.
- Конфликты: перед подключением теперь обнаруживаются запущенные `winws`, `winws2`, `nfqws`, GoodbyeDPI и сторонний WinDivert, даже если они не создают VPN-адаптер.
- Завершение: dropo дожидается подтверждённого выхода `winws2`, Telegram proxy, ByeDPI и Xray; очистка Windows также ждёт фактического завершения каждого найденного процесса.
- Windows по-прежнему поставляется одним `dropo-Windows-x64.exe`; ручная распаковка не требуется.

> Для перехода с 3.0.0 и более ранних Windows-версий новый EXE нужно скачать вручную один раз: старый updater распознаёт только ZIP-артефакты.
