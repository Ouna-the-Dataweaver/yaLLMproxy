#!/usr/bin/env bash
set -euo pipefail

# Directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Project root is the parent of scripts/
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
# Allow overriding VENV_PATH, default to .venv in project root
VENV_PATH="${VENV_PATH:-$PROJECT_ROOT/.venv}"

if ! command -v uv >/dev/null 2>&1; then
  echo "[ERROR] uv is required but was not found in PATH." >&2
  echo "Install uv from https://github.com/astral-sh/uv and re-run this script." >&2
  exit 1
fi

# Create the virtualenv with uv (wrapper around python -m venv)
if [[ -d "$VENV_PATH" ]]; then
  echo "[INFO] Reusing existing virtual environment at $VENV_PATH"
else
  echo "[INFO] Creating virtual environment at $VENV_PATH"
  uv venv "$VENV_PATH"
fi

PYTHON_BIN="$VENV_PATH/bin/python"

# Install dependencies into the venv using uv sync
echo "[INFO] Syncing proxy dependencies"
uv sync \
  --project "$PROJECT_ROOT" \
  --python "$PYTHON_BIN"

cat <<'MSG'

[INFO] Installation complete.
Run the proxy, e.g.:
  uv run uvicorn proxy:app --host 0.0.0.0 --port 17771
MSG
