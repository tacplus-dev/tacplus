// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

const ownerStateImportProto = `syntax = "proto3";
package owner;

import "sys/spec.proto";

message OwnerRecord {
  string id = 1;
}

message OwnerState {
  option (spec.service_state) = {name: "OwnerState", tag: "owner", export: true};

  OwnerRecord current = 1;
}
`

const consumerStateImportProto = `syntax = "proto3";
package consumer;

import "sys/spec.proto";
import "owner.proto";

message ConsumerState {
  option (spec.service_state) = {name: "ConsumerState", tag: "consumer"};

  owner.OwnerRecord imported_current = 1 [
    (spec.state_import) = "owner.OwnerState.current"
  ];
}
`

func prepareStateImportFixture(t *testing.T, consumerProto string) string {
	t.Helper()
	return prepareStateImportFixtureWithOwner(t, ownerStateImportProto, consumerProto)
}

func prepareStateImportFixtureWithOwner(t *testing.T, ownerProto, consumerProto string) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "owner.proto", ownerProto)
	writeProto(t, dir, "consumer.proto", consumerProto)
	return dir
}

func TestResolveStatesDerivesImportsFromFieldStateImport(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)
	cfg := &tactype.Config{SelfPackage: "consumer"}

	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}

	var foundOwn, foundImport bool
	for _, state := range cfg.States {
		switch {
		case state.ProtoFile == "consumer.proto" && state.MessageName == "ConsumerState" && state.AccessMode == tactype.AccessRW:
			foundOwn = true
		case state.ProtoFile == "owner.proto" && state.MessageName == "OwnerState" && state.AccessMode == tactype.AccessRO:
			foundImport = true
		}
	}
	if !foundOwn || !foundImport {
		t.Fatalf("resolved states = %+v, want own RW ConsumerState and imported RO OwnerState", cfg.States)
	}
}

func TestResolveStatesAllowsFileLevelStateImportsWithoutFieldProjection(t *testing.T) {
	consumerProto := strings.Replace(consumerStateImportProto,
		`package consumer;`,
		`package consumer;

option (spec.state_imports) = {source: "owner.OwnerState"};`, 1)
	consumerProto = strings.Replace(consumerProto,
		`  owner.OwnerRecord imported_current = 1 [
    (spec.state_import) = "owner.OwnerState.current"
  ];`,
		`  owner.OwnerRecord imported_current = 1;`, 1)
	dir := prepareStateImportFixture(t, consumerProto)
	cfg := &tactype.Config{SelfPackage: "consumer"}

	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}

	var foundImport bool
	for _, state := range cfg.States {
		if state.ProtoFile == "owner.proto" && state.MessageName == "OwnerState" && state.AccessMode == tactype.AccessRO {
			foundImport = true
		}
	}
	if !foundImport {
		t.Fatalf("resolved states = %+v, want imported RO OwnerState from file-level state_imports", cfg.States)
	}
}

func TestResolveStatesAllowsExactLegacyStateImportsMirror(t *testing.T) {
	consumerProto := strings.Replace(consumerStateImportProto,
		`package consumer;`,
		`package consumer;

option (spec.state_imports) = {source: "owner.OwnerState"};`, 1)
	dir := prepareStateImportFixture(t, consumerProto)
	cfg := &tactype.Config{SelfPackage: "consumer"}

	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}
}

func TestResolveStatesAllowsPackageWideLegacyStateImportsMirror(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)
	writeProto(t, dir, "consumer_imports.proto", `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.state_imports) = {source: "owner.OwnerState"};

message ConsumerImportsAnchor {}
`)
	cfg := &tactype.Config{SelfPackage: "consumer"}

	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}
}

func TestValidateStateImportRejectsRepeatedProjection(t *testing.T) {
	consumerProto := strings.Replace(consumerStateImportProto,
		`  owner.OwnerRecord imported_current = 1 [`,
		`  repeated owner.OwnerRecord imported_current = 1 [`, 1)
	dir := prepareStateImportFixture(t, consumerProto)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("ValidateStateConsistency succeeded, want repeated state_import rejection")
	}
	if !strings.Contains(err.Error(), "state_import currently supports singular message projections only") {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

func TestValidateStateImportRejectsMissingSourceField(t *testing.T) {
	consumerProto := strings.Replace(consumerStateImportProto,
		`owner.OwnerState.current`,
		`owner.OwnerState.missing`, 1)
	dir := prepareStateImportFixture(t, consumerProto)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("ValidateStateConsistency succeeded, want missing source field rejection")
	}
	if !strings.Contains(err.Error(), `references missing source field "missing"`) {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

// A map field directly on a state root is not a valid boundary and must be rejected
// loudly (not silently skipped). Regression guard for the map-on-state-root policy.
func TestValidateStateConsistencyRejectsMapBoundary(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message DemoState {
  option (spec.service_state) = {name: "DemoState", tag: "demo"};

  map<string, string> settings = 1;
}
`)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("expected a map field on a state root to be rejected")
	}
	if !strings.Contains(err.Error(), "is a map") {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

func TestValidateStateConsistencyRejectsScalarBoundaryAnnotation(t *testing.T) {
	// The boundary collector silently skips non-message state-root fields, so a
	// boundary/storage annotation on a scalar generates no state surface at all.
	// Each must be rejected loudly (NEW-23 / H-3), across the full annotation set.
	cases := []struct {
		name  string
		field string
	}{
		{"epoch_expiration", `int64 lease_expires_at = 1 [(spec.epoch_expiration) = true];`},
		{"indexed", `int64 idx = 1 [(spec.indexed) = true];`},
		{"persistence", `string blob = 1 [(spec.persistence) = true];`},
		{"queue", `string tasks = 1 [(spec.queue) = true];`},
		{"topic", `string events = 1 [(spec.topic) = true];`},
		{"leader", `string task_leader = 1 [(spec.leader) = true];`},
		{"collected_health", `string health = 1 [(spec.collected_health) = true];`},
		{"eviction_max_size", `int64 count = 1 [(spec.eviction_max_size) = 100];`},
		{"refresh_on_miss", `string cache_key = 1 [(spec.refresh_on_miss) = "LookupReq"];`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := CopySysProtos(dir); err != nil {
				t.Fatalf("CopySysProtos: %v", err)
			}
			if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
				t.Fatalf("writeMinimalGeneratedStubs: %v", err)
			}
			writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message DemoState {
  option (spec.service_state) = {name: "DemoState", tag: "demo"};

  `+tc.field+`
}
`)

			err := ValidateStateConsistency(dir)
			if err == nil {
				t.Fatalf("expected scalar state-root field with (spec.%s) to be rejected", tc.name)
			}
			if !strings.Contains(err.Error(), "is a scalar on state root") {
				t.Fatalf("ValidateStateConsistency error = %v", err)
			}
		})
	}
}

