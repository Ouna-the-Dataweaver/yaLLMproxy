@echo off
setlocal enabledelayedexpansion

REM Directory containing this script (strip trailing backslash to avoid quote escaping)
set "SCRIPT_DIR=%~dp0"
if "%SCRIPT_DIR:~-1%"=="\" set "SCRIPT_DIR=%SCRIPT_DIR:~0,-1%"
REM Project root is the parent of scripts/
for %%i in ("%SCRIPT_DIR%\..") do set "PROJECT_ROOT=%%~fi"
REM Allow overriding VENV_PATH, default to .venv in project root
if not defined VENV_PATH set "VENV_PATH=%PROJECT_ROOT%\.venv"

REM Check if uv is available
where uv >nul 2>nul
if %errorlevel% neq 0 (
    echo [ERROR] uv is required but was not found in PATH.
    echo Install uv from https://github.com/astral-sh/uv and re-run this script.
    exit /b 1
)

REM Create the virtualenv with uv (wrapper around python -m venv)
if exist "%VENV_PATH%" (
    echo [INFO] Reusing existing virtual environment at %VENV_PATH%
) else (
    echo [INFO] Creating virtual environment at %VENV_PATH%
    uv venv "%VENV_PATH%"
)

set "PYTHON_BIN=%VENV_PATH%\Scripts\python.exe"

REM Install dependencies into the venv using uv sync
echo [INFO] Syncing proxy dependencies
uv sync ^
  --project "%PROJECT_ROOT%" ^
  --python "%PYTHON_BIN%"

echo.
echo [INFO] Installation complete.
echo Run the proxy, e.g.:
echo   uv run uvicorn proxy:app --host 0.0.0.0 --port 17771
echo.
echo Or simply run:
echo   run.bat
