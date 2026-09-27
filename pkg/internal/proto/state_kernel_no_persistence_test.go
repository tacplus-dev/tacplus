// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

// Decision 2026-09-25 (`kernel-no-durability-user-data-to-stratum`): kernel holds
// no durable user data. It is in-memory coordination plus its own identity
// snapshot; a boundary that must survive a cold start is a stratum record. The
// 2026-09-23 decision that made kernel boundaries durable is reversed, which turns
// (spec.persistence) on a kernel-labelled state root from "the one place it works"
// into a generation error — the annotation now describes an intent NO store
// implements, and on kernel it describes one kernel is specifically not allowed to
// grow back.
//
// The error phrase is pinned verbatim because it is the whole diagnostic: it tells
// the proto author where the data belongs, not merely that the annotation is
// refused.
const kernelPersistenceRefusal = "durable data belongs in stratum records"

// FIRES. Every way a state root can be kernel-labelled: alone, and alongside
// another store's label (the routing list is repeated, and one kernel entry
// anywhere in it is enough — the boundary still lands in kernel).
func TestValidateStateConsistencyRejectsPersistenceOnKernelLabelledState(t *testing.T) {
	cases := []struct {
		name  string
		label string
	}{
		{"kernel_only", `["kernel"]`},
		{"kernel_first_of_many", `["kernel", "edge"]`},
		{"kernel_last_of_many", `["edge", "kernel"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := unimplementedStatedbProbeDir(t, kernelPersistenceProbe(tc.label,
				`Item roster = 1 [(spec.persistence) = true];`))
			err := ValidateStateConsistency(dir)
			if err == nil {
				t.Fatalf("(spec.persistence) on a state root labelled %s must be refused: kernel holds no "+
					"durable user data, so the annotation asks for something kernel will never do", tc.label)
			}
			if !strings.Contains(err.Error(), kernelPersistenceRefusal) {
				t.Fatalf("refusal must name where the data belongs (%q); error = %v", kernelPersistenceRefusal, err)
			}
			if !strings.Contains(err.Error(), "DemoState.roster") {
				t.Fatalf("refusal must name the offending field; error = %v", err)
			}
		})
	}
}

// DOES NOT FIRE. The SAME annotation on a non-kernel-labelled state root stays
// valid: it is still transported to registry.json (BoundaryInfo.persistence) for a
// store that implements it, and refusing it everywhere would delete the annotation
// rather than restrict it. This is the half that makes the rule a placement rule.
func TestValidateStateConsistencyAcceptsPersistenceOnNonKernelLabelledState(t *testing.T) {
	for _, label := range []string{`["demo"]`, `["edge"]`, `["edge", "demo"]`} {
		t.Run(label, func(t *testing.T) {
			dir := unimplementedStatedbProbeDir(t, kernelPersistenceProbe(label,
				`Item roster = 1 [(spec.persistence) = true];`))
			if err := ValidateStateConsistency(dir); err != nil {
				t.Fatalf("(spec.persistence) on a state root labelled %s must still validate: %v", label, err)
			}
		})
	}
}

// DOES NOT FIRE. A kernel-labelled state root without the annotation is the fleet's
// normal shape — the rule must key off the annotation, not off the label.
func TestValidateStateConsistencyAcceptsKernelLabelledStateWithoutPersistence(t *testing.T) {
	dir := unimplementedStatedbProbeDir(t, kernelPersistenceProbe(`["kernel"]`, `Item roster = 1;`))
	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("a kernel-labelled state root with no (spec.persistence) must validate: %v", err)
	}
}

// DOES NOT FIRE. identity_snapshot: true is the one named exception in the
// decision this rule implements ("[Kernel] keeps exactly one durable thing:
// its OWN identity snapshot") — a kernel-labelled root marked as such may
// carry (spec.persistence). Confirmed against the real fleet shape: kernel's
// Identity, PrincipalState and ConfigState roots all set this and all carry
// (spec.persistence) on some fields (platform/proto/services/kernel.proto).
func TestValidateStateConsistencyAcceptsPersistenceOnIdentitySnapshotState(t *testing.T) {
	dir := unimplementedStatedbProbeDir(t, kernelIdentitySnapshotProbe(
		`Item roster = 1 [(spec.persistence) = true];`))
	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("(spec.persistence) on an identity_snapshot kernel state root must validate: %v", err)
	}
}

// FIRES. identity_snapshot only exempts the root it is set on — a sibling
// kernel-labelled root without it still gets the ordinary Rule 3b refusal.
func TestValidateStateConsistencyRejectsPersistenceOnNonIdentitySnapshotKernelState(t *testing.T) {
	dir := unimplementedStatedbProbeDir(t, kernelPersistenceProbe(`["kernel"]`,
		`Item roster = 1 [(spec.persistence) = true];`))
	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("(spec.persistence) on a kernel-labelled root without identity_snapshot must still be refused")
	}
	if !strings.Contains(err.Error(), kernelPersistenceRefusal) {
		t.Fatalf("refusal must name where the data belongs (%q); error = %v", kernelPersistenceRefusal, err)
	}
}

func kernelPersistenceProbe(label, field string) string {
	return `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ` + label + `};

  ` + field + `
}
`
}

func kernelIdentitySnapshotProbe(field string) string {
	return `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["kernel"], identity_snapshot: true};

  ` + field + `
}
`
}
