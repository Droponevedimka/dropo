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

- В автоматическом режиме весь Discord realtime-контур (voice, video и Go Live) сразу использует UDP-совместимый узел подписки; web/API продолжает работать через отдельную стратегию zapret2.
- Убраны перезапуски `winws2` при смене динамического Discord endpoint и экспериментальные fake-пакеты для зашифрованного RTP, которые обрывали активный голос и стримы.
- Прямой резервный маршрут использует стабильный официальный профиль Discord discovery/STUN и zapret2 1.0.3 с актуальными UDP-диапазонами `19294–19344` и `50000–50099`.
- После смены VPN-узла или перехода на direct клиент гарантированно переподключает текущие voice/video-соединения к новому outbound.
- Диагностика Discord теперь пишет выбранный маршрут и узел, цепочки outbound, endpoint, счётчики и дельты трафика, состояние медиапотока и результат каждого принудительного переподключения.

> Для перехода с 3.0.0 и более ранних Windows-версий новый EXE нужно скачать вручную один раз: старый updater распознаёт только ZIP-артефакты.
