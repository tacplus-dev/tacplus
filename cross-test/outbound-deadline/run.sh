#!/usr/bin/env bash
# Copyright (c) 2026 Seabeam LLC
# SPDX-License-Identifier: Apache-2.0
#
# Outbound-deadline gate for RUST (issue #38).
#
# Generates the rust model with the local tacplus build, then runs a three-hop
# chain of the REAL generated ContractApi servers and clients (root -> mid ->
# leaf, one process on loopback). The fixture checks the wire budget leaf
# receives and how long leaf's handler lives relative to the root's deadline.
# See fixture/rust/main.rs for the two facts and what each one proves.
#
# Standalone, like server-runtime: it needs the go and cargo toolchains, so it
# is NOT part of `just test`.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GATE_DIR="$ROOT_DIR/cross-test/outbound-deadline"
PROTO_SRC="$ROOT_DIR/cross-test/consumer-contract/proto"
WORK_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/outbound-deadline.XXXXXX")"

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

TACPLUS_BIN="$WORK_ROOT/tacplus-local"
echo "==> Building local tacplus binary"
(cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)

MODEL_DIR="$WORK_ROOT/rust"
mkdir -p "$MODEL_DIR"
cat > "$MODEL_DIR/tacplus.yaml" <<EOF
language: "rust"
pkg: "com.gitlab.example.tacplus.consumer.contractclient"
src: "$PROTO_SRC"
output-dir: "model"
EOF
echo "==> Generating rust model"
(cd "$MODEL_DIR" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC" --tag latest) >"$MODEL_DIR/generate.log" 2>&1 || {
  cat "$MODEL_DIR/generate.log" >&2
  die "rust generation failed"
}

CRATE_DIR="$MODEL_DIR/outbound-deadline"
mkdir -p "$CRATE_DIR/src"
cp "$GATE_DIR/fixture/rust/main.rs" "$CRATE_DIR/src/main.rs"
cat > "$CRATE_DIR/Cargo.toml" <<EOF
[package]
name = "outbound-deadline-rust"
version = "0.1.0"
edition = "2021"

[dependencies]
collection = { path = "$MODEL_DIR/model/chassis/collection" }
contractclient-model = { path = "$MODEL_DIR/model" }
servicemanager = { path = "$MODEL_DIR/model/chassis/servicemanager" }
tokio = { version = "1.49.0", features = ["rt-multi-thread", "macros", "net", "sync", "time"] }
tokio-stream = { version = "0.1.17", features = ["net"] }
tonic = "0.14.0"
EOF

echo "==> Building rust outbound-deadline fixture"
(cd "$CRATE_DIR" && CARGO_TARGET_DIR="$CRATE_DIR/target" cargo build --quiet) >"$MODEL_DIR/build.log" 2>&1 || {
  cat "$MODEL_DIR/build.log" >&2
  die "rust outbound-deadline build failed"
}
if [[ -s "$MODEL_DIR/build.log" ]]; then
  cat "$MODEL_DIR/build.log" >&2
  die "warning/error output found in the rust build"
fi

echo "==> Running three-hop chain"
"$CRATE_DIR/target/debug/outbound-deadline-rust"
