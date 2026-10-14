// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/duration_bound_field_shape_test.go): the duration/port
// bound family emitted `if self.<f> < 0` for any field shape, so assert_net_port
// on a string produced a comparison against a String and on a repeated field a
// comparison against a Vec<i32> — neither of which the Rust compiler accepts. A
// proto3-optional field is an Option<T> and needs unwrapping, the way
// generateRustNumericConstraintValidation already does.
func rustDurationBound(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: rustDurationBoundValue(annotation)},
	}
}

func rustDurationBoundValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_net_port":
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	case "assert_duration":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 60}
	}
}

var rustDurationBoundAnnotations = []string{"assert_net_port", "assert_duration", "assert_duration_min", "assert_duration_max"}

func TestRustDurationBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range rustDurationBoundAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: rustDurationBound("name", "string", annotation)},
			{name: "bytes", field: rustDurationBound("blob", "bytes", annotation)},
			{
				name: "repeated",
				field: func() model.Field {
					f := rustDurationBound("ports", "int32", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
			{
				name: "map",
				field: func() model.Field {
					f := rustDurationBound("labels", "string", annotation)
					f.IsMap = true
					return f
				}(),
			},
			{
				name: "enum",
				field: func() model.Field {
					f := rustDurationBound("mode", "TlsMode", annotation)
					f.IsEnum = true
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

func TestRustDurationBoundUnwrapsOptionalField(t *testing.T) {
	field := rustDurationBound("idle_timeout", "int64", "assert_duration_min")
	field.IsOptional = true

	msg := model.Message{Name: "ConnConfig", Fields: []model.Field{field}}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if let Some(val) = self.idle_timeout {",
		"if val < 60 {",
		`return Err(format!("field idle_timeout must be >= 60, got: {}", val));`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "if self.idle_timeout < 60 {") {
		t.Errorf("generated Rust compares an Option<i64> to an integer bound, which does not compile:\n%s", rendered)
	}
}

// The guard is a guard, not a removal: the shapes the family is documented for
// render byte-for-byte as before.
func TestRustDurationBoundKeepsSupportedShapes(t *testing.T) {
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
			rustDurationBound("port", "int32", "assert_net_port"),
			rustDurationBound("ttl_seconds", "int64", "assert_duration"),
			rustDurationBound("min_seconds", "int64", "assert_duration_min"),
			rustDurationBound("max_seconds", "int64", "assert_duration_max"),
			nonNegative,
		},
	}

	var b strings.Builder
	if err := generateMessageValidation(&b, msg, nil); err != nil {
		t.Fatalf("generateMessageValidation: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"// Validate port (assert_net_port)\nif self.port < 0 || self.port > 65535 {\nreturn Err(format!(\"field port must be a valid port number (0-65535), got: {}\", self.port));\n}\n",
		"// Validate ttl_seconds (assert_duration: POSITIVE)\nif self.ttl_seconds <= 0 {\n",
		"// Validate idle_seconds (assert_duration: NON_NEGATIVE)\nif self.idle_seconds < 0 {\n",
		"// Validate min_seconds (assert_duration_min: 60)\nif self.min_seconds < 60 {\n",
		"// Validate max_seconds (assert_duration_max: 60)\nif self.max_seconds > 60 {\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}
