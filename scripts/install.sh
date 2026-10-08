#!/usr/bin/env bash
#
# Wedjat installer
#
#   curl -sSfL https://raw.githubusercontent.com/Ebraam-Ashraf/wedjat/main/scripts/install.sh | sudo bash
#   sudo ./install.sh --version v1.2.3
#   sudo ./install.sh --local [DIST_DIR]
#
# Everything lives inside main(), which is only called on the last line.
# That way bash has parsed the whole script before running any of it, so
# nothing in here can swallow the rest of the script from stdin when it is
# piped through `curl | bash`.

set -euo pipefail
IFS=$'\n\t'

readonly REPO_OWNER="Ebraam-Ashraf"
readonly REPO_NAME="wedjat"

readonly INSTALL_DIR="/usr/local/bin"
readonly LIB_DIR="/usr/local/lib/wedjat"
readonly CONFIG_DIR="/etc/wedjat"
readonly DATA_DIR="/var/lib/wedjat"
readonly SERVICE_DIR="/etc/systemd/system"

MODE=remote
VERSION=latest
DIST_DIR=""
WORK_DIR=""

# ------------------------------------------------------------
# Logging
# ------------------------------------------------------------

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RESET=$'\033[0m'
    C_BOLD=$'\033[1m'
    C_DIM=$'\033[2m'
    C_RED=$'\033[31m'
    C_GREEN=$'\033[32m'
    C_YELLOW=$'\033[33m'
    C_BLUE=$'\033[34m'
else
    C_RESET="" C_BOLD="" C_DIM="" C_RED="" C_GREEN="" C_YELLOW="" C_BLUE=""
fi

step() { printf '\n%s==>%s %s%s%s\n' "$C_BLUE" "$C_RESET" "$C_BOLD" "$1" "$C_RESET"; }
ok()   { printf '  %s✓%s %s\n' "$C_GREEN" "$C_RESET" "$1"; }
info() { printf '  %s%s%s\n' "$C_DIM" "$1" "$C_RESET"; }
warn() { printf '  %s!%s %s\n' "$C_YELLOW" "$C_RESET" "$1" >&2; }
die()  { printf '\n%sERROR:%s %s\n' "$C_RED" "$C_RESET" "$1" >&2; exit 1; }

usage() {
    cat <<'EOF'
Usage: install.sh [--local [DIST_DIR]] [--version TAG]

  (default)         download and install the latest GitHub Release
  --version TAG     install a specific release tag (for example v1.2.3)
  --local [DIR]     install a pre-built archive from DIR (default: ./dist)
  -h, --help        show this help
EOF
}

cleanup() {
    [ -n "$WORK_DIR" ] && rm -rf -- "$WORK_DIR"
}

# ------------------------------------------------------------
# Steps
# ------------------------------------------------------------

parse_args() {
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --local)
                MODE=local
                shift
                if [ "$#" -gt 0 ] && [[ "$1" != -* ]]; then
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
                die "unknown option: $1 (see --help)"
                ;;
        esac
    done
}

check_requirements() {
    step "Checking requirements"

    [ "$EUID" -eq 0 ] || die "run as root, for example: curl ... | sudo bash"
    [ "$(uname -s)" = Linux ] || die "Wedjat supports Linux only"

    local cmd
    local required=(systemctl install tar sha256sum getent)
    [ "$MODE" = remote ] && required+=(curl)

    for cmd in "${required[@]}"; do
        command -v "$cmd" >/dev/null 2>&1 || die "$cmd is required but not installed"
    done

    case "$(uname -m)" in
        x86_64|amd64)  ARCH=amd64 ;;
        aarch64|arm64) ARCH=arm64 ;;
        *) die "unsupported CPU architecture: $(uname -m)" ;;
    esac
    ASSET="wedjat-linux-$ARCH.tar.gz"

    ok "Linux/$ARCH with systemd"
}

fetch() {
    # fetch URL DEST : quiet download, progress bar only on a terminal
    local url="$1" dest="$2"
    local opts=(--fail --location --retry 5 --retry-delay 2)

    if [ -t 2 ]; then
        opts+=(--progress-bar)
    else
        opts+=(--silent --show-error)
    fi

    curl "${opts[@]}" "$url" -o "$dest"
}

