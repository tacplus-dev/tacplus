// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidateMinGroupSizeAnnotations enforces the two invariants
// (spec.record).min_group_size needs to mean anything.
//
//  1. The threshold cannot be negative. min_group_size is the smallest result
//     size the record's size-only surfaces will disclose; 0 is "no guard", and a
//     negative value is a guard that can never refuse. Generation FAILS rather
//     than emitting a comparison that is always false.
//
//  2. Something must read it. The threshold is enforced on the size-only RPCs of
//     the record query plane, and that plane exists only where a (spec.trait)
//     declares a record store as its host (see collectRecordQueryPlanes). A
//     record declaring a threshold into a fleet with no such trait has no
//     guarded surface at all — the declaration is decoration, and an unread
//     annotation is a generation failure here the same way it is everywhere else
//     in this tree.
func ValidateMinGroupSizeAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateMinGroupSizeAnnotations(fds)
}

// hasRecordQueryPlane reports whether the fleet carries a (spec.trait) that asks
// for the record query plane: a trait with a host, where that host is a service
// declaring (spec.record_store). This is the same condition the record-map
// injector resolves a plane on, stated once so "guarded surface exists" cannot
// come to mean two different things.
func hasRecordQueryPlane(fds []*FileDescriptor) bool {
	recordStores := make(map[string]bool)
	for _, fd := range fds {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}
	for _, fd := range fds {
		if fd.Trait == nil || fd.Trait.Host == "" {
			continue
		}
		if recordStores[fd.Trait.Host] {
			return true
		}
	}
	return false
}

// validateMinGroupSizeAnnotations is the file-list form of
// ValidateMinGroupSizeAnnotations; fds must already be in a deterministic order
// so the error text is stable.
func validateMinGroupSizeAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)

	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			ann, ok := msg.Annotations["record_min_group_size"]
			if !ok {
				continue
			}
			if ann.IntValue < 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).min_group_size is %d; a minimum group size cannot be negative (0 or unset = no guard)",
					fd.Package, msg.Name, ann.IntValue))
				continue
			}
			if !planeExists {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).min_group_size declares a threshold but no (spec.trait) hosts a record query plane, so nothing would enforce it",
					fd.Package, msg.Name))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("min_group_size annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
