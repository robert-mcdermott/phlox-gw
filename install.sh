#!/bin/sh

set -eu
umask 022

REPOSITORY="robert-mcdermott/phlox-gw"
GITHUB_URL="https://github.com/${REPOSITORY}"
BINARY_NAME="phlox-gw"

VERSION="${PHLOX_GW_VERSION:-latest}"
INSTALL_DIR="${PHLOX_GW_INSTALL_DIR:-}"
TMP_DIR=""
DEST_TMP=""

info() {
    printf '==> %s\n' "$*"
}

warn() {
    printf 'Warning: %s\n' "$*" >&2
}

error() {
    printf 'Error: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'USAGE'
Install the latest Phlox-GW release for macOS or Linux.

Usage:
  install.sh [--version VERSION] [--install-dir DIRECTORY]

Options:
  --version VERSION       Release to install, such as v0.1.0. Defaults to latest.
  --install-dir DIRECTORY Installation directory. Defaults to $HOME/.local/bin.
  -h, --help              Show this help.

Environment:
  PHLOX_GW_VERSION        Same as --version.
  PHLOX_GW_INSTALL_DIR    Same as --install-dir.

The installer downloads the matching raw release binary and checksums.txt from
GitHub, verifies SHA-256, and atomically installs the binary as phlox-gw. It does
not use sudo, modify PATH, create application data, or start Phlox-GW.
USAGE
}

has_command() {
    command -v "$1" >/dev/null 2>&1
}

cleanup() {
    if [ -n "${DEST_TMP:-}" ] && [ -e "$DEST_TMP" ]; then
        rm -f "$DEST_TMP"
    fi
    if [ -n "${TMP_DIR:-}" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

trap cleanup 0
trap 'exit 1' HUP INT TERM

while [ "$#" -gt 0 ]; do
    case "$1" in
        --version)
            [ "$#" -ge 2 ] || error "--version requires a value"
            VERSION="$2"
            shift 2
            ;;
        --install-dir)
            [ "$#" -ge 2 ] || error "--install-dir requires a value"
            INSTALL_DIR="$2"
            shift 2
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            error "unknown argument: $1"
            ;;
    esac
done

if [ -z "$INSTALL_DIR" ]; then
    [ -n "${HOME:-}" ] || error "HOME is not set; provide --install-dir"
    INSTALL_DIR="$HOME/.local/bin"
fi

