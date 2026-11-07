// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// `tacplus verify` runs OpenJML ESC over the generated Helpers.java (#257), so
// all of it must typecheck under OpenJML, not only javac. OpenJML 21-0.23
// rejects a method reference or lambda that returns a primitive into a
// Stream.map (it cannot infer R by boxing): `.map(Integer::parseInt)` fails
// where `.map(Integer::valueOf)`, the same parse already boxed, passes.
func TestJavaRepeatedFromEnvVarMapsToBoxedValues(t *testing.T) {
	cases := map[string]string{
		"int32":    ".map(Integer::valueOf)",
		"sfixed32": ".map(Integer::valueOf)",
		"int64":    ".map(Long::valueOf)",
		"uint32":   ".map(s -> Integer.valueOf(Integer.parseUnsignedInt(s)))",
		"fixed64":  ".map(s -> Long.valueOf(Long.parseUnsignedLong(s)))",
		"float":    ".map(Float::valueOf)",
		"double":   ".map(Double::valueOf)",
		"bool":     ".map(Boolean::valueOf)",
	}
	for typ, want := range cases {
		var b strings.Builder
		writeRepeatedScalarFromEnvVar(&b, model.Field{Name: "values", Type: typ, IsRepeated: true}, "Values", "PROTO_VALUES", "")
		got := b.String()
		if !strings.Contains(got, want) {
			t.Errorf("%s: want %q in:\n%s", typ, want, got)
		}
		for _, primitive := range []string{"::parseInt)", "::parseLong)", "::parseUnsignedInt)", "::parseUnsignedLong)", "::parseFloat)", "::parseDouble)", "::parseBoolean)"} {
			if strings.Contains(got, primitive) {
				t.Errorf("%s: %q maps to a primitive, which OpenJML cannot typecheck:\n%s", typ, primitive, got)
			}
		}
	}
}

// writeJmlSpecs' clauses must typecheck against protoc's Java API and be
// provable from the validator they sit on: no String.size(), no
// zero-argument getter of a repeated field, and the spec on the Depth method
// ESC proves (it does not inline a callee), not only on its public wrapper.
func TestJavaJmlSpecsTypecheckAndSitOnTheDepthMethod(t *testing.T) {
	msg := model.Message{
		Name: "Sizes",
		Fields: []model.Field{
			{Name: "name", Type: "string", Annotations: map[string]model.AnnotationValue{
				"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
			}},
			{Name: "blob", Type: "bytes", Annotations: map[string]model.AnnotationValue{
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 16},
			}},
			{Name: "tags", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 3},
			}},
			{Name: "labels", Type: "map", IsMap: true, IsRepeated: true, MapKeyType: "string", MapValueType: "string", Annotations: map[string]model.AnnotationValue{
				"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 2},
			}},
			{Name: "port", Type: "int32", Annotations: map[string]model.AnnotationValue{
				"assert_net_port": {Type: model.AnnotationTypeBool, BoolValue: true},
			}},
		},
	}
	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	got := b.String()
	for _, want := range []string{
		"//@ ensures !msg.getName().isEmpty();",
		"//@ ensures msg.getBlob().size() <= 16;",
		"//@ ensures msg.getTagsCount() <= 3;",
		"//@ ensures msg.getLabelsMap().size() <= 2;",
		"//@ ensures msg.getPort() >= 0 && msg.getPort() <= 65535;\npublic static void validateSizesDepth(",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"getName().size()", "getTags().size()"} {
		if strings.Contains(got, bad) {
			t.Errorf("ill-typed spec %q in:\n%s", bad, got)
		}
	}
}

// assert_numeric is specified too, in the comparison the validator makes; its
// in/not_in check is a disjunction ESC reads, not a stream over a lambda, which
// ESC treats as a call that can do anything.
func TestJavaJmlSpecsCoverAssertNumeric(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	numeric := func(c model.NumericConstraint) map[string]model.AnnotationValue {
		return map[string]model.AnnotationValue{"assert_numeric": {Type: model.AnnotationTypeNumericConstraint, NumericConstraint: &c}}
	}
	msg := model.Message{
		Name: "Bounds",
		Fields: []model.Field{
			{Name: "window", Type: "int32", Annotations: numeric(model.NumericConstraint{Ge: i(1), Le: i(64)})},
			{Name: "not_seven", Type: "int64", Annotations: numeric(model.NumericConstraint{Ne: i(7)})},
			{Name: "one_of", Type: "int32", Annotations: numeric(model.NumericConstraint{In: []int64{1, 2, 4}})},
			{Name: "none_of", Type: "int64", Annotations: numeric(model.NumericConstraint{NotIn: []int64{3, 5}})},
			{Name: "even", Type: "uint32", Annotations: numeric(model.NumericConstraint{MultipleOf: i(2)})},
			{Name: "big", Type: "uint64", Annotations: numeric(model.NumericConstraint{Lt: i(10)})},
			{Name: "mode", Type: "Mode", IsEnum: true, Annotations: map[string]model.AnnotationValue{
				"assert_enum_defined": {Type: model.AnnotationTypeBool, BoolValue: true},
			}},
		},
	}
	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	got := b.String()
	for _, want := range []string{
		"//@ ensures msg.getWindow() >= 1 && msg.getWindow() <= 64;",
		"//@ ensures msg.getNotSeven() != 7L;",
		"//@ ensures msg.getOneOf() == 1 || msg.getOneOf() == 2 || msg.getOneOf() == 4;",
		"//@ ensures msg.getNoneOf() != 3L && msg.getNoneOf() != 5L;",
		"//@ ensures Integer.toUnsignedLong(msg.getEven()) % 2L == 0L;",
		"//@ ensures Long.compareUnsigned(msg.getBig(), 10L) < 0;",
		"//@ ensures msg.getModeValue() != 0;",
		"if (!(val == 1L || val == 2L || val == 4L)) {",
		"if (val == 3L || val == 5L) {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "anyMatch(candidate -> val == candidate)") {
		t.Errorf("integer in/not_in still streams over a lambda:\n%s", got)
	}
}
