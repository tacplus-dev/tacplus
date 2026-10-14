// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"
)

// ValidateArtifactOnWrite is the build-time guard on PLAN.md's knowledge/
// artifact invariant: a write path that reaches a state root marked "the
// living side" (requires_artifact_on_write) must leave an artifact
// ((spec.record)) somewhere in its package, or the build fails.

func artifactOnWriteFixture(t *testing.T, dir, body string) {
	t.Helper()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "living/living.proto", body)
}

// The reference positive shape: an opted-in state root, a WRITE RPC on the
// package's own (non-synthesized) service, and a (spec.record) message in the
// same package — modelled on tasks.TasksState / tasks.Tasks / tasks.TaskActivity.
func TestValidateArtifactOnWriteAcceptsWriteWithRecord(t *testing.T) {
	dir := t.TempDir()
	artifactOnWriteFixture(t, dir, `syntax = "proto3";
package living;

import "sys/spec.proto";

message LivingState {
  option (spec.service_state) = {
    label: ["living"]
    requires_artifact_on_write: true
  };

  string title = 1 [(spec.assert_max_size) = 200];
}

service Living {
  rpc CreateThing(CreateThingRequest) returns (CreateThingResponse);
}

message CreateThingRequest {
  string title = 1 [(spec.assert_max_size) = 200];
}
message CreateThingResponse {}

message ThingActivity {
  option (spec.record) = {key: "event_id" owner: "spiffe_id"};

  string event_id = 1 [(spec.assert_max_size) = 200];
  string spiffe_id = 2 [(spec.assert_max_size) = 200];
}
`)

	if err := ValidateArtifactOnWrite(dir); err != nil {
		t.Fatalf("ValidateArtifactOnWrite rejected a WRITE RPC backed by a declared record: %v", err)
	}
}

// A state root with no requires_artifact_on_write opt-in is out of scope even
// with WRITE RPCs and zero records — this is the "don't red the fleet" case
// (kernel/opa/sentinel/oauth/letsencrypt/operator all look like this).
func TestValidateArtifactOnWriteIgnoresUnoptedRoot(t *testing.T) {
	dir := t.TempDir()
	artifactOnWriteFixture(t, dir, `syntax = "proto3";
package living;

import "sys/spec.proto";

message LivingState {
  option (spec.service_state) = {label: ["living"]};

  string title = 1 [(spec.assert_max_size) = 200];
}

service Living {
  rpc CreateThing(CreateThingRequest) returns (CreateThingResponse);
}

message CreateThingRequest {
  string title = 1 [(spec.assert_max_size) = 200];
}
message CreateThingResponse {}
`)

	if err := ValidateArtifactOnWrite(dir); err != nil {
		t.Fatalf("ValidateArtifactOnWrite flagged a state root with no requires_artifact_on_write opt-in: %v", err)
	}
}

// An opted-in root with only READ RPCs has no write path to guard, so it must
// pass even without a record.
func TestValidateArtifactOnWriteIgnoresReadOnlyService(t *testing.T) {
	dir := t.TempDir()
	artifactOnWriteFixture(t, dir, `syntax = "proto3";
package living;

import "sys/spec.proto";

message LivingState {
  option (spec.service_state) = {
    label: ["living"]
    requires_artifact_on_write: true
  };

  string title = 1 [(spec.assert_max_size) = 200];
}

service Living {
  rpc GetThing(GetThingRequest) returns (GetThingResponse);
}

message GetThingRequest {}
message GetThingResponse {
  string title = 1 [(spec.assert_max_size) = 200];
}
`)

	if err := ValidateArtifactOnWrite(dir); err != nil {
		t.Fatalf("ValidateArtifactOnWrite flagged a read-only service on an opted-in root: %v", err)
	}
}

// The proven-red control: a purpose-built violation, an opted-in living-side
// state root whose package declares a WRITE RPC (CreateThing) and NO
// (spec.record) message anywhere in the package. Generation must fail, naming
// both the offending RPC and the state root — not some unrelated error.
func TestValidateArtifactOnWriteRejectsWriteWithNoRecord(t *testing.T) {
	dir := t.TempDir()
	artifactOnWriteFixture(t, dir, `syntax = "proto3";
package living;

import "sys/spec.proto";

message LivingState {
  option (spec.service_state) = {
    label: ["living"]
    requires_artifact_on_write: true
  };

  string title = 1 [(spec.assert_max_size) = 200];
}

service Living {
  rpc CreateThing(CreateThingRequest) returns (CreateThingResponse);
  rpc UpdateThing(UpdateThingRequest) returns (UpdateThingResponse);
}

message CreateThingRequest {
  string title = 1 [(spec.assert_max_size) = 200];
}
message CreateThingResponse {}
message UpdateThingRequest {
  string title = 1 [(spec.assert_max_size) = 200];
}
message UpdateThingResponse {}
`)

	err := ValidateArtifactOnWrite(dir)
	if err == nil {
		t.Fatal("ValidateArtifactOnWrite accepted a WRITE RPC on a living-side root with no (spec.record) in the package, want error")
	}
	got := err.Error()
	if !strings.Contains(got, "LivingState") {
		t.Fatalf("error = %q, want it to name the offending state root", got)
	}
	if !strings.Contains(got, "Living.CreateThing") {
		t.Fatalf("error = %q, want it to name the offending WRITE RPC", got)
	}
	if !strings.Contains(got, "Living.UpdateThing") {
		t.Fatalf("error = %q, want it to name every offending WRITE RPC", got)
	}
	if !strings.Contains(got, "no (spec.record) message declared") {
		t.Fatalf("error = %q, want the missing-artifact message", got)
	}
}