case "$INSTALL_DIR" in
    /*) ;;
    *) error "installation directory must be an absolute path: $INSTALL_DIR" ;;
esac

if [ "$VERSION" != "latest" ]; then
    case "$VERSION" in
        [0-9]*) VERSION="v$VERSION" ;;
    esac
    case "$VERSION" in
        v[0-9]*) ;;
        *) error "invalid release version: $VERSION" ;;
    esac
    case "$VERSION" in
        *[!0-9A-Za-z._-]*) error "invalid release version: $VERSION" ;;
    esac
fi

has_command curl || error "curl is required"
has_command uname || error "uname is required"
has_command awk || error "awk is required"
has_command mktemp || error "mktemp is required"

OS_NAME=$(uname -s)
ARCH_NAME=$(uname -m)

case "$OS_NAME" in
    Darwin)
        case "$ARCH_NAME" in
            arm64|aarch64) ASSET="phlox-gw-darwin-arm64" ;;
            x86_64|amd64) error "macOS Intel is not currently a supported release target" ;;
            *) error "unsupported macOS architecture: $ARCH_NAME" ;;
        esac
        ;;
    Linux)
        case "$ARCH_NAME" in
            x86_64|amd64) ASSET="phlox-gw-linux-amd64" ;;
            arm64|aarch64) ASSET="phlox-gw-linux-arm64" ;;
            *) error "unsupported Linux architecture: $ARCH_NAME" ;;
        esac
        ;;
    *)
        error "unsupported operating system: $OS_NAME"
        ;;
esac

if [ "$VERSION" = "latest" ]; then
    DOWNLOAD_BASE="${GITHUB_URL}/releases/latest/download"
    VERSION_LABEL="latest stable release"
else
    DOWNLOAD_BASE="${GITHUB_URL}/releases/download/${VERSION}"
    VERSION_LABEL="$VERSION"
fi

TMP_DIR=$(mktemp -d 2>/dev/null || mktemp -d -t phlox-gw-install) ||
    error "could not create a temporary directory"
BINARY_PATH="$TMP_DIR/$ASSET"
CHECKSUM_PATH="$TMP_DIR/checksums.txt"

download() {
    source_url="$1"
    destination="$2"
    curl \
        --proto '=https' \
        --proto-redir '=https' \
        --tlsv1.2 \
        --fail \
        --silent \
        --show-error \
        --location \
        --retry 3 \
        --connect-timeout 15 \
        --output "$destination" \
        "$source_url"
}

info "Installing Phlox-GW $VERSION_LABEL for $OS_NAME/$ARCH_NAME"
info "Downloading $ASSET"
download "$DOWNLOAD_BASE/$ASSET" "$BINARY_PATH" ||
    error "failed to download $ASSET"

info "Downloading checksums.txt"
download "$DOWNLOAD_BASE/checksums.txt" "$CHECKSUM_PATH" ||
    error "failed to download checksums.txt"

EXPECTED=$(
    awk -v filename="$ASSET" '
        $2 == filename || $2 == "*" filename {
            count++
            checksum = $1
        }
        END {
            if (count != 1) {
                exit 1
            }
            print checksum
        }
    ' "$CHECKSUM_PATH"
) || error "checksums.txt does not contain exactly one entry for $ASSET"

[ "${#EXPECTED}" -eq 64 ] || error "invalid SHA-256 value for $ASSET"
case "$EXPECTED" in
    *[!0-9A-Fa-f]*) error "invalid SHA-256 value for $ASSET" ;;
esac

if has_command sha256sum; then
    ACTUAL=$(sha256sum "$BINARY_PATH" | awk '{ print $1 }')
elif has_command shasum; then
    ACTUAL=$(shasum -a 256 "$BINARY_PATH" | awk '{ print $1 }')
else
    error "sha256sum or shasum is required"
fi

EXPECTED=$(printf '%s' "$EXPECTED" | tr '[:upper:]' '[:lower:]')
ACTUAL=$(printf '%s' "$ACTUAL" | tr '[:upper:]' '[:lower:]')

if [ "$EXPECTED" != "$ACTUAL" ]; then
    printf 'Checksum verification failed for %s\n' "$ASSET" >&2
    printf 'Expected: %s\n' "$EXPECTED" >&2
    printf 'Actual:   %s\n' "$ACTUAL" >&2
    exit 1
fi

info "Checksum verified"

if [ ! -d "$INSTALL_DIR" ]; then
    mkdir -p "$INSTALL_DIR" 2>/dev/null ||
        error "cannot create $INSTALL_DIR; choose a writable --install-dir"
fi
[ -w "$INSTALL_DIR" ] ||
    error "cannot write to $INSTALL_DIR; choose a writable --install-dir"

DESTINATION="$INSTALL_DIR/$BINARY_NAME"
DEST_TMP=$(mktemp "$INSTALL_DIR/.phlox-gw.install.XXXXXX") ||
    error "failed to create a temporary installation file in $INSTALL_DIR"
cp "$BINARY_PATH" "$DEST_TMP" || error "failed to copy binary into $INSTALL_DIR"
chmod 0755 "$DEST_TMP" || error "failed to make the installed binary executable"
mv -f "$DEST_TMP" "$DESTINATION" || error "failed to install $DESTINATION"
DEST_TMP=""

INSTALLED_VERSION=$("$DESTINATION" --version 2>/dev/null) ||
    error "the installed binary did not pass its version check"

info "$INSTALLED_VERSION"
info "Installed at $DESTINATION"

case ":${PATH:-}:" in
    *:"$INSTALL_DIR":*)
        RUN_COMMAND="$BINARY_NAME"
        ;;
    *)
        warn "$INSTALL_DIR is not currently in PATH"
        warn "Add it to PATH or run $DESTINATION directly"
        RUN_COMMAND="$DESTINATION"
        ;;
esac

cat <<EOF

For a local evaluation:
  mkdir -p "${HOME:-$INSTALL_DIR}/.local/share/phlox-gw"
  cd "${HOME:-$INSTALL_DIR}/.local/share/phlox-gw"
  $RUN_COMMAND

Phlox-GW will print a one-time administrator password and listen on
http://127.0.0.1:8080. Before shared or production use, configure a persistent
PHLOX_GW_DATA_DIR and a stable high-entropy PHLOX_GW_SESSION_SECRET.

Documentation: https://github.com/${REPOSITORY}#production-oriented-run
EOF
