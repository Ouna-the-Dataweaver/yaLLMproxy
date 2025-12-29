#!/usr/bin/env bash

# Directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Forwarder venv
FWD_VENV="$SCRIPT_DIR/.venv_fwd"
FWD_PY="$FWD_VENV/bin/python"
BASE_PY="$SCRIPT_DIR/.venv/bin/python"

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

if [[ "${FORWARD_DEBUG:-}" == "1" ]]; then
  echo "[DEBUG] SCRIPT_DIR=$SCRIPT_DIR"
  echo "[DEBUG] BASE_PY=$BASE_PY"
  echo "[DEBUG] FWD_PY=$FWD_PY"
fi

# Load config defaults (CFG_*)
if command -v mktemp >/dev/null 2>&1; then
  CFG_TMP="$(mktemp "${TMPDIR:-/tmp}/yallmp_forwarder_cfg.XXXXXX")"
else
  CFG_TMP="${TMPDIR:-/tmp}/yallmp_forwarder_cfg.$$"
  : > "$CFG_TMP"
fi

if [[ "${FORWARD_DEBUG:-}" == "1" ]]; then
  echo "[DEBUG] Reading config via \"$BASE_PY\" \"$SCRIPT_DIR/scripts/print_run_config.py\""
fi
"$BASE_PY" "$SCRIPT_DIR/scripts/print_run_config.py" > "$CFG_TMP" 2> "$CFG_TMP.err"
CFG_EXIT=$?
if [[ "$CFG_EXIT" != "0" ]]; then
  echo "[WARN] Config helper exit code $CFG_EXIT"
fi
if [[ -s "$CFG_TMP.err" ]]; then
  while IFS= read -r line; do
    echo "[WARN] Config helper stderr: $line"
  done < "$CFG_TMP.err"
fi

CFG_FOUND=0
while IFS= read -r line; do
  case "$line" in
    CFG_*)
      export "$line"
      CFG_FOUND=1
      if [[ "${FORWARD_DEBUG:-}" == "1" ]]; then
        echo "[DEBUG] $line"
      fi
      ;;
  esac
done < "$CFG_TMP"

if [[ "$CFG_FOUND" == "0" ]]; then
  echo "[WARN] No CFG_ values found; using defaults/env."
fi
rm -f "$CFG_TMP" "$CFG_TMP.err" >/dev/null 2>&1

# Defaults (override with config, then env vars)
LISTEN_HOST="0.0.0.0"
LISTEN_PORT="7979"
TARGET_HOST="127.0.0.1"
TARGET_PORT="7978"
BUF_SIZE="65536"
LOG_LEVEL="INFO"
IDLE_LOG="0"

if [[ -n "${CFG_FORWARD_LISTEN_HOST:-}" ]]; then
  LISTEN_HOST="$CFG_FORWARD_LISTEN_HOST"
fi
if [[ -n "${CFG_FORWARD_LISTEN_PORT:-}" ]]; then
  LISTEN_PORT="$CFG_FORWARD_LISTEN_PORT"
fi
if [[ -n "${CFG_FORWARD_TARGET_HOST:-}" ]]; then
  TARGET_HOST="$CFG_FORWARD_TARGET_HOST"
fi
if [[ -n "${CFG_FORWARD_TARGET_PORT:-}" ]]; then
  TARGET_PORT="$CFG_FORWARD_TARGET_PORT"
fi

if [[ -n "${FORWARD_LISTEN_HOST:-}" ]]; then
  LISTEN_HOST="$FORWARD_LISTEN_HOST"
fi
if [[ -n "${FORWARD_LISTEN_PORT:-}" ]]; then
  LISTEN_PORT="$FORWARD_LISTEN_PORT"
fi
if [[ -n "${FORWARD_TARGET_HOST:-}" ]]; then
  TARGET_HOST="$FORWARD_TARGET_HOST"
fi
if [[ -n "${FORWARD_TARGET_PORT:-}" ]]; then
  TARGET_PORT="$FORWARD_TARGET_PORT"
fi
if [[ -n "${FORWARD_BUF_SIZE:-}" ]]; then
  BUF_SIZE="$FORWARD_BUF_SIZE"
fi
if [[ -n "${FORWARD_LOG_LEVEL:-}" ]]; then
  LOG_LEVEL="$FORWARD_LOG_LEVEL"
fi
if [[ -n "${FORWARD_IDLE_LOG:-}" ]]; then
  IDLE_LOG="$FORWARD_IDLE_LOG"
fi

echo "[INFO] Forwarding $LISTEN_HOST:$LISTEN_PORT -> $TARGET_HOST:$TARGET_PORT"
echo "[INFO] Press Ctrl+C to stop."
export PYTHONUNBUFFERED=1
"$FWD_PY" "$SCRIPT_DIR/scripts/tcp_forward.py" \
  --listen-host "$LISTEN_HOST" \
  --listen-port "$LISTEN_PORT" \
  --target-host "$TARGET_HOST" \
  --target-port "$TARGET_PORT" \
  --bufsize "$BUF_SIZE" \
  --log-level "$LOG_LEVEL" \
  --idle-log-seconds "$IDLE_LOG"
