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
// Executed with the real JDK's source launcher.
func TestGeneratedJavaFloatBoundsRefuseNaN(t *testing.T) {
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not installed; skipping the executable assert_float NaN check")
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
			writeJavaFloatConstraintChecks(&checks, "ratio", "val", "", tc.constraint, 1e-9)
			program := `public class Main {
    static class ProtoException extends RuntimeException {
        static final int INVALID_ARGUMENT = 3;
        ProtoException(int code, String message) { super(message); }
    }

    static void check(double val) {
` + checks.String() + `    }

    public static void main(String[] args) {
        StringBuilder out = new StringBuilder();
        for (double v : new double[] {Double.NaN, 0.5, 2.0, -1.0, Double.POSITIVE_INFINITY}) {
            String verdict = "accept";
            try {
                check(v);
            } catch (ProtoException e) {
                verdict = "refuse";
            }
            out.append(out.length() == 0 ? "" : " ").append(verdict);
        }
        System.out.print(out);
    }
}
`
			dir := t.TempDir()
			src := filepath.Join(dir, "Main.java")
			if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
				t.Fatalf("write Main.java: %v", err)
			}
			out, err := exec.Command("java", src).Output()
			if err != nil {
				var stderr []byte
				if ee, ok := err.(*exec.ExitError); ok {
					stderr = ee.Stderr
				}
				t.Fatalf("java on the generated checks failed: %v\n%s\n--- program ---\n%s", err, stderr, program)
			}
			if got := strings.TrimSpace(string(out)); got != tc.want {
				t.Fatalf("NaN 0.5 2 -1 +Inf: got %q, want %q\n--- checks ---\n%s", got, tc.want, checks.String())
			}
		})
	}
}
