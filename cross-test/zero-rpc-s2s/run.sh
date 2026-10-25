#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# Zero-rpc (client-only) Rust host gate (#94, addendum to #64).
#
# Generates the Rust model of cross-test/proto-zerorpc (a service with a state
# and an s2s client edge, but no rpc) and runs its REAL generated ServiceManager
# against a fake kernel and a fake llmproxy peer, over TLS with SPIFFE certs.
# The host calls new() and run() and makes one llmproxy call through the
# generated s2s client; it must not publish instance health, ask for a serving
# cert or bind a listener. See fixture/main.rs.
#
# Standalone, like outbound-deadline: it needs the go and cargo toolchains.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATE_DIR="$ROOT_DIR/cross-test/zero-rpc-s2s"
PROTO_SRC="$ROOT_DIR/cross-test/proto-zerorpc"
WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/zero-rpc-s2s.XXXXXX")"

cleanup() {
  rm -rf "$WORK_ROOT"
}
trap cleanup EXIT

die() {
  echo "ERROR: $*" >&2
  exit 1
}

command -v cargo >/dev/null || die "cargo is required"
[[ -d "$PROTO_SRC" ]] || die "proto fixture not found: $PROTO_SRC"

TACPLUS_BIN="${TACPLUS_BIN:-$WORK_ROOT/tacplus-local}"
if [[ ! -x "$TACPLUS_BIN" ]]; then
  echo "==> Building local tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

MODEL_DIR="$WORK_ROOT/rust"
mkdir -p "$MODEL_DIR"
cat > "$MODEL_DIR/tacplus.yaml" <<EOF
language: "rust"
pkg: "com.gitlab.example.dummy.zerorpc"
src: "$PROTO_SRC"
output-dir: "model"
EOF
echo "==> Generating rust model"
(cd "$MODEL_DIR" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$MODEL_DIR/generate.log" 2>&1 || {
  cat "$MODEL_DIR/generate.log" >&2
  die "rust generation failed"
}

CRATE_DIR="$MODEL_DIR/zero-rpc-s2s"
mkdir -p "$CRATE_DIR/src"
cp "$GATE_DIR/fixture/main.rs" "$CRATE_DIR/src/main.rs"
cat > "$CRATE_DIR/Cargo.toml" <<EOF
[package]
name = "zero-rpc-s2s"
version = "0.1.0"
edition = "2021"

[workspace]

[dependencies]
zerorpc-model = { path = "$MODEL_DIR/model" }
base64 = "0.21.7"
http = "1"
rcgen = "0.14"
serde_json = "1"
tokio = { version = "1.49.0", features = ["full"] }
tokio-stream = { version = "0.1.17", features = ["net"] }
tonic = { version = "0.14.0", features = ["tls-ring"] }
tonic-health = "0.14.0"
tower = { version = "0.5.3", features = ["util"] }
EOF

echo "==> Building rust zero-rpc-s2s fixture"
(cd "$CRATE_DIR" && CARGO_TARGET_DIR="$CRATE_DIR/target" cargo build --quiet) >"$MODEL_DIR/build.log" 2>&1 || {
  cat "$MODEL_DIR/build.log" >&2
  die "rust zero-rpc-s2s build failed"
}
if [[ -s "$MODEL_DIR/build.log" ]]; then
  cat "$MODEL_DIR/build.log" >&2
  die "warning/error output found in the rust build"
fi

echo "==> Running the client-only host against the fake kernel and llmproxy"
"$CRATE_DIR/target/debug/zero-rpc-s2s"
