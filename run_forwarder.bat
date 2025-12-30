@echo off
setlocal

REM Directory containing this script (strip trailing backslash to avoid quote escaping)
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"

REM Forwarder venv
set "FWD_VENV=%SCRIPT_DIR%\.venv_fwd"
set "FWD_PY=%FWD_VENV%\Scripts\python.exe"
set "BASE_PY=%SCRIPT_DIR%\.venv\Scripts\python.exe"

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
if /i "%FORWARD_DEBUG%"=="1" echo [DEBUG] Reading config via "%BASE_PY%" "%SCRIPT_DIR%\scripts\print_run_config.py"
"%BASE_PY%" "%SCRIPT_DIR%\scripts\print_run_config.py" > "%CFG_TMP%" 2> "%CFG_TMP%.err"
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
set "LISTEN_HOST=0.0.0.0"
set "LISTEN_PORT=7979"
set "TARGET_HOST=127.0.0.1"
set "TARGET_PORT=7978"
set "BUF_SIZE=65536"
set "LOG_LEVEL=INFO"
set "IDLE_LOG=0"

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

echo [INFO] Forwarding %LISTEN_HOST%:%LISTEN_PORT% ^> %TARGET_HOST%:%TARGET_PORT%
echo [INFO] Press Ctrl+C to stop (then Y if prompted).
set "PYTHONUNBUFFERED=1"
"%FWD_PY%" "%SCRIPT_DIR%\scripts\tcp_forward.py" --listen-host "%LISTEN_HOST%" --listen-port "%LISTEN_PORT%" --target-host "%TARGET_HOST%" --target-port "%TARGET_PORT%" --bufsize "%BUF_SIZE%" --log-level "%LOG_LEVEL%" --idle-log-seconds "%IDLE_LOG%"
exit /b 0

:no_base_venv
echo [ERROR] Base venv not found at %BASE_PY%.
echo Create the main venv first (e.g. run install.bat), then re-run.
exit /b 1

:venv_create_failed
echo [ERROR] Failed to create forwarder venv at %FWD_VENV%.
exit /b 1
