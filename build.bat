@echo off
REM Build backup.exe: Go size optimization + UPX compression.
REM Output goes to the project root (next to config.yaml / data / logs).
setlocal
cd /d "%~dp0"

set GOPROXY=https://goproxy.cn,direct

echo [1/3] go build (-s -w -trimpath)...
pushd src
go build -ldflags "-s -w" -trimpath -o ..\backup.exe .
if errorlevel 1 (
  popd
  echo build failed
  exit /b 1
)
popd
for %%F in (backup.exe) do echo     built: %%~zF bytes

echo [2/3] UPX compress...
where upx >nul 2>&1
if errorlevel 1 goto noup
upx --best backup.exe
if errorlevel 1 echo     UPX failed, keeping uncompressed exe.
goto done

:noup
echo     upx not found, skipped. install: scoop install upx

:done
echo [3/3] result:
for %%F in (backup.exe) do echo     backup.exe: %%~zF bytes
endlocal
