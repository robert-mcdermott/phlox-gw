#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="$ROOT_DIR/install.sh"
INSTALLER_SHELL="${INSTALLER_SHELL:-sh}"
TEST_ROOT="$(mktemp -d)"
FAKE_BIN="$TEST_ROOT/fake-bin"
FIXTURES="$TEST_ROOT/fixtures"

cleanup() {
  rm -rf "$TEST_ROOT"
}

trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_contains() {
  local file="$1"
  local expected="$2"
  grep -Fq "$expected" "$file" || fail "$file does not contain: $expected"
}

mkdir -p "$FAKE_BIN" "$FIXTURES"

cat > "$FIXTURES/phlox-gw" <<'BINARY'
#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  echo "phlox-gw v9.9.9 (commit installer-test, built 2026-07-11T00:00:00Z)"
  exit 0
fi
exit 2
BINARY
chmod +x "$FIXTURES/phlox-gw"

if command -v sha256sum >/dev/null 2>&1; then
  FIXTURE_SHA="$(sha256sum "$FIXTURES/phlox-gw" | awk '{print $1}')"
else
  FIXTURE_SHA="$(shasum -a 256 "$FIXTURES/phlox-gw" | awk '{print $1}')"
fi

for asset in \
  phlox-gw-darwin-arm64 \
  phlox-gw-linux-amd64 \
  phlox-gw-linux-arm64; do
  printf '%s  %s\n' "$FIXTURE_SHA" "$asset" >> "$FIXTURES/checksums.txt"
done

cat > "$FAKE_BIN/uname" <<'UNAME'
#!/bin/sh
case "${1:-}" in
  -s) printf '%s\n' "$PHLOX_TEST_OS" ;;
  -m) printf '%s\n' "$PHLOX_TEST_ARCH" ;;
  *) exit 2 ;;
esac
UNAME
chmod +x "$FAKE_BIN/uname"

cat > "$FAKE_BIN/curl" <<'CURL'
#!/bin/sh
set -eu

output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --output)
      output="$2"
      shift 2
      ;;
    --proto|--proto-redir|--connect-timeout|--retry)
      shift 2
      ;;
    --tlsv1.2|--fail|--silent|--show-error|--location)
      shift
      ;;
    --*)
      echo "unexpected curl option: $1" >&2
      exit 2
      ;;
    *)
      url="$1"
      shift
      ;;
  esac
done

[ -n "$output" ]
[ -n "$url" ]
printf '%s\n' "$url" >> "$PHLOX_TEST_CURL_LOG"

case "$url" in
  */checksums.txt)
    cp "${PHLOX_TEST_CHECKSUM_FILE:-$PHLOX_TEST_FIXTURES/checksums.txt}" "$output"
    ;;
  */phlox-gw-darwin-arm64|*/phlox-gw-linux-amd64|*/phlox-gw-linux-arm64)
    cp "$PHLOX_TEST_FIXTURES/phlox-gw" "$output"
    ;;
  *)
    echo "unexpected download URL: $url" >&2
    exit 22
    ;;
esac
CURL
chmod +x "$FAKE_BIN/curl"

run_installer() {
  local os_name="$1"
  local arch_name="$2"
  local home_dir="$3"
  local log_file="$4"
  shift 4

  env \
    PATH="$FAKE_BIN:$PATH" \
    HOME="$home_dir" \
    PHLOX_TEST_OS="$os_name" \
    PHLOX_TEST_ARCH="$arch_name" \
    PHLOX_TEST_FIXTURES="$FIXTURES" \
    PHLOX_TEST_CURL_LOG="$log_file" \
    "$INSTALLER_SHELL" "$INSTALLER" "$@"
}

LINUX_HOME="$TEST_ROOT/linux-home"
LINUX_LOG="$TEST_ROOT/linux-curl.log"
mkdir -p "$LINUX_HOME"
run_installer Linux x86_64 "$LINUX_HOME" "$LINUX_LOG" > "$TEST_ROOT/linux.out"

