@echo off
setlocal enabledelayedexpansion

REM Directory containing this script (strip trailing backslash to avoid quote escaping)
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

REM Configuration (override with config, then YALLMP_HOST / YALLMP_PORT)
set "HOST=127.0.0.1"
set "PORT=7978"

REM Check if uv is available
where uv >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] uv is required but was not found in PATH.
    echo Install uv from https://github.com/astral-sh/uv and re-run this script.
    exit /b 1
)

for /f "usebackq delims=" %%A in (`uv run --project "%SCRIPT_DIR%" python "%SCRIPT_DIR%\scripts\print_run_config.py" ^| findstr /b CFG_`) do set "%%A"
if not "%CFG_PROXY_HOST%"=="" set "HOST=%CFG_PROXY_HOST%"
if not "%CFG_PROXY_PORT%"=="" set "PORT=%CFG_PROXY_PORT%"

if not "%YALLMP_HOST%"=="" set "HOST=%YALLMP_HOST%"
if not "%YALLMP_PORT%"=="" set "PORT=%YALLMP_PORT%"

REM Optional flags
set "RELOAD_ARG="
:parse_args
if "%~1"=="" goto after_args
if /i "%~1"=="--reload" set "RELOAD_ARG=--reload"
shift
goto parse_args
:after_args

REM Start the proxy server
echo [INFO] Starting proxy server on http://%HOST%:%PORT%
uv run --project "%SCRIPT_DIR%" uvicorn src.main:app --host %HOST% --port %PORT% %RELOAD_ARG%
