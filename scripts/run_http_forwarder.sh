#!/usr/bin/env bash

# Directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Project root is the parent of scripts/
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Forwarder venv
FWD_VENV="$PROJECT_ROOT/.venv_fwd"
FWD_PY="$FWD_VENV/bin/python"
BASE_PY="$PROJECT_ROOT/.venv/bin/python"

if [[ -x "$FWD_PY" ]]; then
  :
else
  if [[ ! -x "$BASE_PY" ]]; then
    echo "[ERROR] Base venv not found at $BASE_PY."
    echo "Create the main venv first (e.g. run install.sh), then re-run."
    exit 1
  fi
  echo "[INFO] Creating forwarder venv at $FWD_VENV"
  "$BASE_PY" -m venv "$FWD_VENV"
  if [[ ! -x "$FWD_PY" ]]; then
    echo "[ERROR] Failed to create forwarder venv at $FWD_VENV."
    exit 1
  fi
fi

# Check if uv is available
if ! command -v uv >/dev/null 2>&1; then
  echo "[ERROR] uv is required but was not found in PATH."
  echo "Install uv from https://github.com/astral-sh/uv and re-run this script."
  exit 1
fi

# Ensure HTTP forwarder deps exist in forwarder venv
if ! "$FWD_PY" - <<'PY' >/dev/null 2>&1
import fastapi, httpx, uvicorn, yaml, dotenv  # noqa: F401
PY
then
  echo "[INFO] Installing HTTP forwarder dependencies into $FWD_VENV"
  uv pip install --python "$FWD_PY" "fastapi>=0.100.0" "uvicorn[standard]>=0.23.0" "httpx[http2]>=0.24.0" "pyyaml>=6.0" "python-dotenv>=1.0.0"
fi

# Defaults (override with config, then env vars)
HOST=""
PORT=""
TARGET_SCHEME=""
TARGET_HOST=""
TARGET_PORT=""
LOG_LEVEL=""

while IFS= read -r line; do
  case "$line" in
    CFG_*)
      export "$line"
      ;;
  esac
done < <("$FWD_PY" "$SCRIPT_DIR/print_run_config.py" | grep '^CFG_')

if [[ -n "${CFG_HTTP_FORWARD_LISTEN_HOST:-}" ]]; then
  HOST="$CFG_HTTP_FORWARD_LISTEN_HOST"
fi
if [[ -n "${CFG_HTTP_FORWARD_LISTEN_PORT:-}" ]]; then
  PORT="$CFG_HTTP_FORWARD_LISTEN_PORT"
fi
if [[ -n "${CFG_HTTP_FORWARD_TARGET_SCHEME:-}" ]]; then
  TARGET_SCHEME="$CFG_HTTP_FORWARD_TARGET_SCHEME"
fi
if [[ -n "${CFG_HTTP_FORWARD_TARGET_HOST:-}" ]]; then
  TARGET_HOST="$CFG_HTTP_FORWARD_TARGET_HOST"
fi
if [[ -n "${CFG_HTTP_FORWARD_TARGET_PORT:-}" ]]; then
  TARGET_PORT="$CFG_HTTP_FORWARD_TARGET_PORT"
fi

if [[ -n "${HTTP_FORWARD_LISTEN_HOST:-}" ]]; then
  HOST="$HTTP_FORWARD_LISTEN_HOST"
fi
if [[ -n "${HTTP_FORWARD_LISTEN_PORT:-}" ]]; then
  PORT="$HTTP_FORWARD_LISTEN_PORT"
fi
if [[ -n "${HTTP_FORWARD_TARGET_SCHEME:-}" ]]; then
  TARGET_SCHEME="$HTTP_FORWARD_TARGET_SCHEME"
fi
if [[ -n "${HTTP_FORWARD_TARGET_HOST:-}" ]]; then
  TARGET_HOST="$HTTP_FORWARD_TARGET_HOST"
fi
if [[ -n "${HTTP_FORWARD_TARGET_PORT:-}" ]]; then
  TARGET_PORT="$HTTP_FORWARD_TARGET_PORT"
fi
if [[ -n "${HTTP_FORWARD_LOG_LEVEL:-}" ]]; then
  LOG_LEVEL="$HTTP_FORWARD_LOG_LEVEL"
fi

# Fallback defaults (only if still empty)
if [[ -z "$HOST" ]]; then
  HOST="0.0.0.0"
fi
if [[ -z "$PORT" ]]; then
  PORT="6969"
fi
if [[ -z "$TARGET_SCHEME" ]]; then
  TARGET_SCHEME="http"
fi
if [[ -z "$TARGET_HOST" ]]; then
  TARGET_HOST="127.0.0.1"
fi
if [[ -z "$TARGET_PORT" ]]; then
  TARGET_PORT="7979"
fi
if [[ -z "$LOG_LEVEL" ]]; then
  LOG_LEVEL="info"
fi

echo "[INFO] HTTP forwarding http://$HOST:$PORT -> $TARGET_SCHEME://$TARGET_HOST:$TARGET_PORT"
echo "[INFO] Press Ctrl+C to stop."
export PYTHONUNBUFFERED=1
export HTTP_FORWARD_LISTEN_HOST="$HOST"
export HTTP_FORWARD_LISTEN_PORT="$PORT"
export HTTP_FORWARD_TARGET_SCHEME="$TARGET_SCHEME"
export HTTP_FORWARD_TARGET_HOST="$TARGET_HOST"
export HTTP_FORWARD_TARGET_PORT="$TARGET_PORT"

cd "$PROJECT_ROOT" && "$FWD_PY" -m uvicorn src.http_forwarder:app --host "$HOST" --port "$PORT" --log-level "$LOG_LEVEL"
