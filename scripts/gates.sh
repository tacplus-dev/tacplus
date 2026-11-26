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
# its own. A gate GATE with a recipe GATE-shards (current-generation, its
# languages and fixtures: tacplus#977) runs its shards as its legs: each line
# GATE-shards prints is `SHARD [DEP...]`, and the leg GATE[SHARD] runs
# `just GATE-shard SHARD` once the shards DEP passed (a dependency named
# before it). The shards fill every slot the other gates leave, so the gate's
# wall time is its slowest chain of shards: run on a pool of its own in one
# slot (tacplus#812) it was the longest gate, 1767 s of a 46-minute run.
# At most GATES_JOBS jobs run at once (default: the CPUs), each building with
# the share of the CPUs a job of half as many would get (CARGO_BUILD_JOBS,
# GOFLAGS -p): most legs use about one CPU or two, and a heavy one alone (the
# test gate's `go test ./...`) is not slowed by the pool's width.
#
# The verdict does not depend on the cap or the order the jobs ran in: each
# gate's output prints whole, in the order named, as soon as it and every gate
# before it finished, under the `=== GATE ===` and `=== GATE rc=N ===` lines,
# and the last line is `=== OVERALL fail=N ===`, giving the first failing
# leg's reason and naming the failing legs after a failure
# (`=== OVERALL fail=1 (LEG: REASON; failed: LEG...) ===`; scripts/gates_test.go runs it
# with two caps and compares). Before that last line a table gives each gate's
# wall time, each shard's under its gate, the total, and the pool's use of the
# machine: how busy its slots were, and the CPU time its processes used
# against the CPUs' (a reaped process's: bash's `times`).
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
# Every leg's Rust through one compiler cache, its server started here, once,
# for them all (tacplus#979).
# shellcheck source=../cross-test/lib/sccache.sh
source "$ROOT_DIR/cross-test/lib/sccache.sh"

