// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"
)

// (spec.assert_required) and the conditional family — (spec.required_if_true),
// (spec.required_if_false), (spec.required_if_eq) — ask the same question of a
// field: is it at its zero value? Each language therefore owns exactly ONE
// zero-check ladder, and both annotation families must route through it.
//
// They did not. The conditional family got a type-correct shared helper; the
// unconditional assert_required kept a hand-rolled ladder per language, and every
// one of them was wrong:
//
//   - go emitted `m.<F> <= 0` for numerics — against a REPEATED numeric that is a
//     slice compared with an integer, and `m.<F> == ""` for a proto3-optional
//     string is a *string; neither compiles. Enum and map fields emitted nothing.
//   - rust and java enforced it only when the declared type was literally
//     "string"; every other shape silently emitted no check.
//   - python covered "string" plus four integer types and nothing else.
//   - typescript tested `!m.<f>`, which is truthy for an empty array, an empty
//     object and an empty Uint8Array.
//
// Result: the same proto, the same annotation, five different meanings — and in
// three languages a required field that was never actually required. This test
// pins the single-ladder rule so a future arm cannot be hand-rolled back in.
func TestAssertRequiredSharesTheConditionalZeroCheckLadder(t *testing.T) {
	emitters := map[string]struct {
		path   string
		helper string
	}{
		"go":         {"../go/types/validation.go", "requiredZeroCheck(field, goFieldName)"},
		"java":       {"../java/types/validation.go", "javaRequiredZeroCheck(field, javaFieldName, getterName)"},
		"python":     {"../python/types/validation.go", "pyRequiredZeroCheck(field, pyFieldName)"},
		"rust":       {"../rust/types/validation.go", "rustRequiredZeroCheck(field, rustFieldName)"},
		"typescript": {"../typescript/types/validation.go", "tsRequiredZeroCheck(field, fieldName)"},
	}
	for lang, emitter := range emitters {
		src, err := os.ReadFile(emitter.path)
		if err != nil {
			t.Fatalf("%s: read %s: %v", lang, emitter.path, err)
		}
		text := string(src)
		idx := strings.Index(text, `case "assert_required":`)
		if idx < 0 {
			idx = strings.Index(text, `field.Annotations["assert_required"]`)
		}
		if idx < 0 {
			t.Errorf("%s validation emitter (%s) no longer handles assert_required", lang, emitter.path)
			continue
		}
		// The emitted check follows the case label directly; a hand-rolled ladder
		// would put a type switch here instead of the shared helper call.
		window := text[idx:min(idx+1200, len(text))]
		if !strings.Contains(window, emitter.helper) {
			t.Errorf("%s assert_required (%s) does not call %s; it has its own zero-value ladder again, "+
				"so assert_required and required_if_* can disagree about whether the same field is set",
				lang, emitter.path, emitter.helper)
		}
	}
}
