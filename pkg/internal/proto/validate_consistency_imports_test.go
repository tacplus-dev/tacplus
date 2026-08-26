// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

// ValidateConsistencyImports keeps state boundaries self-contained: a proto
// file that declares (spec.service_state) may not import another file that
// also declares one, because that would make one service's state depend on
// another's.

// consistencyFixture writes owner.proto (a state root) plus the given consumer
// proto into a parseable proto dir.
func consistencyFixture(t *testing.T, consumerProto string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "owner.proto", `syntax = "proto3";
package owner;

import "sys/spec.proto";

message OwnerRecord {
  string id = 1;
}

message OwnerState {
  option (spec.service_state) = {name: "OwnerState", label: ["owner"], export: true};

  OwnerRecord current = 1;
}
`)
	writeProto(t, dir, "consumer.proto", consumerProto)
	return dir
}

// A file with no state root of its own may import a state file freely.
func TestValidateConsistencyImportsAcceptsNonStateImporter(t *testing.T) {
	dir := consistencyFixture(t, `syntax = "proto3";
package consumer;

import "owner.proto";

message ConsumerView {
  owner.OwnerRecord current = 1;
}
`)

	if err := ValidateConsistencyImports(dir); err != nil {
		t.Fatalf("ValidateConsistencyImports rejected a non-state importer: %v", err)
	}
}

// Two state roots, one importing the other — the exact coupling the gate
// exists to refuse.
func TestValidateConsistencyImportsRejectsStateImportingState(t *testing.T) {
	dir := consistencyFixture(t, `syntax = "proto3";
package consumer;

import "sys/spec.proto";
import "owner.proto";

message ConsumerState {
  option (spec.service_state) = {name: "ConsumerState", label: ["consumer"]};

  owner.OwnerRecord imported_current = 1;
}
`)

	err := ValidateConsistencyImports(dir)
	if err == nil {
		t.Fatal("ValidateConsistencyImports accepted a state root importing another state root, want error")
	}
	if !strings.Contains(err.Error(), "consumer.proto imports owner.proto") {
		t.Fatalf("error = %q, want it to name both files", err.Error())
	}
	if !strings.Contains(err.Error(), "cannot import another proto file with (spec.service_state)") {
		t.Fatalf("error = %q, want the state-boundary message", err.Error())
	}
}

// State synthesized from (spec.service_event) is an EventBus topic
// declaration, not a real state root, so importing it is not a boundary
// violation — hasServiceState deliberately skips it.
func TestValidateConsistencyImportsIgnoresEventSynthesizedState(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "topics.proto", `syntax = "proto3";
package topics;

import "sys/spec.proto";

message HostedEvent {
  string event_id = 1;
}

message HostedEventBus {
  option (spec.service_event) = {label: "topics"};

  repeated HostedEvent events = 1 [(spec.topic) = true];
}
`)
	writeProto(t, dir, "consumer.proto", `syntax = "proto3";
package consumer;

import "sys/spec.proto";
import "topics.proto";

message ConsumerState {
  option (spec.service_state) = {name: "ConsumerState", label: ["consumer"]};

  topics.HostedEvent latest = 1;
}
`)

	if err := ValidateConsistencyImports(dir); err != nil {
		t.Fatalf("ValidateConsistencyImports rejected an import of event-synthesized state: %v", err)
	}
}
