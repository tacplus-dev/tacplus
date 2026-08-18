// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"
)

// The conditional-requirement family — (spec.required_if_true),
// (spec.required_if_false), (spec.required_if_eq) — is a field-level VALIDATION
// constraint: the descriptor parser lifts all three into field.Annotations
// (pkg/internal/common/parser/descriptor.go), and every language's validation
// emitter is expected to enforce them. Java shipped with none of them
// implemented, and the two mechanisms that should have caught that both keyed on
// the `assert_` prefix:
//
//   - each emitter's default case, which hard-errors on an unknown validation
//     annotation so a declared constraint is never silently dropped (OF-26d);
//   - cmd/soundness's constraint × language parity matrix, whose spec.proto scan
//     only collected `assert_*` names.
//
// This test pins both halves of the prefix widening. The parity matrix itself is
// exercised by the soundness gate, which reports 4/5 PARTIAL for all three rows
// when the Java emitter is reverted.
func TestValidationEmittersRejectUnknownConditionalRequirement(t *testing.T) {
	emitters := map[string]string{
		"go":         "../go/types/validation.go",
		"java":       "../java/types/validation.go",
		"python":     "../python/types/validation.go",
		"rust":       "../rust/types/validation.go",
		"typescript": "../typescript/types/validation.go",
	}
	for lang, path := range emitters {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read %s: %v", lang, path, err)
		}
		if !strings.Contains(string(src), `strings.HasPrefix(annotationName, "required_if_")`) {
			t.Errorf("%s validation emitter (%s) does not hard-error on an unknown required_if_* annotation; "+
				"a conditional requirement added to spec.proto would be silently dropped for this language", lang, path)
		}
	}
}

// The soundness constraint scan must see the conditional-requirement family, or
// a language missing all three reads as fully green in the parity matrix (which
// is exactly how the Java gap survived).
func TestSoundnessConstraintScanCoversConditionalRequirement(t *testing.T) {
	src, err := os.ReadFile("../../../cmd/soundness/main.go")
	if err != nil {
		t.Fatalf("read soundness main.go: %v", err)
	}
	if !strings.Contains(string(src), "(?:assert|required_if)_[a-z0-9_]+") {
		t.Error("cmd/soundness parseConstraints no longer scans spec.proto for the required_if_* family; " +
			"the constraint × language matrix would stop reporting conditional-requirement parity gaps")
	}
}
