// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/comparison_bound_field_shape_test.go), and the worst of
// the five: TypeScript neither fails to compile nor throws. `m.name < 5` on a
// string and `m.ports < 5` on an array are legal JavaScript that coerce to a
// comparison nobody wrote, so a misapplied bound silently accepts everything —
// a fail-open divergence from the go/java/rust siblings generated from the same
// proto, and the exact shape batch 27 fixed for the repeated string asserts.
func tsBound(name, typ, annotation string, bound int64) model.Field {
	return model.Field{
		Name: name,
		Type: typ,
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeInt, IntValue: bound},
		},
	}
}

func TestTypeScriptComparisonBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range []string{"assert_gt", "assert_ge", "assert_lt", "assert_le"} {
		for _, tc := range []struct {
			name  string
			field model.Field
			want  string
		}{
			{name: "string", field: tsBound("name", "string", annotation, 5), want: "name"},
			{name: "bytes", field: tsBound("blob", "bytes", annotation, 5), want: "blob"},
			{
				name: "repeated",
				field: func() model.Field {
					f := tsBound("ports", "int32", annotation, 5)
					f.IsRepeated = true
					return f
				}(),
				want: "ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := tsBound("labels", "string", annotation, 5)
					f.IsMap = true
					return f
				}(),
				want: "labels",
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateTypeScriptValidationFunction(&b, msg)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated TypeScript silently accepts every value:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + tc.want + " requires a scalar numeric field"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

// The guard is a guard, not a removal: the shapes the family is documented for
// render exactly as before, float bounds included.
func TestTypeScriptComparisonBoundKeepsSupportedShapes(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			tsBound("max_retries", "int32", "assert_ge", 0),
			tsBound("backoff_multiplier", "double", "assert_gt", 0),
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if (m.maxRetries !== undefined && m.maxRetries < 0) {",
		"if (m.backoffMultiplier !== undefined && m.backoffMultiplier <= 0) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated TypeScript validation:\n%s", want, rendered)
		}
	}
}
