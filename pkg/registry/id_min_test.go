// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// Service IDs share ONE 1-99 space across every proto repo merged into a build,
// but each repo allocates from its own registry.yaml. Without a floor, two repos
// both pick "max local ID + 1" and collide — that is how the app's echo service
// was handed 47, the ID platform had already given operator. The multi-source
// merge hard-errors on it, so nothing ships broken, but the app cannot generate
// at all until a human picks a free number by hand.
//
// id_min removes the collision by construction: platform allocates from 1, an
// app from its own floor.
func TestIDMinFloorsAllocationForAnAppRegistry(t *testing.T) {
	dir := t.TempDir()
	// An app registry: no services yet, allocating above the platform's range.
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("id_min: 60\nservices: []\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if r.IDMin != 60 {
		t.Fatalf("id_min not parsed: got %d", r.IDMin)
	}

	// The FIRST service in an empty app registry must land on the floor, not 1.
	// Landing on 1 is precisely the collision this exists to prevent.
	id, err := r.ServiceID("myapp-thing")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if id != 60 {
		t.Errorf("first app service got id %d, want 60 — an empty registry with a "+
			"floor must start AT the floor or it collides with the platform", id)
	}
	// The next one continues from there rather than resetting.
	id2, err := r.ServiceID("myapp-other")
	if err != nil {
		t.Fatalf("assign second: %v", err)
	}
	if id2 != 61 {
		t.Errorf("second app service got id %d, want 61", id2)
	}
}

// A registry with no floor keeps allocating exactly as before, so the platform
// repo is unaffected by this change.
func TestNoIDMinPreservesSequentialAllocation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "registry.yaml"),
		[]byte("services:\n  - name: a\n    id: 5\n  - name: b\n    id: 9\n"), 0644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	r, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	id, err := r.ServiceID("c")
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if id != 10 {
		t.Errorf("got id %d, want 10 (max+1) — a registry without a floor must be unchanged", id)
	}
}
