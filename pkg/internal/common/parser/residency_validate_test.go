// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// residencyRecord builds a (spec.record) message carrying whichever of
// residency / mutability / retention the case under test needs, plus the fields
// it declares. recordStoreHost() and recordPlaneTrait() are shared with
// min_group_size_validate_test.go — VAL-6 is the same invariant, so it is the
// same fixture, not a second copy of it.
func residencyRecord(residency *model.ResidencyPolicyEntry, mutability string, retention *model.RetentionPolicyEntry, fields ...FieldDescriptor) *FileDescriptor {
	anns := map[string]model.AnnotationValue{
		"record": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	if residency != nil {
		anns["record_residency"] = model.AnnotationValue{
			Type: model.AnnotationTypeString, StringValue: residency.Window, RecordResidency: residency,
		}
	}
	if mutability != "" {
		anns["record_mutability"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: mutability}
	}
	if retention != nil {
		anns["record_retention"] = model.AnnotationValue{
			Type: model.AnnotationTypeBool, BoolValue: true, RecordRetention: retention,
		}
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

func warm(seconds int64) *model.ResidencyPolicyEntry {
	return &model.ResidencyPolicyEntry{Window: "RESIDENCY_WARM", WindowSeconds: seconds}
}

// ---------------------------------------------------------------------------
// Positive controls. Each one is the paired green half of a red test below:
// without them, a red test would still pass if the fixture failed generation
// for some entirely unrelated reason.
// ---------------------------------------------------------------------------

// Every record proto in the fleet today declares neither annotation. Unset must
// keep meaning exactly what it means now, or the P1 upgrade silently changes the
// meaning of every existing record.
func TestValidateResidencyIgnoresUndeclaredRecord(t *testing.T) {
	if err := validateResidencyAnnotations(withPlane(residencyRecord(nil, "", nil))); err != nil {
		t.Fatalf("a record declaring neither residency nor mutability must generate: %v", err)
	}
}

// The paired control for the cold refusal (§8 of the P0b design): the SAME
// declaration spelled RESIDENCY_WARM generates clean.
func TestValidateResidencyAcceptsWarmWithEpoch(t *testing.T) {
	fds := withPlane(residencyRecord(warm(0), "MUTABILITY_APPEND_ONLY", nil, epochField("store_epoch")))
	if err := validateResidencyAnnotations(fds); err != nil {
		t.Fatalf("RESIDENCY_WARM with a well-formed store_epoch must generate: %v", err)
	}
}

func TestValidateResidencyAcceptsHot(t *testing.T) {
	hot := &model.ResidencyPolicyEntry{Window: "RESIDENCY_HOT"}
	if err := validateResidencyAnnotations(withPlane(residencyRecord(hot, "MUTABILITY_CRUD", nil))); err != nil {
		t.Fatalf("RESIDENCY_HOT (today's behaviour, said out loud) must generate: %v", err)
	}
}

// VAL-4 refuses the RETENTION knob on an append-only forever dataset, and
// deliberately does NOT refuse a memory window on one: eviction at window age is
// cache maintenance, never data loss. This is the control that pins that
// deliberate omission, so a later "tighten it up" edit has to argue with a test.
func TestValidateResidencyAcceptsWindowOnAppendOnlyForever(t *testing.T) {
	fds := withPlane(residencyRecord(warm(86400), "MUTABILITY_APPEND_ONLY",
		&model.RetentionPolicyEntry{Forever: true}, epochField("store_epoch")))
	if err := validateResidencyAnnotations(fds); err != nil {
		t.Fatalf("a memory window on an append-only forever dataset destroys nothing and must generate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Red controls.
// ---------------------------------------------------------------------------

// VAL-1 — acceptance item 7. Cold is expressible so that it can be refused
// EXPLICITLY; the refusal must say "reserved, not implemented" in those words,
// say what would otherwise happen silently, and name both usable presets.
func TestValidateResidencyRejectsCold(t *testing.T) {
	cold := &model.ResidencyPolicyEntry{Window: "RESIDENCY_COLD"}
	err := validateResidencyAnnotations(withPlane(residencyRecord(cold, "", nil)))
	if err == nil {
		t.Fatal("RESIDENCY_COLD must fail generation: tacplus generates no cold tier")
	}
	for _, want := range []string{
		"tasks.TaskActivity",
		"RESIDENCY_COLD",
		"RESERVED and NOT IMPLEMENTED",
		"silently store the record hot",
		"RESIDENCY_WARM",
		"RESIDENCY_HOT",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("cold refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// VAL-2 — an author who wrote the block meant something. Silently reading it as
// HOT is the fail-silent class this tree closes.
func TestValidateResidencyRejectsUnspecifiedWindow(t *testing.T) {
	unspec := &model.ResidencyPolicyEntry{Window: "RESIDENCY_UNSPECIFIED"}
	err := validateResidencyAnnotations(withPlane(residencyRecord(unspec, "", nil)))
	if err == nil {
		t.Fatal("a present residency block with RESIDENCY_UNSPECIFIED must fail generation")
	}
	if !strings.Contains(err.Error(), "RESIDENCY_UNSPECIFIED") {
		t.Fatalf("error must name the unspecified window, got: %v", err)
	}
}

// VAL-3 (a) — warm with no epoch field at all: the window has no clock.
func TestValidateResidencyRejectsWarmWithoutEpochField(t *testing.T) {
	err := validateResidencyAnnotations(withPlane(residencyRecord(warm(0), "", nil)))
	if err == nil {
		t.Fatal("RESIDENCY_WARM with no (spec.store_epoch) field must fail generation")
	}
	if !strings.Contains(err.Error(), "no (spec.store_epoch) field") {
		t.Fatalf("error must say the epoch field is missing, got: %v", err)
	}
}

// VAL-3 (b) — an epoch field the warm partition cannot range on is an epoch
// field that does not work, which is worse than an absent one.
func TestValidateResidencyRejectsEpochWithoutRangeQuery(t *testing.T) {
	f := epochField("store_epoch")
	delete(f.Annotations, "query_field")
	err := validateResidencyAnnotations(withPlane(residencyRecord(warm(0), "", nil, f)))
	if err == nil {
		t.Fatal("a (spec.store_epoch) field with no QUERY_OP_RANGE must fail generation")
	}
	if !strings.Contains(err.Error(), "QUERY_OP_RANGE") {
		t.Fatalf("error must name the missing range op, got: %v", err)
	}
}

// VAL-3 (c) — the store epoch is Unix milliseconds; a string is not one.
func TestValidateResidencyRejectsNonInt64Epoch(t *testing.T) {
	f := epochField("store_epoch")
	f.Type = "string"
	err := validateResidencyAnnotations(withPlane(residencyRecord(warm(0), "", nil, f)))
	if err == nil {
		t.Fatal("a non-int64 (spec.store_epoch) field must fail generation")
	}
	if !strings.Contains(err.Error(), "not int64") {
		t.Fatalf("error must name the wrong type, got: %v", err)
	}
}

// VAL-3 (d) — a row has exactly one store epoch; which of two the window
// measures against is not decidable here.
func TestValidateResidencyRejectsTwoEpochFields(t *testing.T) {
	fds := withPlane(residencyRecord(warm(0), "", nil, epochField("store_epoch"), epochField("written_at")))
	err := validateResidencyAnnotations(fds)
	if err == nil {
		t.Fatal("two (spec.store_epoch) fields must fail generation")
	}
	if !strings.Contains(err.Error(), "store_epoch, written_at") {
		t.Fatalf("error must name both epoch fields, got: %v", err)
	}
}

// VAL-4 — acceptance item 9. An append-only dataset declaring "forever" refuses
// the retention knob at generation.
func TestValidateResidencyRejectsRetentionKnobOnAppendOnlyForever(t *testing.T) {
	fds := withPlane(residencyRecord(nil, "MUTABILITY_APPEND_ONLY",
		&model.RetentionPolicyEntry{Forever: true, TimestampField: "timestamp_ms"}))
	err := validateResidencyAnnotations(fds)
	if err == nil {
		t.Fatal("an append-only forever dataset declaring a retention knob must fail generation")
	}
	for _, want := range []string{"MUTABILITY_APPEND_ONLY", "forever:true", "timestamp_ms"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("retention-knob refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// VAL-4b — forever AND a duration is a self-contradiction on any record, not
// only an append-only one.
func TestValidateResidencyRejectsForeverWithDuration(t *testing.T) {
	fds := withPlane(residencyRecord(nil, "",
		&model.RetentionPolicyEntry{Forever: true, DurationSeconds: 7776000}))
	err := validateResidencyAnnotations(fds)
	if err == nil {
		t.Fatal("retention.forever:true with duration_seconds > 0 must fail generation")
	}
	if !strings.Contains(err.Error(), "cannot be both never aged out") {
		t.Fatalf("error must name the contradiction, got: %v", err)
	}
}

// VAL-5 — window_seconds is a WARM-only knob. On HOT nothing reads it, which is
// the same rule the unimplemented statedb tuning family gets.
func TestValidateResidencyRejectsWindowSecondsOnHot(t *testing.T) {
	hot := &model.ResidencyPolicyEntry{Window: "RESIDENCY_HOT", WindowSeconds: 3600}
	err := validateResidencyAnnotations(withPlane(residencyRecord(hot, "", nil)))
	if err == nil {
		t.Fatal("window_seconds on RESIDENCY_HOT must fail generation: nothing reads it")
	}
	if !strings.Contains(err.Error(), "RESIDENCY_WARM only") && !strings.Contains(err.Error(), "read for RESIDENCY_WARM only") {
		t.Fatalf("error must say the knob is WARM-only, got: %v", err)
	}
}

// VAL-6 — verbatim the second min_group_size invariant: an annotation nothing
// reads is a generation failure, not a no-op.
func TestValidateResidencyRejectsDeclarationWithNoQueryPlane(t *testing.T) {
	err := validateResidencyAnnotations([]*FileDescriptor{
		residencyRecord(warm(0), "", nil, epochField("store_epoch")),
	})
	if err == nil {
		t.Fatal("residency declared into a fleet with no record query plane must fail generation")
	}
	if !strings.Contains(err.Error(), "no (spec.trait) hosts a record query plane") {
		t.Fatalf("error must say nothing would read the annotation, got: %v", err)
	}
}
