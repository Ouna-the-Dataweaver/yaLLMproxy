#!/bin/bash

# Get the directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration
PORT=7979

# Check if uv is available
if ! command -v uv >/dev/null 2>&1; then
    echo "[ERROR] uv is required but was not found in PATH."
    echo "Install uv from https://github.com/astral-sh/uv and re-run this script."
    exit 1
fi

# Start the proxy server
echo "[INFO] Starting proxy server on http://0.0.0.0:$PORT"
uv run --project "$SCRIPT_DIR" uvicorn src.main:app --host 0.0.0.0 --port $PORT
