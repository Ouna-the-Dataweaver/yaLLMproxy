#!/usr/bin/env bash

# Generate SSL certificates using mkcert
# Reads hosts from http_forwarder_settings.ssl.hosts in config.yaml

set -e

# Directory containing this script
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Project root is the parent of scripts/
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Check if mkcert is installed
if ! command -v mkcert >/dev/null 2>&1; then
    echo "[ERROR] mkcert is not installed or not in PATH."
    echo ""
    echo "Install mkcert using one of the following methods:"
    echo "  Debian/Ubuntu: sudo apt install libnss3-tools && go install filippo.io/mkcert@latest"
    echo "  Arch: sudo pacman -S mkcert"
    echo "  macOS: brew install mkcert"
    echo "  Or download from https://github.com/FiloSottile/mkcert/releases"
    echo ""
    echo "After installing, run: mkcert -install"
    exit 1
fi

# Use Python from main venv to run print_run_config.py
PY="$PROJECT_ROOT/.venv/bin/python"
if [[ ! -x "$PY" ]]; then
    # Try forwarder venv
    PY="$PROJECT_ROOT/.venv_fwd/bin/python"
fi
if [[ ! -x "$PY" ]]; then
    echo "[ERROR] No Python venv found. Run install first."
    exit 1
fi

# Get SSL config from print_run_config.py
SSL_HOSTS=""
SSL_CERT=""
SSL_KEY=""

while IFS= read -r line; do
    case "$line" in
        CFG_HTTP_FORWARD_SSL_HOSTS=*)
            SSL_HOSTS="${line#CFG_HTTP_FORWARD_SSL_HOSTS=}"
            ;;
        CFG_HTTP_FORWARD_SSL_CERT=*)
            SSL_CERT="${line#CFG_HTTP_FORWARD_SSL_CERT=}"
            ;;
        CFG_HTTP_FORWARD_SSL_KEY=*)
            SSL_KEY="${line#CFG_HTTP_FORWARD_SSL_KEY=}"
            ;;
    esac
done < <("$PY" "$SCRIPT_DIR/print_run_config.py" | grep '^CFG_HTTP_FORWARD_SSL')

# Defaults
if [[ -z "$SSL_HOSTS" ]]; then
    SSL_HOSTS="localhost 127.0.0.1"
fi
if [[ -z "$SSL_CERT" ]]; then
    SSL_CERT="certs/cert.pem"
fi
if [[ -z "$SSL_KEY" ]]; then
    SSL_KEY="certs/key.pem"
fi

# Create certs directory
CERT_DIR="$PROJECT_ROOT/certs"
if [[ ! -d "$CERT_DIR" ]]; then
    echo "[INFO] Creating certs directory at $CERT_DIR"
    mkdir -p "$CERT_DIR"
fi

# Ensure parent directory for cert exists
CERT_PATH="$PROJECT_ROOT/$SSL_CERT"
CERT_PARENT="$(dirname "$CERT_PATH")"
if [[ ! -d "$CERT_PARENT" ]]; then
    mkdir -p "$CERT_PARENT"
fi

echo "[INFO] Generating certificates for: $SSL_HOSTS"
echo "[INFO] Certificate: $SSL_CERT"
echo "[INFO] Key: $SSL_KEY"
echo ""

# Generate certificate with mkcert
cd "$PROJECT_ROOT"
# shellcheck disable=SC2086
mkcert -cert-file "$SSL_CERT" -key-file "$SSL_KEY" $SSL_HOSTS

echo ""
echo "[SUCCESS] Certificates generated successfully!"
echo ""
echo "To enable HTTPS, set ssl.enabled: true in configs/config.yaml"
echo "Then run: task forwarder:http"
