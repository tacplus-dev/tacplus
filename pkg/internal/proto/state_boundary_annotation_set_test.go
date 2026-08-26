// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// stateBoundaryFieldAnnotations is a hand-maintained set (NEW-23 leg b): nothing
// mechanically ties it to what the descriptor parser actually puts into
// field.Annotations, so a typo, a rename, or a newly-parsed boundary annotation
// would go unnoticed and silently guard nothing. These two guards close both
// directions against the parser itself.

// The proto below carries every annotation the state.go set names PLUS the six
// its doc comment records as "valid in spec.proto but not parsed into
// field.Annotations". All of them sit on ONE message-typed field, which is where
// each is legal, so the parser is exercised exactly as production does.
const boundaryAnnotationProbeProto = `syntax = "proto3";
package demo;

import "sys/spec.proto";

message Item {
  string id = 1;
}

message DemoState {
  option (spec.service_state) = {name: "DemoState", label: ["demo"]};

  Item everything = 1 [
    (spec.epoch_expiration) = true,
    (spec.indexed) = true,
    (spec.persistence) = true,
    (spec.queue) = true,
    (spec.priority_queue) = "severity",
    (spec.topic) = true,
    (spec.command) = true,
    (spec.view_sources) = "other",
    (spec.view_member) = "view",
    (spec.instance_keyed) = true,
    (spec.leader) = true,
    (spec.collected_health) = true,
    (spec.eviction_max_size) = 100,
    (spec.refresh_on_miss) = "LookupReq",
    (spec.sync_backup_count) = 2,
    (spec.async_backup_count) = 1,
    (spec.eviction_policy) = EVICTION_POLICY_LRU,
    (spec.eviction_max_size_policy) = MAX_SIZE_POLICY_USED_HEAP_SIZE,
    (spec.eviction_ttl_seconds) = 60,
    (spec.eviction_max_idle_seconds) = 30
  ];
}
`

func boundaryAnnotationProbeFields(t *testing.T) map[string]struct{} {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "demo.proto", boundaryAnnotationProbeProto)

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	for _, fd := range dp.GetAllFiles() {
		for _, msg := range fd.Messages {
			if msg.Name != "DemoState" {
				continue
			}
			for _, field := range msg.Fields {
				if field.Name != "everything" {
					continue
				}
				got := make(map[string]struct{}, len(field.Annotations))
				for name := range field.Annotations {
					got[name] = struct{}{}
				}
				return got
			}
		}
	}
	t.Fatal("probe proto did not yield DemoState.everything")
	return nil
}

// Every name in the set must be one the parser really produces. A dead entry
// (typo, renamed annotation) guards nothing: ValidateStateConsistency would
// never see it in field.Annotations, so the scalar-boundary rejection it is
// supposed to drive would silently stop firing.
func TestStateBoundaryFieldAnnotationsAreAllParsed(t *testing.T) {
	got := boundaryAnnotationProbeFields(t)
	for _, name := range stateBoundaryFieldAnnotations {
		if _, ok := got[name]; !ok {
			t.Errorf("stateBoundaryFieldAnnotations names %q, but the descriptor parser never puts it in field.Annotations — "+
				"the scalar state-root rejection for it can never fire; fix the name or drop the entry", name)
		}
	}
}

// The other direction. These six were previously unparsed, and therefore
// uncheckable, so a scalar state root wearing one was fail-open. They are now
// parsed as presence markers and refused outright — nothing implements them
// (GEN-ANN-1) — which subsumes the scalar-root guard rather than joining it.
// Both properties are load-bearing: parsed, so the rejection can fire, and NOT in
// stateBoundaryFieldAnnotations, which would demote them back to a scalar-only
// rule that lets them through on a message-typed boundary.
func TestUnimplementedStatedbAnnotationsAreParsedAndNotBoundaryAnnotations(t *testing.T) {
	got := boundaryAnnotationProbeFields(t)
	for _, name := range parser.UnimplementedStatedbAnnotations() {
		if strings.HasPrefix(name, "state_") {
			continue // message-level; the probe reads one field's annotations
		}
		if _, ok := got[name]; !ok {
			t.Errorf("the descriptor parser no longer records (spec.%s), so the GEN-ANN-1 rejection in ValidateStateConsistency can never fire "+
				"and the annotation is silently inert again", name)
		}
		for _, listed := range stateBoundaryFieldAnnotations {
			if name == listed {
				t.Errorf("%q is both rejected as unimplemented and listed as a boundary annotation — it must be exactly one", name)
			}
		}
	}
}
