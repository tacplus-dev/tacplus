#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# One Rust build directory for every cross-test leg in a gate run. Each
# generated crate pulls the same dependency set (tonic, prost, tokio, rustls,
# ...); with a target directory per leg that set compiled once per leg and
# one run grew target/cross-test by tens of GB. Source this file after ROOT_DIR
# is set and build with CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET".
#
# Set TACPLUS_CROSS_TEST_CARGO_TARGET to move it. A leg that must not share
# (a clean-build test, or a feature set that would thrash the cache) keeps its
# own directory and says why with a "separate-cargo-target:" comment on the
# line that sets it.

CROSS_TEST_CARGO_TARGET="${TACPLUS_CROSS_TEST_CARGO_TARGET:-$ROOT_DIR/target/cross-test/cargo}"
mkdir -p "$CROSS_TEST_CARGO_TARGET"
CROSS_TEST_CARGO_TARGET="$(cd "$CROSS_TEST_CARGO_TARGET" && pwd)"
# Cargo tags a target directory it creates itself, and `cargo clean` refuses
# one without the tag; the mkdir above means cargo never creates this one.
if [[ ! -f "$CROSS_TEST_CARGO_TARGET/CACHEDIR.TAG" ]]; then
  printf 'Signature: 8a477f597d28d172789f06886806bc55\n' > "$CROSS_TEST_CARGO_TARGET/CACHEDIR.TAG"
fi
# Exported so a bare `cargo test` in a leg lands here too.
export CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET"

# Once a leg passes, remove its own crates from the shared directory. A
# generated workspace's members (the model and chassis crates) build under a
# new path in every leg, so no later leg reuses them, and their test binaries
# are most of what the directory grows by: one gate run filled a 193G disk
# (tacplus#282). The dependencies the members built against stay for the next
# leg. Call it only after the leg passed, so a failing leg keeps its build for
# diagnosis. Takes each directory holding a workspace's Cargo.toml: a leg
# whose own crate depends on a generated model in another workspace names both.
# Cargo's "Removed N files, SIZE total" line goes to the gate log.
prune_cargo_workspace() {
  local manifest_dir
  for manifest_dir in "$@"; do
    [[ -f "$manifest_dir/Cargo.toml" ]] || continue
    (cd "$manifest_dir" && cargo clean --workspace --target-dir "$CROSS_TEST_CARGO_TARGET")
  done
}
