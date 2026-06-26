#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEMO_DIR="${PHLOX_GW_CLUSTER_DEMO_DIR:-"$ROOT_DIR/.phlox-gw-cluster-demo"}"
NODES="${PHLOX_GW_CLUSTER_NODES:-3}"
BASE_PORT="${PHLOX_GW_CLUSTER_BASE_PORT:-8081}"

if [[ -z "${PHLOX_GW_DATABASE_URL:-}" ]]; then
  echo "PHLOX_GW_DATABASE_URL is required for the demo cluster." >&2
  echo "Example: export PHLOX_GW_DATABASE_URL='postgres://phlox_gw:phlox-gw-dev-password@127.0.0.1:5432/phlox_gw?sslmode=disable'" >&2
  exit 1
fi

mkdir -p "$DEMO_DIR"

generate_secret() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -base64 48
  else
    LC_ALL=C tr -dc 'A-Za-z0-9_-' < /dev/urandom | head -c 64
    printf '\n'
  fi
}

SESSION_SECRET_FILE="$DEMO_DIR/session-secret"
if [[ ! -f "$SESSION_SECRET_FILE" ]]; then
  umask 077
  generate_secret > "$SESSION_SECRET_FILE"
fi
SESSION_SECRET="$(< "$SESSION_SECRET_FILE")"
SIGNING_KEY_FILE="${PHLOX_GW_CONFIG_SIGNING_KEY_FILE:-"$DEMO_DIR/phlox-gw-signing-key.json"}"

detect_binary() {
  if [[ -n "${PHLOX_GW_BINARY:-}" ]]; then
    printf '%s\n' "$PHLOX_GW_BINARY"
    return
  fi
  if [[ -x "$ROOT_DIR/phlox-gw" ]]; then
    printf '%s\n' "$ROOT_DIR/phlox-gw"
    return
  fi
  mkdir -p "$DEMO_DIR"
  echo "Building demo binary..." >&2
  (cd "$ROOT_DIR" && go build -o "$DEMO_DIR/phlox-gw" ./cmd/phlox-gw)
  printf '%s\n' "$DEMO_DIR/phlox-gw"
}

BINARY="$(detect_binary)"
if [[ ! -x "$BINARY" ]]; then
  echo "Phlox-GW binary is not executable: $BINARY" >&2
  exit 1
fi

PIDS=()
cleanup() {
  for pid in "${PIDS[@]:-}"; do
    kill "$pid" >/dev/null 2>&1 || true
  done
  wait >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "Starting Phlox-GW demo cluster"
echo "  nodes:       $NODES"
echo "  base port:   $BASE_PORT"
echo "  data:        $DEMO_DIR"
echo "  binary:      $BINARY"
echo "  first node:  http://127.0.0.1:$BASE_PORT/"
echo

for i in $(seq 1 "$NODES"); do
  port=$((BASE_PORT + i - 1))
  node_dir="$DEMO_DIR/node-$i"
  log_file="$DEMO_DIR/node-$i.log"
  mkdir -p "$node_dir"
  env \
    PHLOX_GW_ADDR="127.0.0.1:$port" \
    PHLOX_GW_DEPLOYMENT_MODE="cluster-postgres" \
    PHLOX_GW_INSTANCE_ID="demo-node-$i" \
    PHLOX_GW_DATA_DIR="$node_dir" \
    PHLOX_GW_SESSION_SECRET="$SESSION_SECRET" \
    PHLOX_GW_CONFIG_SIGNING_KEY_FILE="$SIGNING_KEY_FILE" \
    "$BINARY" > "$log_file" 2>&1 &
  pid=$!
  PIDS+=("$pid")
  echo "  demo-node-$i pid=$pid url=http://127.0.0.1:$port/ log=$log_file"
done

echo
echo "Press Ctrl+C to stop all demo nodes."
wait
