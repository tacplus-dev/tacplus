// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateMaxSizeRequiresBoundForRepeatedFields(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "sample.proto", `syntax = "proto3";
package sample;

message Sample {
  repeated string tags = 1;
}
`)

	err := ValidateMaxSize(dir)
	if err == nil {
		t.Fatal("expected missing assert_max_size error")
	}
	if got := err.Error(); !strings.Contains(got, "repeated field Sample.tags is missing (spec.assert_max_size)") {
		t.Fatalf("error = %q, want missing repeated field", got)
	}
}

func TestValidateMaxSizeAcceptsExplicitBound(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "sample.proto", `syntax = "proto3";
package sample;

import "sys/spec.proto";

message Sample {
  repeated string tags = 1 [(spec.assert_max_size) = 16];
}
`)

	if err := ValidateMaxSize(dir); err != nil {
		t.Fatalf("ValidateMaxSize: %v", err)
	}
}

// A message-typed collection boundary of a state root has its own eviction controls
// and is exempt from assert_max_size — this is detected by type (the old per-field
// consistency marker was removed). Regression guard for NEW-9.
func TestValidateMaxSizeExemptsStateBoundaryCollections(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "sample.proto", `syntax = "proto3";
package sample;

import "sys/spec.proto";

message Player {
  string player_id = 1 [(spec.indexed) = true];
}

message SampleState {
  option (spec.service_state) = {name: "SampleState", tag: "sample"};

  repeated Player players = 1;
}
`)

	if err := ValidateMaxSize(dir); err != nil {
		t.Fatalf("state-boundary collection should be exempt from assert_max_size: %v", err)
	}
}

// A MAP field on a state root is NOT a generated boundary (the state client rejects
// map boundaries), so it must still carry an explicit assert_max_size rather than
// escape the capacity check via the message-typed exemption. Regression guard for
// NEW-13 (map over-exemption).
func TestValidateMaxSizeRequiresBoundForStateRootMap(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "sample.proto", `syntax = "proto3";
package sample;

import "sys/spec.proto";

message SampleState {
  option (spec.service_state) = {name: "SampleState", tag: "sample"};

  map<string, string> settings = 1;
}
`)

	err := ValidateMaxSize(dir)
	if err == nil {
		t.Fatal("expected a map on a state root without assert_max_size to fail")
	}
	if got := err.Error(); !strings.Contains(got, "settings") {
		t.Fatalf("error = %q, want it to name the unbounded map field", got)
	}
}

func writeProto(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
