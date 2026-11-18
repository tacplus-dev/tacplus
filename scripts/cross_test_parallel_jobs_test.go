// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// parallelJobs sources cross-test/lib/parallel-jobs.sh under the gates' shell
// options, defines body (a runner function and the like), runs script and
// returns its combined output and whether it exited zero.
func parallelJobs(t *testing.T, env []string, body, script string) (string, bool) {
	t.Helper()
	lib, err := filepath.Abs("../cross-test/lib/parallel-jobs.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail; source \"$1\"\n"+body+"\n"+script, "bash", lib)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); !ok {
			t.Fatal(err)
		}
	}
	return string(out), err == nil
}

// current-generation ran its languages one after another for 2h26m of a 2h55m
// gate run (tacplus#812). Its jobs run in a pool of workers: every job runs,
// never more at once than the pool holds, and the pool is used.
//
// Each job appends its start and its end to one event log, and the test counts
// the jobs running from it: an append of one short line is atomic, so the
// log's order is the events' order. No job looks at another's live state: a
// job listing the others' markers raced their removal (tacplus#852).
func TestParallelJobsRunsEveryJobBoundedByThePool(t *testing.T) {
	dir := t.TempDir()
	events := filepath.Join(dir, "events")
	runner := `job() {
  echo "start $2" >> "` + events + `"
  sleep 0.4
  echo "end $2" >> "` + events + `"
  echo "ran $2 on worker $1"
}`
	jobs := []string{"rust/base", "rust/redo", "go/base", "go/redo", "java/base", "java/redo", "go/c2sonly"}
	out, ok := parallelJobs(t, nil, runner, `parallel_jobs_run 3 "`+filepath.Join(dir, "logs")+`" job `+strings.Join(jobs, " "))
	if !ok {
		t.Fatalf("parallel_jobs_run failed:\n%s", out)
	}
	for _, j := range jobs {
		if !strings.Contains(out, "["+j+"] ran "+j+" on worker ") {
			t.Errorf("job %s did not run, or its output is not labelled with it:\n%s", j, out)
		}
	}
	log, err := os.ReadFile(events)
	if err != nil {
		t.Fatal(err)
	}
	started, ended := map[string]int{}, map[string]int{}
	running, most := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		event, job, _ := strings.Cut(line, " ")
		switch event {
		case "start":
			started[job]++
			running++
			most = max(most, running)
		case "end":
			if started[job] == ended[job] {
				t.Fatalf("job %s ended before it started:\n%s", job, log)
			}
			ended[job]++
			running--
		default:
			t.Fatalf("event log line %q is neither a start nor an end:\n%s", line, log)
		}
	}
	for _, j := range jobs {
		if started[j] != 1 || ended[j] != 1 {
			t.Errorf("job %s started %d times and ended %d times, not once each:\n%s", j, started[j], ended[j], log)
		}
	}
	if most > 3 {
		t.Errorf("%d jobs ran at once in a pool of 3", most)
	}
	if most < 2 {
		t.Errorf("the jobs ran one at a time in a pool of 3")
	}
}

// A job's output is printed whole, every line labelled with the job, so
// workers finishing together do not interleave it.
func TestParallelJobsPrintsEachJobsOutputWhole(t *testing.T) {
	runner := `job() {
  echo "first $2"
  sleep 0.2
  echo "second $2"
}`
	out, ok := parallelJobs(t, nil, runner, `parallel_jobs_run 4 "`+filepath.Join(t.TempDir(), "logs")+`" job a b c d`)
	if !ok {
		t.Fatalf("parallel_jobs_run failed:\n%s", out)
	}
	lines := strings.Split(out, "\n")
	for _, j := range []string{"a", "b", "c", "d"} {
		first := -1
		for i, l := range lines {
			if l == "["+j+"] first "+j {
				first = i
			}
		}
		if first < 0 || first+1 >= len(lines) || lines[first+1] != "["+j+"] second "+j {
			t.Errorf("job %s's output is not whole:\n%s", j, out)
		}
	}
}

// The issue's last criterion: the output still names which language failed.
// The run fails, the failing job's output says it failed, and the summary at
// the end names it; a job that passed is not named there.
func TestParallelJobsNamesTheJobThatFailed(t *testing.T) {
	runner := `job() {
  case "$2" in
    rust/*) echo "cargo check: error[E0425]"; exit 3 ;;
  esac
  echo "PASS $2"
}`
	out, ok := parallelJobs(t, nil, runner, `parallel_jobs_run 2 "`+filepath.Join(t.TempDir(), "logs")+`" job go/base rust/base`)
	if ok {
		t.Fatalf("parallel_jobs_run passed with a failing job:\n%s", out)
	}
	for _, want := range []string{"[rust/base] cargo check: error[E0425]", "==> [rust/base] FAILED (exit 3)", "failed: rust/base"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "failed: go/base") || strings.Contains(out, "failed: rust/base go/base") {
		t.Errorf("a job that passed is named as failed:\n%s", out)
	}
}

// The gate scripts rely on set -e: a command that fails inside a job fails the
// job, as it failed the serial run. Running a function as an if condition
// would silently turn errexit off inside it.
func TestParallelJobsKeepsErrexitInsideAJob(t *testing.T) {
	runner := `job() {
  false
  echo "kept going after a failed command"
}`
	out, ok := parallelJobs(t, nil, runner, `parallel_jobs_run 2 "`+filepath.Join(t.TempDir(), "logs")+`" job only`)
	if ok || strings.Contains(out, "kept going") {
		t.Fatalf("a job went on past a failing command:\n%s", out)
	}
}

// Once a job failed, no worker starts another: a red land answers sooner.
func TestParallelJobsStartsNothingAfterAFailure(t *testing.T) {
	runner := `job() {
  [[ "$2" != first ]] || exit 1
  echo "started $2"
}`
	out, ok := parallelJobs(t, nil, runner, `parallel_jobs_run 1 "`+filepath.Join(t.TempDir(), "logs")+`" job first second third`)
	if ok || strings.Contains(out, "started second") || strings.Contains(out, "started third") {
		t.Fatalf("jobs started after the first failure:\n%s", out)
	}
}

// The pool is bounded by the job's CPUs: the processors it may run on, and the
// container's CPU quota (cgroup v2 cpu.max) when that is lower.
func TestParallelJobsCPUsHonoursTheCgroupQuota(t *testing.T) {
	dir := t.TempDir()
	cpus := func(cpuMax string) int {
		t.Helper()
		file := filepath.Join(dir, "cpu.max")
		if err := os.WriteFile(file, []byte(cpuMax+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, ok := parallelJobs(t, []string{"PARALLEL_JOBS_CPU_MAX=" + file}, "", "parallel_jobs_cpus")
		if !ok {
			t.Fatalf("parallel_jobs_cpus failed:\n%s", out)
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			t.Fatalf("parallel_jobs_cpus printed %q", out)
		}
		return n
	}
	online := runtime.NumCPU()
	if got := cpus("max 100000"); got != online {
		t.Errorf("no quota: got %d CPUs, want %d", got, online)
	}
	if got := cpus("150000 100000"); got != min(online, 2) {
		t.Errorf("a quota of 1.5 CPUs: got %d, want %d", got, min(online, 2))
	}
	if got := cpus("50000 100000"); got != 1 {
		t.Errorf("a quota of half a CPU: got %d, want 1", got)
	}
}
