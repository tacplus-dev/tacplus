// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// (spec.assert_element_max_size) is the per-element BYTE bound on a repeated or
// map string/bytes field. The emitters already enforce a 32,768-byte default on
// every such element; the annotation replaces that default with the declared
// bound, so the collection sizer may use a real number instead of assuming the
// unbounded worst case.
func elementBoundField(name, typ string, count, elemBound int64) model.Field {
	return model.Field{
		Name: name, Type: typ, IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_max_size":         {Type: model.AnnotationTypeInt, IntValue: count},
			"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: elemBound},
		},
	}
}

func TestGoElementMaxSizeBoundsEachElement(t *testing.T) {
	msg := model.Message{
		Name:   "ProviderInfo",
		Fields: []model.Field{elementBoundField("domains", "string", 32, 253)},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"// Validate Domains (assert_max_size: 32)",
		"if len(m.Domains) > 32 {",
		"// Validate Domains elements (assert_element_max_size: 253 bytes)",
		"for i, val := range m.Domains {",
		"if len(val) > 253 {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
	// The 32 KB default must be gone: it is exactly the unsound assumption the
	// annotation exists to replace.
	if strings.Contains(rendered, "32768") {
		t.Errorf("declared element bound did not replace the 32768-byte default:\n%s", rendered)
	}
}

func TestGoElementMaxSizeBoundsMapValuesNotKeys(t *testing.T) {
	msg := model.Message{
		Name: "Labels",
		Fields: []model.Field{{
			Name: "entries", Type: "string", IsMap: true,
			MapKeyType: "string", MapValueType: "string",
			Annotations: map[string]model.AnnotationValue{
				"assert_max_size":         {Type: model.AnnotationTypeInt, IntValue: 4},
				"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: 64},
			},
		}},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"// Validate Entries values (assert_element_max_size: 64 bytes)",
		"if len(val) > 64 {",
		"// Validate Entries keys (default max size: 32768 bytes)",
		"if len(key) > 32768 {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q in generated validation:\n%s", want, rendered)
		}
	}
}

func TestGoElementMaxSizeAbsentKeepsDefaultBound(t *testing.T) {
	msg := model.Message{
		Name: "Tags",
		Fields: []model.Field{{
			Name: "tags", Type: "string", IsRepeated: true,
			Annotations: map[string]model.AnnotationValue{
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
			},
		}},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	if want := "// Validate Tags elements (default max size: 32768 bytes)"; !strings.Contains(b.String(), want) {
		t.Errorf("missing %q in generated validation:\n%s", want, b.String())
	}
}

// A misapplied annotation must be a generation error, not a silent no-op: the
// declared constraint has no meaning on a singular field or on a non-string
// element type, and dropping it would leave the sizer trusting a bound nothing
// enforces.
func TestGoElementMaxSizeRejectsWrongFieldShape(t *testing.T) {
	cases := []struct {
		name  string
		field model.Field
	}{
		{"singular string", model.Field{Name: "name", Type: "string", Annotations: map[string]model.AnnotationValue{
			"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: 253},
		}}},
		{"repeated int32", elementBoundField("ports", "int32", 8, 253)},
		{"map with int64 values", model.Field{
			Name: "counters", Type: "int64", IsMap: true,
			MapKeyType: "string", MapValueType: "int64",
			Annotations: map[string]model.AnnotationValue{
				"assert_element_max_size": {Type: model.AnnotationTypeInt, IntValue: 253},
			},
		}},
		{"non-integer value", model.Field{Name: "domains", Type: "string", IsRepeated: true,
			Annotations: map[string]model.AnnotationValue{
				"assert_element_max_size": {Type: model.AnnotationTypeString, StringValue: "253"},
			}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			err := generateValidationHelpers(&b, model.Message{Name: "M", Fields: []model.Field{tc.field}})
			if err == nil {
				t.Fatalf("generateValidationHelpers accepted assert_element_max_size on a %s field; want a generation error", tc.name)
			}
			if !strings.Contains(err.Error(), "assert_element_max_size") {
				t.Fatalf("error does not name the annotation: %v", err)
			}
		})
	}
}
