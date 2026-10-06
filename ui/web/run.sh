#!/bin/bash
set -e
cd "$(dirname "$0")"

if [ ! -d "node_modules" ]; then
  echo "Installing deps..."
  npm install
fi

# The dev daemon runs under sudo and creates its Unix socket 0660 root:root,
# so an unprivileged server cannot read live telemetry. Re-exec under sudo
# unless the user opted out or is already root.
if [ -z "$WEDJAT_UI_NO_SUDO" ] && [ "$(id -u)" != "0" ]; then
  echo "Re-running under sudo: the daemon socket is root-owned (0660)."
  exec sudo -E env "WEDJAT_UI_NO_SUDO=1" "$(readlink -f "$0")"
fi

echo "Starting Wedjat Dev UI on http://localhost:${PORT:-3000}"
exec node server.js