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

// Every comparison with NaN is false, so bounds that each refuse one side
// (`if v < lo`, `if v > hi`) let a NaN through all of them (#256). The
// generated assert_float checks must refuse NaN whenever a bound is set, for
// float and double alike, and still accept in-range values and order ±Inf
// against finite bounds. Executed with the real Go toolchain.
func TestGeneratedGoFloatBoundsRefuseNaN(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed; skipping the executable assert_float NaN check")
	}
	cases := []struct {
		name       string
		constraint *model.FloatConstraint
		want       string // NaN 0.5 2 -1 +Inf
	}{
		{"ge_le", &model.FloatConstraint{Ge: goFloat64(0), Le: goFloat64(1)}, "refuse accept refuse refuse refuse"},
		{"gt", &model.FloatConstraint{Gt: goFloat64(0)}, "refuse accept accept refuse accept"},
		{"lt", &model.FloatConstraint{Lt: goFloat64(1)}, "refuse accept refuse accept refuse"},
	}
	for _, typ := range []string{"float64", "float32"} {
		for _, tc := range cases {
			t.Run(typ+"/"+tc.name, func(t *testing.T) {
				var checks strings.Builder
				writeGoFloatConstraintChecks(&checks, "Floats", "Ratio", "v", "", tc.constraint, 1e-9,
					map[string]goFloatMembership{}, map[string]goFloatMembership{})
				program := `package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

var _ = math.IsNaN

func newErr(_ error, format string, args ...any) error { return errors.New(fmt.Sprintf(format, args...)) }

func check(v ` + typ + `) error {
` + strings.ReplaceAll(checks.String(), "protoerrors.NewErrMissingInvalidData", "newErr") + `return nil
}

func main() {
	var out []string
	for _, v := range []float64{math.NaN(), 0.5, 2, -1, math.Inf(1)} {
		if check(` + typ + `(v)) != nil {
			out = append(out, "refuse")
		} else {
			out = append(out, "accept")
		}
	}
	fmt.Print(strings.Join(out, " "))
}
`
				dir := t.TempDir()
				src := filepath.Join(dir, "main.go")
				if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
					t.Fatalf("write main.go: %v", err)
				}
				out, err := exec.Command("go", "run", src).CombinedOutput()
				if err != nil {
					t.Fatalf("go run on the generated checks failed: %v\n%s\n--- program ---\n%s", err, out, program)
				}
				if got := strings.TrimSpace(string(out)); got != tc.want {
					t.Fatalf("NaN 0.5 2 -1 +Inf: got %q, want %q\n--- checks ---\n%s", got, tc.want, checks.String())
				}
			})
		}
	}
}
