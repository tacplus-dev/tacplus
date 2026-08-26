// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"reflect"
	"testing"
)

// The end-to-end path (a full java build reaching WriteCodegenConfig) has no
// fixture, so this pins the audience-set builder directly: a non-topic trait
// entry — the shape ScanVServiceRPCs now produces — must put the TRAIT name in
// its host's accepted `aud` set. Without the entry the set holds only the host
// label and the host rejects correctly-authenticated calls.
func TestAcceptedAudiencesForHostIncludesNonTopicTrait(t *testing.T) {
	entries := []VServiceEntry{
		{Name: "query", FieldID: 40, HostLabel: "stratum"}, // trait: RPCs, no topics
		{Name: "claudesensor", FieldID: 30, HostLabel: "events", RPCs: []string{"PublishX"}},
	}

	got := AcceptedAudiencesForHost("stratum", entries)
	want := []string{"stratum", "query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedAudiencesForHost(stratum) = %#v, want %#v", got, want)
	}

	if got := AcceptedAudiencesForHost("events", entries); !reflect.DeepEqual(got, []string{"events", "claudesensor"}) {
		t.Fatalf("AcceptedAudiencesForHost(events) = %#v, want [events claudesensor]", got)
	}
	if got := AcceptedAudiencesForHost("gateway", entries); !reflect.DeepEqual(got, []string{"gateway"}) {
		t.Fatalf("AcceptedAudiencesForHost(gateway) = %#v, want [gateway]", got)
	}
}
