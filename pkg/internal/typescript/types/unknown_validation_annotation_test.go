// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// go, java, python and rust all treat an unknown VALIDATION annotation as a hard
// error (OF-26d): the generator has no template for it, so the declared
// constraint would be dropped and the target would accept requests every other
// language rejects. TypeScript alone printed a stderr warning and carried on —
// nothing fails on a warning, and TS client-side request validation is a live
// surface.
func TestTypeScriptUnknownValidationAnnotationIsFatal(t *testing.T) {
	for _, annotation := range []string{"assert_not_a_real_constraint", "required_if_not_a_real_constraint"} {
		msg := model.Message{
			Name: "Widget",
			Fields: []model.Field{{
				Name: "name",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					annotation: {Type: model.AnnotationTypeString, StringValue: "x"},
				},
			}},
		}
		var b strings.Builder
		err := generateTypeScriptValidationFunction(&b, msg)
		if err == nil {
			t.Errorf("%s: expected a hard error, got nil (constraint silently dropped)", annotation)
			continue
		}
		if !strings.Contains(err.Error(), "unsupported validation annotation") {
			t.Errorf("%s: unexpected error text %q", annotation, err)
		}
	}
}

// A non-validation annotation must still fall through without failing the build:
// those are consumed by other generators, not by the validation emitter.
func TestTypeScriptNonValidationAnnotationIsNotFatal(t *testing.T) {
	msg := model.Message{
		Name: "Widget",
		Fields: []model.Field{{
			Name: "name",
			Type: "string",
			Annotations: map[string]model.AnnotationValue{
				"record_key": {Type: model.AnnotationTypeBool, BoolValue: true},
			},
		}},
	}
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatalf("non-validation annotation must not fail generation: %v", err)
	}
}
