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

// Protoc-free pin for the record-key check (NEW-16): the descriptor-backed test
// below skips when protoc is absent, which would let a revert of the guard pass
// CI in a protoc-less environment. This exercises the validation directly on
// hand-built fields so the guard is enforced regardless of protoc availability.
func TestValidateRecordKeyExists(t *testing.T) {
	fields := []parser.FieldDescriptor{{Name: "event_id"}, {Name: "owner_id"}}

	if err := validateRecordKeyExists("demo", "DemoEvent", "event_id", fields); err != nil {
		t.Fatalf("existing key must pass, got: %v", err)
	}

	err := validateRecordKeyExists("demo", "DemoEvent", "missing_field", fields)
	if err == nil {
		t.Fatal("expected fail-loud when record key names a missing field")
	}
	if !strings.Contains(err.Error(), "missing_field") || !strings.Contains(err.Error(), "DemoEvent") {
		t.Fatalf("error should name the missing key field and record, got: %v", err)
	}
}

// The RecordMap query plane must fail loud when a record's key names a field that
// does not exist (it emits that field as the Pull/Watch order_by). Regression guard
// for NEW-5 — the check at record_map.go had no pin, so a one-line revert went green.
func TestAppendRecordMapFailsLoudOnMissingRecordKey(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available, skipping record-map key validation test")
	}
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

option (spec.service) = {name: "demo"};
option (spec.record_store) = {};

message DemoEvent {
  option (spec.record) = {key: "missing_field", owner: "owner_id", s2s: {auth_services: ["*"]}};
  string event_id = 1;
  string owner_id = 2;
}
`)

	err := AppendRecordMapServiceToProtos(dir, dir, "")
	if err == nil {
		t.Fatal("expected fail-loud when record key names a missing field")
	}
	if !strings.Contains(err.Error(), "missing_field") {
		t.Fatalf("error should name the missing key field, got: %v", err)
	}
}

func TestAppendRecordMapToServiceProtoEmitsQueryPlaneContract(t *testing.T) {
	dir := t.TempDir()
	relPath := filepath.Join("services", "stratum.proto")
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`syntax = "proto3";
package stratum;

import "sys/spec.proto";

option (spec.service) = {};
option (spec.record_store) = {};
`), 0644); err != nil {
		t.Fatal(err)
	}

	err := appendRecordMapToServiceProto(dir, relPath, []recordType{{
		typeName: "AgentEvent",
		pkg:      "agentsensor",
		relPath:  "vservices/agent-sensor.proto",
		key:      "event_id",
		filters: []recordFilter{
			{name: "spiffe_id", protoType: "string", rangeOp: false},
			{name: "timestamp_ms", protoType: "int64", rangeOp: true},
		},
	}})
	if err != nil {
		t.Fatalf("appendRecordMapToServiceProto failed: %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)

	for _, want := range []string{
		`import "vservices/agent-sensor.proto";`,
		`message QueryHandle`,
		`message QueryStats`,
		`message RecordCount {`,
		`enum RecordChangeType {`,
		// typed filter
		`message AgentEventFilter {`,
		`optional string spiffe_id = 1;`,
		`optional int64 timestamp_ms_from = 2;`,
		`optional int64 timestamp_ms_to = 3;`,
		// open + refine
		`message OpenAgentEventQueryRequest {`,
		`AgentEventFilter filter = 1;`,
		`bool standing = 2;`,
		`message RefineAgentEventRequest {`,
		`AgentEventFilter filter = 2;`,
		// sample (seeded) + pull (paged)
		`message SampleAgentEventRequest {`,
		`uint64 seed = 3;`,
		`message SampleAgentEventResponse {`,
		`repeated agentsensor.AgentEvent items = 1;`,
		`message PullAgentEventRequest {`,
		`message PullAgentEventResponse {`,
		// live watch envelope
		`message AgentEventWatchChange {`,
		`agentsensor.AgentEvent item = 2;`,
		`message AgentEventWatchEvent {`,
		`agentsensor.AgentEvent snapshot_item = 1;`,
		`AgentEventWatchChange delta = 3;`,
		// service + rpcs
		`service RecordMap {`,
		`option (spec.service_s2s) = {};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["argus"]};`,
		`rpc OpenAgentEventQuery(OpenAgentEventQueryRequest) returns (QueryHandle) {`,
		`rpc CountAgentEvent(QueryHandle) returns (RecordCount) {`,
		`rpc SampleAgentEvent(SampleAgentEventRequest) returns (SampleAgentEventResponse) {`,
		`rpc PullAgentEvent(PullAgentEventRequest) returns (PullAgentEventResponse) {`,
		`option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: ["event_id"], default_page_size: 100};`,
		`rpc CloseAgentEvent(QueryHandle) returns (QueryStats) {`,
		`rpc RefineAgentEvent(RefineAgentEventRequest) returns (QueryStats) {`,
		`rpc WatchAgentEvent(QueryHandle) returns (stream AgentEventWatchEvent) {`,
		`option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: ["event_id"]};`,
		`rpc SubscribeAgentEventCount(QueryHandle) returns (stream RecordCount) {`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated proto missing %q:\n%s", want, got)
		}
	}

	// RecordMap must carry the synthesized-service sentinel so the EnforceVerbs
	// gate skips it — its Open/Count/Sample/Pull/Close/Refine query-lifecycle
	// verbs are a generated S2S vocabulary, not governed REST verbs. Without it,
	// no stratum.RecordMap consumer (e.g. argus) can generate its client.
	assertGeneratedServiceMarkerLeadsService(t, got, "RecordMap")
}
