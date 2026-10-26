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
# Exported so a bare `cargo test` in a leg lands here too.
export CARGO_TARGET_DIR="$CROSS_TEST_CARGO_TARGET"
