// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// fakeJust stands in for `just` under scripts/gates.sh. `multi` names two
// legs and nothing else, the way cert-stream does; every other gate has a
// body. A leg fails when it finds its cargo directory in use by another leg
// running at the same time, so a shared slot changes the verdict, when the
// gates' work directory is not fenced off the root Go module, and when it is
// not given its share of the CPUs for cargo and the go tool, or the cap for
// current-generation's own worker pool.
const fakeJust = `#!/usr/bin/env bash
case "$1" in
  protoc-check) echo "protoc-check: fake"; exit 0 ;;
  --show)
    case "$2" in
      multi) printf '# a gate of two legs\nmulti: multi-a multi-b\n' ;;
      *) printf '# a gate\n%s: generate\n    {{ lock }} run %s\n' "$2" "$2" ;;
    esac
    exit 0 ;;
  --no-deps) leg="$2" ;;
  *) echo "unexpected: $*"; exit 99 ;;
esac
[[ -f "$GATES_WORKDIR/go.mod" ]] || { echo "leg $leg: $GATES_WORKDIR is not fenced off the root Go module"; exit 8; }
[[ "$CARGO_BUILD_JOBS" =~ ^[0-9]+$ && "$GOFLAGS" == *-p=[0-9]* && "$GOFLAGS" == *-timeout=* ]] \
  || { echo "leg $leg: no CPU share (CARGO_BUILD_JOBS=$CARGO_BUILD_JOBS GOFLAGS=$GOFLAGS)"; exit 7; }
[[ "$TACPLUS_CURRENT_GENERATION_JOBS" =~ ^[1-9][0-9]*$ ]] \
  || { echo "leg $leg: current-generation's pool is not capped (TACPLUS_CURRENT_GENERATION_JOBS=$TACPLUS_CURRENT_GENERATION_JOBS)"; exit 6; }
dir="$TACPLUS_CROSS_TEST_CARGO_TARGET"
mkdir -p "$(dirname "$dir")"
mkdir "$dir.in-use" 2>/dev/null || { echo "leg $leg: $dir is in use by another leg"; exit 9; }
echo "leg $leg starts"
case "$leg" in
  slow) sleep 2 ;;
  # hang never ends by itself, and leaves a child that marks the work
  # directory if it outlives the leg.
  hang) ( sleep 4; touch "$GATES_WORKDIR/orphan" ) & sleep 30 ;;
  *) sleep 1 ;;
esac
echo "leg $leg ends"
rmdir "$dir.in-use"
case "$leg" in broken|multi-b) exit 3 ;; esac
exit 0
`

// runGates runs scripts/gates.sh on the fake gates with a cap and returns its
// output, exit status and wall time.
func runGates(t *testing.T, cap string) (string, int, time.Duration) {
	t.Helper()
	return runGatesOf(t, t.TempDir(), cap, nil, "alpha", "multi", "slow", "broken", "omega")
}

// runGatesOf runs scripts/gates.sh on the fake gates named, in dir, with a
// cap and the environment added.
func runGatesOf(t *testing.T, dir, cap string, env []string, gates ...string) (string, int, time.Duration) {
	t.Helper()
	just := filepath.Join(dir, "just")
	if err := os.WriteFile(just, []byte(fakeJust), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", append([]string{"gates.sh"}, gates...)...)
	cmd.Env = append(append(os.Environ(),
		"GATES_JUST="+just,
		"GATES_JOBS="+cap,
		"GATES_WORKDIR="+filepath.Join(dir, "work"),
		"TACPLUS_BIN=/bin/true",
	), env...)
	start := time.Now()
	out, err := cmd.CombinedOutput()
	took := time.Since(start)
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), rc, took
}

var gateTimes = regexp.MustCompile(` *\b\d+s\b|GATES_JOBS=\d+`)

// progressLines are the lines gates.sh prints as each gate ends, in the order
// they end (tacplus#893): the only ones that depend on the cap.
var progressLines = regexp.MustCompile(`(?m)^gates: .*\n`)

