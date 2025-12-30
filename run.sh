#!/bin/bash

# Get the directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Configuration (override with config, then YALLMP_HOST / YALLMP_PORT)
HOST="127.0.0.1"
PORT="7978"

# Check if uv is available
if ! command -v uv >/dev/null 2>&1; then
    echo "[ERROR] uv is required but was not found in PATH."
    echo "Install uv from https://github.com/astral-sh/uv and re-run this script."
    exit 1
fi

while IFS= read -r line; do
    case "$line" in
        CFG_*) export "$line" ;;
    esac
done < <(uv run --project "$SCRIPT_DIR" python "$SCRIPT_DIR/scripts/print_run_config.py")

if [[ -n "${CFG_PROXY_HOST:-}" ]]; then
    HOST="$CFG_PROXY_HOST"
fi
if [[ -n "${CFG_PROXY_PORT:-}" ]]; then
    PORT="$CFG_PROXY_PORT"
fi
if [[ -n "${YALLMP_HOST:-}" ]]; then
    HOST="$YALLMP_HOST"
fi
if [[ -n "${YALLMP_PORT:-}" ]]; then
    PORT="$YALLMP_PORT"
fi

RELOAD_ARGS=()
for arg in "$@"; do
    if [[ "$arg" == "--reload" ]]; then
        RELOAD_ARGS+=(--reload)
    fi
done

# Start the proxy server
echo "[INFO] Starting proxy server on http://$HOST:$PORT"
uv run --project "$SCRIPT_DIR" uvicorn src.main:app --host "$HOST" --port "$PORT" "${RELOAD_ARGS[@]}"
