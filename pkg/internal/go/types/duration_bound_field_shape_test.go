// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// spec.proto scopes assert_net_port to a port number and the assert_duration
// family to a duration count, but no emitter checked the field it was handed —
// the same gap batch 30 closed on assert_gt / assert_ge / assert_lt / assert_le,
// on the four annotations that sit directly above them in the same switch. The
// go emitter wrote the bare comparison for every shape, so a misapplied
// annotation produced generated Go that does not build: `if m.Name < 0` against
// a string, `if m.Ports < 0` against a []int32, `if m.Labels < 0` against a map.
// A proto3-optional field renders as a pointer, so the scalar form did not build
// there either.
func goDurationBound(name, typ, annotation string) model.Field {
	return model.Field{
		Name:        name,
		Type:        typ,
		Annotations: map[string]model.AnnotationValue{annotation: durationBoundValue(annotation)},
	}
}

func durationBoundValue(annotation string) model.AnnotationValue {
	switch annotation {
	case "assert_net_port":
		return model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	case "assert_duration":
		return model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: "DURATION_TYPE_POSITIVE"}
	default:
		return model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: 60}
	}
}

var durationBoundAnnotations = []string{"assert_net_port", "assert_duration", "assert_duration_min", "assert_duration_max"}

func TestGoDurationBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range durationBoundAnnotations {
		for _, tc := range []struct {
			name  string
			field model.Field
			bad   string
		}{
			{
				name:  "string",
				field: goDurationBound("name", "string", annotation),
				bad:   "m.Name",
			},
			{
				name:  "bytes",
				field: goDurationBound("blob", "bytes", annotation),
				bad:   "m.Blob",
			},
			{
				name: "repeated",
				field: func() model.Field {
					f := goDurationBound("ports", "int32", annotation)
					f.IsRepeated = true
					return f
				}(),
				bad: "m.Ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := goDurationBound("labels", "string", annotation)
					f.IsMap = true
					return f
				}(),
				bad: "m.Labels",
			},
			{
				name: "enum",
				field: func() model.Field {
					f := goDurationBound("mode", "TlsMode", annotation)
					f.IsEnum = true
					return f
				}(),
				bad: "m.Mode",
			},
		} {
			t.Run(annotation+"/"+tc.name, func(t *testing.T) {
				msg := model.Message{Name: "ConnConfig", Fields: []model.Field{tc.field}}

				var b strings.Builder
				err := generateValidationHelpers(&b, msg)
				if err == nil {
					t.Fatalf("%s on a %s field accepted; generated Go does not compile:\n%s", annotation, tc.name, b.String())
				}
				want := annotation + " annotation on field ConnConfig." + toGoFieldName(tc.field.Name) + " requires a scalar numeric field"
				if err.Error() != want {
					t.Errorf("error = %q, want %q", err.Error(), want)
				}
				if strings.Contains(b.String(), tc.bad+" <") || strings.Contains(b.String(), tc.bad+" >") {
					t.Errorf("generated Go still compares %s against an integer bound:\n%s", tc.bad, b.String())
				}
			})
		}
	}
}

// A proto3-optional numeric field is a legitimate target: the bound applies to
// the value when one is present. It has to be dereferenced behind a nil guard,
// the way assert_numeric already does, or the emitted `if m.X < 60` compares a
// *int64 to an untyped constant and the package does not build.
func TestGoDurationBoundDerefsOptionalField(t *testing.T) {
	field := goDurationBound("idle_timeout", "int64", "assert_duration_min")
	field.IsOptional = true

	msg := model.Message{Name: "ConnConfig", Fields: []model.Field{field}}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if m.IdleTimeout != nil {",
		"if *m.IdleTimeout < 60 {",
		`"field IdleTimeout must be >= 60, got: %d", *m.IdleTimeout`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "if m.IdleTimeout < 60 {") {
		t.Errorf("generated Go compares a *int64 to an integer bound, which does not compile:\n%s", rendered)
	}
}

// The guard is a guard, not a removal: every shape the family is documented for
// still renders byte-for-byte as before, including the DURATION_TYPE_NON_NEGATIVE
// arm and the int32 port shape pkg/proto/sys/host.proto already annotates.
func TestGoDurationBoundKeepsSupportedShapes(t *testing.T) {
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
			goDurationBound("port", "int32", "assert_net_port"),
			goDurationBound("ttl_seconds", "int64", "assert_duration"),
			goDurationBound("min_seconds", "int64", "assert_duration_min"),
			goDurationBound("max_seconds", "int64", "assert_duration_max"),
			nonNegative,
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"	// Validate Port (assert_net_port)\n	if m.Port < 0 || m.Port > 65535 {\n		return protoerrors.NewErrMissingInvalidData(nil, \"field Port must be a valid port number (0-65535), got: %d\", m.Port)\n	}\n",
		"	// Validate TtlSeconds (assert_duration: POSITIVE)\n	if m.TtlSeconds <= 0 {\n",
		"	// Validate IdleSeconds (assert_duration: NON_NEGATIVE)\n	if m.IdleSeconds < 0 {\n",
		"	// Validate MinSeconds (assert_duration_min: 60)\n	if m.MinSeconds < 60 {\n",
		"	// Validate MaxSeconds (assert_duration_max: 60)\n	if m.MaxSeconds > 60 {\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}
