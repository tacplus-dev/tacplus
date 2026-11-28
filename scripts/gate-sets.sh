#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# The per-language gate sets of gates.yaml (tacplus#976), and their audit.
#
# Usage:  bash scripts/gate-sets.sh legs LANG        the legs LANG's set runs, in full's order
#         bash scripts/gate-sets.sh table             LEGS: "gate leg languages", a line
#         bash scripts/gate-sets.sh audit [BASE [HEAD]]
#                                                    for the diff BASE..HEAD (default: the
#                                                    merge base with origin/main, and HEAD),
#                                                    the sets a land runs and why
#         bash scripts/gate-sets.sh audit --paths PATH...
#                                                    the same, for the paths given
#
# A change under pkg/internal/<lang>/ or pkg/templates/<lang>/ can change only
# what tacplus generates for <lang>: those packages are imported by pkg/builder
# alone, register nothing at init, and each reads only its own language's
# templates. So <lang>'s set is every leg of the full gates (`just gates`)
# that generates <lang> (LEGS below: a leg that also generates another
# language, a Go server for a TypeScript client, names both), with the legs
# every change runs (soundness, test, floors). Anything else under pkg/ (the
# shared generator, pkg/internal/common, pkg/templates/embed.go) maps to no
# set, so it runs full.
#
# LEGS covers every gate of the justfile's gates recipe: a gate whose recipe
# names its legs (cert-stream: cert-stream-go ...) by those legs, one that runs
# every language itself (current-generation, determinism) by its --language
# recipes. scripts/gate_sets_test.go checks that against the justfile and
# gates.yaml, so a gate added to `just gates` fails the test until it is here.
#
# audit reads gates.yaml as the looper does (tacplus-dev/looper README, Gates
# scaled to the diff; repo-looper's choose_gates): every changed path is
# matched against every set; a path that matches no set, or one of full's
# paths, a change to gates.yaml itself and no changed path at all run full.
# It prints each path and the sets it matched (and by which pattern), the sets
# chosen and why, each chosen set's legs, and the chosen sets' total minutes.
# The full gates of main (the nightly scan, the rolling check) still run
# everything: a leg red there that the sets of the lands since its last green
# did not run is a miss of this map (audit names the legs each set ran), which
# the full check records (tacplus-dev/looper#253).
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MAP="${GATE_SETS_MAP:-$ROOT_DIR/gates.yaml}"
LANGUAGES=(go java rust typescript python csharp swift)

# gate | leg | the languages it generates (* every one: it runs on every set)
LEGS='
soundness             | soundness                    | *
test                  | test                         | *
current-generation    | current-generation-go        | go
current-generation    | current-generation-java      | java
current-generation    | current-generation-rust      | rust
current-generation    | current-generation-typescript| typescript
determinism           | determinism-go               | go
determinism           | determinism-rust             | rust
determinism           | determinism-python           | python
determinism           | determinism-typescript       | typescript
determinism           | determinism-java             | java
cert-stream           | cert-stream-go               | go
cert-stream           | cert-stream-java             | java
cert-stream           | cert-stream-rust             | rust
stream-expiry         | stream-expiry-go             | go
stream-expiry         | stream-expiry-java           | java
stream-expiry         | stream-expiry-rust           | rust
rotation              | rotation-go                  | go
rotation              | rotation-java                | java
rotation              | rotation-rust                | rust
rotation              | rotation-clients-typescript  | typescript go
rotation              | rotation-clients-python      | python go
rotation              | rotation-clients-csharp      | csharp go
rotation              | rotation-clients-swift       | swift go
memberport            | memberport-java              | java
memberport            | memberport-go                | go java
memberport            | memberport-rust              | rust java
claims-provenance     | claims-provenance            | java
accounting-identity   | accounting-identity          | java
w2s-validate          | w2s-validate-rust            | rust
w2s-validate          | w2s-validate-go              | go
w2s-validate          | w2s-validate-java            | java
c2d-server            | c2d-server-go                | go
c2d-server            | c2d-server-java              | java
c2d-server            | c2d-server-rust              | rust
c2d-server            | c2d-server-swift             | swift go
zero-rpc-s2s          | zero-rpc-s2s                 | rust
config-push           | config-push-go               | go
llm-rule              | llm-rule                     | rust go
jwk-alg               | jwk-alg-go                   | go
jwk-alg               | jwk-alg-java                 | java
jwk-alg               | jwk-alg-python               | python
jwk-alg               | jwk-alg-rust                 | rust
jwk-alg               | jwk-alg-typescript           | typescript
redact                | redact-go                    | go
redact                | redact-java                  | java
redact                | redact-rust                  | rust
trace-carry           | trace-carry-go               | go
trace-carry           | trace-carry-java             | java
trace-carry           | trace-carry-rust             | rust
floors                | floors                       | *
tacplus-script        | tacplus-script               | go
'
# Why the legs that name two languages do: rotation-clients-* run each client
# against the generated Go server; memberport-go and -rust run --interop, a
# leg against the generated Java member port; c2d-server-swift's daemon is the
# generated Go one. llm-rule generates a Rust model and a Go one, each with
# its advisor gate. floors generates every language's manifests.

