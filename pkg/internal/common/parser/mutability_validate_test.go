// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// appendOnlyMutationProto is an author declaring a row mutation against an
// append-only record: an RPC whose verb resolves to the UPDATE pattern and whose
// request and response ARE the record message. This is the surface P1 has to
// refuse — "append-only emits no mutating RPCs" cannot mean "removes one that
// exists", because rows reach the record store by EventBus publish and there is
// no row-write RPC in the tree at all. So the wall is: none may be declared, and
// none may be emitted.
func appendOnlyMutationProto(mutability, verb string) string {
	return `syntax = "proto3";

package appendonlydemo;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: ` + mutability + `
  };
  string event_id = 1;
  string owner_id = 2;
}

service TaskWriter {
  option (spec.service_c2s) = {auth_entitled_users: true, label: "tasks"};

  rpc ` + verb + `TaskActivity(TaskActivity) returns (TaskActivity);
}
`
}

// Red control. An UPDATE over an append-only record must fail generation, and
// the refusal must name the RPC, the record, and the annotation that forbids it.
func TestConvertToProtoInfoRejectsUpdateOnAppendOnlyRecord(t *testing.T) {
	dp := writeSurfaceProtoDir(t, appendOnlyMutationProto("MUTABILITY_APPEND_ONLY", "Update"))

	_, err := dp.ConvertToProtoInfo("surface.proto")
	if err == nil {
		t.Fatal("an UPDATE RPC over a MUTABILITY_APPEND_ONLY record must fail generation")
	}
	for _, want := range []string{
		"append-only record mutation errors",
		"TaskWriter.UpdateTaskActivity",
		"appendonlydemo.TaskActivity",
		"MUTABILITY_APPEND_ONLY",
		"written once",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("append-only refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Red control, the other mutating pattern. DELETE is refused for the same
// reason UPDATE is; pinning only one would let a half-revert through.
func TestConvertToProtoInfoRejectsDeleteOnAppendOnlyRecord(t *testing.T) {
	dp := writeSurfaceProtoDir(t, appendOnlyMutationProto("MUTABILITY_APPEND_ONLY", "Delete"))

	_, err := dp.ConvertToProtoInfo("surface.proto")
	if err == nil {
		t.Fatal("a DELETE RPC over a MUTABILITY_APPEND_ONLY record must fail generation")
	}
	if !strings.Contains(err.Error(), "TaskWriter.DeleteTaskActivity") {
		t.Fatalf("refusal must name the offending RPC, got: %v", err)
	}
}

// Paired positive control (a): the IDENTICAL proto on a CRUD record generates.
// Without this the red tests above would still pass if the fixture failed for an
// unrelated reason, and — more importantly — this is what pins that the refusal
// is driven by the ANNOTATION and not by the RPC's name.
func TestConvertToProtoInfoAcceptsUpdateOnCrudRecord(t *testing.T) {
	dp := writeSurfaceProtoDir(t, appendOnlyMutationProto("MUTABILITY_CRUD", "Update"))

	if _, err := dp.ConvertToProtoInfo("surface.proto"); err != nil {
		t.Fatalf("an UPDATE RPC over a MUTABILITY_CRUD record must generate: %v", err)
	}
}

// Paired positive control (b): a READ over an append-only record generates. An
// append-only record is fully readable — refusing reads would make the
// annotation unusable — so the refusal must key on the resolved verb PATTERN,
// not on "this RPC mentions the record".
func TestConvertToProtoInfoAcceptsGetOnAppendOnlyRecord(t *testing.T) {
	dp := writeSurfaceProtoDir(t, appendOnlyMutationProto("MUTABILITY_APPEND_ONLY", "Get"))

	if _, err := dp.ConvertToProtoInfo("surface.proto"); err != nil {
		t.Fatalf("a GET RPC over an append-only record must generate: %v", err)
	}
}

// Paired positive control (c): the query plane's own Update/Delete verbs carry
// Refine<T>Request / QueryHandle / QueryStats, never the record itself, because
// they mutate the query PREDICATE and the query's LIFETIME rather than a stored
// row. They must keep generating against an append-only record — this is the
// exact case the definition of "row-mutating" was written to spare, so it gets a
// test rather than a comment.
func TestConvertToProtoInfoAcceptsQueryHandleMutationOnAppendOnlyRecord(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package appendonlydemo;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
  };
  string event_id = 1;
  string owner_id = 2;
}

message QueryHandle { string query_id = 1; }
message QueryStats { string query_id = 1; }
message RefineTaskActivityRequest { string query_id = 1; }

service TaskQuery {
  option (spec.service_c2s) = {auth_entitled_users: true, label: "tasks"};

  rpc UpdateTaskActivityQuery(RefineTaskActivityRequest) returns (QueryStats);
  rpc DeleteTaskActivityQuery(QueryHandle) returns (QueryStats);
}
`)

	if _, err := dp.ConvertToProtoInfo("surface.proto"); err != nil {
		t.Fatalf("the query plane's handle-scoped Update/Delete must generate for an append-only record: %v", err)
	}
}
