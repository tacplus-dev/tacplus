// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// residencyRecord builds a (spec.record) message carrying whichever of
// residency / mutability the case under test needs, plus the fields it declares.
// recordStoreHost() and recordPlaneTrait() are shared with
// min_group_size_validate_test.go — VAL-4 is the same invariant, so it is the
// same fixture, not a second copy of it.
func residencyRecord(residency *model.ResidencyPolicyEntry, mutability string, fields ...FieldDescriptor) *FileDescriptor {
	anns := map[string]model.AnnotationValue{
		"record": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	if residency != nil {
		anns["record_residency"] = model.AnnotationValue{
			Type: model.AnnotationTypeBool, BoolValue: true, RecordResidency: residency,
		}
	}
	if mutability != "" {
		anns["record_mutability"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: mutability}
	}
	return &FileDescriptor{
		Path:    "services/tasks.proto",
		Package: "tasks",
		Messages: []MessageDescriptor{{
			Name:        "TaskActivity",
			Annotations: anns,
			Fields:      fields,
		}},
	}
}

// epochField is a well-formed (spec.store_epoch) field: int64, RANGE-queryable.
func epochField(name string) FieldDescriptor {
	return FieldDescriptor{
		Name: name, Type: "int64", TypeKind: "scalar",
		Annotations: map[string]model.AnnotationValue{
			"store_epoch": {Type: model.AnnotationTypeBool, BoolValue: true},
			"query_field": {Type: model.AnnotationTypeString, StringValue: "range"},
		},
	}
}

func withPlane(record *FileDescriptor) []*FileDescriptor {
	return []*FileDescriptor{recordStoreHost(), recordPlaneTrait("stratum"), record}
}

// clocks is the shape every record declares under the one-clock lifecycle.
func clocks(idle, deletedMaxAge int64) *model.ResidencyPolicyEntry {
	return &model.ResidencyPolicyEntry{IdleSeconds: idle, DeletedMaxAgeSeconds: deletedMaxAge}
}

// ---------------------------------------------------------------------------
// Positive controls. Each one is the paired green half of a red test below:
// without them, a red test would still pass if the fixture failed generation
// for some entirely unrelated reason.
// ---------------------------------------------------------------------------

// A record declaring neither annotation must keep generating, or the meaning of
// every existing record proto changes on the upgrade.
func TestValidateResidencyIgnoresUndeclaredRecord(t *testing.T) {
	if err := validateResidencyAnnotations(withPlane(residencyRecord(nil, ""))); err != nil {
		t.Fatalf("a record declaring neither residency nor mutability must generate: %v", err)
	}
}

func TestValidateResidencyAcceptsBothClocks(t *testing.T) {
	fds := withPlane(residencyRecord(clocks(3600, 2592000), "MUTABILITY_APPEND_ONLY",
		epochField("store_epoch")))
	if err := validateResidencyAnnotations(fds); err != nil {
		t.Fatalf("a record declaring both lifecycle clocks must generate: %v", err)
	}
}

// 0 on either clock means "apply the platform default", not "never" and not an
// error. This is the control that pins it: a defaulted block is the common case,
// and refusing it would make every record carry two hand-picked numbers.
func TestValidateResidencyAcceptsDefaultedClocks(t *testing.T) {
	fds := withPlane(residencyRecord(clocks(0, 0), "", epochField("store_epoch")))
	if err := validateResidencyAnnotations(fds); err != nil {
		t.Fatalf("0 on both clocks means the platform default and must generate: %v", err)
	}
}

// deleted_max_age_seconds on a record with no (spec.deleted_flag) field is
// carried, not refused: a type that later gains the marker should not also have
// to gain a number. Pins that deliberate omission so a later "tighten it up"
// edit has to argue with a test.
func TestValidateResidencyAcceptsDeletedMaxAgeWithoutDeletedFlag(t *testing.T) {
	fds := withPlane(residencyRecord(clocks(0, 604800), "", epochField("store_epoch")))
	if err := validateResidencyAnnotations(fds); err != nil {
		t.Fatalf("deleted_max_age_seconds without a (spec.deleted_flag) field must generate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Red controls.
// ---------------------------------------------------------------------------

// VAL-1 — a clock cannot run backwards, and 0 is already the spelling of "use
// the platform default", so a negative number is not a spelling of anything.
func TestValidateResidencyRejectsNegativeIdleSeconds(t *testing.T) {
	err := validateResidencyAnnotations(withPlane(residencyRecord(clocks(-1, 0), "")))
	if err == nil {
		t.Fatal("a negative idle_seconds must fail generation")
	}
	for _, want := range []string{"tasks.TaskActivity", "idle_seconds:-1", "use the platform default"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// VAL-2 — same rule, same reason, on the purge clock.
func TestValidateResidencyRejectsNegativeDeletedMaxAge(t *testing.T) {
	err := validateResidencyAnnotations(withPlane(residencyRecord(clocks(0, -7), "")))
	if err == nil {
		t.Fatal("a negative deleted_max_age_seconds must fail generation")
	}
	for _, want := range []string{"deleted_max_age_seconds:-7", "use the platform default"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// VAL-3 (a) — an epoch field the disk leg cannot range on is an epoch field that
// does not work, which is worse than an absent one.
func TestValidateResidencyRejectsEpochWithoutRangeQuery(t *testing.T) {
	f := epochField("store_epoch")
	delete(f.Annotations, "query_field")
	err := validateResidencyAnnotations(withPlane(residencyRecord(clocks(3600, 0), "", f)))
	if err == nil {
		t.Fatal("a (spec.store_epoch) field with no QUERY_OP_RANGE must fail generation")
	}
	if !strings.Contains(err.Error(), "QUERY_OP_RANGE") {
		t.Fatalf("error must name the missing range op, got: %v", err)
	}
}

// VAL-3 (b) — the store epoch is Unix milliseconds; a string is not one.
func TestValidateResidencyRejectsNonInt64Epoch(t *testing.T) {
	f := epochField("store_epoch")
	f.Type = "string"
	err := validateResidencyAnnotations(withPlane(residencyRecord(clocks(3600, 0), "", f)))
	if err == nil {
		t.Fatal("a non-int64 (spec.store_epoch) field must fail generation")
	}
	if !strings.Contains(err.Error(), "not int64") {
		t.Fatalf("error must name the wrong type, got: %v", err)
	}
}

// VAL-3 (c) — a row has exactly one store epoch; which of two the purge measures
// the delete from is not decidable here.
func TestValidateResidencyRejectsTwoEpochFields(t *testing.T) {
	fds := withPlane(residencyRecord(clocks(3600, 0), "",
		epochField("store_epoch"), epochField("written_at")))
	err := validateResidencyAnnotations(fds)
	if err == nil {
		t.Fatal("two (spec.store_epoch) fields must fail generation")
	}
	if !strings.Contains(err.Error(), "store_epoch, written_at") {
		t.Fatalf("error must name both epoch fields, got: %v", err)
	}
}

// VAL-3 is checked wherever a (spec.store_epoch) field is declared, not only on
// a record that declares residency: the field is the store's durable write clock
// either way, and a malformed one breaks the purge and the disk leg whether or
// not the author also wrote a residency block.
func TestValidateResidencyRejectsMalformedEpochWithoutResidency(t *testing.T) {
	f := epochField("store_epoch")
	f.Type = "string"
	err := validateResidencyAnnotations(withPlane(residencyRecord(nil, "", f)))
	if err == nil {
		t.Fatal("a malformed (spec.store_epoch) field must fail generation with no residency block")
	}
	if !strings.Contains(err.Error(), "not int64") {
		t.Fatalf("error must name the wrong type, got: %v", err)
	}
}

// VAL-4 — verbatim the second min_group_size invariant: an annotation nothing
// reads is a generation failure, not a no-op.
func TestValidateResidencyRejectsDeclarationWithNoQueryPlane(t *testing.T) {
	err := validateResidencyAnnotations([]*FileDescriptor{
		residencyRecord(clocks(3600, 0), "", epochField("store_epoch")),
	})
	if err == nil {
		t.Fatal("residency declared into a fleet with no record query plane must fail generation")
	}
	if !strings.Contains(err.Error(), "no (spec.trait) hosts a record query plane") {
		t.Fatalf("error must say nothing would read the annotation, got: %v", err)
	}
}
