// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// Same defect as the go emitter's (see
// pkg/internal/go/types/duration_bound_field_shape_test.go), and the worst of
// the five: TypeScript neither fails to compile nor throws. `m.name < 0` on a
// string and `m.ports > 65535` on an array are legal JavaScript that coerce to a
// comparison nobody wrote, so a misapplied port or duration bound silently
// accepts everything — a fail-open divergence from the go/java/rust siblings
// generated from the same proto.
func tsDurationBound(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: tsDurationBoundValue(annotation)},
	}
}

func tsDurationBoundValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_net_port":
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	case "assert_duration":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 60}
	}
}

var tsDurationBoundAnnotations = []string{"assert_net_port", "assert_duration", "assert_duration_min", "assert_duration_max"}

func TestTypeScriptDurationBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range tsDurationBoundAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
			want  string
		}{
			{name: "string", field: tsDurationBound("name", "string", annotation), want: "name"},
			{name: "bytes", field: tsDurationBound("blob", "bytes", annotation), want: "blob"},
			{
				name: "repeated",
				field: func() model.Field {
					f := tsDurationBound("ports", "int32", annotation)
					f.IsRepeated = true
					return f
				}(),
				want: "ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := tsDurationBound("labels", "string", annotation)
					f.IsMap = true
					return f
				}(),
				want: "labels",
			},
			{
				name: "enum",
				field: func() model.Field {
					f := tsDurationBound("mode", "TlsMode", annotation)
					f.IsEnum = true
					return f
				}(),
				want: "mode",
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
// render exactly as before.
func TestTypeScriptDurationBoundKeepsSupportedShapes(t *testing.T) {
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
			tsDurationBound("port", "int32", "assert_net_port"),
			tsDurationBound("ttl_seconds", "int64", "assert_duration"),
			tsDurationBound("min_seconds", "int64", "assert_duration_min"),
			tsDurationBound("max_seconds", "int64", "assert_duration_max"),
			nonNegative,
		},
	}

	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("generateTypeScriptValidationFunction: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if (m.port !== undefined && (m.port < 0 || m.port > 65535)) {",
		"if (m.ttlSeconds !== undefined && m.ttlSeconds <= 0) {",
		"if (m.idleSeconds !== undefined && m.idleSeconds < 0) {",
		"if (m.minSeconds !== undefined && m.minSeconds < 60) {",
		"if (m.maxSeconds !== undefined && m.maxSeconds > 60) {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated TypeScript validation:\n%s", want, rendered)
		}
	}
}
