#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# A bounded job scheduler for the gates (tacplus#300): `just gates` runs its
# gates and their language legs with it, and current-generation its fixtures.
# Run serially, the full gates used about 1.5 of 16 CPUs and a land took hours.
#
#   jobs_init LOGDIR CAP        start a job set; at most CAP jobs run at once
#   jobs_add NAME DEPS CMD...   add a job: CMD runs once every job named in
#                               DEPS (space-separated, added before it) passed
#   jobs_run                    run them all; JOBS_FAILED is the number that
#                               failed. Call it as a plain statement, never in
#                               a condition (`if jobs_run`, `jobs_run || ...`):
#                               bash ignores errexit in everything a condition
#                               runs, the jobs' subshells included, even where
#                               they set -e again.
#
# Each job runs in its own subshell, with the errexit setting of the caller,
# its output in LOGDIR/<index>.log. A job whose dependency failed does not run
# and fails. While the set runs, JOBS_EMIT (default jobs_emit_log) is called
# with each job's index as soon as it AND every job added before it finished,
# so what it prints comes out in the order the jobs were added, whatever the
# order they ran in: the log and the verdict never depend on the cap.
# Afterwards JOBS_RC[i] holds each job's exit status, JOBS_SECS[i] its wall
# time in seconds, and JOBS_START[i] and JOBS_END[i] when it started and ended
# (seconds since the epoch).
#
# A running job gets JOBS_SLOT, an integer below CAP that no other running job
# holds. A job keeps per-slot state with it (the cargo target directory: see
# use_slot_cargo_target in cargo-target.sh), so two jobs running at once never
# share it, while the jobs that run one after another in a slot reuse it.
#
# Plain bash 3.2 (macOS): indexed arrays only, no `wait -n`. The scheduler
# polls each running job's status file.

# Half the CPUs, at least one: the default cap of `just gates` and of the
# runners that run their own legs concurrently. Each job is itself a build
# that uses more than one CPU.
jobs_default_cap() {
  local cpus
  cpus="$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)"
  [[ "$cpus" =~ ^[0-9]+$ ]] || cpus=2
  echo $(( cpus / 2 > 0 ? cpus / 2 : 1 ))
}

jobs_init() {
  JOBS_DIR="$1"
  JOBS_CAP="$2"
  [[ "$JOBS_CAP" =~ ^[0-9]+$ && "$JOBS_CAP" -ge 1 ]] || JOBS_CAP=1
  rm -rf "$JOBS_DIR"
  mkdir -p "$JOBS_DIR"
  JOBS_NAMES=()
  JOBS_DEPS=()
  JOBS_CMDS=()
  JOBS_RC=()
  JOBS_SECS=()
  JOBS_START=()
  JOBS_END=()
}

jobs_add() {
  local name="$1" deps="$2"
  shift 2
  local cmd="" arg
  for arg in "$@"; do
    cmd+="$(printf '%q' "$arg") "
  done
  JOBS_NAMES+=("$name")
  JOBS_DEPS+=("$deps")
  JOBS_CMDS+=("$cmd")
}

# The default emitter: the job's log under a header, then its status.
jobs_emit_log() {
  local i="$1"
  echo "--- ${JOBS_NAMES[$i]} ---"
  cat "$JOBS_DIR/$i.log"
  echo "--- ${JOBS_NAMES[$i]} rc=${JOBS_RC[$i]} (${JOBS_SECS[$i]}s) ---"
}

jobs_index_of() {
  local name="$1" i
  for ((i = 0; i < ${#JOBS_NAMES[@]}; i++)); do
    [[ "${JOBS_NAMES[$i]}" == "$name" ]] && { echo "$i"; return 0; }
  done
  return 1
}

# Prints "ok" when every dependency of job $1 passed, "failed NAME" when one
# failed or does not exist, and nothing while one is still to finish.
jobs_deps_state() {
  local dep j
  for dep in ${JOBS_DEPS[$1]}; do
    j="$(jobs_index_of "$dep")" || { echo "failed $dep"; return; }
    [[ "$j" -lt "$1" ]] || { echo "failed $dep"; return; }
    [[ -n "${JOBS_RC[$j]:-}" ]] || return 0
    [[ "${JOBS_RC[$j]}" -eq 0 ]] || { echo "failed $dep"; return; }
  done
  echo ok
}

jobs_start() {
  local i="$1" slot="$2" errexit=0
  [[ "$-" == *e* ]] && errexit=1
  (
    set +e
    export JOBS_SLOT="$slot"
    start=$(date +%s)
    ( [[ "$errexit" -eq 1 ]] && set -e; eval "${JOBS_CMDS[$i]}" ) >"$JOBS_DIR/$i.log" 2>&1 </dev/null
    rc=$?
    end=$(date +%s)
    echo "$rc $((end - start)) $start $end" >"$JOBS_DIR/$i.status.tmp"
    mv "$JOBS_DIR/$i.status.tmp" "$JOBS_DIR/$i.status"
  ) &
  JOBS_PID[$i]=$!
}

jobs_run() {
  local n=${#JOBS_NAMES[@]} i k state running=0 finished=0 emitted=0 failed=0 free
  local -a slot_of=() slot_busy=() status=()
  JOBS_PID=()
  for ((k = 0; k < JOBS_CAP; k++)); do slot_busy[$k]=0; done
  while [[ $emitted -lt $n ]]; do
    # Reap the jobs that finished.
    for ((i = 0; i < n; i++)); do
      [[ "${status[$i]:-}" == running && -f "$JOBS_DIR/$i.status" ]] || continue
      wait "${JOBS_PID[$i]}" 2>/dev/null
      read -r JOBS_RC[$i] JOBS_SECS[$i] JOBS_START[$i] JOBS_END[$i] <"$JOBS_DIR/$i.status"
      status[$i]=done
      slot_busy[${slot_of[$i]}]=0
      running=$((running - 1))
      finished=$((finished + 1))
    done
    # Start what is ready, in the order added, while a slot is free. A job
    # whose dependency failed finishes at once, failed, without running.
    for ((i = 0; i < n; i++)); do
      [[ -z "${status[$i]:-}" ]] || continue
      state="$(jobs_deps_state "$i")"
      if [[ "$state" == failed* ]]; then
        echo "not run: dependency ${state#failed } failed" >"$JOBS_DIR/$i.log"
        JOBS_RC[$i]=1
        JOBS_SECS[$i]=0
        JOBS_START[$i]=$(date +%s)
        JOBS_END[$i]=${JOBS_START[$i]}
        status[$i]=done
        finished=$((finished + 1))
        continue
      fi
      [[ "$state" == ok && $running -lt $JOBS_CAP ]] || continue
      for ((k = 0; k < JOBS_CAP; k++)); do
        [[ ${slot_busy[$k]} -eq 0 ]] && { free=$k; break; }
      done
      slot_busy[$free]=1
      slot_of[$i]=$free
      status[$i]=running
      running=$((running + 1))
      jobs_start "$i" "$free"
    done
    # Emit, in the order added, every finished job whose predecessors were.
    while [[ $emitted -lt $n && "${status[$emitted]:-}" == done ]]; do
      [[ "${JOBS_RC[$emitted]}" -eq 0 ]] || failed=$((failed + 1))
      "${JOBS_EMIT:-jobs_emit_log}" "$emitted"
      emitted=$((emitted + 1))
    done
    if [[ $emitted -lt $n ]]; then sleep 1; fi
  done
  JOBS_FAILED=$failed
}
