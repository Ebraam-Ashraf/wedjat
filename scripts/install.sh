```bash
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

# ------------------------------------------------------------
# Logging helpers
# ------------------------------------------------------------

print_step() {
    printf '\n==> %s\n' "$1"
}

print_ok() {
    printf '    ✓ %s\n' "$1"
}

die() {
    printf '\nERROR: %s\n' "$1" >&2
    exit 1
}

# ------------------------------------------------------------
# Usage
# ------------------------------------------------------------

usage() {
    printf '%s\n' \
        "Usage: install.sh [--local [DIST_DIR]] [--version TAG]" \
        "Default downloads the latest GitHub Release; --local installs a built archive."
}

# ------------------------------------------------------------
# Arguments
# ------------------------------------------------------------

while [ "$#" -gt 0 ]; do
    case "$1" in
        --local)
            MODE=local
            shift

            if [ "$#" -gt 0 ] && [[ "$1" != --* ]]; then
                DIST_DIR="$1"
                shift
            fi
            ;;

        --version)
            [ "$#" -ge 2 ] || die "--version requires a release tag"

            VERSION="$2"
            shift 2
            ;;

        -h|--help)
            usage
            exit 0
            ;;

        *)
            die "unknown option: $1"
            ;;
    esac
done

# ------------------------------------------------------------
# Requirements
# ------------------------------------------------------------

print_step "Checking requirements"

[ "$EUID" -eq 0 ] \
    || die "run as root (for example: curl ... | sudo bash)"

[ "$(uname -s)" = Linux ] \
    || die "Wedjat currently supports Linux only"

command -v systemctl >/dev/null 2>&1 \
    || die "systemd is required"

command -v install >/dev/null 2>&1 \
    || die "install is required"

command -v tar >/dev/null 2>&1 \
    || die "tar is required"

command -v sha256sum >/dev/null 2>&1 \
    || die "sha256sum is required"

if [ "$MODE" = remote ]; then
    command -v curl >/dev/null 2>&1 \
        || die "curl is required"
fi

print_ok "Required commands found"

# ------------------------------------------------------------
# Architecture
# ------------------------------------------------------------

case "$(uname -m)" in
    x86_64|amd64)
        ARCH=amd64
        ;;

    aarch64|arm64)
        ARCH=arm64
        ;;

    *)
        die "unsupported CPU architecture: $(uname -m)"
        ;;
esac

ASSET="wedjat-linux-$ARCH.tar.gz"

# ------------------------------------------------------------
# Temporary directory
# ------------------------------------------------------------

WORK_DIR="$(mktemp -d /tmp/wedjat.XXXXXX)"

trap 'rm -rf -- "$WORK_DIR"' EXIT

ARCHIVE="$WORK_DIR/$ASSET"

# ------------------------------------------------------------
# Download / local archive
# ------------------------------------------------------------

if [ "$MODE" = local ]; then

    print_step "Preparing local release"

    SOURCE_ARCHIVE="$DIST_DIR/$ASSET"

    [ -f "$SOURCE_ARCHIVE" ] \
        || die "missing $SOURCE_ARCHIVE; build it with make release"

    [ -f "$SOURCE_ARCHIVE.sha256" ] \
        || die "missing $SOURCE_ARCHIVE.sha256"

    cp -- "$SOURCE_ARCHIVE" "$ARCHIVE"
    cp -- "$SOURCE_ARCHIVE.sha256" "$ARCHIVE.sha256"

    print_ok "Local release copied"

else

    print_step "Downloading Wedjat"

    [[ "$VERSION" = latest || "$VERSION" =~ ^v[0-9][A-Za-z0-9.+_-]*$ ]] \
        || die "invalid release tag: $VERSION"

    if [ "$VERSION" = latest ]; then
        RELEASE_BASE="https://github.com/$REPO_OWNER/$REPO_NAME/releases/latest/download"
    else
        RELEASE_BASE="https://github.com/$REPO_OWNER/$REPO_NAME/releases/download/$VERSION"
    fi

    echo "    $ASSET"

    curl \
        --fail \
        --location \
        --retry 5 \
        --retry-delay 2 \
        --progress-bar \
        "$RELEASE_BASE/$ASSET" \
        -o "$ARCHIVE" \
        || die "release asset not found: $ASSET"

    print_ok "$ASSET downloaded"

    echo "    $ASSET.sha256"

    curl \
        --fail \
        --location \
        --retry 5 \
        --retry-delay 2 \
        --progress-bar \
        "$RELEASE_BASE/$ASSET.sha256" \
        -o "$ARCHIVE.sha256" \
        || die "release checksum not found"

    print_ok "$ASSET.sha256 downloaded"
fi

# ------------------------------------------------------------
# Show downloaded files
# ------------------------------------------------------------

print_step "Downloaded files"

ls -lh \
    "$ARCHIVE" \
    "$ARCHIVE.sha256"

# ------------------------------------------------------------
# Verify checksum
# ------------------------------------------------------------

print_step "Verifying SHA-256 checksum"

(
    cd "$WORK_DIR"
    sha256sum --check "$ASSET.sha256"
) || die "release checksum verification failed"

print_ok "SHA-256 verified"

# ------------------------------------------------------------
# Validate archive
# ------------------------------------------------------------

print_step "Validating release archive"

while IFS= read -r member; do
    case "$member" in
        wedjatd|wedjat|config.yaml|wedjatd.service)
            ;;

        ebpf|ebpf/)
            ;;

        ebpf/*.bpf.o)
            ;;

        *)
            die "unexpected path in release archive: $member"
            ;;
    esac
done < <(tar --list --gzip --file "$ARCHIVE")

print_ok "Release archive validated"

# ------------------------------------------------------------
# Extract
# ------------------------------------------------------------

tar \
    --extract \
    --gzip \
    --file "$ARCHIVE" \
    --directory "$WORK_DIR" \
    --no-same-owner \
    --no-same-permissions

[ -f "$WORK_DIR/wedjatd" ] \
    || die "archive does not contain wedjatd"

[ -f "$WORK_DIR/wedjat" ] \
    || die "archive does not contain wedjat"

[ -f "$WORK_DIR/config.yaml" ] \
    || die "archive does not contain config.yaml"

[ -f "$WORK_DIR/wedjatd.service" ] \
    || die "archive does not contain wedjatd.service"

# ------------------------------------------------------------
# Install
# ------------------------------------------------------------

print_step "Installing Wedjat"

command -v getent >/dev/null 2>&1 \
    || die "getent is required"

if ! getent group wedjat >/dev/null; then
    groupadd --system wedjat
fi

install \
    -d \
    -o root \
    -g wedjat \
    -m 0750 \
    "$CONFIG_DIR"

install \
    -d \
    -o root \
    -g wedjat \
    -m 2750 \
    "$DATA_DIR"

install \
    -o root \
    -g root \
    -m 0755 \
    "$WORK_DIR/wedjatd" \
    "$INSTALL_DIR/wedjatd"

install \
    -o root \
    -g root \
    -m 0755 \
    "$WORK_DIR/wedjat" \
    "$INSTALL_DIR/wedjat"

if [ ! -e "$CONFIG_DIR/config.yaml" ]; then
    install \
        -o root \
        -g wedjat \
        -m 0640 \
        "$WORK_DIR/config.yaml" \
        "$CONFIG_DIR/config.yaml"
fi

install \
    -o root \
    -g root \
    -m 0644 \
    "$WORK_DIR/wedjatd.service" \
    "$SERVICE_DIR/wedjatd.service"

# The compiled BPF objects are the daemon's tracer.
if [ -d "$WORK_DIR/ebpf" ]; then
    install \
        -d \
        -o root \
        -g root \
        -m 0755 \
        "$LIB_DIR/ebpf"

    install \
        -o root \
        -g root \
        -m 0644 \
        "$WORK_DIR"/ebpf/*.bpf.o \
        "$LIB_DIR/ebpf/"
fi

print_ok "Wedjat installed"

# ------------------------------------------------------------
# User group
# ------------------------------------------------------------

SUDO_USER="$(printenv SUDO_USER 2>/dev/null || true)"

if [ -n "$SUDO_USER" ] \
    && id "$SUDO_USER" >/dev/null 2>&1 \
    && command -v usermod >/dev/null 2>&1; then

    if ! id -nG "$SUDO_USER" \
        | tr ' ' '\n' \
        | grep -qx wedjat; then

        usermod -aG wedjat "$SUDO_USER"

        printf \
            '    Added %s to the wedjat group; log out and back in for database access.\n' \
            "$SUDO_USER"
    fi
fi

# ------------------------------------------------------------
# Systemd
# ------------------------------------------------------------

print_step "Configuring Wedjat service"

systemctl daemon-reload
systemctl enable wedjatd.service

if systemctl is-active --quiet wedjatd.service; then
    systemctl restart wedjatd.service
else
    systemctl start wedjatd.service
fi

print_ok "wedjatd service enabled"

# ------------------------------------------------------------
# Verify service
# ------------------------------------------------------------

print_step "Checking Wedjat service"

if systemctl is-active --quiet wedjatd.service; then
    print_ok "wedjatd is running"
else
    echo "    WARNING: wedjatd is not currently running"
    echo "    Check with: sudo systemctl status wedjatd"
fi

# ------------------------------------------------------------
# Done
# ------------------------------------------------------------

echo
echo "=============================================="
echo " Wedjat installation completed"
echo "=============================================="
echo
echo " Release : $VERSION"
echo " Binary  : $INSTALL_DIR/wedjatd"
echo " Service : wedjatd.service"
echo
echo " Dashboard:"
echo "   sudo wedjat"
echo
```