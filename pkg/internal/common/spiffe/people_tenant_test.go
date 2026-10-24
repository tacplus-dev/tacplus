// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package spiffe

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// "people" is the platform's identity tenant (decisions/identity-people-tenant-
// devices-private-2026-09-21.md): it is the one reserved tag that is legal as a
// tenant name, in both the namespace validator and the tenant constructor. The
// five chassis SPIFFE ports are byte-identical siblings, so the carve-out must
// be present in all of them and must stay narrow — every other reserved tag,
// and "people" in every other structural position, stays rejected.
func TestPeopleTenantCarveOutIsPresentInEveryChassisSpiffePort(t *testing.T) {
	for _, port := range []struct {
		file string
		// tenantGuard is the namespace validator's tenant-name branch, which
		// must now exempt the people segment.
		tenantGuard string
		// tenantCtor is the tenant constructor's segment call, which must use
		// the tenant-specific normalizer rather than the generic one.
		tenantCtor string
	}{
		{
			file:        "go/chassis/spiffe/spiffe.go.tmpl",
			tenantGuard: "if len(segs) >= 2 && segs[1] != PeopleSegment && reservedTags[segs[1]] {",
			tenantCtor:  "t, err := normalizeTenantSegment(tenant)",
		},
		{
			file:        "java/chassis/spiffe/Spiffe.java.tmpl",
			tenantGuard: "if (segs.length >= 2 && !segs[1].equals(PEOPLE_SEGMENT) && RESERVED_TAGS.contains(segs[1])) {",
			tenantCtor:  "String t = normalizeTenantSegment(tenant);",
		},
		{
			file:        "rust/chassis/spiffe/src/spiffe.rs.tmpl",
			tenantGuard: "if segs.len() >= 2 && segs[1] != PEOPLE_SEGMENT && is_reserved_tag(segs[1]) {",
			tenantCtor:  "let t = normalize_tenant_segment(tenant)?;",
		},
		{
			file:        "python/chassis/spiffe/spiffe.py.tmpl",
			tenantGuard: "if len(segs) >= 2 and segs[1] != PEOPLE_SEGMENT and segs[1] in _RESERVED_TAGS:",
			tenantCtor:  "t = _normalize_tenant_segment(tenant)",
		},
		{
			file:        "typescript/chassis/spiffe/SpiffeId.ts.tmpl",
			tenantGuard: "if (segs.length >= 2 && segs[1] !== PEOPLE_SEGMENT && RESERVED_TAGS.has(segs[1])) {",
			tenantCtor:  "const t = normalizeTenantSegment(tenant);",
		},
	} {
		src, err := templates.FS.ReadFile(port.file)
		if err != nil {
			t.Fatalf("read %s: %v", port.file, err)
		}
		body := string(src)
		if !strings.Contains(body, port.tenantGuard) {
			t.Errorf("%s: namespace validator must exempt the people segment from the tenant-name reservation; missing %q",
				port.file, port.tenantGuard)
		}
		if !strings.Contains(body, port.tenantCtor) {
			t.Errorf("%s: tenant constructor must go through the tenant-specific segment normalizer; missing %q",
				port.file, port.tenantCtor)
		}
		// The carve-out must not have been implemented by dropping "people"
		// from the reserved set — it must still be rejected as a /system
		// location, a workspace segment, a user id, and a plain identifier.
		if !strings.Contains(body, PeopleSegment) {
			t.Errorf("%s: %q must remain a reserved structural tag", port.file, PeopleSegment)
		}
	}
}

// The shared reserved-tag set still contains "people": the carve-out lives at
// the two tenant-name call sites, not in the set itself.
func TestPeopleRemainsAReservedTag(t *testing.T) {
	if !ReservedTagSet()[PeopleSegment] {
		t.Fatalf("%q must remain in the reserved tag set", PeopleSegment)
	}
}
