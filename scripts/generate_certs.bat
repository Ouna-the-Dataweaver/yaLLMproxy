@echo off
setlocal enabledelayedexpansion

REM Generate SSL certificates using mkcert
REM Reads hosts from http_forwarder_settings.ssl.hosts in config.yaml

REM Directory containing this script
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"
REM Project root is the parent of scripts/
for %%i in ("%SCRIPT_DIR%\..") do set "PROJECT_ROOT=%%~fi"

REM Check if mkcert is installed
where mkcert >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] mkcert is not installed or not in PATH.
    echo.
    echo Install mkcert using one of the following methods:
    echo   scoop install mkcert
    echo   choco install mkcert
    echo   Or download from https://github.com/FiloSottile/mkcert/releases
    echo.
    echo After installing, run: mkcert -install
    exit /b 1
)

REM Use Python from main venv to run print_run_config.py
set "PY=%PROJECT_ROOT%\.venv\Scripts\python.exe"
if not exist "%PY%" (
    REM Try forwarder venv
    set "PY=%PROJECT_ROOT%\.venv_fwd\Scripts\python.exe"
)
if not exist "%PY%" (
    echo [ERROR] No Python venv found. Run install first.
    exit /b 1
)

REM Get SSL hosts from config
set "SSL_HOSTS="
set "SSL_CERT="
set "SSL_KEY="
for /f "usebackq delims=" %%A in (`"%PY%" "%SCRIPT_DIR%\print_run_config.py" ^| findstr /b CFG_HTTP_FORWARD_SSL`) do set "%%A"

if not "%CFG_HTTP_FORWARD_SSL_HOSTS%"=="" set "SSL_HOSTS=%CFG_HTTP_FORWARD_SSL_HOSTS%"
if not "%CFG_HTTP_FORWARD_SSL_CERT%"=="" set "SSL_CERT=%CFG_HTTP_FORWARD_SSL_CERT%"
if not "%CFG_HTTP_FORWARD_SSL_KEY%"=="" set "SSL_KEY=%CFG_HTTP_FORWARD_SSL_KEY%"

REM Defaults
if "%SSL_HOSTS%"=="" set "SSL_HOSTS=localhost 127.0.0.1"
if "%SSL_CERT%"=="" set "SSL_CERT=certs/cert.pem"
if "%SSL_KEY%"=="" set "SSL_KEY=certs/key.pem"

REM Create certs directory
set "CERT_DIR=%PROJECT_ROOT%\certs"
if not exist "%CERT_DIR%" (
    echo [INFO] Creating certs directory at %CERT_DIR%
    mkdir "%CERT_DIR%"
)

REM Full paths for cert and key
set "CERT_PATH=%PROJECT_ROOT%\%SSL_CERT%"
set "KEY_PATH=%PROJECT_ROOT%\%SSL_KEY%"

REM Get directory from cert path and ensure it exists
for %%i in ("%CERT_PATH%") do set "CERT_PARENT=%%~dpi"
if not exist "%CERT_PARENT%" mkdir "%CERT_PARENT%"

echo [INFO] Generating certificates for: %SSL_HOSTS%
echo [INFO] Certificate: %SSL_CERT%
echo [INFO] Key: %SSL_KEY%
echo.

REM Generate certificate with mkcert
REM mkcert outputs cert.pem and cert-key.pem by default, we need to specify output names
cd /d "%PROJECT_ROOT%"
mkcert -cert-file "%SSL_CERT%" -key-file "%SSL_KEY%" %SSL_HOSTS%

if %errorlevel% equ 0 (
    echo.
    echo [SUCCESS] Certificates generated successfully!
    echo.
    echo To enable HTTPS, set ssl.enabled: true in configs/config.yaml
    echo Then run: task forwarder:http
) else (
    echo.
    echo [ERROR] Failed to generate certificates.
    exit /b 1
)
