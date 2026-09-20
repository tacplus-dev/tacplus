// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// deletedField is a well-formed (spec.deleted_flag) field: a non-repeated bool.
func deletedField(name string) FieldDescriptor {
	return FieldDescriptor{
		Name: name, Type: "bool", TypeKind: "scalar",
		Annotations: map[string]model.AnnotationValue{
			"deleted_flag": {Type: model.AnnotationTypeBool, BoolValue: true},
		},
	}
}

// ---------------------------------------------------------------------------
// Positive controls. Each is the paired green half of a red test below: without
// them a red test would still pass if the fixture failed for an unrelated
// reason.
// ---------------------------------------------------------------------------

// ZERO markers is valid. An append-only type has no soft-delete state, and every
// record proto in the fleet today declares no marker — so the annotation is
// additive and nothing is excluded from an unmarked record's queries.
func TestValidateDeletedFlagIgnoresUndeclaredRecord(t *testing.T) {
	rec := residencyRecord(clocks(3600, 0), "", epochField("store_epoch_ms"))
	if err := validateDeletedFlagAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("a record declaring no (spec.deleted_flag) field must generate: %v", err)
	}
}

// The shape the annotation exists for: one bool field carrying the row's
// soft-delete state.
func TestValidateDeletedFlagAcceptsOneMarkedField(t *testing.T) {
	rec := residencyRecord(clocks(3600, 2592000), "",
		epochField("store_epoch_ms"), deletedField("deleted"))
	if err := validateDeletedFlagAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("a record with one well-formed (spec.deleted_flag) field must generate: %v", err)
	}
}

// The marker is orthogonal to the residency clocks: soft-delete state is
// meaningful on its own, so a record that declares no residency block may still
// carry it. This is the rule the retired (spec.ejectionable) EJ-4 took the
// opposite side of, pinned so the coupling cannot come back by accident.
func TestValidateDeletedFlagAcceptsRecordWithoutResidency(t *testing.T) {
	rec := residencyRecord(nil, "", deletedField("deleted"))
	if err := validateDeletedFlagAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("(spec.deleted_flag) on a record with no residency block must generate: %v", err)
	}
}

// DeletedFlagField is the lookup the query/handler generators use. It must find
// the marked field, and answer "no marker" for a record that declares none.
func TestDeletedFlagFieldLooksUpTheMarkedField(t *testing.T) {
	marked := residencyRecord(nil, "", epochField("store_epoch_ms"), deletedField("deleted"))
	f, ok := DeletedFlagField(&marked.Messages[0])
	if !ok {
		t.Fatal("DeletedFlagField must find the one marked field")
	}
	if f.Name != "deleted" {
		t.Fatalf("DeletedFlagField returned %q, want deleted", f.Name)
	}

	unmarked := residencyRecord(nil, "", epochField("store_epoch_ms"))
	if _, ok := DeletedFlagField(&unmarked.Messages[0]); ok {
		t.Fatal("a record with no marker must report no deleted-flag field")
	}
}

// A malformed or duplicated marker never reaches generation (the validator
// refuses it), and if one somehow did, the lookup must answer "no marker" —
// which generates the pre-marker query plane rather than a wrong one.
func TestDeletedFlagFieldRefusesMalformedAndDuplicated(t *testing.T) {
	bad := deletedField("deleted")
	bad.Type = "string"
	rec := residencyRecord(nil, "", bad)
	if _, ok := DeletedFlagField(&rec.Messages[0]); ok {
		t.Fatal("a non-bool marker must not be handed to the generators as usable")
	}

	two := residencyRecord(nil, "", deletedField("deleted"), deletedField("removed"))
	if _, ok := DeletedFlagField(&two.Messages[0]); ok {
		t.Fatal("two markers must not be handed to the generators as usable")
	}
}

// ---------------------------------------------------------------------------
// Refusals.
// ---------------------------------------------------------------------------

// DF-1: only a stored record has a query plane, so only a record has rows for
// the marker to exclude from one.
func TestValidateDeletedFlagRefusesNonRecord(t *testing.T) {
	fd := &FileDescriptor{
		Path:    "services/tasks.proto",
		Package: "tasks",
		Messages: []MessageDescriptor{{
			Name:   "TaskRequest",
			Fields: []FieldDescriptor{deletedField("deleted")},
		}},
	}
	err := validateDeletedFlagAnnotations([]*FileDescriptor{recordStoreHost(), recordPlaneTrait("stratum"), fd})
	if err == nil {
		t.Fatal("(spec.deleted_flag) on a non-record must fail generation")
	}
	if !strings.Contains(err.Error(), "is not a (spec.record)") {
		t.Fatalf("refusal must say the message is not a record, got: %v", err)
	}
}

// DF-2: the soft-delete state is two-valued. A string is not.
func TestValidateDeletedFlagRefusesWrongType(t *testing.T) {
	bad := deletedField("deleted")
	bad.Type = "string"
	rec := residencyRecord(clocks(3600, 0), "", epochField("store_epoch_ms"), bad)
	err := validateDeletedFlagAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("(spec.deleted_flag) on a string field must fail generation")
	}
	if !strings.Contains(err.Error(), "not bool") {
		t.Fatalf("refusal must name the required type, got: %v", err)
	}
}

// DF-2: a row has one soft-delete state, so a repeated marker is not one.
func TestValidateDeletedFlagRefusesRepeated(t *testing.T) {
	f := deletedField("deleted")
	f.IsRepeated = true
	rec := residencyRecord(clocks(3600, 0), "", epochField("store_epoch_ms"), f)
	err := validateDeletedFlagAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("a repeated (spec.deleted_flag) field must fail generation")
	}
	if !strings.Contains(err.Error(), "is repeated") {
		t.Fatalf("refusal must say the field is repeated, got: %v", err)
	}
}

// DF-3: which of two markers the query plane excludes on is not decidable here —
// the same shape residency VAL-3 takes for two (spec.store_epoch) fields.
func TestValidateDeletedFlagRefusesTwoFields(t *testing.T) {
	rec := residencyRecord(clocks(3600, 0), "",
		epochField("store_epoch_ms"), deletedField("deleted"), deletedField("removed"))
	err := validateDeletedFlagAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("two (spec.deleted_flag) fields must fail generation")
	}
	if !strings.Contains(err.Error(), "declares 2 (spec.deleted_flag) fields") {
		t.Fatalf("refusal must count the fields, got: %v", err)
	}
}
