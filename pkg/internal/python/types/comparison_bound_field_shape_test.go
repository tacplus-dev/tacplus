// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/comparison_bound_field_shape_test.go): the simple bound
// family emitted `if msg.<f> <= N` for any field shape. Python does not fail to
// compile, it fails at request time — `"abc" < 5` and `[1, 2] < 5` both raise
// TypeError — so a misapplied annotation turned every call carrying that
// message into an unhandled crash inside the validator instead of an
// INVALID_ARGUMENT, and did it only in the python target.
func pyBound(name, typ, annotation string, bound int64) model.Field {
	return model.Field{
		Name: name,
		Type: typ,
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeInt, IntValue: bound},
		},
	}
}

func TestPythonComparisonBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range []string{"assert_gt", "assert_ge", "assert_lt", "assert_le"} {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: pyBound("name", "string", annotation, 5)},
			{name: "bytes", field: pyBound("blob", "bytes", annotation, 5)},
			{
				name: "repeated",
				field: func() model.Field {
					f := pyBound("ports", "int32", annotation, 5)
					f.IsRepeated = true
					return f
				}(),
			},
			{
				name: "map",
				field: func() model.Field {
					f := pyBound("labels", "string", annotation, 5)
					f.IsMap = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				blocks, err := buildValidationBlocks(msg, nil)
				if err == nil {
					var rendered strings.Builder
					for _, block := range blocks {
						rendered.WriteString(block.Code)
					}
					t.Fatalf("%s on a %s field accepted; generated python raises TypeError at request time:\n%s", annotation, tc.name, rendered.String())
				}
				want := annotation + " annotation on field ConnConfig." + tc.field.Name + " requires a scalar numeric field"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

// The guard is a guard, not a removal: the shapes the family is documented for
// render exactly as before, float bounds included.
func TestPythonComparisonBoundKeepsSupportedShapes(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			pyBound("max_retries", "int32", "assert_ge", 0),
			pyBound("backoff_multiplier", "double", "assert_gt", 0),
		},
	}

	blocks, err := buildValidationBlocks(msg, nil)
	if err != nil {
		t.Fatalf("buildValidationBlocks: %v", err)
	}
	var rendered strings.Builder
	for _, block := range blocks {
		rendered.WriteString(block.Code)
	}
	out := rendered.String()

	for _, want := range []string{
		"if msg.max_retries < 0:",
		"if msg.backoff_multiplier <= 0:",
		`raise invalid_argument(f"field max_retries must be >= 0, got: {msg.max_retries}")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated python validation:\n%s", want, out)
		}
	}
}
