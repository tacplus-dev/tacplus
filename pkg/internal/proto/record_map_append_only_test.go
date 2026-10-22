// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"
)

// assertNoRowMutatingRPCs is a GENERATOR self-check: it holds the record query
// plane's template to "emits no row-mutating RPC for an append-only record". It
// cannot fire on today's template, because today's template emits no row write
// for any record at all — rows reach the store by EventBus publish.
//
// That is exactly why it needs a red control that does not depend on the current
// template. These feed the function the emitted text directly, so the wall is
// proven to refuse a row write before anyone trusts it to be holding one back.

func appendOnlyRecords() []recordType {
	return []recordType{
		{typeName: "TaskActivity", pkg: "tasks", relPath: "vservices/tasks.proto", key: "event_id", appendOnly: true},
		{typeName: "PlainRecord", pkg: "tasks", relPath: "vservices/tasks.proto", key: "record_id"},
	}
}

// Positive control: the plane the template actually emits today passes, for both
// halves. Without this leg the red tests below would pass even if the checker
// rejected everything.
func TestAssertNoRowMutatingRPCsAcceptsTodaysPlane(t *testing.T) {
	plane := traitPlane{
		relPath: "vservices/query.proto", name: "query", host: "stratum",
		s2sService: "RecordMap", c2sService: "RecordQuery", authServices: []string{"reporter"},
	}
	records := appendOnlyRecords()
	for _, half := range []struct {
		name string
		text string
	}{
		{"s2s", recordMapS2SService(plane, records)},
		{"c2s", recordQueryC2SService(plane, records)},
	} {
		if err := assertNoRowMutatingRPCs(half.text, records); err != nil {
			t.Fatalf("%s half of today's plane must pass the append-only wall: %v", half.name, err)
		}
	}
}

// Red control (a): a hypothetical future template edit that teaches the plane a
// row write. This is the failure the wall exists for — the alternative is an
// append-only record acquiring a mutating RPC with every gate still green.
func TestAssertNoRowMutatingRPCsRejectsEmittedRowUpdate(t *testing.T) {
	emitted := `
service RecordMap {
  rpc CreateTaskActivityQuery(OpenTaskActivityQueryRequest) returns (QueryHandle);
  rpc UpdateTaskActivity(TaskActivity) returns (TaskActivity);
}
`
	err := assertNoRowMutatingRPCs(emitted, appendOnlyRecords())
	if err == nil {
		t.Fatal("a row-mutating RPC emitted for an append-only record must fail generation")
	}
	for _, want := range []string{"UpdateTaskActivity", "TaskActivity", "MUTABILITY_APPEND_ONLY", "written once"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("emission refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Red control (b): DELETE carrying the record, and the record named by its fully
// qualified name rather than its local one.
func TestAssertNoRowMutatingRPCsRejectsQualifiedRowDelete(t *testing.T) {
	emitted := "  rpc DeleteTaskActivity(tasks.TaskActivity) returns (QueryStats);\n"
	if err := assertNoRowMutatingRPCs(emitted, appendOnlyRecords()); err == nil {
		t.Fatal("a DELETE carrying the append-only record (fully qualified) must fail generation")
	}
}

// Paired control: the SAME emitted row write against a record that is NOT
// append-only passes. This pins that the wall keys on the ANNOTATION, not on the
// RPC's shape — a CRUD record is allowed a row write whenever one exists.
func TestAssertNoRowMutatingRPCsAcceptsRowUpdateOnCrudRecord(t *testing.T) {
	emitted := "  rpc UpdatePlainRecord(PlainRecord) returns (PlainRecord);\n"
	if err := assertNoRowMutatingRPCs(emitted, appendOnlyRecords()); err != nil {
		t.Fatalf("a row write on a CRUD record must generate: %v", err)
	}
}

// Paired control: the query plane's own Update/Delete verbs carry
// Refine<T>Request / QueryHandle / QueryStats, never the record. They mutate the
// query predicate and the query's lifetime, not a stored row, and must keep
// generating for an append-only record.
func TestAssertNoRowMutatingRPCsAcceptsQueryHandleVerbs(t *testing.T) {
	emitted := `
  rpc UpdateTaskActivityQuery(RefineTaskActivityRequest) returns (QueryStats);
  rpc DeleteTaskActivityQuery(QueryHandle) returns (QueryStats);
`
	if err := assertNoRowMutatingRPCs(emitted, appendOnlyRecords()); err != nil {
		t.Fatalf("handle-scoped Update/Delete are not row mutations and must generate: %v", err)
	}
}
