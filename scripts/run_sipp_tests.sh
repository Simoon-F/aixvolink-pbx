#!/bin/sh
set -eu

SIPP=${SIPP:-sipp}
PORT=${AIXVOLINKPBX_SIPP_PORT:-15060}
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
SERVER_BIN=${TMPDIR:-/tmp}/aixvolinkpbx-sipp-server
SERVER_LOG=${TMPDIR:-/tmp}/aixvolinkpbx-sipp-server.log
TARGET=127.0.0.1:$PORT

command -v "$SIPP" >/dev/null 2>&1 || {
  echo "sipp is required" >&2
  exit 1
}

cleanup() {
  if [ -n "${SERVER_PID:-}" ]; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
  rm -f "$SERVER_BIN"
}
trap cleanup EXIT INT TERM

go build -trimpath -o "$SERVER_BIN" ./test/sipp/server
"$SERVER_BIN" -port "$PORT" >"$SERVER_LOG" 2>&1 &
SERVER_PID=$!

ready=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if nc -z -w 1 127.0.0.1 "$PORT" 2>/dev/null; then
    ready=1
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  cat "$SERVER_LOG" >&2
  exit 1
fi

register_user() {
  local_port=$1
  username=$2
  scenario=register.xml
  if [ "$username" = 1002 ]; then
    scenario=register-1002.xml
  fi
  "$SIPP" "$TARGET" -sf "$ROOT/test/sipp/$scenario" \
    -i 127.0.0.1 -p "$local_port" -m 1 -nd -timeout 10s >/dev/null
}

run_pair() {
  caller_scenario=$1
  callee_scenario=$2
  caller_port=$3
  callee_port=$4

  register_user "$callee_port" 1002
  "$SIPP" "$TARGET" -sf "$ROOT/test/sipp/$callee_scenario" -i 127.0.0.1 -p "$callee_port" -m 1 -nd -timeout 10s >/dev/null &
  callee_pid=$!
  sleep 1
  register_user "$caller_port" 1001
  "$SIPP" "$TARGET" -sf "$ROOT/test/sipp/$caller_scenario" -i 127.0.0.1 -p "$caller_port" -m 1 -nd -timeout 10s >/dev/null
  wait "$callee_pid"
}

run_pair caller-answer.xml callee-answer.xml 16101 16102
run_pair caller-reject.xml callee-reject.xml 16201 16202
run_pair caller-cancel.xml callee-cancel.xml 16301 16302

echo "SIPp Phase 1 protocol scenarios passed"
