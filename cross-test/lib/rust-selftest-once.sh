#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# A Rust chassis crate's self-tests once per gate for each source they test
# (tacplus#843).
# Most fixtures of a gate render the same chassis crates (key, jwk, token,
# spiffe, x509, ...): identical source, built by the same toolchain with the
# same flags, gives the identical test, and running it in every fixture cost
# the Rust gate most of its time. Source this file; it defines functions only.
#
# rust_selftest_start DIR [JOB] starts the gate's record of the crates' hashes
# that passed in DIR, empty, and exports RUST_SELFTEST_PASSED_DIR. With JOB true (a
# run of one fixture) it keeps DIR when RUST_SELFTEST_PASSED_DIR is DIR already:
# a job of a parallel gate (tacplus#812) shares its pool's record, which the
# job's caller started, rather than emptying it for the jobs running beside it.
#
# rust_selftest_reused WORKSPACE LOG CMD... [::: CMD...] sets
# RUST_SELFTEST_REUSED to the chassis crates (`-p NAME` lines) of the cargo
# calls CMD whose content hash (cross-test/lib/rust_selftest_hash.py: the
# crate's part of each call's cargo tree, its sources, the part of the model
# types crate it reaches, the call and the toolchain) has passed in this gate.
# The caller leaves them out of its calls and checks them with `cargo check`.
# It prints each to the gate log and LOG with its hash and the fixture it passed
# in. Without RUST_SELFTEST_PASSED_DIR, or a hash it cannot compute, it reuses
# none.
#
# rust_selftest_record WORKSPACE LOG CMD... [::: CMD...], once the calls CMD
# have passed, records the hash of each chassis crate they selected, printed to
# the gate log and LOG. The fixture is LOG's directory.

RUST_SELFTEST_HASH_PY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/rust_selftest_hash.py"

rust_selftest_start() {
  local dir="$1" job="${2:-false}"
  if [[ "$job" != true || "${RUST_SELFTEST_PASSED_DIR:-}" != "$dir" ]]; then
    rm -rf "$dir"
  fi
  mkdir -p "$dir"
  export RUST_SELFTEST_PASSED_DIR="$dir"
}

rust_selftest_hashes() {
  local workspace="$1"
  shift
  python3 -I "$RUST_SELFTEST_HASH_PY" "$workspace" "$@"
}

rust_selftest_reused() {
  local workspace="$1" log="$2"
  shift 2
  local hashes crate hash from
  RUST_SELFTEST_REUSED=""
  : >"$log"
  [[ -n "${RUST_SELFTEST_PASSED_DIR:-}" ]] || return 0
  hashes="$(rust_selftest_hashes "$workspace" "$@" 2>>"$log")" || {
    echo "    rust self-test: no content hash; running every crate's tests" | tee -a "$log"
    return 0
  }
  while read -r crate hash; do
    [[ -n "$crate" && -f "$RUST_SELFTEST_PASSED_DIR/$hash" ]] || continue
    from="$(cat "$RUST_SELFTEST_PASSED_DIR/$hash")"
    echo "    rust self-test reused: $crate hash $hash passed in fixture $from" | tee -a "$log"
    RUST_SELFTEST_REUSED+="-p $crate"$'\n'
  done <<<"$hashes"
}

rust_selftest_record() {
  local workspace="$1" log="$2"
  shift 2
  local fixture hashes crate hash
  [[ -n "${RUST_SELFTEST_PASSED_DIR:-}" ]] || return 0
  fixture="$(basename "$(dirname "$log")")"
  hashes="$(rust_selftest_hashes "$workspace" "$@" 2>>"$log")" || {
    echo "    rust self-test: no content hash; this fixture's tests are not reused" | tee -a "$log"
    return 0
  }
  while read -r crate hash; do
    [[ -n "$crate" ]] || continue
    echo "    rust self-test ran: $crate hash $hash in fixture $fixture" | tee -a "$log"
    # Renamed into place: a job beside this one reads it whole or not at all.
    printf '%s\n' "$fixture" >"$RUST_SELFTEST_PASSED_DIR/.$hash.$$"
    mv -f "$RUST_SELFTEST_PASSED_DIR/.$hash.$$" "$RUST_SELFTEST_PASSED_DIR/$hash"
  done <<<"$hashes"
}
