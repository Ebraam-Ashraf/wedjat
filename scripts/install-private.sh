#!/usr/bin/env bash
set -euo pipefail

REPO="Ebraam-Ashraf/wedjat"
ARCHIVE="wedjat-linux-amd64.tar.gz"
CHECKSUM="${ARCHIVE}.sha256"
SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"

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
# Requirements
# ------------------------------------------------------------

print_step "Checking requirements"

command -v gh >/dev/null 2>&1 || die "GitHub CLI (gh) is not installed"
command -v curl >/dev/null 2>&1 || die "curl is not installed"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is not installed"

print_ok "Required commands found"

# ------------------------------------------------------------
# Authentication
# ------------------------------------------------------------

print_step "Checking GitHub authentication"

gh auth status >/dev/null 2>&1 \
    || die "You are not authenticated. Run: gh auth login"

GH_USER="$(gh api user --jq '.login')"

print_ok "Authenticated as $GH_USER"

# ------------------------------------------------------------
# Find latest release
# ------------------------------------------------------------

print_step "Finding latest Wedjat release"

VERSION="$(
    gh release view \
        --repo "$REPO" \
        --json tagName \
        --jq '.tagName'
)"

[ -n "$VERSION" ] || die "Could not determine latest release"

print_ok "Latest release: $VERSION"

# Keep downloads across interruptions so rerunning the installer can resume.
TMP_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TMP_DIR"' EXIT

# ------------------------------------------------------------
# Get GitHub token
# ------------------------------------------------------------

GH_TOKEN="$(gh auth token)"

[ -n "$GH_TOKEN" ] || die "Could not obtain GitHub authentication token"

# ------------------------------------------------------------
# Download helper
# ------------------------------------------------------------

download_asset() {
    local asset_name="$1"
    local output_file="$2"

    local asset_url

    asset_url="$(
        gh api \
            --header "Accept: application/vnd.github+json" \
            "/repos/$REPO/releases/tags/$VERSION" |
        python3 -c '
import json
import sys

data = json.load(sys.stdin)
name = sys.argv[1]

for asset in data["assets"]:
    if asset["name"] == name:
        print(asset["url"])
        sys.exit(0)

sys.exit(1)
' "$asset_name"
    )" || die "Could not find release asset: $asset_name"

    echo "    $asset_name"

    local curl_status

    if curl \
        --fail \
        --location \
        --continue-at - \
        --retry 5 \
        --retry-delay 2 \
        --progress-bar \
        --header "Accept: application/octet-stream" \
        --header "Authorization: Bearer $GH_TOKEN" \
        "$asset_url" \
        -o "$output_file"; then
        :
    else
        curl_status=$?
        # A complete cached file can make curl reject a resume request with
        # HTTP 416. Retry that case from the beginning; keep other failures.
        if [ "$curl_status" -eq 33 ]; then
            curl \
                --fail \
                --location \
                --retry 5 \
                --retry-delay 2 \
                --progress-bar \
                --header "Accept: application/octet-stream" \
                --header "Authorization: Bearer $GH_TOKEN" \
                "$asset_url" \
                -o "$output_file"
        else
            return "$curl_status"
        fi
    fi

    print_ok "$asset_name downloaded"
}

# ------------------------------------------------------------
# Download release
# ------------------------------------------------------------

print_step "Downloading Wedjat"

download_asset \
    "$ARCHIVE" \
    "$TMP_DIR/$ARCHIVE"

download_asset \
    "$CHECKSUM" \
    "$TMP_DIR/$CHECKSUM"

# ------------------------------------------------------------
# Show downloaded files
# ------------------------------------------------------------

print_step "Downloaded files"

ls -lh \
    "$TMP_DIR/$ARCHIVE" \
    "$TMP_DIR/$CHECKSUM"

# ------------------------------------------------------------
# Verify checksum
# ------------------------------------------------------------

print_step "Verifying SHA-256 checksum"

(
    cd "$TMP_DIR"
    sha256sum --check "$CHECKSUM"
)

print_ok "SHA-256 verified"

# ------------------------------------------------------------
# Install
# ------------------------------------------------------------

print_step "Installing Wedjat"

sudo bash "$SCRIPT_DIR/install.sh" --local "$TMP_DIR"

print_ok "Wedjat installed"

# ------------------------------------------------------------
# Verify service
# ------------------------------------------------------------

print_step "Checking Wedjat service"

if sudo systemctl is-active --quiet wedjatd.service; then
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
echo " Binary  : /usr/local/bin/wedjatd"
echo " Service : wedjatd.service"
echo
echo " Dashboard:"
echo "   sudo wedjat"
echo