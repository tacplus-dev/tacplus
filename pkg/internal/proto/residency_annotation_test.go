// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// residency_validate_test.go in the parser package pins each rule at the
// FileDescriptor level, which is fast and exhaustive but never runs protoc: it
// would keep passing if spec.proto did not declare residency/mutability/
// store_epoch/deleted_flag at all, or declared them so that no author could
// spell them.
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

// lifecycleRecordProto is the worked example from the record-lifecycle decision,
// shrunk to the parts under test: a record declaring both residency clocks, the
// store-epoch field the purge measures a delete from, and the soft-delete marker
// the query plane excludes on.
const lifecycleRecordProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
    residency: {idle_seconds: 3600 deleted_max_age_seconds: 2592000}
  };
  string event_id = 1;
  string owner_id = 2;
  int64 store_epoch = 3 [
    (spec.store_epoch) = true,
    (spec.query_field) = {op: QUERY_OP_RANGE}
  ];
  bool deleted = 4 [(spec.deleted_flag) = true];
}
`

// negativeClockRecordProto is the refusal: a clock cannot run backwards, and 0
// already means "use the platform default", so a negative number is not a
// spelling of anything.
const negativeClockRecordProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    residency: {idle_seconds: -1}
  };
  string event_id = 1;
  string owner_id = 2;
}
`

// twoMarkersRecordProto is the deleted-flag refusal: a row has exactly one
// soft-delete state, and which field carries it is not decidable at generation.
const twoMarkersRecordProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
  };
  string event_id = 1;
  string owner_id = 2;
  bool deleted = 3 [(spec.deleted_flag) = true];
  bool removed = 4 [(spec.deleted_flag) = true];
}
`

func skipWithoutProtoc(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
}

// Paired control for the refusals below: the whole lifecycle surface — two
// clocks, a store epoch and a soft-delete marker — parses and generates clean.
// Without this leg, a red test would still pass if the fixture failed for some
// entirely unrelated reason — a mis-typed option name, say, which is exactly the
// failure that would make the whole annotation surface unspellable while every
// test stayed green.
func TestResidencyLifecycleDeclarationGenerates(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, lifecycleRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	if err := parser.ValidateResidencyAnnotations(dp); err != nil {
		t.Fatalf("a record declaring both lifecycle clocks must generate: %v", err)
	}
	if err := parser.ValidateDeletedFlagAnnotations(dp); err != nil {
		t.Fatalf("a record declaring one (spec.deleted_flag) field must generate: %v", err)
	}
}

// End to end: a negative clock FAILS GENERATION rather than being read as the
// platform default, which is the fail-silent class this surface closes.
func TestResidencyNegativeClockFailsGeneration(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, negativeClockRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	err = parser.ValidateResidencyAnnotations(dp)
	if err == nil {
		t.Fatal("(spec.record).residency.idle_seconds = -1 must fail generation")
	}
	for _, want := range []string{"tasks.TaskActivity", "idle_seconds:-1", "use the platform default"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// End to end: two (spec.deleted_flag) fields FAIL GENERATION. Pins the field
// extension itself — a record can spell the marker twice, and the refusal has to
// come from the validator rather than from protoc.
func TestDeletedFlagTwoMarkersFailGeneration(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, twoMarkersRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	err = parser.ValidateDeletedFlagAnnotations(dp)
	if err == nil {
		t.Fatal("two (spec.deleted_flag) fields must fail generation")
	}
	for _, want := range []string{"tasks.TaskActivity", "declares 2 (spec.deleted_flag) fields", "deleted, removed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// The store epoch's and the soft-delete marker's LOCATIONS must survive protoc
// and reach the model, or the wrapper generators have nothing to stamp and the
// query plane has nothing to exclude on. Pins the two field extensions and both
// residency clocks, independently of any validator that reads them.
func TestLifecycleAnnotationsReachTheModel(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, lifecycleRecordProto)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := dp.GetFileDescriptor(filepath.Join("vservices", "tasks.proto"))
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	var epochs []string
	seen := false
	for mi := range fd.Messages {
		msg := &fd.Messages[mi]
		res := msg.Annotations["record_residency"].RecordResidency
		if res == nil {
			continue
		}
		seen = true
		if res.IdleSeconds != 3600 || res.DeletedMaxAgeSeconds != 2592000 {
			t.Fatalf("%s: residency clocks did not reach the model: %+v", msg.Name, res)
		}
		if msg.Annotations["record_mutability"].StringValue != "MUTABILITY_APPEND_ONLY" {
			t.Fatalf("%s: mutability did not reach the model: %+v", msg.Name, msg.Annotations["record_mutability"])
		}
		for fi := range msg.Fields {
			if msg.Fields[fi].Annotations["store_epoch"].BoolValue {
				epochs = append(epochs, msg.Fields[fi].Name)
			}
		}
		f, ok := parser.DeletedFlagField(msg)
		if !ok || f.Name != "deleted" {
			t.Fatalf("%s: (spec.deleted_flag) did not reach the model; got %v/%v", msg.Name, f, ok)
		}
	}
	if !seen {
		t.Fatal("no record carrying (spec.record).residency reached the model")
	}
	if len(epochs) != 1 || epochs[0] != "store_epoch" {
		t.Fatalf("(spec.store_epoch) did not reach the model; found %v", epochs)
	}
}

// The record query plane emits no row-mutating RPC for an append-only record.
// Today it emits no row write for ANY record — rows reach the store by EventBus
// publish — so this asserts the emitted surface, and assertNoRowMutatingRPCs is
// what holds a future template edit to it (its own red control lives in
// record_map_append_only_test.go).
func TestRecordPlaneEmitsNoRowMutationForAppendOnlyRecord(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, lifecycleRecordProto)
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
