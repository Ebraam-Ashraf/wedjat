#!/usr/bin/env bash
# wedjat uninstall. Keeps collected history by default.
set -euo pipefail

echo "[1/4] stopping daemon..."
systemctl disable --now wedjatd || true

echo "[2/4] removing unit + binaries..."
rm -f /etc/systemd/system/wedjatd.service
rm -f /usr/local/bin/wedjatd /usr/local/bin/wedjat
# The compiled BPF objects are daemon code, not collected data, so they go with
# the binaries.
rm -rf /usr/local/lib/wedjat
systemctl daemon-reload

echo "[3/4] configuration and history are preserved by default."
read -r -p "Delete Wedjat configuration and collected data? [y/N] " ans || ans=N
if [[ "$ans" =~ ^[yY]$ ]]; then
  DATA_DIR=/var/lib/wedjat
  CONFIG_DIR=/etc/wedjat
  MARKER="$DATA_DIR/.wedjat-data"
  if [ -L "$DATA_DIR" ] || [ -L "$MARKER" ] || [ ! -f "$MARKER" ] ||
    ! printf 'wedjat-data\nversion=1\n' | cmp -s - "$MARKER"; then
    echo "Refusing purge: the Wedjat data marker is missing, invalid, or a symlink." >&2
    exit 1
  fi
  if find "$DATA_DIR" -type l -print -quit | grep -q .; then
    echo "Refusing purge: a symlink exists in the data directory." >&2
    exit 1
  fi
  shopt -s nullglob
  for path in "$DATA_DIR"/meta.db* "$DATA_DIR"/*.db*; do
    name="${path##*/}"
    if [[ "$name" == meta.db* || "$name" =~ ^(r-)?[0-9]{4}-[0-9]{2}-[0-9]{2}\.db($|[-.]) ]]; then
      [ -f "$path" ] && rm -f -- "$path"
    fi
  done
  if [ -d "$DATA_DIR/dumps" ]; then
    find "$DATA_DIR/dumps" -mindepth 1 -depth -delete
  fi
  if [ -f "$CONFIG_DIR/config.yaml" ] && [ ! -L "$CONFIG_DIR/config.yaml" ]; then
    rm -f -- "$CONFIG_DIR/config.yaml"
  fi
  rmdir "$CONFIG_DIR" 2>/dev/null || true
  echo "Known Wedjat data and config files removed; the ownership marker remains."
else
  echo "Configuration and history preserved."
fi

echo "[4/4] done."
