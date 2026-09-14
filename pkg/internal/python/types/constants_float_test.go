// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os/exec"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// TestDefaultFloatAnnotationLiteral guards against %g's exponential notation
// producing invalid Python literals such as "1e+06.0".
func TestDefaultFloatAnnotationLiteral(t *testing.T) {
	tests := map[float64]string{
		1000000.0: "1e+06",
		0.0000001: "1e-07",
		3.14:      "3.14",
		5.0:       "5.0",
		250000.5:  "250000.5",
	}

	python3, hasPython := exec.LookPath("python3")

	for input, want := range tests {
		gotType, got := defaultAnnotationToPython("default_float", model.AnnotationValue{FloatValue: input})
		if gotType != "float" {
			t.Fatalf("defaultAnnotationToPython(default_float, %v) type = %q, want %q", input, gotType, "float")
		}
		if got != want {
			t.Fatalf("defaultAnnotationToPython(default_float, %v) = %q, want %q", input, got, want)
		}

		// Confirm the literal is valid Python syntax. Skipped when no python3
		// binary is available; the string assertion above still applies.
		if hasPython != nil {
			continue
		}
		if out, err := exec.Command(python3, "-c", "import ast; ast.literal_eval('"+got+"')").CombinedOutput(); err != nil {
			t.Fatalf("literal %q is not valid Python: %v\n%s", got, err, out)
		}
	}
}
