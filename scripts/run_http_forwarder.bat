@echo off
setlocal enabledelayedexpansion

REM Directory containing this script (strip trailing backslash to avoid quote escaping)
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"
REM Project root is the parent of scripts/
for %%i in ("%SCRIPT_DIR%\..") do set "PROJECT_ROOT=%%~fi"

REM Forwarder venv
set "FWD_VENV=%PROJECT_ROOT%\.venv_fwd"
set "FWD_PY=%FWD_VENV%\Scripts\python.exe"
set "BASE_PY=%PROJECT_ROOT%\.venv\Scripts\python.exe"

if exist "%FWD_PY%" goto venv_ok
if not exist "%BASE_PY%" goto no_base_venv
echo [INFO] Creating forwarder venv at %FWD_VENV%
"%BASE_PY%" -m venv "%FWD_VENV%"
if not exist "%FWD_PY%" goto venv_create_failed
:venv_ok

REM Check if uv is available
where uv >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] uv is required but was not found in PATH.
    echo Install uv from https://github.com/astral-sh/uv and re-run this script.
    exit /b 1
)

REM Ensure HTTP forwarder deps exist in forwarder venv
"%FWD_PY%" -c "import fastapi, httpx, uvicorn, yaml, dotenv" >nul 2>nul
if %errorlevel% neq 0 (
    echo [INFO] Installing HTTP forwarder dependencies into %FWD_VENV%
    uv pip install --python "%FWD_PY%" "fastapi>=0.100.0" "uvicorn[standard]>=0.23.0" "httpx[http2]>=0.24.0" "pyyaml>=6.0" "python-dotenv>=1.0.0"
)

REM Defaults (override with config, then env vars)
set "HOST="
set "PORT="
set "TARGET_SCHEME="
set "TARGET_HOST="
set "TARGET_PORT="
set "LOG_LEVEL="

for /f "usebackq delims=" %%A in (`"%FWD_PY%" "%SCRIPT_DIR%\print_run_config.py" ^| findstr /b CFG_`) do set "%%A"
if not "%CFG_HTTP_FORWARD_LISTEN_HOST%"=="" set "HOST=%CFG_HTTP_FORWARD_LISTEN_HOST%"
if not "%CFG_HTTP_FORWARD_LISTEN_PORT%"=="" set "PORT=%CFG_HTTP_FORWARD_LISTEN_PORT%"
if not "%CFG_HTTP_FORWARD_TARGET_SCHEME%"=="" set "TARGET_SCHEME=%CFG_HTTP_FORWARD_TARGET_SCHEME%"
if not "%CFG_HTTP_FORWARD_TARGET_HOST%"=="" set "TARGET_HOST=%CFG_HTTP_FORWARD_TARGET_HOST%"
if not "%CFG_HTTP_FORWARD_TARGET_PORT%"=="" set "TARGET_PORT=%CFG_HTTP_FORWARD_TARGET_PORT%"

if not "%HTTP_FORWARD_LISTEN_HOST%"=="" set "HOST=%HTTP_FORWARD_LISTEN_HOST%"
if not "%HTTP_FORWARD_LISTEN_PORT%"=="" set "PORT=%HTTP_FORWARD_LISTEN_PORT%"
if not "%HTTP_FORWARD_TARGET_SCHEME%"=="" set "TARGET_SCHEME=%HTTP_FORWARD_TARGET_SCHEME%"
if not "%HTTP_FORWARD_TARGET_HOST%"=="" set "TARGET_HOST=%HTTP_FORWARD_TARGET_HOST%"
if not "%HTTP_FORWARD_TARGET_PORT%"=="" set "TARGET_PORT=%HTTP_FORWARD_TARGET_PORT%"
if not "%HTTP_FORWARD_LOG_LEVEL%"=="" set "LOG_LEVEL=%HTTP_FORWARD_LOG_LEVEL%"

REM Fallback defaults (only if still empty)
if "%HOST%"=="" set "HOST=0.0.0.0"
if "%PORT%"=="" set "PORT=6969"
if "%TARGET_SCHEME%"=="" set "TARGET_SCHEME=http"
if "%TARGET_HOST%"=="" set "TARGET_HOST=127.0.0.1"
if "%TARGET_PORT%"=="" set "TARGET_PORT=7979"
if "%LOG_LEVEL%"=="" set "LOG_LEVEL=info"

echo [INFO] HTTP forwarding http://%HOST%:%PORT% ^> %TARGET_SCHEME%://%TARGET_HOST%:%TARGET_PORT%
echo [INFO] Press Ctrl+C to stop (then Y if prompted).
set "PYTHONUNBUFFERED=1"
set "HTTP_FORWARD_LISTEN_HOST=%HOST%"
set "HTTP_FORWARD_LISTEN_PORT=%PORT%"
set "HTTP_FORWARD_TARGET_SCHEME=%TARGET_SCHEME%"
set "HTTP_FORWARD_TARGET_HOST=%TARGET_HOST%"
set "HTTP_FORWARD_TARGET_PORT=%TARGET_PORT%"
cd /d "%PROJECT_ROOT%" && "%FWD_PY%" -m uvicorn src.http_forwarder:app --host %HOST% --port %PORT% --log-level %LOG_LEVEL%
exit /b 0

:no_base_venv
echo [ERROR] Base venv not found at %BASE_PY%.
echo Create the main venv first (e.g. run install.bat), then re-run.
exit /b 1

:venv_create_failed
echo [ERROR] Failed to create forwarder venv at %FWD_VENV%.
exit /b 1