func TestValidateStateConsistencyRejectsMalformedSubscribeRef(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	// A subscribe source that is not <pkg>.<message>.<field> is malformed; it must
	// be rejected loudly rather than silently dropped by the boundary collector (MS-3).
	writeProto(t, dir, "demo.proto", `syntax = "proto3";
package demo;

import "sys/spec.proto";

message DemoState {
  option (spec.service_state) = {name: "DemoState", tag: "demo"};
  option (spec.subscribe) = {source: "not_a_valid_ref"};

  string name = 1;
}
`)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("expected a malformed (spec.subscribe) source to be rejected")
	}
	if !strings.Contains(err.Error(), "malformed (spec.subscribe) source") {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

func TestValidateStateImportRejectsNonExportedRoot(t *testing.T) {
	ownerProto := strings.Replace(ownerStateImportProto, `, export: true`, ``, 1)
	dir := prepareStateImportFixtureWithOwner(t, ownerProto, consumerStateImportProto)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("ValidateStateConsistency succeeded, want non-exported root rejection")
	}
	if !strings.Contains(err.Error(), "references non-exported state_import root") {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

func TestValidateStateImportRejectsTypeMismatch(t *testing.T) {
	consumerProto := strings.Replace(consumerStateImportProto,
		`message ConsumerState {`,
		`message OtherRecord { string id = 1; }

message ConsumerState {`, 1)
	consumerProto = strings.Replace(consumerProto,
		`owner.OwnerRecord imported_current = 1 [`,
		`OtherRecord imported_current = 1 [`, 1)
	dir := prepareStateImportFixture(t, consumerProto)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("ValidateStateConsistency succeeded, want state_import type mismatch rejection")
	}
	if !strings.Contains(err.Error(), "does not match state_import source") {
		t.Fatalf("ValidateStateConsistency error = %v", err)
	}
}

// MS-7: a backing-store service (spec.service {store: true}) PHYSICALLY HOLDS
// every distributed state partition, so it resolves RW to ALL states in the
// build — foreign packages included — by design, not over-broad tagging. This
// pins the intended behavior (which the "tagged to them" comment obscured) so
// a future change that narrowed the backing store's reach would fail.
func TestResolveStatesBackingStoreGetsRWToAllPartitions(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	// The backing store itself (store: true), with its own state.
	writeProto(t, dir, "kernel.proto", `syntax = "proto3";
package kernel;

import "sys/spec.proto";

option (spec.service) = {name: "kernel", store: true};

message KernelState {
  option (spec.service_state) = {name: "KernelState", tag: "kernel", export: true};
  string x = 1;
}
`)
	// Two FOREIGN state partitions the kernel does not own.
	writeProto(t, dir, "owner.proto", ownerStateImportProto)
	writeProto(t, dir, "other.proto", `syntax = "proto3";
package other;

import "sys/spec.proto";

message OtherState {
  option (spec.service_state) = {name: "OtherState", tag: "other", export: true};
  string y = 1;
}
`)

	cfg := &tactype.Config{SelfPackage: "kernel"}
	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}

	want := map[string]bool{"KernelState": false, "OwnerState": false, "OtherState": false}
	for _, st := range cfg.States {
		if _, ok := want[st.MessageName]; ok {
			if st.AccessMode != tactype.AccessRW {
				t.Errorf("backing store must have RW to %q, got %v", st.MessageName, st.AccessMode)
			}
			want[st.MessageName] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("backing store did not resolve state %q (must reach EVERY partition): %+v", name, cfg.States)
		}
	}
}

// Complement: an ORDINARY (non-store) service resolves only its OWN state, not
// foreign partitions — the backing-store all-states grant is store-specific.
func TestResolveStatesOrdinaryServiceGetsOnlyOwnState(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "owner.proto", ownerStateImportProto)
	writeProto(t, dir, "other.proto", `syntax = "proto3";
package other;

import "sys/spec.proto";

message OtherState {
  option (spec.service_state) = {name: "OtherState", tag: "other", export: true};
  string y = 1;
}
`)

	cfg := &tactype.Config{SelfPackage: "owner"}
	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}
	for _, st := range cfg.States {
		if st.MessageName == "OtherState" {
			t.Fatalf("an ordinary service must NOT resolve a foreign state (%q) it does not own or import: %+v", st.MessageName, cfg.States)
		}
	}
}
