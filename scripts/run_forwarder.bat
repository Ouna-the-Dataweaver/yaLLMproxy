@echo off
setlocal

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

if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] SCRIPT_DIR=%SCRIPT_DIR%
if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] BASE_PY=%BASE_PY%
if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] FWD_PY=%FWD_PY%

REM Load config defaults (CFG_*)
set "CFG_TMP=%TEMP%\yallmp_forwarder_cfg_%RANDOM%%RANDOM%.txt"
if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] Reading config via "%BASE_PY%" "%SCRIPT_DIR%\print_run_config.py"
"%BASE_PY%" "%SCRIPT_DIR%\print_run_config.py" > "%CFG_TMP%" 2> "%CFG_TMP%.err"
set "CFG_EXIT=%ERRORLEVEL%"
if not "%CFG_EXIT%"=="0" echo [WARN] Config helper exit code %CFG_EXIT%
if exist "%CFG_TMP%.err" (
  for /f "usebackq delims=" %%A in ("%CFG_TMP%.err") do echo [WARN] Config helper stderr: %%A
)
set "CFG_FOUND=0"
for /f "usebackq delims=" %%A in (`findstr /b CFG_ "%CFG_TMP%"`) do (
  set "%%A"
  set "CFG_FOUND=1"
  if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] %%A
)
if "%CFG_FOUND%"=="0" echo [WARN] No CFG_ values found; using defaults/env.
del "%CFG_TMP%" "%CFG_TMP%.err" >nul 2>nul

REM Defaults (override with config, then env vars)
set "LISTEN_HOST="
set "LISTEN_PORT="
set "TARGET_HOST="
set "TARGET_PORT="
set "BUF_SIZE="
set "LOG_LEVEL="
set "IDLE_LOG="

if not "%CFG_FORWARD_LISTEN_HOST%"=="" set "LISTEN_HOST=%CFG_FORWARD_LISTEN_HOST%"
if not "%CFG_FORWARD_LISTEN_PORT%"=="" set "LISTEN_PORT=%CFG_FORWARD_LISTEN_PORT%"
if not "%CFG_FORWARD_TARGET_HOST%"=="" set "TARGET_HOST=%CFG_FORWARD_TARGET_HOST%"
if not "%CFG_FORWARD_TARGET_PORT%"=="" set "TARGET_PORT=%CFG_FORWARD_TARGET_PORT%"

if not "%FORWARD_LISTEN_HOST%"=="" set "LISTEN_HOST=%FORWARD_LISTEN_HOST%"
if not "%FORWARD_LISTEN_PORT%"=="" set "LISTEN_PORT=%FORWARD_LISTEN_PORT%"
if not "%FORWARD_TARGET_HOST%"=="" set "TARGET_HOST=%FORWARD_TARGET_HOST%"
if not "%FORWARD_TARGET_PORT%"=="" set "TARGET_PORT=%FORWARD_TARGET_PORT%"
if not "%FORWARD_BUF_SIZE%"=="" set "BUF_SIZE=%FORWARD_BUF_SIZE%"
if not "%FORWARD_LOG_LEVEL%"=="" set "LOG_LEVEL=%FORWARD_LOG_LEVEL%"
if not "%FORWARD_IDLE_LOG%"=="" set "IDLE_LOG=%FORWARD_IDLE_LOG%"

REM Fallback defaults (only if still empty)
if "%LISTEN_HOST%"=="" set "LISTEN_HOST=0.0.0.0"
if "%LISTEN_PORT%"=="" set "LISTEN_PORT=7979"
if "%TARGET_HOST%"=="" set "TARGET_HOST=127.0.0.1"
if "%TARGET_PORT%"=="" set "TARGET_PORT=7978"
if "%BUF_SIZE%"=="" set "BUF_SIZE=65536"
if "%LOG_LEVEL%"=="" set "LOG_LEVEL=INFO"
if "%IDLE_LOG%"=="" set "IDLE_LOG=0"

echo [INFO] Forwarding %LISTEN_HOST%:%LISTEN_PORT% ^> %TARGET_HOST%:%TARGET_PORT%
echo [INFO] Press Ctrl+C to stop (then Y if prompted).
set "PYTHONUNBUFFERED=1"
"%FWD_PY%" "%SCRIPT_DIR%\tcp_forward.py" --listen-host "%LISTEN_HOST%" --listen-port "%LISTEN_PORT%" --target-host "%TARGET_HOST%" --target-port "%TARGET_PORT%" --bufsize "%BUF_SIZE%" --log-level "%LOG_LEVEL%" --idle-log-seconds "%IDLE_LOG%"
exit /b 0

:no_base_venv
echo [ERROR] Base venv not found at %BASE_PY%.
echo Create the main venv first (e.g. run install.bat), then re-run.
exit /b 1

:venv_create_failed
echo [ERROR] Failed to create forwarder venv at %FWD_VENV%.
exit /b 1
