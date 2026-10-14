// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// TestJavaValidationAnnotationOrderIsStable is a regression guard for the
// GEN-DET-1 class (commit 6bae90cc): writeMessageValidation ranged
// field.Annotations (a Go map) directly instead of via
// model.SortedAnnotationNames, so a field carrying >=2 emitting annotations
// produced its validation blocks in nondeterministic map-iteration order,
// byte-diffing the generated Java across otherwise-identical runs. Java is
// EXCLUDED from the cross-test determinism gate (protobuf toolchain skew), so
// this unit test is the only guard for the Java leg. A field with three
// numeric-bound annotations must emit their blocks in sorted annotation-name
// order: assert_ge < assert_gt < assert_le.
func TestJavaValidationAnnotationOrderIsStable(t *testing.T) {
	// The three bounds are intentionally redundant — the only point is to place
	// >=2 emitting annotations on one field so their emission order is exercised.
	msg := model.Message{
		Name: "BoundedValue",
		Fields: []model.Field{
			{
				Name: "level",
				Type: "int64",
				Annotations: map[string]model.AnnotationValue{
					"assert_ge": {Type: model.AnnotationTypeInt, IntValue: 1},
					"assert_gt": {Type: model.AnnotationTypeInt, IntValue: 0},
					"assert_le": {Type: model.AnnotationTypeInt, IntValue: 100},
				},
			},
		},
	}
	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "BoundedOuter", nil); err != nil {
		t.Fatalf("writeMessageValidation: %v", err)
	}
	rendered := b.String()

	assertJavaBlocksInOrder(t, rendered,
		"// Validate level (assert_ge: 1)",
		"// Validate level (assert_gt: 0)",
		"// Validate level (assert_le: 100)",
	)
}

// assertJavaBlocksInOrder fails if the substrings are not all present in
// rendered in the given relative order.
func assertJavaBlocksInOrder(t *testing.T, rendered string, subs ...string) {
	t.Helper()
	prev := -1
	for _, s := range subs {
		idx := strings.Index(rendered, s)
		if idx < 0 {
			t.Fatalf("rendered output missing %q:\n%s", s, rendered)
		}
		if idx <= prev {
			t.Fatalf("block %q emitted out of sorted order (index %d <= previous %d):\n%s", s, idx, prev, rendered)
		}
		prev = idx
	}
}
