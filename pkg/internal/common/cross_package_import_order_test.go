// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"
)

// GEN-DET-GAP residual (batch 16). GEN-DET-1 found six sites that ranged a Go
// map straight into ordered generated output. Three of them — the
// cross-package import blocks in rust statewrapper_gen, typescript statewrapper
// and java servicewrapper_gen — are still guarded by NOTHING:
//
//   - the determinism gate cannot reach them: no cross-test proto has a state
//     root importing types from >=2 DIFFERENT packages, so every backing map
//     holds <=1 entry and any iteration order is trivially "deterministic";
//   - the render tests cannot reach them either: they harness template data
//     that is built AFTER the sort.
//
// The ledger accepted the gap on the grounds that the alternatives (a 7-language
// fixture, or refactoring generation code purely for a test) cost more than the
// one-line sorts are worth. That reasoning holds — but "expensive to test
// behaviourally" is not the same as "untestable". A source-order pin is cheap
// and it is revert-proof in the direction that matters: delete the sort, or
// range the map directly into the emitter again, and this fails.
//
// Unlike go, none of these three languages has a downstream formatter that
// re-sorts imports, so a regression here byte-diffs generated output between
// two otherwise-identical runs.
func TestCrossPackageImportsAreSortedBeforeEmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		// seq must appear in the source in this order.
		seq []string
		// banned must not appear at all.
		banned []string
	}{
		{
			name: "rust statewrapper use-lines",
			file: "../rust/types/statewrapper_gen.go",
			seq: []string{
				"crossPkgKeys := make([]string, 0, len(crossPkgTypes))",
				"sort.Strings(crossPkgKeys)",
				"for _, pkg := range crossPkgKeys {",
				"crossPkgImports.WriteString(importPath)",
			},
			// Emitting straight from the map is the defect shape.
			banned: []string{"for pkg, dir := range crossPkgTypes {\n\t\tdirModule"},
		},
		{
			name: "typescript statewrapper import block",
			file: "../typescript/types/statewrapper.go",
			seq: []string{
				"for _, ci := range crossImportMap {",
				"sort.Slice(crossImports,",
				"CrossImports:",
			},
		},
		{
			name: "java servicewrapper cross-package imports",
			file: "../java/types/servicewrapper_gen.go",
			seq: []string{
				"for pkg, dir := range info.CrossPackageTypes {",
				"sort.Strings(crossPkgImportLines)",
				"for _, importLine := range crossPkgImportLines {",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.file)
			if err != nil {
				t.Fatalf("read %s: %v", tc.file, err)
			}
			s := string(data)

			prev := -1
			for _, want := range tc.seq {
				i := strings.Index(s, want)
				if i < 0 {
					t.Fatalf("%s: missing %q — the map-derived import collection must still be sorted before it is emitted", tc.file, want)
				}
				if i <= prev {
					t.Fatalf("%s: %q appears before the step that must precede it; the sort has to happen between collection and emission", tc.file, want)
				}
				prev = i
			}
			for _, bad := range tc.banned {
				if strings.Contains(s, bad) {
					t.Errorf("%s: emits import lines by ranging the map directly (%q); that is nondeterministic across runs", tc.file, bad)
				}
			}
		})
	}
}
