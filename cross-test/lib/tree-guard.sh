#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# A gate's verdict is about the tree it was asked to test. Anything that
# changes the working tree while a gate runs (a checkout, a stash, an edit)
# silently turns the verdict into one about a different tree. Source this
# file, call tree_guard_begin once the gate starts, and tree_guard_end before
# it reports; the end check dies if the tree changed under the gate.
#
# Only the tracked and untracked SOURCE tree is hashed; build outputs the
# gates write under target/ and the like are git-ignored and do not count.

tree_guard_fingerprint() {
  local root="$1"
  (cd "$root" && git rev-parse HEAD 2>/dev/null; git status --porcelain=v1 --untracked-files=all 2>/dev/null; git diff 2>/dev/null) | shasum -a 256 | cut -d' ' -f1
}

tree_guard_begin() {
  TREE_GUARD_ROOT="$1"
  if ! git -C "$TREE_GUARD_ROOT" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    TREE_GUARD_START=""
    return
  fi
  TREE_GUARD_START="$(tree_guard_fingerprint "$TREE_GUARD_ROOT")"
}

tree_guard_end() {
  [[ -n "${TREE_GUARD_START:-}" ]] || return 0
  local now
  now="$(tree_guard_fingerprint "$TREE_GUARD_ROOT")"
  if [[ "$now" != "$TREE_GUARD_START" ]]; then
    echo "tree guard: the working tree at $TREE_GUARD_ROOT changed while this gate ran; its verdict is about a different tree. Re-run it." >&2
    exit 2
  fi
}