// The gates run concurrently (tacplus#300), but their verdict and log must
// not depend on how many ran at once or in what order they finished. Only
// the progress lines, printed as each gate ends, do.
func TestGatesVerdictDoesNotDependOnTheCap(t *testing.T) {
	serial, serialRC, serialTook := runGates(t, "1")
	parallel, parallelRC, parallelTook := runGates(t, "4")

	for _, want := range []string{
		"=== protoc-check rc=0 ===",
		"=== alpha ===\nleg alpha starts\nleg alpha ends\n=== alpha rc=0 ===",
		"=== multi ===\n--- multi-a ---\nleg multi-a starts\nleg multi-a ends\n--- multi-a rc=0 ---\n--- multi-b ---\nleg multi-b starts\nleg multi-b ends\n--- multi-b rc=3 ---\n=== multi rc=3 ===",
		"=== slow rc=0 ===",
		"=== broken rc=3 ===",
		"=== omega rc=0 ===",
	} {
		if !strings.Contains(progressLines.ReplaceAllString(serial, ""), want) {
			t.Errorf("GATES_JOBS=1 output lacks %q:\n%s", want, serial)
		}
	}
	order := regexp.MustCompile(`(?m)^=== ([a-z-]+) ===$`).FindAllStringSubmatch(serial, -1)
	var names []string
	for _, m := range order {
		names = append(names, m[1])
	}
	if got := strings.Join(names, " "); got != "protoc-check alpha multi slow broken omega" {
		t.Errorf("gates printed in the order %q, not the order named", got)
	}
	if !strings.HasSuffix(serial, "=== OVERALL fail=1 ===\n") || serialRC != 1 {
		t.Errorf("a failing gate must end with OVERALL fail=1 and exit 1; exit %d:\n%s", serialRC, serial)
	}
	if !regexp.MustCompile(`(?m)^  alpha +\d+s  pass$`).MatchString(serial) ||
		!regexp.MustCompile(`(?m)^  broken +\d+s  FAIL$`).MatchString(serial) ||
		!regexp.MustCompile(`(?m)^  total +\d+s$`).MatchString(serial) {
		t.Errorf("no wall-time table with each gate and the total:\n%s", serial)
	}

	if serialRC != parallelRC {
		t.Errorf("exit %d with GATES_JOBS=1, %d with GATES_JOBS=4", serialRC, parallelRC)
	}
	for _, want := range []string{"alpha", "multi", "slow", "broken", "omega"} {
		if !regexp.MustCompile(`(?m)^gates: ` + want + ` (rc=0 pass|rc=3 FAIL) in \d+s, \d of 5 done`).MatchString(parallel) {
			t.Errorf("no progress line for %s as it ended:\n%s", want, parallel)
		}
	}
	normal := func(out string) string {
		return gateTimes.ReplaceAllString(progressLines.ReplaceAllString(out, ""), "N")
	}
	if a, b := normal(serial), normal(parallel); a != b {
		t.Errorf("the output depends on the cap\n--- GATES_JOBS=1:\n%s\n--- GATES_JOBS=4:\n%s", a, b)
	}
	if parallelTook >= serialTook {
		t.Errorf("GATES_JOBS=4 took %s, GATES_JOBS=1 %s: the gates did not run concurrently", parallelTook, serialTook)
	}
}

// A gate's rc and wall time reach the trace as it ends, not once every gate
// named before it ended too, and a gate that hangs is stopped at
// GATES_TIMEOUT, with its children, and fails naming itself (tacplus#893): a
// hung gate held a build slot for hours with nothing in the trace.
func TestGatesReportEachGateAsItEndsAndStopAHungOne(t *testing.T) {
	dir := t.TempDir()
	out, rc, took := runGatesOf(t, dir, "2", []string{"GATES_TIMEOUT=3"}, "hang", "alpha")
	if rc != 1 || !strings.HasSuffix(out, "=== OVERALL fail=1 ===\n") {
		t.Errorf("a hung gate must fail the gates; exit %d:\n%s", rc, out)
	}
	if took > 20*time.Second {
		t.Errorf("the gates took %s: the hung gate was not stopped at GATES_TIMEOUT=3", took)
	}
	alphaDone := strings.Index(out, "gates: alpha rc=0 pass in ")
	hangDone := strings.Index(out, "gates: hang rc=124 FAIL in ")
	hangBlock := strings.Index(out, "=== hang ===")
	if alphaDone < 0 || hangDone < 0 || alphaDone > hangBlock || alphaDone > hangDone {
		t.Errorf("alpha's rc did not reach the trace as it ended, before the hung gate's:\n%s", out)
	}
	if !strings.Contains(out, "gates: hang timed out after 3s (GATES_TIMEOUT): stopped\n") ||
		!strings.Contains(out, "=== hang rc=124 ===") {
		t.Errorf("the hung gate does not fail naming its timeout:\n%s", out)
	}
	if !regexp.MustCompile(`gates: alpha rc=0 pass in \d+s, 1 of 2 done; running: hang\n`).MatchString(out) {
		t.Errorf("the progress line does not name the gates still running:\n%s", out)
	}
	time.Sleep(5*time.Second - took)
	if _, err := os.Stat(filepath.Join(dir, "work", "orphan")); err == nil {
		t.Errorf("the hung gate's child outlived its timeout")
	}
}

// The gate list stays in the justfile, where scripts/verify_test.go reads it.
func TestGatesRecipeRunsTheGatesScript(t *testing.T) {
	recipes := justRecipes(t, "../justfile")
	m := gateList.FindStringSubmatch(recipes["gates"])
	if m == nil {
		t.Fatalf("the gates recipe does not run scripts/gates.sh with its gate list:\n%s", recipes["gates"])
	}
	for _, gate := range strings.Fields(m[1]) {
		if _, ok := recipes[gate]; !ok {
			t.Errorf("the gates recipe names %s, which is not a recipe", gate)
		}
	}
}
