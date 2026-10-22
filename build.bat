@echo off
setlocal
cd /d "%~dp0"

rem Build: packs tools\*.exe into assets\*.gz, runs tests, compiles dist\PhonkerLoader_vX.exe.
rem The version is taken from the VERSION file. -H windowsgui: the window mode needs no console.
set /p VERSION=<VERSION

echo [1/3] Packing tools...
go run ./cmd/pack || goto :fail

set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0

echo [2/3] Running tests...
go test . || goto :fail

echo [3/3] Building dist\PhonkerLoader_v%VERSION%.exe...
go build -trimpath -ldflags "-s -w -H windowsgui -X main.appVersion=%VERSION%" -o "dist\PhonkerLoader_v%VERSION%.exe" . || goto :fail

rmdir /s /q assets
echo.
echo Done: dist\PhonkerLoader_v%VERSION%.exe
exit /b 0

:fail
if exist assets rmdir /s /q assets
echo.
echo Build failed.
exit /b 1