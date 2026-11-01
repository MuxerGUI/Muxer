@echo off
cd /d "%~dp0"
set "PATH=C:\Program Files\nodejs;C:\Program Files (x86)\NSIS;C:\Program Files\Go\bin;%USERPROFILE%\go\bin;%PATH%"
set "CGO_ENABLED=0"
set "SHAREDIR=build\share"

echo ==================================================
echo   Muxer Share Build (NO keys.json)
echo ==================================================
echo.

for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-dd"') do set "BUILDTIME=%%i"

wails build -ldflags "-H windowsgui -X main.BuildTime=%BUILDTIME%" -nsis
if errorlevel 1 goto failed

if exist "%SHAREDIR%" rd /s /q "%SHAREDIR%"
mkdir "%SHAREDIR%"
if errorlevel 1 goto failed

copy /y "build\bin\Muxer.exe" "%SHAREDIR%\" >nul
if errorlevel 1 goto failed
if exist "build\bin\Muxer-amd64-installer.exe" copy /y "build\bin\Muxer-amd64-installer.exe" "%SHAREDIR%\" >nul
copy /y "keys.example.json" "%SHAREDIR%\" >nul

if exist "%SHAREDIR%\keys.json" del /f /q "%SHAREDIR%\keys.json"

echo.
echo Verifying...
if exist "%SHAREDIR%\keys.json" (
  echo   [FAIL] keys.json found in share dir, aborting.
  goto failed
)
echo   [OK] No keys.json in share dir.
echo.
echo Share package ready: %SHAREDIR%
dir /b "%SHAREDIR%"
echo.
echo Send this folder ^(or just Muxer.exe / the installer^).
echo Do NOT send build\bin - it contains your real keys.json.
goto end

:failed
echo.
echo Share build FAILED, check errors above.

:end
echo.
echo Press any key to exit...
pause >nul
