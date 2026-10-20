@echo off
REM Build backup.exe: Go size optimization + UPX compression.
REM Output goes to release\ (backup.exe + a starter config.yaml), so it can be
REM shipped / uploaded to a GitHub Release as a self-contained folder.
setlocal
cd /d "%~dp0"

set GOPROXY=https://goproxy.cn,direct

if not exist release mkdir release

echo [1/4] go build (-s -w -H=windowsgui -trimpath)...
pushd src
go build -ldflags "-s -w -H=windowsgui" -trimpath -o ..\release\backup.exe .
if errorlevel 1 (
  popd
  echo build failed
  exit /b 1
)
popd
for %%F in (release\backup.exe) do echo     built: %%~zF bytes

echo [2/4] UPX compress...
where upx >nul 2>&1
if errorlevel 1 goto noup
upx --best --no-backup release\backup.exe
if errorlevel 1 echo     UPX failed, keeping uncompressed exe.
if exist release\backup.upx del /q release\backup.upx
goto stage

:noup
echo     upx not found, skipped. install: scoop install upx

:stage
echo [3/4] stage config...
REM Do not overwrite an existing release\config.yaml (may hold real paths).
if exist release\config.yaml goto havecfg
copy /y config.example.yaml release\config.yaml >nul
echo     release\config.yaml created from config.example.yaml
goto result

:havecfg
echo     release\config.yaml exists, kept as is

:result
echo [4/4] result:
for %%F in (release\backup.exe) do echo     release\backup.exe: %%~zF bytes
echo     folder: %CD%\release
endlocal
