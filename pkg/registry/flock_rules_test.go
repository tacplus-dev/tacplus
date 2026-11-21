// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A flock merge rule is pinned on first generation and never changes
// (tacplus#206).
func TestPinFlockRules(t *testing.T) {
	dir := t.TempDir()
	r, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PinFlockRules(map[string]string{"p.Person.display": "last_write", "p.Person": "fields"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "registry.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "field: p.Person.display\n      rule: last_write") {
		t.Fatalf("rule not pinned:\n%s", data)
	}

	r, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// The same rules, and a new field: accepted.
	if err := r.PinFlockRules(map[string]string{"p.Person.display": "last_write", "p.Person.generation": "max"}); err != nil {
		t.Fatal(err)
	}
	// A released rule that changed: refused, naming it.
	err = r.PinFlockRules(map[string]string{"p.Person.display": "min"})
	if err == nil || !strings.Contains(err.Error(), "p.Person.display: min, released as last_write") {
		t.Fatalf("a changed rule was not refused: %v", err)
	}
}
