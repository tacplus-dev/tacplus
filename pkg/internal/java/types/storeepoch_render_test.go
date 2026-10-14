// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/verbs"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// (spec.store_epoch) says WHERE the row's last-write clock lives; the generated
// wrapper owns the STAMP, exactly as (spec.principal) does for the caller's
// principal. These tests pin what the Java wrapper does with it — and, for
// (spec.record).mutability, what it refuses to do at all.

// storeEpochSanitizer indexes one append-only record carrying a store epoch, one
// CRUD record carrying one, and a plain message carrying none, so every test
// below can vary exactly one thing.
func storeEpochSanitizer() *javaEgressSanitizer {
	epoch := model.Field{
		Name: "store_epoch", Type: "int64", Number: 3,
		Annotations: map[string]model.AnnotationValue{
			"store_epoch": {Type: model.AnnotationTypeBool, BoolValue: true},
			"query_field": {Type: model.AnnotationTypeString, StringValue: "range"},
		},
	}
	record := func(name, mutability string) model.Message {
		anns := map[string]model.AnnotationValue{
			"record": {Type: model.AnnotationTypeBool, BoolValue: true},
		}
		if mutability != "" {
			anns["record_mutability"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: mutability}
		}
		return model.Message{
			Name:        name,
			Annotations: anns,
			Fields: []model.Field{
				{Name: "event_id", Type: "string", Number: 1},
				{Name: "owner_id", Type: "string", Number: 2},
				epoch,
			},
		}
	}
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		record("TaskActivity", "MUTABILITY_APPEND_ONLY"),
		record("MutableRow", "MUTABILITY_CRUD"),
		{Name: "PlainRequest", Fields: []model.Field{{Name: "event_id", Type: "string", Number: 1}}},
	}}})
}

func storeEpochMethod(name, input string, pattern verbs.Pattern) *parser.MethodInfo {
	m := &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "PlainRequest",
		FullInputType:  "tasks." + input,
		FullOutputType: "tasks.PlainRequest",
		AuthDisabled:   true,
	}
	if pattern != "" {
		m.Class = &verbs.Classification{Verb: name, Pattern: pattern, Access: verbs.AccessWrite}
	}
	return m
}

func renderStoreEpoch(m *parser.MethodInfo) string {
	return generateJavaGRPCWrapper(
		m,
		"TaskWriter",
		"tasks",
		"Tasks."+m.InputType,
		"Tasks."+m.OutputType,
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{},
		"tasks",
		"vservices",
		map[string]string{},
		proto.SkippedTraceTypes{},
		storeEpochSanitizer(),
		nil,
		nil,
	)
}

