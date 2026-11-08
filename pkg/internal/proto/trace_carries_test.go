// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// #313: storage paths clear the injected trace_id before persisting, and decide
// at generation time which value types carry it. A type InjectTraceFields
// injected carries it; a type it skipped (its own trace_id, at 200 or not) does
// not, and must generate exactly as before.
func TestCarriesInjectedTrace(t *testing.T) {
	dir := t.TempDir()
	protoDir := filepath.Join(dir, "services", "demo")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		t.Fatalf("mkdir proto dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(protoDir, "demo.proto"), []byte(`syntax = "proto3";

package demo;

message Player {
  string player_id = 1;
}

message OwnTrace {
  string trace_id = 200;
}

message OwnTraceElsewhere {
  string trace_id = 3;
}
`), 0644); err != nil {
		t.Fatalf("write proto: %v", err)
	}

	skipped, err := InjectTraceFields(dir, dir)
	if err != nil {
		t.Fatalf("InjectTraceFields: %v", err)
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}

	for _, tc := range []struct {
		fqn  string
		want bool
	}{
		{"demo.Player", true},
		// Defines trace_id at the injected number: skipped by injection, so it is
		// the author's own field and is left alone.
		{"demo.OwnTrace", false},
		{"demo.OwnTraceElsewhere", false},
		{"demo.Missing", false},
		{"", false},
	} {
		if got := CarriesInjectedTrace(dp, tc.fqn, skipped); got != tc.want {
			t.Errorf("CarriesInjectedTrace(%q) = %v, want %v", tc.fqn, got, tc.want)
		}
	}

	if !FieldTypeCarriesInjectedTrace(dp, "demo", "Player", skipped) {
		t.Error("a bare field type must resolve in the file's package")
	}
	if !FieldTypeCarriesInjectedTrace(dp, "other", "demo.Player", skipped) {
		t.Error("a qualified field type must resolve in its own package")
	}
	if CarriesInjectedTrace(nil, "demo.Player", skipped) {
		t.Error("a nil parser has nothing to resolve against")
	}
}
