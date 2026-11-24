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
# its own. At most GATES_JOBS jobs run at once (default: half the CPUs), each
# building with its share of the CPUs (CARGO_BUILD_JOBS, GOFLAGS -p), and
# current-generation, which runs its fixtures on a worker pool of its own
# (tacplus#812), gets the same cap as TACPLUS_CURRENT_GENERATION_JOBS. So the
# gates use at most about every CPU.
#
# The verdict does not depend on the cap or the order the jobs ran in: each
# gate's output prints whole, in the order named, as soon as it and every gate
# before it finished, under the `=== GATE ===` and `=== GATE rc=N ===` lines,
# and the last line is `=== OVERALL fail=N ===`, giving the first failing
# leg's reason and naming the failing legs after a failure
# (`=== OVERALL fail=1 (LEG: REASON; failed: LEG...) ===`; scripts/gates_test.go runs it
# with two caps and compares). Before that last line a table gives each gate's
# wall time and the total.
#
# A gate whose output waits on a slower one named before it still shows in the
# trace as it ends (tacplus#893): `gates: GATE rc=N pass|FAIL in Ss, D of T
# done; running: ...`, the one line in the order the gates ended. Each leg is
# stopped, with every process it started, after GATES_TIMEOUT seconds (default
# 5400; 0 for none), and fails with rc=124 and a line that names it: a gate
# that hung held a build slot for hours, the trace silent on which. The
# default is the longest a gate may take inside full's budget (gates.yaml),
# not its usual time: current-generation, the longest, took most of a
# 3396 s run (#300), and a slow runner's must not fail for it. The timeout
# needs timeout(1) (coreutils; gtimeout on macOS).
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
export TACPLUS_CURRENT_GENERATION_JOBS="${TACPLUS_CURRENT_GENERATION_JOBS:-$CAP}"

# Each job builds with its share of the CPUs (jobs_share): cargo's and the go
# tool's parallelism, which otherwise is every CPU in each of CAP jobs at once.
# current-generation's workers inherit it, so its pool uses about its slot's
# share times its cap: every CPU, in its slot.
# `go test` gets a longer deadline than its default 10 minutes for the same
# reason: a heavy package (one that builds generated Java, C# or Rust in its
# tests) runs beside the other gates, and the gates' budget, not the
# package's, bounds the run (gates.yaml).
share="$(jobs_share "$CAP")"
export CARGO_BUILD_JOBS="${CARGO_BUILD_JOBS:-$share}"
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-p=$share -timeout=${GATES_GO_TEST_TIMEOUT:-40m}"

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

TIMEOUT="${GATES_TIMEOUT:-5400}"
timeout_cmd=""
if [[ "$TIMEOUT" != 0 ]]; then
  timeout_cmd="$(command -v timeout || command -v gtimeout || true)"
  [[ -n "$timeout_cmd" ]] || echo "gates: no timeout(1) on PATH: GATES_TIMEOUT=$TIMEOUT is not enforced"
fi

