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
// family emitted `if self.<f> <= N` for any field shape, so assert_ge on a
// string produced `if self.name < 5` against a String and on a repeated field
// `if self.ports < 5` against a Vec<i32> — neither of which the Rust compiler
// accepts. A proto3-optional field is an Option<T> and needs unwrapping, the
// way generateRustNumericConstraintValidation already does.
func rustBound(name, typ, annotation string, bound int64) model.Field {
	return model.Field{
		Name: name,
		Type: typ,
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeInt, IntValue: bound},
		},
	}
}

func TestRustComparisonBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range []string{"assert_gt", "assert_ge", "assert_lt", "assert_le"} {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: rustBound("name", "string", annotation, 5)},
			{name: "bytes", field: rustBound("blob", "bytes", annotation, 5)},
			{
				name: "repeated",
				field: func() model.Field {
					f := rustBound("ports", "int32", annotation, 5)
					f.IsRepeated = true
					return f
				}(),
			},
			{
				name: "map",
				field: func() model.Field {
					f := rustBound("labels", "string", annotation, 5)
					f.IsMap = true
					return f
				}(),
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateMessageValidation(&b, msg, nil)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Rust does not compile:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + tc.field.Name + " requires a scalar numeric field"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
			})
		}
	}
}

func TestRustComparisonBoundUnwrapsOptionalField(t *testing.T) {
	field := rustBound("idle_timeout", "int32", "assert_ge", 5)
	field.IsOptional = true

	msg := model.Message{Name: "ConnConfig", Fields: []model.Field{field}}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if let Some(val) = self.idle_timeout {",
		"if val < 5 {",
		`return Err(format!("field idle_timeout must be >= 5, got: {}", val));`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "if self.idle_timeout < 5 {") {
		t.Errorf("generated Rust compares an Option<i32> to an integer bound, which does not compile:\n%s", rendered)
	}
}

// The guard is a guard, not a removal: the shapes the family is documented for
// render exactly as before, float bounds included.
func TestRustComparisonBoundKeepsSupportedShapes(t *testing.T) {
	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			rustBound("max_retries", "int32", "assert_ge", 0),
			rustBound("backoff_multiplier", "double", "assert_gt", 0),
		},
	}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"// Validate max_retries (assert_ge: 0)\nif self.max_retries < 0 {",
		"// Validate backoff_multiplier (assert_gt: 0.0)\nif self.backoff_multiplier <= 0.0 {",
		`return Err(format!("field max_retries must be >= 0, got: {}", self.max_retries));`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}
