#!/usr/bin/env bash
# Copyright (c) 2026 Second Star Cavalier LLC
#
# Behavioral W2S request-validation fixture for the generated Rust REST
# dispatch module.
#
# The generated `rest_dispatch::dispatch` / `dispatch_stream` are the only
# generated W2S SERVER surface Rust emits, and nothing in-repo hosted them, so
# the ingress (request) validation leg was unpinned while the egress (response)
# leg was already covered. This gate generates a real Rust service whose C2S
# backend has an `assert_email` request constraint, hosts the REAL generated
# dispatch functions against a REAL tonic backend, and drives both an invalid
# and a valid request through each of the unary and SSE legs.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PROTO_SRC="$ROOT_DIR/cross-test/w2s-validate/proto"

die() { echo "w2s-validate check FAILED: $*" >&2; exit 1; }

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-w2s-validate-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF2
language: "rust"
pkg: "com.example.w2svalidate"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF2

echo "==> Generating W2S request-validation fixture (rust)"
(cd "$WORK" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

DISPATCH="$OUT/s2sm/rest_dispatch/mod.rs"
echo "==> Asserting generated Rust REST dispatch seams"
[[ -f "$DISPATCH" ]] || die "no generated rest_dispatch module at $DISPATCH"
grep -q "pub async fn dispatch(" "$DISPATCH"        || die "unary dispatch missing"
grep -q "pub async fn dispatch_stream(" "$DISPATCH" || die "streaming dispatch missing"

cp "$ROOT_DIR/cross-test/w2s-validate/testdata/w2s_request_validation_test.rs" \
  "$OUT/s2sm/w2s_request_validation_test.rs"
printf '\n#[cfg(test)]\nmod w2s_request_validation_test;\n' >> "$OUT/s2sm/mod.rs"

echo "==> Running generated Rust W2S request-validation tests"
(cd "$OUT" && CARGO_TARGET_DIR="$OUT/target" cargo test -p w2svalidate-model rust_w2s_) \
  || die "generated Rust W2S request-validation tests failed"

echo "==> w2s-validate check PASSED (rust)"
