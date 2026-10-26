#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Keepalive regression gate for the generated RUST server (issue #29).
#
# Go and Java servers enforce keepaliveconst.PermitMinTimeSecs; the Rust server
# has no permit policy. This gate proves the consequence the platform depends
# on: a generated Rust client pinging every keepaliveconst.TimeSecs while idle
# keeps ONE connection for the whole idle hold against a generated Rust server
# (no GOAWAY, no redial).
#
#   - the generated Rust ContractApi server, hosted by the server-runtime rust
#     fixture (the real generated ValidationWrapper on a TCP port)
#   - the generated Rust C2S client, dialled through a counting proxy
#
# Control run (must FAIL, proves the assertion can fail):
#   TACPLUS_KEEPALIVE_CUT_AFTER_SECS=20 TACPLUS_KEEPALIVE_HOLD_SECS=45 \
#     cross-test/keepalive-rust/run.sh
#
# Separate from `just test`: the default hold is 600 s and it needs cargo.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
GATE_DIR="$ROOT_DIR/cross-test/keepalive-rust"
FIXTURE_SRC="$ROOT_DIR/cross-test/server-runtime/fixture/rust/main.rs"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
WORK_ROOT="${TACPLUS_KEEPALIVE_RUST_WORKDIR:-}"
TACPLUS_BIN="${TACPLUS_BIN:-}"
KEEP_WORK="${TACPLUS_KEEPALIVE_RUST_KEEP:-false}"
HOLD_SECS="${TACPLUS_KEEPALIVE_HOLD_SECS:-600}"
FIXTURE_PID=""

die() {
  echo "ERROR: $*" >&2
  exit 1
}

if [[ -z "$WORK_ROOT" ]]; then
  WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/tacplus-keepalive-rust.XXXXXX")"
else
  mkdir -p "$WORK_ROOT"
  WORK_ROOT="$(cd "$WORK_ROOT" && pwd)"
fi

stop_fixture() {
  if [[ -n "$FIXTURE_PID" ]] && kill -0 "$FIXTURE_PID" 2>/dev/null; then
    kill -TERM "$FIXTURE_PID" 2>/dev/null || true
    wait "$FIXTURE_PID" 2>/dev/null || true
  fi
  FIXTURE_PID=""
}

cleanup() {
  stop_fixture
  if [[ "$KEEP_WORK" != true ]]; then
    rm -rf "$WORK_ROOT"
  fi
}
trap cleanup EXIT

if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$WORK_ROOT/tacplus-local"
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

if [[ "$TACPLUS_BIN" == */* ]]; then
  TACPLUS_BIN="$(cd "$(dirname "$TACPLUS_BIN")" && pwd)/$(basename "$TACPLUS_BIN")"
fi

assert_clean_log() {
  local log="$1"
  if [[ -s "$log" ]] && rg -n '(WARN|WARNING|warning:|ERROR|panic:|fatal:)' "$log"; then
    die "warning/error output found in $log"
  fi
}

MODEL_DIR="$WORK_ROOT/rust"
echo "==> Generating rust model"
mkdir -p "$MODEL_DIR"
cat > "$MODEL_DIR/tacplus.yaml" <<EOF
language: "rust"
pkg: "com.gitlab.example.tacplus.consumer.contractclient"
src: "$PROTO_SRC"
output-dir: "model"
EOF
(cd "$MODEL_DIR" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$MODEL_DIR/generate.log" 2>&1 || {
  cat "$MODEL_DIR/generate.log" >&2
  die "rust generation failed"
}
assert_clean_log "$MODEL_DIR/generate.log"

# Builds one rust crate (fixture server or driving client) against the model.
build_crate() {
  local kind="$1"
  local src="$2"
  local crate="keepalive-rust-$kind"
  local dir="$MODEL_DIR/$kind"
  mkdir -p "$dir/src"
  cp "$src" "$dir/src/main.rs"
  cat > "$dir/Cargo.toml" <<EOF
[package]
name = "$crate"
version = "0.1.0"
edition = "2021"

[dependencies]
collection = { path = "$MODEL_DIR/model/chassis/collection" }
contractclient-model = { path = "$MODEL_DIR/model" }
futures = "0.3.31"
grpcserver = { path = "$MODEL_DIR/model/chassis/grpcserver" }
servicemanager = { path = "$MODEL_DIR/model/chassis/servicemanager" }
tokio = { version = "1.49.0", features = ["io-util", "macros", "net", "rt-multi-thread", "signal", "sync", "time"] }
tokio-stream = { version = "0.1.17", features = ["net"] }
tonic = "0.14.0"
EOF
  echo "==> Building rust $kind"
  (cd "$dir" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build --quiet) >"$MODEL_DIR/$kind-build.log" 2>&1 || {
    cat "$MODEL_DIR/$kind-build.log" >&2
    die "rust $kind build failed"
  }
  assert_clean_log "$MODEL_DIR/$kind-build.log"
}

build_crate fixture "$FIXTURE_SRC"
build_crate client "$GATE_DIR/client/main.rs"

FIXTURE_BIN="$CROSS_TEST_CARGO_TARGET/debug/keepalive-rust-fixture"
CLIENT_BIN="$CROSS_TEST_CARGO_TARGET/debug/keepalive-rust-client"

# Starts the fixture server and waits for its {"grpc":"host:port"} line.
FIXTURE_STDOUT="$MODEL_DIR/fixture-stdout.log"
FIXTURE_STDERR="$MODEL_DIR/fixture-stderr.log"
"$FIXTURE_BIN" >"$FIXTURE_STDOUT" 2>"$FIXTURE_STDERR" &
FIXTURE_PID=$!
waited=0
while [[ ! -s "$FIXTURE_STDOUT" ]]; do
  kill -0 "$FIXTURE_PID" 2>/dev/null || {
    cat "$FIXTURE_STDERR" >&2
    die "fixture server exited before publishing its port"
  }
  sleep 0.1
  waited=$((waited + 1))
  [[ $waited -lt 300 ]] || die "fixture server did not publish its port within 30s"
done
SERVER_ADDR="$(sed -n 's/.*"grpc":"\([^"]*\)".*/\1/p' "$FIXTURE_STDOUT" | head -n 1)"
[[ -n "$SERVER_ADDR" ]] || {
  cat "$FIXTURE_STDOUT" >&2
  die "could not parse the fixture server port line"
}

CLIENT_LOG="$MODEL_DIR/client.log"
echo "==> Holding one generated rust client connection idle for ${HOLD_SECS}s ($SERVER_ADDR)"
# The control run sets TACPLUS_KEEPALIVE_CUT_AFTER_SECS, so the client exits
# non-zero by design; the die below is then the expected outcome.
TACPLUS_SERVER_ADDR="$SERVER_ADDR" \
  TACPLUS_KEEPALIVE_HOLD_SECS="$HOLD_SECS" \
  timeout "$((HOLD_SECS + 120))" "$CLIENT_BIN" >"$CLIENT_LOG" 2>&1 || {
  cat "$CLIENT_LOG" >&2
  cat "$FIXTURE_STDERR" >&2 || true
  die "generated rust client did not hold one connection against the rust server"
}
cat "$CLIENT_LOG"
assert_clean_log "$CLIENT_LOG"

stop_fixture
if [[ -s "$FIXTURE_STDERR" ]]; then
  cat "$FIXTURE_STDERR" >&2
  die "fixture server wrote to stderr"
fi

echo "keepalive-rust OK"
