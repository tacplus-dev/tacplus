// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRecordPrivateAnnotations enforces the two invariants
// (spec.record).private already implies before its generated owner-pin lands
// (tracked separately, see decisions/identity-people-tenant-devices-private-2026-09-21.md
// §4): a private record must declare (spec.store_principal), and must never
// declare s2s_subscribe.
//
// STORE_PRINCIPAL IS MANDATORY, NOT OPT-IN, ON A PRIVATE RECORD. Unlike a plain
// record, where store_principal is an author's opt-in filter, a private record's
// entire guarantee is the owner pin, and (spec.store_principal) is the field the
// pin binds to (see pkg/internal/rust/types/principal_gen.go's
// storePrincipalField / rustResponsePrincipalFilterBlock — the generated pin,
// once it lands, is this same lookup made to abort rather than filter on
// mismatch). A private record naming no such field has nothing for that pin to
// bind to, which is exactly the "record nobody remembered to annotate" failure
// mode (spec.store_namespace)'s own mandatory-field posture exists to catch —
// same doctrine, applied here because `private` is a stronger claim than
// store_namespace's tenant boundary and deserves at least the same floor.
//
// NO S2S_SUBSCRIBE. A subscriber to a record's raw topic is a SERVICE, not a
// per-request caller — there is no principal in scope for a stream handler to
// pin against. Declaring both means every row of every owner ever written
// streams to any authorized peer unfiltered, which is exactly the fleet-wide
// broadcast `private` exists to prevent. s2s, s2s_read and c2s_read stay
// declarable on a private record: they are the record query plane's
// request/response surfaces, where a per-call caller exists for the pin to
// bind to.
func ValidateRecordPrivateAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs []string
	for _, path := range paths {
		fd := all[path]
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record_private"]; !ok || !ann.BoolValue {
				continue
			}
			hasStorePrincipal := false
			for fi := range msg.Fields {
				if msg.Fields[fi].Annotations["store_principal"].BoolValue {
					hasStorePrincipal = true
					break
				}
			}
			if !hasStorePrincipal {
				errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true but it declares no "+
					"(spec.store_principal) field; give the string field holding the row's owner (spec.store_principal) "+
					"= true so the generated pin has a field to bind the caller's identity against",
					fd.Package, msg.Name, path))
			}
			surface, ok := msg.Annotations["event_surface"]
			if !ok || len(surface.EventPublishEntries) == 0 {
				continue
			}
			if surface.EventPublishEntries[0].MethodS2SSubscribe != nil {
				errs = append(errs, fmt.Sprintf("%s.%s (%s): (spec.record).private is true and it declares "+
					"s2s_subscribe; a subscriber to a record's topic is a service with no per-request caller to "+
					"pin a private row's owner against, so this is a fleet-wide broadcast of the exact data "+
					"private exists to keep to one principal — drop s2s_subscribe or drop private",
					fd.Package, msg.Name, path))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("record private annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
