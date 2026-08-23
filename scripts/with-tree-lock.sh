#!/usr/bin/env bash
# Serialize commands that write this checkout's shared build output.
#
# Usage:  bash scripts/with-tree-lock.sh <command> [args...]
#
# Why this exists: several background jobs run `just gates`, `just
# current-generation`, `just determinism`, ... against this SAME checkout at the
# same time. They all write the same target/ and generated-sources/ trees and
# collide there, producing corrupted or false results rather than an honest
# failure. Three incidents so far: 2026-08-19 (current-generation-rust x2,
# decisions/same-tree-write-exclusion-not-commit-vs-verify.md), 2026-08-22 (a
# voided gate run), 2026-08-23 (cg-race-measure vs. a concurrent `just gates`).
# Read-only work (grep, git log) is safe alongside anything and does not need
# this; anything that regenerates or compiles does.
#
# flock(1) is NOT available on macOS and util-linux is not installed here, so
# this is a plain mkdir-based lock — mkdir is atomic on every filesystem we care
# about, so exactly one process can win it — the same primitive already used by
# testapp/build.sh (decisions/echo-e0463-concurrent-build-virtiofs-race.md).
#
# Unlike that one, this waits instead of failing fast: these are automated
# background jobs, and queueing behind the running job is what the tree-exclusion
# rule actually asks for. It logs while it waits so the queueing is visible in
# the child's log. TACPLUS_TREE_LOCK_TIMEOUT (seconds, default 5400) caps the
# wait so a wedged holder can never hang a job forever.
#
# Re-entrant: the winner exports TACPLUS_TREE_LOCK, so nested invocations from
# inside an already-locked recipe (`just gates` -> `just soundness`) run through
# without deadlocking on the lock their own parent holds.
set -uo pipefail

if [[ $# -eq 0 ]]; then
  echo "with-tree-lock: usage: bash scripts/with-tree-lock.sh <command> [args...]" >&2
  exit 2
fi

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK_DIR="$ROOT/.tacplus-tree.lock"

# Already inside a lock held for this same tree: just run.
if [[ "${TACPLUS_TREE_LOCK:-}" == "$LOCK_DIR" ]]; then
  "$@"
  exit $?
fi

timeout="${TACPLUS_TREE_LOCK_TIMEOUT:-5400}"
waited=0
announced=0

while ! command mkdir "$LOCK_DIR" 2>/dev/null; do
  held_by="$(cat "$LOCK_DIR/pid" 2>/dev/null || echo unknown)"
  if [[ "$held_by" != "unknown" ]] && ! kill -0 "$held_by" 2>/dev/null; then
    echo "with-tree-lock: stale lock from dead pid $held_by — removing" >&2
    rm -rf "$LOCK_DIR"
    continue
  fi
  if [[ $announced -eq 0 ]]; then
    echo "with-tree-lock: tree busy (pid ${held_by}, $(cat "$LOCK_DIR/cmd" 2>/dev/null || echo unknown)) — waiting for $LOCK_DIR" >&2
    announced=1
  elif [[ $((waited % 30)) -eq 0 ]]; then
    echo "with-tree-lock: still waiting (${waited}s, holder pid ${held_by})" >&2
  fi
  if [[ $waited -ge $timeout ]]; then
    echo "with-tree-lock: FAIL gave up after ${waited}s waiting for $LOCK_DIR (holder pid ${held_by})" >&2
    exit 1
  fi
  sleep 1
  waited=$((waited + 1))
done

echo "$$" > "$LOCK_DIR/pid"
echo "$*" > "$LOCK_DIR/cmd"
trap 'rm -rf "$LOCK_DIR"' EXIT
[[ $announced -eq 1 ]] && echo "with-tree-lock: acquired after ${waited}s" >&2

export TACPLUS_TREE_LOCK="$LOCK_DIR"
# Capture and re-raise explicitly: the EXIT trap runs as the script's last
# command and would otherwise replace the wrapped command's status with its own.
"$@"
rc=$?
exit "$rc"
