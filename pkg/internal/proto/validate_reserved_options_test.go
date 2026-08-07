// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeProtoFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A reserved field inside an active spec option literal must hard-error, with
// the file, the field, and the replacement named.
func TestValidateReservedSpecOptionFields_Rejects(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "vservices/demo.proto", `syntax = "proto3";
package demo;
message DemoEvents {
  option (spec.timeseries_projection) = {
    name: "DemoRecord"
    engine: "DemoEngine(ts)"
    ttl: "toDateTime(ts) + INTERVAL 90 DAY"
    field: {source: "kind" low_cardinality: true}
  };
}
`)
	err := ValidateReservedSpecOptionFields(dir)
	if err == nil {
		t.Fatal("expected reserved-field error, got nil")
	}
	for _, want := range []string{"vservices/demo.proto", "engine", "ttl", "low_cardinality", "cardinality_hint"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

// Commented-out options, reserved names inside STRING VALUES, in // comments
// inside a block, and ordinary message fields named like reserved fields must
// NOT trigger (comment-safe + string-safe).
func TestValidateReservedSpecOptionFields_NoFalsePositives(t *testing.T) {
	dir := t.TempDir()
	writeProtoFile(t, dir, "services/clean.proto", `syntax = "proto3";
package clean;
// option (spec.timeseries_projection) = { engine: "old" ttl: "old" };
message CleanEvents {
  option (spec.timeseries_projection) = {
    name: "CleanRecord"
    // engine: "commented inside block"
    when: "ev"
    retention: {timestamp_field: "ts" duration_seconds: 60}
    field: {source: "note" as: "engine_note"}
    field: {source: "desc" cardinality_hint: CARDINALITY_LOW}
  };
}
message Unrelated {
  string engine = 1; // a normal field NAMED engine is fine (no colon syntax)
  int64 ttl_seconds = 2;
  string description = 3; // mentions ttl: and engine: in prose? only in comment
}
message StringCarrier {
  option (spec.record) = {key: "id" owner: "who"};
  string id = 1;
  string who = 2;
  string query = 3; // e.g. value might be "SELECT ... TTL ..." at runtime; not proto text
}
`)
	if err := ValidateReservedSpecOptionFields(dir); err != nil {
		t.Fatalf("clean proto must pass, got: %v", err)
	}
}

// The reserved table must stay in lockstep with sys/spec.proto's actual
// reserved declarations — if a field is reserved there but missing here (or
// vice versa for the names we claim), this drift-pin fails.
func TestReservedTableMatchesSpecProto(t *testing.T) {
	// Locate sys/spec.proto relative to the repo root.
	specPath := filepath.Join("..", "..", "proto", "sys", "spec.proto")
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read spec.proto: %v", err)
	}
	re := regexp.MustCompile(`reserved\s+((?:"[a-z_]+"(?:\s*,\s*)?)+)\s*;`)
	nameRe := regexp.MustCompile(`"([a-z_]+)"`)
	declared := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(data), -1) {
		for _, n := range nameRe.FindAllStringSubmatch(m[1], -1) {
			declared[n[1]] = true
		}
	}
	if len(declared) == 0 {
		t.Fatal("no reserved names parsed from spec.proto — pin is broken")
	}
	// Every declared reserved name that our validator claims must be declared.
	for name := range reservedSpecOptionFields {
		if !declared[name] {
			t.Errorf("validator table has %q but spec.proto does not reserve it", name)
		}
	}
	// Every reserved name in spec.proto should be covered by the validator,
	// except names too generic to scan safely (documented exclusions).
	excluded := map[string]bool{"type": true} // too generic: matches ordinary text-format usage
	for name := range declared {
		if !excluded[name] && reservedSpecOptionFields[name] == "" {
			t.Errorf("spec.proto reserves %q but the validator table does not cover it", name)
		}
	}
}
