// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// namespaceRecordProto is a record store's row type. nsField is spliced in
// verbatim so the two controls below differ in EXACTLY the annotation under
// test and nothing else — a record that generates, and the same record with the
// (spec.store_namespace) marker removed.
func namespaceRecordProto(nsField string) string {
	return `syntax = "proto3";

package nsdemo;

import "sys/spec.proto";

message TaskRow {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
  };
  string event_id = 1;
  string owner_id = 2;
  ` + nsField + `
}
`
}

// Red control. A (spec.record) declaring no namespace marker must fail
// generation. This is the whole difference between the namespace backstop and
// (spec.store_principal): an isolation guard an author can forget protects only
// the records somebody remembered, and the record that leaks is by definition
// the one nobody thought about.
func TestRecordWithoutStoreNamespaceFailsGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, namespaceRecordProto(`string namespace = 3;`))

	err := ValidateRecordNamespaceAnnotations(dp)
	if err == nil {
		t.Fatal("a (spec.record) declaring no (spec.store_namespace) field must fail generation")
	}
	for _, want := range []string{
		"record namespace annotation errors",
		"nsdemo.TaskRow",
		"(spec.store_namespace)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err.Error(), want)
		}
	}
}

// Paired positive control. The IDENTICAL proto WITH the marker generates.
// Without this the red control above would still pass if the fixture failed for
// an unrelated reason.
func TestRecordWithStoreNamespacePassesGeneration(t *testing.T) {
	dp := writeSurfaceProtoDir(t, namespaceRecordProto(`string namespace = 3 [(spec.store_namespace) = true];`))

	if err := ValidateRecordNamespaceAnnotations(dp); err != nil {
		t.Fatalf("a (spec.record) declaring (spec.store_namespace) must generate, got: %v", err)
	}
}

// The marker must also be a singular string — the wrapper compares it against
// the caller's namespace claim, so a repeated or non-string field would emit
// code that cannot compile. Shape is enforced alongside the principal pair.
func TestStoreNamespaceMustBeSingularString(t *testing.T) {
	dp := writeSurfaceProtoDir(t, namespaceRecordProto(`repeated string namespace = 3 [(spec.store_namespace) = true];`))

	err := ValidatePrincipalAnnotations(dp)
	if err == nil {
		t.Fatal("a repeated (spec.store_namespace) field must fail generation")
	}
	if !strings.Contains(err.Error(), "(spec.store_namespace) requires a singular string field") {
		t.Fatalf("refusal must name the shape requirement, got: %v", err)
	}
}
