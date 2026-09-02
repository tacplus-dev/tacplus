// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// recordReadPlane is the trait plane every test in this file emits against. Its
// trait-wide audience is deliberately a name no record grants, so a test that
// asserts "the record's own audience was used" cannot pass by accident when the
// per-record path silently falls back.
func recordReadPlane(relPath string) traitPlane {
	return traitPlane{
		relPath:      relPath,
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"traitwide"},
	}
}

// The whole point of (spec.record).s2s_read: a record's read RPCs carry the
// RECORD's audience, not the trait's. Before this field existed, authorizing one
// service to read one record type authorized it to read every record type in the
// fleet — the fleet-wide grant decisions/mcp-generation-and-service-owned-data-
// 2026-08-31.md §2 rules out.
func TestRecordMapS2SUsesPerRecordReadAudience(t *testing.T) {
	scoped := recordType{
		typeName: "Message", pkg: "messaging", relPath: "services/messaging.proto",
		key: "delivery_id", readAuth: []string{"messaging"}, declaresRead: true,
		filters: []recordFilter{{name: "delivery_id", protoType: "string"}},
	}
	unscoped := recordType{
		typeName: "ScottyActivity", pkg: "scotty", relPath: "services/scotty.proto",
		key: "activity_id",
	}

	out := recordMapS2SService(recordReadPlane("vservices/query.proto"), []recordType{scoped, unscoped})

	// Every Message RPC is scoped to messaging alone.
	for _, rpc := range []string{
		"CreateMessageQuery", "GetMessageCount", "GetMessageSample",
		"ListMessage", "DeleteMessageQuery", "UpdateMessageQuery",
		"WatchMessage", "SubscribeMessageCount",
	} {
		block := rpcOptionBlock(t, out, rpc)
		if !strings.Contains(block, `auth_services: ["messaging"]`) {
			t.Errorf("%s must carry the record's own read audience, got: %s", rpc, block)
		}
		if strings.Contains(block, "traitwide") {
			t.Errorf("%s fell back to the trait-wide audience despite s2s_read: %s", rpc, block)
		}
	}

	// A record declaring no s2s_read is untouched — the field is additive, and a
	// record that predates it must emit exactly the audience it emitted before.
	block := rpcOptionBlock(t, out, "CreateScottyActivityQuery")
	if !strings.Contains(block, `auth_services: ["traitwide"]`) {
		t.Errorf("a record with no s2s_read must keep the trait-wide audience, got: %s", block)
	}
}

// rpcOptionBlock returns the emitted text of one rpc declaration, so an
// assertion about ONE record's audience cannot accidentally match another
// record's identical-looking option line elsewhere in the same service.
func rpcOptionBlock(t *testing.T, service, rpcName string) string {
	t.Helper()
	marker := "rpc " + rpcName + "("
	idx := strings.Index(service, marker)
	if idx == -1 {
		t.Fatalf("rpc %s not emitted", rpcName)
	}
	rest := service[idx:]
	end := strings.Index(rest, "\n  }")
	if end == -1 {
		if nl := strings.Index(rest, "\n  rpc "); nl != -1 {
			return rest[:nl]
		}
		return rest
	}
	return rest[:end]
}

// A point read by key is the read surface's most basic question, and the plane
// could not express it: no record in the fleet declares (spec.query_field) on its
// key, so the key never reached <T>Filter. Declaring s2s_read folds it in.
func TestKeyBecomesFilterCriterionWhenReadIsDeclared(t *testing.T) {
	msg := parser.MessageDescriptor{
		Name: "Message",
		Fields: []parser.FieldDescriptor{
			{Name: "delivery_id", Type: "string", TypeKind: "scalar"},
			{Name: "sender", Type: "string", TypeKind: "scalar"},
		},
	}
	existing := []recordFilter{{name: "sender", protoType: "string"}}

	got := appendKeyFilterCriterion(existing, "delivery_id", &msg)
	if len(got) != 2 || got[1].name != "delivery_id" || got[1].rangeOp {
		t.Fatalf("key must be appended as an EQ criterion, got %+v", got)
	}
}

// The criterion is never emitted twice. An author who already declared the key
// (spec.query_field)-queryable would otherwise get a duplicate field in <T>Filter.
func TestKeyFilterCriterionNotDuplicated(t *testing.T) {
	msg := parser.MessageDescriptor{
		Name:   "Message",
		Fields: []parser.FieldDescriptor{{Name: "delivery_id", Type: "string", TypeKind: "scalar"}},
	}
	existing := []recordFilter{{name: "delivery_id", protoType: "string"}}

	got := appendKeyFilterCriterion(existing, "delivery_id", &msg)
	if len(got) != 1 {
		t.Fatalf("an already-declared key criterion must not be duplicated, got %+v", got)
	}
}

