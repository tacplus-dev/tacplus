// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// TestGoValidationInHelpersAreSorted is a regression guard for the GEN-DET-1
// class (commit 6bae90cc): the in/not_in validation-helper emitters ranged
// their backing maps directly, so the generated top-level isIn*/isIn*NotIn
// functions — which gofmt never reorders — were emitted in nondeterministic Go
// map-iteration order, byte-diffing the helpers file across otherwise-identical
// runs. The cross-test determinism gate cannot catch this: no cross-test proto
// message carries >=2 in/not_in-constrained fields, so the maps only ever hold a
// single (deterministic) entry. This unit test exercises the >=2 case directly
// and asserts the helper functions are emitted in stable sorted-by-name order.
func TestGoValidationInHelpersAreSorted(t *testing.T) {
	msg := model.Message{
		Name: "SortNumeric",
		Fields: []model.Field{
			numericInField("zeta", []int64{1, 2}),
			numericInField("alpha", []int64{3, 4}),
			numericInField("mike", []int64{5}),
			numericNotInField("yankee", []int64{7, 8}),
			numericNotInField("bravo", []int64{9}),
		},
	}
	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	// isIn* helpers must appear in field-name-sorted order (Alpha < Mike < Zeta).
	assertSubstringsInOrder(t, rendered,
		"func isInSortNumericAlpha(val int64) bool {",
		"func isInSortNumericMike(val int64) bool {",
		"func isInSortNumericZeta(val int64) bool {",
	)
	// isIn*NotIn helpers likewise (Bravo < Yankee).
	assertSubstringsInOrder(t, rendered,
		"func isInSortNumericBravoNotIn(val int64) bool {",
		"func isInSortNumericYankeeNotIn(val int64) bool {",
	)
}

func numericInField(name string, in []int64) model.Field {
	return model.Field{
		Name: name,
		Type: "int64",
		Annotations: map[string]model.AnnotationValue{
			"assert_numeric": {
				Type:              model.AnnotationTypeNumericConstraint,
				NumericConstraint: &model.NumericConstraint{In: in},
			},
		},
	}
}

func numericNotInField(name string, notIn []int64) model.Field {
	return model.Field{
		Name: name,
		Type: "int64",
		Annotations: map[string]model.AnnotationValue{
			"assert_numeric": {
				Type:              model.AnnotationTypeNumericConstraint,
				NumericConstraint: &model.NumericConstraint{NotIn: notIn},
			},
		},
	}
}

// assertSubstringsInOrder fails if the substrings are not all present in
// rendered in the given relative order.
func assertSubstringsInOrder(t *testing.T, rendered string, subs ...string) {
	t.Helper()
	prev := -1
	for _, s := range subs {
		idx := strings.Index(rendered, s)
		if idx < 0 {
			t.Fatalf("rendered output missing %q:\n%s", s, rendered)
		}
		if idx <= prev {
			t.Fatalf("helper %q emitted out of sorted order (index %d <= previous %d):\n%s", s, idx, prev, rendered)
		}
		prev = idx
	}
}