LINUX_DEST="$LINUX_HOME/.local/bin/phlox-gw"
[[ -x "$LINUX_DEST" ]] || fail "Linux AMD64 binary was not installed"
[[ "$($LINUX_DEST --version)" == "phlox-gw v9.9.9"* ]] ||
  fail "installed Linux AMD64 binary failed its version check"
assert_contains "$LINUX_LOG" "/releases/latest/download/phlox-gw-linux-amd64"
assert_contains "$LINUX_LOG" "/releases/latest/download/checksums.txt"
assert_contains "$TEST_ROOT/linux.out" "Checksum verified"

MAC_HOME="$TEST_ROOT/mac-home"
MAC_INSTALL="$TEST_ROOT/mac bin"
MAC_LOG="$TEST_ROOT/mac-curl.log"
mkdir -p "$MAC_HOME"
run_installer Darwin arm64 "$MAC_HOME" "$MAC_LOG" \
  --version 1.2.3 --install-dir "$MAC_INSTALL" > "$TEST_ROOT/mac.out"

[[ -x "$MAC_INSTALL/phlox-gw" ]] || fail "macOS ARM64 binary was not installed"
assert_contains "$MAC_LOG" "/releases/download/v1.2.3/phlox-gw-darwin-arm64"
assert_contains "$TEST_ROOT/mac.out" "Installed at $MAC_INSTALL/phlox-gw"

ARM_HOME="$TEST_ROOT/arm-home"
ARM_LOG="$TEST_ROOT/arm-curl.log"
mkdir -p "$ARM_HOME"
run_installer Linux aarch64 "$ARM_HOME" "$ARM_LOG" > "$TEST_ROOT/arm.out"
assert_contains "$ARM_LOG" "/releases/latest/download/phlox-gw-linux-arm64"

INTEL_HOME="$TEST_ROOT/intel-home"
INTEL_LOG="$TEST_ROOT/intel-curl.log"
mkdir -p "$INTEL_HOME"
if run_installer Darwin x86_64 "$INTEL_HOME" "$INTEL_LOG" \
  > "$TEST_ROOT/intel.out" 2> "$TEST_ROOT/intel.err"; then
  fail "macOS Intel should be rejected"
fi
assert_contains "$TEST_ROOT/intel.err" "macOS Intel is not currently a supported release target"
[[ ! -s "$INTEL_LOG" ]] || fail "unsupported platform attempted a download"

BAD_CHECKSUMS="$TEST_ROOT/bad-checksums.txt"
printf '%064d  phlox-gw-linux-amd64\n' 0 > "$BAD_CHECKSUMS"
BAD_HOME="$TEST_ROOT/bad-home"
BAD_INSTALL="$TEST_ROOT/bad-bin"
BAD_LOG="$TEST_ROOT/bad-curl.log"
mkdir -p "$BAD_HOME"
if env \
  PATH="$FAKE_BIN:$PATH" \
  HOME="$BAD_HOME" \
  PHLOX_TEST_OS=Linux \
  PHLOX_TEST_ARCH=x86_64 \
  PHLOX_TEST_FIXTURES="$FIXTURES" \
  PHLOX_TEST_CHECKSUM_FILE="$BAD_CHECKSUMS" \
  PHLOX_TEST_CURL_LOG="$BAD_LOG" \
  "$INSTALLER_SHELL" "$INSTALLER" --install-dir "$BAD_INSTALL" \
  > "$TEST_ROOT/bad.out" 2> "$TEST_ROOT/bad.err"; then
  fail "checksum mismatch should fail installation"
fi
assert_contains "$TEST_ROOT/bad.err" "Checksum verification failed"
[[ ! -e "$BAD_INSTALL/phlox-gw" ]] || fail "checksum failure installed a binary"

echo "Installer tests passed"