// The stamp: a request arriving with the epoch unset is stamped by the wrapper
// before the handler runs, and a request arriving with one set is REFUSED. There
// is no "equal passes" case the way (spec.principal) has one — the store owns
// this value outright, so any caller-supplied value is a forgery.
func TestJavaStoreEpoch_StampsAndRefusesCallerSuppliedValue(t *testing.T) {
	got := renderStoreEpoch(storeEpochMethod("CreateTaskActivity", "TaskActivity", verbs.PatternCreate))
	for _, want := range []string{
		"// (spec.store_epoch) store_epoch: the row's last-write clock is the STORE's, never",
		"if (request.getStoreEpoch() != 0L) {",
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "store_epoch is stamped by the store and must not be supplied by the caller");`,
		"request = request.toBuilder().setStoreEpoch(System.currentTimeMillis()).build();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	// The stamp must land BEFORE the handler is invoked — an epoch stamped after
	// the write is not the row's write time.
	stamp := strings.Index(got, "request = request.toBuilder().setStoreEpoch")
	handler := strings.Index(got, "handler.createTaskActivity(caller, request)")
	if stamp < 0 || handler < 0 || stamp > handler {
		t.Fatalf("the stamp must run before the handler call\ngot:\n%s", got)
	}
	// ...and AFTER request validation, so a malformed request is rejected on its
	// own terms rather than being mutated first.
	validate := strings.Index(got, "Helpers.validateTaskActivity(request);")
	if validate < 0 || validate > stamp {
		t.Fatalf("request validation must precede the stamp\ngot:\n%s", got)
	}
}

// Additive: a request type declaring no (spec.store_epoch) field generates the
// wrapper it generated before the annotation existed.
func TestJavaStoreEpoch_UndeclaredEmitsNoStamp(t *testing.T) {
	got := renderStoreEpoch(storeEpochMethod("CreatePlainRequest", "PlainRequest", verbs.PatternCreate))
	for _, forbidden := range []string{"store_epoch", "StoreEpoch", "INVALID_ARGUMENT"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("a request with no store_epoch field must emit no stamp, but wrapper contains %q\ngot:\n%s", forbidden, got)
		}
	}
}

// A write whose epoch the wrapper cannot stamp must FAIL generation rather than
// reach a handler with an unstamped epoch — a row the purge would measure as
// infinitely old. Same fail-closed stance
// javaMinGroupSizeUnenforceable takes for the shapes its guard cannot reach.
func TestJavaStoreEpoch_UnstampableShapeIsAGenerationError(t *testing.T) {
	san := storeEpochSanitizer()
	unary := storeEpochMethod("CreateTaskActivity", "TaskActivity", verbs.PatternCreate)
	if why := javaStoreEpochUnenforceable(unary, san, "tasks"); why != "" {
		t.Fatalf("a unary write is stampable, got refusal %q", why)
	}
	none := storeEpochMethod("CreatePlainRequest", "PlainRequest", verbs.PatternCreate)
	if why := javaStoreEpochUnenforceable(none, san, "tasks"); why != "" {
		t.Fatalf("a request with no epoch field needs no stamp, got refusal %q", why)
	}
	streamed := storeEpochMethod("CreateTaskActivity", "TaskActivity", verbs.PatternCreate)
	streamed.ClientStreaming = true
	if why := javaStoreEpochUnenforceable(streamed, san, "tasks"); why == "" {
		t.Fatal("a streamed write carrying a store_epoch field must fail generation: the wrapper renders no per-item stamp")
	}
}

// The runtime end of MUTABILITY_APPEND_ONLY. A row mutation over a write-once
// record has no guard to render — the correct generated code for it is NO
// generated code — so hosting it is a generation failure. This is the second end
// of the same annotation parser.validateAppendOnlyMethods refuses at the
// descriptor; either one reverting alone is caught by the other.
func TestJavaAppendOnly_RowMutationIsAGenerationError(t *testing.T) {
	san := storeEpochSanitizer()
	for _, pattern := range []verbs.Pattern{verbs.PatternUpdate, verbs.PatternDelete} {
		m := storeEpochMethod("MutateTaskActivity", "TaskActivity", pattern)
		why := javaAppendOnlyUnenforceable(m, san, "tasks")
		if why == "" {
			t.Fatalf("a %s over an append-only record must fail generation", pattern)
		}
		for _, want := range []string{"tasks.TaskActivity", "MUTABILITY_APPEND_ONLY", "written once"} {
			if !strings.Contains(why, want) {
				t.Fatalf("%s refusal %q does not contain %q", pattern, why, want)
			}
		}
	}
}

// Paired controls for the refusal above. It must key on the ANNOTATION and the
// resolved PATTERN, not on the record being mentioned: a read over an
// append-only record is legitimate, and so is a row mutation over a CRUD one.
func TestJavaAppendOnly_ReadsAndCrudMutationsAreHosted(t *testing.T) {
	san := storeEpochSanitizer()
	read := storeEpochMethod("GetTaskActivity", "TaskActivity", verbs.PatternGet)
	if why := javaAppendOnlyUnenforceable(read, san, "tasks"); why != "" {
		t.Fatalf("a read over an append-only record must be hosted, got refusal %q", why)
	}
	create := storeEpochMethod("CreateTaskActivity", "TaskActivity", verbs.PatternCreate)
	if why := javaAppendOnlyUnenforceable(create, san, "tasks"); why != "" {
		t.Fatalf("an append to an append-only record must be hosted, got refusal %q", why)
	}
	crud := storeEpochMethod("UpdateMutableRow", "MutableRow", verbs.PatternUpdate)
	if why := javaAppendOnlyUnenforceable(crud, san, "tasks"); why != "" {
		t.Fatalf("a row mutation over a CRUD record must be hosted, got refusal %q", why)
	}
	unclassified := storeEpochMethod("UpdateTaskActivity", "TaskActivity", "")
	if why := javaAppendOnlyUnenforceable(unclassified, san, "tasks"); why != "" {
		t.Fatalf("an unclassified method is refused by the verb gate, not this one, got %q", why)
	}
}
