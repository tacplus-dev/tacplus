// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/duration_bound_field_shape_test.go): the duration/port
// bound family emitted `if (msg.get<F>() < 0)` for any field shape. On a String
// that is a compile error, on an enum the getter returns the enum type rather
// than a number (unlike assert_gt, this family never had a getNumber() arm), and
// on a repeated or map field the accessor itself does not exist — the generated
// getters are `get<F>List()` / `get<F>Map()`.
func javaDurationBound(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: javaDurationBoundValue(annotation)},
	}
}

func javaDurationBoundValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_net_port":
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	case "assert_duration":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 60}
	}
}

var javaDurationBoundAnnotations = []string{"assert_net_port", "assert_duration", "assert_duration_min", "assert_duration_max"}

func TestJavaDurationBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range javaDurationBoundAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
			want  string
		}{
			{name: "string", field: javaDurationBound("name", "string", annotation), want: "name"},
			{name: "bytes", field: javaDurationBound("blob", "bytes", annotation), want: "blob"},
			{
				name: "repeated",
				field: func() model.Field {
					f := javaDurationBound("ports", "int32", annotation)
					f.IsRepeated = true
					return f
				}(),
				want: "ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := javaDurationBound("labels", "string", annotation)
					f.IsMap = true
					return f
				}(),
				want: "labels",
			},
			{
				name: "enum",
				field: func() model.Field {
					f := javaDurationBound("mode", "TlsMode", annotation)
					f.IsEnum = true
					return f
				}(),
				want: "mode",
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
// render exactly as before.
func TestJavaDurationBoundKeepsSupportedShapes(t *testing.T) {
	nonNegative := model.Field{
		Name: "idle_seconds",
		Type: "int64",
		Annotations: map[string]model.AnnotationValue{
			"assert_duration": {Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_NON_NEGATIVE"},
		},
	}

	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			javaDurationBound("port", "int32", "assert_net_port"),
			javaDurationBound("ttl_seconds", "int64", "assert_duration"),
			javaDurationBound("min_seconds", "int64", "assert_duration_min"),
			javaDurationBound("max_seconds", "int64", "assert_duration_max"),
			nonNegative,
		},
	}

	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if (msg.getPort() < 0 || msg.getPort() > 65535) {",
		"if (msg.getTtlSeconds() <= 0) {",
		"if (msg.getIdleSeconds() < 0) {",
		"if (msg.getMinSeconds() < 60L) {",
		"if (msg.getMaxSeconds() > 60L) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated Java validation:\n%s", want, rendered)
		}
	}
}