// A key the filter cannot carry degrades to "no point read" rather than failing
// the build: list/query still work and the record's other criteria are untouched.
// The key's EXISTENCE is already proven by validateRecordKeyExists, so a miss
// here is a type mismatch, not a typo.
func TestKeyFilterCriterionSkipsNonScalarKey(t *testing.T) {
	msg := parser.MessageDescriptor{
		Name: "Weird",
		Fields: []parser.FieldDescriptor{
			{Name: "composite_key", Type: "CompositeKey", TypeKind: "message"},
			{Name: "tags", Type: "string", TypeKind: "scalar", IsRepeated: true},
		},
	}
	if got := appendKeyFilterCriterion(nil, "composite_key", &msg); len(got) != 0 {
		t.Errorf("a message-typed key must not become a filter criterion, got %+v", got)
	}
	if got := appendKeyFilterCriterion(nil, "tags", &msg); len(got) != 0 {
		t.Errorf("a repeated key must not become a filter criterion, got %+v", got)
	}
}

// The reader declares (spec.record_read) and nothing else; the transport
// dependency on the plane's S2S half is injected, because it is a CONSEQUENCE of
// the read rather than a second decision that could disagree with the first.
func TestRecordReadInjectsImpliedS2SClient(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordReadFixture(t, `s2s_read: {auth_services: ["reader"]}`, true)

	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "reader.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `option (spec.grpc_s2s_client) = {service: "query.RecordMap"};`) {
		t.Fatalf("reader proto must gain the implied s2s client, got:\n%s", body)
	}
	// It must land in the file preamble. An option after the first top-level
	// block is a FileOptions extension inside a ServiceOptions body, which protoc
	// rejects outright — the failure this placement rule exists to prevent.
	txt := string(body)
	optIdx := strings.Index(txt, "option (spec.grpc_s2s_client)")
	svcIdx := strings.Index(txt, "\nservice ")
	if svcIdx != -1 && optIdx > svcIdx {
		t.Fatalf("implied client option landed inside a top-level block:\n%s", txt)
	}
}

// The injection is idempotent: an author who wrote the client by hand gets one
// option, not two.
func TestRecordReadImpliedClientNotDuplicated(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordReadFixture(t, `s2s_read: {auth_services: ["reader"]}`, true)

	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "reader.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(body), `option (spec.grpc_s2s_client) = {service: "query.RecordMap"};`); n != 1 {
		t.Fatalf("implied client option emitted %d times, want 1", n)
	}
}

// A file declaring no (spec.record_read) is never rewritten. The injection must
// touch exactly the readers and nothing else.
func TestNoRecordReadLeavesProtoUntouched(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordReadFixture(t, "", false)

	before, err := os.ReadFile(filepath.Join(dir, "reader.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "reader.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("a proto declaring no record_read must not be rewritten:\n%s", after)
	}
}

// recordReadFixture writes a minimal three-file fleet: a trait hosting the record
// query plane on a record store, a record, and a reader service. readOpt is the
// record's s2s_read clause (empty for none); declareRead controls whether the
// reader declares (spec.record_read).
func recordReadFixture(t *testing.T, readOpt string, declareRead bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if readOpt != "" {
		readOpt = "\n    " + readOpt
	}
	writeProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};
`)
	writeProto(t, dir, "owner.proto", `syntax = "proto3";
package owner;

import "sys/spec.proto";

option (spec.service) = {name: "owner"};

message Row {
  option (spec.record) = {
    key: "row_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["owner"]}`+readOpt+`
  };
  string row_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
  string namespace = 3 [(spec.store_namespace) = true];
}
`)
	readerOpt := ""
	if declareRead {
		readerOpt = "\noption (spec.record_read) = {record: \"owner.Row\"};"
	}
	writeProto(t, dir, "reader.proto", `syntax = "proto3";
package reader;

import "sys/spec.proto";

option (spec.service) = {name: "reader"};`+readerOpt+`

service Reader {
  option (spec.service_s2s) = {};
  rpc Ping(PingRequest) returns (PingResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}

message PingRequest {
  string trace_id = 200;
}

message PingResponse {
  string trace_id = 200;
}
`)
	writeProto(t, dir, filepath.Join("vservices", "query.proto"), `syntax = "proto3";
package query;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["stratum"]
};
`)
	return dir
}
