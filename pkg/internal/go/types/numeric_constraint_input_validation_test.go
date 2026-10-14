// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// assert_numeric / assert_float are only meaningful on a scalar integer /
// floating-point field, and multiple_of is a divisor. rust
// (generateRustNumericConstraintValidation), python
// (buildPyNumericConstraintValidation), typescript and java all reject a
// misapplied annotation at generation time with a clear error. The go emitter
// was the sole outlier: it emitted the comparison unconditionally, so
// `assert_numeric` on a string field produced `if m.Name <= 0` and
// `multiple_of: 0` produced `if m.Count % 0 != 0` — both of which the Go
// compiler rejects ("division by zero" for the latter), turning an authoring
// mistake in a .proto into a generated package that cannot build instead of an
// actionable generator error. multiple_of is a presence-tracked `optional
// int64` in spec.proto, so `multiple_of: 0` reaches the model as a set value.
func TestGoNumericConstraintRejectsZeroMultipleOf(t *testing.T) {
	zero := int64(0)
	field := goRepeatedNumeric("count", "int32", &model.NumericConstraint{MultipleOf: &zero})
	field.IsRepeated = false

	msg := model.Message{Name: "Telemetry", Fields: []model.Field{field}}

	var b strings.Builder
	err := generateValidationHelpers(&b, msg)
	if err == nil {
		t.Fatalf("multiple_of: 0 accepted; generated validation emits a divide-by-zero:\n%s", b.String())
	}
	const want = "assert_numeric multiple_of annotation on field Telemetry.Count must be non-zero"
	if err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if strings.Contains(b.String(), "% 0 != 0") {
		t.Errorf("generated Go contains a modulo by zero:\n%s", b.String())
	}
}

// A non-zero multiple_of stays accepted — the fix is a guard, not a removal.
func TestGoNumericConstraintKeepsNonZeroMultipleOf(t *testing.T) {
	field := goRepeatedNumeric("count", "int32", &model.NumericConstraint{MultipleOf: goInt64(8)})
	field.IsRepeated = false

	msg := model.Message{Name: "Telemetry", Fields: []model.Field{field}}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	for _, want := range []string{
		"if m.Count % 8 != 0 {",
		`"field Count must be a multiple of 8, got: %v", m.Count`,
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in generated validation:\n%s", want, b.String())
		}
	}
}

// assert_numeric on a non-integer or map field must fail generation rather than
// emit an integer comparison against a string / bytes / map value.
func TestGoNumericConstraintRejectsNonIntegerField(t *testing.T) {
	cases := []struct {
		name  string
		field model.Field
		want  string
	}{
		{
			name: "string",
			field: func() model.Field {
				f := goRepeatedNumeric("name", "string", &model.NumericConstraint{Gt: goInt64(0)})
				f.IsRepeated = false
				return f
			}(),
			want: "assert_numeric annotation on field Telemetry.Name requires an integer field",
		},
		{
			name: "float",
			field: func() model.Field {
				f := goRepeatedNumeric("ratio", "double", &model.NumericConstraint{Gt: goInt64(0)})
				f.IsRepeated = false
				return f
			}(),
			want: "assert_numeric annotation on field Telemetry.Ratio requires an integer field",
		},
		{
			name: "map",
			field: func() model.Field {
				f := goRepeatedNumeric("counts", "int32", &model.NumericConstraint{Gt: goInt64(0)})
				f.IsRepeated = false
				f.IsMap = true
				return f
			}(),
			want: "assert_numeric annotation on field Telemetry.Counts requires an integer field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			err := generateValidationHelpers(&b, model.Message{Name: "Telemetry", Fields: []model.Field{tc.field}})
			if err == nil {
				t.Fatalf("assert_numeric on a %s field accepted; generated validation:\n%s", tc.name, b.String())
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// assert_float on a non-float or map field must fail generation for the same
// reason.
func TestGoFloatConstraintRejectsNonFloatField(t *testing.T) {
	cases := []struct {
		name  string
		field model.Field
		want  string
	}{
		{
			name: "string",
			field: func() model.Field {
				f := goRepeatedFloat("name", "string", &model.FloatConstraint{Ge: goFloat64(0.5)})
				f.IsRepeated = false
				return f
			}(),
			want: "assert_float annotation on field Telemetry.Name requires a float or double field",
		},
		{
			name: "integer",
			field: func() model.Field {
				f := goRepeatedFloat("count", "int64", &model.FloatConstraint{Ge: goFloat64(0.5)})
				f.IsRepeated = false
				return f
			}(),
			want: "assert_float annotation on field Telemetry.Count requires a float or double field",
		},
		{
			name: "map",
			field: func() model.Field {
				f := goRepeatedFloat("ratios", "double", &model.FloatConstraint{Ge: goFloat64(0.5)})
				f.IsRepeated = false
				f.IsMap = true
				return f
			}(),
			want: "assert_float annotation on field Telemetry.Ratios requires a float or double field",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			err := generateValidationHelpers(&b, model.Message{Name: "Telemetry", Fields: []model.Field{tc.field}})
			if err == nil {
				t.Fatalf("assert_float on a %s field accepted; generated validation:\n%s", tc.name, b.String())
			}
			if err.Error() != tc.want {
				t.Errorf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}
