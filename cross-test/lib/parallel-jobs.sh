#!/usr/bin/env bash
# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0
#
# A pool of workers for a gate's independent jobs (tacplus#812):
# current-generation ran its languages and fixtures one after another, 2h26m
# of a 2h55m gate run. Source this file and call
#
#   parallel_jobs_run SLOTS OUTDIR RUNNER JOB...
#
# It runs `RUNNER WORKER JOB` for every JOB, at most SLOTS at once, in the
# order given (put the longest first). WORKER (1..SLOTS) names the worker the
# job runs on, so a runner can give each worker a cache of its own. A job's
# output goes to OUTDIR/<job>.log and is printed whole when it ends, every line
# labelled [JOB], so two workers never interleave. Once a job fails no worker
# starts another; the ones running finish. It returns non-zero if any job
# failed, and its last line names them.
#
# flock(1) is not on macOS (scripts/with-tree-lock.sh): a worker claims a job
# and the output with mkdir, which is atomic.

# The CPUs this job may use: the processors it may run on, lowered to the
# cgroup v2 CPU quota (PARALLEL_JOBS_CPU_MAX, default /sys/fs/cgroup/cpu.max)
# when a container sets one. At least 1.
parallel_jobs_cpus() {
  local n quota period
  n="$(nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 1)"
  local cpu_max="${PARALLEL_JOBS_CPU_MAX:-/sys/fs/cgroup/cpu.max}"
  if [[ -r "$cpu_max" ]] && read -r quota period <"$cpu_max" && [[ "$quota" =~ ^[0-9]+$ && "$period" =~ ^[1-9][0-9]*$ ]]; then
    local limit=$(((quota + period - 1) / period))
    ((limit < n)) && n=$limit
  fi
  ((n >= 1)) || n=1
  echo "$n"
}

parallel_jobs_run() {
  local slots="$1" outdir="$2" runner="$3"
  shift 3
  local jobs=("$@")
  rm -rf "$outdir"
  mkdir -p "$outdir/claimed" "$outdir/failed"
  ((slots >= 1)) || slots=1
  ((slots <= ${#jobs[@]})) || slots=${#jobs[@]}

  local pids=() worker
  for ((worker = 1; worker <= slots; worker++)); do
    parallel_jobs_worker "$worker" "$outdir" "$runner" "${jobs[@]}" &
    pids+=("$!")
  done
  local pid
  for pid in "${pids[@]}"; do
    wait "$pid" || true
  done

  local failed=() job
  for job in "${jobs[@]}"; do
    [[ -e "$outdir/failed/$(parallel_jobs_file "$job")" ]] && failed+=("$job")
  done
  if ((${#failed[@]} > 0)); then
    echo "==> ${#failed[@]} of ${#jobs[@]} jobs failed: ${failed[*]}" >&2
    return 1
  fi
}

# A job's name as a file name: rust/base is rust-base.
parallel_jobs_file() {
  local name="$1"
  printf '%s\n' "${name//\//-}"
}

parallel_jobs_worker() {
  local worker="$1" outdir="$2" runner="$3"
  shift 3
  local job file rc start
  for job in "$@"; do
    file="$(parallel_jobs_file "$job")"
    [[ -z "$(ls -A "$outdir/failed")" ]] || return 0
    mkdir "$outdir/claimed/$file" 2>/dev/null || continue
    parallel_jobs_print "$outdir" echo "==> [$job] started on worker $worker"
    start=$SECONDS
    # In the background and waited for, not tested by an if or ||: either
    # turns set -e off inside the runner, and the gates rely on it.
    "$runner" "$worker" "$job" >"$outdir/$file.log" 2>&1 &
    rc=0
    wait "$!" || rc=$?
    if ((rc == 0)); then
      parallel_jobs_print "$outdir" parallel_jobs_report "$job" "$outdir/$file.log" "passed in $((SECONDS - start))s"
    else
      : >"$outdir/failed/$file"
      parallel_jobs_print "$outdir" parallel_jobs_report "$job" "$outdir/$file.log" "FAILED (exit $rc) after $((SECONDS - start))s"
    fi
  done
}

parallel_jobs_report() {
  local job="$1" log="$2" verdict="$3"
  sed "s|^|[$job] |" "$log"
  echo "==> [$job] $verdict"
}

# Runs a command holding the pool's output lock.
parallel_jobs_print() {
  local outdir="$1"
  shift
  until mkdir "$outdir/print.lock" 2>/dev/null; do
    sleep 0.05
  done
  "$@" || true
  rmdir "$outdir/print.lock"
}
