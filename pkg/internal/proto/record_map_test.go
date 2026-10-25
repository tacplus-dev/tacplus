// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
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
	relPath := filepath.Join("vservices", "query.proto")
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`syntax = "proto3";
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
`), 0644); err != nil {
		t.Fatal(err)
	}

	plane := traitPlane{
		relPath:      relPath,
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"reporter"},
	}

	err := appendRecordPlaneToTraitProto(dir, plane, []recordType{{
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
		t.Fatalf("appendRecordPlaneToTraitProto failed: %v", err)
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
		// create + update
		`message OpenAgentEventQueryRequest {`,
		`AgentEventFilter filter = 1;`,
		`bool standing = 2;`,
		`bool include_deleted = 3;`,
		`message RefineAgentEventRequest {`,
		`AgentEventFilter filter = 2;`,
		// sample (seeded) + list (paged)
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
		`option (spec.service_s2s) = {label: "stratum"};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["reporter"]};`,
		// c2s sibling: hosted on the same physical service, entitled against the trait.
		`service RecordQuery {`,
		`option (spec.service_c2s) = {auth_entitled_users: true, label: "stratum"};`,
		`rpc CreateAgentEventQuery(OpenAgentEventQueryRequest) returns (QueryHandle) {`,
		`rpc GetAgentEventCount(QueryHandle) returns (RecordCount) {`,
		`rpc GetAgentEventSample(SampleAgentEventRequest) returns (SampleAgentEventResponse) {`,
		`rpc ListAgentEvent(PullAgentEventRequest) returns (PullAgentEventResponse) {`,
		`option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: ["event_id"], default_page_size: 100};`,
		`rpc DeleteAgentEventQuery(QueryHandle) returns (QueryStats) {`,
		`rpc GetAgentEventRows(GetAgentEventRowsRequest) returns (GetAgentEventRowsResponse) {`,
		`message GetAgentEventRowsRequest {`,
		`rpc UpdateAgentEventQuery(RefineAgentEventRequest) returns (QueryStats) {`,
		`rpc WatchAgentEvent(QueryHandle) returns (stream AgentEventWatchEvent) {`,
		`option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: ["event_id"]};`,
		`rpc SubscribeAgentEventCount(QueryHandle) returns (stream RecordCount) {`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated proto missing %q:\n%s", want, got)
		}
	}

	// The S2S audience comes off the declaration, so no application service name
	// may survive in generator source.
	if strings.Contains(got, "argus") {
		t.Fatalf("emitted plane still carries a hardcoded auth target:\n%s", got)
	}

	// Both halves must carry the synthesized-service sentinel so the EnforceVerbs
	// gate skips them. Their RPCs are spelled in the same governed verbs, but the
	// sentinel is what keeps the gate off a synthesized service. Without it, no
	// consumer of the s2s half can generate its client.
	assertGeneratedServiceMarkerLeadsService(t, got, "RecordMap")
	assertGeneratedServiceMarkerLeadsService(t, got, "RecordQuery")
}

// recordPlaneFixture writes the three protos the trait-driven record query plane
// needs: a physical record store, a record type, and (when withTrait) the trait
// declaration that asks for the plane over them.
func recordPlaneFixture(t *testing.T, withTrait bool) string {
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
	writeProto(t, dir, "vservices/tasks.proto", `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {key: "event_id", owner: "owner_id", s2s: {auth_services: ["*"]}};
  string event_id = 1;
  string owner_id = 2;
  string actor = 3 [(spec.query_field) = {op: QUERY_OP_EQ}];
}
`)
	if withTrait {
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
	}
	return dir
}

func readProto(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// The paired control for the trait: the DECLARATION is what produces the query
// plane, not the presence of a record store.
//
// Positive leg — with the trait declared, its own proto carries both sibling
// services, both labelled with the declared host, and the s2s half's audience is
// the declared one. The record store's own proto carries neither: the plane
// belongs to the contract, not the implementor.
func TestRecordQueryPlaneEmittedFromTraitDeclaration(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordPlaneFixture(t, true)

	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}

	trait := readProto(t, dir, "vservices/query.proto")
	for _, want := range []string{
		`import "vservices/tasks.proto";`,
		"service RecordMap {",
		`option (spec.service_s2s) = {label: "stratum"};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["reporter"]};`,
		"service RecordQuery {",
		`option (spec.service_c2s) = {auth_entitled_users: true, label: "stratum"};`,
		"rpc CreateTaskActivityQuery(OpenTaskActivityQueryRequest) returns (QueryHandle)",
		"rpc WatchTaskActivity(QueryHandle) returns (stream TaskActivityWatchEvent)",
	} {
		if !strings.Contains(trait, want) {
			t.Fatalf("trait proto missing %q:\n%s", want, trait)
		}
	}

	host := readProto(t, dir, "services/stratum.proto")
	if strings.Contains(host, "service ") {
		t.Fatalf("record store proto must gain no service of its own:\n%s", host)
	}

	// The emitted plane must be well-formed enough to parse, and must satisfy the
	// host-label/audience pairing: the gateway dials the host, but the audience it
	// entitles callers against is the trait name.
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("re-parsing the emitted plane failed: %v", err)
	}
	if err := parser.ValidateC2SHostLabels(dp); err != nil {
		t.Fatalf("emitted c2s surface fails its own host-label control: %v", err)
	}
	fd, err := dp.GetFileDescriptor("vservices/query.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if fd.VServiceName != "query" {
		t.Fatalf("audience identity = %q, want the trait name %q", fd.VServiceName, "query")
	}
	var sawC2S bool
	for _, svc := range fd.Services {
		if svc.Name != "RecordQuery" {
			continue
		}
		sawC2S = true
		if !svc.IsC2S {
			t.Fatalf("RecordQuery is not classified c2s")
		}
		if svc.HostLabel != "stratum" {
			t.Fatalf("RecordQuery host label = %q, want stratum", svc.HostLabel)
		}
	}
	if !sawC2S {
		t.Fatal("emitted c2s sibling did not parse back as a service")
	}
}

