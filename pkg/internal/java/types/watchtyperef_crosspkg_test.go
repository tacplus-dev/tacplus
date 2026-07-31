// Copyright (c) 2026 Second Star Cavalier LLC

package java

import "testing"

// A cross-package collection item (e.g. a stratum RecordMap paging
// agentsensor.AgentEvent) must resolve to the item's OWN Java outer class, not
// the response envelope's package.
func TestJavaWatchTypeRefCrossPackage(t *testing.T) {
	got := javaWatchTypeRef("agentsensor.AgentEvent", "Stratum.ListAgentEventResponse")
	if want := "Agentsensor.AgentEvent"; got != want {
		t.Fatalf("cross-package item: got %q, want %q", got, want)
	}
	// Same-package item keeps the envelope's resolved outer class (conflict-safe).
	if got := javaWatchTypeRef("catalog.Event", "Catalog.ListEventsResponse"); got != "Catalog.Event" {
		t.Fatalf("same-package item: got %q, want %q", got, "Catalog.Event")
	}
}
