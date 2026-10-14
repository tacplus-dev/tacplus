// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// routingOnlyProto declares two record topics on one bus. Only Delivery is
// flagged routing_only_services; Plain is the paired control that must keep
// delivering its whole self to every subscriber.
const routingOnlyProto = `syntax = "proto3";
package mail;

import "sys/spec.proto";

message Delivery {
  option (spec.record) = {
    key: "delivery_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["*"]}
    s2s_subscribe: {
      auth_services: ["dispatcher", "stratum"]
      routing_only_services: ["dispatcher"]
    }
  };
  string delivery_id = 1;
  string recipient = 2 [(spec.store_principal) = true];
  string tenant = 3 [(spec.store_namespace) = true];
  string spiffe_id = 4;
  string body = 5;
}

message Plain {
  option (spec.record) = {
    key: "plain_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["*"]}
    s2s_subscribe: {auth_services: ["dispatcher"]}
  };
  string plain_id = 1;
  string tenant = 2 [(spec.store_namespace) = true];
  string spiffe_id = 3;
}

message MailBus {
  Delivery delivery_stream = 1;
  Plain plain_stream = 2;
}
`

// A subscriber named in routing_only_services must resolve to the three source
// fields the envelope projects, and a subscriber that is NOT named must resolve
// to nothing at all — the narrowing is per-consumer, declared by the owner.
//
// Paired control: Plain declares an s2s_subscribe with no routing_only_services
// and must come back not-ok, so an ordinary topic keeps delivering the full
// message exactly as it did before the field existed.
func TestRoutingEnvelopeResolvesPerConsumer(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, routingOnlyProto)
	p, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	env, ok, err := boundaries.LookupRoutingEnvelope(p, "mail.Delivery")
	if err != nil {
		t.Fatalf("LookupRoutingEnvelope(mail.Delivery): %v", err)
	}
	if !ok {
		t.Fatal("mail.Delivery declares routing_only_services but did not resolve an envelope")
	}
	if env.KeyField != "delivery_id" || env.PrincipalField != "recipient" || env.NamespaceField != "tenant" {
		t.Fatalf("envelope fields = {%q, %q, %q}, want {delivery_id, recipient, tenant}",
			env.KeyField, env.PrincipalField, env.NamespaceField)
	}
	if !env.Covers("dispatcher") {
		t.Fatal("dispatcher is named in routing_only_services but is not covered")
	}
	if env.Covers("stratum") {
		t.Fatal("stratum is authorized to subscribe but is NOT routing-only; it must get the full message")
	}

	if _, ok, err := boundaries.LookupRoutingEnvelope(p, "mail.Plain"); err != nil || ok {
		t.Fatalf("mail.Plain declares no routing_only_services; got ok=%v err=%v", ok, err)
	}
}

// The synthesized envelope is a real proto message in the source type's own
// package — that is what lets every language backend spell it as <T>RoutingEnvelope
// with no import of its own. Only the flagged type gets one.
func TestAppendRoutingEnvelopesSynthesizesOnlyFlaggedTypes(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, routingOnlyProto)

	if err := AppendRoutingEnvelopesToProtos(dir, dir); err != nil {
		t.Fatalf("AppendRoutingEnvelopesToProtos: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "vservices", "tasks.proto"))
	if err != nil {
		t.Fatalf("read fixture proto: %v", err)
	}
	got := string(body)

	for _, want := range []string{
		"message DeliveryRoutingEnvelope {",
		"string delivery_id = 1;",
		"string recipient = 2;",
		"string namespace = 3;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("synthesized proto missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "message PlainRoutingEnvelope") {
		t.Fatal("Plain declares no routing_only_services and must get no envelope")
	}
}

// REFUSAL. A type flagged routing_only_services with no (spec.store_principal)
// has no recipient to put in the envelope. An envelope with an empty recipient
// is a routing table nobody can route with, so generation FAILS rather than
// emitting one — the watch_egress_gen.go posture.
func TestRoutingEnvelopeRefusesWithoutStorePrincipal(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, `syntax = "proto3";
package mail;

import "sys/spec.proto";

message Delivery {
  option (spec.record) = {
    key: "delivery_id"
    owner: "spiffe_id"
    s2s: {auth_services: ["*"]}
    s2s_subscribe: {
      auth_services: ["dispatcher"]
      routing_only_services: ["dispatcher"]
    }
  };
  string delivery_id = 1;
  string tenant = 2 [(spec.store_namespace) = true];
  string spiffe_id = 3;
  string body = 4;
}

message MailBus {
  Delivery delivery_stream = 1;
}
`)

	err := AppendRoutingEnvelopesToProtos(dir, dir)
	if err == nil {
		t.Fatal("a routing_only_services type with no (spec.store_principal) must refuse generation")
	}
	for _, want := range []string{"routing_only_services", "store_principal", "recipient"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name %q; got: %v", want, err)
		}
	}
}
