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
// running at the same time, so a shared slot changes the verdict, and when the
// gates' work directory is not fenced off the root Go module.
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
dir="$TACPLUS_CROSS_TEST_CARGO_TARGET"
mkdir -p "$(dirname "$dir")"
mkdir "$dir.in-use" 2>/dev/null || { echo "leg $leg: $dir is in use by another leg"; exit 9; }
echo "leg $leg starts"
case "$leg" in slow) sleep 2 ;; *) sleep 1 ;; esac
echo "leg $leg ends"
rmdir "$dir.in-use"
case "$leg" in broken|multi-b) exit 3 ;; esac
exit 0
`

// runGates runs scripts/gates.sh on the fake gates with a cap and returns its
// output, exit status and wall time.
func runGates(t *testing.T, cap string) (string, int, time.Duration) {
	t.Helper()
	dir := t.TempDir()
	just := filepath.Join(dir, "just")
	if err := os.WriteFile(just, []byte(fakeJust), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "gates.sh", "alpha", "multi", "slow", "broken", "omega")
	cmd.Env = append(os.Environ(),
		"GATES_JUST="+just,
		"GATES_JOBS="+cap,
		"GATES_WORKDIR="+filepath.Join(dir, "work"),
		"TACPLUS_BIN=/bin/true",
	)
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

// The gates run concurrently (tacplus#300), but their verdict and log must
// not depend on how many ran at once or in what order they finished.
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
		if !strings.Contains(serial, want) {
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
	if a, b := gateTimes.ReplaceAllString(serial, "N"), gateTimes.ReplaceAllString(parallel, "N"); a != b {
		t.Errorf("the output depends on the cap\n--- GATES_JOBS=1:\n%s\n--- GATES_JOBS=4:\n%s", a, b)
	}
	if parallelTook >= serialTook {
		t.Errorf("GATES_JOBS=4 took %s, GATES_JOBS=1 %s: the gates did not run concurrently", parallelTook, serialTook)
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

// Two jobs that run at once never share a cargo directory, and a job's
// directory sits beside the runner's.
func TestUseSlotCargoTarget(t *testing.T) {
	lib, err := filepath.Abs("../cross-test/lib/cargo-target.sh")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "cargo")
	slot := func(n string) string {
		t.Helper()
		cmd := exec.Command("bash", "-c", `set -euo pipefail; ROOT_DIR=/nonexistent; source "$1"; JOBS_SLOT=$2 use_slot_cargo_target; echo "$CROSS_TEST_CARGO_TARGET $CARGO_TARGET_DIR"`, "bash", lib, n)
		cmd.Env = append(os.Environ(), "TACPLUS_CROSS_TEST_CARGO_TARGET="+base)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if got, want := slot("0"), base+"-slot0 "+base+"-slot0"; got != want {
		t.Errorf("slot 0: got %q, want %q", got, want)
	}
	if got, want := slot("3"), base+"-slot3 "+base+"-slot3"; got != want {
		t.Errorf("slot 3: got %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(base+"-slot3", "CACHEDIR.TAG")); err != nil {
		t.Errorf("the slot directory has no CACHEDIR.TAG, so cargo clean refuses it: %v", err)
	}
}
