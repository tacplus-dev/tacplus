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
// (`if (val < lo)`, `if (val > hi)`) let a NaN through all of them (#256). The
// generated assert_float checks must refuse NaN whenever a bound is set, and
// still accept in-range values and order ±Inf against finite bounds.
// Executed with the real node (the checks are plain JavaScript).
func TestGeneratedTypeScriptFloatBoundsRefuseNaN(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; skipping the executable assert_float NaN check")
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
			writeTsFloatConstraintChecks(&checks, "ratio", "val", tc.constraint, 1e-9, "\t")
			program := `function newErrMissingInvalidData(_cause, message) { return new Error(message); }

function check(val) {
` + checks.String() + `}

const out = [NaN, 0.5, 2, -1, Infinity].map((v) => {
	try {
		check(v);
		return "accept";
	} catch (e) {
		return "refuse";
	}
});
process.stdout.write(out.join(" "));
`
			dir := t.TempDir()
			src := filepath.Join(dir, "check.js")
			if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
				t.Fatalf("write check.js: %v", err)
			}
			out, err := exec.Command("node", src).CombinedOutput()
			if err != nil {
				t.Fatalf("node on the generated checks failed: %v\n%s\n--- program ---\n%s", err, out, program)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("NaN 0.5 2 -1 +Inf: got %q, want %q\n--- checks ---\n%s", got, tc.want, checks.String())
			}
		})
	}
}
