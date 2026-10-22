// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
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
		{Name: "telemetry", FieldID: 30, HostLabel: "events", RPCs: []string{"PublishX"}},
	}

	got := AcceptedAudiencesForHost("stratum", entries, nil)
	want := []string{"stratum", "query"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedAudiencesForHost(stratum) = %#v, want %#v", got, want)
	}

	if got := AcceptedAudiencesForHost("events", entries, nil); !reflect.DeepEqual(got, []string{"events", "telemetry"}) {
		t.Fatalf("AcceptedAudiencesForHost(events) = %#v, want [events telemetry]", got)
	}
	if got := AcceptedAudiencesForHost("gateway", entries, nil); !reflect.DeepEqual(got, []string{"gateway"}) {
		t.Fatalf("AcceptedAudiencesForHost(gateway) = %#v, want [gateway]", got)
	}
}

// Paired control for (spec.service).accepted_audiences. The declaring half must
// answer to BOTH audiences it names; the default half — no annotation, which is
// every service deployed today — must answer only to its own name. Run as one
// test so the default can never be "fixed" into silently accepting extras.
func TestAcceptedAudiencesDeclaredVersusDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, "services", name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Declares two accepted audiences beyond its own label.
	write("ensemble.proto", `syntax = "proto3";
package ensemble;
option (spec.service) = {name: "ensemble", accepted_audiences: ["scotty", "journeyman"]};
`)
	// Declares none: the default, and the shape of every already-deployed service.
	write("plain.proto", `syntax = "proto3";
package plain;
option (spec.service) = {name: "plain"};
`)

	declared, err := ScanAcceptedAudiences(dir, "ensemble")
	if err != nil {
		t.Fatalf("ScanAcceptedAudiences(ensemble): %v", err)
	}
	if want := []string{"scotty", "journeyman"}; !reflect.DeepEqual(declared, want) {
		t.Fatalf("ScanAcceptedAudiences(ensemble) = %#v, want %#v", declared, want)
	}
	got := AcceptedAudiencesForHost("ensemble", nil, declared)
	if want := []string{"ensemble", "scotty", "journeyman"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ensemble accepted audiences = %#v, want %#v", got, want)
	}

	plainDeclared, err := ScanAcceptedAudiences(dir, "plain")
	if err != nil {
		t.Fatalf("ScanAcceptedAudiences(plain): %v", err)
	}
	if len(plainDeclared) != 0 {
		t.Fatalf("undeclared service must scan to no audiences, got %#v", plainDeclared)
	}
	if got := AcceptedAudiencesForHost("plain", nil, plainDeclared); !reflect.DeepEqual(got, []string{"plain"}) {
		t.Fatalf("plain accepted audiences = %#v, want [plain]", got)
	}
}

// A declared audience that duplicates the service label or a hosted trait must
// not appear twice: the generated Authorizer renders this set into a map/list
// literal, and a duplicate key is a compile error in some of the five targets.
func TestAcceptedAudiencesDeclaredDeduplicates(t *testing.T) {
	entries := []VServiceEntry{{Name: "query", FieldID: 40, HostLabel: "stratum"}}
	got := AcceptedAudiencesForHost("stratum", entries, []string{"stratum", "query", "scotty"})
	if want := []string{"stratum", "query", "scotty"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("AcceptedAudiencesForHost = %#v, want %#v", got, want)
	}
}
