# dropo Installer Assets

Эта папка содержит ресурсы для создания установщика Windows.

## Структура

```
installer/
├── assets/
│   ├── icon.ico         # Иконка приложения
│   └── license.txt      # Файл лицензии
└── README.md
```

## Сборка

Для сборки приложения и установщика используйте скрипт из корня проекта:

```powershell
# Собрать всё (приложение + portable + installer)
.\build.ps1

# Только сборка приложения
.\build.ps1 -Build

# Только создание portable ZIP
.\build.ps1 -Portable

# Только создание установщика
.\build.ps1 -Installer

# Очистка и пересборка
.\build.ps1 -Clean -Build
```

Или через batch файл:
```batch
build.bat -all
```

## Требования для установщика

Для создания установщика необходим [NSIS](https://nsis.sourceforge.io/Download):

```bash
# Установка через winget
winget install NSIS.NSIS
```

## Выходные файлы

После сборки файлы будут в папке `release/`:

```
release/
├── {version}/                    # Папка версии
│   ├── dropo.exe                # Приложение
│   ├── sing-box.exe             # VPN движок
│   └── resources/
│       └── template.json        # Шаблон конфигурации
├── dropo-{version}.zip          # Portable версия
└── dropo-{version}-setup.exe    # Установщик
```
