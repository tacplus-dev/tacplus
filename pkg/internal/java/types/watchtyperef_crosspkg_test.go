// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import "testing"

// A cross-package collection item (e.g. a stratum RecordMap paging
// agentsensor.AgentEvent) must resolve to the item's OWN Java outer class, not
// the response envelope's package.
func TestJavaWatchTypeRefCrossPackage(t *testing.T) {
	got := javaWatchTypeRef("agentsensor.AgentEvent", "Stratum.ListAgentEventResponse", nil)
	if want := "Agentsensor.AgentEvent"; got != want {
		t.Fatalf("cross-package item: got %q, want %q", got, want)
	}
	// Same-package item keeps the envelope's resolved outer class (conflict-safe).
	if got := javaWatchTypeRef("catalog.Event", "Catalog.ListEventsResponse", nil); got != "Catalog.Event" {
		t.Fatalf("same-package item: got %q, want %q", got, "Catalog.Event")
	}
}

// A cross-package item whose proto file name collides with a top-level
// declaration (tasks.proto declaring "service Tasks") gets its default Java
// outer class renamed to TasksOuterClass by protoc. The item reference MUST use
// the same resolved outer class the wrapper's import line uses — otherwise the
// generated Java imports TasksOuterClass but writes Tasks.TaskActivity, and
// javac fails with "package Tasks does not exist".
func TestJavaWatchTypeRefCrossPackageOuterClassConflict(t *testing.T) {
	crossPkgOuterClasses := map[string]string{"tasks": "TasksOuterClass"}

	got := javaWatchTypeRef("tasks.TaskActivity", "Stratum.PullTaskActivityResponse", crossPkgOuterClasses)
	if want := "TasksOuterClass.TaskActivity"; got != want {
		t.Fatalf("conflict-renamed cross-package item: got %q, want %q", got, want)
	}

	// A cross-package item in a non-colliding package still resolves through the
	// map, unchanged.
	crossPkgOuterClasses["agentsensor"] = "Agentsensor"
	if got := javaWatchTypeRef("agentsensor.AgentEvent", "Stratum.ListAgentEventResponse", crossPkgOuterClasses); got != "Agentsensor.AgentEvent" {
		t.Fatalf("non-colliding cross-package item: got %q, want %q", got, "Agentsensor.AgentEvent")
	}

	// Same-package items never consult the map — the envelope's outer class
	// already carries any conflict resolution.
	if got := javaWatchTypeRef("stratum.Sample", "Stratum.ListSampleResponse", crossPkgOuterClasses); got != "Stratum.Sample" {
		t.Fatalf("same-package item: got %q, want %q", got, "Stratum.Sample")
	}
}
