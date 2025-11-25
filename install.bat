@echo off
setlocal enabledelayedexpansion

REM Directory containing this script (so we can create .venv next to it)
set "SCRIPT_DIR=%~dp0"
REM Allow overriding VENV_PATH, default to .venv beside script
if not defined VENV_PATH set "VENV_PATH=%SCRIPT_DIR%.venv"

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

REM Install dependencies into the venv explicitly
echo [INFO] Installing proxy dependencies
uv pip install ^
  --python "%PYTHON_BIN%" ^
  fastapi ^
  httpx ^
  pyyaml ^
  uvicorn ^
  openai

uv pip install "python-dotenv"

echo.
echo [INFO] Installation complete.
echo Activate the environment with:
echo   "%VENV_PATH%\Scripts\activate"
echo Then run the proxy, e.g.:
echo   uvicorn proxy:app --host 0.0.0.0 --port 17771
echo.
echo Or simply run:
echo   run.bat
