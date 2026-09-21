// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// recordPrivateProto is a record store's row type. private/subscribe are
// spliced in verbatim so the controls below differ in EXACTLY the annotations
// under test.
func recordPrivateProto(private, subscribe string) string {
	return `syntax = "proto3";

package privatedemo;

import "sys/spec.proto";

message RuleNode {
  option (spec.record) = {
    key: "rule_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    ` + subscribe + `
    ` + private + `
  };
  string rule_id = 1;
  string owner_id = 2;
  string namespace = 3 [(spec.store_namespace) = true];
}
`
}

// Red control. A private record declaring s2s_subscribe must fail generation:
// a subscriber is a service with no per-request caller to pin a row's owner
// against, so this would broadcast every owner's rows to any authorized peer.
func TestPrivateRecordWithS2SSubscribeFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, `s2s_subscribe: {auth_services: ["stratum"]}`))

	err := ValidateRecordPrivateAnnotations(dp)
	if err == nil {
		t.Fatal("a private (spec.record) declaring s2s_subscribe must fail generation")
	}
	for _, want := range []string{
		"record private annotation errors",
		"privatedemo.RuleNode",
		"s2s_subscribe",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Paired positive control. The identical private record WITHOUT s2s_subscribe
// generates — private does not forbid the record plane's other surfaces.
func TestPrivateRecordWithoutS2SSubscribePassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, ``))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a private (spec.record) declaring no s2s_subscribe must generate, got: %v", err)
	}
}

// Regression guard: a NON-private record declaring s2s_subscribe is
// untouched — this check exists for private records only.
func TestNonPrivateRecordWithS2SSubscribePassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(``, `s2s_subscribe: {auth_services: ["stratum"]}`))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a non-private record's s2s_subscribe must not be refused by this check, got: %v", err)
	}
}
