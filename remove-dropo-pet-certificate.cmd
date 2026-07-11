@echo off
setlocal
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0remove-dropo-pet-certificate.ps1"
if errorlevel 1 (
  echo.
  echo Certificate removal failed.
  pause
  exit /b 1
)
echo.
pause
