#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DIST_DIR="${DIST_DIR:-"$ROOT_DIR/dist"}"
SKIP_FRONTEND=0
CLEAN=0

usage() {
  cat <<'USAGE'
Usage: scripts/build-release.sh [--skip-frontend] [--clean]

Builds the embedded frontend, then cross-compiles Phlox-GW binaries for:
  - macOS ARM64
  - Linux x86_64 and ARM64
  - Windows x86_64 and ARM64

Environment:
  DIST_DIR                    Output directory. Defaults to ./dist.
  PHLOX_GW_SKIP_FRONTEND_BUILD=1  Same as --skip-frontend.
  GOFLAGS                     Extra flags passed through to go build by Go.
  GOCACHE                     Optional Go build cache location.
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-frontend)
      SKIP_FRONTEND=1
      shift
      ;;
    --clean)
      CLEAN=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "${PHLOX_GW_SKIP_FRONTEND_BUILD:-}" == "1" ]]; then
  SKIP_FRONTEND=1
fi

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Missing required command: $1" >&2
    exit 1
  fi
}

checksum_one() {
  local file hash
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    hash="$(sha256sum "$file" | awk '{print $1}')"
  else
    hash="$(shasum -a 256 "$file" | awk '{print $1}')"
  fi
  printf '%s  %s\n' "$hash" "$(basename "$file")"
}

require_cmd go
if [[ "$SKIP_FRONTEND" -eq 0 ]]; then
  require_cmd npm
fi

if [[ "$CLEAN" -eq 1 ]]; then
  rm -rf "$DIST_DIR"
fi
mkdir -p "$DIST_DIR"

if [[ "$SKIP_FRONTEND" -eq 0 ]]; then
  echo "==> Building frontend"
  (cd "$ROOT_DIR/frontend" && npm run build)
else
  echo "==> Skipping frontend build"
fi

declare -a TARGETS=(
  "darwin arm64 phlox-gw-darwin-arm64"
  "linux amd64 phlox-gw-linux-amd64"
  "linux arm64 phlox-gw-linux-arm64"
  "windows amd64 phlox-gw-windows-amd64.exe"
  "windows arm64 phlox-gw-windows-arm64.exe"
)

CHECKSUMS="$DIST_DIR/checksums.txt"
: > "$CHECKSUMS"

for target in "${TARGETS[@]}"; do
  read -r GOOS_VALUE GOARCH_VALUE OUTPUT_NAME <<< "$target"
  OUTPUT_PATH="$DIST_DIR/$OUTPUT_NAME"
  echo "==> Building $OUTPUT_NAME"
  CGO_ENABLED=0 GOOS="$GOOS_VALUE" GOARCH="$GOARCH_VALUE" \
    go build -trimpath -ldflags="-s -w" -o "$OUTPUT_PATH" ./cmd/phlox-gw
  checksum_one "$OUTPUT_PATH" >> "$CHECKSUMS"
done

echo "==> Wrote release binaries to $DIST_DIR"
echo "==> Checksums: $CHECKSUMS"
