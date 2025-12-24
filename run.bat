@echo off
setlocal enabledelayedexpansion

REM Directory containing this script
set "SCRIPT_DIR=%~dp0"

REM Configuration
set "PORT=7979"

REM Check if uv is available
where uv >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] uv is required but was not found in PATH.
    echo Install uv from https://github.com/astral-sh/uv and re-run this script.
    exit /b 1
)

REM Start the proxy server
echo [INFO] Starting proxy server on http://0.0.0.0:%PORT%
uv run --project "%SCRIPT_DIR%" uvicorn src.main:app --host 0.0.0.0 --port %PORT%
