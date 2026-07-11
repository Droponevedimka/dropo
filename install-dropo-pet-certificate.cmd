@echo off
setlocal
powershell.exe -NoLogo -NoProfile -ExecutionPolicy Bypass -File "%~dp0install-dropo-pet-certificate.ps1"
if errorlevel 1 (
  echo.
  echo Certificate installation failed or was cancelled.
  pause
  exit /b 1
)
echo.
pause
