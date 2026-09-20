// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

// virtualFixture builds a parseable proto dir containing only the files given,
// keyed by relative path.
func virtualFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	for rel, content := range files {
		writeProto(t, dir, rel, content)
	}
	return dir
}

// opaServerProto is a service whose proto imports sys/opalib.proto, so it
// genuinely implements OpaEval and is a legal opa_import target.
const opaServerProto = `syntax = "proto3";
package opaserver;

import "sys/spec.proto";
import "sys/opalib.proto";

message OpaServerState {
  option (spec.service_state) = {name: "OpaServerState", label: ["opaserver"]};

  opalib.OpaPolicy policy = 1;
}
`

// plainServerProto is a service that does NOT import sys/opalib.proto, so it
// cannot answer an OpaEval call.
const plainServerProto = `syntax = "proto3";
package plainserver;

import "sys/spec.proto";

message PlainServerState {
  option (spec.service_state) = {name: "PlainServerState", label: ["plainserver"]};

  string note = 1;
}
`

// ValidateVirtualImports resolves each (spec.opa_import) target to a service
// that actually imports sys/opalib.proto. A misdeclared target is a client the
// generator would emit against a service that cannot answer it.

func TestValidateVirtualImportsAcceptsTargetImplementingOpa(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"opaserver/opaserver.proto": opaServerProto,
		"consumer/consumer.proto": `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.opa_import) = {source: "consumer.EvalInput", service: "opaserver"};

message EvalInput {
  string subject = 1;
}
`,
	})

	if err := ValidateVirtualImports(dir); err != nil {
		t.Fatalf("ValidateVirtualImports rejected a valid opa_import: %v", err)
	}
}

func TestValidateVirtualImportsRejectsTargetWithoutOpaImplementation(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"plainserver/plainserver.proto": plainServerProto,
		"consumer/consumer.proto": `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.opa_import) = {source: "consumer.EvalInput", service: "plainserver"};

message EvalInput {
  string subject = 1;
}
`,
	})

	err := ValidateVirtualImports(dir)
	if err == nil {
		t.Fatal("ValidateVirtualImports accepted an opa_import targeting a service that does not implement OpaEval, want error")
	}
	if !strings.Contains(err.Error(), "does not implement OpaEval") {
		t.Fatalf("error = %q, want the missing-implementation message", err.Error())
	}
	if !strings.Contains(err.Error(), "'plainserver'") {
		t.Fatalf("error = %q, want it to name the bad target", err.Error())
	}
}

// A target package that is not in the proto set at all resolves to no
// capabilities and fails the same way — a dangling client is still a client
// that cannot be called.
func TestValidateVirtualImportsRejectsUnknownTargetService(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"consumer/consumer.proto": `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.opa_import) = {source: "consumer.EvalInput", service: "nosuchservice"};

message EvalInput {
  string subject = 1;
}
`,
	})

	err := ValidateVirtualImports(dir)
	if err == nil {
		t.Fatal("ValidateVirtualImports accepted an opa_import targeting an absent service, want error")
	}
	if !strings.Contains(err.Error(), "'nosuchservice'") {
		t.Fatalf("error = %q, want it to name the absent target", err.Error())
	}
}

func TestValidateVirtualImportsRejectsMissingServiceField(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"opaserver/opaserver.proto": opaServerProto,
		"consumer/consumer.proto": `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.opa_import) = {source: "consumer.EvalInput"};

message EvalInput {
  string subject = 1;
}
`,
	})

	err := ValidateVirtualImports(dir)
	if err == nil {
		t.Fatal("ValidateVirtualImports accepted an opa_import with no service, want error")
	}
	if !strings.Contains(err.Error(), "'service' field is required") {
		t.Fatalf("error = %q, want the required-service message", err.Error())
	}
}

// ValidateRecordLinkage enforces the event invariant: an event declares its
// producer surface rather than defaulting to one. It deliberately does NOT
// require a transformation's source to be a record.

func TestValidateRecordLinkageAcceptsRecordWithTransformationAndSurface(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"events/events.proto": `syntax = "proto3";
package events;

import "sys/spec.proto";

message SessionEvent {
  option (spec.record) = {
    key: "event_id", owner: "spiffe_id", s2s: {}
  };
  option (spec.transformation) = {
    name: "SessionRow"
    field: {source: "timestamp_ms" partition_key: true}
  };

  string event_id = 1;
  string spiffe_id = 2;
  int64 timestamp_ms = 3;
}
`,
	})

	if err := ValidateRecordLinkage(dir); err != nil {
		t.Fatalf("ValidateRecordLinkage rejected a well-formed record: %v", err)
	}
}

// PAIRED CONTROL (declaration half): a transformation is a pure typed conversion
// A -> B. It needs no spec.record, no producer surface, and no EventBus topic to
// be a legal declaration — requiring a record is exactly the coupling the
// transformation/delivery split cuts.
func TestValidateRecordLinkageAcceptsTransformationWithoutRecord(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"events/events.proto": `syntax = "proto3";
package events;

import "sys/spec.proto";

message SessionEvent {
  option (spec.transformation) = {
    name: "SessionRow"
    field: {source: "timestamp_ms" partition_key: true}
  };

  string event_id = 1;
  int64 timestamp_ms = 2;
}
`,
	})

	if err := ValidateRecordLinkage(dir); err != nil {
		t.Fatalf("ValidateRecordLinkage rejected a record-free transformation: %v", err)
	}
}

// The producer surface is declared, never defaulted: an event with neither c2s
// nor s2s has no publisher and must fail generation.
func TestValidateRecordLinkageRejectsEventWithoutProducerSurface(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"events/events.proto": `syntax = "proto3";
package events;

import "sys/spec.proto";

message SessionEvent {
  option (spec.event) = {};

  string event_id = 1;
}
`,
	})

	err := ValidateRecordLinkage(dir)
	if err == nil {
		t.Fatal("ValidateRecordLinkage accepted an event with no producer surface, want error")
	}
	if !strings.Contains(err.Error(), "declares no producer surface") {
		t.Fatalf("error = %q, want the missing-surface message", err.Error())
	}
}

// An internal-only event declaring just s2s is complete.
func TestValidateRecordLinkageAcceptsInternalOnlyEvent(t *testing.T) {
	dir := virtualFixture(t, map[string]string{
		"events/events.proto": `syntax = "proto3";
package events;

import "sys/spec.proto";

message SessionEvent {
  option (spec.event) = {s2s: {}};

  string event_id = 1;
}
`,
	})

	if err := ValidateRecordLinkage(dir); err != nil {
		t.Fatalf("ValidateRecordLinkage rejected an internal-only event: %v", err)
	}
}
