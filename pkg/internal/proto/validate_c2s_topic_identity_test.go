// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

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

// (spec.identity_spiffe_actor) is OPTIONAL provenance metadata for the
// delegated-mint actor — a topic type may declare exactly one such field
// alongside its required subject/resource identity, and the gate must accept
// it.
func TestValidateC2STopicActorIdentityAcceptsOptionalActorField(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
  string actor_spiffe_id = 3 [(spec.identity_spiffe_actor) = true];
`)

	if err := ValidateC2STopicActorIdentity(dir); err != nil {
		t.Fatalf("ValidateC2STopicActorIdentity rejected a topic with a valid optional actor field: %v", err)
	}
}

// At most one (spec.identity_spiffe_actor) field is allowed per topic type.
func TestValidateC2STopicActorIdentityRejectsTwoActorFields(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
  string actor_spiffe_id = 3 [(spec.identity_spiffe_actor) = true];
  string other_actor_spiffe_id = 4 [(spec.identity_spiffe_actor) = true];
`)

	err := ValidateC2STopicActorIdentity(dir)
	if err == nil {
		t.Fatal("ValidateC2STopicActorIdentity accepted a topic with two (spec.identity_spiffe_actor) fields, want error")
	}
	if !strings.Contains(err.Error(), "at most one is allowed") {
		t.Fatalf("error = %q, want the at-most-one-actor-field message", err.Error())
	}
}

// A field must never be both (spec.identity_spiffe_actor) and
// (spec.store_principal) — the actor must never appear in a store's read
// filter. This is a build-time assertion of that invariant, not just
// convention.
func TestValidateC2STopicActorIdentityRejectsActorFieldAlsoStorePrincipal(t *testing.T) {
	dir := c2sTopicFixture(t, `  string event_id = 1;
  string spiffe_id = 2 [(spec.identity_spiffe_subject) = true];
  string actor_spiffe_id = 3 [(spec.identity_spiffe_actor) = true, (spec.store_principal) = true];
`)

	err := ValidateC2STopicActorIdentity(dir)
	if err == nil {
		t.Fatal("ValidateC2STopicActorIdentity accepted a field annotated both identity_spiffe_actor and store_principal, want error")
	}
	if !strings.Contains(err.Error(), "identity_spiffe_actor") || !strings.Contains(err.Error(), "store_principal") {
		t.Fatalf("error = %q, want it to name both annotations", err.Error())
	}
}

