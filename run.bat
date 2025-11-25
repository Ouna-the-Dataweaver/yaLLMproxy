@echo off
setlocal enabledelayedexpansion

REM Directory containing this script
set "SCRIPT_DIR=%~dp0"
REM Default venv path
set "VENV_PATH=%SCRIPT_DIR%.venv"

REM Check if virtual environment exists
if not exist "%VENV_PATH%\Scripts\activate.bat" (
    echo [ERROR] Virtual environment not found at %VENV_PATH%
    echo Please run install.bat first to create the virtual environment.
    exit /b 1
)

REM Activate the virtual environment
call "%VENV_PATH%\Scripts\activate.bat"

REM Start the proxy server
echo [INFO] Starting proxy server on http://0.0.0.0:17771
uvicorn proxy:app --host 0.0.0.0 --port 17771
