// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// GEN-ANN-1. spec.proto declares replica-count and eviction tuning annotations
// with worked examples, and docs/USER.md showed (spec.eviction_ttl_seconds) as a
// working retention control. No collector, template or emitter in any of the
// seven target languages reads any of them — the one implemented member of the
// family is (spec.eviction_max_size). Declaring one used to produce a boundary
// with none of the requested behavior and no diagnostic, so an operator's
// durability or retention intent was lost silently. Each must now be refused.
func TestValidateStateConsistencyRejectsUnimplementedStatedbAnnotations(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"sync_backup_count", `Item roster = 1 [(spec.sync_backup_count) = 2];`},
		{"async_backup_count", `Item roster = 1 [(spec.async_backup_count) = 1];`},
		{"eviction_policy", `Item sessions = 1 [(spec.eviction_policy) = EVICTION_POLICY_LRU];`},
		{"eviction_max_size_policy", `Item sessions = 1 [(spec.eviction_max_size_policy) = MAX_SIZE_POLICY_USED_HEAP_SIZE];`},
		{"eviction_ttl_seconds", `Item sessions = 1 [(spec.eviction_ttl_seconds) = 3600];`},
		{"eviction_max_idle_seconds", `Item sessions = 1 [(spec.eviction_max_idle_seconds) = 900];`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := unimplementedStatedbProbeDir(t, `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["demo"]};

  `+tc.field+`
}
`)
			err := ValidateStateConsistency(dir)
			if err == nil {
				t.Fatalf("expected (spec.%s) to be rejected: tacplus does not implement it, so it would silently generate nothing", tc.name)
			}
			if !strings.Contains(err.Error(), "(spec."+tc.name+"), which tacplus declares but does not implement") {
				t.Fatalf("ValidateStateConsistency error = %v", err)
			}
		})
	}
}

// The message-level state-root defaults for the same three concerns are equally
// inert and equally silent.
func TestValidateStateConsistencyRejectsUnimplementedStatedbStateDefaults(t *testing.T) {
	cases := []struct {
		name   string
		option string
	}{
		{"state_eviction_policy", `option (spec.state_eviction_policy) = EVICTION_POLICY_LRU;`},
		{"state_sync_backup_count", `option (spec.state_sync_backup_count) = 2;`},
		{"state_async_backup_count", `option (spec.state_async_backup_count) = 1;`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := unimplementedStatedbProbeDir(t, `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["demo"]};
  `+tc.option+`

  Item roster = 1;
}
`)
			err := ValidateStateConsistency(dir)
			if err == nil {
				t.Fatalf("expected (spec.%s) to be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), "(spec."+tc.name+"), which tacplus declares but does not implement") {
				t.Fatalf("ValidateStateConsistency error = %v", err)
			}
		})
	}
}

// (spec.eviction_max_size) is the one member of the family a generator really
// consumes (boundaries.go -> the Go state wrapper's entry bound), so the
// rejection must not swallow it too.
func TestValidateStateConsistencyStillAcceptsEvictionMaxSize(t *testing.T) {
	dir := unimplementedStatedbProbeDir(t, `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["demo"]};

  Item sessions = 1 [(spec.eviction_max_size) = 100];
}
`)
	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("(spec.eviction_max_size) is implemented and must still validate: %v", err)
	}
}

// The rejection list and the parser's presence markers are one set with two
// halves in different packages. A name the validator rejects but the parser never
// records is a dead rule that silently accepts the annotation again — the exact
// failure mode this closes.
func TestUnimplementedStatedbAnnotationsAreAllParsed(t *testing.T) {
	names := parser.UnimplementedStatedbAnnotations()
	if len(names) != 9 {
		t.Fatalf("expected 6 field-level + 3 message-level annotations, got %d: %v", len(names), names)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			field := "Item roster = 1 [(spec." + name + ") = 1];"
			option := ""
			if strings.HasPrefix(name, "state_") {
				field = "Item roster = 1;"
				option = "option (spec." + name + ") = 1;"
			}
			switch name {
			case "eviction_policy":
				field = `Item roster = 1 [(spec.eviction_policy) = EVICTION_POLICY_LRU];`
			case "eviction_max_size_policy":
				field = `Item roster = 1 [(spec.eviction_max_size_policy) = MAX_SIZE_POLICY_USED_HEAP_SIZE];`
			case "state_eviction_policy":
				option = `option (spec.state_eviction_policy) = EVICTION_POLICY_LRU;`
			}
			dir := unimplementedStatedbProbeDir(t, `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["demo"]};
  `+option+`

  `+field+`
}
`)
			err := ValidateStateConsistency(dir)
			if err == nil || !strings.Contains(err.Error(), "(spec."+name+")") {
				t.Fatalf("parser does not record (spec.%s), so the rejection can never fire; error = %v", name, err)
			}
		})
	}
}

func unimplementedStatedbProbeDir(t *testing.T, protoSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "demo.proto", protoSrc)
	return dir
}
