// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

// ValidateC2STopicActorIdentity is the identity gate on exported (C2S) topics:
// every event type a client can publish must carry a server-authoritative
// SPIFFE identity field, either a stamped subject or one tacplus constructs
// from siblings. Without it the server has nowhere to record who published the
// event, so the build must fail.

// c2sTopicFixture writes a single exported service_event bus whose topic
// carries the given event message body.
func c2sTopicFixture(t *testing.T, eventBody string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "topics/topics.proto", `syntax = "proto3";
package topics;

import "sys/spec.proto";

message HostedEvent {
`+eventBody+`}

message HostedEventBus {
  option (spec.service_state) = {
    name: "HostedEventBus"
    label: ["topics"]
    export: true
  };
  option (spec.service_event) = {};

  repeated HostedEvent events = 1 [(spec.topic) = true];
}
`)
	return dir
}

func TestValidateC2STopicActorIdentityAcceptsStampedSubject(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
`)

	if err := ValidateC2STopicActorIdentity(dir); err != nil {
		t.Fatalf("ValidateC2STopicActorIdentity rejected a topic with a stamped subject: %v", err)
	}
}

// A service-ingested topic identifies its owner by a constructed resource
// SPIFFE instead of a caller subject; that satisfies the gate too.
func TestValidateC2STopicActorIdentityAcceptsConstructedResource(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string tenant_namespace = 2;
  string owner_spiffe = 3 [(spec.identity_spiffe_resource) = {
    namespace_field: "tenant_namespace"
    kind: "hosted-event"
    id_field: "event_id"
  }];
`)

	if err := ValidateC2STopicActorIdentity(dir); err != nil {
		t.Fatalf("ValidateC2STopicActorIdentity rejected a topic with a constructed resource identity: %v", err)
	}
}

func TestValidateC2STopicActorIdentityRejectsTopicWithNoIdentityField(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string payload = 2;
`)

	err := ValidateC2STopicActorIdentity(dir)
	if err == nil {
		t.Fatal("ValidateC2STopicActorIdentity accepted a C2S topic with no SPIFFE identity field, want error")
	}
	if !strings.Contains(err.Error(), "missing a required SPIFFE identity field") {
		t.Fatalf("error = %q, want the missing-identity message", err.Error())
	}
	if !strings.Contains(err.Error(), "HostedEventBus.events") {
		t.Fatalf("error = %q, want it to name the offending topic", err.Error())
	}
}

// A human-readable claim is display metadata, not identity — it must not
// satisfy the gate on its own.
func TestValidateC2STopicActorIdentityRejectsHumanReadableClaimOnly(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string actor_email = 2 [(spec.identity_subject) = true];
`)

	err := ValidateC2STopicActorIdentity(dir)
	if err == nil {
		t.Fatal("ValidateC2STopicActorIdentity accepted identity_subject in place of a SPIFFE identity, want error")
	}
	if !strings.Contains(err.Error(), "missing a required SPIFFE identity field") {
		t.Fatalf("error = %q, want the missing-identity message", err.Error())
	}
}

// A non-exported (internal) event bus is S2S only, so the C2S identity rule
// does not apply to it.
func TestValidateC2STopicActorIdentityIgnoresNonExportedBus(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "topics/topics.proto", `syntax = "proto3";
package topics;

import "sys/spec.proto";

message HostedEvent {
  string event_id = 1;
  string payload = 2;
}

message HostedEventBus {
  option (spec.service_state) = {name: "HostedEventBus", label: ["topics"]};
  option (spec.service_event) = {};

  repeated HostedEvent events = 1 [(spec.topic) = true];
}
`)

	if err := ValidateC2STopicActorIdentity(dir); err != nil {
		t.Fatalf("ValidateC2STopicActorIdentity rejected a non-exported event bus: %v", err)
	}
}
