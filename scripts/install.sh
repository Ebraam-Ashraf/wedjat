#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

REPO_OWNER="Ebraam-Ashraf"
REPO_NAME="wedjat"
INSTALL_DIR="/usr/local/bin"
LIB_DIR="/usr/local/lib/wedjat"
CONFIG_DIR="/etc/wedjat"
DATA_DIR="/var/lib/wedjat"
SERVICE_DIR="/etc/systemd/system"
SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
MODE=remote
VERSION=latest
DIST_DIR="$ROOT_DIR/dist"

usage() {
  printf '%s\n' "Usage: install.sh [--local [DIST_DIR]] [--version TAG]"
  printf '%s\n' "Default downloads the latest GitHub Release; --local installs a built archive."
}
die() { printf 'wedjat installer: %s\n' "$*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    --local)
      MODE=local
      shift
      if [ "$#" -gt 0 ] && [[ "$1" != --* ]]; then DIST_DIR="$1"; shift; fi
      ;;
    --version)
      [ "$#" -ge 2 ] || die "--version requires a release tag"
      VERSION="$2"
      shift 2
      ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[ "$EUID" -eq 0 ] || die "run as root (for example: curl ... | sudo bash)"
[ "$(uname -s)" = Linux ] || die "Wedjat currently supports Linux only"
command -v systemctl >/dev/null || die "systemd is required"
command -v install >/dev/null || die "install is required"
command -v tar >/dev/null || die "tar is required"
command -v sha256sum >/dev/null || die "sha256sum is required"
if [ "$MODE" = remote ]; then command -v curl >/dev/null || die "curl is required"; fi

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported CPU architecture: $(uname -m)" ;;
esac
ASSET="wedjat-linux-$ARCH.tar.gz"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf -- "$WORK_DIR"' EXIT
ARCHIVE="$WORK_DIR/$ASSET"

if [ "$MODE" = local ]; then
  SOURCE_ARCHIVE="$DIST_DIR/$ASSET"
  [ -f "$SOURCE_ARCHIVE" ] || die "missing $SOURCE_ARCHIVE; build it with make release"
  [ -f "$SOURCE_ARCHIVE.sha256" ] || die "missing checksum $SOURCE_ARCHIVE.sha256"
  cp -- "$SOURCE_ARCHIVE" "$ARCHIVE"
  cp -- "$SOURCE_ARCHIVE.sha256" "$ARCHIVE.sha256"
else
  [[ "$VERSION" = latest || "$VERSION" =~ ^v[0-9][A-Za-z0-9.+_-]*$ ]] || die "invalid release tag: $VERSION"
  if [ "$VERSION" = latest ]; then
    RELEASE_BASE="https://github.com/$REPO_OWNER/$REPO_NAME/releases/latest/download"
  else
    RELEASE_BASE="https://github.com/$REPO_OWNER/$REPO_NAME/releases/download/$VERSION"
  fi
  curl --fail --location --silent --show-error "$RELEASE_BASE/$ASSET" -o "$ARCHIVE" || die "release asset not found: $ASSET"
  curl --fail --location --silent --show-error "$RELEASE_BASE/$ASSET.sha256" -o "$ARCHIVE.sha256" || die "release checksum not found"
fi

(cd "$WORK_DIR" && sha256sum --check "$ASSET.sha256") || die "release checksum verification failed"

while IFS= read -r member; do
  case "$member" in
    wedjatd|wedjat|config.yaml|wedjatd.service) ;;
    ebpf|ebpf/) ;;
    ebpf/*.bpf.o) ;;
    *) die "unexpected path in release archive: $member" ;;
  esac
done < <(tar --list --gzip --file "$ARCHIVE")
tar --extract --gzip --file "$ARCHIVE" --directory "$WORK_DIR" --no-same-owner --no-same-permissions
[ -f "$WORK_DIR/wedjatd" ] || die "archive does not contain wedjatd"
[ -f "$WORK_DIR/wedjat" ] || die "archive does not contain wedjat"
[ -f "$WORK_DIR/config.yaml" ] || die "archive does not contain config.yaml"
[ -f "$WORK_DIR/wedjatd.service" ] || die "archive does not contain wedjatd.service"

command -v getent >/dev/null || die "getent is required"
if ! getent group wedjat >/dev/null; then groupadd --system wedjat; fi
install -d -o root -g wedjat -m 0750 "$CONFIG_DIR"
install -d -o root -g wedjat -m 2750 "$DATA_DIR"
install -o root -g root -m 0755 "$WORK_DIR/wedjatd" "$INSTALL_DIR/wedjatd"
install -o root -g root -m 0755 "$WORK_DIR/wedjat" "$INSTALL_DIR/wedjat"
if [ ! -e "$CONFIG_DIR/config.yaml" ]; then
  install -o root -g wedjat -m 0640 "$WORK_DIR/config.yaml" "$CONFIG_DIR/config.yaml"
fi
install -o root -g root -m 0644 "$WORK_DIR/wedjatd.service" "$SERVICE_DIR/wedjatd.service"

# The compiled BPF objects are the daemon's tracer, not optional extras: without
# them it loads nothing and falls back to NVML-only polling.
if [ -d "$WORK_DIR/ebpf" ]; then
  install -d -o root -g root -m 0755 "$LIB_DIR/ebpf"
  install -o root -g root -m 0644 "$WORK_DIR"/ebpf/*.bpf.o "$LIB_DIR/ebpf/"
fi

SUDO_USER="$(printenv SUDO_USER 2>/dev/null || true)"
if [ -n "$SUDO_USER" ] && id "$SUDO_USER" >/dev/null 2>&1 && command -v usermod >/dev/null; then
  if ! id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx wedjat; then
    usermod -aG wedjat "$SUDO_USER"
    printf 'Added %s to the wedjat group; log out and back in for database access.\n' "$SUDO_USER"
  fi
fi

systemctl daemon-reload
systemctl enable wedjatd.service
if systemctl is-active --quiet wedjatd.service; then
  systemctl restart wedjatd.service
else
  systemctl start wedjatd.service
fi
printf '\nWedjat daemon installed and running.\n'
printf "Run 'sudo wedjat' to open the dashboard.\n"