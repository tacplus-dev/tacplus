// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import "testing"

// The sentinel's KIND is what lets a reader tell "this build must reach a state
// host, so it needs an S2S identity" from "this is the mesh face of a record
// store that a local daemon may never touch". Collapsing the two made a c2d-only
// build demand a token minter (sideband2).
func TestParseSynthesizedKind(t *testing.T) {
	for _, tc := range []struct {
		name    string
		comment string
		want    string
	}{
		{"store", "// tacplus:generated-service store\n", SynthesizedKindStore},
		{"state", "// tacplus:generated-service state\n", SynthesizedKindState},
		{"recordmap", "// tacplus:generated-service recordmap\n", SynthesizedKindRecordMap},
		{"opaeval", "// tacplus:generated-service opaeval\n", SynthesizedKindOpaEval},
		// A bare sentinel predates the kind. It must still mark the service
		// synthesized (asserted below) while reporting no kind, so an old emitter
		// or a hand-written fixture cannot be mistaken for a specific surface.
		{"bare sentinel has no kind", "// tacplus:generated-service\n", ""},
		{"unknown word is not a kind", "// tacplus:generated-service mutation\n", ""},
		{"no sentinel", "// just a comment\n", ""},
		{"kind survives a following line", "// tacplus:generated-service store\n// more\n", SynthesizedKindStore},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseSynthesizedKind(tc.comment); got != tc.want {
				t.Fatalf("parseSynthesizedKind(%q) = %q, want %q", tc.comment, got, tc.want)
			}
		})
	}
}
