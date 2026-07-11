# dropo Flutter UI

Production Flutter UI for Windows and Android. Windows uses the local HTTP
bridge to `dropo-core.exe`; Android uses a method channel and a gomobile AAR.

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

The repository build script prepares the gomobile bridge before compiling an
Android APK:

```powershell
cd ..
.\build.ps1 -Android
```

Do not run `flutter build apk` directly unless
`android/app/libs/dropoandroid.aar` has already been generated.
