// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// recordPrivateProto is a record store's row type. private/surface/ownerField
// are spliced in verbatim so the controls below differ in EXACTLY the
// annotations under test. surface carries the one read/subscribe grant a case
// is about (s2s_subscribe or s2s_read), and ownerField lets a control omit
// (spec.store_principal) entirely to prove it's mandatory on a private record.
func recordPrivateProto(private, surface, ownerField string) string {
	return `syntax = "proto3";

package privatedemo;

import "sys/spec.proto";

message RuleNode {
  option (spec.record) = {
    key: "rule_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    ` + surface + `
    ` + private + `
  };
  string rule_id = 1;
  ` + ownerField + `
  string namespace = 3 [(spec.store_namespace) = true];
}
`
}

// recordPrivateOwnerField is the (spec.store_principal)-annotated owner field
// every positive/red-s2s-subscribe control below uses; only the two new
// store_principal tests vary it.
const recordPrivateOwnerField = `string owner_id = 2 [(spec.store_principal) = true];`

// Red control. A private record declaring s2s_subscribe must fail generation:
// a subscriber is a service with no per-request caller to pin a row's owner
// against, so this would broadcast every owner's rows to any authorized peer.
func TestPrivateRecordWithS2SSubscribeFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, `s2s_subscribe: {auth_services: ["stratum"]}`, recordPrivateOwnerField))

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
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, ``, recordPrivateOwnerField))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a private (spec.record) declaring no s2s_subscribe must generate, got: %v", err)
	}
}

// Regression guard: a NON-private record declaring s2s_subscribe is
// untouched — this check exists for private records only.
func TestNonPrivateRecordWithS2SSubscribePassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(``, `s2s_subscribe: {auth_services: ["stratum"]}`, recordPrivateOwnerField))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a non-private record's s2s_subscribe must not be refused by this check, got: %v", err)
	}
}

// Red control. A private record declaring no (spec.store_principal) field has
// nothing for the (not-yet-emitted) owner pin to bind to — the same "record
// nobody remembered to annotate" failure store_namespace's mandatory-field
// check already exists to catch, applied here because private is a stronger
// claim.
func TestPrivateRecordWithoutStorePrincipalFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, ``, `string owner_id = 2;`))

	err := ValidateRecordPrivateAnnotations(dp)
	if err == nil {
		t.Fatal("a private (spec.record) declaring no (spec.store_principal) field must fail generation")
	}
	for _, want := range []string{
		"record private annotation errors",
		"privatedemo.RuleNode",
		"(spec.store_principal)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Paired positive control. The identical private record WITH the
// store_principal field generates.
func TestPrivateRecordWithStorePrincipalPassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, ``, recordPrivateOwnerField))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a private (spec.record) declaring (spec.store_principal) must generate, got: %v", err)
	}
}

// Red control. A private record declaring s2s_read must fail generation. The
// record query plane emits no peer-facing read for a private type (record_map.go
// skips it when rendering the S2S half), because a peer calls that surface with
// its own service identity and the owner pin has no caller principal to compare
// rows against. The grant would therefore authorize nothing — and before the
// skip landed it authorized every named service to read every principal's rows
// in the tenant, since the only gate the wrapper generators can apply there is
// the (spec.store_namespace) tenant backstop.
func TestPrivateRecordWithS2SReadFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(`private: true`, `s2s_read: {auth_services: ["profile"]}`, recordPrivateOwnerField))

	err := ValidateRecordPrivateAnnotations(dp)
	if err == nil {
		t.Fatal("a private (spec.record) declaring s2s_read must fail generation")
	}
	for _, want := range []string{
		"record private annotation errors",
		"privatedemo.RuleNode",
		"s2s_read",
		"c2s half",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Regression guard: a NON-private record declaring s2s_read is untouched. The
// peer-facing read is the ordinary shape for a tenant-scoped record, where the
// namespace backstop IS the whole boundary the record claims.
func TestNonPrivateRecordWithS2SReadPassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, recordPrivateProto(``, `s2s_read: {auth_services: ["profile"]}`, recordPrivateOwnerField))

	if err := ValidateRecordPrivateAnnotations(dp); err != nil {
		t.Fatalf("a non-private record's s2s_read must not be refused by this check, got: %v", err)
	}
}
