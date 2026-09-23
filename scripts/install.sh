#!/usr/bin/env bash
# wedjat install: daemon (privileged) + ui (unprivileged) + systemd unit.
set -euo pipefail
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "[1/6] building wedjatd..."
go build -o /tmp/wedjatd ./daemon

echo "[2/6] building wedjat ui..."
go build -o /tmp/wedjat ./ui

echo "[3/6] installing binaries..."
install -m 0755 /tmp/wedjatd /usr/local/bin/wedjatd
install -m 0755 /tmp/wedjat /usr/local/bin/wedjat

echo "[4/6] installing systemd unit..."
install -m 0644 "$REPO_DIR/../deploy/wedjatd.service" /etc/systemd/system/wedjatd.service

echo "[5/6] creating user + dirs..."
id -u wedjat >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin wedjat
mkdir -p /var/log/wedjat /var/lib/wedjat /run/wedjat
chown wedjat:wedjat /var/log/wedjat /var/lib/wedjat /run/wedjat

echo "[6/6] enabling daemon..."
systemctl daemon-reload
systemctl enable --now wedjatd
echo "done. wedjatd running, use 'wedjat' for live/history/inspect."
