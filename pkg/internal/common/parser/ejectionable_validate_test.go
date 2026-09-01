// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ejectionField is a well-formed (spec.ejectionable) field: a non-repeated
// spec.Ejectionable enum.
func ejectionField(name string) FieldDescriptor {
	return FieldDescriptor{
		Name: name, Type: "spec.Ejectionable", TypeKind: "enum",
		Annotations: map[string]model.AnnotationValue{
			"ejectionable": {Type: model.AnnotationTypeBool, BoolValue: true},
		},
	}
}

// ---------------------------------------------------------------------------
// Positive controls. Each is the paired green half of a red test below: without
// them a red test would still pass if the fixture failed for an unrelated
// reason.
// ---------------------------------------------------------------------------

// Every record proto in the fleet today declares no ejectionable field. Unset
// must keep meaning exactly what it means now, or adding the marker changes the
// meaning of every existing record.
func TestValidateEjectionableIgnoresUndeclaredRecord(t *testing.T) {
	rec := residencyRecord(warm(0), "", nil, epochField("store_epoch_ms"))
	if err := validateEjectionableAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("a record declaring no (spec.ejectionable) field must generate: %v", err)
	}
}

// The shape the annotation exists for: a warm record whose rows each carry
// their own answer to whether the window applies to them.
func TestValidateEjectionableAcceptsWarmRecord(t *testing.T) {
	rec := residencyRecord(warm(3600), "", nil, epochField("store_epoch_ms"), ejectionField("ejectionable"))
	if err := validateEjectionableAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("a warm record with one well-formed (spec.ejectionable) field must generate: %v", err)
	}
}

// The gate is orthogonal to the window preset, not a warm-only knob: a HOT
// record may declare it (every row simply never evicts, which is what HOT
// already means), exactly as mutability is legal on either preset.
func TestValidateEjectionableAcceptsHotRecord(t *testing.T) {
	rec := residencyRecord(&model.ResidencyPolicyEntry{Window: "RESIDENCY_HOT"}, "", nil, ejectionField("ejectionable"))
	if err := validateEjectionableAnnotations(withPlane(rec)); err != nil {
		t.Fatalf("RESIDENCY_HOT with an (spec.ejectionable) field must generate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Refusals.
// ---------------------------------------------------------------------------

// EJ-1: only a stored record has a residency window, so only a record has an
// eviction check for the gate to gate.
func TestValidateEjectionableRefusesNonRecord(t *testing.T) {
	fd := &FileDescriptor{
		Path:    "services/tasks.proto",
		Package: "tasks",
		Messages: []MessageDescriptor{{
			Name:   "TaskRequest",
			Fields: []FieldDescriptor{ejectionField("ejectionable")},
		}},
	}
	err := validateEjectionableAnnotations([]*FileDescriptor{recordStoreHost(), recordPlaneTrait("stratum"), fd})
	if err == nil {
		t.Fatal("(spec.ejectionable) on a non-record must fail generation")
	}
	if !strings.Contains(err.Error(), "is not a (spec.record)") {
		t.Fatalf("refusal must say the message is not a record, got: %v", err)
	}
}

// EJ-2: the gate is read as a tri-state enum. A bool has no
// EJECTIONABLE_UNSPECIFIED to carry the never-evict default.
func TestValidateEjectionableRefusesWrongType(t *testing.T) {
	bad := FieldDescriptor{
		Name: "ejectionable", Type: "bool", TypeKind: "scalar",
		Annotations: map[string]model.AnnotationValue{
			"ejectionable": {Type: model.AnnotationTypeBool, BoolValue: true},
		},
	}
	rec := residencyRecord(warm(0), "", nil, epochField("store_epoch_ms"), bad)
	err := validateEjectionableAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("(spec.ejectionable) on a bool field must fail generation")
	}
	if !strings.Contains(err.Error(), "not spec.Ejectionable") {
		t.Fatalf("refusal must name the required type, got: %v", err)
	}
}

// EJ-2: a row has one answer, so a repeated gate is not one.
func TestValidateEjectionableRefusesRepeated(t *testing.T) {
	f := ejectionField("ejectionable")
	f.IsRepeated = true
	rec := residencyRecord(warm(0), "", nil, epochField("store_epoch_ms"), f)
	err := validateEjectionableAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("a repeated (spec.ejectionable) field must fail generation")
	}
	if !strings.Contains(err.Error(), "is repeated") {
		t.Fatalf("refusal must say the field is repeated, got: %v", err)
	}
}

// EJ-3: which of two gate fields the sweep reads is not decidable here — the
// same shape residency VAL-3 takes for two (spec.store_epoch) fields.
func TestValidateEjectionableRefusesTwoFields(t *testing.T) {
	rec := residencyRecord(warm(0), "", nil,
		epochField("store_epoch_ms"), ejectionField("ejectionable"), ejectionField("evictable"))
	err := validateEjectionableAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("two (spec.ejectionable) fields must fail generation")
	}
	if !strings.Contains(err.Error(), "declares 2 (spec.ejectionable) fields") {
		t.Fatalf("refusal must count the fields, got: %v", err)
	}
}

// EJ-4: a record with no residency block has no window, so the gate would be
// decoration. Same posture residency VAL-6 takes for a residency nothing reads.
func TestValidateEjectionableRefusesRecordWithoutResidency(t *testing.T) {
	rec := residencyRecord(nil, "", nil, ejectionField("ejectionable"))
	err := validateEjectionableAnnotations(withPlane(rec))
	if err == nil {
		t.Fatal("(spec.ejectionable) on a record with no residency must fail generation")
	}
	if !strings.Contains(err.Error(), "declares no (spec.record).residency") {
		t.Fatalf("refusal must name the missing residency block, got: %v", err)
	}
}
