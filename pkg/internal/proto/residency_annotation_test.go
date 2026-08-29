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

// residency_validate_test.go in the parser package pins each rule at the
// FileDescriptor level, which is fast and exhaustive but never runs protoc: it
// would keep passing if spec.proto did not declare residency/mutability/
// store_epoch at all, or declared them so that no author could spell them.
//
// These tests pin the half that actually matters — a real .proto, run through
// the real protoc and the real descriptor parser — so the surface itself is
// under test, not just the validator that reads it.

// residencyFixture writes a fleet that carries a record query plane (a record
// store service + a trait hosting it), plus one record proto with whatever
// (spec.record) body the case under test needs.
func residencyFixture(t *testing.T, recordBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "services/stratum.proto", `syntax = "proto3";
package stratum;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};
`)
	writeProto(t, dir, "vservices/query.proto", `syntax = "proto3";
package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["reporter"]
};
`)
	writeProto(t, dir, "vservices/tasks.proto", recordBody)
	return dir
}

// warmRecordProto is the worked example from the P0(b) design, shrunk to the
// parts under test: an immutable-warm record with the store-epoch field the
// window measures against.
const warmRecordProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
    residency: {window: RESIDENCY_WARM}
  };
  string event_id = 1;
  string owner_id = 2;
  int64 store_epoch = 3 [
    (spec.store_epoch) = true,
    (spec.query_field) = {op: QUERY_OP_RANGE}
  ];
}
`

// coldRecordProto is §8 of the design: cold is EXPRESSIBLE, so that it can be
// refused explicitly rather than being a typo nobody catches.
const coldRecordProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    residency: {window: RESIDENCY_COLD}
  };
  string event_id = 1;
  string owner_id = 2;
}
`

func skipWithoutProtoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
}

// Paired control for the cold refusal below: the same declaration spelled
// RESIDENCY_WARM parses and generates clean. Without this leg, the red test
// would still pass if the fixture failed for some entirely unrelated reason —
// a mis-typed option name, say, which is exactly the failure that would make
// the whole annotation surface unspellable while every test stayed green.
func TestResidencyWarmDeclarationGenerates(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, warmRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	if err := parser.ValidateResidencyAnnotations(dp); err != nil {
		t.Fatalf("an immutable-warm record with a store_epoch field must generate: %v", err)
	}
}

// Acceptance item 7, end to end: declaring cold FAILS GENERATION, with the
// refusal saying "reserved, not implemented" in those words.
func TestResidencyColdDeclarationFailsGeneration(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, coldRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	err = parser.ValidateResidencyAnnotations(dp)
	if err == nil {
		t.Fatal("(spec.record).residency.window = RESIDENCY_COLD must fail generation")
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

// The store epoch's LOCATION must survive protoc and reach the model, or the
// wrapper generators have nothing to stamp. Pins the (spec.store_epoch) field
// extension itself, independently of any validator that reads it.
func TestStoreEpochFieldAnnotationReachesTheModel(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, warmRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := dp.GetFileDescriptor(filepath.Join("vservices", "tasks.proto"))
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	var found []string
	for mi := range fd.Messages {
		msg := &fd.Messages[mi]
		res := msg.Annotations["record_residency"].RecordResidency
		if res == nil || res.Window != "RESIDENCY_WARM" {
			continue
		}
		if msg.Annotations["record_mutability"].StringValue != "MUTABILITY_APPEND_ONLY" {
			t.Fatalf("%s: mutability did not reach the model: %+v", msg.Name, msg.Annotations["record_mutability"])
		}
		for fi := range msg.Fields {
			if msg.Fields[fi].Annotations["store_epoch"].BoolValue {
				found = append(found, msg.Fields[fi].Name)
			}
		}
	}
	if len(found) != 1 || found[0] != "store_epoch" {
		t.Fatalf("(spec.store_epoch) did not reach the model; found %v", found)
	}
}

// The record query plane emits no row-mutating RPC for an append-only record.
// Today it emits no row write for ANY record — rows reach the store by EventBus
// publish — so this asserts the emitted surface, and assertNoRowMutatingRPCs is
// what holds a future template edit to it (its own red control lives in
// record_map_append_only_test.go).
func TestRecordPlaneEmitsNoRowMutationForAppendOnlyRecord(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, warmRecordProto)
	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "vservices", "query.proto"))
	if err != nil {
		t.Fatalf("read emitted plane: %v", err)
	}
	plane := string(b)
	// The plane IS emitted for the record — otherwise "no mutating RPC" would be
	// trivially true because nothing at all was generated.
	if !strings.Contains(plane, "rpc CreateTaskActivityQuery(") {
		t.Fatalf("no query plane emitted for the append-only record:\n%s", plane)
	}
	// Update/Delete<T>Query are QUERY-handle verbs and are expected; what must
	// not appear is a mutation carrying the record message itself.
	for _, forbidden := range []string{
		"rpc UpdateTaskActivity(TaskActivity)",
		"rpc DeleteTaskActivity(TaskActivity)",
		"returns (TaskActivity)",
	} {
		if strings.Contains(plane, forbidden) {
			t.Fatalf("append-only record got a row-mutating RPC %q:\n%s", forbidden, plane)
		}
	}
}
