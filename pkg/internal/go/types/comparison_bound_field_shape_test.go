// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// spec.proto scopes assert_gt / assert_ge / assert_lt / assert_le to scalar
// numeric fields ("These work with int32, int64, uint32, uint64 fields"), but
// no emitter checked the field it was handed. The go emitter wrote the bare
// comparison for every shape, so a misapplied annotation produced generated Go
// that does not build: `if m.Name < 5` against a string, `if m.Ports < 5`
// against a []int32, `if m.Labels < 5` against a map. A proto3-optional field
// renders as a pointer, so the scalar form did not build there either — the
// same defect batch 28 fixed for assert_numeric / assert_float, on the simple
// bound family those two annotations sit beside.
func goBound(name, typ, annotation string, bound int64) model.Field {
	return model.Field{
		Name: name,
		Type: typ,
		Annotations: map[string]model.AnnotationValue{
			annotation: {Type: model.AnnotationTypeInt, IntValue: bound},
		},
	}
}

func TestGoComparisonBoundRejectsNonScalarNumericField(t *testing.T) {
	for _, annotation := range []string{"assert_gt", "assert_ge", "assert_lt", "assert_le"} {
		for _, tc := range []struct {
			name  string
			field model.Field
			bad   string
		}{
			{
				name:  "string",
				field: goBound("name", "string", annotation, 5),
				bad:   "m.Name",
			},
			{
				name:  "bytes",
				field: goBound("blob", "bytes", annotation, 5),
				bad:   "m.Blob",
			},
			{
				name: "repeated",
				field: func() model.Field {
					f := goBound("ports", "int32", annotation, 5)
					f.IsRepeated = true
					return f
				}(),
				bad: "m.Ports",
			},
			{
				name: "map",
				field: func() model.Field {
					f := goBound("labels", "string", annotation, 5)
					f.IsMap = true
					return f
				}(),
				bad: "m.Labels",
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
// the way assert_numeric already does, or the emitted `if m.X < 5` compares a
// *int32 to an untyped constant and the package does not build.
func TestGoComparisonBoundDerefsOptionalField(t *testing.T) {
	field := goBound("idle_timeout", "int32", "assert_ge", 5)
	field.IsOptional = true

	msg := model.Message{Name: "ConnConfig", Fields: []model.Field{field}}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if m.IdleTimeout != nil {",
		"if *m.IdleTimeout < 5 {",
		`"field IdleTimeout must be >= 5, got: %v", *m.IdleTimeout`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "if m.IdleTimeout < 5 {") {
		t.Errorf("generated Go compares a *int32 to an integer bound, which does not compile:\n%s", rendered)
	}
}

// The guard is a guard, not a removal: every shape the family is documented for
// still renders exactly as before, including the double field
// pkg/proto/sys/httpclient.proto already annotates with assert_gt and the enum
// shape java renders through getNumber().
func TestGoComparisonBoundKeepsSupportedShapes(t *testing.T) {
	enumField := goBound("mode", "TlsMode", "assert_le", 3)
	enumField.IsEnum = true

	msg := model.Message{
		Name: "ConnConfig",
		Fields: []model.Field{
			goBound("max_retries", "int32", "assert_ge", 0),
			goBound("backoff_multiplier", "double", "assert_gt", 0),
			enumField,
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"	// Validate MaxRetries (assert_ge: 0)\n	if m.MaxRetries < 0 {",
		"	// Validate BackoffMultiplier (assert_gt: 0)\n	if m.BackoffMultiplier <= 0 {",
		"	// Validate Mode (assert_le: 3)\n	if m.Mode > 3 {",
		`return protoerrors.NewErrMissingInvalidData(nil, "field MaxRetries must be >= 0, got: %v", m.MaxRetries)`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}
