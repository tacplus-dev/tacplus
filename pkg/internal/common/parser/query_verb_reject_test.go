// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// The governed verb registry folds Query -> List, and Classify refuses folded
// synonyms outright. Generation must therefore REFUSE a trait's C2S proto that
// declares an RPC named Query<T>, rather than silently emitting it with no
// entitlement metadata.
//
// verbs_test.go pins the refusal at the MethodShape level. This pins it at the
// level that actually matters: a real .proto run through protoc and the real
// descriptor parser, so the aggregated classification-failure gate in
// ConvertToProtoInfo is what produces the error. A revert that loosened either
// the fold table or the aggregation gate would still pass the MethodShape unit
// test — it fails here.
func TestConvertToProtoInfoRejectsQueryVerbOnTraitC2SRPC(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package queryverbdemo;

import "sys/spec.proto";

message QueryTaskRequest {}
message QueryTaskResponse {}

service TaskQueryService {
  option (spec.service_c2s) = {auth_entitled_users: true, label: "tasks"};

  rpc QueryTask(QueryTaskRequest) returns (QueryTaskResponse);
}
`)

	_, err := dp.ConvertToProtoInfo("surface.proto")
	if err == nil {
		t.Fatal("ConvertToProtoInfo accepted a C2S RPC named QueryTask; Query is a folded synonym of List and must be refused at generation")
	}
	for _, want := range []string{
		"rpc verb/pattern classification failed",
		"TaskQueryService.QueryTask",
		"folded synonym",
		`"List"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("classification failure %q does not contain %q", err.Error(), want)
		}
	}
}

// Paired positive control: the identical proto spelled with the canonical verb
// classifies cleanly. Without this, the negative test above would still pass if
// the fixture failed generation for some unrelated reason.
func TestConvertToProtoInfoAcceptsCanonicalListVerbOnTraitC2SRPC(t *testing.T) {
	dp := writeSurfaceProtoDir(t, `syntax = "proto3";

package queryverbdemo;

import "sys/spec.proto";

message ListTaskRequest {}
message ListTaskResponse {}

service TaskQueryService {
  option (spec.service_c2s) = {auth_entitled_users: true, label: "tasks"};

  rpc ListTask(ListTaskRequest) returns (ListTaskResponse) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: ["task_id"], default_page_size: 100};
  }
}
`)

	info, err := dp.ConvertToProtoInfo("surface.proto")
	if err != nil {
		t.Fatalf("ConvertToProtoInfo rejected the canonical spelling ListTask: %v", err)
	}
	if len(info.Services) != 1 || len(info.Services[0].Methods) != 1 {
		t.Fatalf("expected one service with one method, got %#v", info.Services)
	}
	class := info.Services[0].Methods[0].Class
	if class == nil {
		t.Fatal("ListTask classified to a nil Class")
	}
	if class.Verb != "List" {
		t.Fatalf("ListTask classified with verb %q, want \"List\"", class.Verb)
	}
}
