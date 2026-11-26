#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# One compiler cache for every cross-test leg (tacplus#979): each job slot of
# a gate run builds in a target directory of its own, since cargo locks one for
# a whole build, and each slot compiled the dependency set again (or started
# from a copy of several GB). With sccache on PATH every rustc goes through it, by way
# of cross-test/lib/sccache-rustc.sh, so a dependency one slot compiled is a
# cache hit in the others and in the next run. Its cache is the build's own
# when it configured one (tacplus-cache env: RUSTC_WRAPPER=sccache and its
# SCCACHE_ settings), else one directory beside the shared cargo directory,
# which sccache bounds (10 GB, least recently used first). Another
# RUSTC_WRAPPER is kept; TACPLUS_CROSS_TEST_SCCACHE=0 turns it off. A cache
# only speeds a build up: images/cache-determinism.sh holds a cold and a warm
# build to the same verdict.
# Source it with ROOT_DIR set (cross-test/lib/cargo-target.sh and
# scripts/gates.sh do); sccache_active says whether it is on, sccache_stats prints what the cache did since the
# server started or its stats were zeroed.
if [[ "${TACPLUS_CROSS_TEST_SCCACHE:-1}" != 0 && "${RUSTC_WRAPPER:-sccache}" == sccache ]] && command -v sccache >/dev/null; then
  RUSTC_WRAPPER="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/sccache-rustc.sh"
  export RUSTC_WRAPPER
  if [[ -z "${SCCACHE_DIR:-}" && -z "${SCCACHE_GCS_BUCKET:-}" ]]; then
    export SCCACHE_DIR="$ROOT_DIR/target/cross-test/sccache"
  fi
  # Started here, before any cargo: a server a rustc started would take its
  # cargo's jobserver. One already running (another leg's) is kept.
  env -u CARGO_MAKEFLAGS -u MAKEFLAGS -u MFLAGS sccache --start-server >/dev/null 2>&1 || true
fi

# Whether the legs' rustc goes through sccache.
sccache_active() {
  [[ "${RUSTC_WRAPPER:-}" == */sccache-rustc.sh ]]
}

# One line: the compiles sccache answered from its cache, the ones it ran, and
# the calls it cannot cache (a binary, a proc-macro or a test harness, which
# link); nothing when the legs do not compile through it.
sccache_stats() {
  sccache_active || return 0
  sccache --show-stats 2>/dev/null | awk '
    /^Cache hits +[0-9]+$/ { hits = $NF }
    /^Cache misses +[0-9]+$/ { misses = $NF }
    /^Non-cacheable calls +[0-9]+$/ { other = $NF }
    END { if (hits != "") printf "sccache: %d hits, %d misses, %d not cacheable\n", hits, misses, other }'
}
