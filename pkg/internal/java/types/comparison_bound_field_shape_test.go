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
// family emitted `if (msg.get<F>() <= N)` for any field shape. On a String that
// is a compile error, and on a repeated or map field the accessor itself does
// not exist — the generated getters are `get<F>List()` / `get<F>Map()` — so a
// misapplied annotation produced Java that does not build.
func javaBound(name, typ, annotation string, bound int64) model.Field {
	return model.Field{
		Name: name,
		Type: typ,
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeInt, IntValue: bound},
		},
	}
}

func TestJavaComparisonBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range []string{"assert_gt", "assert_ge", "assert_lt", "assert_le"} {
		for _, tc := range []struct {
			name  string
			field model.Field
			want  string
		}{
			{name: "string", field: javaBound("name", "string", annotation, 5), want: "name"},
			{name: "bytes", field: javaBound("blob", "bytes", annotation, 5), want: "blob"},
			{
				name: "repeated",
				field: func() model.Field {
					f := javaBound("ports", "int32", annotation, 5)
					f.IsRepeated = true
					return f
				}(),
				want: "ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := javaBound("labels", "string", annotation, 5)
					f.IsMap = true
					return f
				}(),
				want: "labels",
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := writeMessageValidation(&b, msg, "Outer", nil)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Java does not compile:\n%s", annotation, tc.name, b.String())
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
// render exactly as before, including the enum arm java reads through
// getNumber().
func TestJavaComparisonBoundKeepsSupportedShapes(t *testing.T) {
	enumField := javaBound("mode", "TlsMode", "assert_le", 3)
	enumField.IsEnum = true

	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			javaBound("max_retries", "int32", "assert_ge", 0),
			javaBound("backoff_multiplier", "double", "assert_gt", 0),
			enumField,
		},
	}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if (msg.getMaxRetries() < 0) {",
		"if (msg.getBackoffMultiplier() <= 0) {",
		"if (msg.getMode().getNumber() > 3) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated Java validation:\n%s", want, rendered)
		}
	}
}
