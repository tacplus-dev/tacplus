// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// The platform defaults for (spec.record).residency. 0/unset on either clock
// means "apply the default", never "never" — under the one-clock lifecycle every
// record ages out of memory and every flagged-deleted row is eventually purged.
//
// Declared here, next to the validator, for the same reason
// collection.DefaultPageSizeFallback is declared next to the paging helpers: the
// number a generator bakes in when an author left the knob at 0 has to be
// readable in one place, not restated per backend.
const (
	// DefaultResidencyIdleSeconds is the effective idle_seconds for a record type
	// that left it at 0: one day with neither a read nor a write before the row
	// leaves memory. Long enough that a daily job still finds its rows resident,
	// short enough that nothing is resident because it once was.
	DefaultResidencyIdleSeconds int64 = 86400

	// DefaultResidencyDeletedMaxAgeSeconds is the effective
	// deleted_max_age_seconds for a record type that left it at 0: thirty days
	// between the delete write and the purge sweep that makes the row gone. Long
	// enough for an operator to notice and undo a mistaken delete, short enough
	// that "deleted" becomes true within a billing month.
	DefaultResidencyDeletedMaxAgeSeconds int64 = 2592000
)

// ValidateResidencyAnnotations enforces the invariants (spec.record).residency
// and (spec.record).mutability need in order to mean anything. It is shaped
// exactly like ValidateMinGroupSizeAnnotations: deterministic file ordering,
// errors accumulated, one aggregated error under a header.
//
//	VAL-1  residency.idle_seconds < 0                             -> fail
//	       A clock cannot run backwards, and 0 already means "use the platform
//	       default" (DefaultResidencyIdleSeconds), so a negative number is not
//	       a spelling of anything.
//	VAL-2  residency.deleted_max_age_seconds < 0                   -> fail
//	       Same rule, same reason, on the purge clock.
//	VAL-3  a (spec.store_epoch) field that is not exactly one non-repeated
//	       int64 carrying (spec.query_field) = {op: QUERY_OP_RANGE} -> fail
//	       deleted_max_age_seconds measures the delete write from the store
//	       epoch and the disk leg ranges on it, so an epoch field the store
//	       cannot read is worse than an absent one. Checked on every record that
//	       declares one, not only on one declaring residency: the field is the
//	       store's clock either way.
//	VAL-4  residency or mutability on a message with no record query
//	       plane in the fleet                                      -> fail
//	       Verbatim the second min_group_size invariant; hasRecordQueryPlane is
//	       shared, not restated.
//
// What is NOT here any more: the HOT/WARM/COLD preset rules (VAL-1, VAL-2 and
// VAL-5 as they were), and every retention rule. The preset enum and
// (spec.record).retention were retired on 2026-09-20 — one lifecycle, two
// numbers, no classes and no second clock measured from a business field.
func ValidateResidencyAnnotations(dp *DescriptorParser) error {
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
	return validateResidencyAnnotations(fds)
}

// storeEpochFields returns the names of msg's (spec.store_epoch) fields that are
// well-formed (int64, non-repeated, RANGE-queryable), and the names of those that
// are not, with the reason. VAL-3 needs both halves: "no epoch field" and "an
// epoch field the disk leg cannot filter on" are different author errors and get
// different sentences.
func storeEpochFields(msg *MessageDescriptor) (ok []string, malformed []string) {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if !f.Annotations["store_epoch"].BoolValue {
			continue
		}
		switch {
		case f.TypeKind != "scalar" || f.Type != "int64":
			malformed = append(malformed, fmt.Sprintf("%s is %s, not int64", f.Name, f.Type))
		case f.IsRepeated:
			malformed = append(malformed, fmt.Sprintf("%s is repeated; a row has one store epoch", f.Name))
		case f.Annotations["query_field"].StringValue != "range":
			malformed = append(malformed, fmt.Sprintf(
				"%s carries no (spec.query_field) = {op: QUERY_OP_RANGE}", f.Name))
		default:
			ok = append(ok, f.Name)
		}
	}
	return ok, malformed
}

// validateResidencyAnnotations is the file-list form of
// ValidateResidencyAnnotations; fds must already be in a deterministic order so
// the error text is stable.
func validateResidencyAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)

	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			residency := msg.Annotations["record_residency"].RecordResidency
			mutability := msg.Annotations["record_mutability"].StringValue

			// VAL-3: the store epoch's well-formedness, independent of residency.
			// It is the record's durable write clock — what the purge measures a
			// delete from and what the disk leg ranges on — so it is checked
			// wherever it is declared.
			okEpochs, malformedEpochs := storeEpochFields(msg)
			switch {
			case len(malformedEpochs) > 0:
				sort.Strings(malformedEpochs)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: its (spec.store_epoch) field is unusable: %s. The store epoch is Unix "+
						"milliseconds, it is what deleted_max_age_seconds measures the delete write "+
						"from, and it is the predicate the disk leg ranges on",
					fd.Package, msg.Name, strings.Join(malformedEpochs, "; ")))
			case len(okEpochs) > 1:
				sort.Strings(okEpochs)
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares %d (spec.store_epoch) fields (%s); a row has exactly one store "+
						"epoch, and which one the purge measures against is not decidable here",
					fd.Package, msg.Name, len(okEpochs), strings.Join(okEpochs, ", ")))
			}

			if residency == nil && mutability == "" {
				continue
			}

			// VAL-4: something must read it. Stated first, because when no plane
			// exists none of the rules below have a consumer either.
			if !planeExists {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record) declares residency/mutability but no (spec.trait) hosts a "+
						"record query plane, so nothing would read it%s",
					fd.Package, msg.Name, noRecordQueryPlaneHint))
			}

			if residency == nil {
				continue
			}
			// VAL-1.
			if residency.IdleSeconds < 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).residency sets idle_seconds:%d; a clock cannot run backwards, "+
						"and 0 already means \"use the platform default\" (%d seconds)",
					fd.Package, msg.Name, residency.IdleSeconds, DefaultResidencyIdleSeconds))
			}
			// VAL-2.
			if residency.DeletedMaxAgeSeconds < 0 {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: (spec.record).residency sets deleted_max_age_seconds:%d; a clock cannot run "+
						"backwards, and 0 already means \"use the platform default\" (%d seconds)",
					fd.Package, msg.Name, residency.DeletedMaxAgeSeconds,
					DefaultResidencyDeletedMaxAgeSeconds))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("residency annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