if [[ $# -eq 0 ]]; then
  echo "gates: usage: bash scripts/gates.sh GATE..." >&2
  exit 2
fi

JUST="${GATES_JUST:-just}"
WORKDIR="${GATES_WORKDIR:-$ROOT_DIR/target/gates}"
CAP="${GATES_JOBS:-$(jobs_cpus)}"
started=$(date +%s)
cd "$ROOT_DIR" || exit 1

if [[ "${RUSTC_WRAPPER:-}" == */sccache-rustc.sh ]]; then
  sccache --zero-stats >/dev/null 2>&1 || true
  echo "gates: Rust compiles through sccache, its cache ${SCCACHE_DIR:-${SCCACHE_GCS_BUCKET:+gs://$SCCACHE_GCS_BUCKET}}"
fi

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

# Each job builds with its share of the CPUs (jobs_share): cargo's and the go
# tool's parallelism, which otherwise is every CPU in each of CAP jobs at once.
# `go test` gets a longer deadline than its default 10 minutes for the same
# reason: a heavy package (one that builds generated Java, C# or Rust in its
# tests) runs beside the other gates, and the gates' budget, not the
# package's, bounds the run (gates.yaml).
share="$(jobs_share "$(( CAP / 2 > 0 ? CAP / 2 : 1 ))")"
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

# Adds the legs of gate $1 as jobs: its shards, when it lists them; else the
# recipes it names, when that is all it does; otherwise the gate itself.
gate_add_legs() {
  local gate="$1" show header body leg shards shard deps dep
  if "$JUST" --show "$gate-shards" >/dev/null 2>&1; then
    if shards="$("$JUST" --no-deps "$gate-shards" 2>&1)" && [[ -n "$shards" ]]; then
      while read -r shard deps; do
        [[ -n "$shard" ]] || continue
        leg=""
        for dep in $deps; do
          leg+="$gate[$dep] "
        done
        jobs_add "$gate[$shard]" "$leg" run_leg "$gate[$shard]" "$gate-shard" "$shard"
      done <<<"$shards"
      return
    fi
    # Its whole recipe then: the same verdict, in one slot.
    printf '%s\n' "$shards"
    echo "gates: $gate-shards listed no shards: running $gate whole"
  fi
  show="$("$JUST" --show "$gate" 2>/dev/null | grep -v '^#')"
  header="$(head -1 <<<"$show")"
  body="$(tail -n +2 <<<"$show" | grep -v '^[[:space:]]*$')"
  if [[ -z "$body" && "$header" == "$gate: "* ]]; then
    for leg in ${header#"$gate: "}; do
      jobs_add "$leg" "" run_leg "$leg" "$leg"
    done
  else
    jobs_add "$gate" "" run_leg "$gate" "$gate"
  fi
}

TIMEOUT="${GATES_TIMEOUT:-5400}"
timeout_cmd=""
if [[ "$TIMEOUT" != 0 ]]; then
  timeout_cmd="$(command -v timeout || command -v gtimeout || true)"
  [[ -n "$timeout_cmd" ]] || echo "gates: no timeout(1) on PATH: GATES_TIMEOUT=$TIMEOUT is not enforced"
fi

# Leg $1, the recipe $2 with the arguments after it, in its job slot's own
# cargo directory. The gate's dependencies (generate, ...) ran before the
# gates, so --no-deps: run again by every gate at once they would write the
# same files concurrently. timeout(1) runs it in a process group of its own
# and stops the whole group: TERM, then KILL a minute later.
run_leg() {
  local name="$1"
  shift
  export TACPLUS_CROSS_TEST_CARGO_TARGET="$WORKDIR/cargo-slot$JOBS_SLOT"
  if [[ -z "$timeout_cmd" ]]; then
    "$JUST" --no-deps "$@"
    return
  fi
  local rc=0 start=$SECONDS
  "$timeout_cmd" -k 60 "$TIMEOUT" "$JUST" --no-deps "$@" || rc=$?
  if [[ $rc -eq 124 || ( $rc -eq 137 && $((SECONDS - start)) -ge $TIMEOUT ) ]]; then
    echo "gates: $name timed out after ${TIMEOUT}s (GATES_TIMEOUT): stopped"
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
  gate_add_legs "$gate"
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
  printf '  %-32s %6ss  %s\n' "${gate_names[$g]}" "${gate_secs[$g]}" "$verdict"
  # A sharded gate's shards, each with its own time: the slowest bounds it.
  for ((j = gate_first[g]; j <= gate_last[g]; j++)); do
    [[ "${JOBS_NAMES[$j]}" == "${gate_names[$g]}["* ]] || continue
    shard="${JOBS_NAMES[$j]#"${gate_names[$g]}["}"
    leg_verdict=pass
    [[ ${JOBS_RC[$j]} -eq 0 ]] || leg_verdict=FAIL
    printf '    %-30s %6ss  %s\n' "${shard%]}" "${JOBS_SECS[$j]}" "$leg_verdict"
  done
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
wall=$(( $(date +%s) - started ))
printf '  %-32s %6ss\n' total "$wall"
# What the compiler cache the slots share did for this run's Rust. Its server
# runs the compiles it misses, so their CPU time is not in the pool's below.
stats="$(sccache_stats)"
[[ -z "$stats" ]] || echo "  $stats"
# The pool's use of the machine: its slots' busy time against CAP slots for
# the whole run, and the CPU time of every process the gates ran and reaped
# (`times`' second line, the children's user and system time) against the
# CPUs'. A low share says the run waited on a long leg or on I/O, not CPUs.
busy=0
for ((j = 0; j < ${#JOBS_NAMES[@]}; j++)); do
  busy=$((busy + ${JOBS_SECS[$j]:-0}))
done
cpus="$(jobs_cpus)"
# In this shell, not a pipeline's: a subshell's children start at zero.
times >"$WORKDIR/times"
tail -n 1 "$WORKDIR/times" | awk -v cap="$CAP" -v busy="$busy" -v wall="$wall" -v cpus="$cpus" '
  function secs(t,  m) { m = t; sub(/m.*/, "", m); sub(/^[0-9]+m/, "", t); sub(/s$/, "", t); return m * 60 + t }
  { if (wall < 1) wall = 1; used = secs($1) + secs($2); cpu = used / wall
    printf "  pool: %d slots, %d%% busy; CPU time %.1fs, %.1f of %d CPUs (%d%%)\n", cap, 100 * busy / (cap * wall), used, cpu, cpus, 100 * cpu / cpus }'
# Each slot's cargo directory holds a dependency build; a failing run keeps
# them, with the jobs' trees, for diagnosis.
[[ $fail -eq 0 ]] && rm -rf "$WORKDIR"/cargo-slot*
echo "=== OVERALL fail=$fail${failed:+ (${reason}failed:$failed)} ==="
exit $fail
