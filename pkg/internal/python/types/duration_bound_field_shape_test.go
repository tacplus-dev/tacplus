// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/duration_bound_field_shape_test.go): the duration/port
// bound family emitted `if msg.<f> < 0` for any field shape. Python does not
// fail to compile, it fails at request time — `"abc" < 0` and `[1, 2] < 0` both
// raise TypeError — so a misapplied annotation turned every call carrying that
// message into an unhandled crash inside the validator instead of an
// INVALID_ARGUMENT, and did it only in the python target.
func pyDurationBound(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: pyDurationBoundValue(annotation)},
	}
}

func pyDurationBoundValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_net_port":
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	case "assert_duration":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 60}
	}
}

var pyDurationBoundAnnotations = []string{"assert_net_port", "assert_duration", "assert_duration_min", "assert_duration_max"}

func TestPythonDurationBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range pyDurationBoundAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
		}{
			{name: "string", field: pyDurationBound("name", "string", annotation)},
			{name: "bytes", field: pyDurationBound("blob", "bytes", annotation)},
			{
				name: "repeated",
				field: func() model.Field {
					f := pyDurationBound("ports", "int32", annotation)
					f.IsRepeated = true
					return f
				}(),
			},
			{
				name: "map",
				field: func() model.Field {
					f := pyDurationBound("labels", "string", annotation)
					f.IsMap = true
					return f
				}(),
			},
			{
				name: "enum",
				field: func() model.Field {
					f := pyDurationBound("mode", "TlsMode", annotation)
					f.IsEnum = true
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
// render exactly as before.
func TestPythonDurationBoundKeepsSupportedShapes(t *testing.T) {
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
			pyDurationBound("port", "int32", "assert_net_port"),
			pyDurationBound("ttl_seconds", "int64", "assert_duration"),
			pyDurationBound("min_seconds", "int64", "assert_duration_min"),
			pyDurationBound("max_seconds", "int64", "assert_duration_max"),
			nonNegative,
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
		"if msg.port < 0 or msg.port > 65535:",
		"if msg.ttl_seconds <= 0:",
		"if msg.idle_seconds < 0:",
		"if msg.min_seconds < 60:",
		"if msg.max_seconds > 60:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in generated python validation:\n%s", want, out)
		}
	}
}