die() { echo "gate-sets: $*" >&2; exit 2; }

# The table's rows, "gate leg languages", one a line.
rows() {
  awk -F'|' 'NF == 3 { for (i = 1; i <= 3; i++) gsub(/^[ \t]+|[ \t]+$/, "", $i); print $1, $2, $3 }' <<<"$LEGS"
}

legs() { # language
  local lang="$1" gate leg langs
  [[ " ${LANGUAGES[*]} " == *" $lang "* ]] || die "no language $lang (one of: ${LANGUAGES[*]})"
  while read -r gate leg langs; do
    if [[ " $langs " == *" * "* || " $langs " == *" $lang "* ]]; then echo "$leg"; fi
  done < <(rows)
}

# The gate map, as repo-looper's read_sets reads it: SETS (in order), and for
# each RUN, MIN and PATHS (patterns, one a line).
SETS=(); declare -A RUN=() MIN=() PATHS=()
read_map() {
  local s k v
  [[ -f "$MAP" ]] || die "no gate map at $MAP"
  while IFS=$'\t' read -r s k v; do
    case "$k" in
      set) SETS+=("$s"); RUN[$s]=""; MIN[$s]=""; PATHS[$s]="" ;;
      run) RUN[$s]="$v" ;;
      minutes) MIN[$s]="$v" ;;
      path) PATHS[$s]+="$v"$'\n' ;;
    esac
  done < <(awk '
    function unq(v) {
      sub(/^[ \t]+/, "", v); sub(/[ \t]+$/, "", v)
      if (v ~ /^".*"$/ || v ~ /^\047.*\047$/) v = substr(v, 2, length(v) - 2)
      return v
    }
    /^[ \t]*(#.*)?$/ { next }
    /^[A-Za-z0-9_-]+:[ \t]*$/ { set = $0; sub(/:.*/, "", set); list = 0; print set "\tset\t"; next }
    /^[ \t]+- / { if (list) { v = $0; sub(/^[ \t]+- /, "", v); print set "\tpath\t" unq(v) }; next }
    /^[ \t]+(run|minutes|memory|paths):/ {
      k = $0; sub(/^[ \t]+/, "", k); v = k; sub(/:.*/, "", k); sub(/^[a-z]+:/, "", v); sub(/^[ \t]+/, "", v); list = 0
      if (k != "paths") { print set "\t" k "\t" unq(v); next }
      if (v == "") { list = 1; next }
      v = substr(v, 2, length(v) - 2); n = split(v, items, ",")
      for (i = 1; i <= n; i++) if (unq(items[i]) != "") print set "\tpath\t" unq(items[i])
    }' "$MAP")
  [[ -n "${RUN[full]:-}" ]] || die "$MAP has no full set"
}

glob_regex() { # pattern: the ERE matching the paths it names (repo-looper's)
  local p="$1" r="" c i=0
  [[ "$p" == */ ]] && p+="**"
  [[ "$p" == */* ]] || p="**/$p"
  p="${p#/}"
  while (( i < ${#p} )); do
    c="${p:i:1}"
    if [[ "${p:i:3}" == "**/" ]]; then r+="(.*/)?"; i=$(( i + 3 )); continue; fi
    if [[ "${p:i:2}" == "**" ]]; then r+=".*"; i=$(( i + 2 )); continue; fi
    case "$c" in
      '*') r+='[^/]*' ;;
      '?') r+='[^/]' ;;
      .|^|$|+|'('|')'|'{'|'}'|'|'|'['|']'|\\) r+="\\$c" ;;
      *) r+="$c" ;;
    esac
    i=$(( i + 1 ))
  done
  echo "^$r\$"
}

# The legs of a set whose run is `just gates-lang LANG`; else its run.
set_runs() { # set
  local run="${RUN[$1]}"
  if [[ "$run" =~ ^just\ gates-lang\ ([a-z]+)$ ]]; then
    echo "  $1 runs: $(legs "${BASH_REMATCH[1]}" | paste -sd' ' -)"
  else
    echo "  $1 runs: $run"
  fi
}

audit() { # changed paths, one a line
  local changed="$1" f s p hit why="" seen=" " chosen=() total=0 sum="" k
  read_map
  echo "changed paths:"
  if [[ -z "$changed" ]]; then
    why="no changed path"
  else
    ! grep -qxF gates.yaml <<<"$changed" || why="gates.yaml changed"
    while IFS= read -r f; do
      hit=""
      for s in "${SETS[@]}"; do
        while IFS= read -r p; do
          [[ -n "$p" && "$f" =~ $(glob_regex "$p") ]] || continue
          hit+="${hit:+, }$s ($p)"
          [[ "$s" == full && -z "$why" ]] && why="$f is mapped to full ($p)"
          [[ "$seen" == *" $s "* ]] || seen+="$s "
          break
        done <<<"${PATHS[$s]}"
      done
      if [[ "$f" == gates.yaml ]]; then
        hit="full (the gate map itself)"
      elif [[ -z "$hit" ]]; then
        hit="no set: full"; why="${why:-$f matches no gate set}"
      fi
      echo "  $f: $hit"
    done <<<"$changed"
  fi
  if [[ -n "$why" ]]; then
    chosen=(full)
  else
    for s in "${SETS[@]}"; do [[ "$seen" == *" $s "* ]] && chosen+=("$s"); done
    k="$(wc -l <<<"$changed" | tr -d ' ')"
    if (( k == 1 )); then why="its one changed path, $changed, maps to $(IFS=+; echo "${chosen[*]}")"
    else why="all $k changed paths map to $(IFS=,; echo "${chosen[*]}" | sed 's/,/, /g')"; fi
  fi
  echo "gates: $(IFS=+; echo "${chosen[*]}") ($why)"
  for s in "${chosen[@]}"; do
    set_runs "$s"
    total=$(( total + ${MIN[$s]:-0} ))
    sum+="${sum:+ + }$s ${MIN[$s]:-?}"
  done
  echo "minutes: $total ($sum)"
}

case "${1:-}" in
  legs)
    [[ $# -eq 2 ]] || die "usage: legs LANG"
    legs "$2"
    ;;
  table)
    rows
    ;;
  audit)
    shift
    if [[ "${1:-}" == --paths ]]; then
      shift
      audit "$(printf '%s\n' "$@" | grep -v '^$')"
    else
      cd "$ROOT_DIR" || exit 2
      base="${1:-${LOOPER_GATES_BASE:-$(git merge-base HEAD origin/main 2>/dev/null)}}"
      [[ -n "$base" ]] || die "no base: name one (audit BASE [HEAD])"
      changed="$(git diff --name-only --no-renames "$base" "${2:-HEAD}")" || die "cannot read the paths changed since $base"
      audit "$changed"
    fi
    ;;
  *)
    die "usage: gate-sets.sh legs LANG | table | audit [BASE [HEAD]] | audit --paths PATH..."
    ;;
esac
