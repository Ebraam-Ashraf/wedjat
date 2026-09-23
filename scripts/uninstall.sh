#!/usr/bin/env bash
# wedjat uninstall. Keeps collected history by default.
set -euo pipefail

echo "[1/4] stopping daemon..."
systemctl disable --now wedjatd || true

echo "[2/4] removing unit + binaries..."
rm -f /etc/systemd/system/wedjatd.service
rm -f /usr/local/bin/wedjatd /usr/local/bin/wedjat
systemctl daemon-reload

echo "[3/4] logs kept at /var/log/wedjat + /var/lib/wedjat."
echo "      delete manually if wanted: rm -rf /var/log/wedjat /var/lib/wedjat"
read -r -p "Delete history now? [y/N] " ans || ans=N
if [ "$ans" = "y" ] || [ "$ans" = "Y" ]; then
  rm -rf /var/log/wedjat /var/lib/wedjat
  echo "history deleted."
fi

echo "[4/4] done."