# One leg, in its job slot's own cargo directory. The gate's dependencies
# (generate, ...) ran before the gates, so --no-deps: run again by every gate
# at once they would write the same files concurrently. timeout(1) runs it in
# a process group of its own and stops the whole group: TERM, then KILL a
# minute later.
run_leg() {
  export TACPLUS_CROSS_TEST_CARGO_TARGET="$WORKDIR/cargo-slot$JOBS_SLOT"
  if [[ -z "$timeout_cmd" ]]; then
    "$JUST" --no-deps "$1"
    return
  fi
  local rc=0 start=$SECONDS
  "$timeout_cmd" -k 60 "$TIMEOUT" "$JUST" --no-deps "$1" || rc=$?
  if [[ $rc -eq 124 || ( $rc -eq 137 && $((SECONDS - start)) -ge $TIMEOUT ) ]]; then
    echo "gates: $1 timed out after ${TIMEOUT}s (GATES_TIMEOUT): stopped"
    rc=124
  fi
  return $rc
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

# Gate $1's rc and wall time into gate_rc and gate_secs, once its every leg
# finished: it fails with the status of its first failing leg, and took from
# the start of its first leg to the end of its last. Returns 1 while a leg
# runs or waits.
gate_rc=()
gate_secs=()
gate_result() {
  local g="$1" first=${gate_first[$1]} last=${gate_last[$1]} j rc=0
  for ((j = first; j <= last; j++)); do
    [[ "${JOBS_STATUS[$j]:-}" == done ]] || return 1
  done
  local start=${JOBS_START[$first]} end=${JOBS_END[$first]}
  for ((j = first; j <= last; j++)); do
    [[ $rc -eq 0 ]] && rc=${JOBS_RC[$j]}
    [[ ${JOBS_START[$j]} -lt $start ]] && start=${JOBS_START[$j]}
    [[ ${JOBS_END[$j]} -gt $end ]] && end=${JOBS_END[$j]}
  done
  gate_rc[$g]=$rc
  gate_secs[$g]=$((end - start))
}

gates_emit() {
  local i="$1" g first last multi=false
  g="$(gate_of "$i")"
  first=${gate_first[$g]}
  last=${gate_last[$g]}
  [[ $first -ne $last ]] && multi=true
  [[ $i -eq $first ]] && echo "=== ${gate_names[$g]} ==="
  $multi && echo "--- ${JOBS_NAMES[$i]} ---"
  cat "$JOBS_DIR/$i.log"
  $multi && echo "--- ${JOBS_NAMES[$i]} rc=${JOBS_RC[$i]} ---"
  [[ $i -eq $last ]] || return 0
  gate_result "$g"
  echo "=== ${gate_names[$g]} rc=${gate_rc[$g]} ==="
}

# As leg $1 ends: when it was its gate's last to, the gate's rc and wall time,
# how many gates are done, and the legs still running.
gates_done() {
  local g done=0 running="" verdict=pass j
  g="$(gate_of "$1")"
  gate_result "$g" || return 0
  [[ ${gate_rc[$g]} -eq 0 ]] || verdict=FAIL
  for ((j = 0; j < ${#gate_names[@]}; j++)); do
    [[ -n "${gate_rc[$j]:-}" ]] && done=$((done + 1))
  done
  for ((j = 0; j < ${#JOBS_NAMES[@]}; j++)); do
    [[ "${JOBS_STATUS[$j]:-}" == running ]] && running+=" ${JOBS_NAMES[$j]}"
  done
  echo "gates: ${gate_names[$g]} rc=${gate_rc[$g]} $verdict in ${gate_secs[$g]}s, $done of ${#gate_names[@]} done${running:+; running:$running}"
}
JOBS_EMIT=gates_emit
JOBS_DONE=gates_done
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
# The failing legs, named on the last line: a build's verdict quotes it, and
# whoever reads only the verdict (a looper run cannot read the build job's
# log) learns which gate failed, not only that one did. The verdict quotes
# its first 200 or so characters, so the first failing leg's reason leads:
# its log's last `FAILED: ` line (a run.sh's die; `just` prints its own
# error after it), from after `FAILED: `, else its last line, cut to 100
# characters.
failed=""
reason=""
for ((j = 0; j < ${#JOBS_NAMES[@]}; j++)); do
  [[ "${JOBS_RC[$j]:-0}" -ne 0 ]] || continue
  failed+=" ${JOBS_NAMES[$j]}"
  if [[ -z "$reason" ]]; then
    reason="$(grep 'FAILED: ' "$JOBS_DIR/$j.log" | tail -n 1)"
    [[ -n "$reason" ]] || reason="$(grep -v '^[[:space:]]*$' "$JOBS_DIR/$j.log" | tail -n 1)"
    reason="${reason#*FAILED: }"
    reason="${JOBS_NAMES[$j]}: ${reason:0:100}; "
  fi
done
printf '  %-22s %6ss\n' total "$(( $(date +%s) - started ))"
# Each slot's cargo directory holds a dependency build; a failing run keeps
# them, with the jobs' trees, for diagnosis.
[[ $fail -eq 0 ]] && rm -rf "$WORKDIR"/cargo-slot*
echo "=== OVERALL fail=$fail${failed:+ (${reason}failed:$failed)} ==="
exit $fail