// Negative leg — remove the trait declaration and the same proto set, record
// store and record type included, produces no query plane at all. No error, no
// partial emission, and above all no fallback to a generator-hardcoded host or
// audience.
func TestRecordQueryPlaneAbsentWithoutTraitDeclaration(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordPlaneFixture(t, false)

	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos must not fail with no trait declared: %v", err)
	}

	for _, rel := range []string{"services/stratum.proto", "vservices/tasks.proto"} {
		got := readProto(t, dir, rel)
		for _, unwanted := range []string{"RecordMap", "RecordQuery", "QueryHandle", "argus"} {
			if strings.Contains(got, unwanted) {
				t.Fatalf("%s gained %q with no trait declared:\n%s", rel, unwanted, got)
			}
		}
	}
}

// A trait naming a host nothing serves is a typo, and a typo that routes nowhere
// must fail generation rather than emit a plane no service picks up.
func TestRecordQueryPlaneFailsLoudOnUnservedHost(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordPlaneFixture(t, false)
	writeProto(t, dir, "vservices/query.proto", `syntax = "proto3";
package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratumm"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["reporter"]
};
`)
	err := AppendRecordMapServiceToProtos(dir, dir, "")
	if err == nil {
		t.Fatal("expected fail-loud for a host label no service serves")
	}
	if !strings.Contains(err.Error(), "stratumm") {
		t.Fatalf("error should name the unserved host, got: %v", err)
	}
}

// A trait that asks for the plane must declare its S2S audience. An empty
// auth_services would emit an s2s half callable by nobody — the silent-staleness
// failure mode the hardcoded ["argus"] had, reintroduced as a silent empty.
func TestRecordQueryPlaneFailsLoudOnMissingAuthServices(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordPlaneFixture(t, false)
	writeProto(t, dir, "vservices/query.proto", `syntax = "proto3";
package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
};
`)
	err := AppendRecordMapServiceToProtos(dir, dir, "")
	if err == nil {
		t.Fatal("expected fail-loud when a trait declares no auth_services")
	}
	if !strings.Contains(err.Error(), "auth_services") {
		t.Fatalf("error should name auth_services, got: %v", err)
	}
}

// c2s_service is OPTIONAL: a trait may host the s2s half alone. The plane still
// exists — the record store is still readable service-to-service, so VAL-6's
// "nothing would read it" stays satisfied — but no client-facing sibling is
// emitted and no c2s grant for one lands in the catalog. That is what lets a
// fleet retire the shared fleet-wide c2s query plane in favour of per-service
// <Svc>Records planes ((spec.record_read).c2s) without also giving up the s2s
// half every stored record's residency validation depends on.
//
// Regression pin: both halves of this were previously mandatory —
// collectRecordQueryPlanes rejected an empty c2s_service as a half-specified
// plane, and appendRecordPlaneToTraitProto emitted the c2s sibling
// unconditionally. Reverting either goes green without this test.
func TestRecordQueryPlaneS2SOnlyTraitEmitsNoC2SSibling(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordPlaneFixture(t, false)
	writeProto(t, dir, "vservices/query.proto", `syntax = "proto3";
package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  auth_services: ["reporter"]
};
`)

	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("an s2s-only trait must generate, got: %v", err)
	}

	trait := readProto(t, dir, "vservices/query.proto")
	for _, want := range []string{
		`import "vservices/tasks.proto";`,
		"service RecordMap {",
		`option (spec.service_s2s) = {label: "stratum"};`,
		`option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["reporter"]};`,
		"rpc CreateTaskActivityQuery(OpenTaskActivityQueryRequest) returns (QueryHandle)",
	} {
		if !strings.Contains(trait, want) {
			t.Fatalf("s2s half missing %q:\n%s", want, trait)
		}
	}

	// The c2s sibling is the whole of what an s2s-only trait must NOT emit.
	if strings.Contains(trait, "service RecordQuery") {
		t.Fatalf("s2s-only trait emitted a c2s sibling:\n%s", trait)
	}
	if strings.Contains(trait, "spec.service_c2s") {
		t.Fatalf("s2s-only trait emitted a c2s service option:\n%s", trait)
	}

	// The emitted plane must still parse, and the surviving half must still be
	// the s2s one hosted on the declared label.
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("re-parsing the emitted s2s-only plane failed: %v", err)
	}
	fd, err := dp.GetFileDescriptor("vservices/query.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	var sawS2S bool
	for _, svc := range fd.Services {
		if svc.Name == "RecordQuery" {
			t.Fatal("RecordQuery parsed back from an s2s-only trait")
		}
		if svc.Name != "RecordMap" {
			continue
		}
		sawS2S = true
		if svc.IsC2S {
			t.Fatal("RecordMap is classified c2s")
		}
		if svc.HostLabel != "stratum" {
			t.Fatalf("RecordMap host label = %q, want stratum", svc.HostLabel)
		}
	}
	if !sawS2S {
		t.Fatal("emitted s2s half did not parse back as a service")
	}
}
