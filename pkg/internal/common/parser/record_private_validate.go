// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateRecordPrivateAnnotations enforces the one invariant
// (spec.record).private already implies before its generated owner-pin lands
// (tracked separately, see decisions/identity-people-tenant-devices-private-2026-09-21.md
// §4): a private record must never declare s2s_subscribe.
//
// A subscriber to a record's raw topic is a SERVICE, not a per-request caller —
// there is no principal in scope for a stream handler to pin against. Declaring
// both means every row of every owner ever written streams to any authorized
// peer unfiltered, which is exactly the fleet-wide broadcast `private` exists to
// prevent. s2s, s2s_read and c2s_read stay declarable on a private record: they
// are the record query plane's request/response surfaces, where a per-call
// caller exists for the (not-yet-emitted) pin to bind to.
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
