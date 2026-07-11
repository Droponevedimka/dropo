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

- Android: Dropo Space автоматически добавляет установленные приложения в рабочий профиль и восстанавливает ранее созданные профили.
- Android: ручная установка открывается только после подтверждения пользователя и сопровождается пошаговой инструкцией.
- Безопасность: URL, серверы и учётные данные VPN-подписки скрыты в клиентских логах.
- Зависимости: обновлены systray и Kotlin, версии Flutter и приложения синхронизированы.
- Windows: неиспользуемый SpoofDPI удалён из приложения и архива движков; ByeDPI и zapret остаются активными методами обхода.
