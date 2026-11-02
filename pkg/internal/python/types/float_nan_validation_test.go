// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func floatBound(v float64) *float64 { return &v }

// Every comparison with NaN is false, so bounds that each refuse one side
// (`if val < lo`, `if val > hi`) let a NaN through all of them (#256). The
// generated assert_float checks must refuse NaN whenever a bound is set, and
// still accept in-range values and order ±Inf against finite bounds.
// Executed with the real python3.
func TestGeneratedPythonFloatBoundsRefuseNaN(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed; skipping the executable assert_float NaN check")
	}
	cases := []struct {
		name       string
		constraint *model.FloatConstraint
		want       string // NaN 0.5 2 -1 +Inf
	}{
		{"ge_le", &model.FloatConstraint{Ge: floatBound(0), Le: floatBound(1)}, "refuse accept refuse refuse refuse"},
		{"gt", &model.FloatConstraint{Gt: floatBound(0)}, "refuse accept accept refuse accept"},
		{"lt", &model.FloatConstraint{Lt: floatBound(1)}, "refuse accept refuse accept refuse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var checks strings.Builder
			writePyFloatConstraintChecks(&checks, "ratio", "val", tc.constraint, 1e-9, 1)
			program := `def invalid_argument(message):
    return ValueError(message)


def check(val):
` + checks.String() + `

out = []
for v in [float("nan"), 0.5, 2.0, -1.0, float("inf")]:
    try:
        check(v)
        out.append("accept")
    except ValueError:
        out.append("refuse")
print(" ".join(out), end="")
`
			dir := t.TempDir()
			src := filepath.Join(dir, "check.py")
			if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
				t.Fatalf("write check.py: %v", err)
			}
			out, err := exec.Command("python3", "-I", src).CombinedOutput()
			if err != nil {
				t.Fatalf("python3 on the generated checks failed: %v\n%s\n--- program ---\n%s", err, out, program)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("NaN 0.5 2 -1 +Inf: got %q, want %q\n--- checks ---\n%s", got, tc.want, checks.String())
			}
		})
	}
}
