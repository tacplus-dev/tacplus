#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# `just gates`: run the gates named on the command line and report a
# trustworthy overall pass/fail.
#
# Usage:  bash scripts/gates.sh GATE...   (the justfile's gates recipe names them)
#
# The gates are independent recipes, so they run concurrently (tacplus#300):
# run one after another they used about 1.5 of 16 CPUs and a land took hours.
# A gate whose recipe only names other recipes (cert-stream: cert-stream-go
# cert-stream-java cert-stream-rust) runs each of them, its legs, as a job of
# its own. At most GATES_JOBS jobs run at once (default: half the CPUs), and
# the runners that run their own legs concurrently (current-generation) get
# the same cap as CROSS_TEST_JOBS. So the gates use at most about every CPU.
#
# The verdict does not depend on the cap or the order the jobs ran in: each
# gate's output prints whole, in the order named, as soon as it and every gate
# before it finished, under the `=== GATE ===` and `=== GATE rc=N ===` lines,
# and the last line is `=== OVERALL fail=N ===` (scripts/gates_test.go runs it
# with two caps and compares). Before that last line a table gives each gate's
# wall time and the total.
#
# Before the gates: protoc-check (a protoc/runtime mismatch breaks three gates
# with an error that names neither protoc nor a version), then the generated
# spec descriptors and one tacplus binary, built once for every gate
# (TACPLUS_BIN), so no two gates regenerate or build into the same paths at
# once. Each job slot builds Rust in its own cargo directory under
# GATES_WORKDIR (default target/gates), where the jobs' logs are kept too.
#
# Its caller holds the tree lock (scripts/with-tree-lock.sh) for the whole run.
# GATES_JUST names the `just` to run (scripts/gates_test.go runs a fake one).
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=../cross-test/lib/jobs.sh
source "$ROOT_DIR/cross-test/lib/jobs.sh"

if [[ $# -eq 0 ]]; then
  echo "gates: usage: bash scripts/gates.sh GATE..." >&2
  exit 2
fi

JUST="${GATES_JUST:-just}"
WORKDIR="${GATES_WORKDIR:-$ROOT_DIR/target/gates}"
CAP="${GATES_JOBS:-$(jobs_default_cap)}"
started=$(date +%s)
cd "$ROOT_DIR" || exit 1

echo "=== protoc-check ==="
"$JUST" protoc-check
rc=$?
echo "=== protoc-check rc=$rc ==="
if [[ $rc -ne 0 ]]; then
  echo "=== OVERALL fail=1 (protoc preflight) ==="
  exit 1
fi

if [[ -z "${TACPLUS_BIN:-}" ]]; then
  TACPLUS_BIN="$ROOT_DIR/target/tacplus-gates"
  if ! out="$("$JUST" --no-deps generate 2>&1 && go build -o "$TACPLUS_BIN" . 2>&1)"; then
    printf '%s\n' "$out"
    echo "=== OVERALL fail=1 (generating and building tacplus) ==="
    exit 1
  fi
fi
export TACPLUS_BIN TACPLUS_BIN_PREBUILT=1
export CROSS_TEST_JOBS="${CROSS_TEST_JOBS:-$CAP}"

# The test gate's `go test ./...` walks target/ (the compiler imports the
# generated descriptors in target/proto), so it would also list the Go trees
# the other gates generate under target/ while it runs, and those a failing
# run keeps. A go.mod makes each work directory a module of its own, which
# `./...` skips.
for fence in "$WORKDIR" "$ROOT_DIR/target/cross-test"; do
  mkdir -p "$fence"
  [[ -f "$fence/go.mod" ]] || printf 'module gates.invalid/workdir\n' >"$fence/go.mod"
done
export GATES_WORKDIR="$WORKDIR"

# The legs of gate $1: the recipes it names, when that is all it does;
# otherwise the gate itself.
gate_legs() {
  local show header body
  show="$("$JUST" --show "$1" 2>/dev/null | grep -v '^#')"
  header="$(head -1 <<<"$show")"
  body="$(tail -n +2 <<<"$show" | grep -v '^[[:space:]]*$')"
  if [[ -z "$body" && "$header" == "$1: "* ]]; then
    echo "${header#"$1: "}"
  else
    echo "$1"
  fi
}

# One leg, in its job slot's own cargo directory. The gate's dependencies
# (generate, ...) ran before the gates, so --no-deps: run again by every gate
# at once they would write the same files concurrently.
run_leg() {
  export TACPLUS_CROSS_TEST_CARGO_TARGET="$WORKDIR/cargo-slot$JOBS_SLOT"
  "$JUST" --no-deps "$1"
}

gate_names=()
gate_first=()
gate_last=()
jobs_init "$WORKDIR/jobs" "$CAP"
for gate in "$@"; do
  gate_names+=("$gate")
  gate_first+=("${#JOBS_NAMES[@]}")
  for leg in $(gate_legs "$gate"); do
    jobs_add "$leg" "" run_leg "$leg"
  done
  gate_last+=("$((${#JOBS_NAMES[@]} - 1))")
done

# Index of the gate job $1 belongs to.
gate_of() {
  local g
  for ((g = 0; g < ${#gate_names[@]}; g++)); do
    [[ $1 -le ${gate_last[$g]} ]] && { echo "$g"; return; }
  done
}

gate_rc=()
gate_secs=()
gates_emit() {
  local i="$1" g first last j rc=0 multi=false
  g="$(gate_of "$i")"
  first=${gate_first[$g]}
  last=${gate_last[$g]}
  [[ $first -ne $last ]] && multi=true
  [[ $i -eq $first ]] && echo "=== ${gate_names[$g]} ==="
  $multi && echo "--- ${JOBS_NAMES[$i]} ---"
  cat "$JOBS_DIR/$i.log"
  $multi && echo "--- ${JOBS_NAMES[$i]} rc=${JOBS_RC[$i]} ---"
  [[ $i -eq $last ]] || return 0
  # The gate fails with the status of its first failing leg; it took from
  # the start of its first leg to the end of its last.
  local start=${JOBS_START[$first]} end=${JOBS_END[$first]}
  for ((j = first; j <= last; j++)); do
    [[ $rc -eq 0 ]] && rc=${JOBS_RC[$j]}
    [[ ${JOBS_START[$j]} -lt $start ]] && start=${JOBS_START[$j]}
    [[ ${JOBS_END[$j]} -gt $end ]] && end=${JOBS_END[$j]}
  done
  gate_rc[$g]=$rc
  gate_secs[$g]=$((end - start))
  echo "=== ${gate_names[$g]} rc=$rc ==="
}
JOBS_EMIT=gates_emit
jobs_run

fail=0
echo "=== gate wall times (GATES_JOBS=$CAP) ==="
for ((g = 0; g < ${#gate_names[@]}; g++)); do
  verdict=pass
  if [[ ${gate_rc[$g]} -ne 0 ]]; then
    verdict=FAIL
    fail=1
  fi
  printf '  %-22s %6ss  %s\n' "${gate_names[$g]}" "${gate_secs[$g]}" "$verdict"
done
printf '  %-22s %6ss\n' total "$(( $(date +%s) - started ))"
# Each slot's cargo directory holds a dependency build; a failing run keeps
# them, with the jobs' trees, for diagnosis.
[[ $fail -eq 0 ]] && rm -rf "$WORKDIR"/cargo-slot*
echo "=== OVERALL fail=$fail ==="
exit $fail