get_release() {
    ARCHIVE="$WORK_DIR/$ASSET"

    if [ "$MODE" = local ]; then
        step "Using local release"

        if [ -z "$DIST_DIR" ]; then
            DIST_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")/.." && pwd)/dist"
        fi

        [ -f "$DIST_DIR/$ASSET" ]        || die "missing $DIST_DIR/$ASSET (build it with: make release)"
        [ -f "$DIST_DIR/$ASSET.sha256" ] || die "missing $DIST_DIR/$ASSET.sha256"

        cp -- "$DIST_DIR/$ASSET" "$ARCHIVE"
        cp -- "$DIST_DIR/$ASSET.sha256" "$ARCHIVE.sha256"

        ok "$ASSET from $DIST_DIR"
        return
    fi

    step "Downloading Wedjat ($VERSION)"

    [[ "$VERSION" = latest || "$VERSION" =~ ^v[0-9][A-Za-z0-9.+_-]*$ ]] \
        || die "invalid release tag: $VERSION"

    local base="https://github.com/$REPO_OWNER/$REPO_NAME/releases"
    if [ "$VERSION" = latest ]; then
        base="$base/latest/download"
    else
        base="$base/download/$VERSION"
    fi

    info "$ASSET"
    fetch "$base/$ASSET" "$ARCHIVE" || die "release asset not found: $ASSET"
    fetch "$base/$ASSET.sha256" "$ARCHIVE.sha256" || die "release checksum not found: $ASSET.sha256"

    ok "Downloaded $ASSET ($(du -h -- "$ARCHIVE" | cut -f1))"
}

verify_release() {
    step "Verifying release"

    (cd "$WORK_DIR" && sha256sum --check --quiet "$ASSET.sha256") \
        || die "SHA-256 checksum verification failed"
    ok "SHA-256 checksum matches"

    local member
    while IFS= read -r member; do
        case "$member" in
            wedjatd|wedjat|config.yaml|wedjatd.service|ebpf|ebpf/|ebpf/*.bpf.o) ;;
            *) die "unexpected path in release archive: $member" ;;
        esac
    done < <(tar --list --gzip --file "$ARCHIVE")
    ok "Archive contents are as expected"

    tar --extract --gzip --file "$ARCHIVE" --directory "$WORK_DIR" \
        --no-same-owner --no-same-permissions

    local f
    for f in wedjatd wedjat config.yaml wedjatd.service; do
        [ -f "$WORK_DIR/$f" ] || die "archive does not contain $f"
    done
}

install_files() {
    step "Installing files"

    getent group wedjat >/dev/null || groupadd --system wedjat

    install -d -o root -g wedjat -m 0750  "$CONFIG_DIR"
    install -d -o root -g wedjat -m 2750  "$DATA_DIR"

    install -o root -g root -m 0755 "$WORK_DIR/wedjatd" "$INSTALL_DIR/wedjatd"
    install -o root -g root -m 0755 "$WORK_DIR/wedjat"  "$INSTALL_DIR/wedjat"
    ok "Binaries      -> $INSTALL_DIR"

    if [ -e "$CONFIG_DIR/config.yaml" ]; then
        info "Config        kept existing $CONFIG_DIR/config.yaml"
    else
        install -o root -g wedjat -m 0640 "$WORK_DIR/config.yaml" "$CONFIG_DIR/config.yaml"
        ok "Config        -> $CONFIG_DIR/config.yaml"
    fi

    install -o root -g root -m 0644 "$WORK_DIR/wedjatd.service" "$SERVICE_DIR/wedjatd.service"
    ok "Service unit  -> $SERVICE_DIR/wedjatd.service"

    if [ -d "$WORK_DIR/ebpf" ]; then
        install -d -o root -g root -m 0755 "$LIB_DIR/ebpf"
        install -o root -g root -m 0644 "$WORK_DIR"/ebpf/*.bpf.o "$LIB_DIR/ebpf/"
        ok "eBPF objects  -> $LIB_DIR/ebpf"
    fi

    local user="${SUDO_USER:-}"
    if [ -n "$user" ] && [ "$user" != root ] && id "$user" >/dev/null 2>&1; then
        if ! id -nG "$user" | tr ' ' '\n' | grep -qx wedjat; then
            usermod -aG wedjat "$user"
            ok "Added $user to the wedjat group"
            info "Log out and back in for database access"
        fi
    fi
}

start_service() {
    step "Starting service"

    systemctl daemon-reload
    systemctl enable --quiet wedjatd.service
    systemctl restart wedjatd.service || true

    if systemctl is-active --quiet wedjatd.service; then
        ok "wedjatd is running"
    else
        warn "wedjatd is not running"
        info "Check with: sudo systemctl status wedjatd"
        info "Logs:       sudo journalctl -u wedjatd -n 50 --no-pager"
    fi
}

summary() {
    printf '\n%s%s Wedjat installed%s\n\n' "$C_GREEN" "✓" "$C_RESET"
    printf '  %-10s %s\n' "Release"   "$VERSION"
    printf '  %-10s %s\n' "Binary"    "$INSTALL_DIR/wedjatd"
    printf '  %-10s %s\n' "Service"   "wedjatd.service"
    printf '  %-10s %s\n' "Config"    "$CONFIG_DIR/config.yaml"
    printf '\n  Open the dashboard with:  %ssudo wedjat%s\n\n' "$C_BOLD" "$C_RESET"
}

# ------------------------------------------------------------
# Main
# ------------------------------------------------------------

main() {
    parse_args "$@"
    check_requirements

    WORK_DIR="$(mktemp -d /tmp/wedjat.XXXXXX)"
    trap cleanup EXIT

    get_release
    verify_release
    install_files
    start_service
    summary
}

main "$@"
