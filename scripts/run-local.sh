#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${PHLOX_GW_ENV_FILE:-"$ROOT_DIR/.env"}"

if [[ -f "$ENV_FILE" ]]; then
  set -a
  # shellcheck source=/dev/null
  . "$ENV_FILE"
  set +a
fi

export PHLOX_GW_ADDR="${PHLOX_GW_ADDR:-127.0.0.1:8080}"
export PHLOX_GW_DATA_DIR="${PHLOX_GW_DATA_DIR:-"$ROOT_DIR/.phlox-gw-data"}"

if [[ "$PHLOX_GW_DATA_DIR" != /* ]]; then
  PHLOX_GW_DATA_DIR="$ROOT_DIR/$PHLOX_GW_DATA_DIR"
  export PHLOX_GW_DATA_DIR
fi

mkdir -p "$PHLOX_GW_DATA_DIR"

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 48
  else
    LC_ALL=C tr -dc 'A-Za-z0-9_-' < /dev/urandom | head -c 64
    printf '\n'
  fi
}

if [[ -z "${PHLOX_GW_SESSION_SECRET:-}" ]]; then
  SECRET_FILE="${PHLOX_GW_SESSION_SECRET_FILE:-"$PHLOX_GW_DATA_DIR/session-secret"}"
  if [[ ! -f "$SECRET_FILE" ]]; then
    umask 077
    generate_secret > "$SECRET_FILE"
  fi
  export PHLOX_GW_SESSION_SECRET="$(< "$SECRET_FILE")"
fi

detect_binary() {
  if [[ -n "${PHLOX_GW_BINARY:-}" ]]; then
    printf '%s\n' "$PHLOX_GW_BINARY"
    return
  fi
  if [[ -x "$ROOT_DIR/phlox-gw" ]]; then
    printf '%s\n' "$ROOT_DIR/phlox-gw"
    return
  fi

  local os arch candidate
  case "$(uname -s)" in
    Darwin) os="darwin" ;;
    Linux) os="linux" ;;
    *) os="" ;;
  esac
  case "$(uname -m)" in
    arm64|aarch64) arch="arm64" ;;
    x86_64|amd64) arch="amd64" ;;
    *) arch="" ;;
  esac
  candidate="$ROOT_DIR/dist/phlox-gw-$os-$arch"
  if [[ -n "$os" && -n "$arch" && -x "$candidate" ]]; then
    printf '%s\n' "$candidate"
    return
  fi
  printf '%s\n' "$ROOT_DIR/phlox-gw"
}

BINARY="$(detect_binary)"
if [[ ! -x "$BINARY" ]]; then
  echo "Phlox-GW binary not found or not executable: $BINARY" >&2
  echo "Build it with: scripts/build-release.sh --skip-frontend, or go build -o phlox-gw ./cmd/phlox-gw" >&2
  exit 1
fi

echo "Starting Phlox-GW"
echo "  binary: $BINARY"
echo "  addr:   $PHLOX_GW_ADDR"
echo "  data:   $PHLOX_GW_DATA_DIR"
exec "$BINARY"
