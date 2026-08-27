@echo off
REM AppStarter build script (Windows)
REM Requires Go 1.24+; run 'go install github.com/akavel/rsrc@latest' before first build
REM Builds manifest plus optional icon into rsrc.syso, then compiles GUI exe with no console window

for /f "tokens=*" %%i in ('go env GOPATH') do set "GP=%%i"
set "PATH=%GP%\bin;%PATH%"

if exist assets\logo.ico (
    echo Generating rsrc.syso with manifest and icon...
    rsrc -ico assets/logo.ico -manifest AppStarter.exe.manifest -o rsrc.syso
) else (
    echo Generating rsrc.syso with manifest only...
    rsrc -manifest AppStarter.exe.manifest -o rsrc.syso
)

echo Building AppStarter.exe GUI subsystem...
go build -ldflags="-H windowsgui" -o AppStarter.exe .
if errorlevel 1 (
    echo Build failed
    exit /b 1
)
echo Done: AppStarter.exe
