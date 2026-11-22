#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The version floors tacplus hands its consumers, checked against OSV
# (tacplus#949). A consumer takes the floors the generated manifests declare,
# not the lock tacplus's own builds resolve: a lock is the newest compatible
# release, so cargo audit, npm audit and OSV-Scanner pass on it while a
# consumer holding a package at its floor gets the vulnerable version. So this
# generates the cross-test models, and cmd/floors asks OSV (its database
# queried at run time, the time printed under "Checked against") about every
# floor they declare: each generated Cargo.toml, package.json, pyproject.toml,
# C# .csproj and Swift Package.swift, and every dependencies.yaml inventory of
# the checkout (Go, Java, Rust, TypeScript, Python). It fails on an advisory
# the floor itself admits, unless security-exceptions.yaml accepts it; a
# RustSec informational advisory (unmaintained, unsound) warns.
#
# Usage:  scripts/floors.sh
#
# A gate (`just floors`, in `just gates`). No tracked file changes (the tree
# guard fails the run if one does).
#
#   TACPLUS_BIN           an existing tacplus binary (default: built from the checkout)
#   TACPLUS_FLOORS_WORK   the work directory (default: target/floors-work)
#   TACPLUS_PYTHON        the Python interpreter tacplus runs grpc_tools.protoc with
#   TACPLUS_OSV           the OSV API (default: cmd/floors's, https://api.osv.dev)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
# shellcheck source=../cross-test/lib/tree-guard.sh
source "$ROOT/cross-test/lib/tree-guard.sh"

die() {
  echo "floors: $*" >&2
  exit 1
}

[[ $# -eq 0 ]] || { sed -n 's/^# Usage:  /Usage: /p' "${BASH_SOURCE[0]}" >&2; exit 2; }

# The models whose manifests carry the floors:
#   set|proto source (under cross-test/)|pkg|languages
# Go and Java manifests take every version from the inventory, which cmd/floors
# reads itself. The base set is a full service (every Rust chassis crate but
# the member port's and the rotation's) and the Python and TypeScript client;
# the member port and rotation fixtures add their crates; the C2D fixture is
# the one a Swift or C# client generates from.
SETS=(
  "base|proto|com.gitlab.example.dummy.proto|rust python typescript"
  "memberport|memberport/proto|com.example.grid|rust"
  "rotation|rotation/proto|com.example.rotationfixture|rust"
  "c2dserver|c2d-server/proto|com.example.c2dserver|csharp swift"
)

tree_guard_begin "$ROOT"

WORK="${TACPLUS_FLOORS_WORK:-$ROOT/target/floors-work}"
rm -rf "$WORK"
mkdir -p "$WORK/trees" "$WORK/logs"

TACPLUS_BIN="${TACPLUS_BIN:-}"
if [[ -z "$TACPLUS_BIN" ]]; then
  TACPLUS_BIN="$ROOT/target/tacplus-floors"
  echo "==> Building tacplus from $ROOT"
  (cd "$ROOT" && go generate ./pkg/internal/proto/spec/... && go build -o "$TACPLUS_BIN" .) || die "building tacplus failed"
fi

generate() { # set src pkg language
  local set="$1" src="$ROOT/cross-test/$2" pkg="$3" language="$4"
  local dir="$WORK/trees/$set/$language" log="$WORK/logs/$set-$language.log"
  [[ -d "$src" ]] || die "$set: no proto set at $src"
  mkdir -p "$dir"
  printf 'language: "%s"\npkg: "%s"\nsrc: "%s"\noutput-dir: "tacplus"\n' "$language" "$pkg" "$src" >"$dir/tacplus.yaml"
  echo "==> $set/$language"
  if ! (cd "$dir" && TACPLUS_PYTHON="${TACPLUS_PYTHON:-}" "$TACPLUS_BIN" build --config tacplus.yaml --src "$src") >"$log" 2>&1; then
    tail -80 "$log" >&2 || true
    die "generating $set/$language failed (log: $log)"
  fi
}

pids=()
for entry in "${SETS[@]}"; do
  IFS='|' read -r set src pkg languages <<<"$entry"
  (
    for language in $languages; do
      generate "$set" "$src" "$pkg" "$language"
    done
  ) &
  pids+=("$!")
done
failed=0
for pid in "${pids[@]}"; do
  wait "$pid" || failed=1
done
((failed == 0)) || die "a model did not generate (above; logs: $WORK/logs)"

# Every Rust chassis crate's manifest is among those checked: a crate the sets
# above stop reaching would take its literal floors out of the check unseen.
missing=()
for template in "$ROOT"/pkg/templates/rust/chassis/*/Cargo.toml.tmpl; do
  crate="$(basename "$(dirname "$template")")"
  [[ -n "$(find "$WORK/trees" -path "*/rust/*/chassis/$crate/Cargo.toml" -print -quit)" ]] || missing+=("$crate")
done
((${#missing[@]} == 0)) || die "no generated model has the Rust chassis crate(s) ${missing[*]}: add a set that does to SETS"

echo "==> Checking every floor against OSV"
rc=0
(cd "$ROOT" && go run ./cmd/floors -root "$ROOT" -generated "$WORK/trees" ${TACPLUS_OSV:+-osv "$TACPLUS_OSV"}) || rc=$?

tree_guard_end
exit "$rc"
