// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// residencyTopicsProto declares two records on one bus: one warm, one with no
// residency block at all. Both are reachable as subscription sources, which is
// what lets the resolver be asked the same question about each.
const residencyTopicsProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
    residency: {window: RESIDENCY_WARM}
  };
  string event_id = 1;
  string owner_id = 2;
  int64 store_epoch = 3 [
    (spec.store_epoch) = true,
    (spec.query_field) = {op: QUERY_OP_RANGE}
  ];
}

message DelegationProbe {
  option (spec.record) = {
    key: "probe_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
  };
  string probe_id = 1;
  string owner_id = 2;
}

message TasksBus {
  TaskActivity activity_stream = 1;
  DelegationProbe probe_stream = 2;
}
`

// The residency of a topic's record type must reach the subscription
// resolution, because that is the single place every language generator asks
// what a topic is — and it is what selects a per-instance consumer group for a
// warm topic (BIG GOAL 4 P1, P0c design §5.2).
//
// Paired control: the same call over a record declaring no residency must come
// back empty, so the shared service-label group it has always used is what the
// generators keep emitting for it.
func TestSubscriptionResolutionCarriesRecordResidency(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, residencyTopicsProto)
	p, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	warm := boundaries.ResolveSubscriptionSource(p, dir, boundaries.SubscriptionData{
		SourcePackage:   "tasks",
		SourceStateRoot: "TasksBus",
		SourceField:     "activity_stream",
	})
	if warm.TypeFqn != "tasks.TaskActivity" {
		t.Fatalf("warm topic resolved to %q, want tasks.TaskActivity", warm.TypeFqn)
	}
	if warm.Residency != "RESIDENCY_WARM" {
		t.Fatalf("warm topic residency = %q, want RESIDENCY_WARM", warm.Residency)
	}

	undeclared := boundaries.ResolveSubscriptionSource(p, dir, boundaries.SubscriptionData{
		SourcePackage:   "tasks",
		SourceStateRoot: "TasksBus",
		SourceField:     "probe_stream",
	})
	if undeclared.TypeFqn != "tasks.DelegationProbe" {
		t.Fatalf("undeclared topic resolved to %q, want tasks.DelegationProbe", undeclared.TypeFqn)
	}
	if undeclared.Residency != "" {
		t.Fatalf("a record declaring no residency must resolve to no residency, got %q", undeclared.Residency)
	}
}
