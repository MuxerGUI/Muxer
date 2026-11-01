@echo off
cd /d "%~dp0"
set "PATH=C:\Program Files\nodejs;C:\Program Files (x86)\NSIS;C:\Program Files\Go\bin;%USERPROFILE%\go\bin;%PATH%"
set "CGO_ENABLED=0"

echo ==================================================
echo   Muxer Build Tool
echo ==================================================
echo.

for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-dd"') do set "BUILDTIME=%%i"

wails build -ldflags "-H windowsgui -X main.BuildTime=%BUILDTIME%" -nsis
if errorlevel 1 goto failed

echo.
echo Build OK!
echo   Portable: build\bin\Muxer.exe
echo   Installer: build\bin\Muxer-amd64-installer.exe
if exist "build\bin\keys.json" (
  echo.
  echo   [!] build\bin\keys.json holds your real API keys.
  echo       Never share the build\bin folder as-is.
  echo       Run the share-build script to make a clean package.
)
goto end

:failed
echo.
echo Build FAILED, check errors above.

:end
echo.
echo Press any key to exit...
pause >nul
