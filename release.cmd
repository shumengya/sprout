@echo off
rem sprout release script.
rem Keep this file pure ASCII with CRLF line endings: no BOM and no chcp, so it
rem never changes the code page of the console that runs it.
rem
rem Usage: release.cmd [version]
rem   version   release version, defaults to today as 2026.11.12
rem
rem What it does: build / vet / test, cross-compile 7 targets, then write
rem release\<version>\ with one archive per platform, SHA256SUMS.txt and
rem RELEASE.md. Release notes come from docs\releases\<version>.md; a draft is
rem created there when the file does not exist yet.
setlocal EnableExtensions

where go >nul 2>&1
if errorlevel 1 (
    echo go was not found in PATH
    exit /b 1
)

pushd "%~dp0" || exit /b 1
go run ./tools/release %*
set "RC=%ERRORLEVEL%"
popd
exit /b %RC%
