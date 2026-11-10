@echo off
rem sprout build script.
rem Keep this file pure ASCII with CRLF line endings: no BOM and no chcp, so it
rem never changes the code page of the console that runs it.
rem
rem Usage: build.cmd [target]
rem   build   compile bin\sprout.exe (default)
rem   check   go build, go vet and go test over all packages
rem   all     check first, then build
setlocal EnableExtensions

where go >nul 2>&1
if errorlevel 1 (
    echo go was not found in PATH
    exit /b 1
)

pushd "%~dp0" || exit /b 1
call :main %*
set "RC=%ERRORLEVEL%"
popd
exit /b %RC%

:main
set "TARGET=%~1"
if not defined TARGET set "TARGET=build"
if /i "%TARGET%"=="build" goto run_build
if /i "%TARGET%"=="check" goto run_check
if /i "%TARGET%"=="all" goto run_all
echo Unknown target: %TARGET%
echo Usage: build.cmd [build/check/all]
exit /b 2

:run_build
call :build
exit /b %ERRORLEVEL%

:run_check
call :check
exit /b %ERRORLEVEL%

:run_all
call :check || exit /b 1
call :build
exit /b %ERRORLEVEL%

:check
echo [1/3] go build ./...
go build ./...
if errorlevel 1 exit /b 1
echo [2/3] go vet ./...
go vet ./...
if errorlevel 1 exit /b 1
echo [3/3] go test ./...
go test ./...
if errorlevel 1 exit /b 1
exit /b 0

:build
rem The version string is injected into main.version; fall back to dev without git.
set "VERSION=dev"
for /f "delims=" %%v in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%v"
rem Only write bin\sprout.exe; other files in bin\ belong to the user.
if not exist bin mkdir bin
echo Building bin\sprout.exe, version %VERSION%
set "CGO_ENABLED=0"
go build -trimpath -ldflags "-X main.version=%VERSION%" -o bin\sprout.exe ./cmd/sprout
if errorlevel 1 exit /b 1
echo Done: bin\sprout.exe
exit /b 0
