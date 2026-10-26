#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# rmcp floor-version gate for the generated Rust MCP dispatch seam.
#
# tacplus pins ONE rmcp version for every Rust consumer that owns an MCP tool
# (rmcpCrateVersion, pkg/internal/rust/types/mcp_dispatch_gen.go). The generated
# ServerHandler::get_info is written against a specific rmcp API — today
# rmcp::model::ServerConfig and InitializeResult::default(), neither of which
# exists before 3.4.1 — so if that constant ever states a floor LOWER than what
# the emitted code needs, a consumer whose lock resolves to the stated floor
# fails to compile GENERATED code. That shipped once (63f07a27: the constant
# said 3.1.4 while the emitted get_info needed 3.4.1).
#
# Nothing caught it because the Go unit tests over this seam
# (pkg/internal/rust/types/mcp_dispatch_test.go) assert on generated SOURCE TEXT
# and never run cargo. This gate compiles the real generated crate against the
# EXACT version the constant declares — pinned with `cargo update --precise`,
# not left to `^3.x` resolution, because resolving to the newest 3.x is exactly
# what hides a too-low floor.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=../lib/cargo-target.sh
source "$ROOT_DIR/cross-test/lib/cargo-target.sh"
PROTO_SRC="$ROOT_DIR/cross-test/mcp-floor-version/proto"
MCP_GEN_GO="$ROOT_DIR/pkg/internal/rust/types/mcp_dispatch_gen.go"
CRATE="mcpfloor-model"

die() { echo "mcp-floor-version check FAILED: $*" >&2; exit 1; }

# The floor is read from the generator's own constant rather than restated here,
# so bumping rmcpCrateVersion re-points this gate at the new floor by itself.
FLOOR="$(sed -n 's/^[[:space:]]*rmcpCrateVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' "$MCP_GEN_GO")"
[[ -n "$FLOOR" ]] || die "could not read rmcpCrateVersion from $MCP_GEN_GO"
echo "==> tacplus declares rmcp floor $FLOOR"

TACPLUS_BIN="${TACPLUS_BIN:-$ROOT_DIR/target/tacplus-mcp-floor-check}"
if [[ ! -x "$TACPLUS_BIN" || -z "${TACPLUS_BIN_PREBUILT:-}" ]]; then
  echo "==> Building tacplus binary"
  (cd "$ROOT_DIR" && go build -o "$TACPLUS_BIN" .)
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cat > "$WORK/tacplus.yaml" <<EOF2
language: "rust"
pkg: "com.example.mcpfloor"
src: "$PROTO_SRC"
output-dir: "tacplus"
EOF2

echo "==> Generating MCP fixture (rust)"
(cd "$WORK" && "$TACPLUS_BIN" build --config tacplus.yaml --src "$PROTO_SRC") \
  || die "generation failed"

OUT="$WORK/tacplus/model"
[[ -d "$OUT" ]] || die "no generated output at $OUT"

# Assert the build actually carries the MCP seam. Without this the cargo build
# below would still pass if MCP emission silently stopped happening, and the
# gate would go green while covering nothing.
MCP_SRC="$OUT/s2sm/internal/grpc/servers/svc/mcpfloor/mcp_floor_api.rs"
echo "==> Asserting generated Rust MCP dispatch seam"
[[ -f "$MCP_SRC" ]] || die "no generated MCP service wrapper at $MCP_SRC"
grep -q "impl rmcp::ServerHandler" "$MCP_SRC" || die "generated wrapper has no rmcp ServerHandler"
grep -q "fn get_info" "$MCP_SRC"               || die "generated wrapper has no get_info"

# The manifest must ask for the same version the constant states; otherwise the
# pin below would be testing a version the consumer never gets.
grep -q "^rmcp = { version = \"$FLOOR\"" "$OUT/Cargo.toml" \
  || die "generated Cargo.toml does not declare rmcp $FLOOR: $(grep '^rmcp' "$OUT/Cargo.toml" || echo 'no rmcp dependency at all')"

echo "==> Pinning rmcp to the declared floor $FLOOR"
(cd "$OUT" && cargo generate-lockfile) || die "cargo generate-lockfile failed"
(cd "$OUT" && cargo update -p rmcp --precise "$FLOOR") \
  || die "cannot pin rmcp to declared floor $FLOOR"
grep -q "^version = \"$FLOOR\"$" <(grep -A1 '^name = "rmcp"$' "$OUT/Cargo.lock") \
  || die "lock did not pin rmcp to $FLOOR"

echo "==> Building generated Rust MCP crate against rmcp $FLOOR"
(cd "$OUT" && CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET" cargo build -p "$CRATE") \
  || die "generated MCP crate does not compile against its OWN declared rmcp floor $FLOOR"

echo "==> mcp-floor-version check PASSED (rust, rmcp $FLOOR)"
