// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// T3: opaqueSegment is exported from the TypeScript client SPIFFE lib for
// cross-language signature parity, but identity construction is a server
// concern, so the client stub must fail closed (throw), never return a wrong or
// empty segment. Make that intent explicit and regression-proof: the impl must
// throw, the generated test must assert the throw, and the dead base32 helper
// must not linger implying a real implementation.
func TestTypeScriptOpaqueSegmentIsFailClosedAndTested(t *testing.T) {
	impl := readTSTemplate(t, "typescript/chassis/spiffe/SpiffeId.ts.tmpl")
	if !strings.Contains(impl, "export function opaqueSegment(") {
		t.Fatalf("opaqueSegment export must be retained for cross-language signature parity")
	}
	if !strings.Contains(impl, "spiffe: opaqueSegment is unavailable in TypeScript browser client output") {
		t.Fatalf("opaqueSegment must throw the documented unavailable error")
	}
	// The throw stub must not carry the dead base32 helper that implies a real
	// implementation.
	for _, dead := range []string{"base32LowerNoPad", "BASE32_ALPHABET"} {
		if strings.Contains(impl, dead) {
			t.Fatalf("client SpiffeId must not retain dead opaque-segment helper %q", dead)
		}
	}

	test := readTSTemplate(t, "typescript/chassis/spiffe/SpiffeId.test.ts.tmpl")
	if !strings.Contains(test, "mustThrow('opaque.unavailable.user', () => opaqueSegment(") {
		t.Fatalf("generated SPIFFE test must assert opaqueSegment throws")
	}
	// The stale property tests assumed a real implementation; they must be gone.
	for _, stale := range []string{"opaque.deterministic", "opaque.domainSeparation", "fixture.opaque.length"} {
		if strings.Contains(test, stale) {
			t.Fatalf("generated SPIFFE test must not assert opaqueSegment return-value properties (%q) for a fail-closed stub", stale)
		}
	}
}

func readTSTemplate(t *testing.T, path string) string {
	t.Helper()
	src, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}
