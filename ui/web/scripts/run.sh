#!/bin/bash
set -euo pipefail

MODE="${1:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

case "$MODE" in
  --dev)
    echo "Starting UI server (Development Mode with HMR)..."
    echo "Connects to the dev daemon at $ROOT_DIR/daemon/dev/run/wedjat.sock"

    # 1. Build and start the Go API server in the background
    cd "$ROOT_DIR"
    GO_BIN="$(command -v go 2>/dev/null || { [ -x /usr/local/go/bin/go ] && echo /usr/local/go/bin/go; } || echo go)"
    mkdir -p dist/dev
    "$GO_BIN" build -o dist/dev/wedjat ./cmd/wedjat

    echo "==> Starting Go API server on port 3000..."
    # Ensure sudo is authenticated in the foreground before forking to background
    sudo -v
    sudo "$ROOT_DIR/dist/dev/wedjat" --web --port=3000 \
        --socket="$ROOT_DIR/daemon/dev/run/wedjat.sock" \
        --data="$ROOT_DIR/daemon/dev/var/lib/wedjat" \
        --no-open &
    API_PID=$!

    # Ensure background API server is killed when the script exits
    trap "sudo kill $API_PID 2>/dev/null || true" EXIT

    # 2. Start Vite dev server for Hot Module Replacement
    WEB_DIR="$SCRIPT_DIR/.."
    cd "$WEB_DIR"
    if [ ! -d node_modules ]; then
      echo "==> Installing web UI dependencies (npm ci)"
      npm ci --prefix "$WEB_DIR"
    fi
    echo "==> Starting Vite HMR dev server..."
    exec npm run dev
    ;;
  --release)
    echo "Starting UI server (Release Mode)..."
    echo "Connects to the installed daemon (/usr/local/bin/wedjat)"
    if [ ! -x /usr/local/bin/wedjat ]; then
        echo "wedjat is not installed. Run 'make install' first." >&2
        exit 1
    fi
    exec sudo /usr/local/bin/wedjat --web --port=3000 --no-open
    ;;
  *)
    echo "Usage: run.sh --dev | --release"
    exit 1
    ;;
esac