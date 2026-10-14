// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// residencyTopicsProto declares two topics on one bus: one carrying a
// (spec.record), one carrying a plain (spec.event). Both are reachable as
// subscription sources, which is what lets the resolver be asked the same
// question about each.
const residencyTopicsProto = `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {
    key: "event_id"
    owner: "owner_id"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
    residency: {idle_seconds: 3600 deleted_max_age_seconds: 2592000}
  };
  string event_id = 1;
  string owner_id = 2;
  int64 store_epoch = 3 [
    (spec.store_epoch) = true,
    (spec.query_field) = {op: QUERY_OP_RANGE}
  ];
}

message DelegationProbe {
  option (spec.event) = {
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

// Whether a topic carries a stored record must reach the subscription
// resolution, because that is the single place every language generator asks
// what a topic is — and it is what selects a per-instance consumer group. Every
// record type's rows land in a node-local store, so a node's copy is only
// complete if that node receives every record.
//
// Paired control: the same call over a plain event must come back false, so the
// shared service-label group it has always used is what the generators keep
// emitting for it.
func TestSubscriptionResolutionCarriesRecordKind(t *testing.T) {
	skipWithoutProtoc(t)
	dir := residencyFixture(t, residencyTopicsProto)
	p, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	record := boundaries.ResolveSubscriptionSource(p, dir, boundaries.SubscriptionData{
		SourcePackage:   "tasks",
		SourceStateRoot: "TasksBus",
		SourceField:     "activity_stream",
	})
	if record.TypeFqn != "tasks.TaskActivity" {
		t.Fatalf("record topic resolved to %q, want tasks.TaskActivity", record.TypeFqn)
	}
	if !record.IsRecord {
		t.Fatal("a topic carrying a (spec.record) must resolve as a record")
	}

	event := boundaries.ResolveSubscriptionSource(p, dir, boundaries.SubscriptionData{
		SourcePackage:   "tasks",
		SourceStateRoot: "TasksBus",
		SourceField:     "probe_stream",
	})
	if event.TypeFqn != "tasks.DelegationProbe" {
		t.Fatalf("event topic resolved to %q, want tasks.DelegationProbe", event.TypeFqn)
	}
	if event.IsRecord {
		t.Fatal("a topic carrying a plain (spec.event) must not resolve as a record")
	}
}
