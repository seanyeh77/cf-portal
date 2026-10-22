#!/usr/bin/env bash
# Runs the Cloudflare mock and the portal against it, for local use and
# the Playwright tests. Data lives in a fresh temporary directory.
#   PORT (default 18080), MOCK_PORT (default 18081), ADMIN_PASSWORD (default "test-password-123")
set -euo pipefail
cd "$(dirname "$0")/.."
PORT=${PORT:-18080}
MOCK_PORT=${MOCK_PORT:-18081}
PW=${ADMIN_PASSWORD:-test-password-123}
DATA=$(mktemp -d)
BIN=$DATA/bin
mkdir -p "$BIN"
go build -o "$BIN/portal" ./cmd/portal
go build -o "$BIN/mockcf" ./testdata/mockcf/cmd/mockcf
"$BIN/mockcf" -addr 127.0.0.1:$MOCK_PORT -token test-token &
MOCK=$!
APP=
cleanup() { kill $MOCK $APP 2>/dev/null || true; rm -rf "$DATA"; }
trap cleanup EXIT
trap 'exit 0' INT TERM
HASH=$(printf '%s\n%s\n' "$PW" "$PW" | "$BIN/portal" hashpw 2>/dev/null)
export CF_API_TOKEN=test-token \
  CF_API_BASE=http://127.0.0.1:$MOCK_PORT/client/v4 \
  ADMIN_PASSWORD_HASH="$HASH" \
  SECRET_KEY=dev-secret-key-dev-secret-key-0123456789 \
  COOKIE_SECURE=false \
  DB_PATH=$DATA/portal.db \
  LISTEN_ADDR=127.0.0.1:$PORT \
  SIMPLE_ICONS_BASE=http://127.0.0.1:$MOCK_PORT/si \
  CHECK_INTERVAL=${CHECK_INTERVAL:-15} \
  SCAN_TIMEOUT=${SCAN_TIMEOUT:-0.3} \
  RECORD_SCAN_INTERVAL=${RECORD_SCAN_INTERVAL:-21600} \
  TIMEZONE=Asia/Taipei
"$BIN/portal" serve &
APP=$!
wait $APP
