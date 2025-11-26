#!/bin/bash

# Get the directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
PORT=17771
VENV_PATH="$SCRIPT_DIR/.venv"

# Check if virtual environment exists
if [ ! -f "$VENV_PATH/bin/activate" ]; then
    echo "[ERROR] Virtual environment not found at $VENV_PATH"
    echo "Please run install.sh first to create the virtual environment."
    exit 1
fi

# Activate the virtual environment
source "$VENV_PATH/bin/activate"

# Start the proxy server
echo "[INFO] Starting proxy server on http://0.0.0.0:$PORT"
uvicorn proxy:app --host 0.0.0.0 --port $PORT