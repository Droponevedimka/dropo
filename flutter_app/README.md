# dropo Flutter UI

Production Windows UI for dropo. The Flutter runner starts the bundled `dropo-core.exe` on Windows and talks to it through the local HTTP bridge.

## Hot reload

Start the Go core bridge:

```powershell
cd ..\app
go run . --no-tray --listen 127.0.0.1:17890
```

Start Flutter:

```powershell
cd ..\flutter_app
flutter run -d windows --dart-define=DROPO_CORE_ENDPOINT=http://127.0.0.1:17890
```

Inside the Flutter terminal:

- `r` hot reload
- `R` hot restart
- `q` quit

## Checks

```powershell
flutter analyze
flutter test
flutter build windows --release
```