// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// assert_numeric / assert_float apply per ELEMENT on a repeated field and
// behind a nil guard on an optional one: java loops
// `for (int i = 0; i < msg.getXList().size(); i++)`, rust
// `for (i, val) in self.x.iter().enumerate()`, python
// `for i, val in enumerate(msg.x)`, TypeScript
// `for (let i = 0; i < m.x.length; i++)`, and all four bind the optional case
// under a has-field check. The go emitter was the sole outlier: both cases
// ignored field.IsRepeated and assert_numeric also ignored field.IsOptional,
// emitting the bare scalar comparison `if m.Scores <= 0` against a []int32 and
// `if m.OptCount < 0` against a *int32 — neither of which compiles, so any
// proto putting these annotations on a repeated or optional-int field produced
// a generated package that could not build.
func TestGoNumericValidatorsLoopOverRepeatedFields(t *testing.T) {
	msg := model.Message{
		Name: "Telemetry",
		Fields: []model.Field{
			goRepeatedNumeric("scores", "int32", &model.NumericConstraint{Gt: goInt64(0), Le: goInt64(100)}),
			goRepeatedFloat("ratios", "float", &model.FloatConstraint{Ge: goFloat64(0.5)}),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"// Validate Scores (assert_numeric)",
		"for i, val := range m.Scores {",
		"if val <= 0 {",
		"if val > 100 {",
		"// Validate Ratios (assert_float)",
		"for i, val := range m.Ratios {",
		"if val < 0.5 {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("repeated field: missing per-element check %q in generated validation:\n%s", want, rendered)
		}
	}

	// The exact pre-fix fragments: the comparison applied to the slice itself.
	for _, bad := range []string{
		"if m.Scores <= 0 {",
		"if m.Scores > 100 {",
		"if m.Ratios < 0.5 {",
	} {
		if strings.Contains(rendered, bad) {
			t.Errorf("generated Go compares a repeated field to a scalar bound (%q), which does not compile:\n%s",
				bad, rendered)
		}
	}

	// Per-element failures must name the offending index, matching the
	// assert_enum_defined repeated branch.
	for _, idx := range []string{
		`"field Scores[%d] must be > 0, got: %v", i, val`,
		`"field Ratios[%d] must be >= 0.5, got: %v", i, val`,
	} {
		if !strings.Contains(rendered, idx) {
			t.Errorf("repeated validation error message missing element index %q:\n%s", idx, rendered)
		}
	}
}

// assert_numeric on an optional field must dereference behind a nil guard, the
// shape assert_float already had.
func TestGoNumericValidatorGuardsOptionalFields(t *testing.T) {
	countField := goRepeatedNumeric("opt_count", "int32", &model.NumericConstraint{Ge: goInt64(1)})
	countField.IsRepeated = false
	countField.IsOptional = true

	msg := model.Message{Name: "Telemetry", Fields: []model.Field{countField}}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if m.OptCount != nil {",
		"if *m.OptCount < 1 {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("optional field: missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "if m.OptCount < 1 {") {
		t.Errorf("generated Go compares an optional field pointer to a scalar bound, which does not compile:\n%s", rendered)
	}
}

// The in/not_in membership helpers take int64/float64, so every call site must
// widen — otherwise a repeated int32 element (or any non-int64 scalar) is a
// type mismatch against the helper the same emitter generates.
func TestGoNumericMembershipHelpersReceiveWidenedValues(t *testing.T) {
	msg := model.Message{
		Name: "Telemetry",
		Fields: []model.Field{
			goRepeatedNumeric("tiers", "int32", &model.NumericConstraint{In: []int64{1, 2, 3}}),
			goRepeatedFloat("weights", "float", &model.FloatConstraint{NotIn: []float64{0}}),
		},
	}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if !isInTelemetryTiers(int64(val)) {",
		"func isInTelemetryTiers(val int64) bool {",
		"if isInFloatTelemetryWeightsNotIn(float64(val), 1e-09) {",
		"func isInFloatTelemetryWeightsNotIn(val float64, epsilon float64) bool {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing widened membership call %q in generated validation:\n%s", want, rendered)
		}
	}
}

// The scalar form must stay scalar — the fix is a branch, not a replacement.
func TestGoNumericValidatorsKeepScalarForm(t *testing.T) {
	scores := goRepeatedNumeric("score", "int32", &model.NumericConstraint{Gt: goInt64(0)})
	scores.IsRepeated = false
	ratio := goRepeatedFloat("ratio", "float", &model.FloatConstraint{Ge: goFloat64(0.5)})
	ratio.IsRepeated = false

	msg := model.Message{Name: "Telemetry", Fields: []model.Field{scores, ratio}}

	var b strings.Builder
	if err := generateValidationHelpers(&b, msg); err != nil {
		t.Fatalf("generateValidationHelpers: %v", err)
	}
	rendered := b.String()

	for _, want := range []string{
		"if m.Score <= 0 {",
		`"field Score must be > 0, got: %v", m.Score`,
		"if m.Ratio < 0.5 {",
		`"field Ratio must be >= 0.5, got: %v", m.Ratio`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("scalar validation missing %q in generated validation:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "range m.Score") || strings.Contains(rendered, "range m.Ratio") {
		t.Errorf("scalar field validated with a loop:\n%s", rendered)
	}
}

func goRepeatedNumeric(name, typ string, constraint *model.NumericConstraint) model.Field {
	return model.Field{
		Name:       name,
		Type:       typ,
		IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_numeric": {Type: model.AnnotationTypeNumericConstraint, NumericConstraint: constraint},
		},
	}
}

func goRepeatedFloat(name, typ string, constraint *model.FloatConstraint) model.Field {
	return model.Field{
		Name:       name,
		Type:       typ,
		IsRepeated: true,
		Annotations: map[string]model.AnnotationValue{
			"assert_float": {Type: model.AnnotationTypeFloatConstraint, FloatConstraint: constraint},
		},
	}
}

func goInt64(v int64) *int64 { return &v }

func goFloat64(v float64) *float64 { return &v }
